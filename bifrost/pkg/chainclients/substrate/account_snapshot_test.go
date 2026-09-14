package substrate

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	thorTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func (m *mockBridge) GetAsgards() (thorTypes.Vaults, error) {
	return m.asgards, nil
}

func snapshotAccountHex(nonce uint32, balance cosmos.Uint, balanceSize int) string {
	data := make([]byte, 16+4*balanceSize)
	binary.LittleEndian.PutUint32(data, nonce)
	amount := balance.BigInt().Bytes()
	for i, b := range amount {
		data[16+len(amount)-1-i] = b
	}
	return "0x" + hex.EncodeToString(data)
}

func (s *ClientSuite) TestGetAccountByAddressSnapshot(c *C) {
	const address = "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	blockHash := "0x" + strings.Repeat("11", 32)
	for _, tt := range []struct {
		chain   common.Chain
		height  int64
		balance string
		amount  string
		missing bool
	}{
		{common.TAOChain, 100, "5000000000", "500000000", false},
		{common.DOTChain, 100, "50000000000", "500000000", false},
		{common.DOTChain, 100, "1844674407370955161600", "18446744073709551616", false},
		{common.TAOChain, 0, "0", "0", false},
		{common.TAOChain, 100, "0", "0", true},
		{common.DOTChain, 100, "0", "0", true},
	} {
		func() {
			var storageCalls, nonceCalls atomic.Int32
			h := newRPCHandler()
			h.on("chain_getBlockHash", func(raw json.RawMessage) (interface{}, error) {
				var params []uint64
				if err := json.Unmarshal(raw, &params); err != nil {
					return nil, err
				}
				if len(params) != 1 || params[0] != uint64(tt.height) {
					return nil, fmt.Errorf("unexpected block parameters: %s", raw)
				}
				return blockHash, nil
			})
			h.on("state_getStorage", func(raw json.RawMessage) (interface{}, error) {
				storageCalls.Add(1)
				var params []string
				if err := json.Unmarshal(raw, &params); err != nil {
					return nil, err
				}
				if len(params) != 2 || params[0] == "" || params[1] != blockHash {
					return nil, fmt.Errorf("unexpected storage parameters: %s", raw)
				}
				if tt.missing {
					return nil, nil
				}
				return snapshotAccountHex(7, cosmos.NewUintFromString(tt.balance), ChainParams[tt.chain].BalanceSize), nil
			})
			h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
				nonceCalls.Add(1)
				return uint32(99), nil
			})
			srv := httptest.NewServer(h)
			defer srv.Close()
			cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second), func(cfg *config.BifrostChainConfiguration) {
				cfg.ChainID = tt.chain
			})
			cl.chainScanner.params = ChainParams[tt.chain]

			acct, err := cl.GetAccountByAddress(address, big.NewInt(tt.height))
			c.Assert(err, IsNil, Commentf("%s at %d", tt.chain, tt.height))
			wantNonce := int64(7)
			if tt.missing {
				wantNonce = 0
			}
			c.Check(acct.Sequence, Equals, wantNonce)
			c.Assert(acct.Coins, HasLen, 1)
			c.Check(acct.Coins[0].Asset.Equals(tt.chain.GetGasAsset()), Equals, true)
			c.Check(acct.Coins[0].Amount.String(), Equals, tt.amount)
			c.Check(storageCalls.Load(), Equals, int32(1))
			c.Check(nonceCalls.Load(), Equals, int32(0))
		}()
	}
}

func (s *ClientSuite) TestGetAccountByAddressSnapshotErrors(c *C) {
	for _, tt := range []struct {
		name       string
		height     *big.Int
		hash       interface{}
		hashErr    error
		storageErr error
		wantError  string
		wantHash   int32
		wantReads  int32
	}{
		{name: "negative height", height: big.NewInt(-1), wantError: "invalid account height", wantHash: 0},
		{name: "oversized height", height: new(big.Int).Lsh(big.NewInt(1), 64), wantError: "invalid account height", wantHash: 0},
		{name: "missing hash", height: big.NewInt(100), wantError: "not found", wantHash: 1},
		{name: "empty hash", height: big.NewInt(100), hash: "", wantError: "not found", wantHash: 1},
		{name: "hash RPC error", height: big.NewInt(100), hashErr: errors.New("hash unavailable"), wantError: "hash unavailable", wantHash: 1},
		{name: "pruned state", height: big.NewInt(100), hash: "0x01", storageErr: errors.New("state unavailable"), wantError: "state unavailable", wantHash: 1, wantReads: 1},
	} {
		func() {
			var hashCalls, storageCalls, nonceCalls atomic.Int32
			h := newRPCHandler()
			h.on("chain_getBlockHash", func(_ json.RawMessage) (interface{}, error) {
				hashCalls.Add(1)
				return tt.hash, tt.hashErr
			})
			h.on("state_getStorage", func(_ json.RawMessage) (interface{}, error) {
				storageCalls.Add(1)
				return nil, tt.storageErr
			})
			h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
				nonceCalls.Add(1)
				return uint32(99), nil
			})
			srv := httptest.NewServer(h)
			defer srv.Close()
			cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
			_, err := cl.GetAccountByAddress("5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY", tt.height)
			c.Assert(err, NotNil, Commentf("%s", tt.name))
			c.Check(strings.Contains(err.Error(), tt.wantError), Equals, true, Commentf("%s: %v", tt.name, err))
			c.Check(hashCalls.Load(), Equals, tt.wantHash)
			c.Check(storageCalls.Load(), Equals, tt.wantReads)
			c.Check(nonceCalls.Load(), Equals, int32(0))
		}()
	}
}

func (s *ClientSuite) TestReportSolvencySnapshot(c *C) {
	blockHash := "0x" + strings.Repeat("11", 32)
	h := newRPCHandler()
	h.on("chain_getBlockHash", func(raw json.RawMessage) (interface{}, error) {
		if string(raw) != "[100]" {
			return nil, fmt.Errorf("unexpected block parameters: %s", raw)
		}
		return blockHash, nil
	})
	h.on("state_getStorage", func(raw json.RawMessage) (interface{}, error) {
		var params []string
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		balance := cosmos.NewUint(9_000_000_000)
		if len(params) == 2 && params[1] == blockHash {
			balance = cosmos.NewUint(5_000_000_000)
		}
		return snapshotAccountHex(7, balance, ChainParams[common.TAOChain].BalanceSize), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second), func(cfg *config.BifrostChainConfiguration) {
		cfg.SolvencyBlocks = 10
	})
	bridge, ok := cl.GetBridge().(*mockBridge)
	c.Assert(ok, Equals, true)
	bridge.asgards = thorTypes.Vaults{{
		PubKey: cl.GetPubKey(), PubKeyEddsa: cl.GetPubKey(),
		Coins: common.Coins{common.NewCoin(common.TAOAsset, cosmos.NewUint(1_000_000_000))},
	}}
	m, err := newFeeTestMetrics()
	c.Assert(err, IsNil)
	cl.blockScanner, err = blockscanner.NewBlockScanner(
		config.BifrostBlockScannerConfiguration{ChainID: common.TAOChain, StartBlockHeight: 100},
		cl.storage, m, bridge, cl.chainScanner,
	)
	c.Assert(err, IsNil)
	cl.globalSolvencyQueue = make(chan stypes.Solvency, 1)

	c.Assert(cl.ReportSolvency(100), IsNil)
	select {
	case msg := <-cl.globalSolvencyQueue:
		c.Check(msg.Height, Equals, int64(100))
		c.Check(msg.Chain, Equals, common.TAOChain)
		c.Check(msg.PubKey, Equals, cl.GetPubKey())
		c.Check(msg.Coins.GetCoin(common.TAOAsset).Amount.String(), Equals, "500000000")
	default:
		c.Fatal("missing solvency report")
	}

	// Unavailable historical state must not produce a report with a substitute balance.
	h.on("state_getStorage", func(_ json.RawMessage) (interface{}, error) {
		return nil, errors.New("state unavailable")
	})
	c.Assert(cl.ReportSolvency(100), IsNil)
	c.Check(len(cl.globalSolvencyQueue), Equals, 0)
}
