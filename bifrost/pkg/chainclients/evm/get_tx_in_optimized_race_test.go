package evm

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	ethclient "github.com/ethereum/go-ethereum/ethclient"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// TestGetTxInOptimizedConcurrentReceipts converts a block's receipts with
// Concurrency > 1 (BASE and BSC run with 4). Each receipt goroutine must keep
// its own error: a shared one is a data race under -race, and lets one
// goroutine drop its valid inbound after seeing another goroutine's error.
// Non-positive Concurrency values must still convert the block.
func TestGetTxInOptimizedConcurrentReceipts(t *testing.T) {
	setupMocknetThorConfig()
	const numTxs = 32
	chainID := big.NewInt(8453)
	signer := etypes.NewPragueSigner(chainID)

	priv := secp256k1.GenPrivKeyFromSecret([]byte("get-tx-in-optimized-race-vault"))
	tm, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	vaultPubKey, err := thorcommon.NewPubKeyFromCrypto(tm)
	if err != nil {
		t.Fatal(err)
	}
	vaultAddr, err := vaultPubKey.GetAddress(thorcommon.BASEChain)
	if err != nil {
		t.Fatal(err)
	}
	vault := common.HexToAddress(vaultAddr.String())

	// half the txs pay the vault, half go elsewhere
	var txs etypes.Transactions
	// every receipt carries the hash of the block the scanner is given below, as a
	// real node's does
	blockHash := etypes.NewBlockWithHeader(&etypes.Header{Number: big.NewInt(100)}).Hash()
	var receipts []*etypes.Receipt
	for i := 0; i < numTxs; i++ {
		key, keyErr := crypto.HexToECDSA(fmt.Sprintf("%064x", i+1))
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		to := vault
		if i%2 == 1 {
			to = common.BigToAddress(big.NewInt(int64(0x1000 + i)))
		}
		tx, signErr := etypes.SignNewTx(key, signer, &etypes.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: big.NewInt(1e9),
			GasFeeCap: big.NewInt(2e9),
			Gas:       21000,
			To:        &to,
			Value:     big.NewInt(1e18),
		})
		if signErr != nil {
			t.Fatal(signErr)
		}
		txs = append(txs, tx)
		receipts = append(receipts, &etypes.Receipt{
			Type:              etypes.DynamicFeeTxType,
			Status:            1,
			CumulativeGasUsed: 21000,
			Logs:              []*etypes.Log{},
			TxHash:            tx.Hash(),
			GasUsed:           21000,
			EffectiveGasPrice: big.NewInt(2e9),
			BlockNumber:       big.NewInt(100),
			BlockHash:         blockHash,
		})
	}
	receiptsJSON, err := json.Marshal(receipts)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &rpcReq)
		if rpcReq.Method == "eth_getBlockReceipts" {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + string(receiptsJSON) + `}`))
			return
		}
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x2105"}`))
	}))
	defer server.Close()
	ethClient, err := ethclient.Dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storage.Close() }()
	blockMetaAccessor, err := evm.NewLevelDBBlockMetaAccessor("base-blockmeta-", "base-signedtx-", storage.GetInternalDb())
	if err != nil {
		t.Fatal(err)
	}
	tokenManager, err := evm.NewTokenManager(
		storage.GetInternalDb(),
		"base-tokenmeta-",
		thorcommon.BASEChain.GetGasAsset(),
		defaultDecimals,
		time.Second,
		nil,
		ethClient,
		routerContractABI,
		erc20ContractABI,
	)
	if err != nil {
		t.Fatal(err)
	}
	pkm, err := pubkeymanager.NewPubKeyManager(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkm.AddPubKey(vaultPubKey, false, thorcommon.SigningAlgoSecp256k1)

	block := etypes.NewBlockWithHeader(&etypes.Header{Number: big.NewInt(100)}).
		WithBody(etypes.Body{Transactions: txs})

	// 0 and negative values must not hang (unbuffered semaphore) or panic
	for _, concurrency := range []int64{4, 1, 0, -1} {
		cfg := getConfigForTest()
		cfg.ChainID = thorcommon.BASEChain
		cfg.Concurrency = concurrency
		scanner := &EVMScanner{
			cfg:               cfg,
			ethClient:         ethClient,
			blockMetaAccessor: blockMetaAccessor,
			bridge:            testMimirBridge{getMimir: func(string) (int64, error) { return -1, nil }},
			pubkeyMgr:         pkm,
			eipSigner:         signer,
			tokenManager:      tokenManager,
		}

		type result struct {
			txIn      stypes.TxIn
			gasPrices []*big.Int
			err       error
		}
		done := make(chan result, 1)
		go func() {
			txIn, gasPrices, err := scanner.getTxInOptimized("eth_getBlockReceipts", block)
			done <- result{txIn, gasPrices, err}
		}()
		var res result
		select {
		case res = <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("concurrency %d: getTxInOptimized did not return", concurrency)
		}
		if res.err != nil {
			t.Fatalf("concurrency %d: %v", concurrency, res.err)
		}
		if len(res.gasPrices) != numTxs {
			t.Fatalf("concurrency %d: got %d gas prices, want %d", concurrency, len(res.gasPrices), numTxs)
		}
		if len(res.txIn.TxArray) != numTxs/2 {
			t.Fatalf("concurrency %d: got %d inbounds, want %d", concurrency, len(res.txIn.TxArray), numTxs/2)
		}
	}

	// the same receipts stamped with another block at this height (a node that
	// reorged since the block was read, or another endpoint on another fork) fail
	// the block so it is retried: they are never observed
	for _, r := range receipts {
		r.BlockHash = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	}
	otherJSON, merr := json.Marshal(receipts)
	if merr != nil {
		t.Fatal(merr)
	}
	other := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		_, _ = io.ReadAll(req.Body)
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + string(otherJSON) + `}`))
	}))
	defer other.Close()
	otherClient, derr := ethclient.Dial(other.URL)
	if derr != nil {
		t.Fatal(derr)
	}
	cfg := getConfigForTest()
	cfg.ChainID = thorcommon.BASEChain
	cfg.Concurrency = 4
	scanner := &EVMScanner{
		cfg:               cfg,
		ethClient:         otherClient,
		blockMetaAccessor: blockMetaAccessor,
		bridge:            testMimirBridge{getMimir: func(string) (int64, error) { return -1, nil }},
		pubkeyMgr:         pkm,
		eipSigner:         signer,
		tokenManager:      tokenManager,
	}
	txIn, _, gerr := scanner.getTxInOptimized("eth_getBlockReceipts", block)
	if gerr == nil || !strings.Contains(gerr.Error(), "not of the scanned block") {
		t.Fatalf("receipts of another block: got err %v, want a scanned-block mismatch", gerr)
	}
	if len(txIn.TxArray) != 0 {
		t.Fatalf("receipts of another block: got %d inbounds, want none", len(txIn.TxArray))
	}
}
