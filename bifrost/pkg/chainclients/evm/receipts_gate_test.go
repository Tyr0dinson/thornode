package evm

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	ethclient "github.com/ethereum/go-ethereum/ethclient"
	"github.com/holiman/uint256"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

var (
	gateRouter    = common.HexToAddress("0xb30ec53f98ff5947ede720d32ac2da7e52a5f56b")
	gateOldRouter = common.HexToAddress("0x1111111111111111111111111111111111111111")
	gateElsewhere = common.HexToAddress("0x2222222222222222222222222222222222222222")
	gateToken     = common.HexToAddress("0x3333333333333333333333333333333333333333")
)

// gatePubKeys is the real PubKeyManager with the router list pinned, because
// contracts normally arrive from thornode.
type gatePubKeys struct {
	pubkeymanager.PubKeyValidator
	contracts []thorcommon.Address
}

func (g gatePubKeys) GetContracts(thorcommon.Chain, bool) []thorcommon.Address { return g.contracts }

type gateFixture struct {
	scanner      *EVMScanner
	signer       etypes.Signer
	vault        common.Address
	vaultKeyHex  *secp256k1.PrivKey
	receipts     []*etypes.Receipt
	receiptCalls atomic.Int64
	otherCalls   atomic.Int64
	blockJSON    []byte
	nonce        uint64

	// blockHash is the hash of the block being scanned: served receipts carry it,
	// as a node's receipts do
	blockMu   sync.Mutex
	blockHash common.Hash
}

// scanning records the block a scanner call is about to read receipts for.
func (f *gateFixture) scanning(block *etypes.Block) *etypes.Block {
	f.blockMu.Lock()
	defer f.blockMu.Unlock()
	f.blockHash = block.Hash()
	return block
}

func newGateFixture(t *testing.T, chain thorcommon.Chain, contracts ...thorcommon.Address) *gateFixture {
	t.Helper()
	setupMocknetThorConfig()
	f := &gateFixture{signer: etypes.LatestSignerForChainID(big.NewInt(56))}

	priv := secp256k1.GenPrivKeyFromSecret([]byte("receipts-gate-vault"))
	f.vaultKeyHex = priv
	tm, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	vaultPubKey, err := thorcommon.NewPubKeyFromCrypto(tm)
	if err != nil {
		t.Fatal(err)
	}
	vaultAddr, err := vaultPubKey.GetAddress(chain)
	if err != nil {
		t.Fatal(err)
	}
	f.vault = common.HexToAddress(vaultAddr.String())

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &rpcReq)
		if rpcReq.Method == "eth_getBlockReceipts" {
			f.receiptCalls.Add(1)
			f.blockMu.Lock()
			served := make([]*etypes.Receipt, len(f.receipts))
			for i, r := range f.receipts {
				stamped := *r
				stamped.BlockHash = f.blockHash
				served[i] = &stamped
			}
			f.blockMu.Unlock()
			out, _ := json.Marshal(served)
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + string(out) + `}`))
			return
		}
		if rpcReq.Method == "eth_getBlockByNumber" && f.blockJSON != nil {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + string(f.blockJSON) + `}`))
			return
		}
		f.otherCalls.Add(1)
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x38"}`))
	}))
	t.Cleanup(server.Close)
	ethClient, err := ethclient.Dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	blockMetaAccessor, err := evm.NewLevelDBBlockMetaAccessor("gate-blockmeta-", "gate-signedtx-", storage.GetInternalDb())
	if err != nil {
		t.Fatal(err)
	}
	tokenManager, err := evm.NewTokenManager(
		storage.GetInternalDb(),
		"gate-tokenmeta-",
		chain.GetGasAsset(),
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

	cfg := getConfigForTest()
	cfg.ChainID = chain
	cfg.Concurrency = 4
	vaultABI, _, err := evm.GetContractABI(routerContractABI, erc20ContractABI)
	if err != nil {
		t.Fatal(err)
	}
	signerCache, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	if err != nil {
		t.Fatal(err)
	}
	f.scanner = &EVMScanner{
		cfg:                cfg,
		vaultABI:           vaultABI,
		signerCacheManager: signerCache,
		ethClient:          ethClient,
		blockMetaAccessor:  blockMetaAccessor,
		bridge:             testMimirBridge{getMimir: func(string) (int64, error) { return 1, nil }},
		pubkeyMgr:          gatePubKeys{PubKeyValidator: pkm, contracts: contracts},
		eipSigner:          f.signer,
		tokenManager:       tokenManager,
	}
	return f
}

// userTx signs a legacy tx from a throwaway key.
func (f *gateFixture) userTx(t *testing.T, to *common.Address, data []byte, gasPrice int64) *etypes.Transaction {
	t.Helper()
	f.nonce++
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := etypes.SignNewTx(key, f.signer, &etypes.LegacyTx{
		Nonce:    f.nonce,
		GasPrice: big.NewInt(gasPrice),
		Gas:      90000,
		To:       to,
		Value:    big.NewInt(1e18),
		Data:     data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// vaultTx signs a legacy tx with the vault's key.
func (f *gateFixture) vaultTx(t *testing.T, to *common.Address, data []byte) *etypes.Transaction {
	t.Helper()
	f.nonce++
	key, err := crypto.ToECDSA(f.vaultKeyHex.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := etypes.SignNewTx(key, f.signer, &etypes.LegacyTx{
		Nonce:    f.nonce,
		GasPrice: big.NewInt(3e9),
		Gas:      90000,
		To:       to,
		Data:     data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// decodedBlock is tx as the scanner gets it: decoded by ethclient from an RPC
// response that reports from as its sender.
func (f *gateFixture) decodedBlock(t *testing.T, tx *etypes.Transaction, from common.Address) *etypes.Block {
	t.Helper()
	header := &etypes.Header{
		Number:     big.NewInt(100),
		BaseFee:    big.NewInt(0),
		TxHash:     common.Hash{1},
		UncleHash:  etypes.EmptyUncleHash,
		Difficulty: big.NewInt(1),
	}
	var fields map[string]any
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	var txFields map[string]any
	if err = json.Unmarshal(raw, &txFields); err != nil {
		t.Fatal(err)
	}
	txFields["from"] = from.Hex()
	txFields["blockHash"] = header.Hash().Hex()
	fields["hash"] = header.Hash().Hex()
	fields["uncles"] = []string{}
	fields["transactions"] = []map[string]any{txFields}
	if f.blockJSON, err = json.Marshal(fields); err != nil {
		t.Fatal(err)
	}
	block, err := f.scanner.ethClient.BlockByNumber(context.Background(), big.NewInt(100))
	if err != nil {
		t.Fatal(err)
	}
	f.otherCalls.Store(0)
	return block
}

func gateBlock(baseFee *big.Int, bloom etypes.Bloom, txs ...*etypes.Transaction) *etypes.Block {
	return etypes.NewBlockWithHeader(&etypes.Header{Number: big.NewInt(100), BaseFee: baseFee, Bloom: bloom}).
		WithBody(etypes.Body{Transactions: txs})
}

func gateReceipt(tx *etypes.Transaction, status uint64, price *big.Int, logs ...*etypes.Log) *etypes.Receipt {
	if logs == nil {
		logs = []*etypes.Log{}
	}
	return &etypes.Receipt{
		Type:              tx.Type(),
		Status:            status,
		CumulativeGasUsed: 21000,
		Logs:              logs,
		TxHash:            tx.Hash(),
		GasUsed:           21000,
		EffectiveGasPrice: price,
		BlockNumber:       big.NewInt(100),
	}
}

func erc20TransferData(to common.Address) []byte {
	data := []byte{0xa9, 0x05, 0x9c, 0xbb}
	data = append(data, common.LeftPadBytes(to.Bytes(), 32)...)
	return append(data, common.LeftPadBytes(big.NewInt(1e18).Bytes(), 32)...)
}

func bloomOf(items ...[]byte) etypes.Bloom {
	var b etypes.Bloom
	for _, item := range items {
		b.Add(item)
	}
	return b
}

func sortedStrings(prices []*big.Int) []string {
	out := make([]string, 0, len(prices))
	for _, p := range prices {
		out = append(out, p.String())
	}
	sort.Strings(out)
	return out
}

func routerAddr() thorcommon.Address { return thorcommon.Address(strings.ToLower(gateRouter.Hex())) }

// A block no vault or router touched needs nothing from its receipts except gas
// prices, so it must not spend an RPC call on them.
func TestGetTxInSkipsReceiptsForUnrelatedBlock(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	block := gateBlock(big.NewInt(0), etypes.Bloom{}, f.userTx(t, &gateElsewhere, nil, 1e9))

	txIn, _, err := f.scanner.getTxIn(f.scanning(block), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 0 {
		t.Fatalf("got %d receipt calls, want 0", got)
	}
	if len(txIn.TxArray) != 0 {
		t.Fatalf("got %d observations, want 0", len(txIn.TxArray))
	}
}

// updateGasPrice feeds the network fee, so prices from tx fields must equal what
// the receipts report: legacy pays gasPrice, dynamic pays baseFee + tip capped at
// feeCap, and a contract creation still counts.
func TestGetTxInSkippedBlockGasPricesMatchReceipts(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	capped, err := etypes.SignNewTx(key, f.signer, &etypes.DynamicFeeTx{
		ChainID: big.NewInt(56), Nonce: 1, GasTipCap: big.NewInt(4e9), GasFeeCap: big.NewInt(5e9), Gas: 21000, To: &gateElsewhere,
	})
	if err != nil {
		t.Fatal(err)
	}
	tipped, err := etypes.SignNewTx(key, f.signer, &etypes.DynamicFeeTx{
		ChainID: big.NewInt(56), Nonce: 2, GasTipCap: big.NewInt(1e9), GasFeeCap: big.NewInt(5e9), Gas: 21000, To: &gateElsewhere,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := f.userTx(t, &gateElsewhere, nil, 7e9)
	create := f.userTx(t, nil, []byte{0x60}, 9e9)
	blob, err := etypes.SignNewTx(key, f.signer, &etypes.BlobTx{
		ChainID:    uint256.NewInt(56),
		Nonce:      3,
		GasTipCap:  uint256.NewInt(1e9),
		GasFeeCap:  uint256.NewInt(5e9),
		Gas:        21000,
		To:         gateElsewhere,
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []common.Hash{{0x01}},
	})
	if err != nil {
		t.Fatal(err)
	}
	block := gateBlock(big.NewInt(2e9), etypes.Bloom{}, capped, tipped, legacy, create, blob)

	_, skipped, err := f.scanner.getTxIn(f.scanning(block), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 0 {
		t.Fatalf("got %d receipt calls, want 0", got)
	}

	f.receipts = []*etypes.Receipt{
		gateReceipt(capped, 1, big.NewInt(5e9)),
		gateReceipt(tipped, 1, big.NewInt(3e9)),
		gateReceipt(legacy, 1, big.NewInt(7e9)),
		gateReceipt(create, 1, big.NewInt(9e9)),
		gateReceipt(blob, 1, big.NewInt(3e9)),
	}
	_, fetched, err := f.scanner.getTxInOptimized("eth_getBlockReceipts", f.scanning(block), "")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := strings.Join(sortedStrings(skipped), ","), strings.Join(sortedStrings(fetched), ","); a != b {
		t.Fatalf("skipped path prices %s, receipts path prices %s", a, b)
	}
}

// Signed items are cleaned up from every tx in a block whether or not receipts
// are fetched.
func TestGetTxInSkippedBlockRemovesSignedTxItems(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	tx := f.userTx(t, &gateElsewhere, nil, 1e9)
	item := evmtypes.SignedTxItem{Hash: tx.Hash().String(), Height: 100, VaultPubKey: "x"}
	if err := f.scanner.blockMetaAccessor.AddSignedTxItem(item); err != nil {
		t.Fatal(err)
	}

	if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
		t.Fatal(err)
	}
	items, err := f.scanner.blockMetaAccessor.GetSignedTxItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d signed items, want 0", len(items))
	}
}

// With the contract whitelist off, an aggregator nobody knows can call the router,
// and the deposit shows only in its logs. tx.To and the sender cannot find it.
func TestGetTxInFetchesReceiptsWhenRouterInBloom(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	tx := f.userTx(t, &gateElsewhere, []byte{1, 2, 3, 4}, 1e9)

	amount := big.NewInt(2e18)
	data, err := f.scanner.vaultABI.Events["Deposit"].Inputs.NonIndexed().Pack(amount, "SWAP:THOR.RUNE")
	if err != nil {
		t.Fatal(err)
	}
	log := &etypes.Log{
		Address: gateRouter,
		Topics: []common.Hash{
			f.scanner.vaultABI.Events["Deposit"].ID,
			common.BytesToHash(f.vault.Bytes()),
			common.BytesToHash(common.Address{}.Bytes()),
		},
		Data:   data,
		TxHash: tx.Hash(),
	}
	f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9), log)}

	txIn, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), bloomOf(gateRouter.Bytes()), tx)), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 1 {
		t.Fatalf("got %d receipt calls, want 1", got)
	}
	if len(txIn.TxArray) != 1 || !strings.EqualFold(txIn.TxArray[0].To, f.vault.Hex()) {
		t.Fatalf("deposit via unknown contract not observed: %+v", txIn.TxArray)
	}
}

// During a router upgrade logs can come from any router the vaults still use.
func TestGetTxInFetchesReceiptsWhenAnyRouterInBloom(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr(), thorcommon.Address(strings.ToLower(gateOldRouter.Hex())))
	tx := f.userTx(t, &gateElsewhere, nil, 1e9)
	f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}

	if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), bloomOf(gateOldRouter.Bytes()), tx)), ""); err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 1 {
		t.Fatalf("got %d receipt calls, want 1", got)
	}
}

// A native send to a vault emits no logs, so only its tx fields reveal it.
func TestGetTxInFetchesReceiptsForTxToVault(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	tx := f.userTx(t, &f.vault, []byte("SWAP:THOR.RUNE"), 1e9)
	f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}

	txIn, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 1 {
		t.Fatalf("got %d receipt calls, want 1", got)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("got %d observations, want 1", len(txIn.TxArray))
	}
}

// A failed outbound emits no logs. Its status is only in the receipt, and it must
// be observed so the outbound is retried.
func TestGetTxInFetchesReceiptsForFailedVaultTx(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	tx := f.vaultTx(t, &gateRouter, []byte{1, 2, 3, 4})
	f.receipts = []*etypes.Receipt{gateReceipt(tx, 0, big.NewInt(3e9))}

	txIn, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 1 {
		t.Fatalf("got %d receipt calls, want 1", got)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("got %d observations, want 1", len(txIn.TxArray))
	}
}

// A vault's token approval goes to the token, not a router or vault, and its log
// comes from the token, so only the sender reveals it.
func TestGetTxInFetchesReceiptsForTxFromVault(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	tx := f.vaultTx(t, &gateToken, []byte{0x09, 0x5e, 0xa7, 0xb3})
	f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(3e9))}

	if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 1 {
		t.Fatalf("got %d receipt calls, want 1", got)
	}
}

// Recovering every sender costs far more than the rest of the gate, and the RPC
// response already reports them.
func TestGetTxInUsesSenderReportedByRPC(t *testing.T) {
	t.Run("reported as vault", func(t *testing.T) {
		f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
		tx := f.userTx(t, &gateElsewhere, nil, 1e9)
		f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}
		block := f.decodedBlock(t, tx, f.vault)

		if _, _, err := f.scanner.getTxIn(f.scanning(block), ""); err != nil {
			t.Fatal(err)
		}
		if got := f.receiptCalls.Load(); got != 1 {
			t.Fatalf("got %d receipt calls, want 1", got)
		}
	})
	t.Run("reported as someone else", func(t *testing.T) {
		f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
		tx := f.vaultTx(t, &gateToken, nil)
		block := f.decodedBlock(t, tx, gateElsewhere)

		if _, _, err := f.scanner.getTxIn(f.scanning(block), ""); err != nil {
			t.Fatal(err)
		}
		if got := f.receiptCalls.Load(); got != 0 {
			t.Fatalf("got %d receipt calls, want 0", got)
		}
	})
}

// A block not decoded from an RPC response has no reported senders. That must fall
// back to recovering them, not cost one RPC per tx.
func TestGetTxInSenderLookupNeverCallsRPC(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	tx := f.userTx(t, &gateElsewhere, nil, 1e9)
	vaultTx := f.vaultTx(t, &gateToken, nil)
	f.receipts = []*etypes.Receipt{
		gateReceipt(tx, 1, big.NewInt(1e9)),
		gateReceipt(vaultTx, 1, big.NewInt(3e9)),
	}

	if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx, vaultTx)), ""); err != nil {
		t.Fatal(err)
	}
	if got := f.otherCalls.Load(); got != 0 {
		t.Fatalf("got %d other RPC calls, want 0", got)
	}
	if got := f.receiptCalls.Load(); got != 1 {
		t.Fatalf("recovered vault sender not found: got %d receipt calls, want 1", got)
	}
}

// A tx sent straight to a router or a whitelisted aggregator takes the contract
// parsing path even if the bloom has no router log.
func TestGetTxInFetchesReceiptsForTxToKnownContract(t *testing.T) {
	aggregator := common.HexToAddress("0x4444444444444444444444444444444444444444")
	for name, to := range map[string]common.Address{"router": gateRouter, "whitelisted": aggregator} {
		t.Run(name, func(t *testing.T) {
			f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
			f.scanner.whitelistContracts = []thorcommon.Address{thorcommon.Address(strings.ToLower(aggregator.Hex()))}
			tx := f.userTx(t, &to, []byte{1, 2, 3, 4}, 1e9)
			f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}

			if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
				t.Fatal(err)
			}
			if got := f.receiptCalls.Load(); got != 1 {
				t.Fatalf("got %d receipt calls, want 1", got)
			}
		})
	}
}

// A direct ERC20 inbound names the vault only in its calldata and token log.
func TestGetTxInFetchesReceiptsForERC20TransferToVault(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	tx := f.userTx(t, &gateToken, erc20TransferData(f.vault), 1e9)
	f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}

	if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 1 {
		t.Fatalf("got %d receipt calls, want 1", got)
	}
}

// Token transfers between other accounts are most of a BSC block and must not
// defeat the gate.
func TestGetTxInSkipsReceiptsForERC20TransferElsewhere(t *testing.T) {
	f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
	tx := f.userTx(t, &gateToken, erc20TransferData(gateElsewhere), 1e9)

	if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
		t.Fatal(err)
	}
	if got := f.receiptCalls.Load(); got != 0 {
		t.Fatalf("got %d receipt calls, want 0", got)
	}
}

// Anything the gate cannot judge goes to the receipts: no routers known yet means
// there is nothing to test the bloom against, and no base fee means tx fields do
// not give the price paid.
func TestGetTxInFetchesReceiptsWhenGateCannotDecide(t *testing.T) {
	t.Run("no routers known", func(t *testing.T) {
		f := newGateFixture(t, thorcommon.BSCChain)
		tx := f.userTx(t, &gateElsewhere, nil, 1e9)
		f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}
		if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
			t.Fatal(err)
		}
		if got := f.receiptCalls.Load(); got != 1 {
			t.Fatalf("got %d receipt calls, want 1", got)
		}
	})
	t.Run("unknown sender", func(t *testing.T) {
		f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		// signed for another chain, so this scanner's signer cannot recover it
		tx, err := etypes.SignNewTx(key, etypes.LatestSignerForChainID(big.NewInt(1)), &etypes.DynamicFeeTx{
			ChainID: big.NewInt(1), GasTipCap: big.NewInt(1e9), GasFeeCap: big.NewInt(1e9), Gas: 21000, To: &gateElsewhere,
		})
		if err != nil {
			t.Fatal(err)
		}
		// the node returns the tx's receipt: every tx a block lists has one
		f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}
		if _, _, err = f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
			t.Fatal(err)
		}
		if got := f.receiptCalls.Load(); got != 1 {
			t.Fatalf("got %d receipt calls, want 1", got)
		}
	})
	t.Run("fee cap under base fee", func(t *testing.T) {
		f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
		tx := f.userTx(t, &gateElsewhere, nil, 1e9)
		f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}
		if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(2e9), etypes.Bloom{}, tx)), ""); err != nil {
			t.Fatal(err)
		}
		if got := f.receiptCalls.Load(); got != 1 {
			t.Fatalf("got %d receipt calls, want 1", got)
		}
	})
	t.Run("truncated ERC20 transfer", func(t *testing.T) {
		f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
		tx := f.userTx(t, &gateToken, []byte{0xa9, 0x05, 0x9c, 0xbb, 0x01}, 1e9)
		f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}
		if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
			t.Fatal(err)
		}
		if got := f.receiptCalls.Load(); got != 1 {
			t.Fatalf("got %d receipt calls, want 1", got)
		}
	})
	t.Run("no base fee", func(t *testing.T) {
		f := newGateFixture(t, thorcommon.BSCChain, routerAddr())
		tx := f.userTx(t, &gateElsewhere, nil, 1e9)
		f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}
		if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(nil, etypes.Bloom{}, tx)), ""); err != nil {
			t.Fatal(err)
		}
		if got := f.receiptCalls.Load(); got != 1 {
			t.Fatalf("got %d receipt calls, want 1", got)
		}
	})
}

// The gate has only been checked against live BSC blocks, so the other chains on
// the block-receipts path keep fetching.
func TestGetTxInStillFetchesReceiptsOnOtherChains(t *testing.T) {
	for _, chain := range []thorcommon.Chain{thorcommon.BASEChain, thorcommon.POLChain} {
		t.Run(chain.String(), func(t *testing.T) {
			f := newGateFixture(t, chain, routerAddr())
			tx := f.userTx(t, &gateElsewhere, nil, 1e9)
			f.receipts = []*etypes.Receipt{gateReceipt(tx, 1, big.NewInt(1e9))}
			if _, _, err := f.scanner.getTxIn(f.scanning(gateBlock(big.NewInt(0), etypes.Bloom{}, tx)), ""); err != nil {
				t.Fatal(err)
			}
			if got := f.receiptCalls.Load(); got != 1 {
				t.Fatalf("got %d receipt calls, want 1", got)
			}
		})
	}
}
