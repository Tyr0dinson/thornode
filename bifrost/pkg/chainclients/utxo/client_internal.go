package utxo

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	bchtxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/bchd-txscript"
	dogetxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/dogd-txscript"
	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc/zec"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

////////////////////////////////////////////////////////////////////////////////////////
// Address Checks
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) getAsgardAddress() ([]common.Address, error) {
	return utxo.GetAsgardAddressCached(&c.asgardCache, c.cfg.ChainID, c.bridge, constants.ThorchainBlockTime)
}

func (c *Client) isAsgardAddress(addressToCheck string) bool {
	asgards, err := c.getAsgardAddress()
	if err != nil {
		c.log.Err(err).Msg("fail to get asgard addresses")
		if len(asgards) == 0 {
			return false
		}
	}
	for _, addr := range asgards {
		if strings.EqualFold(addr.String(), addressToCheck) {
			return true
		}
	}
	return false
}

////////////////////////////////////////////////////////////////////////////////////////
// Reorg Handling
////////////////////////////////////////////////////////////////////////////////////////

// utxoReorgFix is one height's share of a reorg: the errata to report and the
// repaired block meta to persist.
type utxoReorgFix struct {
	meta   *utxo.BlockMeta
	errata []types.ErrataTx
	block  *btcjson.GetBlockVerboseTxResult // the new chain's block at meta.Height
	// planned holds the transactions the stored meta had when the plan read it, so
	// the commit can tell them from ones the observer recorded since
	planned map[string]bool
}

// utxoReorgPlan is the work a detected reorg requires. It is computed without side
// effects and committed only once the whole scan step has succeeded.
type utxoReorgPlan struct {
	fixes []utxoReorgFix
	txIns []types.TxIn
}

// processReorg detects and resolves a reorg at block in one go: plan, rescan, commit.
// FetchTxs uses the pieces directly so the current block is extracted before the
// commit.
func (c *Client) processReorg(block *btcjson.GetBlockVerboseTxResult) ([]types.TxIn, error) {
	plan, err := c.planReorg(block)
	if err != nil {
		return nil, err
	}
	c.commitReorg(plan)
	if plan == nil {
		return nil, nil
	}
	return plan.txIns, nil
}

// planReorg compares the block's previous hash with the stored hash of the previous
// height. On a reorg it re-confirms the observed transactions of every stored block
// back to the common ancestor and rescans those blocks. It reports and persists
// nothing (commitReorg does), and any lookup that cannot be answered aborts it, so
// the scan step fails and is retried with the stored metas untouched: the next
// attempt detects the same reorg and redoes all of it.
func (c *Client) planReorg(block *btcjson.GetBlockVerboseTxResult) (*utxoReorgPlan, error) {
	previousHeight := block.Height - 1
	prevBlockMeta, err := c.temporalStorage.GetBlockMeta(previousHeight)
	if err != nil {
		return nil, fmt.Errorf("fail to get block meta of height(%d): %w", previousHeight, err)
	}
	if prevBlockMeta == nil {
		return nil, nil
	}
	// the block's previous hash need to be the same as the block hash chain client recorded in block meta
	// blockMetas[PreviousHeight].BlockHash == Block.PreviousHash
	if strings.EqualFold(prevBlockMeta.BlockHash, block.PreviousHash) {
		return nil, nil
	}

	c.log.Info().
		Int64("currentHeight", block.Height).
		Str("previousHash", block.PreviousHash).
		Int64("blockMetaHeight", prevBlockMeta.Height).
		Str("blockMetaHash", prevBlockMeta.BlockHash).
		Msg("re-org detected")

	fixes, err := c.planReConfirm(block)
	if err != nil {
		return nil, fmt.Errorf("re-org at height %d cannot be resolved yet: %w", block.Height, err)
	}
	plan := &utxoReorgPlan{fixes: fixes}
	for _, fix := range fixes {
		c.log.Info().Int64("height", fix.meta.Height).Msg("rescanning block")
		txIn, xerr := c.extractTxs(fix.block)
		if xerr != nil {
			// extractTxs already marked the earlier rescans' txs as observed: undo
			// that, or the retry of this step would skip them as reported before
			c.untrackObserved(plan.txIns)
			return nil, fmt.Errorf("rescan block %d: %w", fix.meta.Height, xerr)
		}
		if len(txIn.TxArray) > 0 {
			plan.txIns = append(plan.txIns, txIn)
		}
	}
	return plan, nil
}

// untrackObserved undoes the "observed" marks extractTxs set for txIns, when the scan
// step that produced them is abandoned: the retry must emit them again.
func (c *Client) untrackObserved(txIns []types.TxIn) {
	for _, txIn := range txIns {
		for _, item := range txIn.TxArray {
			if err := c.temporalStorage.UntrackObservedTx(item.Tx); err != nil {
				c.log.Err(err).Str("txid", item.Tx).Msg("fail to untrack observed tx of an abandoned scan step")
			}
		}
	}
}

// planReConfirm walks back from the scanned block's parent until a stored block is
// on the scanned block's chain, re-confirming every observed customer transaction of
// the stored blocks that are not. The walk follows hashes, not heights: each block is
// fetched by the hash its child names, so an endpoint that is behind, still on the
// old fork, or a load-balanced provider answering from different nodes cannot answer
// for another chain (one that does not have the block errors, and the next endpoint
// is asked). A height with no stored block (a gap a skip-ahead left) is walked
// through by hash while stored blocks remain below it; the walk stops where no
// stored block is left in the window. A transaction
// becomes an errata only on the primary's positive "not found", once the primary is
// proven to be on the scanned block's chain; one that the new chain's blocks hold is
// still on chain. It reports and persists nothing; any read that cannot be answered
// aborts the whole plan.
func (c *Client) planReConfirm(block *btcjson.GetBlockVerboseTxResult) ([]utxoReorgFix, error) {
	// calculate the earliest look back height
	earliestHeight := block.Height - c.cfg.BlockScanner.MaxReorgRescanBlocks
	if earliestHeight < 1 {
		earliestHeight = 1
	}

	// transactions the new chain holds need no lookup: an inbound reorged out and
	// mined again in one of these blocks is on chain even if the primary is exactly
	// at the scanned block's parent and never saw it in its mempool
	onNewChain := blockTxids(block)
	primaryProven := false
	var fixes []utxoReorgFix
	expected := block.PreviousHash
	// the current block is not yet in block meta, start from previous block
	for i := block.Height - 1; i >= earliestHeight; i-- {
		blockMeta, err := c.temporalStorage.GetBlockMeta(i)
		if err != nil {
			return nil, fmt.Errorf("fail to get block meta %d from local storage: %w", i, err)
		}
		if blockMeta == nil {
			below, berr := c.storedMetaBelow(i, earliestHeight)
			if berr != nil {
				return nil, berr
			}
			if !below {
				break // nothing stored from here down, nothing to re-confirm
			}
			// a gap (a skip-ahead left it): nothing recorded here, but stored blocks
			// below may be on the old fork, so follow the chain through it by hash
			b, gerr := c.getBlockByHash(i, expected)
			if gerr != nil {
				return nil, fmt.Errorf("fail to get block %s at height %d: %w", expected, i, gerr)
			}
			if !strings.EqualFold(b.Hash, expected) || b.Height != i {
				return nil, fmt.Errorf("asked for block %s at height %d, got block %s at height %d", expected, i, b.Hash, b.Height)
			}
			for txid := range blockTxids(b) {
				onNewChain[txid] = true
			}
			expected = b.PreviousHash
			continue
		}
		if strings.EqualFold(blockMeta.BlockHash, expected) {
			break // we know about this block, everything prior is okay
		}

		b, err := c.getBlockByHash(i, expected)
		if err != nil {
			return nil, fmt.Errorf("fail to get block %s at height %d: %w", expected, i, err)
		}
		if !strings.EqualFold(b.Hash, expected) || b.Height != i {
			return nil, fmt.Errorf("asked for block %s at height %d, got block %s at height %d", expected, i, b.Hash, b.Height)
		}
		for txid := range blockTxids(b) {
			onNewChain[txid] = true
		}

		c.log.Info().Int64("height", blockMeta.Height).Msg("re-confirming transactions")
		fix := utxoReorgFix{meta: blockMeta, block: b, planned: map[string]bool{}}
		for _, tx := range blockMeta.CustomerTransactions {
			fix.planned[tx] = true
		}
		for _, tx := range blockMeta.SelfTransactions {
			fix.planned[tx] = true
		}
		var gone []string
		for _, tx := range blockMeta.CustomerTransactions {
			if onNewChain[tx] {
				c.log.Info().Int64("height", blockMeta.Height).Str("txid", tx).Msg("transaction is on the new chain")
				continue
			}
			if !primaryProven {
				if err = c.requirePrimaryOnChainOf(block); err != nil {
					return nil, err
				}
				primaryProven = true
			}
			switch c.confirmTx(tx) {
			case txStillOnChain:
				c.log.Info().Int64("height", blockMeta.Height).Str("txid", tx).Msg("transaction still exists")
			case txGone:
				c.log.Info().Int64("height", blockMeta.Height).Str("txid", tx).Msg("errata tx")
				fix.errata = append(fix.errata, types.ErrataTx{
					TxID:  common.TxID(tx),
					Chain: c.cfg.ChainID,
				})
				gone = append(gone, tx)
			default:
				return nil, fmt.Errorf("height %d: cannot re-confirm tx %s, the primary did not answer", blockMeta.Height, tx)
			}
		}

		for _, tx := range gone {
			blockMeta.RemoveCustomerTransaction(tx)
		}
		// the stored record now describes the new chain's block at this height
		blockMeta.BlockHash = b.Hash
		blockMeta.PreviousHash = b.PreviousHash
		fixes = append(fixes, fix)
		expected = b.PreviousHash
	}
	return fixes, nil
}

// storedMetaBelow reports whether a block meta is stored at any height below height,
// down to earliest (local storage only).
func (c *Client) storedMetaBelow(height, earliest int64) (bool, error) {
	for j := height - 1; j >= earliest; j-- {
		meta, err := c.temporalStorage.GetBlockMeta(j)
		if err != nil {
			return false, fmt.Errorf("fail to get block meta %d from local storage: %w", j, err)
		}
		if meta != nil {
			return true, nil
		}
	}
	return false, nil
}

// requirePrimaryOnChainOf checks that the primary daemon (the one confirmTx asks) is
// on the scanned block's chain: its block at height-1 is the scanned block's parent.
// A primary that is behind, or still on the old fork, would call a transaction of the
// new chain absent; the reorg step is then retried.
func (c *Client) requirePrimaryOnChainOf(block *btcjson.GetBlockVerboseTxResult) error {
	if c.rpc.PrimaryOutWithFallback() {
		return fmt.Errorf("the primary is out of rotation, so it cannot re-confirm transactions yet")
	}
	hash, err := c.rpc.GetBlockHashPrimary(block.Height - 1)
	if err != nil {
		return fmt.Errorf("cannot read block %d from the primary: %w", block.Height-1, err)
	}
	if !strings.EqualFold(hash, block.PreviousHash) {
		return fmt.Errorf("the primary has block %d as %s, not the scanned block's parent %s: it is behind or on another fork, so its \"not found\" is no proof yet",
			block.Height-1, hash, block.PreviousHash)
	}
	return nil
}

// blockTxids lists the txids of a block.
func blockTxids(block *btcjson.GetBlockVerboseTxResult) map[string]bool {
	out := make(map[string]bool, len(block.Tx))
	for _, tx := range block.Tx {
		out[tx.Txid] = true
	}
	return out
}

// commitReorg reports the errata and persists the repaired block metas of a plan.
// It runs only after every fallible step of the scan step has succeeded.
func (c *Client) commitReorg(plan *utxoReorgPlan) {
	if plan == nil {
		return
	}
	for _, fix := range plan.fixes {
		if len(fix.errata) > 0 {
			c.globalErrataQueue <- types.ErrataBlock{
				Height: fix.meta.Height,
				Txs:    fix.errata,
			}
		}
	}
	// errata are queued before the lock is taken: the observer may wait on metaMu,
	// and must never be needed to drain a blocked errata send
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	for _, fix := range plan.fixes {
		// keep a transaction the observer recorded at this height after the plan read
		// it (an errata'd one was there when the plan read it, so it is not re-added)
		if current, err := c.temporalStorage.GetBlockMeta(fix.meta.Height); err == nil && current != nil {
			for _, tx := range current.CustomerTransactions {
				if !fix.planned[tx] {
					fix.meta.AddCustomerTransaction(tx)
				}
			}
			for _, tx := range current.SelfTransactions {
				if !fix.planned[tx] {
					fix.meta.AddSelfTransaction(tx)
				}
			}
		}
		if err := c.temporalStorage.SaveBlockMeta(fix.meta.Height, fix.meta); err != nil {
			c.log.Err(err).Int64("height", fix.meta.Height).Msg("fail to save block meta of height")
		}
	}
}

// txConfirmation is the three-valued answer of confirmTx.
type txConfirmation int

const (
	txStillOnChain txConfirmation = iota
	txGone
	txUnknown
)

// confirmTx reports whether an already observed transaction still exists after a
// reorg. txGone becomes an errata sent to THORChain, so absence needs positive
// proof: only the primary daemon is asked (it runs with txindex, and a fallback
// that never saw the transaction would call it absent), and only its "not found"
// answer, or a transaction it holds only in a disconnected block, counts. txindex
// keeps the transactions of disconnected blocks and the daemon looks in its mempool
// first, so a block hash with no confirmations means the transaction is in no
// active block and not in the mempool. A transport failure, or a disabled primary,
// is txUnknown: the reorg plan is abandoned with nothing written, so the scan of the
// block fails and is retried with the mismatch still detected.
func (c *Client) confirmTx(txid string) txConfirmation {
	tx, err := c.rpc.GetRawTransactionVerbosePrimary(txid)
	switch {
	case err == nil && tx != nil && tx.BlockHash != "" && tx.Confirmations <= 0:
		c.log.Warn().Str("txid", txid).Str("block", tx.BlockHash).Msg("transaction only in a disconnected block after reorg")
		return txGone
	case err == nil:
		return txStillOnChain
	case isUTXOTxNotFoundErr(err):
		c.log.Warn().Err(err).Str("txid", txid).Msg("transaction no longer on chain after reorg")
		return txGone
	default:
		c.log.Err(err).Str("txid", txid).Msg("fail to re-confirm tx after reorg; the primary could not answer")
		return txUnknown
	}
}

// isMethodNotFoundErr reports a JSON-RPC -32601 from either client type.
func isMethodNotFoundErr(err error) bool {
	var rpcErr *btcjson.RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == btcjson.RPCErrorCode(-32601)
	}
	var zecErr *zec.Error
	if errors.As(err, &zecErr) {
		return zecErr.Code == -32601
	}
	return false
}

////////////////////////////////////////////////////////////////////////////////////////
// Mempool Cache
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) removeFromMemPoolCache(hash string) {
	if err := c.temporalStorage.UntrackMempoolTx(hash); err != nil {
		c.log.Err(err).Str("txid", hash).Msg("fail to remove from mempool cache")
	}
}

func (c *Client) tryAddToMemPoolCache(hash string) bool {
	added, err := c.temporalStorage.TrackMempoolTx(hash)
	if err != nil {
		c.log.Err(err).Str("txid", hash).Msg("fail to add to mempool cache")
	}
	return added
}

func (c *Client) canDeleteBlock(blockMeta *utxo.BlockMeta) bool {
	if blockMeta == nil {
		return true
	}
	// our own broadcasts live in the primary's mempool; a fallback that never saw
	// them would report them absent and let the block meta go too early. Zebra does
	// not implement getmempoolentry, so a method-not-found answer falls back to the
	// primary's raw mempool listing.
	if len(blockMeta.SelfTransactions) > 0 && !c.rpc.PrimaryInRotation() {
		// the health checks took the primary out: keep the meta for now rather than
		// run its full retry loop for every pruned block while a fallback serves reads
		return false
	}
	var rawMempool map[string]struct{}
	for _, tx := range blockMeta.SelfTransactions {
		result, err := c.rpc.GetMempoolEntryPrimary(tx)
		switch {
		case err == nil && result != nil:
			c.log.Info().Str("txid", tx).Msg("still in mempool, block cannot be deleted")
			return false
		case err == nil, isUTXOTxNotFoundErr(err):
			continue
		case isMethodNotFoundErr(err):
			if rawMempool == nil {
				hashes, lerr := c.rpc.GetRawMempoolPrimary()
				if lerr != nil {
					c.log.Warn().Err(lerr).Str("txid", tx).Msg("cannot list the primary's mempool; keeping block meta for now")
					return false
				}
				rawMempool = make(map[string]struct{}, len(hashes))
				for _, h := range hashes {
					rawMempool[strings.ToLower(h)] = struct{}{}
				}
			}
			if _, inMempool := rawMempool[strings.ToLower(tx)]; inMempool {
				c.log.Info().Str("txid", tx).Msg("still in mempool, block cannot be deleted")
				return false
			}
		default:
			c.log.Warn().Err(err).Str("txid", tx).Msg("cannot check mempool on the primary; keeping block meta for now")
			return false
		}
	}
	return true
}

// btcFixedMinRelayFeeSats is the daemon-independent BTC min-relay-fee floor (sats).
// It MUST be identical on every node: it is signing-consensus-relevant (see
// updateNetworkInfo). 1000 sat == Bitcoin Core's historical 0.00001 BTC/kvB default
// (<=29.0), which the network majority runs; pinning here keeps all nodes aligned
// regardless of Core version. Do not make this per-node configurable.
const btcFixedMinRelayFeeSats uint64 = 1000

func (c *Client) updateNetworkInfo() {
	// For BTC the min-relay-fee floor is signing-consensus-relevant: minRelayFeeSats
	// feeds both the reported network fee (sendNetworkFee) and the outbound fee floor
	// (applyFeeLimits), which shape the change output, its sighash and the TSS message
	// id. Bitcoin Core <=29.0 reports relayfee 0.00001 BTC/kvB (1000 sat) while 29.1+
	// and 30.x default to 0.000001 (100 sat), so a daemon-derived floor makes nodes on
	// different Core lines build different change outputs for the same outbound — the
	// minority then signs a different message, is absent from the majority's party, and
	// is blamed/jailed with no error in its own log. Pin BTC to a fixed floor
	// independent of the daemon — and independent of whether the GetNetworkInfo RPC
	// succeeds, so an RPC failure can never leave the BTC floor at 0 — so every node
	// computes the same fee regardless of Core version. Other UTXO chains keep their
	// daemon-derived relay fee.
	if c.cfg.ChainID.Equals(common.BTCChain) {
		c.minRelayFeeSats.Store(btcFixedMinRelayFeeSats)
		return
	}
	// the relay fee is read from the primary only: a fallback provider may run another
	// daemon version with another default, and the fee floor must be this node's own
	// daemon's (when the primary cannot answer, or is out of rotation, the last value
	// is kept)
	if c.rpc.PrimaryOutWithFallback() {
		return
	}
	networkInfo, err := c.rpc.GetNetworkInfoPrimary()
	if err != nil {
		c.log.Err(err).Msg("fail to get network info")
		return
	}
	amt, err := btcutil.NewAmount(networkInfo.RelayFee)
	if err != nil {
		c.log.Err(err).Msg("fail to get minimum relay fee")
		return
	}
	c.minRelayFeeSats.Store(uint64(amt.ToUnit(btcutil.AmountSatoshi)))
}

func (c *Client) sendNetworkFee(height int64) error {
	// get block stats
	var feeRate uint64
	switch c.cfg.ChainID {
	case common.BCHChain:
		// BCH is a special case since the response uses floats
		hash, err := c.rpc.GetBlockHash(height)
		if err != nil {
			return fmt.Errorf("fail to get block hash: %w", err)
		}
		type BlockStats struct {
			AverageFeeRate float64 `json:"avgfeerate"`
		}
		var bs BlockStats
		err = c.rpc.CallWithFallback(&bs, "getblockstats", hash)
		if err != nil {
			return fmt.Errorf("fail to get block stats: %w", err)
		}
		feeRate = uint64(bs.AverageFeeRate * common.One)

	case common.LTCChain, common.BTCChain:
		hash, err := c.rpc.GetBlockHash(height)
		if err != nil {
			return fmt.Errorf("fail to get block hash: %w", err)
		}
		bs, err := c.rpc.GetBlockStats(hash)
		if err != nil {
			return fmt.Errorf("fail to get block stats: %w", err)
		}
		feeRate = uint64(bs.AverageFeeRate)

	default:
		c.log.Fatal().Msg("unsupported chain")
	}

	if feeRate == 0 {
		return nil
	}

	c.networkFeeLock.Lock()
	defer c.networkFeeLock.Unlock()

	minRelayFeeSats := c.minRelayFeeSats.Load()
	if c.cfg.UTXO.EstimatedAverageTxSize*feeRate < minRelayFeeSats {
		feeRate = minRelayFeeSats / c.cfg.UTXO.EstimatedAverageTxSize
		if feeRate*c.cfg.UTXO.EstimatedAverageTxSize < minRelayFeeSats {
			feeRate++
		}
	}

	if feeRate < uint64(c.cfg.UTXO.MinSatsPerVByte) {
		feeRate = uint64(c.cfg.UTXO.MinSatsPerVByte)
	}

	// if gas cache blocks are set, use the max gas over that window
	if c.cfg.BlockScanner.GasCacheBlocks > 0 {
		c.feeRateCache = append(c.feeRateCache, feeRate)
		if len(c.feeRateCache) > c.cfg.BlockScanner.GasCacheBlocks {
			c.feeRateCache = c.feeRateCache[len(c.feeRateCache)-c.cfg.BlockScanner.GasCacheBlocks:]
		}
		for _, rate := range c.feeRateCache {
			if rate > feeRate {
				feeRate = rate
			}
		}
	}

	c.m.GetGauge(metrics.GasPrice(c.cfg.ChainID)).Set(float64(feeRate))

	// skip update if fee has not changed
	if c.lastFeeRate.Load() == feeRate {
		return nil
	}

	c.lastFeeRate.Store(feeRate)
	c.m.GetCounter(metrics.GasPriceChange(c.cfg.ChainID)).Inc()

	c.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           c.cfg.ChainID,
		Height:          height,
		TransactionSize: c.cfg.UTXO.EstimatedAverageTxSize,
		TransactionRate: feeRate,
	}

	c.log.Debug().Msg("send network fee to THORNode successfully")
	return nil
}

// sendNetworkFeeFromBlock will send network fee to Thornode based on the block result,
// for chains like Dogecoin which do not support the getblockstats RPC.
func (c *Client) sendNetworkFeeFromBlock(blockResult *btcjson.GetBlockVerboseTxResult) error {
	height := blockResult.Height
	var total float64 // total coinbase value, block reward + all transaction fees in the block
	var totalVSize int32
	for _, tx := range blockResult.Tx {
		if len(tx.Vin) == 1 && tx.Vin[0].IsCoinBase() {
			for _, opt := range tx.Vout {
				total += opt.Value
			}
		} else {
			totalVSize += tx.Vsize
		}
	}

	transactionSize := c.cfg.UTXO.EstimatedAverageTxSize
	var feeRateSats uint64

	if c.GetChain() == common.ZECChain {
		// ZEC uses ZIP-317 static fee calculation, independent of block contents.
		// Report static 'worst case' scenario with the maximum possible
		// amounts of inputs and max memo size of 80 chars, which counts as
		// 5 actions and will likely always be lower that max inputs.
		tx := wire.MsgTx{TxIn: make([]*wire.TxIn, c.getMaximumUtxosToSpend()-1)}
		memo := strings.Repeat("X", 80)
		feeRateSats = c.getGasCoinZEC(&tx, memo).Amount.Uint64()
		transactionSize = 1
	} else {
		// skip updating network fee if there are no utxos (except coinbase) in the block
		if totalVSize == 0 {
			return nil
		}
		amt, err := btcutil.NewAmount(total - c.cfg.ChainID.DefaultCoinbase())
		if err != nil {
			return fmt.Errorf("fail to parse total block fee amount, err: %w", err)
		}

		// average fee rate in sats/vbyte or default min relay fee
		feeRateSats = uint64(amt.ToUnit(btcutil.AmountSatoshi) / float64(totalVSize))
		if c.cfg.UTXO.DefaultMinRelayFeeSats > feeRateSats {
			feeRateSats = c.cfg.UTXO.DefaultMinRelayFeeSats
		}
	}

	// round to prevent fee observation noise
	resolution := uint64(c.cfg.BlockScanner.GasPriceResolution)
	feeRateSats = ((feeRateSats / resolution) + 1) * resolution

	c.networkFeeLock.Lock()
	defer c.networkFeeLock.Unlock()

	// ZEC fees are static, but its agreed chain height must stay fresh for
	// deterministic transaction expiry. Report every quarter of the expiry
	// window even when fees are unchanged, using the scanned block height so
	// validators report the same heights regardless of local timing.
	// See ZecTxExpiry for the cadence, scanner-lag and quorum-latency budget.
	zecHeightReport := c.GetChain() == common.ZECChain && height%zecHeightReportInterval == 0

	// skip fee if less than 1 resolution away from the last
	lastFeeRate := c.lastFeeRate.Load()
	feeDelta := new(big.Int).Sub(big.NewInt(int64(feeRateSats)), big.NewInt(int64(lastFeeRate)))
	feeDelta.Abs(feeDelta)
	if !zecHeightReport && lastFeeRate != 0 && feeDelta.Cmp(big.NewInt(c.cfg.BlockScanner.GasPriceResolution)) != 1 {
		return nil
	}

	c.log.Info().
		Int64("height", height).
		Uint64("lastFeeRate", lastFeeRate).
		Uint64("feeRateSats", feeRateSats).
		Msg("sendNetworkFee")

	c.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           c.cfg.ChainID,
		Height:          height,
		TransactionSize: transactionSize,
		TransactionRate: feeRateSats,
	}

	c.lastFeeRate.Store(feeRateSats)

	return nil
}

func (c *Client) getBlock(height int64) (*btcjson.GetBlockVerboseTxResult, error) {
	hash, err := c.rpc.GetBlockHash(height)
	if err != nil {
		return &btcjson.GetBlockVerboseTxResult{}, err
	}
	return c.getBlockByHash(height, hash)
}

// getBlockByHash fetches a block by its hash: any endpoint that has the block returns
// the same one, so the read may be served by any of them.
func (c *Client) getBlockByHash(height int64, hash string) (*btcjson.GetBlockVerboseTxResult, error) {
	block, err := c.rpc.GetBlockVerboseTxs(hash)
	if err == nil && block.Hash == hash {
		// remember the coinbase value for the confirmation count of this block
		if value, cbErr := coinbaseValue(block); cbErr == nil {
			c.coinbases.put(height, hash, value)
		}
	}
	return block, err
}

func (c *Client) isValidUTXO(hexPubKey string) bool {
	buf, decErr := hex.DecodeString(hexPubKey)
	if decErr != nil {
		c.log.Err(decErr).Msgf("fail to decode hex string, %s", hexPubKey)
		return false
	}

	switch c.cfg.ChainID {
	case common.DOGEChain:
		scriptType, addresses, requireSigs, err := dogetxscript.ExtractPkScriptAddrs(buf, c.getChainCfgDOGE())
		if err != nil {
			c.log.Err(err).Msg("fail to extract pub key script")
			return false
		}
		switch scriptType {
		case dogetxscript.MultiSigTy:
			return false
		default:
			return len(addresses) == 1 && requireSigs == 1
		}
	case common.BCHChain:
		scriptType, addresses, requireSigs, err := bchtxscript.ExtractPkScriptAddrs(buf, c.getChainCfgBCH())
		if err != nil {
			c.log.Err(err).Msg("fail to extract pub key script")
			return false
		}
		switch scriptType {
		case bchtxscript.MultiSigTy:
			return false

		default:
			return len(addresses) == 1 && requireSigs == 1
		}

	case common.LTCChain:
		scriptType, addresses, requireSigs, err := ltctxscript.ExtractPkScriptAddrs(buf, c.getChainCfgLTC())
		if err != nil {
			c.log.Err(err).Msg("fail to extract pub key script")
			return false
		}
		switch scriptType {
		case ltctxscript.MultiSigTy:
			return false
		default:
			return len(addresses) == 1 && requireSigs == 1
		}

	case common.BTCChain, common.ZECChain:
		// there is no specific txscript package for zcash and vaults are
		// using p2pkh only, which is compatible to btc
		scriptType, addresses, requireSigs, err := btctxscript.ExtractPkScriptAddrs(buf, c.getChainCfgBTC())
		if err != nil {
			c.log.Err(err).Msg("fail to extract pub key script")
			return false
		}
		switch scriptType {
		case btctxscript.MultiSigTy:
			return false
		default:
			return len(addresses) == 1 && requireSigs == 1
		}

	default:
		c.log.Fatal().Msg("unsupported chain")
		return false
	}
}

func (c *Client) isRBFEnabled(tx *btcjson.TxRawResult) bool {
	for _, vin := range tx.Vin {
		if vin.Sequence < (0xffffffff - 1) {
			return true
		}
	}
	return false
}

func (c *Client) getTxIn(tx *btcjson.TxRawResult, height int64, isMemPool bool, vinZeroTxs map[string]*btcjson.TxRawResult) (types.TxInItem, error) {
	if c.ignoreTx(tx, height) {
		c.log.Debug().Int64("height", height).Str("txid", tx.Hash).Msg("ignore tx not matching format")
		return types.TxInItem{}, nil
	}
	// RBF enabled transaction will not be observed until committed to block
	if c.isRBFEnabled(tx) && isMemPool {
		return types.TxInItem{}, nil
	}
	sender, err := c.getSender(tx, vinZeroTxs)
	if err != nil {
		return types.TxInItem{}, fmt.Errorf("fail to get sender from tx: %w", err)
	}
	memo, err := c.getMemo(tx)
	if err != nil {
		return types.TxInItem{}, fmt.Errorf("fail to get memo from tx: %w", err)
	}
	if len([]byte(memo)) > constants.MaxMemoSize {
		return types.TxInItem{}, fmt.Errorf("memo (%s) longer than max allow length (%d)", memo, constants.MaxMemoSize)
	}
	m, err := mem.ParseMemo(common.LatestVersion, memo)
	if err != nil {
		c.log.Debug().Err(err).Str("memo", memo).Msg("fail to parse memo")
	}
	output, err := c.getOutput(sender, tx, m.IsType(mem.TxConsolidate))
	if err != nil {
		if errors.Is(err, btypes.ErrFailOutputMatchCriteria) {
			c.log.Debug().Int64("height", height).Str("txid", tx.Hash).Msg("ignore tx not matching format")
			return types.TxInItem{}, nil
		}
		return types.TxInItem{}, fmt.Errorf("fail to get output from tx: %w", err)
	}

	addresses := c.getAddressesFromScriptPubKey(output.ScriptPubKey)
	toAddr := addresses[0]

	// strip BCH address prefixes
	if c.cfg.ChainID.Equals(common.BCHChain) {
		toAddr = c.stripBCHAddress(toAddr)
	}

	isInbound := c.isAsgardAddress(toAddr)
	if isInbound {
		// only inbound UTXO need to be validated against multi-sig
		if !c.isValidUTXO(output.ScriptPubKey.Hex) {
			return types.TxInItem{}, fmt.Errorf("invalid utxo")
		}
	}
	amount, err := btcutil.NewAmount(output.Value)
	if err != nil {
		return types.TxInItem{}, fmt.Errorf("fail to parse float64: %w", err)
	}
	amt := uint64(amount.ToUnit(btcutil.AmountSatoshi))

	gas, err := c.getGas(tx, isInbound)
	if err != nil {
		return types.TxInItem{}, fmt.Errorf("fail to get gas from tx: %w", err)
	}
	return types.TxInItem{
		BlockHeight: height,
		Tx:          tx.Txid,
		Sender:      sender,
		To:          toAddr,
		Coins: common.Coins{
			common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(amt)),
		},
		Memo: memo,
		Gas:  gas,
	}, nil
}

// stripBCHAddress removes prefix on bch addresses.
func (c *Client) stripBCHAddress(addr string) string {
	split := strings.Split(addr, ":")
	if len(split) > 1 {
		return split[1]
	}
	return split[0]
}

func (c *Client) getVinZeroTxs(block *btcjson.GetBlockVerboseTxResult) (map[string]*btcjson.TxRawResult, error) {
	vinZeroTxs := make(map[string]*btcjson.TxRawResult)
	start := time.Now()

	dustThreshold := c.cfg.ChainID.DustThreshold().Uint64()

	// create our batches
	batches := [][]string{}
	batch := []string{}
	var count, ignoreCount, failMemoSkipCount, skipDustCount int // just for debug logs
	for i := range block.Tx {
		if c.ignoreTx(&block.Tx[i], block.Height) {
			ignoreCount++
			continue
		}

		// skip if sum of vout value is under thorchain dust threshold
		voutSats, err := sumVoutSats(&block.Tx[i])
		if err != nil {
			c.log.Error().Err(err).Str("txid", block.Tx[i].Txid).Msg("fail to sum vout sats")
		} else if voutSats < dustThreshold {
			skipDustCount++
			continue
		}

		memo, err := c.getMemo(&block.Tx[i])
		if err != nil || len(memo) > constants.MaxMemoSize {
			failMemoSkipCount++
			continue
		}

		count++
		batch = append(batch, block.Tx[i].Vin[0].Txid)
		if len(batch) >= c.cfg.UTXO.TransactionBatchSize {
			batches = append(batches, batch)
			batch = []string{}
		}
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}

	c.log.Debug().
		Int64("height", block.Height).
		Int("ignoreCount", ignoreCount).
		Int("failMemoSkipCount", failMemoSkipCount).
		Int("skipDustCount", skipDustCount).
		Int("count", count).
		Int("batchSize", c.cfg.UTXO.TransactionBatchSize).
		Int("batchCount", len(batches)).
		Msg("getVinZeroTxs")

	// get the vin zero txs one batch at a time
	retries := 0
	for i := 0; i < len(batches); i++ {
		results, errs, err := c.rpc.BatchGetRawTransactionVerbose(batches[i])

		// if there was no rpc error, check for any tx errors
		txErrCount := 0
		if err == nil {
			for _, txErr := range errs {
				if txErr != nil {
					err = txErr
				}
				txErrCount++
			}
		}

		// retry the batch a few times on any errors to avoid wasted work
		if err != nil {
			if retries >= 3 {
				return nil, err
			}

			c.log.Err(err).Int("txErrCount", txErrCount).Msgf("retrying block txs batch %d", i)
			time.Sleep(time.Second)
			retries++
			i-- // retry the same batch
			continue
		}

		// add transactions to block result
		for _, tx := range results {
			vinZeroTxs[tx.Txid] = tx
		}
	}

	c.log.Debug().
		Int64("height", block.Height).
		Dur("duration", time.Since(start)).
		Msg("getVinZeroTxs complete")

	return vinZeroTxs, nil
}

func (c *Client) extractTxs(block *btcjson.GetBlockVerboseTxResult) (types.TxIn, error) {
	txIn := types.TxIn{
		Chain:   c.GetChain(),
		MemPool: false,
	}

	var vinZeroTxs map[string]*btcjson.TxRawResult
	var err error
	if !c.disableVinZeroBatch {
		vinZeroTxs, err = c.getVinZeroTxs(block)
		if err != nil {
			c.log.Error().Err(err).Msg("fail to get txid to vin zero tx, getTxIn will fan out")
		}
	}

	var txItems []*types.TxInItem
	for idx, tx := range block.Tx {
		// mempool transaction get committed to block , thus remove it from mempool cache
		c.removeFromMemPoolCache(tx.Hash)
		var txInItem types.TxInItem
		txInItem, err = c.getTxIn(&block.Tx[idx], block.Height, false, vinZeroTxs)
		if err != nil {
			// expected since vouts below dust threshold are skipped for vinZeroTxs
			c.log.Debug().Str("txid", tx.Txid).Err(err).Msg("fail to get TxInItem")
			continue
		}
		if txInItem.IsEmpty() {
			continue
		}
		if txInItem.Coins.IsEmpty() {
			continue
		}
		if txInItem.Coins[0].Amount.LT(c.cfg.ChainID.DustThreshold()) {
			continue
		}
		var added bool
		added, err = c.temporalStorage.TrackObservedTx(txInItem.Tx)
		if err != nil {
			c.log.Err(err).Msgf("fail to determine whether hash(%s) had been observed before", txInItem.Tx)
		}
		if !added {
			c.log.Info().Msgf("tx: %s had been report before, ignore", txInItem.Tx)
			continue
		}
		txItems = append(txItems, &txInItem)
	}
	txIn.TxArray = txItems
	return txIn, nil
}

// ignoreTx checks if we can already ignore a tx according to preset rules
// Allow up to 10 Vouts with value and 2 OP_RETURN Vouts (for getMemo appending).
func (c *Client) ignoreTx(tx *btcjson.TxRawResult, height int64) bool {
	if len(tx.Vin) == 0 || len(tx.Vout) == 0 || len(tx.Vout) > 12 {
		return true
	}
	if tx.Vin[0].Txid == "" {
		return true
	}
	// LockTime <= current height doesn't affect spendability,
	// and most wallets for users doing Memoless Savers deposits automatically set LockTime to the current height.
	if tx.LockTime > uint32(height) {
		return true
	}
	countWithOutput := 0
	for _, vout := range tx.Vout {
		// analyze-ignore(float-comparison)
		if vout.Value > 0 {
			countWithOutput++
		}
	}

	// none of the output has any value
	if countWithOutput == 0 {
		return true
	}
	// there are more than ten outputs with value in them, not THORChain format
	if countWithOutput > 10 {
		return true
	}
	return false
}

// getOutput retrieve the correct output for both inbound
// outbound tx.
// logic is if sender is a vault then prefer the first Vout with value,
// else prefer the first Vout with value that's to a vault
// an exception need to be made for consolidate tx , because consolidate tx will be send from asgard back asgard itself
func (c *Client) getOutput(sender string, tx *btcjson.TxRawResult, consolidate bool) (btcjson.Vout, error) {
	isSenderAsgard := c.isAsgardAddress(sender)
	for _, vout := range tx.Vout {
		if strings.EqualFold(vout.ScriptPubKey.Type, "nulldata") {
			continue
		}
		// analyze-ignore(float-comparison)
		if vout.Value <= 0 {
			continue
		}
		addresses := c.getAddressesFromScriptPubKey(vout.ScriptPubKey)
		if len(addresses) != 1 {
			// If more than one address, ignore this Vout.
			// TODO check what we do if get multiple addresses
			continue
		}
		receiver := addresses[0]
		if c.cfg.ChainID.Equals(common.BCHChain) {
			receiver = c.stripBCHAddress(receiver)
		}
		// To be observed, either the sender or receiver must be an observed THORChain vault;
		// if the sender is a vault then assume the first Vout is the output (and a later Vout could be change).
		// If the sender isn't a vault, then do do not for instance
		// return a change address Vout as the output if before the vault-inbound Vout.
		if !isSenderAsgard && !c.isAsgardAddress(receiver) {
			continue
		}

		if consolidate && receiver == sender {
			return vout, nil
		}
		if !consolidate && receiver != sender {
			return vout, nil
		}
	}
	return btcjson.Vout{}, btypes.ErrFailOutputMatchCriteria
}

// isFromAsgard returns true if the tx is from asgard and false if not or on error.
// Since this is used to determine UTXOs used for outbounds, the risk of false negative
// is only that vault members may not find consensus on the outbound, whereas aborting
// on the error would guarantee the member is not a part of consensus. Returning a false
// negative should never be done, as it could result in members using an unconfirmed or
// dust VIN not sent by asgard in an outbound, which can be gamed by a malicious party.
func (c *Client) isFromAsgard(txid string) bool {
	// lookup the txid
	tx, err := c.rpc.GetRawTransactionVerbose(txid)
	if err != nil {
		c.log.Error().Err(err).Str("txid", txid).Msg("fail to get tx")
		return false
	}

	// get the sender
	sender, err := c.getSender(tx, nil)
	if err != nil {
		c.log.Error().Err(err).Str("txid", txid).Msg("fail to get sender")
		return false
	}

	// check if the sender is an asgard address
	return c.isAsgardAddress(sender)
}

// getSender returns sender address for a btc tx, using vin:0
func (c *Client) getSender(tx *btcjson.TxRawResult, vinZeroTxs map[string]*btcjson.TxRawResult) (string, error) {
	if len(tx.Vin) == 0 {
		return "", fmt.Errorf("no vin available in tx")
	}

	var vout btcjson.Vout
	if vinZeroTxs != nil {
		vinTx, ok := vinZeroTxs[tx.Vin[0].Txid]
		if !ok {
			// if vouts are below dust this is expected, so skip log noise
			value, err := sumVoutSats(tx)
			if err != nil || value >= c.cfg.ChainID.DustThreshold().Uint64() {
				c.log.Debug().Str("txid", tx.Txid).Msg("vin zero tx not found")
			}
			return "", fmt.Errorf("missing vin zero tx")
		}
		vout = vinTx.Vout[tx.Vin[0].Vout]
	} else {
		vinTx, err := c.rpc.GetRawTransactionVerbose(tx.Vin[0].Txid)
		if err != nil {
			return "", fmt.Errorf("fail to query raw tx")
		}
		vout = vinTx.Vout[tx.Vin[0].Vout]
	}

	// Validate sender UTXO is single-sig to prevent multisig sender spoofing.
	// An attacker could create a bare multisig with [victim_pubkey, attacker_pubkey],
	// sign with their key, and have the transaction attributed to the victim
	// (since ExtractPkScriptAddrs returns addresses[0] for bare multisig).
	// P2SH/P2WSH-wrapped multisig is unaffected (returns single 3xxx/bc1q address).
	if !c.isValidUTXO(vout.ScriptPubKey.Hex) {
		return "", fmt.Errorf("sender utxo must be single-sig")
	}

	addresses := c.getAddressesFromScriptPubKey(vout.ScriptPubKey)
	if len(addresses) == 0 {
		return "", fmt.Errorf("no address available in vout")
	}
	address := addresses[0]

	if c.cfg.ChainID.Equals(common.BCHChain) {
		address = c.stripBCHAddress(address)
	}
	return address, nil
}

func (c *Client) getAddressesFromScriptPubKey(scriptPubKey btcjson.ScriptPubKeyResult) []string {
	if c.cfg.ChainID.Equals(common.BTCChain) {
		return c.getAddressesFromScriptPubKeyBTC(scriptPubKey)
	}
	return scriptPubKey.Addresses
}

// getMemo returns memo for a btc tx, using vout OP_RETURN
func (c *Client) getMemo(tx *btcjson.TxRawResult) (string, error) {
	var memo string

	if c.cfg.ChainID.Equals(common.ZECChain) {
		if len(tx.Vin) == 0 || len(tx.Vout) == 0 {
			c.log.Error().Str("txid", tx.Txid).Msg("shielded tx")
			return "", nil
		}
	}

	for _, vOut := range tx.Vout {
		switch strings.ToLower(vOut.ScriptPubKey.Type) {
		case "witness_v0_keyhash", "pubkeyhash", "nulldata":
			// do nothing
		case "nonstandard":
			// zcashd reports the synthetic segwit-style memo continuation outputs as
			// nonstandard, even though the script still disassembles to `0 <20-byte-data>`.
			if !c.cfg.ChainID.Equals(common.ZECChain) {
				continue
			}
		default:
			continue
		}

		buf, err := hex.DecodeString(vOut.ScriptPubKey.Hex)
		if err != nil {
			c.log.Err(err).Msg("fail to hex decode scriptPubKey")
			continue
		}

		var asm string
		switch c.cfg.ChainID {
		case common.DOGEChain:
			asm, err = dogetxscript.DisasmString(buf)
		case common.BCHChain:
			asm, err = bchtxscript.DisasmString(buf)
		case common.LTCChain:
			asm, err = ltctxscript.DisasmString(buf)
		case common.BTCChain:
			asm, err = btctxscript.DisasmString(buf)
		case common.ZECChain:
			asm, err = btctxscript.DisasmString(buf)

		default:
			c.log.Fatal().Msg("unsupported chain")
		}

		if err != nil {
			c.log.Err(err).Msg("fail to disasm script pubkey")
			continue
		}
		fields := strings.Fields(asm)

		if len(fields) < 2 {
			// we need at least OP_RETURN + data, or 0 + address
			continue
		}

		if fields[0] == "OP_RETURN" {
			// skip "0" field to avoid log noise
			if fields[1] == "0" {
				continue
			}

			var decoded string
			decoded, err = c.decodeHexString(fields[1])
			if err != nil {
				// silently return no memo to reduce log noise
				return "", nil
			}
			memo += decoded
			continue
		}

		// don't inspect further non OP_RETURN outputs unless we found one
		if len(memo) < constants.MaxOpReturnDataSize {
			continue
		}

		// marker can be at position >= 79 for a single / multiple OP_RETURN_
		if strings.LastIndex(memo, "^") < constants.MaxOpReturnDataSize-1 {
			// no continuation marker found
			continue
		}

		var pubkey string

		switch len(fields) {
		case 2:
			// Pay-to-witness-public-key-hash (P2WPKH) script format
			// Format: <0> <20-byte-key-hash>
			if fields[0] != "0" {
				continue
			}

			pubkey = fields[1]
		case 5:
			// Pay-to-public-key-hash (P2PKH) script format
			// Format: OP_DUP OP_HASH160 <20-byte-key-hash> OP_EQUALVERIFY OP_CHECKSIG
			requiredOps := []string{
				"OP_DUP", "OP_HASH160", fields[2],
				"OP_EQUALVERIFY", "OP_CHECKSIG",
			}

			isValidScript := true
			for i := 0; i < 4; i++ {
				if fields[i] != requiredOps[i] {
					isValidScript = false
					break
				}
			}

			if !isValidScript {
				continue
			}

			pubkey = fields[2]
		default:
			continue
		}

		// process pubkey

		// pubkey hash is ripemd-160, which is 20 bytes, 40 chars in hex
		if len(pubkey) != 40 {
			continue
		}

		// remove trailing zeros, if found
		pubkey = c.regexpRemoveTrailingZeros.ReplaceAllString(pubkey, "")

		decoded, err := c.decodeHexString(pubkey)
		if err != nil {
			// silently return no memo to reduce log noise
			return "", nil
		}
		memo += decoded

		// if pubkey has been stripped (was ending with "00") stop processing
		if len(pubkey) != 40 {
			break
		}

		// if memo > max size, stop processing
		if len(memo) >= constants.MaxMemoSize {
			break
		}
	}

	if strings.LastIndex(memo, "^") >= constants.MaxOpReturnDataSize-1 {
		memo = strings.Replace(memo, "^", "", 1)
	}

	return memo, nil
}

// decodeHexString decodes a provided hex string and returns the result
// as string or "" if the decoding failed
func (c *Client) decodeHexString(hexString string) (string, error) {
	errMsg := "fail to decode data: " + hexString

	decoded, err := hex.DecodeString(hexString)
	if err != nil {
		c.log.Debug().Err(err).Msg(errMsg)
		return "", err
	}

	// check for non alphanumeric chars
	// only allow letters: a-z A-Z, numbers, space and the following symbols:
	// !"#$%&'()*+,-./:;<=>?@[\]^_`{|}~
	for i, b := range decoded {
		if b < 0x20 || b > 0x7E {
			err := fmt.Errorf("invalid hex value at position %d: 0x%X", i, b)
			c.log.Debug().Err(err).Msg(errMsg)
			return "", err
		}
	}

	return string(decoded), nil
}

// getGas returns gas for a tx (sum vin - sum vout)
func (c *Client) getGas(tx *btcjson.TxRawResult, isInbound bool) (common.Gas, error) {
	var sumVin uint64 = 0
	for _, vin := range tx.Vin {
		vinTx, err := c.rpc.GetRawTransactionVerbose(vin.Txid)
		if err != nil {
			return common.Gas{}, fmt.Errorf("fail to query raw tx from node")
		}

		amount, err := btcutil.NewAmount(vinTx.Vout[vin.Vout].Value)
		if err != nil {
			return nil, err
		}
		sumVin += uint64(amount.ToUnit(btcutil.AmountSatoshi))
	}
	var sumVout uint64 = 0
	for _, vout := range tx.Vout {
		// Ignore all values after the first OP_RETURN on outbounds to consider the values
		// in the extra p2wpkh outputs as part of the gas and avoid insolvency.
		if !isInbound && strings.ToLower(vout.ScriptPubKey.Type) == "nulldata" {
			break
		}

		amount, err := btcutil.NewAmount(vout.Value)
		if err != nil {
			return nil, err
		}
		sumVout += uint64(amount.ToUnit(btcutil.AmountSatoshi))
	}
	totalGas := sumVin - sumVout
	return common.Gas{
		common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(totalGas)),
	}, nil
}

func (c *Client) getCoinbaseValue(blockHeight int64) (int64, error) {
	hash, err := c.rpc.GetBlockHash(blockHeight)
	if err != nil {
		return 0, fmt.Errorf("fail to get block verbose tx: %w", err)
	}
	// A block's content is fixed by its hash, so reuse the value computed when
	// this block was scanned instead of fetching the whole block again.
	if value, ok := c.coinbases.get(blockHeight, hash); ok {
		return value, nil
	}
	result, err := c.rpc.GetBlockVerboseTxs(hash)
	if err != nil {
		return 0, fmt.Errorf("fail to get block verbose tx: %w", err)
	}
	return coinbaseValue(result)
}

// coinbaseValue returns the total output value of the block's coinbase tx.
func coinbaseValue(result *btcjson.GetBlockVerboseTxResult) (int64, error) {
	for _, tx := range result.Tx {
		if len(tx.Vin) == 1 && tx.Vin[0].IsCoinBase() {
			total := float64(0)
			for _, opt := range tx.Vout {
				total += opt.Value
			}
			amt, err := btcutil.NewAmount(total)
			if err != nil {
				return 0, fmt.Errorf("fail to parse amount: %w", err)
			}
			return int64(amt), nil
		}
	}
	return 0, fmt.Errorf("fail to get coinbase value")
}

// getBlockRequiredConfirmation find out how many confirmation the given txIn need to have before it can be send to THORChain
func (c *Client) getBlockRequiredConfirmation(txIn types.TxIn, height int64) (int64, error) {
	asgardAddresses, err := c.getAsgardAddress()
	if err != nil {
		c.log.Err(err).Msg("fail to get asgard addresses")
	}
	totalTxValue := txIn.GetTotalTransactionValue(c.cfg.ChainID.GetGasAsset(), asgardAddresses)
	totalFeeAndSubsidy, err := c.getCoinbaseValue(height)
	if err != nil {
		c.log.Err(err).Msgf("fail to get coinbase value")
	}
	confMul, err := utxo.GetConfMulBasisPoint(c.GetChain().String(), c.bridge)
	if err != nil {
		c.log.Err(err).Msgf("fail to get conf multiplier mimir value for %s", c.GetChain().String())
	}
	if totalFeeAndSubsidy == 0 {
		var cbValue btcutil.Amount
		cbValue, err = btcutil.NewAmount(c.cfg.ChainID.DefaultCoinbase())
		if err != nil {
			return 0, fmt.Errorf("fail to get default coinbase value: %w", err)
		}
		totalFeeAndSubsidy = int64(cbValue)
	}
	confValue := common.GetUncappedShare(confMul, cosmos.NewUint(constants.MaxBasisPts), cosmos.SafeUintFromInt64(totalFeeAndSubsidy))
	confirm := totalTxValue.Quo(confValue).Uint64()
	confirm, err = utxo.MaxConfAdjustment(confirm, c.GetChain().String(), c.bridge)
	if err != nil {
		c.log.Err(err).Msgf("fail to get max conf value adjustment for %s", c.GetChain().String())
	}
	if confirm < c.cfg.MinConfirmations {
		confirm = c.cfg.MinConfirmations
	}
	c.log.Info().Msgf("totalTxValue:%s, totalFeeAndSubsidy:%d, confirm:%d", totalTxValue, totalFeeAndSubsidy, confirm)

	return int64(confirm), nil
}
