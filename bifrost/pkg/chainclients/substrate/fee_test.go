package substrate

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

func (s *ScannerSuite) TestReportNetworkFeeArithmetic(c *C) {
	for _, tt := range []struct {
		name       string
		sample     uint64
		last       uint64
		resolution int64
		want       uint64
		skip       bool
	}{
		{"sum exceeds uint64", math.MaxUint64, math.MaxUint64, 0, math.MaxUint64, false},
		{"average rounds down", math.MaxUint64, math.MaxUint64 - 1, 0, math.MaxUint64 - 1, false},
		{"average, not median", 100, 50_000, 0, 349, false},
		{"aligned at maximum", math.MaxUint64, math.MaxUint64, 3, math.MaxUint64, false},
		{"aligned below maximum", math.MaxUint64 - 1, math.MaxUint64 - 1, 2, math.MaxUint64 - 1, false},
		{"round up near maximum", math.MaxUint64 - 2, math.MaxUint64 - 2, 2, math.MaxUint64 - 1, false},
		{"rounding exceeds uint64", math.MaxUint64, math.MaxUint64, 2, 0, true},
	} {
		sc, queue := newTestScannerWithFeeQueue(tt.resolution)
		sc.currentFee.Store(42)
		for i := 0; i < FeeCacheSize-1; i++ {
			sc.updateFeeCache(tt.sample)
		}
		sc.updateFeeCache(tt.last)
		original := append([]uint64(nil), sc.feeCache...)
		sc.updateNetworkFee(100)
		c.Check(sc.feeCache, DeepEquals, original)
		if tt.skip {
			c.Check(len(queue), Equals, 0, Commentf("%s", tt.name))
			c.Check(sc.currentFee.Load(), Equals, uint64(42))
			continue
		}
		c.Assert(len(queue), Equals, 1, Commentf("%s", tt.name))
		c.Check((<-queue).TransactionRate, Equals, tt.want, Commentf("%s", tt.name))
		c.Check(sc.currentFee.Load(), Equals, tt.want)
	}

	sc, queue := newTestScannerWithFeeQueue(2)
	sc.params.DefaultFee = math.MaxUint64
	sc.currentFee.Store(42)
	sc.updateNetworkFee(100)
	c.Check(len(queue), Equals, 0)
	c.Check(sc.currentFee.Load(), Equals, uint64(42))
}

func (s *ScannerSuite) TestFetchTxsExcludesTipsFromFeeSamples(c *C) {
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
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
		transfer := stypes.BalanceTransferKeepAlive(params.Index(BalancesPallet), params.Index(BalancesTransferKeepAliveCall), make([]byte, 32), cosmos.NewUint(5_000_000_000))

		for _, fee := range []stypes.Fee{{Total: 150_000, Tip: 50_000}, {Total: 150_000}, {Total: 150_000, Tip: 150_000}, {}} {
			func() {
				payload := make([]byte, 32+2*params.BalanceSize)
				binary.LittleEndian.PutUint64(payload[32:], fee.Total)
				binary.LittleEndian.PutUint64(payload[32+params.BalanceSize:], fee.Tip)
				events := stypes.EncodeCompact(2)
				events = append(events, 0, 0, 0, 0, 0, params.Index(TransactionPaymentPallet), 0)
				events = append(events, payload...)
				events = append(events, 0) // No topics.
				events = append(events, 0, 0, 0, 0, 0, params.Index(SystemPallet), 0)
				events = append(events, 0, 0, 0, 0, 0) // Weight, dispatch class, pays fee, and topics.
				hash := "0x" + strings.Repeat("11", 32)
				h := newRPCHandler()
				h.on("chain_getBlockHash", func(_ json.RawMessage) (interface{}, error) { return hash, nil })
				h.on("state_getRuntimeVersion", func(_ json.RawMessage) (interface{}, error) {
					return rpc.RuntimeVersion{SpecVersion: 1, TransactionVersion: 1}, nil
				})
				h.on("chain_getBlock", func(_ json.RawMessage) (interface{}, error) {
					return rpc.SignedBlock{Block: rpc.Block{Extrinsics: []string{encode(transfer)}}}, nil
				})
				h.on("state_getStorage", func(raw json.RawMessage) (interface{}, error) {
					var args []string
					if err := json.Unmarshal(raw, &args); err != nil {
						return nil, err
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
				sc.runtime = runtimeContext{metadata: metadata, layout: mustTestExtrinsicLayout(chain), specVersion: 1, txVersion: 1}
				txIn, err := sc.FetchTxs(100, 0)
				c.Assert(err, IsNil, Commentf("%s: %+v", chain, fee))
				c.Assert(txIn.TxArray, HasLen, 1)
				gas := chain.NativeGasToThorCoinCeil(cosmos.NewUint(fee.Total))
				if gas.Amount.IsZero() {
					gas.Amount = cosmos.OneUint()
				}
				c.Check(txIn.TxArray[0].Gas[0], DeepEquals, gas)
				if fee.Total > fee.Tip {
					c.Check(sc.feeCache, DeepEquals, []uint64{fee.Total - fee.Tip})
				} else {
					c.Check(sc.feeCache, HasLen, 0)
				}
			}()
		}
	}
}
