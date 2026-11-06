package utxo

import (
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	utxotypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

func (c *Client) getBlockFIRO(height int64) (*btcjson.GetBlockVerboseTxResult, error) {
	hash, err := c.rpc.GetBlockHash(height)
	if err != nil {
		return nil, err
	}
	block, err := c.rpc.GetBlockVerboseTxsFIRO(hash)
	if err != nil {
		return nil, err
	}
	if !isHash256Hex(hash) || block.Hash != hash || block.Height != height || !block.ChainLock || block.Confirmations <= 0 {
		return nil, btypes.ErrUnavailableBlock
	}
	header, err := c.firoHeaderAt(height)
	if err != nil {
		return nil, err
	}
	if header.Hash != hash || !header.ChainLock {
		return nil, btypes.ErrUnavailableBlock
	}
	// A conflict below finality is not an ordinary PoW reorg. Keep the cursor
	// and existing observations intact and stop until operators investigate.
	if c.temporalStorage != nil {
		var previous *utxotypes.BlockMeta
		previous, err = c.temporalStorage.GetBlockMeta(height - 1)
		if err != nil {
			return nil, err
		}
		if previous != nil && previous.BlockHash != "" && previous.BlockHash != block.PreviousHash {
			return nil, fmt.Errorf("FIRO finalized block conflicts with stored history at %d", height)
		}
		var current *utxotypes.BlockMeta
		current, err = c.temporalStorage.GetBlockMeta(height)
		if err != nil {
			return nil, err
		}
		if current != nil && current.BlockHash != "" && current.BlockHash != hash {
			return nil, fmt.Errorf("FIRO finalized hash changed at %d", height)
		}
	}
	return &block.GetBlockVerboseTxResult, nil
}

// normalizeFiroOutputs recognizes only the supported transparent templates.
// Unknown/privacy scripts are not coerced into ordinary payment outputs.
func normalizeFiroOutputs(tx *btcjson.TxRawResult) (bool, error) {
	if len(tx.Vin) == 0 || len(tx.Vout) == 0 || tx.Vin[0].IsCoinBase() || tx.Vin[0].Txid == "" {
		return false, nil
	}
	for _, input := range tx.Vin {
		// Legacy privacy spends can have type zero but use null outpoints.
		// They cannot supply an attributable transparent sender or input fee.
		if input.Txid == "" || strings.Trim(input.Txid, "0") == "" {
			return false, nil
		}
	}
	for i := range tx.Vout {
		out := &tx.Vout[i]
		if out.N != uint32(i) {
			return false, fmt.Errorf("FIRO output index mismatch")
		}
		_, err := amountToSats(out.Value)
		if err != nil {
			return false, err
		}
		script, err := hex.DecodeString(out.ScriptPubKey.Hex)
		if err != nil || len(script) == 0 {
			return false, fmt.Errorf("invalid FIRO output script")
		}
		if script[0] == 0x6a {
			out.ScriptPubKey.Type = "nulldata"
			continue
		}
		address, err := firoutil.AddressFromScript(script, common.FIRONetwork())
		if err != nil {
			return false, nil
		}
		out.ScriptPubKey.Addresses = []string{address.String()}
		switch address.Kind() {
		case firoutil.PubKeyHash:
			out.ScriptPubKey.Type = "pubkeyhash"
		case firoutil.ScriptHash:
			out.ScriptPubKey.Type = "scripthash"
		case firoutil.Exchange:
			out.ScriptPubKey.Type = "exchangeaddress"
		}
	}
	return true, nil
}

func (c *Client) firoGas(tx *btcjson.TxRawResult, outbound bool, cache map[string]*rpc.FiroTxResult) (int64, error) {
	var inputs, outputs int64
	for _, in := range tx.Vin {
		previous, err := c.getPreviousOutputFIRO(in.Txid, in.Vout, cache)
		if err != nil {
			return 0, err
		}
		amount, err := amountToSats(previous.Value)
		if err != nil || amount > btcutil.MaxSatoshi-inputs {
			return 0, fmt.Errorf("invalid FIRO input sum")
		}
		inputs += amount
	}
	for _, out := range tx.Vout {
		// THORChain accounts for burned continuation values as outbound gas.
		if outbound && out.ScriptPubKey.Type == "nulldata" {
			break
		}
		amount, err := amountToSats(out.Value)
		if err != nil || amount > btcutil.MaxSatoshi-outputs {
			return 0, fmt.Errorf("invalid FIRO output sum")
		}
		outputs += amount
	}
	if inputs < outputs {
		return 0, fmt.Errorf("FIRO output sum exceeds inputs")
	}
	return inputs - outputs, nil
}

func (c *Client) getTxInFIRO(tx *btcjson.TxRawResult, height int64, outboundOnly bool, vaults []common.Address, cache map[string]*rpc.FiroTxResult) (*types.TxInItem, error) {
	ok, err := normalizeFiroOutputs(tx)
	if err != nil || !ok {
		return nil, err
	}
	if !isHash256Hex(tx.Txid) {
		return nil, fmt.Errorf("invalid FIRO transaction ID")
	}
	if len(tx.Vout) > 12 {
		return nil, nil
	}
	previous, err := c.getPreviousOutputFIRO(tx.Vin[0].Txid, tx.Vin[0].Vout, cache)
	if err != nil {
		return nil, err
	}
	sender, err := firoScriptAddress(previous.ScriptPubKey.Hex)
	if err != nil {
		return nil, nil
	} // unsupported sender; never attribute bare multisig
	fromVault := slices.Contains(vaults, common.Address(sender.String()))
	if outboundOnly && !fromVault {
		return nil, nil
	}
	memo, err := c.getMemo(tx)
	if err != nil || len(memo) > constants.MaxMemoSize {
		// An invalid on-chain memo will never improve on retry. Observe the
		// payment with an empty memo so THORChain can refund it. Only missing
		// RPC/storage evidence should stop this finalized block's cursor.
		memo = ""
	}
	parsed, _ := mem.ParseMemo(common.LatestVersion, memo)
	consolidate := parsed != nil && parsed.IsType(mem.TxConsolidate)
	var payment *btcjson.Vout
	for i := range tx.Vout {
		out := &tx.Vout[i]
		// Payment and change precede the memo and its continuation outputs.
		if out.ScriptPubKey.Type == "nulldata" {
			break
		}
		// Only skip empty RPC outputs here; monetary calculations use amountToSats.
		// analyze-ignore(float-comparison)
		if out.Value <= 0 || len(out.ScriptPubKey.Addresses) != 1 {
			continue
		}
		receiver := out.ScriptPubKey.Addresses[0]
		if !fromVault && !slices.Contains(vaults, common.Address(receiver)) {
			continue
		}
		if (consolidate && receiver == sender.String()) || (!consolidate && receiver != sender.String()) {
			payment = out
			break
		}
	}
	if payment == nil {
		return nil, nil
	}
	amount, err := amountToSats(payment.Value)
	if err != nil {
		return nil, err
	}
	if uint64(amount) < common.FIROChain.DustThreshold().Uint64() {
		return nil, nil
	}
	gas, err := c.firoGas(tx, fromVault && !slices.Contains(vaults, common.Address(payment.ScriptPubKey.Addresses[0])), cache)
	if err != nil {
		return nil, err
	}
	return &types.TxInItem{
		BlockHeight: height, Tx: tx.Txid, Sender: sender.String(), To: payment.ScriptPubKey.Addresses[0], Memo: memo,
		Coins: common.Coins{common.NewCoin(common.FIROAsset, cosmos.NewUint(uint64(amount)))},
		Gas:   common.Gas{common.NewCoin(common.FIROAsset, cosmos.NewUint(uint64(gas)))},
	}, nil
}

func (c *Client) extractTxsFIRO(block *btcjson.GetBlockVerboseTxResult) (types.TxIn, error) {
	result := types.TxIn{Chain: common.FIROChain}
	vaults, err := c.getAsgardAddress()
	if err != nil {
		return result, err
	}
	cache := make(map[string]*rpc.FiroTxResult)
	var items []*types.TxInItem
	for i := range block.Tx {
		var item *types.TxInItem
		item, err = c.getTxInFIRO(&block.Tx[i], block.Height, false, vaults, cache)
		if err != nil {
			return result, err
		}
		if item != nil {
			items = append(items, item)
		}
	}
	for _, tx := range block.Tx {
		c.removeFromMemPoolCache(tx.Txid)
	}
	// Do not claim any observed IDs until every relevant transaction was read.
	result.TxArray, err = c.trackObservedTxs(items)
	return result, err
}

func (c *Client) fetchMemPoolFIRO(height int64) (result types.TxIn, err error) {
	result = types.TxIn{Chain: common.FIROChain, MemPool: true}
	ids, err := c.rpc.GetRawMempool()
	if err != nil {
		return result, err
	}
	vaults, err := c.getAsgardAddress()
	if err != nil {
		return result, err
	}
	sort.Strings(ids)
	cache := make(map[string]*rpc.FiroTxResult)
	limit := c.cfg.UTXO.TransactionBatchSize * c.cfg.UTXO.MaxMempoolBatches
	if limit <= 0 {
		limit = 500
	}
	var claimed []string
	defer func() {
		if err != nil {
			for _, id := range claimed {
				c.removeFromMemPoolCache(id)
			}
		}
	}()
	for _, id := range ids {
		if len(claimed) >= limit {
			break
		}
		var added bool
		added, err = c.temporalStorage.TrackMempoolTx(id)
		if err != nil {
			return result, err
		}
		if !added {
			continue
		}
		claimed = append(claimed, id)
		var tx *rpc.FiroTxResult
		tx, err = c.firoTransaction(id, cache)
		if err != nil {
			return result, err
		}
		// A delayed mempool snapshot can recreate a work marker after block cleanup.
		// Leave a freshly ChainLocked transaction to the block scanner.
		if tx.ChainLock {
			c.removeFromMemPoolCache(id)
			continue
		}
		if tx.Type != 0 {
			continue
		}
		var item *types.TxInItem
		item, err = c.getTxInFIRO(&tx.TxRawResult, height, true, vaults, cache)
		if err != nil {
			return result, err
		}
		if item != nil {
			result.TxArray = append(result.TxArray, item)
		}
	}
	return result, nil
}

func (c *Client) sendNetworkFeeFIRO(block *btcjson.GetBlockVerboseTxResult) error {
	cache := make(map[string]*rpc.FiroTxResult)
	var fees, size int64
	for i := range block.Tx {
		tx := &block.Tx[i]
		ok, err := normalizeFiroOutputs(tx)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		fee, err := c.firoGas(tx, false, cache)
		if err != nil {
			return err
		}
		if tx.Size <= 0 || int64(tx.Size) > 4_000_000 || fee > btcutil.MaxSatoshi-fees {
			return fmt.Errorf("invalid FIRO fee sample")
		}
		fees, size = fees+fee, size+int64(tx.Size)
	}
	rate := firoDefaultFeeRate
	if size > 0 {
		rate = max(firoMinFeeRate, (fees+size-1)/size)
	}
	c.networkFeeLock.Lock()
	defer c.networkFeeLock.Unlock()
	c.lastFeeRate.Store(uint64(rate))
	// A report for each finalized block also keeps the agreed chain height
	// moving when the chain is quiet and the fee rate stays constant.
	c.globalNetworkFeeQueue <- common.NetworkFee{
		Chain: common.FIROChain, Height: block.Height,
		TransactionSize: c.cfg.UTXO.EstimatedAverageTxSize, TransactionRate: uint64(rate),
	}
	return nil
}

func (c *Client) registerAddressFIRO(address string) error {
	c.firoWalletMu.Lock()
	defer c.firoWalletMu.Unlock()
	err := c.rpc.EnsureWatchAddressFIRO(address)
	if err != nil {
		return fmt.Errorf("import/rescan FIRO address: %w", err)
	}
	return nil
}

func (c *Client) getAddressesFromScriptPubKeyFIRO(script btcjson.ScriptPubKeyResult) []string {
	address, err := firoScriptAddress(script.Hex)
	if err != nil {
		return nil
	}
	return []string{address.String()}
}
