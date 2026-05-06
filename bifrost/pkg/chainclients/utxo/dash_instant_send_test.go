package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

type dashRPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     any               `json:"id"`
}

type dashTestPoolMgr struct{}

func (dashTestPoolMgr) GetValue(_ common.Asset, _ common.Asset, amount cosmos.Uint) (cosmos.Uint, error) {
	return amount.MulUint64(priceDashUSDForSimulationTesting), nil
}

func newDashTestAddress(t *testing.T, seed byte) string {
	t.Helper()

	hash := bytes.Repeat([]byte{seed}, 20)
	addr, err := btcutil.NewAddressPubKeyHash(hash, &dashutil.RegressionNetParams)
	if err != nil {
		t.Fatalf("new address: %v", err)
	}
	return addr.String()
}

func newDashP2PKHScriptHex(t *testing.T, address string) string {
	t.Helper()

	addr, err := btcutil.DecodeAddress(address, &dashutil.RegressionNetParams)
	if err != nil {
		t.Fatalf("decode address: %v", err)
	}
	script, err := btctxscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("pay-to-addr script: %v", err)
	}
	return hex.EncodeToString(script)
}

func newDashMemoScriptHex(t *testing.T, memo string) string {
	t.Helper()

	script, err := btctxscript.NullDataScript([]byte(memo))
	if err != nil {
		t.Fatalf("nulldata script: %v", err)
	}
	return hex.EncodeToString(script)
}

func newDashTestServer(t *testing.T, handler func(method string, params []json.RawMessage) any) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req dashRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  handler(req.Method, req.Params),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
}

func newDashClientForRPCServer(t *testing.T, serverURL string) *Client {
	t.Helper()

	rpcClient, err := rpc.NewClient(serverURL, "thorchain", "password", 0, time.Second, common.DASHChain, zerolog.Nop())
	if err != nil {
		t.Fatalf("new rpc client: %v", err)
	}

	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() {
		_ = storage.Close()
	})

	temporalStorage, err := sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 1000)
	if err != nil {
		t.Fatalf("new temporal storage: %v", err)
	}

	return &Client{
		cfg: config.BifrostChainConfiguration{
			ChainID:             common.DASHChain,
			ParallelMempoolScan: 1,
		},
		log:             zerolog.Nop(),
		rpc:             rpcClient,
		temporalStorage: temporalStorage,
		poolMgr:         dashTestPoolMgr{},
		instantSendCfg: config.DashInstantSendConfiguration{
			Enabled:               true,
			USDThreshold:          100,
			MinFeeRateSatPerVByte: 1.5,
			MaxMempoolAge:         2 * time.Minute,
		},
	}
}

func TestDashGetHeightUsesBestChainlock(t *testing.T) {
	server := newDashTestServer(t, func(method string, _ []json.RawMessage) any {
		if method != "getbestchainlock" {
			t.Fatalf("unexpected method: %s", method)
		}
		return map[string]any{
			"blockhash":   "0000000000000000000000000000000000000000000000000000000000000123",
			"height":      321,
			"signature":   "sig",
			"known_block": true,
		}
	})
	defer server.Close()

	client := newDashClientForRPCServer(t, server.URL)

	height, err := client.GetHeight()
	if err != nil {
		t.Fatalf("GetHeight() error = %v", err)
	}
	if height != 321 {
		t.Fatalf("GetHeight() = %d, want 321", height)
	}
}

func TestDashRawMempoolVerboseFeeRateMilliSatPerVByte(t *testing.T) {
	tests := []struct {
		name  string
		entry rpc.GetRawMempoolVerboseResult
		want  uint64
	}{
		{
			name: "legacy top-level fee",
			entry: rpc.GetRawMempoolVerboseResult{
				Vsize: 250,
				Fee:   0.0001,
			},
			want: 40000,
		},
		{
			name: "fractional fee rate",
			entry: rpc.GetRawMempoolVerboseResult{
				Vsize: 250,
				Fees: rpc.MempoolFees{
					Base: 0.00000375,
				},
			},
			want: 1500,
		},
		{
			name: "dash core nested fee",
			entry: rpc.GetRawMempoolVerboseResult{
				Vsize: 240,
				Fees: rpc.MempoolFees{
					Base: 0.00015,
				},
			},
			want: 62500,
		},
		{
			name: "round to nearest millisat",
			entry: rpc.GetRawMempoolVerboseResult{
				Vsize: 3,
				Fee:   0.00000002,
			},
			want: 667,
		},
		{
			name: "zero vsize",
			entry: rpc.GetRawMempoolVerboseResult{
				Vsize: 0,
				Fee:   0.0001,
			},
			want: 0,
		},
		{
			name: "negative fee",
			entry: rpc.GetRawMempoolVerboseResult{
				Vsize: 250,
				Fee:   -0.0001,
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.entry.FeeRateMilliSatPerVByte()
			if got != tt.want {
				t.Fatalf("FeeRateMilliSatPerVByte() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDashAllowInstantSendPolicyBoundaries(t *testing.T) {
	client := Client{
		instantSendCfg: config.DashInstantSendConfiguration{
			USDThreshold:          10,
			MinFeeRateSatPerVByte: 1.5,
		},
	}
	thresholdUSD := cosmos.NewUint(10).MulUint64(common.One)

	allow, reason := client.allowInstantSend(1499, 0, true, cosmos.ZeroUint())
	if allow {
		t.Fatalf("allowInstantSend() = true, want false")
	}
	if reason != "fee rate too low: 1.499 sat/vB < 1.500 sat/vB" {
		t.Fatalf("reason = %q", reason)
	}

	allow, reason = client.allowInstantSend(1500, 0, false, thresholdUSD)
	if allow {
		t.Fatalf("allowInstantSend() = true, want false")
	}
	if reason != "usd value 10.000000 >= threshold 10" {
		t.Fatalf("reason = %q", reason)
	}

	allow, reason = client.allowInstantSend(1500, 0, false, thresholdUSD.Sub(cosmos.NewUint(1)))
	if !allow {
		t.Fatalf("allowInstantSend() = false, want true: %s", reason)
	}
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDashGetInstantSendLocksFiltersUnlockedTxs(t *testing.T) {
	asgardAddress := newDashTestAddress(t, 0x11)
	senderAddress := newDashTestAddress(t, 0x22)

	mempoolLocked := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mempoolUnlocked := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	prevLocked := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	prevUnlocked := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	lockedOutputScript := newDashP2PKHScriptHex(t, asgardAddress)
	senderScript := newDashP2PKHScriptHex(t, senderAddress)
	memoScript := newDashMemoScriptHex(t, "NOOP")

	server := newDashTestServer(t, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{
				mempoolLocked: map[string]any{
					"size":   200,
					"vsize":  200,
					"weight": 800,
					"fees": map[string]any{
						"base": 0.00002,
					},
					"time":   time.Now().Unix(),
					"height": 123,
				},
				mempoolUnlocked: map[string]any{
					"size":   200,
					"vsize":  200,
					"weight": 800,
					"fee":    0.00002,
					"time":   time.Now().Unix(),
					"height": 123,
				},
			}
		case "getrawtransaction":
			var txid string
			if err := json.Unmarshal(params[0], &txid); err != nil {
				t.Fatalf("decode txid: %v", err)
			}

			switch txid {
			case mempoolLocked:
				return map[string]any{
					"txid":                 mempoolLocked,
					"hash":                 mempoolLocked,
					"version":              2,
					"locktime":             0,
					"confirmations":        0,
					"instantlock":          true,
					"instantlock_internal": false,
					"vin":                  []map[string]any{{"txid": prevLocked, "vout": 0, "sequence": uint32(4294967295)}},
					"vout":                 []map[string]any{{"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": lockedOutputScript, "type": "pubkeyhash", "addresses": []string{asgardAddress}}}, {"n": 1, "value": 0.0, "scriptPubKey": map[string]any{"hex": memoScript, "type": "nulldata"}}},
				}
			case mempoolUnlocked:
				return map[string]any{
					"txid":                 mempoolUnlocked,
					"hash":                 mempoolUnlocked,
					"version":              2,
					"locktime":             0,
					"confirmations":        0,
					"instantlock":          false,
					"instantlock_internal": false,
					"vin":                  []map[string]any{{"txid": prevUnlocked, "vout": 0, "sequence": uint32(4294967295)}},
					"vout":                 []map[string]any{{"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": lockedOutputScript, "type": "pubkeyhash", "addresses": []string{asgardAddress}}}, {"n": 1, "value": 0.0, "scriptPubKey": map[string]any{"hex": memoScript, "type": "nulldata"}}},
				}
			case prevLocked, prevUnlocked:
				return map[string]any{
					"txid":          txid,
					"hash":          txid,
					"version":       2,
					"locktime":      0,
					"confirmations": 1,
					"vin":           []map[string]any{{"txid": "coinbase", "vout": 0}},
					"vout":          []map[string]any{{"n": 0, "value": 0.10002, "scriptPubKey": map[string]any{"hex": senderScript, "type": "pubkeyhash", "addresses": []string{senderAddress}}}},
				}
			default:
				t.Fatalf("unexpected tx lookup: %s", txid)
			}
		default:
			t.Fatalf("unexpected method: %s", method)
		}
		return nil
	})
	defer server.Close()

	client := newDashClientForRPCServer(t, server.URL)

	addr, err := common.NewAddress(asgardAddress)
	if err != nil {
		t.Fatalf("new asgard address: %v", err)
	}
	client.asgardCache.Store(&sharedutxo.AsgardCache{
		Addresses: []common.Address{addr},
		FetchedAt: time.Now(),
	})

	rawTx, err := client.rpc.GetRawTransactionVerboseDASH(mempoolLocked)
	if err != nil {
		t.Fatalf("GetRawTransactionVerboseDASH() error = %v", err)
	}
	item, err := client.getTxIn(&rawTx.TxRawResult, 123, true, nil)
	if err != nil {
		t.Fatalf("getTxIn() error = %v", err)
	}
	if item.IsEmpty() {
		t.Fatalf("getTxIn() returned empty item")
	}

	txIn, err := client.getInstantSendLocks(123)
	if err != nil {
		t.Fatalf("getInstantSendLocks() error = %v", err)
	}
	if txIn.MemPool {
		t.Fatalf("expected mempool flag to be false for accepted InstantSend observations")
	}
	if txIn.ConfirmationRequired != 0 {
		t.Fatalf("ConfirmationRequired = %d, want 0", txIn.ConfirmationRequired)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("observed tx count = %d, want 1", len(txIn.TxArray))
	}
	if txIn.TxArray[0].Tx != mempoolLocked {
		t.Fatalf("observed tx = %s, want %s", txIn.TxArray[0].Tx, mempoolLocked)
	}
	if txIn.TxArray[0].Sender != senderAddress {
		t.Fatalf("sender = %s, want %s", txIn.TxArray[0].Sender, senderAddress)
	}
	if txIn.TxArray[0].To != asgardAddress {
		t.Fatalf("to = %s, want %s", txIn.TxArray[0].To, asgardAddress)
	}
	if txIn.TxArray[0].Memo != "NOOP" {
		t.Fatalf("memo = %q, want NOOP", txIn.TxArray[0].Memo)
	}
	if got := txIn.TxArray[0].Coins.GetCoin(common.DASHAsset).Amount.Uint64(); got != 10_000_000 {
		t.Fatalf("amount = %d, want 10000000", got)
	}
}
