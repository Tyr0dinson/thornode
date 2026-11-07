package utxo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/config"

	sutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type refundFundingPolicyBridge struct {
	thorclient.ThorchainBridge
	keys []thorclient.PubKeyContractAddressPair
}

func (b *refundFundingPolicyBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return b.keys, nil
}

func (*refundFundingPolicyBridge) GetMimirWithRef(template, _ string) (int64, error) {
	if template == constants.MimirTemplateConfMultiplierBasisPoints {
		return 10000, nil
	}
	return 0, nil
}

type utxoRefundFundingFixture struct {
	mu             sync.Mutex
	client         *Client
	policy         *refundFundingPolicyBridge
	inbound        common.ObservedTx
	block          btcjson.GetBlockVerboseTxResult
	parent         btcjson.TxRawResult
	tip            int64
	tipHash        string
	location       string
	missingMember  bool
	missingVerbose bool
	missingReward  bool
	canonicalHash  string
	inputHook      func()
	parents        map[string]btcjson.TxRawResult
	parentCalls    int
	batchSizes     []int
	requestDelay   time.Duration
	batchHook      func([]map[string]any) []map[string]any
	batchEnvelope  func([]map[string]any) any
}

func newUTXORefundFundingFixture(t *testing.T, chain common.Chain) *utxoRefundFundingFixture {
	t.Helper()
	f := &utxoRefundFundingFixture{tip: 105, tipHash: strings.Repeat("f", 64), location: "canonical"}
	f.client = newRefundFundingRPCClient(t, chain, func(method string, params []json.RawMessage) (any, any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case "getblockcount":
			return f.tip, nil
		case "getblockhash":
			var height int64
			if err := json.Unmarshal(params[0], &height); err != nil {
				t.Error(err)
			}
			if height == f.block.Height {
				if f.canonicalHash != "" {
					return f.canonicalHash, nil
				}
				return f.block.Hash, nil
			}
			return f.tipHash, nil
		case "getrawtransaction":
			var id string
			if err := json.Unmarshal(params[0], &id); err != nil {
				t.Error(err)
			}
			parent, known := f.parents[id]
			if id == f.parent.Txid || known {
				f.parentCalls++
				if f.inputHook != nil {
					f.inputHook()
				}
				if known {
					return parent, nil
				}
				return f.parent, nil
			}
			if !strings.EqualFold(id, f.inbound.Tx.ID.String()) {
				t.Errorf("unexpected funding tx lookup %s", id)
			}
			switch f.location {
			case "absent":
				return nil, map[string]any{"code": -5, "message": "No such mempool or blockchain transaction"}
			case "rpc-error":
				return nil, map[string]any{"code": -28, "message": "Loading block index"}
			case "mempool":
				return map[string]any{"txid": id, "confirmations": 0}, nil
			case "orphan":
				return map[string]any{"txid": id, "blockhash": f.block.Hash, "confirmations": -1}, nil
			default:
				return map[string]any{"txid": id, "blockhash": f.block.Hash, "confirmations": 1 + f.tip - f.block.Height}, nil
			}
		case "getblock":
			var verbosity int
			if err := json.Unmarshal(params[1], &verbosity); err != nil {
				t.Error(err)
			}
			if verbosity == 1 {
				ids := []string{f.inbound.Tx.ID.String()}
				if f.missingMember {
					ids = nil
				}
				return btcjson.GetBlockVerboseResult{Height: f.block.Height, Hash: f.block.Hash, Tx: ids}, nil
			}
			if f.missingReward {
				return map[string]any{"height": f.block.Height, "hash": f.block.Hash, "tx": []any{map[string]any{"vin": []any{map[string]any{"coinbase": "00"}}, "vout": []any{map[string]any{}}}, f.block.Tx[1]}}, nil
			}
			if f.missingVerbose || f.missingMember {
				copyBlock := f.block
				copyBlock.Tx = copyBlock.Tx[:1]
				return copyBlock, nil
			}
			return f.block, nil
		default:
			t.Errorf("unexpected refund funding RPC %s", method)
			return nil, nil
		}
	}, refundFundingRPCHooks{
		beforeRequest: func() {
			f.mu.Lock()
			delay := f.requestDelay
			f.mu.Unlock()
			time.Sleep(delay)
		},
		batchResponse: func(results []map[string]any) []map[string]any {
			f.mu.Lock()
			f.batchSizes = append(f.batchSizes, len(results))
			hook := f.batchHook
			f.mu.Unlock()
			if hook != nil {
				return hook(results)
			}
			return results
		},
		batchEnvelope: func(results []map[string]any) any {
			f.mu.Lock()
			hook := f.batchEnvelope
			f.mu.Unlock()
			if hook != nil {
				return hook(results)
			}
			return results
		},
	})
	f.client.cfg.MinConfirmations = 2
	f.policy = &refundFundingPolicyBridge{}
	f.client.bridge = f.policy
	f.client.regexpRemoveTrailingZeros = regexp.MustCompile("(?:00)+$")
	vaultKey, senderKey := ttypes.GetRandomPubKey(), ttypes.GetRandomPubKey()
	f.policy.keys = []thorclient.PubKeyContractAddressPair{{PubKey: vaultKey, Algo: common.SigningAlgoSecp256k1}}
	vault, err := vaultKey.GetAddress(chain)
	require.NoError(t, err)
	sender, err := senderKey.GetAddress(chain)
	require.NoError(t, err)
	vaultScript, err := f.client.getSourceScript(stypes.TxOutItem{VaultPubKey: vaultKey})
	require.NoError(t, err)
	senderScript, err := f.client.getSourceScript(stypes.TxOutItem{VaultPubKey: senderKey})
	require.NoError(t, err)
	vaultAddress, senderAddress := f.client.refundFundingAddress(vault.String()), f.client.refundFundingAddress(sender.String())
	f.parent = btcjson.TxRawResult{Txid: strings.Repeat("b", 64), Vout: []btcjson.Vout{{Value: 1.001, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: hex.EncodeToString(senderScript), Addresses: []string{senderAddress}}}}}
	const memo = "=:ETH.ETH:0x1234"
	id := common.TxID(strings.Repeat("a", 64))
	f.block = btcjson.GetBlockVerboseTxResult{Height: 100, Hash: strings.Repeat("c", 64), Tx: []btcjson.TxRawResult{
		{Txid: strings.Repeat("d", 64), Vin: []btcjson.Vin{{Coinbase: "00"}}, Vout: []btcjson.Vout{{Value: 50}}},
		{Txid: id.String(), Vin: []btcjson.Vin{{Txid: f.parent.Txid, Sequence: 0xffffffff}}, Vout: []btcjson.Vout{
			{Value: 1, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: hex.EncodeToString(vaultScript), Addresses: []string{vaultAddress}}},
			{ScriptPubKey: btcjson.ScriptPubKeyResult{Type: "nulldata", Hex: hex.EncodeToString(append([]byte{0x6a, byte(len(memo))}, []byte(memo)...))}},
		}},
	}}
	coins := common.Coins{common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(100_000_000))}
	f.inbound = common.NewObservedTx(common.NewTx(id, common.Address(senderAddress), common.Address(vaultAddress), coins, nil, memo), 102, vaultKey, 102)
	f.client.asgardCache.Store(&sutxo.AsgardCache{Addresses: []common.Address{vault}, FetchedAt: time.Now().Add(time.Hour)})
	return f
}

func TestUTXORefundFundingCanonicalAndRemined(t *testing.T) {
	for _, chain := range []common.Chain{common.BTCChain, common.BCHChain, common.LTCChain, common.DOGEChain, common.ZECChain} {
		t.Run(chain.String(), func(t *testing.T) {
			f := newUTXORefundFundingFixture(t, chain)
			require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
			// The source vault can have retired, and a re-mine can be later than
			// the finalise height encoded in the already credited observation.
			f.client.asgardCache.Store(&sutxo.AsgardCache{FetchedAt: time.Now().Add(time.Hour)})
			f.policy.keys = nil
			f.block.Height = 110
			f.block.Hash = strings.Repeat("e", 64)
			f.tip = 112
			require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
		})
	}
}

func TestUTXORefundFundingHoldsPhantomsAndUncertainty(t *testing.T) {
	for _, mode := range []string{"absent", "mempool", "orphan", "rpc-error", "wrong-canonical-hash", "missing-membership", "missing-verbose-funding", "missing-reward", "insufficient-confirmations"} {
		t.Run(mode, func(t *testing.T) {
			f := newUTXORefundFundingFixture(t, common.BTCChain)
			switch mode {
			case "wrong-canonical-hash":
				f.canonicalHash = strings.Repeat("e", 64)
			case "missing-membership":
				f.missingMember = true
			case "missing-verbose-funding":
				f.missingVerbose = true
			case "missing-reward":
				f.missingReward = true
			case "insufficient-confirmations":
				f.tip = 101
			default:
				f.location = mode
			}
			require.Error(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
			f.location, f.canonicalHash, f.missingMember, f.missingVerbose, f.missingReward, f.tip = "canonical", "", false, false, false, 105
			require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "a hold must remain retryable")
		})
	}
}

func TestUTXORefundFundingBindsCreditedDeposit(t *testing.T) {
	for _, field := range []string{"sender", "destination", "amount", "vault"} {
		t.Run(field, func(t *testing.T) {
			f := newUTXORefundFundingFixture(t, common.BTCChain)
			changed := f.inbound
			switch field {
			case "sender":
				changed.Tx.FromAddress = changed.Tx.ToAddress
			case "destination":
				changed.Tx.ToAddress = changed.Tx.FromAddress
			case "memo":
				changed.Tx.Memo += ":changed"
			case "amount":
				changed.Tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(200_000_000))}
			case "vault":
				changed.ObservedPubKey = ttypes.GetRandomPubKey()
			}
			require.Error(t, f.client.ValidateRefundFunding(context.Background(), changed))
		})
	}
}

func TestUTXORefundFundingRejectsChangingView(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	f.inputHook = func() { f.tipHash = strings.Repeat("e", 64) }
	require.ErrorContains(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "chain view changed")
}

func TestUTXORefundFundingCancelledRequest(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, f.client.ValidateRefundFunding(ctx, f.inbound))
}

func TestUTXORefundFundingBlockIncludesCurrentPolicyBatch(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	copyDeposit := f.block.Tx[1]
	copyDeposit.Txid = strings.Repeat("9", 64)
	copyDeposit.Vout = append([]btcjson.Vout(nil), copyDeposit.Vout...)
	copyDeposit.Vout[0].Value = 500
	f.block.Tx = append(f.block.Tx, copyDeposit)
	asgards := f.client.asgardCache.Load().Addresses
	batch, funding, err := f.client.refundFundingBatch(context.Background(), &f.block, asgards, f.inbound.Tx.ID)
	require.NoError(t, err)
	require.NotNil(t, funding)
	require.Len(t, batch.TxArray, 2, "whole-block policy must include the other vault deposit")
	required, err := f.client.refundFundingRequirement(batch, asgards, cosmos.NewUint(50*common.One))
	require.NoError(t, err)
	f.tip = f.block.Height + required - 1
	require.ErrorContains(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "insufficient canonical confirmations")
	f.tip++
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
}

func TestUTXORefundFundingFiltersUnrelatedRejectedDeposit(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	rejected := f.block.Tx[1]
	rejected.Txid = strings.Repeat("8", 64)
	rejected.Vout = append([]btcjson.Vout(nil), rejected.Vout...)
	memo := []byte(strings.Repeat("x", constants.MaxMemoSize+1))
	rejected.Vout[1].ScriptPubKey.Hex = hex.EncodeToString(append([]byte{0x6a, 0x4c, byte(len(memo))}, memo...))
	f.block.Tx = append(f.block.Tx, rejected)
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "a transaction the scanner filters must not permanently strand a valid refund")
}

type refundFundingRPCHooks struct {
	beforeRequest func()
	batchResponse func([]map[string]any) []map[string]any
	batchEnvelope func([]map[string]any) any
}

// No temporal store is initialized: the guard must work without !115 metadata.
func newRefundFundingRPCClient(t *testing.T, chain common.Chain, respond func(string, []json.RawMessage) (any, any), hooks ...refundFundingRPCHooks) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		for _, hook := range hooks {
			if hook.beforeRequest != nil {
				hook.beforeRequest()
			}
		}
		type request struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string
			Params  []json.RawMessage
			ID      any
		}
		answer := func(req request) map[string]any {
			result, rpcErr := respond(req.Method, req.Params)
			return map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result, "error": rpcErr}
		}
		var response any
		if strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
			var requests []request
			if err := json.Unmarshal(body, &requests); err != nil {
				t.Error(err)
				return
			}
			results := make([]map[string]any, len(requests))
			for i, req := range requests {
				// Zebra's legacy middleware rewrites individual objects only;
				// its batch members must already use strict JSON-RPC 2.0.
				if chain.Equals(common.ZECChain) && (req.JSONRPC != "2.0" || len(req.Params) != 2 || strings.TrimSpace(string(req.Params[1])) != "1") {
					results[i] = map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32600, "message": "Zebra batch requires JSON-RPC 2.0 and numeric verbosity"}}
					continue
				}
				results[i] = answer(req)
			}
			for _, hook := range hooks {
				if hook.batchResponse != nil {
					results = hook.batchResponse(results)
				}
			}
			response = results
			for _, hook := range hooks {
				if hook.batchEnvelope != nil {
					response = hook.batchEnvelope(results)
				}
			}
		} else {
			var req request
			if err := json.Unmarshal(body, &req); err != nil {
				t.Error(err)
				return
			}
			response = answer(req)
		}
		if r.Context().Err() != nil {
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil && r.Context().Err() == nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	daemon, err := rpc.NewClient(server.URL, "", "", 0, time.Second, chain, zerolog.Nop())
	require.NoError(t, err)
	return &Client{rpc: daemon, log: zerolog.Nop(), cfg: config.BifrostChainConfiguration{ChainID: chain, RPCHost: server.URL}}
}

func TestUTXORefundFundingResolvedMemo(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	f.inbound.Tx.Memo = "r:12345"
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
	f.inbound.Tx.Memo = ""
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
}

func TestUTXORefundFundingSkipsDustParents(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	dust := f.block.Tx[1]
	dust.Txid = strings.Repeat("7", 64)
	dust.Vin = []btcjson.Vin{{Txid: strings.Repeat("6", 64), Sequence: 0xffffffff}}
	dust.Vout = append([]btcjson.Vout(nil), dust.Vout...)
	dust.Vout[0].Value = 0.000006
	f.block.Tx = append(f.block.Tx, dust)
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "fixture rejects any unexpected parent RPC, so dust cannot trigger a lookup")
}

func TestUTXORefundFundingChecksParentIndex(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	f.block.Tx[1].Vin[0].Vout = 99
	require.ErrorContains(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "input index")
}

func TestUTXORefundFundingAcceptsAdvancingTip(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	f.inputHook = func() { f.tip++ }
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
}

func TestUTXORefundFundingRejectsRetreatingTip(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	f.inputHook = func() { f.tip-- }
	require.ErrorContains(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "chain view changed")
}

func TestUTXORefundFundingWholeBlockMaturityFromEntryPoint(t *testing.T) {
	if common.CurrentChainNetwork == common.MockNet {
		t.Skip("mocknet caps amount-derived confirmations")
	}
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	sibling := f.block.Tx[1]
	sibling.Txid = strings.Repeat("9", 64)
	sibling.Vout = append([]btcjson.Vout(nil), sibling.Vout...)
	sibling.Vout[0].Value = 500
	f.block.Tx = append(f.block.Tx, sibling)
	require.ErrorContains(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "insufficient canonical confirmations")
}

func addUTXORefundFundingParents(f *utxoRefundFundingFixture, count int) {
	f.parents = make(map[string]btcjson.TxRawResult, count)
	for i := 1; i < count; i++ {
		tx := f.block.Tx[1]
		tx.Txid = fmt.Sprintf("%064x", i)
		tx.Vin = append([]btcjson.Vin(nil), tx.Vin...)
		tx.Vin[0].Txid = fmt.Sprintf("%064x", 10000+i)
		tx.Vout = append([]btcjson.Vout(nil), tx.Vout...)
		tx.Vout[0].Value = float64(f.client.cfg.ChainID.DustThreshold().Uint64()) / float64(common.One)
		parent := f.parent
		parent.Txid = tx.Vin[0].Txid
		f.parents[parent.Txid] = parent
		f.block.Tx = append(f.block.Tx, tx)
	}
}

func TestUTXORefundFundingBatchesDistinctParentsWithinDeadline(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	addUTXORefundFundingParents(f, 1000)
	// The unchanged serial implementation needs over 31 seconds of transport
	// time. Two bounded batches leave room for the complete canonical proof.
	f.requestDelay = 31 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, f.client.ValidateRefundFunding(ctx, f.inbound))
	require.Equal(t, []int{500, 500}, f.batchSizes)
	require.Equal(t, 1000, f.parentCalls)
	t.Logf("1000 distinct parents and 31ms per HTTP request validated in %s", time.Since(start))
}

func TestUTXORefundFundingReusesBlockAndSharedParents(t *testing.T) {
	for _, inBlock := range []bool{false, true} {
		t.Run(fmt.Sprintf("in-block-%t", inBlock), func(t *testing.T) {
			f := newUTXORefundFundingFixture(t, common.BTCChain)
			sibling := f.block.Tx[1]
			sibling.Txid = strings.Repeat("9", 64)
			f.block.Tx = append(f.block.Tx, sibling)
			if inBlock {
				f.block.Tx = append(f.block.Tx, f.parent)
			}
			require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
			if inBlock {
				require.Zero(t, f.parentCalls)
				require.Empty(t, f.batchSizes)
			} else {
				require.Equal(t, 1, f.parentCalls)
				require.Equal(t, []int{1}, f.batchSizes)
			}
		})
	}
}

func TestUTXORefundFundingValidatesParentBatchResponses(t *testing.T) {
	for _, mode := range []string{"out-of-order", "missing", "duplicate-id", "unknown-id", "missing-id", "wrong-parent", "null-parent", "item-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newUTXORefundFundingFixture(t, common.BTCChain)
			addUTXORefundFundingParents(f, 2)
			f.batchHook = func(results []map[string]any) []map[string]any {
				switch mode {
				case "out-of-order":
					results[0], results[1] = results[1], results[0]
				case "missing":
					return results[:1]
				case "duplicate-id":
					results[1]["id"] = results[0]["id"]
				case "unknown-id":
					results[1]["id"] = 99
				case "missing-id":
					delete(results[1], "id")
				case "wrong-parent":
					parent, ok := results[1]["result"].(btcjson.TxRawResult)
					if !ok {
						t.Errorf("expected parent transaction, got %T", results[1]["result"])
						return results
					}
					parent.Txid = strings.Repeat("e", 64)
					results[1]["result"] = parent
				case "null-parent":
					results[1]["result"] = nil
				case "item-error":
					results[1]["error"] = map[string]any{"code": -5, "message": "parent unavailable"}
					results[1]["result"] = nil
				}
				return results
			}
			err := f.client.ValidateRefundFunding(context.Background(), f.inbound)
			if mode == "out-of-order" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				f.mu.Lock()
				f.batchHook = nil
				f.mu.Unlock()
				require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound), "a partial batch must not poison retry state")
			}
		})
	}
}

func TestUTXORefundFundingParentBatchCancellation(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.batchHook = func(results []map[string]any) []map[string]any {
		close(entered)
		<-release
		return results
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.client.ValidateRefundFunding(ctx, f.inbound) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("parent batch was not reached")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("parent batch did not honor caller cancellation")
	}
	close(release)
}

func TestUTXORefundFundingParentResponseDecodeBounds(t *testing.T) {
	id := strings.Repeat("a", 64)
	valid := fmt.Sprintf(`[{"id":1,"result":{"txid":%q}}]`, id)
	parents, err := decodeRefundFundingParents(strings.NewReader(valid), []string{id}, int64(len(valid)))
	require.NoError(t, err)
	require.Len(t, parents, 1)
	_, err = decodeRefundFundingParents(strings.NewReader(valid), []string{id}, int64(len(valid)-1))
	require.ErrorContains(t, err, "response exceeds")
	require.ErrorIs(t, err, errRefundFundingParentBatchTooLarge)
	_, err = decodeRefundFundingParents(strings.NewReader(`[{"id":1,"result":`), []string{id}, 1024)
	require.Error(t, err, "a truncated response must not authorize funding")
	_, err = decodeRefundFundingParents(strings.NewReader(valid+`null`), []string{id}, 1024)
	require.Error(t, err, "trailing data must not authorize funding")
}

func TestUTXORefundFundingSharedParentDifferentOutputs(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	// One parent funds both a user and an Asgard change output. The two
	// candidates therefore have different senders despite sharing a parent ID.
	vaultOutput := f.block.Tx[1].Vout[0]
	vaultOutput.Value = 501.001
	f.parent.Vout = append(f.parent.Vout, vaultOutput)
	sibling := f.block.Tx[1]
	sibling.Txid = strings.Repeat("9", 64)
	sibling.Vin = append([]btcjson.Vin(nil), sibling.Vin...)
	sibling.Vin[0].Vout = 1
	payout := f.parent.Vout[0]
	payout.Value = 1
	change := vaultOutput
	change.Value = 500
	sibling.Vout = []btcjson.Vout{payout, change, sibling.Vout[1]}
	f.block.Tx = append(f.block.Tx, sibling)
	// Reusing the user's sender for both referenced outputs would misclassify
	// the 500 BTC change as a deposit and fail the whole-block maturity gate.
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
	require.Equal(t, 1, f.parentCalls)
	require.Equal(t, []int{1}, f.batchSizes)
	// Check extraction directly as well, since mocknet caps amount-derived
	// confirmations and would otherwise hide the extra deposit classification.
	asgards := f.client.asgardCache.Load().Addresses
	batch, funding, err := f.client.refundFundingBatch(context.Background(), &f.block, asgards, f.inbound.Tx.ID)
	require.NoError(t, err)
	require.NotNil(t, funding)
	require.Len(t, batch.TxArray, 1)
}

func TestUTXORefundFundingZebraBatchProtocol(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.ZECChain)
	addUTXORefundFundingParents(f, 2)
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
	require.Equal(t, []int{2}, f.batchSizes)
	require.Equal(t, 2, f.parentCalls)
}

func TestUTXORefundFundingShrinksOversizedParentBatches(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.BTCChain)
	output := f.parent.Vout[0]
	output.ScriptPubKey.Asm = "0 " + output.ScriptPubKey.Hex[4:]
	output.ScriptPubKey.Type = "witness_v0_keyhash"
	output.ScriptPubKey.ReqSigs = 1
	f.parent.Vout = make([]btcjson.Vout, 400)
	for i := range f.parent.Vout {
		f.parent.Vout[i] = output
		f.parent.Vout[i].N = uint32(i)
	}
	encoded, err := json.Marshal(f.parent)
	require.NoError(t, err)
	require.Greater(t, int64(len(encoded))*500, refundFundingMaxParentBatchBytes)
	require.Less(t, int64(len(encoded)+256)*250, refundFundingMaxParentBatchBytes)
	addUTXORefundFundingParents(f, 500)
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
	require.Equal(t, []int{500, 250, 250}, f.batchSizes, "retry the same parents at half size, then keep the smaller bound")
	require.Equal(t, 1000, f.parentCalls, "the first oversized batch is retried, and no successful batch is accumulated or re-fetched")
}

func TestUTXORefundFundingShrinksZebraAggregateCapacityError(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.ZECChain)
	addUTXORefundFundingParents(f, 500)
	f.batchEnvelope = func(results []map[string]any) any {
		if len(results) > 250 {
			return map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32011, "message": "The batch response was too large", "data": "Exceeded max limit of 52428800"}}
		}
		return results
	}
	require.NoError(t, f.client.ValidateRefundFunding(context.Background(), f.inbound))
	require.Equal(t, []int{500, 250, 250}, f.batchSizes)
}

func TestUTXORefundFundingHoldsOtherZebraErrors(t *testing.T) {
	for _, mode := range []string{"other-code", "wrong-message", "single-response-overflow", "per-item", "non-null-id", "wrong-version"} {
		t.Run(mode, func(t *testing.T) {
			f := newUTXORefundFundingFixture(t, common.ZECChain)
			addUTXORefundFundingParents(f, 2)
			f.batchEnvelope = func(results []map[string]any) any {
				rpcErr := map[string]any{"code": -32011, "message": "The batch response was too large"}
				envelope := map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcErr}
				switch mode {
				case "other-code":
					rpcErr["code"] = -32603
				case "wrong-message":
					rpcErr["message"] = "The response was too large"
				case "single-response-overflow":
					rpcErr["code"] = -32008
				case "per-item":
					results[0]["error"] = rpcErr
					results[0]["result"] = nil
					return results
				case "non-null-id":
					envelope["id"] = 1
				case "wrong-version":
					envelope["jsonrpc"] = "1.0"
				}
				return envelope
			}
			err := f.client.ValidateRefundFunding(context.Background(), f.inbound)
			require.Error(t, err)
			require.NotErrorIs(t, err, errRefundFundingParentBatchTooLarge)
			require.Equal(t, []int{2}, f.batchSizes, "non-capacity errors must hold without splitting")
		})
	}
}

func TestUTXORefundFundingHoldsSingletonZebraCapacityError(t *testing.T) {
	f := newUTXORefundFundingFixture(t, common.ZECChain)
	f.batchEnvelope = func([]map[string]any) any {
		return map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32011, "message": "The batch response was too large"}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := f.client.ValidateRefundFunding(ctx, f.inbound)
	require.ErrorIs(t, err, errRefundFundingParentBatchTooLarge)
	require.Equal(t, []int{1}, f.batchSizes, "an oversized singleton cannot be split or allowed")
}
