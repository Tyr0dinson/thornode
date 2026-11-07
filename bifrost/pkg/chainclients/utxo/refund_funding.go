package utxo

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/btcutil"

	ctypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

var _ ctypes.RefundFundingValidator = (*Client)(nil)

type refundFundingLocation struct {
	TxID          string `json:"txid"`
	BlockHash     string `json:"blockhash"`
	Confirmations *int64 `json:"confirmations"`
}

// ValidateRefundFunding reconstructs current funding without observation metadata,
// scanner callbacks or deduplication state. It intentionally works before !115.
func (c *Client) ValidateRefundFunding(ctx context.Context, inbound common.ObservedTx) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id, err := hex.DecodeString(inbound.Tx.ID.String())
	if err != nil || len(id) != 32 || !inbound.Tx.Chain.Equals(c.cfg.ChainID) || inbound.ObservedPubKey.IsEmpty() {
		return fmt.Errorf("invalid refund funding observation")
	}
	vault, err := inbound.ObservedPubKey.GetAddress(c.cfg.ChainID)
	if err != nil || c.refundFundingAddress(vault.String()) != c.refundFundingAddress(inbound.Tx.ToAddress.String()) {
		return fmt.Errorf("refund funding destination is not the observed vault")
	}
	var openingHeight int64
	if err = c.rpc.CallContext(ctx, &openingHeight, "getblockcount"); err != nil {
		return err
	}
	if openingHeight <= 0 {
		return fmt.Errorf("refund funding tip is unavailable")
	}
	var openingHash string
	if err = c.rpc.CallContext(ctx, &openingHash, "getblockhash", openingHeight); err != nil {
		return err
	}
	var location refundFundingLocation
	if err = c.rpc.CallContext(ctx, &location, "getrawtransaction", inbound.Tx.ID.String(), true); err != nil {
		return fmt.Errorf("refund funding location: %w", err)
	}
	if !strings.EqualFold(location.TxID, inbound.Tx.ID.String()) || location.BlockHash == "" || location.Confirmations == nil || *location.Confirmations <= 0 {
		return fmt.Errorf("refund funding transaction is not canonically mined")
	}
	var rawBlock json.RawMessage
	if err = c.rpc.CallContext(ctx, &rawBlock, "getblock", location.BlockHash, 2); err != nil {
		return fmt.Errorf("refund funding block: %w", err)
	}
	var block btcjson.GetBlockVerboseTxResult
	if err = json.Unmarshal(rawBlock, &block); err != nil {
		return err
	}
	if block.Height <= 0 || !strings.EqualFold(block.Hash, location.BlockHash) {
		return fmt.Errorf("refund funding block identity is unavailable")
	}
	asgards, err := sharedutxo.GetAsgardAddress(c.cfg.ChainID, c.bridge)
	if err != nil {
		return fmt.Errorf("refund funding vault policy: %w", err)
	}
	asgards = append(asgards, vault)
	for i, address := range asgards {
		asgards[i] = common.Address(c.refundFundingAddress(address.String()))
	}
	batch, funding, err := c.refundFundingBatch(ctx, &block, asgards, inbound.Tx.ID)
	if err != nil {
		return err
	}
	credited := inbound
	credited.Tx.FromAddress = common.Address(c.refundFundingAddress(inbound.Tx.FromAddress.String()))
	credited.Tx.ToAddress = common.Address(c.refundFundingAddress(inbound.Tx.ToAddress.String()))
	if !ctypes.RefundFundingMatches(credited, funding) {
		return fmt.Errorf("canonical refund funding does not match the credited deposit")
	}
	reward, err := c.refundFundingReward(rawBlock)
	if err != nil {
		return err
	}
	required, err := c.refundFundingRequirement(batch, asgards, reward)
	if err != nil {
		return err
	}
	var tip int64
	if err = c.rpc.CallContext(ctx, &tip, "getblockcount"); err != nil {
		return err
	}
	if tip < block.Height || tip-block.Height < required {
		return fmt.Errorf("refund funding has insufficient canonical confirmations")
	}
	var canonical, anchor string
	if err = c.rpc.CallContext(ctx, &canonical, "getblockhash", block.Height); err != nil {
		return err
	}
	if err = c.rpc.CallContext(ctx, &anchor, "getblockhash", openingHeight); err != nil {
		return err
	}
	// New blocks on an unchanged anchor do not change the proof. Requiring an
	// unchanged tip fails on fast chains (mocknet mines every second), and nodes
	// then hold the same refund for different times and desynchronize keysign.
	if openingHash == "" || tip < openingHeight || !strings.EqualFold(anchor, openingHash) || !strings.EqualFold(canonical, block.Hash) {
		return fmt.Errorf("chain view changed during refund funding validation")
	}
	return ctx.Err()
}

func (c *Client) refundFundingAddress(address string) string {
	if c.cfg.ChainID.Equals(common.BCHChain) {
		return c.stripBCHAddress(address)
	}
	return address
}

func refundFundingVault(address string, asgards []common.Address) bool {
	for _, vault := range asgards {
		if address == vault.String() {
			return true
		}
	}
	return false
}

// Dust and parser rejections happen before any parent lookup. Only candidates
// that can contribute to the whole-block policy incur an input RPC.
func (c *Client) refundFundingBatch(ctx context.Context, block *btcjson.GetBlockVerboseTxResult, asgards []common.Address, fundingID common.TxID) (types.TxIn, *types.TxInItem, error) {
	batch := types.TxIn{Chain: c.cfg.ChainID}
	blockParents := make(map[string]*btcjson.TxRawResult, len(block.Tx))
	for i := range block.Tx {
		blockParents[strings.ToLower(block.Tx[i].Txid)] = &block.Tx[i]
	}
	type candidate struct {
		raw       *btcjson.TxRawResult
		memo      string
		sender    string
		senderErr error
	}
	var candidates []candidate
	var parentIDs []string
	byParent := make(map[string][]int)
	var funding *types.TxInItem
	for i := range block.Tx {
		raw := &block.Tx[i]
		if c.ignoreTx(raw, block.Height) {
			continue
		}
		total, err := sumVoutSats(raw)
		if err != nil {
			return batch, nil, err
		}
		if total < c.cfg.ChainID.DustThreshold().Uint64() {
			continue
		}
		memo, err := c.getMemo(raw)
		if err != nil || len([]byte(memo)) > constants.MaxMemoSize {
			continue
		}
		relevant := false
		for _, output := range raw.Vout {
			addresses := c.getAddressesFromScriptPubKey(output.ScriptPubKey)
			if len(addresses) != 1 {
				continue
			}
			amount, amountErr := refundFundingAmount(output.Value)
			if amountErr != nil {
				return batch, nil, amountErr
			}
			if amount >= c.cfg.ChainID.DustThreshold().Uint64() && refundFundingVault(c.refundFundingAddress(addresses[0]), asgards) {
				relevant = true
				break
			}
		}
		if !relevant {
			continue
		}
		parentID := strings.ToLower(raw.Vin[0].Txid)
		if len(byParent[parentID]) == 0 {
			parentIDs = append(parentIDs, parentID)
		}
		byParent[parentID] = append(byParent[parentID], len(candidates))
		candidates = append(candidates, candidate{raw: raw, memo: memo})
	}
	deriveSenders := func(parent *btcjson.TxRawResult, indices []int) error {
		for _, index := range indices {
			candidate := &candidates[index]
			input := candidate.raw.Vin[0]
			if parent == nil || !strings.EqualFold(parent.Txid, input.Txid) {
				return fmt.Errorf("refund funding input identity unavailable")
			}
			if uint64(input.Vout) >= uint64(len(parent.Vout)) {
				return fmt.Errorf("refund funding input index unavailable")
			}
			// getSender needs only this candidate's first input parent. Retain
			// its derived address/error, allowing full fetched parents to expire
			// at the end of their batch rather than accumulating for the block.
			candidate.sender, candidate.senderErr = c.getSender(candidate.raw, map[string]*btcjson.TxRawResult{input.Txid: parent})
		}
		return nil
	}
	var missing []string
	for _, parentID := range parentIDs {
		if parent := blockParents[parentID]; parent != nil {
			if err := deriveSenders(parent, byParent[parentID]); err != nil {
				return batch, nil, err
			}
		} else {
			missing = append(missing, parentID)
		}
	}
	batchSize := 500
	if configured := c.cfg.UTXO.TransactionBatchSize; configured > 0 && configured < batchSize {
		batchSize = configured
	}
	for start := 0; start < len(missing); {
		ids := missing[start:min(start+batchSize, len(missing))]
		fetched, err := c.refundFundingParents(ctx, ids)
		if errors.Is(err, errRefundFundingParentBatchTooLarge) && len(ids) > 1 {
			// Retry this range at a smaller size and keep that bound for the
			// remaining parents. Successful full parents remain batch-local.
			batchSize = max(1, len(ids)/2)
			continue
		}
		if err != nil {
			return batch, nil, fmt.Errorf("refund funding input: %w", err)
		}
		for i, parent := range fetched {
			if err = deriveSenders(parent, byParent[ids[i]]); err != nil {
				return batch, nil, err
			}
		}
		start += len(ids)
	}
	for _, candidate := range candidates {
		raw, memo, sender := candidate.raw, candidate.memo, candidate.sender
		isFunding := strings.EqualFold(raw.Txid, fundingID.String())
		if candidate.senderErr != nil {
			if isFunding {
				return batch, nil, candidate.senderErr
			}
			continue
		}
		parsed, _ := mem.ParseMemo(common.LatestVersion, memo)
		output, ok := c.refundFundingOutput(sender, raw, parsed.IsType(mem.TxConsolidate), asgards)
		if !ok {
			continue
		}
		addresses := c.getAddressesFromScriptPubKey(output.ScriptPubKey)
		to := c.refundFundingAddress(addresses[0])
		if refundFundingVault(to, asgards) && !c.isValidUTXO(output.ScriptPubKey.Hex) {
			continue
		}
		amount, err := refundFundingAmount(output.Value)
		if err != nil {
			return batch, nil, err
		}
		if amount < c.cfg.ChainID.DustThreshold().Uint64() {
			continue
		}
		item := &types.TxInItem{BlockHeight: block.Height, Tx: raw.Txid, Sender: sender, To: to, Memo: memo, Coins: common.NewCoins(common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(amount)))}
		if isFunding {
			funding = item
		}
		if refundFundingVault(to, asgards) {
			batch.TxArray = append(batch.TxArray, item)
		}
	}
	return batch, funding, nil
}

const refundFundingMaxParentBatchBytes int64 = 32 << 20

var errRefundFundingParentBatchTooLarge = errors.New("parent batch response exceeds byte limit")

// refundFundingParents uses real JSON-RPC batches even for ZEC, whose generic
// scanner RPC wrapper fans batches out into individual requests. Every batch
// remains inside the proof's caller context and the configured request timeout.
func (c *Client) refundFundingParents(ctx context.Context, ids []string) ([]*btcjson.TxRawResult, error) {
	type request struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}
	requests := make([]request, len(ids))
	var verbose any = true
	version := "1.0"
	if c.cfg.ChainID.Equals(common.ZECChain) {
		verbose = 1
		// Zebra's legacy compatibility shim does not rewrite batch members.
		version = "2.0"
	}
	for i, id := range ids {
		requests[i] = request{JSONRPC: version, ID: i + 1, Method: "getrawtransaction", Params: []any{id, verbose}}
	}
	body, err := json.Marshal(requests)
	if err != nil {
		return nil, err
	}
	host := c.cfg.RPCHost
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.cfg.UserName, c.cfg.Password)
	client := &http.Client{Timeout: c.cfg.BlockScanner.HTTPRequestTimeout}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("parent batch HTTP status %d", response.StatusCode)
	}
	parents, err := decodeRefundFundingParents(response.Body, ids, refundFundingMaxParentBatchBytes)
	if err != nil {
		return nil, err
	}
	return parents, ctx.Err()
}

func decodeRefundFundingParents(reader io.Reader, ids []string, maxBytes int64) ([]*btcjson.TxRawResult, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes", errRefundFundingParentBatchTooLarge, maxBytes)
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '{' {
		var aggregate struct {
			JSONRPC string            `json:"jsonrpc"`
			ID      json.RawMessage   `json:"id"`
			Result  json.RawMessage   `json:"result"`
			Error   *btcjson.RPCError `json:"error"`
		}
		// Zebra/jsonrpsee can replace an oversized aggregate with a small
		// top-level error before our byte reader reaches its own limit.
		if json.Unmarshal(body, &aggregate) == nil && aggregate.JSONRPC == "2.0" &&
			bytes.Equal(bytes.TrimSpace(aggregate.ID), []byte("null")) && len(aggregate.Result) == 0 &&
			aggregate.Error != nil && aggregate.Error.Code == -32011 && aggregate.Error.Message == "The batch response was too large" {
			return nil, fmt.Errorf("%w: %s", errRefundFundingParentBatchTooLarge, aggregate.Error.Message)
		}
	}
	var results []struct {
		ID     *int                 `json:"id"`
		Result *btcjson.TxRawResult `json:"result"`
		Error  *btcjson.RPCError    `json:"error"`
	}
	if err = json.Unmarshal(body, &results); err != nil {
		return nil, err
	}
	if len(results) != len(ids) {
		return nil, fmt.Errorf("parent batch result count mismatch")
	}
	parents := make([]*btcjson.TxRawResult, len(ids))
	for _, result := range results {
		if result.ID == nil || *result.ID <= 0 || *result.ID > len(ids) || parents[*result.ID-1] != nil {
			return nil, fmt.Errorf("parent batch result identity unavailable")
		}
		if result.Error != nil {
			return nil, result.Error
		}
		if result.Result == nil || !strings.EqualFold(result.Result.Txid, ids[*result.ID-1]) {
			return nil, fmt.Errorf("refund funding input identity unavailable")
		}
		parents[*result.ID-1] = result.Result
	}
	return parents, nil
}

func (c *Client) refundFundingOutput(sender string, raw *btcjson.TxRawResult, consolidate bool, asgards []common.Address) (btcjson.Vout, bool) {
	senderVault := refundFundingVault(sender, asgards)
	for _, output := range raw.Vout {
		if strings.EqualFold(output.ScriptPubKey.Type, "nulldata") {
			continue
		}
		amount, err := refundFundingAmount(output.Value)
		if err != nil || amount == 0 {
			continue
		}
		addresses := c.getAddressesFromScriptPubKey(output.ScriptPubKey)
		if len(addresses) != 1 {
			continue
		}
		receiver := c.refundFundingAddress(addresses[0])
		if !senderVault && !refundFundingVault(receiver, asgards) {
			continue
		}
		if (consolidate && receiver == sender) || (!consolidate && receiver != sender) {
			return output, true
		}
	}
	return btcjson.Vout{}, false
}

// Missing reward data is uncertainty; explicit zero retains the scanner's
// default-subsidy policy (including ZEC's explicit empty transparent outputs).
func (c *Client) refundFundingReward(raw json.RawMessage) (cosmos.Uint, error) {
	var block struct {
		Tx []struct {
			Vin  []btcjson.Vin `json:"vin"`
			Vout *[]struct {
				Value *float64 `json:"value"`
			} `json:"vout"`
		} `json:"tx"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return cosmos.ZeroUint(), err
	}
	if len(block.Tx) == 0 || len(block.Tx[0].Vin) == 0 || block.Tx[0].Vin[0].Coinbase == "" || block.Tx[0].Vout == nil {
		return cosmos.ZeroUint(), fmt.Errorf("refund funding coinbase data unavailable")
	}
	if len(*block.Tx[0].Vout) == 0 && !c.cfg.ChainID.Equals(common.ZECChain) {
		return cosmos.ZeroUint(), fmt.Errorf("refund funding coinbase outputs unavailable")
	}
	reward := cosmos.ZeroUint()
	for _, output := range *block.Tx[0].Vout {
		if output.Value == nil {
			return reward, fmt.Errorf("refund funding coinbase value unavailable")
		}
		amount, err := refundFundingAmount(*output.Value)
		if err != nil {
			return reward, err
		}
		reward = reward.Add(cosmos.NewUint(amount))
	}
	if reward.IsZero() {
		amount, err := refundFundingAmount(c.cfg.ChainID.DefaultCoinbase())
		if err != nil || amount <= 0 {
			return reward, fmt.Errorf("refund funding default coinbase unavailable")
		}
		reward = cosmos.NewUint(amount)
	}
	return reward, nil
}

func (c *Client) refundFundingRequirement(batch types.TxIn, asgards []common.Address, reward cosmos.Uint) (int64, error) {
	multiplier, err := sharedutxo.GetConfMulBasisPoint(c.cfg.ChainID.String(), c.bridge)
	if err != nil {
		return 0, err
	}
	denominator := common.GetUncappedShare(multiplier, cosmos.NewUint(constants.MaxBasisPts), reward)
	if denominator.IsZero() {
		return 0, fmt.Errorf("refund funding confirmation denominator is zero")
	}
	total := batch.GetTotalTransactionValue(c.cfg.ChainID.GetGasAsset(), asgards)
	required := total.Quo(denominator).BigInt()
	if !required.IsUint64() {
		return 0, fmt.Errorf("refund funding confirmation requirement overflows")
	}
	count, err := sharedutxo.MaxConfAdjustment(required.Uint64(), c.cfg.ChainID.String(), c.bridge)
	if err != nil {
		return 0, err
	}
	count = max(count, c.cfg.MinConfirmations)
	if count > math.MaxInt64 {
		return 0, fmt.Errorf("refund funding confirmation requirement overflows")
	}
	return int64(count), nil
}

func refundFundingAmount(value float64) (uint64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Signbit(value) {
		return 0, fmt.Errorf("refund funding amount is invalid")
	}
	// Bound the RPC decimal before btcutil's float-to-int conversion. NewAmount
	// checks NaN/Inf but permits finite values outside its signed integer range.
	scaled := new(big.Float).Mul(big.NewFloat(value), big.NewFloat(common.One))
	whole, _ := scaled.Int(nil)
	if !whole.IsInt64() {
		return 0, fmt.Errorf("refund funding amount overflows")
	}
	amount, err := btcutil.NewAmount(value)
	if err != nil || amount < 0 {
		return 0, fmt.Errorf("refund funding amount is invalid")
	}
	return uint64(amount), nil
}
