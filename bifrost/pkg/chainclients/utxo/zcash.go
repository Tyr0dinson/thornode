package utxo

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	btcjson "github.com/btcsuite/btcd/btcjson"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/chain/zec/go/zec"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// getChainCfgZEC returns the Zcash chain configuration for the current network.
// Returns NetworkParams with proper Zcash address prefixes (t1/t3 for mainnet, tm/tn for testnet).
func (c *Client) getChainCfgZEC() *NetworkParams {
	switch common.CurrentChainNetwork {
	case common.MockNet, common.TestNet:
		return &RegtestNetParams
	case common.MainNet, common.StageNet:
		return &MainNetParams
	default:
		c.log.Fatal().Msg("unsupported network")
		return nil
	}
}

// getZECNetwork returns the zec.Network for Rust FFI calls
func (c *Client) getZECNetwork() zec.Network {
	switch common.CurrentChainNetwork {
	case common.MockNet, common.TestNet:
		return zec.NetworkRegtest
	case common.MainNet, common.StageNet:
		return zec.NetworkMain
	default:
		c.log.Fatal().Msg("unsupported network")
		return zec.NetworkRegtest
	}
}

// getPubKeyBytes extracts raw bytes from a bech32-encoded PubKey
func getPubKeyBytes(pubKey common.PubKey) ([]byte, error) {
	pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, string(pubKey))
	if err != nil {
		return nil, fmt.Errorf("fail to decode pubkey from bech32: %w", err)
	}
	return pk.Bytes(), nil
}

// getAddressesFromScriptPubKeyZEC extracts Zcash transparent addresses from script public key.
// Uses Zcash-specific address encoding to produce proper t-addresses (t1/t3 for mainnet, tm/tn for testnet).
func (c *Client) getAddressesFromScriptPubKeyZEC(scriptPubKey btcjson.ScriptPubKeyResult) []string {
	// Try to use RPC-provided addresses first
	if len(scriptPubKey.Addresses) > 0 {
		return scriptPubKey.Addresses
	}

	// If no addresses from RPC, extract from scriptPubKey hex
	if len(scriptPubKey.Hex) == 0 {
		return nil
	}

	// Use Zcash-specific address extraction with proper network params
	extractedAddresses, err := GetAddressesFromScriptPubKeyZEC(
		scriptPubKey.Hex,
		scriptPubKey.Addresses,
		c.getChainCfgZEC(),
	)
	if err != nil {
		c.log.Err(err).Msg("fail to extract Zcash addresses from script pub key")
		return nil
	}

	return extractedAddresses
}

// signSigHashZEC signs a single sighash using TSS or local private key.
// Returns DER-encoded ECDSA signature for use with Rust ApplySignatures.
func (c *Client) signSigHashZEC(sigHash []byte, tx stypes.TxOutItem) ([]byte, error) {
	var signable *tssSignableZEC
	if tx.VaultPubKey.Equals(c.nodePubKey) {
		// For Yggdrasil vaults, use local private key
		// Note: This path needs a local key signable implementation
		// For now, we'll use TSS for all signing
		signable = newTssSignableZEC(tx.VaultPubKey, c.tssKeySigner, c.log)
	} else {
		signable = newTssSignableZEC(tx.VaultPubKey, c.tssKeySigner, c.log)
	}

	signature, err := signable.Sign(sigHash)
	if err != nil {
		return nil, fmt.Errorf("fail to sign sighash: %w", err)
	}

	if signature == nil {
		return nil, fmt.Errorf("signature is nil after signing")
	}

	// Serialize to DER format for Rust ApplySignatures
	return signature.Serialize(), nil
}

// signPtxZEC signs all sighashes in the PartialTx and returns the final signed transaction bytes.
// Uses Rust ApplySignatures to assemble the final transaction with scriptSig fields.
func (c *Client) signPtxZEC(ptx zec.PartialTx, tx stypes.TxOutItem) ([]byte, error) {
	if len(ptx.Sighashes) == 0 {
		return nil, errors.New("no sighashes in PartialTx")
	}

	// Sign all sighashes
	signatures := make([][]byte, len(ptx.Sighashes))
	for idx, sigHash := range ptx.Sighashes {
		signature, err := c.signSigHashZEC(sigHash, tx)
		if err != nil {
			return nil, fmt.Errorf("fail to sign sighash at index %d: %w", idx, err)
		}
		signatures[idx] = signature
	}

	// Check signatures were produced
	if signatures == nil {
		return nil, errors.New("no signatures produced")
	}

	// Get vault public key bytes
	vaultPubKeyBytes, err := getPubKeyBytes(tx.VaultPubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get vault pubkey bytes: %w", err)
	}

	// Call Rust to apply signatures and assemble final transaction
	txBytes, err := zec.ApplySignatures(vaultPubKeyBytes, ptx, signatures, c.getZECNetwork())
	if err != nil {
		return nil, fmt.Errorf("fail to apply ZEC signatures: %w", err)
	}

	return txBytes, nil
}

// SignTxZEC signs a Zcash transaction using Rust-based signing (BuildPtx → sign → ApplySignatures).
// Returns signed transaction bytes, checkpoint data (for retry), and observation TxInItem.
func (c *Client) SignTxZEC(tx stypes.TxOutItem, thorchainHeight int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if !tx.Chain.Equals(common.ZECChain) {
		return nil, nil, nil, errors.New("not ZEC chain")
	}

	// Skip outbounds without coins
	if tx.Coins.IsEmpty() {
		return nil, nil, nil, nil
	}

	// Build or load PartialTx
	var ptx zec.PartialTx
	var checkpoint []byte
	var err error

	if len(tx.Checkpoint) > 0 {
		// Load from checkpoint for retry
		c.log.Info().Msg("loading ZEC transaction from checkpoint")
		var initialPtx zec.PartialTx
		err = json.Unmarshal(tx.Checkpoint, &initialPtx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to unmarshal checkpoint: %w", err)
		}

		// Re-run Rust BuildPtx to regenerate sighashes
		vaultPubKeyBytes, err := getPubKeyBytes(tx.VaultPubKey)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to get vault pubkey bytes: %w", err)
		}

		ptx, err = zec.BuildPtx(vaultPubKeyBytes, initialPtx, c.getZECNetwork())
		if err != nil {
			return nil, tx.Checkpoint, nil, fmt.Errorf("rust BuildPtx failed on checkpoint: %w", err)
		}
		checkpoint = tx.Checkpoint // Keep existing checkpoint for further retries
	} else {
		// Build fresh PartialTx
		ptx, checkpoint, err = c.buildPtxZEC(tx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to build ZEC ptx: %w", err)
		}
	}

	// Validate outputs count (1 or 2: customer + optional change)
	if len(ptx.Outputs) < 1 || len(ptx.Outputs) > 2 {
		return nil, nil, nil, fmt.Errorf("invalid output count: %d (expected 1-2)", len(ptx.Outputs))
	}

	// Verify fee doesn't exceed MaxGas
	if !tx.MaxGas.IsEmpty() {
		gasCoins := tx.MaxGas.ToCoins()
		if len(gasCoins) > 0 && gasCoins[0].Asset.Equals(common.ZECAsset) {
			if gasCoins[0].Amount.BigInt().Uint64() < ptx.Fee {
				return nil, checkpoint, nil, fmt.Errorf("fee %d exceeds MaxGas %s", ptx.Fee, tx.MaxGas)
			}
		}
	}

	// Sign the PartialTx
	txBytes, err := c.signPtxZEC(ptx, tx)
	if err != nil {
		return nil, checkpoint, nil, fmt.Errorf("fail to sign ZEC ptx: %w", err)
	}

	// Create observation TxInItem (sent before broadcast)
	chainHeight, err := c.GetHeight()
	if err != nil {
		c.log.Err(err).Msg("fail to get chain height for observation")
		chainHeight = 0 // Fallback: voter doesn't strictly need height
	}

	sender, err := tx.VaultPubKey.GetAddress(common.ZECChain)
	if err != nil {
		return txBytes, checkpoint, nil, fmt.Errorf("fail to get sender address: %w", err)
	}

	// First output is customer payment
	outAmount := ptx.Outputs[0].Amount
	txIn := &stypes.TxInItem{
		BlockHeight: int64(chainHeight),
		Tx:          string(ptx.Txid),
		Sender:      sender.String(),
		To:          tx.ToAddress.String(),
		Coins:       common.Coins{common.NewCoin(common.ZECAsset, cosmos.NewUint(outAmount))},
		Memo:        tx.Memo,
		Gas:         common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(ptx.Fee))},
	}

	return txBytes, checkpoint, txIn, nil
}

// convertRPCUtxosToZecUtxos converts RPC UTXO results to zec.Utxo format.
// Maps JSON field names: outputIndex → Vout, satoshis → Value
func convertRPCUtxosToZecUtxos(rpcUtxos []rpc.Utxo) []zec.Utxo {
	utxos := make([]zec.Utxo, len(rpcUtxos))
	for i, utxo := range rpcUtxos {
		utxos[i] = zec.Utxo{
			Txid:   utxo.Txid,
			Height: uint32(utxo.Height),
			Value:  uint64(utxo.Value),
			Vout:   utxo.Vout,
			Script: utxo.Script,
		}
	}
	return utxos
}

// getUtxoToSpendZEC retrieves and filters UTXOs for ZEC transaction signing.
// Similar to getUtxoToSpend but uses ZEC-specific GetAddressUtxos RPC method.
func (c *Client) getUtxoToSpendZEC(pubKey common.PubKey, amount cosmos.Uint) ([]zec.Utxo, error) {
	// Get vault address
	addr, err := pubKey.GetAddress(common.ZECChain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey: %w", err)
	}

	// Fetch all UTXOs for this address
	rpcUtxos, err := c.rpc.GetAddressUtxos(addr.String())
	if err != nil {
		return nil, fmt.Errorf("fail to get UTXOs: %w", err)
	}

	// Sort UTXOs by age (older first) for better UTXO management
	sort.SliceStable(rpcUtxos, func(i, j int) bool {
		if rpcUtxos[i].Height < rpcUtxos[j].Height {
			return true // older blocks first (lower height)
		} else if rpcUtxos[i].Height > rpcUtxos[j].Height {
			return false
		}
		return rpcUtxos[i].Txid < rpcUtxos[j].Txid
	})

	// Filter and select UTXOs
	var selectedUtxos []rpc.Utxo
	var totalAmount cosmos.Uint
	minUTXOAmt := int64(common.ZECChain.DustThreshold().Uint64())
	maxUTXOs := c.getMaximumUtxosToSpend()

	for _, utxo := range rpcUtxos {
		// Skip dust UTXOs (below threshold)
		if utxo.Value < minUTXOAmt {
			continue
		}

		// TODO: Add validation for valid script/address if needed
		// For now, trust the RPC response

		selectedUtxos = append(selectedUtxos, utxo)
		totalAmount = totalAmount.AddUint64(uint64(utxo.Value))

		// Stop when we have enough UTXOs to cover the amount
		if int64(len(selectedUtxos)) >= maxUTXOs && totalAmount.GTE(amount) {
			break
		}
	}

	if totalAmount.LT(amount) {
		return nil, fmt.Errorf("insufficient funds: have %s, need %s", totalAmount, amount)
	}

	// Convert to zec.Utxo format
	return convertRPCUtxosToZecUtxos(selectedUtxos), nil
}

// buildPtxZEC builds a PartialTx structure and calls Rust BuildPtx to generate ZIP-244 sighashes.
// Returns the PartialTx with populated sighashes and checkpoint data for retry support.
func (c *Client) buildPtxZEC(tx stypes.TxOutItem) (zec.PartialTx, []byte, error) {
	// Validate output address
	err := zec.ValidateAddress(tx.ToAddress.String(), c.getZECNetwork())
	if err != nil {
		return zec.PartialTx{}, nil, fmt.Errorf("invalid toAddress: %s, err: %w", tx.ToAddress, err)
	}

	// Get vault public key bytes
	vaultPubKeyBytes, err := getPubKeyBytes(tx.VaultPubKey)
	if err != nil {
		return zec.PartialTx{}, nil, fmt.Errorf("fail to get vault pubkey bytes: %w", err)
	}

	// Get current ZEC height
	zecHeight, err := c.GetHeight()
	if err != nil {
		return zec.PartialTx{}, nil, fmt.Errorf("fail to get ZEC block height: %w", err)
	}

	// Get vault address for change output
	fromAddress, err := tx.VaultPubKey.GetAddress(common.ZECChain)
	if err != nil {
		return zec.PartialTx{}, nil, fmt.Errorf("fail to get vault address: %w", err)
	}

	// Customer payment amount
	amountToCustomer := tx.Coins.GetCoin(common.ZECAsset).Amount

	// Estimate fee for UTXO selection (use maximum estimate with 2 outputs)
	// We'll recalculate the exact fee after selecting UTXOs
	estimatedFee := cosmos.NewUint(zec.CalculateFeeWithMemo(10, 2, tx.Memo)) // assume ~10 inputs for initial estimate

	// Calculate total amount needed
	totalNeeded := amountToCustomer.Add(estimatedFee)

	// Get UTXOs to spend
	utxos, err := c.getUtxoToSpendZEC(tx.VaultPubKey, totalNeeded)
	if err != nil {
		return zec.PartialTx{}, nil, fmt.Errorf("fail to get UTXOs: %w", err)
	}
	if len(utxos) == 0 {
		return zec.PartialTx{}, nil, errors.New("no UTXOs available to spend")
	}

	// Calculate total input amount
	totalInputAmount := cosmos.ZeroUint()
	for _, utxo := range utxos {
		totalInputAmount = totalInputAmount.AddUint64(utxo.Value)
	}

	// Calculate fee using ZIP-317 with actual UTXO count
	fee := zec.CalculateFeeWithMemo(uint64(len(utxos)), 2, tx.Memo)

	// Handle MaxGas if specified
	if !tx.MaxGas.IsEmpty() {
		maxGasCoin := tx.MaxGas.ToCoins().GetCoin(common.ZECAsset)
		if fee > maxGasCoin.Amount.Uint64() {
			c.log.Info().Msgf("max gas: %s, however estimated gas need %d", tx.MaxGas, fee)
			fee = maxGasCoin.Amount.Uint64()
		} else if fee < maxGasCoin.Amount.Uint64() {
			// Add difference to customer's payment
			gap := maxGasCoin.Amount.Uint64() - fee
			c.log.Info().Msgf("max gas: %s, however only %d required, gap %d goes to customer", tx.MaxGas, fee, gap)
			amountToCustomer = amountToCustomer.Add(cosmos.NewUint(gap))
		}
	} else {
		// Handle special memos (Consolidate)
		parsedMemo, err := memo.ParseMemo(common.LatestVersion, tx.Memo)
		if err == nil && parsedMemo.GetType() == memo.TxConsolidate {
			c.log.Info().Msgf("consolidate tx, deducting gas: %d", fee)
			amountToCustomer = common.SafeSub(amountToCustomer, cosmos.NewUint(fee))
		}
	}

	// Verify we have enough funds
	if totalInputAmount.LT(amountToCustomer.AddUint64(fee)) {
		return zec.PartialTx{}, nil, fmt.Errorf("insufficient funds: have %s, need %s", totalInputAmount, amountToCustomer.AddUint64(fee))
	}

	// Calculate change
	change := uint64(0)
	if totalInputAmount.GT(amountToCustomer.AddUint64(fee)) {
		change = totalInputAmount.Sub(amountToCustomer).SubUint64(fee).BigInt().Uint64()
	}

	c.log.Info().Msgf("ZEC tx: inputs=%d, to customer=%s, fee=%d, change=%d",
		totalInputAmount.Uint64(), amountToCustomer, fee, change)

	// Build outputs
	outputs := []zec.Output{
		{
			Address: tx.ToAddress.String(),
			Amount:  amountToCustomer.BigInt().Uint64(),
			Memo:    tx.Memo,
		},
	}

	// Add change output if needed
	if change > 0 {
		c.log.Info().Msgf("adding change output: %d back to %s", change, fromAddress)
		outputs = append(outputs, zec.Output{
			Address: fromAddress.String(),
			Amount:  change,
			Memo:    "",
		})
	}

	// Create PartialTx structure
	ptx := zec.PartialTx{
		Height:       uint32(zecHeight),
		ExpiryHeight: 0, // never expires
		Inputs:       utxos,
		Outputs:      outputs,
		Fee:          fee,
	}

	c.log.Debug().Msgf("ZEC gas fee: %d for inputs=%d, outputs=%d, memo_len=%d",
		ptx.Fee, len(ptx.Inputs), len(ptx.Outputs), len(tx.Memo))

	// Serialize initial ptx for checkpoint
	checkpoint, err := json.Marshal(ptx)
	if err != nil {
		return zec.PartialTx{}, nil, fmt.Errorf("failed to marshal initial PartialTx: %w", err)
	}

	// Call Rust to build the partial transaction and generate ZIP-244 sighashes
	ptx, err = zec.BuildPtx(vaultPubKeyBytes, ptx, c.getZECNetwork())
	if err != nil {
		return zec.PartialTx{}, checkpoint, fmt.Errorf("rust BuildPtx failed: %w", err)
	}

	c.log.Info().Msgf("BuildPtx successful: %d sighashes generated", len(ptx.Sighashes))

	// Sanity check sighashes
	if len(ptx.Sighashes) != len(ptx.Inputs) {
		return zec.PartialTx{}, checkpoint, fmt.Errorf("sighash count mismatch: %d sighashes for %d inputs", len(ptx.Sighashes), len(ptx.Inputs))
	}

	return ptx, checkpoint, nil
}
