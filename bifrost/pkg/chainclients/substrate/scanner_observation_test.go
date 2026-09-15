package substrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

func (s *ScannerSuite) TestFetchTxsUnsupportedDestination(c *C) {
	m, metricsErr := newFeeTestMetrics()
	c.Assert(metricsErr, IsNil)
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		counter := m.GetCounterVec(metrics.BlockScanError(chain))
		c.Assert(counter, NotNil)
		value := counter.WithLabelValues("unsupported_observation", "")
		path := "types/testdata/metadata-tao-mainnet-401.hex"
		encode := buildTestExtrinsic
		if chain == common.DOTChain {
			path = "types/testdata/metadata-dot-assethub-2003001.hex"
			encode = buildAssetHubTestExtrinsic
		}
		raw, err := os.ReadFile(path)
		c.Assert(err, IsNil)
		metadata, err := stypes.ParseMetadata(strings.TrimSpace(string(raw)))
		c.Assert(err, IsNil)
		params := ChainParams[chain]
		amount := cosmos.NewUint(5_000_000_000)
		transfer := stypes.BalanceTransferKeepAlive(
			params.Index(BalancesPallet), params.Index(BalancesTransferKeepAliveCall),
			bytes.Repeat([]byte{0xAA}, 32), amount,
		)
		unsupported := append([]byte(nil), transfer...)
		unsupported[2] = 3        // MultiAddress::Address32 has the same payload width as Id.
		truncated := transfer[:3] // Id without its account ID.
		batch := func(call []byte) []byte {
			return stypes.BatchAll(params.Index(UtilityPallet), params.Index(UtilityBatchAllCall), [][]byte{call})
		}
		validHex := encode(transfer)
		validRaw, err := stypes.HexDecode(validHex)
		c.Assert(err, IsNil)

		// Extrinsic 0 failed; extrinsic 1 succeeded. Both have zero-weight dispatch info.
		events := stypes.EncodeCompact(2)
		events = append(events, 0, 0, 0, 0, 0, params.Index(SystemPallet), 1)
		events = append(events, 1, 0, 0, 0, 0, 0) // CannotLookup, dispatch info, no topics.
		events = append(events, 0, 1, 0, 0, 0, params.Index(SystemPallet), 0)
		events = append(events, 0, 0, 0, 0, 0) // Dispatch info, no topics.

		for _, tt := range []struct {
			name    string
			call    []byte
			wantErr bool
		}{
			{"unsupported plain transfer", unsupported, false},
			{"unsupported batched transfer", batch(unsupported), false},
			{"truncated plain transfer", truncated, true},
			{"truncated batched transfer", batch(truncated), true},
		} {
			func() {
				hash := "0x" + strings.Repeat("11", 32)
				h := newRPCHandler()
				h.on("chain_getBlockHash", func(json.RawMessage) (interface{}, error) { return hash, nil })
				h.on("state_getRuntimeVersion", func(json.RawMessage) (interface{}, error) {
					return rpc.RuntimeVersion{SpecVersion: 1, TransactionVersion: 1}, nil
				})
				h.on("chain_getBlock", func(json.RawMessage) (interface{}, error) {
					return rpc.SignedBlock{Block: rpc.Block{Extrinsics: []string{encode(tt.call), validHex}}}, nil
				})
				h.on("state_getStorage", func(raw json.RawMessage) (interface{}, error) {
					var args []string
					if decodeErr := json.Unmarshal(raw, &args); decodeErr != nil {
						return nil, decodeErr
					}
					if len(args) != 2 || args[1] != hash {
						return nil, fmt.Errorf("unexpected storage parameters: %s", raw)
					}
					switch args[0] {
					case "0xf0c365c3cf59d671eb72da0e7a4113c49f1f0515f462cdcf84e0f1d6045dfcbb":
						return "0x0100000000000000", nil
					case "0x26aa394eea5630e07c48ae0c9558cef780d41e5e16056765bc8461851072c9d7":
						return stypes.HexEncode(events), nil
					default:
						return nil, fmt.Errorf("unexpected storage key: %s", args[0])
					}
				})
				srv := httptest.NewServer(h)
				defer srv.Close()
				cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second), func(cfg *config.BifrostChainConfiguration) { cfg.ChainID = chain })
				sc := cl.chainScanner
				sc.params = params
				sc.cfg.ChainID = chain
				sc.rpcClient = cl.rpcClient
				sc.pubkeyMgr = pubkeymanager.NewMockPoolAddressValidator()
				sc.runtime = runtimeContext{metadata: metadata, layout: mustTestExtrinsicLayout(chain), specVersion: 1, txVersion: 1}
				cleaned := false
				sc.failHandler = func(string) error { cleaned = true; return nil }
				var logs bytes.Buffer
				sc.logger = zerolog.New(&logs).With().Stringer("chain", chain).Logger()

				before := testutil.ToFloat64(value)
				txIn, fetchErr := sc.FetchTxs(100, 0)
				c.Check(testutil.ToFloat64(value), Equals, before)
				c.Check(cleaned, Equals, false)
				if tt.wantErr {
					c.Assert(fetchErr, NotNil, Commentf("%s: %s", chain, tt.name))
					c.Check(logs.Len(), Equals, 0)
					c.Check(errors.Is(fetchErr, errUnsupportedObservation), Equals, false)
					c.Check(txIn.TxArray, HasLen, 0)
					_, height := sc.getLatestBlock()
					c.Check(height, Equals, int64(0))
					sc.metrics = m
					_, fetchErr = sc.FetchTxs(100, 0)
					c.Assert(fetchErr, NotNil)
					c.Check(testutil.ToFloat64(value), Equals, before)
					return
				}
				c.Assert(fetchErr, IsNil, Commentf("%s: %s", chain, tt.name))
				var entry struct {
					Level   string `json:"level"`
					Chain   string `json:"chain"`
					Height  int64  `json:"height"`
					Index   int    `json:"index"`
					TxID    string `json:"txid"`
					Error   string `json:"error"`
					Message string `json:"message"`
				}
				c.Assert(json.Unmarshal(logs.Bytes(), &entry), IsNil)
				skippedRaw, decodeErr := stypes.HexDecode(encode(tt.call))
				c.Assert(decodeErr, IsNil)
				c.Check(entry.Level, Equals, "warn")
				c.Check(entry.Chain, Equals, chain.String())
				c.Check(entry.Height, Equals, int64(100))
				c.Check(entry.Index, Equals, 0)
				c.Check(entry.TxID, Equals, stypes.ExtrinsicHash(skippedRaw))
				c.Check(strings.Contains(entry.Error, "unsupported destination address type: 3"), Equals, true)
				c.Check(entry.Message, Equals, "skip unsupported extrinsic")
				c.Assert(txIn.TxArray, HasLen, 1)
				c.Check(txIn.TxArray[0].Tx, Equals, stypes.ExtrinsicHash(validRaw))
				c.Check(txIn.TxArray[0].Coins[0], DeepEquals, chain.NativeGasToThorCoin(amount))
				latestHash, height := sc.getLatestBlock()
				c.Check(latestHash, Equals, hash)
				c.Check(height, Equals, int64(100))

				// Rescanning with metrics records one skipped extrinsic.
				sc.metrics = m
				_, fetchErr = sc.FetchTxs(100, 0)
				c.Assert(fetchErr, IsNil)
				c.Check(testutil.ToFloat64(value), Equals, before+1)

				// Missing chain collectors must not prevent scanning.
				sc.cfg.ChainID = common.Chain("UNREGISTERED")
				c.Assert(m.GetCounterVec(metrics.BlockScanError(sc.cfg.ChainID)), IsNil)
				_, fetchErr = sc.FetchTxs(100, 0)
				c.Assert(fetchErr, IsNil)
				c.Check(testutil.ToFloat64(value), Equals, before+1)
			}()
		}
	}
}
