package ethereum

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/holiman/uint256"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
	tokenlist "gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
)

var (
	gateRouter    = ecommon.HexToAddress("0xd37bbe5744d730a1d98d8dc97c42f0ca46ad7146")
	gateOldRouter = ecommon.HexToAddress("0x1111111111111111111111111111111111111111")
	gateElsewhere = ecommon.HexToAddress("0x2222222222222222222222222222222222222222")
	// unrelatedBloom has logs in it, none from a contract the scanner parses.
	unrelatedBloom = bloomOf(gateElsewhere.Bytes())
)

// gatePubKeys is the real PubKeyManager with the router list pinned, because
// contracts normally arrive from thornode.
type gatePubKeys struct {
	pubkeymanager.PubKeyValidator
	contracts []thorcommon.Address
}

func (g gatePubKeys) GetContracts(thorcommon.Chain, bool) []thorcommon.Address { return g.contracts }

type gateFixture struct {
	scanner      *ETHScanner
	signer       etypes.Signer
	vault        ecommon.Address
	vaultKey     *secp256k1.PrivKey
	receipts     []*etypes.Receipt
	blockJSON    []byte
	receiptCalls atomic.Int64
	otherCalls   atomic.Int64
	mimir        int64
	nonce        uint64
}

func routerAddr(addr ecommon.Address) thorcommon.Address {
	return thorcommon.Address(strings.ToLower(addr.Hex()))
}

func newGateFixture(t *testing.T, contracts ...thorcommon.Address) *gateFixture {
	t.Helper()
	setupMocknetThorConfig()
	f := &gateFixture{signer: etypes.NewPragueSigner(big.NewInt(Mainnet)), mimir: 1}

	f.vaultKey = secp256k1.GenPrivKeyFromSecret([]byte("eth-receipts-gate-vault"))
	tm, err := cryptocodec.ToCmtPubKeyInterface(f.vaultKey.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	vaultPubKey, err := thorcommon.NewPubKeyFromCrypto(tm)
	if err != nil {
		t.Fatal(err)
	}
	vaultAddr, err := vaultPubKey.GetAddress(thorcommon.ETHChain)
	if err != nil {
		t.Fatal(err)
	}
	f.vault = ecommon.HexToAddress(vaultAddr.String())

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &rpcReq)
		if rpcReq.Method == "eth_getBlockReceipts" {
			f.receiptCalls.Add(1)
			out, _ := json.Marshal(f.receipts)
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + string(out) + `}`))
			return
		}
		f.otherCalls.Add(1)
		if rpcReq.Method == "eth_getBlockByNumber" && f.blockJSON != nil {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + string(f.blockJSON) + `}`))
			return
		}
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	t.Cleanup(server.Close)
	client, err := ethclient.Dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	blockMetaAccessor, err := evm.NewLevelDBBlockMetaAccessor(prefixBlockMeta, prefixSignedTxItem, storage.GetInternalDb())
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := evm.NewLevelDBTokenMeta(storage.GetInternalDb(), prefixTokenMeta)
	if err != nil {
		t.Fatal(err)
	}
	if err = tokens.SaveTokenMeta("ETH", ethToken, defaultDecimals); err != nil {
		t.Fatal(err)
	}
	vaultABI, erc20ABI, err := evm.GetContractABI(routerContractABI, erc20ContractABI)
	if err != nil {
		t.Fatal(err)
	}
	signerCache, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	if err != nil {
		t.Fatal(err)
	}
	pkm, err := pubkeymanager.NewPubKeyManager(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkm.AddPubKey(vaultPubKey, false, thorcommon.SigningAlgoSecp256k1)

	cfg := getConfigForTest()
	cfg.Concurrency = 4
	f.scanner = &ETHScanner{
		cfg:                cfg,
		client:             client,
		blockMetaAccessor:  blockMetaAccessor,
		tokens:             tokens,
		vaultABI:           vaultABI,
		erc20ABI:           erc20ABI,
		bridge:             testMimirBridge{getMimir: func(string) (int64, error) { return f.mimir, nil }},
		pubkeyMgr:          gatePubKeys{PubKeyValidator: pkm, contracts: contracts},
		eipSigner:          f.signer,
		whitelistTokens:    tokenlist.GetETHTokenList().Tokens,
		signerCacheManager: signerCache,
	}
	return f
}

// signedTx signs a legacy tx from key.
func (f *gateFixture) signedTx(t *testing.T, key *secp256k1.PrivKey, to *ecommon.Address, value *big.Int, data []byte) *etypes.Transaction {
	t.Helper()
	f.nonce++
	ecdsaKey, err := crypto.ToECDSA(key.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := etypes.SignNewTx(ecdsaKey, f.signer, &etypes.LegacyTx{
		Nonce:    f.nonce,
		GasPrice: big.NewInt(1e9),
		Gas:      90000,
		To:       to,
		Value:    value,
		Data:     data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func (f *gateFixture) userTx(t *testing.T, to *ecommon.Address, data []byte) *etypes.Transaction {
	t.Helper()
	return f.signedTx(t, secp256k1.GenPrivKey(), to, big.NewInt(1e18), data)
}

func (f *gateFixture) vaultTx(t *testing.T, to *ecommon.Address, data []byte) *etypes.Transaction {
	t.Helper()
	return f.signedTx(t, f.vaultKey, to, big.NewInt(0), data)
}

func (f *gateFixture) depositLog(t *testing.T, router ecommon.Address) *etypes.Log {
	t.Helper()
	data, err := f.scanner.vaultABI.Events["Deposit"].Inputs.NonIndexed().Pack(big.NewInt(2e18), "SWAP:THOR.RUNE")
	if err != nil {
		t.Fatal(err)
	}
	return &etypes.Log{
		Address: router,
		Topics: []ecommon.Hash{
			f.scanner.vaultABI.Events["Deposit"].ID,
			ecommon.BytesToHash(f.vault.Bytes()),
			ecommon.BytesToHash(ecommon.Address{}.Bytes()),
		},
		Data: data,
	}
}

// decodedBlock returns a block holding tx as ethclient decodes it from an RPC
// response that reports from as its sender.
func (f *gateFixture) decodedBlock(t *testing.T, tx *etypes.Transaction, from ecommon.Address) *etypes.Block {
	t.Helper()
	header := &etypes.Header{
		Number:     big.NewInt(100),
		BaseFee:    big.NewInt(1e9),
		Bloom:      unrelatedBloom,
		TxHash:     ecommon.Hash{1},
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
	block, err := f.scanner.client.BlockByNumber(context.Background(), big.NewInt(100))
	if err != nil {
		t.Fatal(err)
	}
	f.otherCalls.Store(0)
	return block
}

func gateBlock(bloom etypes.Bloom, txs ...*etypes.Transaction) *etypes.Block {
	return etypes.NewBlockWithHeader(&etypes.Header{Number: big.NewInt(100), BaseFee: big.NewInt(1e9), Bloom: bloom}).
		WithBody(etypes.Body{Transactions: txs})
}

func gateReceipt(tx *etypes.Transaction, status uint64, logs ...*etypes.Log) *etypes.Receipt {
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
		EffectiveGasPrice: big.NewInt(1e9),
		BlockNumber:       big.NewInt(100),
	}
}

// scan runs extractTxs over block with receipts as eth_getBlockReceipts would serve
// them, which only for this block hash are accepted.
func (f *gateFixture) scan(t *testing.T, block *etypes.Block, receipts ...*etypes.Receipt) stypes.TxIn {
	t.Helper()
	for _, r := range receipts {
		r.BlockHash = block.Hash()
	}
	f.receipts = receipts
	txIn, err := f.scanner.extractTxs(block)
	if err != nil {
		t.Fatal(err)
	}
	return txIn
}

func (f *gateFixture) assertSkipped(t *testing.T, txIn stypes.TxIn) {
	t.Helper()
	if got := f.receiptCalls.Load(); got != 0 {
		t.Fatalf("got %d block receipt calls, want 0", got)
	}
	if got := f.otherCalls.Load(); got != 0 {
		t.Fatalf("got %d other RPC calls, want 0", got)
	}
	if len(txIn.TxArray) != 0 {
		t.Fatalf("got %d observations, want 0", len(txIn.TxArray))
	}
}

func (f *gateFixture) assertFetched(t *testing.T) {
	t.Helper()
	if got := f.receiptCalls.Load(); got != 1 {
		t.Fatalf("got %d block receipt calls, want 1", got)
	}
}

func erc20TransferData(to ecommon.Address) []byte {
	return erc20TransferCallData(to, big.NewInt(1e18))
}

func bloomOf(items ...[]byte) etypes.Bloom {
	var b etypes.Bloom
	for _, item := range items {
		b.Add(item)
	}
	return b
}

func gateToken(t *testing.T) ecommon.Address {
	t.Helper()
	tokens := tokenlist.GetETHTokenList().Tokens
	if len(tokens) == 0 {
		t.Fatal("no ETH tokens")
	}
	return ecommon.HexToAddress(tokens[0].Address)
}

// A block no vault or router touched needs nothing from its receipts.
func TestExtractTxsSkipsReceiptsForUnrelatedBlock(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter))
	f.assertSkipped(t, f.scan(t, gateBlock(unrelatedBloom, f.userTx(t, &gateElsewhere, nil))))
}

// A skipped block must clear the same signed items as the receipts path, which
// passes over blob txs and contract creations.
func TestExtractTxsSkippedBlockRemovesSameSignedTxItems(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter))
	plain := f.userTx(t, &gateElsewhere, nil)
	creation := f.userTx(t, nil, nil)
	blob := etypes.NewTx(&etypes.BlobTx{ChainID: uint256.NewInt(Mainnet), To: gateElsewhere, Nonce: 99})
	for _, tx := range []*etypes.Transaction{plain, creation, blob} {
		item := evmtypes.SignedTxItem{Hash: tx.Hash().String(), Height: 100, VaultPubKey: "x"}
		if err := f.scanner.blockMetaAccessor.AddSignedTxItem(item); err != nil {
			t.Fatal(err)
		}
	}

	f.assertSkipped(t, f.scan(t, gateBlock(unrelatedBloom, plain, creation, blob)))

	items, err := f.scanner.blockMetaAccessor.GetSignedTxItems()
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, item := range items {
		left = append(left, item.Hash)
	}
	want := []string{creation.Hash().String(), blob.Hash().String()}
	sort.Strings(left)
	sort.Strings(want)
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Fatalf("signed items left: %v, want %v", left, want)
	}
}

// With the contract whitelist off, an aggregator nobody knows can call the router,
// and the deposit shows only in its logs. tx.To and the sender cannot find it.
func TestExtractTxsFetchesReceiptsWhenRouterInBloom(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter))
	tx := f.userTx(t, &gateElsewhere, []byte{1, 2, 3, 4})
	block := gateBlock(bloomOf(gateRouter.Bytes()), tx)

	txIn := f.scan(t, block, gateReceipt(tx, 1, f.depositLog(t, gateRouter)))

	f.assertFetched(t)
	if len(txIn.TxArray) != 1 || !strings.EqualFold(txIn.TxArray[0].To, f.vault.Hex()) {
		t.Fatalf("deposit via unknown contract not observed: %+v", txIn.TxArray)
	}
}

// During a router upgrade logs can come from any router the vaults still use.
func TestExtractTxsFetchesReceiptsWhenAnyRouterInBloom(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter), routerAddr(gateOldRouter))
	tx := f.userTx(t, &gateElsewhere, nil)
	f.scan(t, gateBlock(bloomOf(gateOldRouter.Bytes()), tx), gateReceipt(tx, 1))
	f.assertFetched(t)
}

// The log parser accepts whitelisted contracts as emitters when this flag is on,
// so the bloom must be tested against them too.
func TestExtractTxsFetchesReceiptsForWhitelistedEmitterWhenFlagOn(t *testing.T) {
	saved := useWhitelistSmartContract
	defer func() { useWhitelistSmartContract = saved }()
	emitter := ecommon.HexToAddress(whitelistSmartContractAddress[0].String())

	useWhitelistSmartContract = false
	f := newGateFixture(t, routerAddr(gateRouter))
	f.assertSkipped(t, f.scan(t, gateBlock(bloomOf(emitter.Bytes()), f.userTx(t, &gateElsewhere, nil))))

	useWhitelistSmartContract = true
	f = newGateFixture(t, routerAddr(gateRouter))
	tx := f.userTx(t, &gateElsewhere, nil)
	f.scan(t, gateBlock(bloomOf(emitter.Bytes()), tx), gateReceipt(tx, 1))
	f.assertFetched(t)
}

// Anything the filter cannot judge goes to the receipts.
func TestExtractTxsFetchesReceiptsWhenFilterCannotDecide(t *testing.T) {
	t.Run("no routers known", func(t *testing.T) {
		f := newGateFixture(t)
		tx := f.userTx(t, &gateElsewhere, nil)
		f.scan(t, gateBlock(unrelatedBloom, tx), gateReceipt(tx, 1))
		f.assertFetched(t)
	})
	t.Run("empty bloom on a block with txs", func(t *testing.T) {
		f := newGateFixture(t, routerAddr(gateRouter))
		tx := f.userTx(t, &gateElsewhere, nil)
		f.scan(t, gateBlock(etypes.Bloom{}, tx), gateReceipt(tx, 1))
		f.assertFetched(t)
	})
	t.Run("unrecoverable sender", func(t *testing.T) {
		f := newGateFixture(t, routerAddr(gateRouter))
		tx := etypes.NewTx(&etypes.LegacyTx{To: &gateElsewhere, GasPrice: big.NewInt(1), Gas: 21000})
		f.scan(t, gateBlock(unrelatedBloom, tx), gateReceipt(tx, 1))
		f.assertFetched(t)
	})
	t.Run("truncated transfer calldata", func(t *testing.T) {
		f := newGateFixture(t, routerAddr(gateRouter))
		token := gateToken(t)
		tx := f.userTx(t, &token, erc20TransferData(f.vault)[:20])
		f.scan(t, gateBlock(unrelatedBloom, tx), gateReceipt(tx, 1))
		f.assertFetched(t)
	})
}

// A native send to a vault emits no logs, so only its tx fields reveal it.
func TestExtractTxsFetchesReceiptsForTxToVault(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter))
	tx := f.userTx(t, &f.vault, []byte("SWAP:THOR.RUNE"))

	txIn := f.scan(t, gateBlock(unrelatedBloom, tx), gateReceipt(tx, 1))

	f.assertFetched(t)
	if len(txIn.TxArray) != 1 {
		t.Fatalf("got %d observations, want 1", len(txIn.TxArray))
	}
}

// A failed outbound emits no logs. Its status is only in the receipt, and it must
// be observed so the vault pays for the gas.
func TestExtractTxsFetchesReceiptsForFailedVaultTx(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter))
	tx := f.vaultTx(t, &gateElsewhere, []byte{1, 2, 3, 4})

	txIn := f.scan(t, gateBlock(unrelatedBloom, tx), gateReceipt(tx, 0))

	f.assertFetched(t)
	if len(txIn.TxArray) != 1 {
		t.Fatalf("got %d observations, want 1", len(txIn.TxArray))
	}
}

func TestExtractTxsFetchesCachedOutboundAfterVaultRemoval(t *testing.T) {
	for name, status := range map[string]uint64{"failed": etypes.ReceiptStatusFailed, "successful": etypes.ReceiptStatusSuccessful} {
		t.Run(name, func(t *testing.T) {
			f := newGateFixture(t, routerAddr(gateRouter))
			tx := f.vaultTx(t, &gateRouter, []byte{1, 2, 3, 4})
			_, vault := f.scanner.pubkeyMgr.IsValidPoolAddress(f.vault.Hex(), thorcommon.ETHChain)
			out := stypes.TxOutItem{Chain: thorcommon.ETHChain, VaultPubKey: vault.PubKey, Memo: "OUT:123"}
			cache := f.scanner.signerCacheManager
			if err := cache.SetSignedWithExpiry(out.CacheHash(), out.CacheVault(out.Chain), tx.Hash().Hex(), 1); err != nil {
				t.Fatal(err)
			}
			// Replay the block after the signing vault has left the node's vault list.
			f.scanner.pubkeyMgr.RemovePubKey(vault.PubKey)
			other := f.userTx(t, &gateElsewhere, nil)
			otherReceipt := gateReceipt(other, 1, f.depositLog(t, gateElsewhere))
			block := gateBlock(etypes.CreateBloom(otherReceipt), tx, other)
			f.scan(t, block, gateReceipt(tx, status), otherReceipt)
			f.assertFetched(t)
			if got := cache.HasSigned(out.CacheHash()); got != (status == etypes.ReceiptStatusSuccessful) {
				t.Fatalf("signer cache contains %s outbound: %v", name, got)
			}
		})
	}
}

// A vault's token approval goes to the token and its log comes from the token, so
// only the sender reveals it.
func TestExtractTxsFetchesReceiptsForTxFromVault(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter))
	token := gateToken(t)
	tx := f.vaultTx(t, &token, []byte{0x09, 0x5e, 0xa7, 0xb3})

	txIn := f.scan(t, gateBlock(unrelatedBloom, tx), gateReceipt(tx, 1))

	f.assertFetched(t)
	if len(txIn.TxArray) != 1 {
		t.Fatalf("got %d observations, want 1", len(txIn.TxArray))
	}
}

// Recovering every sender costs far more than the rest of the filter, and the RPC
// response already reports them.
func TestExtractTxsUsesSenderReportedByRPC(t *testing.T) {
	t.Run("reported as vault", func(t *testing.T) {
		f := newGateFixture(t, routerAddr(gateRouter))
		tx := f.userTx(t, &gateElsewhere, nil)
		block := f.decodedBlock(t, tx, f.vault)

		f.scan(t, block, gateReceipt(tx, 1))

		f.assertFetched(t)
	})
	t.Run("reported as someone else", func(t *testing.T) {
		f := newGateFixture(t, routerAddr(gateRouter))
		tx := f.vaultTx(t, &gateElsewhere, nil)
		block := f.decodedBlock(t, tx, gateElsewhere)

		f.assertSkipped(t, f.scan(t, block))
	})
}

// A block not decoded from an RPC response has no reported senders. That must fall
// back to recovering them, not cost one RPC per tx.
func TestExtractTxsSenderLookupNeverCallsRPC(t *testing.T) {
	t.Run("sender not a vault", func(t *testing.T) {
		f := newGateFixture(t, routerAddr(gateRouter))
		tx := f.userTx(t, &gateElsewhere, nil)

		f.assertSkipped(t, f.scan(t, gateBlock(unrelatedBloom, tx)))
	})
	t.Run("sender a vault", func(t *testing.T) {
		f := newGateFixture(t, routerAddr(gateRouter))
		tx := f.vaultTx(t, &gateElsewhere, nil)

		f.scan(t, gateBlock(unrelatedBloom, tx), gateReceipt(tx, 1))

		f.assertFetched(t)
		if got := f.otherCalls.Load(); got != 0 {
			t.Fatalf("got %d other RPC calls, want 0", got)
		}
	})
}

// A tx to a router or a whitelisted aggregator only produces an observation through
// a router log, so with no router in the bloom it is no reason to fetch.
func TestExtractTxsSkipsReceiptsForTxToKnownContractWithoutRouterLog(t *testing.T) {
	aggregator := ecommon.HexToAddress(whitelistSmartContractAddress[0].String())
	for name, to := range map[string]ecommon.Address{"router": gateRouter, "whitelisted": aggregator} {
		t.Run(name, func(t *testing.T) {
			f := newGateFixture(t, routerAddr(gateRouter))
			f.assertSkipped(t, f.scan(t, gateBlock(unrelatedBloom, f.userTx(t, &to, []byte{1, 2, 3, 4}))))
		})
	}
}

// A direct ERC20 inbound names the vault only in its calldata and token log.
func TestExtractTxsFetchesReceiptsForERC20TransferToVault(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter))
	token := gateToken(t)
	tx := f.userTx(t, &token, erc20TransferData(f.vault))
	f.scan(t, gateBlock(unrelatedBloom, tx), gateReceipt(tx, 1))
	f.assertFetched(t)
}

func TestExtractTxsDirectERC20TransferRecipient(t *testing.T) {
	for _, tc := range []struct {
		addressedToVault bool
		upperByte        byte
	}{
		{addressedToVault: false},
		{addressedToVault: true},
		{addressedToVault: false, upperByte: 0xff},
		{addressedToVault: true, upperByte: 0xff},
	} {
		f := newGateFixture(t, routerAddr(gateRouter))
		token := gateToken(t)
		if err := f.scanner.tokens.SaveTokenMeta("TKN", token.Hex(), 18); err != nil {
			t.Fatal(err)
		}
		recipient := gateElsewhere
		if tc.addressedToVault {
			recipient = f.vault
		}
		data := erc20TransferData(recipient)
		data[4] = tc.upperByte
		tx := f.signedTx(t, secp256k1.GenPrivKey(), &token, new(big.Int), data)
		sender, err := f.signer.Sender(tx)
		if err != nil {
			t.Fatal(err)
		}
		transfer := func(to ecommon.Address, amount int64) *etypes.Log {
			return &etypes.Log{
				Address: token,
				TxHash:  tx.Hash(),
				Topics:  []ecommon.Hash{ecommon.HexToHash(testERC20TransferEvent), paddedAddressHash(sender), paddedAddressHash(to)},
				Data:    ecommon.BigToHash(big.NewInt(amount)).Bytes(),
			}
		}
		// Both receipts pay the vault 0.1 tokens, regardless of the 1 token requested in calldata.
		receipt := gateReceipt(tx, etypes.ReceiptStatusSuccessful, transfer(gateElsewhere, 9e17), transfer(f.vault, 1e17))
		txIn := f.scan(t, gateBlock(etypes.CreateBloom(receipt), tx), receipt)
		if !tc.addressedToVault {
			f.assertSkipped(t, txIn)
			full, err := f.scanner.fromTxToTxInWithReceipts(tx, map[ecommon.Hash]*etypes.Receipt{tx.Hash(): receipt}, false)
			if err != nil || full != nil {
				t.Fatalf("receipt path credited an incidental transfer: observation=%v err=%v", full, err)
			}
			continue
		}
		f.assertFetched(t)
		if len(txIn.TxArray) != 1 {
			t.Fatalf("got %d vault transfers, want 1", len(txIn.TxArray))
		}
		item := txIn.TxArray[0]
		if !strings.EqualFold(item.To, f.vault.Hex()) || len(item.Coins) != 1 || item.Coins[0].Amount.Uint64() != 10_000_000 {
			t.Fatalf("unexpected vault payment: %+v", item)
		}
	}
}

// extractTxs passes over blob txs and contract creations, whoever sent them, so they
// are no reason to fetch.
func TestExtractTxsIgnoresTxsItNeverParses(t *testing.T) {
	f := newGateFixture(t, routerAddr(gateRouter))
	ecdsaKey, err := crypto.ToECDSA(f.vaultKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := etypes.SignNewTx(ecdsaKey, f.signer, &etypes.BlobTx{
		ChainID: uint256.NewInt(Mainnet), To: f.vault, BlobFeeCap: uint256.NewInt(1), BlobHashes: []ecommon.Hash{{1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.assertSkipped(t, f.scan(t, gateBlock(unrelatedBloom, blob, f.vaultTx(t, nil, nil))))
}

// observed reduces observations to what THORChain would see, ignoring goroutine order.
func observed(txIn stypes.TxIn) []string {
	out := make([]string, 0, len(txIn.TxArray))
	for _, item := range txIn.TxArray {
		raw, _ := json.Marshal(item)
		out = append(out, string(raw))
	}
	sort.Strings(out)
	return out
}

// The property behind the filter: on any block with a truthful bloom, skipping must
// lose nothing. Each random block is scanned with its own bloom, and again with a
// bloom that forces the receipts path. Both must give the same observations, with the
// contract whitelist on and off.
func TestExtractTxsFilterMatchesReceiptsPath(t *testing.T) {
	type gen func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log)
	gens := map[string]gen{
		"unrelated": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.userTx(t, &gateElsewhere, nil), 1, nil
		},
		"unrelated_call": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			tx := f.userTx(t, &gateElsewhere, []byte{1, 2, 3, 4})
			return tx, 1, []*etypes.Log{{Address: gateElsewhere, Topics: []ecommon.Hash{{1}}, Data: []byte{1}}}
		},
		"failed": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.userTx(t, &gateElsewhere, []byte{1, 2, 3, 4}), 0, nil
		},
		"router_deposit": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.userTx(t, &gateRouter, []byte{1, 2, 3, 4}), 1, []*etypes.Log{f.depositLog(t, gateRouter)}
		},
		"aggregator": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.userTx(t, &gateElsewhere, []byte{1, 2, 3, 4}), 1, []*etypes.Log{f.depositLog(t, gateRouter)}
		},
		"router_no_log": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.userTx(t, &gateRouter, []byte{1, 2, 3, 4}), 1, nil
		},
		"router_failed": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.userTx(t, &gateRouter, []byte{1, 2, 3, 4}), 0, nil
		},
		"whitelisted_no_router_log": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			to := ecommon.HexToAddress(whitelistSmartContractAddress[0].String())
			return f.userTx(t, &to, []byte{1, 2, 3, 4}), 1, []*etypes.Log{{Address: gateElsewhere, Topics: []ecommon.Hash{{1}}, Data: []byte{1}}}
		},
		"whitelisted_deposit": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			to := ecommon.HexToAddress(whitelistSmartContractAddress[0].String())
			return f.userTx(t, &to, []byte{1, 2, 3, 4}), 1, []*etypes.Log{f.depositLog(t, gateRouter)}
		},
		"to_vault": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.userTx(t, &f.vault, []byte("SWAP:THOR.RUNE")), 1, nil
		},
		"from_vault": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.vaultTx(t, &gateElsewhere, nil), 1, nil
		},
		"from_vault_fail": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			return f.vaultTx(t, &gateElsewhere, []byte{1, 2, 3, 4}), 0, nil
		},
		"direct_erc20": func(f *gateFixture) (*etypes.Transaction, uint64, []*etypes.Log) {
			token := gateToken(t)
			key := secp256k1.GenPrivKey()
			tx := f.signedTx(t, key, &token, big.NewInt(0), erc20TransferData(f.vault))
			sender, err := etypes.Sender(f.signer, tx)
			if err != nil {
				t.Fatal(err)
			}
			return tx, 1, []*etypes.Log{{
				Address: token,
				Topics:  []ecommon.Hash{ecommon.HexToHash(testERC20TransferEvent), paddedAddressHash(sender), paddedAddressHash(f.vault)},
				Data:    ecommon.LeftPadBytes(big.NewInt(1e18).Bytes(), ecommon.HashLength),
			}}
		},
	}
	names := make([]string, 0, len(gens))
	for name := range gens {
		names = append(names, name)
	}
	sort.Strings(names)

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // fixed seed: a reproducible differential test
	skips, observations := 0, 0
	for trial := 0; trial < 300; trial++ {
		f := newGateFixture(t, routerAddr(gateRouter))
		f.mimir = int64(trial % 2)

		var txs []*etypes.Transaction
		var receipts []*etypes.Receipt
		var bloom etypes.Bloom
		bloom.Add(gateElsewhere.Bytes())
		for i := rng.Intn(4) + 1; i > 0; i-- {
			// mostly unrelated, as on mainnet, so that some blocks do skip
			name := names[rng.Intn(len(names))]
			if rng.Intn(2) == 0 {
				name = "unrelated"
			}
			tx, status, logs := gens[name](f)
			txs = append(txs, tx)
			receipts = append(receipts, gateReceipt(tx, status, logs...))
			for _, log := range logs {
				bloom.Add(log.Address.Bytes())
				for _, topic := range log.Topics {
					bloom.Add(topic.Bytes())
				}
			}
		}

		withFilter := observed(f.scan(t, gateBlock(bloom, txs...), receipts...))
		if f.receiptCalls.Load() == 0 {
			skips++
		}
		forced := bloom
		forced.Add(gateRouter.Bytes())
		f.receiptCalls.Store(0)
		viaReceipts := observed(f.scan(t, gateBlock(forced, txs...), receipts...))
		if f.receiptCalls.Load() != 1 {
			t.Fatalf("trial %d: forced bloom did not fetch receipts", trial)
		}
		observations += len(viaReceipts)
		if strings.Join(withFilter, "\n") != strings.Join(viaReceipts, "\n") {
			t.Fatalf("trial %d: filter observed\n%v\nreceipts path observed\n%v", trial, withFilter, viaReceipts)
		}
	}
	// guard against the property passing because nothing happened
	if skips == 0 || observations == 0 {
		t.Fatalf("no coverage: %d skipped blocks, %d observations", skips, observations)
	}
	t.Logf("%d skipped blocks, %d observations", skips, observations)
}
