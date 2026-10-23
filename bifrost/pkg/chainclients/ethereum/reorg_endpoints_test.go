package ethereum

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
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
	scanner := func(primary *httptest.Server, others ...*httptest.Server) *ETHScanner {
		var eps []config.ChainEndpointConfig
		if len(others) > 0 {
			eps = append(eps, config.ChainEndpointConfig{Name: "primary", RPCHost: primary.URL, Priority: 1})
			for i, o := range others {
				eps = append(eps, config.ChainEndpointConfig{Name: "other" + string(rune('a'+i)), RPCHost: o.URL, Priority: 2 + i})
			}
		}
		pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
		c.Assert(err, IsNil)
		e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
			ChainID:   common.ETHChain,
			RPCHost:   primary.URL,
			Endpoints: eps,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ChainID:            common.ETHChain,
				StartBlockHeight:   1,
				HTTPRequestTimeout: time.Second * 10,
				GasCacheBlocks:     40,
				Concurrency:        1,
				GasPriceResolution: 10_000_000_000,
				FixedGasRate:       10_000_000_000,
			},
		}, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
		c.Assert(err, IsNil)
		// block 99 was observed on the old fork with the inbound held in it
		held := "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"
		c.Assert(e.ethScanner.blockMetaAccessor.SaveBlockMeta(99, &types.BlockMeta{
			Height:       99,
			BlockHash:    "0x00000000000000000000000000000000000000000000000000000000000000dd",
			Transactions: []types.TransactionMeta{{Hash: held, BlockHeight: 99}},
		}), IsNil)
		return e.ethScanner
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

// rescanProxy serves a non-empty replacement block 99 holding tx, answers its
// receipt (bulk and per transaction) with one that carries *receiptBlock as its block
// hash, and forwards everything else to target.
func rescanProxy(target, blockAnswer string, tx *etypes.Transaction, receiptBlock *string) *httptest.Server {
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
		receipt := `{"transactionHash":"` + tx.Hash().Hex() + `","transactionIndex":"0x0","blockNumber":"0x63",` +
			`"blockHash":"` + *receiptBlock + `","cumulativeGasUsed":"0x5208","gasUsed":"0x5208","effectiveGasPrice":"0x1",` +
			`"logs":[],"status":"0x1","type":"0x0","logsBloom":"0x` + strings.Repeat("00", 256) + `"}`
		switch rpcReq.Method {
		case "eth_getTransactionByHash":
			answer("null")
		case "eth_getBlockByNumber":
			answer(blockAnswer)
		case "eth_getBlockReceipts":
			answer("[" + receipt + "]")
		case "eth_getTransactionReceipt":
			answer(receipt)
		default:
			resp, err := http.Post(target+req.URL.Path, "application/json", bytes.NewReader(body))
			if err != nil {
				http.Error(rw, err.Error(), http.StatusBadGateway)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			rw.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(rw, resp.Body)
		}
	}))
}

// A reorg rescans the replacement block; its receipts must belong to that block. A
// receipt of another block at the same height (a node on the other fork, or one
// that reorged again since the block was read) fails the plan, so nothing is
// committed and the step is retried; it is never observed from the wrong fork.
func (s *UnstuckTestSuite) TestReorgRescanRejectsReceiptsOfAnotherBlock(c *C) {
	config.Init()
	to := ecommon.HexToAddress("0x9f4aab49a9cd8fc54dcb3701846f608a6f2c44da")
	tx := etypes.NewTx(&etypes.LegacyTx{Nonce: 0, To: &to, Value: big.NewInt(1), Gas: 21000, GasPrice: big.NewInt(1)})
	txJSON, err := json.Marshal(tx)
	c.Assert(err, IsNil)
	blockAnswer := strings.Replace(strings.Replace(reorgBlockJSON,
		`"transactions":[]`, `"transactions":[`+string(txJSON)+`]`, 1),
		`"transactionsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"`,
		`"transactionsRoot":"0x`+strings.Repeat("ab", 32)+`"`, 1)
	var header etypes.Header
	c.Assert(json.Unmarshal([]byte(blockAnswer), &header), IsNil)
	replacement := header.Hash()

	receiptBlock := "0x" + strings.Repeat("11", 32)
	node := rescanProxy(s.server.URL, blockAnswer, tx, &receiptBlock)
	defer node.Close()
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: node.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.ETHChain,
			StartBlockHeight:   1,
			HTTPRequestTimeout: time.Second * 10,
			GasCacheBlocks:     40,
			Concurrency:        1,
			GasPriceResolution: 10_000_000_000,
			FixedGasRate:       10_000_000_000,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	scanner := e.ethScanner
	c.Assert(scanner.blockMetaAccessor.SaveBlockMeta(99, &types.BlockMeta{
		Height:    99,
		BlockHash: "0x00000000000000000000000000000000000000000000000000000000000000dd",
	}), IsNil)
	block100 := &etypes.Header{Number: big.NewInt(100), ParentHash: replacement}

	// receipts of another block: the plan fails and the stored record is untouched
	_, err = scanner.planReorg(block100)
	c.Assert(err, ErrorMatches, "(?s).*not the scanned block.*")
	meta, err := scanner.blockMetaAccessor.GetBlockMeta(99)
	c.Assert(err, IsNil)
	c.Assert(meta.BlockHash, Equals, "0x00000000000000000000000000000000000000000000000000000000000000dd")

	// receipts of the replacement block: the plan completes and repairs the record
	receiptBlock = replacement.Hex()
	plan, err := scanner.planReorg(block100)
	c.Assert(err, IsNil)
	c.Assert(plan.metas, HasLen, 1)
	c.Assert(plan.metas[0].BlockHash, Equals, replacement.Hex())
}

// A transaction the observer records at a height while a reorg plan is being worked
// out (onObservedTxIn) survives the commit, which repairs the same record: neither
// write is lost, so a later reorg still re-checks that transaction.
func (s *UnstuckTestSuite) TestCommitReorgKeepsTransactionsObservedMeanwhile(c *C) {
	config.Init()
	node := witnessProxy(s.server.URL, false)
	defer node.Close()
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: node.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.ETHChain,
			StartBlockHeight:   1,
			HTTPRequestTimeout: time.Second * 10,
			GasCacheBlocks:     40,
			Concurrency:        1,
			GasPriceResolution: 10_000_000_000,
			FixedGasRate:       10_000_000_000,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	scanner := e.ethScanner
	scanner.globalErrataQueue = make(chan stypes.ErrataBlock, 4)
	held := "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"
	c.Assert(scanner.blockMetaAccessor.SaveBlockMeta(99, &types.BlockMeta{
		Height:       99,
		BlockHash:    "0x00000000000000000000000000000000000000000000000000000000000000dd",
		Transactions: []types.TransactionMeta{{Hash: held, BlockHeight: 99}},
	}), IsNil)
	var parentHeader etypes.Header
	c.Assert(json.Unmarshal([]byte(reorgBlockJSON), &parentHeader), IsNil)

	plan, err := scanner.planReorg(&etypes.Header{Number: big.NewInt(100), ParentHash: parentHeader.Hash()})
	c.Assert(err, IsNil)
	c.Assert(plan.errata, HasLen, 1)

	// meanwhile the observer records another transaction at height 99
	observed := "0x" + strings.Repeat("cd", 32)
	scanner.onObservedTxIn(stypes.TxInItem{Tx: observed}, 99)

	scanner.commitReorg(plan)
	meta, err := scanner.blockMetaAccessor.GetBlockMeta(99)
	c.Assert(err, IsNil)
	c.Assert(meta.BlockHash, Equals, parentHeader.Hash().Hex()) // repaired
	hashes := make([]string, 0, len(meta.Transactions))
	for _, tx := range meta.Transactions {
		hashes = append(hashes, tx.Hash)
	}
	c.Assert(hashes, DeepEquals, []string{observed}) // errata'd tx gone, observed one kept
}
