package solana

import (
	"fmt"
	"math/big"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

////////////////////////////////////////////////////////////////////////////////////////
// sol_keepalive.go — vault keep-alive self-sign
//
// When trading is halted (or the chain is simply quiet), organic outbounds stop and
// the newest signature for a vault address ages out of a non-archival RPC's history
// retention. The scanner's getSignaturesForAddress until-cursor then anchors to a
// signature the RPC can no longer resolve (-32020), wedging observation for the
// vault. Instead of forcing every node onto archival RPCs, the keep-alive lands one
// minimal on-chain transaction per stale vault so the cursor re-anchors at a recent
// slot.
//
// The keep-alive is a TSS-signed vault→self transfer with the `consolidate` memo —
// the same "network sends to itself" primitive the UTXO clients use for UTXO
// consolidation. Thornode's ConsolidateHandler accepts any asgard self-send without
// requiring a scheduled TxOutItem, and the generic observation accounting nets the
// vault record to minus the transaction fee. No thornode change is involved.
//
// Node coordination mirrors the UTXO consolidate: every vault member evaluates the
// same trigger against the same chain facts (current slot, newest vault signature)
// and constructs a byte-identical transaction (the amount is derived from the
// newest signature's slot), so TSS keysign parties converge without any explicit
// coordination. Nodes that are not members of a vault skip it.
////////////////////////////////////////////////////////////////////////////////////////

const (
	// defaultSignatureRefreshBlocks is ~1 day of THORChain blocks at 6s.
	defaultSignatureRefreshBlocks = 14400

	// solSlotsPerThorchainBlock converts the THORChain-block threshold to Solana
	// slots (6s thorchain block / ~400ms solana slot).
	solSlotsPerThorchainBlock = 15

	// keepAlivePollInterval is how often the loop polls the scanner's finalized
	// scan slot for a cadence-boundary crossing. It is only a detection cadence,
	// not the trigger: the trigger is the shared boundary below, so this is kept
	// well under the ~20s blockhash quantization so every member detects the same
	// boundary within one blockhash tick and signs identical bytes.
	keepAlivePollInterval = 10 * time.Second

	// keepAliveSlotCadence is the spacing, in Solana slots, of the keep-alive
	// evaluation boundaries (~5 min at 400ms/slot). The keep-alive fires only when
	// the shared finalized scan slot crosses one of these boundaries, so all
	// members trigger together and — like a scheduled outbound inheriting its
	// consensus txOut.Height — derive the identical anchor slot to hand the keysign.
	keepAliveSlotCadence = 750

	// keepAliveFeeMarginLamports is the balance headroom required beyond the
	// self-send amount so the transaction fee is always payable.
	keepAliveFeeMarginLamports = 100_000
)

// keepAliveLoop watches the scanner's finalized scan slot and fires the keep-alive
// once per shared cadence boundary. Driving the trigger off the shared scan slot
// rather than a per-node wall clock is what makes the keysign converge: every
// member crosses the same boundary and hands SignTx the identical anchor slot as
// its height, exactly as scheduled outbounds all sign at their consensus
// txOut.Height. A per-node ticker (the previous design) let members sign at
// different heights/blockhash ticks, so the keysign party could never reach
// threshold on identical bytes.
func (c *SOLClient) keepAliveLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(keepAlivePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			scanSlot, err := c.solScanner.ScanHeight()
			if err != nil || scanSlot <= 0 {
				continue
			}
			boundary := c.nextKeepAliveSlotMultiple(scanSlot)
			if boundary <= c.getKeepAliveSlotMultiple() {
				continue // no new boundary crossed
			}
			c.keepAliveCheck(scanSlot)
			// Advance past this boundary so it is acted on exactly once, whether or
			// not a keysign actually fired (disabled/inactive/not-yet-stale all
			// legitimately no-op until the next boundary).
			c.setKeepAliveSlotMultiple(boundary)
		}
	}
}

// ShouldKeepAlive reports whether the shared finalized scan slot has crossed a new
// keep-alive cadence boundary since this node last acted. Mirrors
// ShouldReportSolvency: because every member drives it off the same scanned slot,
// they cross boundaries together.
func (c *SOLClient) ShouldKeepAlive(slot int64) bool {
	return c.nextKeepAliveSlotMultiple(slot) > c.getKeepAliveSlotMultiple()
}

// nextKeepAliveSlotMultiple returns the boundary this node should act on for the
// given scan slot. Mirrors nextSolvencyReportSlotMultiple: a fresh node jumps
// straight to the current boundary instead of replaying history, and otherwise
// advances one boundary at a time so members with slightly different tips still
// converge on the same next boundary.
func (c *SOLClient) nextKeepAliveSlotMultiple(slot int64) int64 {
	current := c.getKeepAliveSlotMultiple()
	if keepAliveSlotCadence <= 0 {
		return current
	}
	crossedMultiple := slot / keepAliveSlotCadence
	if slot%keepAliveSlotCadence == 0 {
		crossedMultiple--
	}
	if crossedMultiple <= current {
		return current
	}
	if current == 0 && crossedMultiple > 1 {
		return crossedMultiple
	}
	return current + 1
}

func (c *SOLClient) getKeepAliveSlotMultiple() int64 {
	c.keepAliveSlotMultipleMu.Lock()
	defer c.keepAliveSlotMultipleMu.Unlock()
	return c.keepAliveSlotMultiple
}

func (c *SOLClient) setKeepAliveSlotMultiple(v int64) {
	c.keepAliveSlotMultipleMu.Lock()
	defer c.keepAliveSlotMultipleMu.Unlock()
	c.keepAliveSlotMultiple = v
}

// keepAliveCheck evaluates every vault this node is a member of at a shared scan
// slot. anchorSlot is the finalized scan slot the boundary was crossed at; it is
// used both as the staleness reference and, critically, as the height handed to
// SignTx so every member's keysign coordinates on the same value.
func (c *SOLClient) keepAliveCheck(anchorSlot int64) {
	refreshBlocks, err := c.bridge.GetMimir(constants.MimirKeySignatureRefreshBlocks)
	if err != nil {
		c.logger.Err(err).Msgf("fail to get %s mimir", constants.MimirKeySignatureRefreshBlocks)
		return
	}
	if refreshBlocks < 0 {
		refreshBlocks = defaultSignatureRefreshBlocks
	}
	if refreshBlocks == 0 {
		return // keep-alive disabled
	}

	nodeStatus, err := c.bridge.FetchNodeStatus()
	if err != nil {
		c.logger.Err(err).Msg("fail to get node status")
		return
	}
	if nodeStatus != ttypes.NodeStatus_Active {
		return
	}
	if signing.IsSigningHalted(c.bridge, c.cfg.ChainID, c.logger) {
		return
	}
	// CreateTx needs a fresh recent blockhash from the scanner; an unhealthy
	// scanner would produce an expired blockhash and a guaranteed-dead broadcast.
	if !c.IsBlockScannerHealthy() {
		return
	}

	nodePubKey := c.pubkeyMgr.GetNodePubKey(common.SigningAlgoSecp256k1)
	if nodePubKey.IsEmpty() {
		return
	}

	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		c.logger.Err(err).Msg("fail to get current asgards")
		return
	}

	// anchorSlot (the shared finalized scan slot) is the staleness reference and
	// the keysign height. Deliberately NOT c.rpcClient.GetSlot()/GetBlockHeight():
	// those are per-node and would desync the keysign message and party.
	for _, vault := range vaults {
		if !vault.Contains(nodePubKey) {
			// not a member of this vault, its members handle the keep-alive
			continue
		}
		if vault.PubKeyEddsa.IsEmpty() {
			continue
		}
		if err := c.keepAliveVault(vault, uint64(anchorSlot), refreshBlocks, anchorSlot); err != nil {
			c.logger.Err(err).
				Str("vault_pubkey", vault.PubKey.String()).
				Msg("fail to keep alive vault")
		}
	}
}

func (c *SOLClient) keepAliveVault(vault ttypes.Vault, currentSlot uint64, refreshBlocks, anchorSlot int64) error {
	addr, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil {
		return fmt.Errorf("fail to get vault address: %w", err)
	}
	vaultAddr := addr.String()

	// The newest signature for the address, regardless of whether it was an
	// observable transfer — any transaction re-anchors the scanner cursor.
	sigs, err := c.rpcClient.GetSignaturesForAddressUntil(vaultAddr, rpc.GetSignaturesForAddressUntilParams{
		Commitment: "finalized",
		Limit:      1,
	})
	if err != nil {
		return fmt.Errorf("fail to get newest signature: %w", err)
	}
	if len(sigs) == 0 {
		// A vault with no history has no cursor to keep fresh.
		return nil
	}
	newest := sigs[0]
	if !keepAliveDue(currentSlot, newest.Slot, refreshBlocks) {
		return nil
	}

	amount := keepAliveAmount(newest.Slot)

	// Balance guard: the transfer momentarily debits the full amount before
	// crediting it back, so the account must hold amount plus fee headroom.
	balance, err := c.rpcClient.GetBalance(vaultAddr, "finalized", 0)
	if err != nil {
		return fmt.Errorf("fail to get vault balance: %w", err)
	}
	requiredLamports := new(big.Int).Add(
		convertToLamports(amount),
		big.NewInt(keepAliveFeeMarginLamports),
	)
	if balance.Cmp(requiredLamports) < 0 {
		c.logger.Info().
			Str("vault", vaultAddr).
			Str("balance", balance.String()).
			Str("required", requiredLamports.String()).
			Msg("vault balance too low for keep-alive, skipping")
		return nil
	}

	toi := stypes.TxOutItem{
		Chain:            common.SOLChain,
		ToAddress:        addr,
		VaultPubKey:      vault.PubKey,
		VaultPubKeyEddsa: vault.PubKeyEddsa,
		Coins: common.Coins{
			common.NewCoin(common.SOLAsset, amount),
		},
		Memo:    mem.NewConsolidateMemo().String(),
		MaxGas:  common.Gas{common.SOLChain.NativeGasToThorCoin(cosmos.NewUint(baseFeePerSigLamports))},
		GasRate: baseFeePerSigLamports,
	}

	c.logger.Info().
		Str("vault", vaultAddr).
		Uint64("newest_sig_slot", newest.Slot).
		Uint64("current_slot", currentSlot).
		Int64("anchor_slot", anchorSlot).
		Str("amount", amount.String()).
		Msg("vault signature is stale, sending keep-alive self-sign")

	// anchorSlot is the shared keysign height: every member of the party derives
	// the same value from the same finalized scan boundary, so they coordinate on
	// one keysign session — the property scheduled outbounds get from txOut.Height.
	signed, _, _, err := c.SignTx(toi, anchorSlot)
	if err != nil {
		return fmt.Errorf("fail to sign keep-alive tx: %w", err)
	}
	if len(signed) == 0 {
		// not selected for keysign, or the signer cache already covers this round
		return nil
	}

	txid, err := c.BroadcastTx(toi, signed)
	if err != nil {
		return fmt.Errorf("fail to broadcast keep-alive tx: %w", err)
	}
	c.logger.Info().
		Str("vault", vaultAddr).
		Str("txid", txid).
		Msg("broadcast keep-alive self-sign")
	return nil
}

// keepAliveDue returns true when the newest signature for a vault is older than
// the refresh threshold. Both inputs are chain facts, so every node evaluates the
// same result.
func keepAliveDue(currentSlot, newestSigSlot uint64, refreshBlocks int64) bool {
	if refreshBlocks <= 0 || currentSlot <= newestSigSlot {
		return false
	}
	thresholdSlots := uint64(refreshBlocks) * solSlotsPerThorchainBlock
	return currentSlot-newestSigSlot > thresholdSlots
}

// keepAliveAmount derives the self-send amount (1e8 base units) from the slot of
// the newest signature being refreshed. All nodes derive an identical amount (so
// TSS parties sign identical bytes), each refresh round gets a distinct amount
// (so its CacheHash never collides with a previous round in the signer cache),
// and the result always clears the scanner dust threshold (so both sides of the
// self-send are observed and the vault record nets exactly minus the fee).
func keepAliveAmount(anchorSlot uint64) cosmos.Uint {
	dust := common.SOLChain.DustThreshold()
	jitter := anchorSlot % dust.Uint64()
	return dust.MulUint64(2).AddUint64(jitter)
}
