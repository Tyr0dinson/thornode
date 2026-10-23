package evm

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// reorgBlockJSON is the empty block a witnessProxy returns at any height.
const reorgBlockJSON = `{"parentHash":"0x0000000000000000000000000000000000000000000000000000000000000000","sha3Uncles":"0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347","miner":"0x0000000000000000000000000000000000000000","stateRoot":"0x0000000000000000000000000000000000000000000000000000000000000000","transactionsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421","receiptsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421","logsBloom":"0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000","difficulty":"0x1","number":"0x63","gasLimit":"0x1","gasUsed":"0x0","timestamp":"0x1","extraData":"0x","mixHash":"0x0000000000000000000000000000000000000000000000000000000000000000","nonce":"0x0000000000000000","transactions":[],"uncles":[]}`

// witnessProxy forwards every JSON-RPC call to target, except that it answers
// eth_getTransactionByHash with null (it does not know the transaction) and
// eth_getBlockByNumber with reorgBlockJSON, or with null when behind is set (it has
// not reached the block yet).
func witnessProxy(target string, behind bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(body, &rpcReq)
		rw.Header().Set("Content-Type", "application/json")
		answer := func(result string) {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + string(rpcReq.ID) + `,"result":` + result + `}`))
		}
		switch {
		case rpcReq.Method == "eth_getTransactionByHash", rpcReq.Method == "eth_getBlockByNumber" && behind:
			answer("null")
			return
		case rpcReq.Method == "eth_getBlockByNumber":
			answer(reorgBlockJSON)
			return
		}
		resp, err := http.Post(target+req.URL.Path, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		rw.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(rw, resp.Body)
	}))
}

// After a reorg every read of the plan comes from one witness: the active endpoint,
// once its block at the reorg tip is the parent of the scanned block. Its "not found"
// decides; another endpoint that still has the transaction (here the unstuck suite's
// mock node, which has 0x88df... pending, like a node still on the old fork) cannot
// veto the errata, and an active endpoint that is behind or on another fork cannot
// cause one.
func (s *UnstuckTestSuite) TestReorgIsReadFromOneCaughtUpWitness(c *C) {
	config.Init()
	atTip := witnessProxy(s.server.URL, false)
	defer atTip.Close()
	behind := witnessProxy(s.server.URL, true)
	defer behind.Close()
	scanner := func(primary *httptest.Server, others ...*httptest.Server) *EVMScanner {
		var eps []config.ChainEndpointConfig
		if len(others) > 0 {
			eps = append(eps, config.ChainEndpointConfig{Name: "primary", RPCHost: primary.URL, Priority: 1})
			for i, o := range others {
				eps = append(eps, config.ChainEndpointConfig{Name: "other" + string(rune('a'+i)), RPCHost: o.URL, Priority: 2 + i})
			}
		}
		pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
		c.Assert(err, IsNil)
		e, err := NewEVMClient(s.thorKeys, config.BifrostChainConfiguration{
			ChainID:        common.AVAXChain,
			RPCHost:        primary.URL,
			SolvencyBlocks: 10,
			Endpoints:      eps,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ChainID:            common.AVAXChain,
				StartBlockHeight:   1,
				HTTPRequestTimeout: time.Second * 10,
				GasCacheBlocks:     40,
				Concurrency:        1,
			},
		}, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
		c.Assert(err, IsNil)
		// block 99 was observed on the old fork with the inbound held in it
		held := "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"
		c.Assert(e.evmScanner.blockMetaAccessor.SaveBlockMeta(99, &types.BlockMeta{
			Height:       99,
			BlockHash:    "0x00000000000000000000000000000000000000000000000000000000000000dd",
			Transactions: []types.TransactionMeta{{Hash: held, BlockHeight: 99}},
		}), IsNil)
		return e.evmScanner
	}
	var parentHeader etypes.Header
	c.Assert(json.Unmarshal([]byte(reorgBlockJSON), &parentHeader), IsNil)
	block100 := &etypes.Header{Number: big.NewInt(100), ParentHash: parentHeader.Hash()}

	// the active endpoint has the new block 99 and does not know held; the other
	// endpoint still has it: errata, and block 99's record takes the witness's hash
	two := scanner(atTip, s.server)
	c.Assert(two.endpointMgr.Active().Name, Equals, "primary")
	plan, err := two.planReorg(block100)
	c.Assert(err, IsNil)
	c.Assert(plan.errata, HasLen, 1)
	c.Assert(plan.errata[0].Txs, HasLen, 1)
	c.Assert(string(plan.errata[0].Txs[0].TxID), Equals, "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b")
	c.Assert(plan.metas, HasLen, 1)
	c.Assert(plan.metas[0].BlockHash, Equals, parentHeader.Hash().Hex())

	// the active endpoint's block 99 is not the scanned block's parent (another fork):
	// undecided, the reorg step is retried
	_, err = two.planReorg(&etypes.Header{Number: big.NewInt(100), ParentHash: ecommon.HexToHash("0x01")})
	c.Assert(err, ErrorMatches, ".*behind or on another fork.*")

	// the active endpoint has not reached block 99: undecided, retried
	lagging := scanner(behind, s.server)
	c.Assert(lagging.endpointMgr.Active().Name, Equals, "primary")
	_, err = lagging.planReorg(block100)
	c.Assert(err, ErrorMatches, ".*cannot read block 99.*")

	// a single endpoint served the scanned block itself and decides on its own answers
	one := scanner(atTip)
	plan, err = one.planReorg(block100)
	c.Assert(err, IsNil)
	c.Assert(plan.errata, HasLen, 1)
}
