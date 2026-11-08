package ethereum

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// reorgBlockJSON is the empty block a witnessProxy returns at any height.
const reorgBlockJSON = `{"parentHash":"0x0000000000000000000000000000000000000000000000000000000000000000","sha3Uncles":"0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347","miner":"0x0000000000000000000000000000000000000000","stateRoot":"0x0000000000000000000000000000000000000000000000000000000000000000","transactionsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421","receiptsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421","logsBloom":"0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000","difficulty":"0x1","number":"0x63","gasLimit":"0x1","gasUsed":"0x0","timestamp":"0x1","extraData":"0x","mixHash":"0x0000000000000000000000000000000000000000000000000000000000000000","nonce":"0x0000000000000000","transactions":[],"uncles":[]}`

// reorgTipFor returns block 100 as the child of the block in parentJSON, as served JSON
// and as a header: the block a scanner holds when it detects the reorg, which a witness
// at the reorg tip has too.
func reorgTipFor(parentJSON string) (string, *etypes.Header) {
	var parent etypes.Header
	if err := json.Unmarshal([]byte(parentJSON), &parent); err != nil {
		panic(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(parentJSON), &fields); err != nil {
		panic(err)
	}
	fields["number"], fields["parentHash"] = "0x64", parent.Hash().Hex()
	fields["transactions"], fields["transactionsRoot"] = []any{}, etypes.EmptyRootHash.Hex()
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	var tip etypes.Header
	if err = json.Unmarshal(raw, &tip); err != nil {
		panic(err)
	}
	return string(raw), &tip
}

// isTipRequest reports whether a JSON-RPC request asks for block 100 (reorgTipFor).
func isTipRequest(params []json.RawMessage) bool {
	return len(params) > 0 && string(params[0]) == `"0x64"`
}

// witnessProxy forwards every JSON-RPC call to target, except that it answers
// eth_getTransactionByHash with null (it does not know the transaction) and
// eth_getBlockByNumber with reorgBlockJSON (block 100: its reorgTipFor child), or with
// null when behind is set (it has not reached the block yet).
func witnessProxy(target string, behind bool) *httptest.Server {
	tipJSON, _ := reorgTipFor(reorgBlockJSON)
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string            `json:"method"`
			ID     json.RawMessage   `json:"id"`
			Params []json.RawMessage `json:"params"`
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
		case rpcReq.Method == "eth_getBlockByNumber" && isTipRequest(rpcReq.Params):
			answer(tipJSON)
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
	_, block100 := reorgTipFor(reorgBlockJSON)

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
	tipJSON, _ := reorgTipFor(blockAnswer)
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string            `json:"method"`
			ID     json.RawMessage   `json:"id"`
			Params []json.RawMessage `json:"params"`
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
			if isTipRequest(rpcReq.Params) {
				answer(tipJSON)
				return
			}
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
	_, block100 := reorgTipFor(blockAnswer)

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

// receiptGate forwards every request to target, counting receipt reads in hits and
// answering them with HTTP 500 when fail is set.
func receiptGate(target string, fail bool, hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		if bytes.Contains(body, []byte(`"eth_getBlockReceipts"`)) || bytes.Contains(body, []byte(`"eth_getTransactionReceipt"`)) {
			hits.Add(1)
			if fail {
				http.Error(rw, "receipts unavailable", http.StatusInternalServerError)
				return
			}
		}
		resp, err := http.Post(target+req.URL.Path, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		rw.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		rw.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(rw, resp.Body)
	}))
}

// A reorg rescan reads its receipts from the witness only, the endpoint whose block
// it extracts: when the witness cannot serve them the step fails and is retried, and
// another endpoint, even one holding the right receipts, is never asked, so a plan
// never mixes two nodes' views of the chain.
func (s *UnstuckTestSuite) TestReorgRescanReadsReceiptsFromTheWitnessOnly(c *C) {
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

	receiptBlock := replacement.Hex()
	node := rescanProxy(s.server.URL, blockAnswer, tx, &receiptBlock)
	defer node.Close()
	var witnessHits, otherHits atomic.Int32
	witness := receiptGate(node.URL, true, &witnessHits)
	defer witness.Close()
	other := receiptGate(node.URL, false, &otherHits)
	defer other.Close()
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: witness.URL,
		Endpoints: []config.ChainEndpointConfig{
			{Name: "witness", RPCHost: witness.URL, Priority: 1},
			{Name: "other", RPCHost: other.URL, Priority: 2},
		},
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
	// both endpoints in rotation, so an unpinned read would fail over to the other
	for _, en := range scanner.endpointMgr.Entries() {
		for i := 0; i < 5 && en.State() != endpoints.StateHealthy; i++ {
			en.RecordHealthCheck(true, scanner.endpointMgr.Config())
		}
		c.Assert(en.Eligible(), Equals, true)
	}
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "witness")
	c.Assert(scanner.blockMetaAccessor.SaveBlockMeta(99, &types.BlockMeta{
		Height:    99,
		BlockHash: "0x00000000000000000000000000000000000000000000000000000000000000dd",
	}), IsNil)
	_, block100 := reorgTipFor(blockAnswer)

	_, err = scanner.planReorg(block100)
	c.Assert(err, NotNil)
	c.Assert(witnessHits.Load() > 0, Equals, true)
	c.Assert(otherHits.Load(), Equals, int32(0))
	meta, err := scanner.blockMetaAccessor.GetBlockMeta(99)
	c.Assert(err, IsNil)
	c.Assert(meta.BlockHash, Equals, "0x00000000000000000000000000000000000000000000000000000000000000dd")
}

// failedReceiptWitness is a witness at the reorg tip that knows tx as mined in block
// 100 with a failed receipt, and forwards everything else to target.
func failedReceiptWitness(c *C, target string, tx *etypes.Transaction, from ecommon.Address) *httptest.Server {
	raw, err := tx.MarshalJSON()
	c.Assert(err, IsNil)
	var fields map[string]any
	c.Assert(json.Unmarshal(raw, &fields), IsNil)
	fields["blockNumber"], fields["blockHash"], fields["transactionIndex"], fields["from"] = "0x64", ecommon.Hash{0x64}.Hex(), "0x0", from.Hex()
	txJSON, err := json.Marshal(fields)
	c.Assert(err, IsNil)
	receiptJSON, err := json.Marshal(&etypes.Receipt{
		Status: etypes.ReceiptStatusFailed, CumulativeGasUsed: 21000, Logs: []*etypes.Log{}, TxHash: tx.Hash(),
		GasUsed: 21000, EffectiveGasPrice: big.NewInt(1), BlockHash: ecommon.Hash{0x64}, BlockNumber: big.NewInt(100),
	})
	c.Assert(err, IsNil)
	tipJSON, _ := reorgTipFor(reorgBlockJSON)
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string            `json:"method"`
			ID     json.RawMessage   `json:"id"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &rpcReq)
		rw.Header().Set("Content-Type", "application/json")
		answer := func(result []byte) {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + string(rpcReq.ID) + `,"result":` + string(result) + `}`))
		}
		switch rpcReq.Method {
		case "eth_getBlockByNumber":
			if isTipRequest(rpcReq.Params) {
				answer([]byte(tipJSON))
				return
			}
			answer([]byte(reorgBlockJSON))
			return
		case "eth_getTransactionByHash":
			answer(txJSON)
			return
		case "eth_getTransactionReceipt":
			answer(receiptJSON)
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

// The witness only has to have the scanned block's parent. When another endpoint
// served the scanned block, a transaction re-mined there can be unknown to the
// witness: its "not found" for a stored transaction the scanned block lists means it
// has not imported that block yet, so the reorg step is retried instead of sending an
// errata. A listed transaction the witness knows is judged on its receipt like any
// other: one that failed is still an errata.
func (s *UnstuckTestSuite) TestReorgWaitsForAWitnessThatHasTheScannedBlock(c *C) {
	config.Init()
	key, err := crypto.GenerateKey()
	c.Assert(err, IsNil)
	tx, err := etypes.SignTx(etypes.NewTx(&etypes.LegacyTx{Nonce: 7, Gas: 21000, GasPrice: big.NewInt(1)}), etypes.HomesteadSigner{}, key)
	c.Assert(err, IsNil)
	// metas store hashes as observations carry them: lowercase hex without 0x
	listed := tx.Hash().Hex()[2:]
	gone := "00000000000000000000000000000000000000000000000000000000000000ee"
	scanner := func(primary *httptest.Server, stored ...string) *ETHScanner {
		pubkeyMgr, perr := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
		c.Assert(perr, IsNil)
		e, nerr := NewClient(s.thorKeys, config.BifrostChainConfiguration{
			ChainID: common.ETHChain,
			RPCHost: primary.URL,
			Endpoints: []config.ChainEndpointConfig{
				{Name: "primary", RPCHost: primary.URL, Priority: 1},
				{Name: "other", RPCHost: s.server.URL, Priority: 2},
			},
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
		c.Assert(nerr, IsNil)
		metaTxs := make([]types.TransactionMeta, 0, len(stored))
		for _, h := range stored {
			metaTxs = append(metaTxs, types.TransactionMeta{Hash: h, BlockHeight: 99})
		}
		c.Assert(e.ethScanner.blockMetaAccessor.SaveBlockMeta(99, &types.BlockMeta{
			Height:       99,
			BlockHash:    "0x00000000000000000000000000000000000000000000000000000000000000dd",
			Transactions: metaTxs,
		}), IsNil)
		return e.ethScanner
	}
	var parentHeader etypes.Header
	c.Assert(json.Unmarshal([]byte(reorgBlockJSON), &parentHeader), IsNil)
	_, block100 := reorgTipFor(reorgBlockJSON)

	// the witness knows neither transaction: the listed one makes the step undecided
	atTip := witnessProxy(s.server.URL, false)
	defer atTip.Close()
	behind := scanner(atTip, listed, gone)
	_, err = behind.planReorgWith(block100, etypes.Transactions{tx}, 0)
	c.Assert(err, ErrorMatches, ".*has not imported the scanned block yet.*")
	// without the scanned block's list the same answers are two errata, as before
	plan, err := behind.planReorg(block100)
	c.Assert(err, IsNil)
	c.Assert(plan.errata, HasLen, 1)
	c.Assert(plan.errata[0].Txs, HasLen, 2)

	// the witness has the listed transaction, failed: an errata
	failed := failedReceiptWitness(c, s.server.URL, tx, crypto.PubkeyToAddress(key.PublicKey))
	defer failed.Close()
	plan, err = scanner(failed, listed).planReorgWith(block100, etypes.Transactions{tx}, 0)
	c.Assert(err, IsNil)
	c.Assert(plan.errata, HasLen, 1)
	c.Assert(plan.errata[0].Txs, HasLen, 1)
	c.Assert(string(plan.errata[0].Txs[0].TxID), Equals, listed)
}

// The chain height the scan was driven by reaches the witness check: a witness that is
// behind it (another endpoint served the scan, and may hold a block re-mining a stored
// transaction that the witness does not have yet) leaves the step undecided, and so
// does one that holds a competing block at the scanned height.
func (s *UnstuckTestSuite) TestReorgWitnessMustHoldTheScannedBlockAndReachTheScan(c *C) {
	config.Init()
	witness := witnessProxy(s.server.URL, false)
	defer witness.Close()
	// a witness at the reorg tip: its own tip is block 100
	atTip := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(body, &rpcReq)
		if rpcReq.Method == "eth_blockNumber" {
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + string(rpcReq.ID) + `,"result":"0x64"}`))
			return
		}
		resp, err := http.Post(witness.URL+req.URL.Path, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(rw, resp.Body)
	}))
	defer atTip.Close()
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: atTip.URL,
		Endpoints: []config.ChainEndpointConfig{
			{Name: "primary", RPCHost: atTip.URL, Priority: 1},
			{Name: "other", RPCHost: s.server.URL, Priority: 2},
		},
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
	_, block100 := reorgTipFor(reorgBlockJSON)

	_, err = scanner.planReorgWith(block100, nil, 101)
	c.Assert(err, ErrorMatches, ".*behind the chain height 101.*")
	competing := *block100
	competing.Extra = []byte{1}
	_, err = scanner.planReorgWith(&competing, nil, 0)
	c.Assert(err, ErrorMatches, ".*not the scanned block.*")
	plan, err := scanner.planReorgWith(block100, nil, 100)
	c.Assert(err, IsNil)
	c.Assert(plan, NotNil)
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "primary") // undecided is not a failure
}
