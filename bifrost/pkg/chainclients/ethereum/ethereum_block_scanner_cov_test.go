package ethereum

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	tokenlist "gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// covChain serves blocks, txs and receipts keyed by height / hash.
type covChain struct {
	mu       sync.Mutex
	blocks   map[uint64]json.RawMessage
	receipts map[string]json.RawMessage
	txs      map[string]json.RawMessage
	rcptErr  map[string]bool
}

func newCovChain(env *covEnv) *covChain {
	ch := &covChain{
		blocks:   map[uint64]json.RawMessage{},
		receipts: map[string]json.RawMessage{},
		txs:      map[string]json.RawMessage{},
		rcptErr:  map[string]bool{},
	}
	env.rpc.set("eth_getBlockByNumber", func(p []json.RawMessage) (any, error) {
		var num string
		_ = json.Unmarshal(p[0], &num)
		n, ok := new(big.Int).SetString(strings.TrimPrefix(num, "0x"), 16)
		if !ok {
			return nil, errors.New("bad block number")
		}
		ch.mu.Lock()
		defer ch.mu.Unlock()
		if b, found := ch.blocks[n.Uint64()]; found {
			return b, nil
		}
		return nil, nil
	})
	env.rpc.set("eth_getTransactionReceipt", func(p []json.RawMessage) (any, error) {
		var h string
		_ = json.Unmarshal(p[0], &h)
		ch.mu.Lock()
		defer ch.mu.Unlock()
		if ch.rcptErr[strings.ToLower(h)] {
			return nil, errors.New("receipt unavailable")
		}
		if r, found := ch.receipts[strings.ToLower(h)]; found {
			return r, nil
		}
		return nil, nil
	})
	env.rpc.set("eth_getTransactionByHash", func(p []json.RawMessage) (any, error) {
		var h string
		_ = json.Unmarshal(p[0], &h)
		ch.mu.Lock()
		defer ch.mu.Unlock()
		if r, found := ch.txs[strings.ToLower(h)]; found {
			return r, nil
		}
		return nil, nil
	})
	return ch
}

func (ch *covChain) addBlock(t *testing.T, header *etypes.Header, txs ...*etypes.Transaction) {
	t.Helper()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.blocks[header.Number.Uint64()] = covBlockJSON(t, header, txs)
}

func (ch *covChain) addReceipt(tx *etypes.Transaction, status uint64, logs []*etypes.Log) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.receipts[strings.ToLower(tx.Hash().Hex())] = covReceiptJSON(tx.Hash(), status, logs)
}

func covUserTx(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to ecommon.Address, value *big.Int, data []byte) *etypes.Transaction {
	t.Helper()
	tx, err := etypes.SignTx(etypes.NewTx(&etypes.DynamicFeeTx{
		ChainID: big.NewInt(covChainID), Nonce: nonce, To: &to, Value: value, Gas: 100_000,
		GasFeeCap: big.NewInt(3_000_000_000), GasTipCap: big.NewInt(2_000_000_000), Data: data,
	}), etypes.LatestSignerForChainID(big.NewInt(covChainID)), key)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestCovFetchTxs(t *testing.T) {
	env := newCovEnv(t, nil)
	e := env.client.ethScanner
	ch := newCovChain(env)
	user, _ := crypto.GenerateKey()
	vault := ecommon.HexToAddress(env.vaultAddr.String())
	oneEth := big.NewInt(1e18)

	// inbound to vault with a hex memo
	inbound := covUserTx(t, user, 0, vault, oneEth, []byte(hex.EncodeToString([]byte("ADD:ETH.ETH"))))
	ch.addReceipt(inbound, 1, nil)
	// dust inbound is dropped
	dust := covUserTx(t, user, 1, vault, big.NewInt(1), nil)
	ch.addReceipt(dust, 1, nil)
	// tx without receipt is ignored
	noReceipt := covUserTx(t, user, 2, vault, oneEth, nil)
	// receipt rpc failure is skipped
	rcptFail := covUserTx(t, user, 3, vault, oneEth, nil)
	ch.rcptErr[strings.ToLower(rcptFail.Hash().Hex())] = true
	// unrelated tx is ignored
	unrelated := covUserTx(t, user, 4, ecommon.HexToAddress("0x1234"), oneEth, nil)
	ch.addReceipt(unrelated, 1, nil)
	// failed outbound from the vault reports the burnt gas
	to := ecommon.HexToAddress(covRouter.String())
	failed := env.covSignedTx(t, etypes.NewTransaction(9, to, big.NewInt(0), 50_000, big.NewInt(1e9), nil))
	ch.addReceipt(failed, 0, nil)
	// contract creation is skipped
	create, _ := etypes.SignTx(etypes.NewContractCreation(5, big.NewInt(0), 100_000, big.NewInt(1e9), []byte{0x60}),
		etypes.LatestSignerForChainID(big.NewInt(covChainID)), user)

	ch.addBlock(t, covHeader(10, ecommon.Hash{}, true), inbound, dust, noReceipt, rcptFail, unrelated, failed, create)

	txIn, err := e.FetchTxs(10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(txIn.TxArray) != 2 {
		t.Fatalf("expected 2 txs, got %d: %+v", len(txIn.TxArray), txIn.TxArray)
	}
	byTx := map[string]*stypes.TxInItem{}
	for _, it := range txIn.TxArray {
		byTx[it.Tx] = it
	}
	in := byTx[inbound.Hash().Hex()[2:]]
	if in == nil || in.Memo != "ADD:ETH.ETH" || in.BlockHeight != 10 || !in.Coins[0].Amount.Equal(cosmos.NewUint(common.One)) {
		t.Fatalf("inbound = %+v", in)
	}
	f := byTx[failed.Hash().Hex()[2:]]
	if f == nil || !strings.HasPrefix(f.Memo, "OUT:") || !strings.EqualFold(f.Sender, env.vaultAddr.String()) || f.Gas.IsEmpty() {
		t.Fatalf("failed outbound = %+v", f)
	}
	if e.getCurrentBlockHeight() != 10 {
		t.Fatalf("height = %d", e.getCurrentBlockHeight())
	}
	meta, err := e.blockMetaAccessor.GetBlockMeta(10)
	if err != nil || meta == nil || len(meta.Transactions) != 2 {
		t.Fatalf("block meta = %+v err=%v", meta, err)
	}

	// network fee posted once: base fee 1 gwei + 25th pct tip rounded up to 1 gwei
	select {
	case nf := <-env.feeQ:
		if nf.Height != 10 || nf.TransactionRate == 0 || nf.TransactionSize != 100_000 {
			t.Fatalf("network fee = %+v", nf)
		}
	default:
		t.Fatal("expected a network fee")
	}

	// same fee again is not re-posted; far behind tip is not posted
	ch.addBlock(t, covHeader(11, ecommon.Hash{}, false))
	ch.addBlock(t, covHeader(12, ecommon.Hash{}, false))
	if _, err = e.FetchTxs(11, 11); err != nil {
		t.Fatal(err)
	}
	if _, err = e.FetchTxs(12, 100); err != nil {
		t.Fatal(err)
	}
	select {
	case nf := <-env.feeQ:
		t.Fatalf("unexpected network fee %+v", nf)
	default:
	}
	// metas older than the reorg window (0 here) are pruned
	if meta, _ = e.blockMetaAccessor.GetBlockMeta(10); meta != nil {
		t.Fatalf("block meta 10 should be pruned, got %+v", meta)
	}

	// missing block / rpc failure
	if _, err = e.FetchTxs(99, 99); !errors.Is(err, btypes.ErrUnavailableBlock) {
		t.Fatalf("expected unavailable block, got %v", err)
	}
	env.rpc.set("eth_getBlockByNumber", covErr("down"))
	if _, err = e.FetchTxs(10, 10); err == nil || errors.Is(err, btypes.ErrUnavailableBlock) {
		t.Fatalf("expected fetch error, got %v", err)
	}
}

func TestCovProcessReorg(t *testing.T) {
	env := newCovEnv(t, nil)
	e := env.client.ethScanner
	e.cfg.MaxReorgRescanBlocks = 100 // keep stored metas for the reorg check
	ch := newCovChain(env)
	user, _ := crypto.GenerateKey()
	vault := ecommon.HexToAddress(env.vaultAddr.String())

	kept := covUserTx(t, user, 0, vault, big.NewInt(1e18), nil)
	gone := covUserTx(t, user, 1, vault, big.NewInt(1e18), nil)
	pending := covUserTx(t, user, 2, vault, big.NewInt(1e18), nil)
	failedTx := covUserTx(t, user, 3, vault, big.NewInt(1e18), nil)
	noRcpt := covUserTx(t, user, 4, vault, big.NewInt(1e18), nil)
	ch.txs[strings.ToLower(kept.Hash().Hex())] = covTxByHash(t, kept, true)
	ch.txs[strings.ToLower(pending.Hash().Hex())] = covTxByHash(t, pending, false)
	ch.txs[strings.ToLower(failedTx.Hash().Hex())] = covTxByHash(t, failedTx, true)
	ch.txs[strings.ToLower(noRcpt.Hash().Hex())] = covTxByHash(t, noRcpt, true)
	ch.addReceipt(kept, 1, nil)
	ch.addReceipt(failedTx, 0, nil)
	ch.rcptErr[strings.ToLower(noRcpt.Hash().Hex())] = true

	// stored meta for height 9 with a stale hash
	stale := evmtypes.NewBlockMeta(covHeader(9, ecommon.HexToHash("0xaa"), false), stypes.TxIn{})
	for _, tx := range []*etypes.Transaction{kept, gone, pending, failedTx, noRcpt} {
		stale.Transactions = append(stale.Transactions, evmtypes.TransactionMeta{Hash: tx.Hash().Hex(), BlockHeight: 9})
	}
	if err := e.blockMetaAccessor.SaveBlockMeta(9, stale); err != nil {
		t.Fatal(err)
	}
	// stored meta for height 8 whose header can no longer be fetched
	if err := e.blockMetaAccessor.SaveBlockMeta(8, evmtypes.NewBlockMeta(covHeader(8, ecommon.Hash{}, false), stypes.TxIn{})); err != nil {
		t.Fatal(err)
	}

	// the canonical block 9 now holds the kept tx
	newNine := covHeader(9, ecommon.HexToHash("0xbb"), true)
	ch.addBlock(t, newNine, kept)
	// block 10 points at the canonical 9, not the stored one
	ten := covHeader(10, newNine.Hash(), false)
	ch.addBlock(t, ten)

	txIn, err := e.FetchTxs(10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(txIn.TxArray) != 1 || txIn.TxArray[0].Tx != kept.Hash().Hex()[2:] || txIn.TxArray[0].BlockHeight != 9 {
		t.Fatalf("rescanned txs = %+v", txIn.TxArray)
	}
	select {
	case errata := <-env.errataQ:
		if errata.Height != 9 || len(errata.Txs) != 2 {
			t.Fatalf("errata = %+v", errata)
		}
		got := map[string]bool{}
		for _, tx := range errata.Txs {
			got[tx.TxID.String()] = true
		}
		if !got[gone.Hash().Hex()] || !got[failedTx.Hash().Hex()] {
			t.Fatalf("errata txs = %+v", errata.Txs)
		}
	default:
		t.Fatal("expected errata for removed txs")
	}
	meta, err := e.blockMetaAccessor.GetBlockMeta(9)
	if err != nil || !strings.EqualFold(meta.BlockHash, newNine.Hash().Hex()) || len(meta.Transactions) != 3 {
		t.Fatalf("updated meta = %+v err=%v", meta, err)
	}

	// matching parent hash -> no reorg
	reorged, err := e.processReorg(covHeader(11, ten.Hash(), false))
	if err != nil || reorged != nil {
		t.Fatalf("no reorg expected, got %v %v", reorged, err)
	}
}

func TestCovTokenMetadata(t *testing.T) {
	env := newCovEnv(t, nil)
	e := env.client.ethScanner
	listed := tokenlist.GetETHTokenList().Tokens[0].Address

	symbolSel := "0x" + hex.EncodeToString(e.erc20ABI.Methods["symbol"].ID)
	decimalsSel := "0x" + hex.EncodeToString(e.erc20ABI.Methods["decimals"].ID)
	symbolOut, _ := e.erc20ABI.Methods["symbol"].Outputs.Pack("U.S-D+C")
	var decimals any = covUint256(big.NewInt(6))
	var symbol any = "0x" + hex.EncodeToString(symbolOut)
	env.rpc.set("eth_call", func(p []json.RawMessage) (any, error) {
		s := string(p[0])
		switch {
		case strings.Contains(s, symbolSel):
			if err, ok := symbol.(error); ok {
				return nil, err
			}
			return symbol, nil
		case strings.Contains(s, decimalsSel):
			if err, ok := decimals.(error); ok {
				return nil, err
			}
			return decimals, nil
		}
		return nil, errors.New("unexpected call")
	})

	if d, err := e.getDecimals(ethToken); err != nil || d != 18 {
		t.Fatalf("eth decimals = %d %v", d, err)
	}
	if d, err := e.getDecimals(listed); err != nil || d != 6 {
		t.Fatalf("decimals = %d %v", d, err)
	}
	if s, err := e.getSymbol(ethToken); err != nil || s != "ETH" {
		t.Fatalf("eth symbol = %s %v", s, err)
	}

	// non-whitelisted tokens are rejected
	if _, err := e.getTokenMeta("0x9999999999999999999999999999999999999999"); err == nil {
		t.Fatal("expected not whitelisted error")
	}
	if _, err := e.GetAssetFromTokenAddress("0x9999999999999999999999999999999999999999"); err == nil {
		t.Fatal("expected asset error")
	}
	if d := e.GetTokenDecimalsForTHORChain("0x9999999999999999999999999999999999999999"); d != 0 {
		t.Fatalf("unknown token decimals = %d", d)
	}

	// whitelisted token metadata is discovered, sanitised and cached
	tm, err := e.getTokenMeta(listed)
	if err != nil || tm.Symbol != "USDC" || tm.Decimal != 6 {
		t.Fatalf("token meta = %+v %v", tm, err)
	}
	asset, err := e.GetAssetFromTokenAddress(listed)
	if err != nil || asset.String() != "ETH.USDC-"+strings.ToUpper(listed) {
		t.Fatalf("asset = %s %v", asset, err)
	}
	if a, err2 := e.GetAssetFromTokenAddress(ethToken); err2 != nil || !a.Equals(common.ETHAsset) {
		t.Fatalf("eth asset = %s", a)
	}
	if d := e.GetTokenDecimalsForTHORChain(listed); d != 6 {
		t.Fatalf("thorchain decimals = %d", d)
	}
	if d := e.GetTokenDecimalsForTHORChain(ethToken); d != 0 {
		t.Fatalf("eth thorchain decimals = %d", d)
	}
	// 1 USDC (6 decimals) -> 1e8
	if got := e.ConvertAmount(listed, big.NewInt(1_000_000)); !got.Equal(cosmos.NewUint(common.One)) {
		t.Fatalf("convert = %s", got)
	}

	// decimal call failures default to 18; undecodable symbol is used raw
	second := tokenlist.GetETHTokenList().Tokens[1].Address
	decimals = errors.New("reverted")
	symbol = "0x" + hex.EncodeToString([]byte("RAW"))
	tm, err = e.getTokenMeta(second)
	if err != nil || tm.Symbol != "RAW" || tm.Decimal != 18 {
		t.Fatalf("fallback token meta = %+v %v", tm, err)
	}
	decimals = "0x"
	if _, err = e.getDecimals(second); err == nil {
		t.Fatal("expected unpack error")
	}
	// symbol call failure
	third := tokenlist.GetETHTokenList().Tokens[2].Address
	symbol = errors.New("reverted")
	if _, err = e.getTokenMeta(third); err == nil {
		t.Fatal("expected symbol error")
	}

	if e.isRelevantERC20Token(nil) {
		t.Fatal("nil address is not relevant")
	}
}

func TestCovScannerMimirAndSolvency(t *testing.T) {
	env := newCovEnv(t, nil)
	e := env.client.ethScanner
	env.bridge.mimirErr = errors.New("down")
	if e.directERC20InboundEnabled() {
		t.Fatal("mimir error should disable direct ERC20 inbounds")
	}
	env.bridge.mimirErr = nil
	env.bridge.setMimir(strings.ReplaceAll(constants.MimirTemplateEVMDirectERC20Inbound, "%s", "ETH"), 1)
	if !e.directERC20InboundEnabled() {
		t.Fatal("mimir should enable direct ERC20 inbounds")
	}

	var reported []int64
	e.solvencyReporter = func(h int64) error {
		reported = append(reported, h)
		return nil
	}
	e.cfg.ObservationFlexibilityBlocks = 5
	if err := e.ReportSolvency(10, 20); err != nil {
		t.Fatal(err)
	}
	if err := e.ReportSolvency(18, 20); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || reported[0] != 18 {
		t.Fatalf("reported = %v", reported)
	}
	e.solvencyReporter = nil
	if err := e.ReportSolvency(20, 20); err != nil {
		t.Fatal(err)
	}
}

func TestCovFromTxToTxInRouting(t *testing.T) {
	env := newCovEnv(t, nil)
	e := env.client.ethScanner
	ch := newCovChain(env)
	user, _ := crypto.GenerateKey()
	userAddr := crypto.PubkeyToAddress(user.PublicKey)
	if err := e.tokens.SaveTokenMeta("TKN", covToken, 18); err != nil {
		t.Fatal(err)
	}
	token := ecommon.HexToAddress(covToken)
	vault := ecommon.HexToAddress(env.vaultAddr.String())
	transfer := erc20TransferCallData(vault, big.NewInt(1e18))
	transferLog := &etypes.Log{
		Address: token,
		Topics:  []ecommon.Hash{ecommon.HexToHash(testERC20TransferEvent), paddedAddressHash(userAddr), paddedAddressHash(vault)},
		Data:    ecommon.LeftPadBytes(big.NewInt(1e18).Bytes(), 32),
	}

	unknownToken := covUserTx(t, user, 0, ecommon.HexToAddress("0x5555555555555555555555555555555555555555"), big.NewInt(0), transfer)
	ch.addReceipt(unknownToken, 1, nil)
	noLogs := covUserTx(t, user, 1, token, big.NewInt(0), transfer)
	ch.addReceipt(noLogs, 1, nil)
	withLog := covUserTx(t, user, 2, token, big.NewInt(0), transfer)
	ch.addReceipt(withLog, 1, []*etypes.Log{transferLog})
	random := covUserTx(t, user, 3, ecommon.HexToAddress("0x6666666666666666666666666666666666666666"), big.NewInt(1e18), []byte{1, 2, 3, 4})
	ch.addReceipt(random, 1, nil)
	// vault approval / cancel (no value)
	approval := env.covSignedTx(t, etypes.NewTransaction(1, token, big.NewInt(0), 50_000, big.NewInt(1e9), []byte("0xdeadbeef")))
	ch.addReceipt(approval, 1, nil)
	cancel := env.covSignedTx(t, etypes.NewTransaction(2, vault, big.NewInt(0), 50_000, big.NewInt(1e9), []byte("cancel")))
	ch.addReceipt(cancel, 1, nil)

	for _, disable := range []int64{0, 1} {
		env.bridge.setMimir(constants.EVMDisableContractWhitelist.String(), disable)
		env.bridge.setMimir(strings.ReplaceAll(constants.MimirTemplateEVMDirectERC20Inbound, "%s", "ETH"), 0)
		for name, tx := range map[string]*etypes.Transaction{"unknown token": unknownToken, "no logs": noLogs, "flag off": withLog, "random": random} {
			item, err := e.fromTxToTxIn(tx)
			if err != nil || item != nil {
				t.Fatalf("disable=%d %s: expected ignore, got %+v %v", disable, name, item, err)
			}
		}
		// approval from vault: memo cleared, observable dust amount
		item, err := e.fromTxToTxIn(approval)
		if err != nil || item == nil || item.Memo != "" || !item.Coins[0].Amount.Equal(common.ETHChain.DustThreshold()) {
			t.Fatalf("disable=%d approval = %+v %v", disable, item, err)
		}
		// cancel to self keeps memo
		item, err = e.fromTxToTxIn(cancel)
		if err != nil || item == nil || item.Memo != "cancel" {
			t.Fatalf("disable=%d cancel = %+v %v", disable, item, err)
		}
		// direct ERC20 inbound once enabled
		env.bridge.setMimir(strings.ReplaceAll(constants.MimirTemplateEVMDirectERC20Inbound, "%s", "ETH"), 1)
		item, err = e.fromTxToTxIn(withLog)
		if err != nil || item == nil || !strings.EqualFold(item.To, env.vaultAddr.String()) {
			t.Fatalf("disable=%d direct erc20 = %+v %v", disable, item, err)
		}
	}

	// whitelist mimir failure falls back to whitelist mode
	env.bridge.mimirErr = errors.New("down")
	if item, err := e.fromTxToTxIn(random); err != nil || item != nil {
		t.Fatalf("random with mimir error = %+v %v", item, err)
	}
	env.bridge.mimirErr = nil

	if item, err := e.fromTxToTxIn(nil); item != nil || err != nil {
		t.Fatal("nil tx should be ignored")
	}
}

func TestCovSmartContractAndFailedTx(t *testing.T) {
	env := newCovEnv(t, nil)
	e := env.client.ethScanner
	user, _ := crypto.GenerateKey()
	router := ecommon.HexToAddress(covRouter.String())
	tx := covUserTx(t, user, 0, router, big.NewInt(1e18), nil)

	// failed receipt is ignored by the smart contract parser
	if item, err := e.getTxInFromSmartContract(tx, &etypes.Receipt{Status: 0}, 0); item != nil || err != nil {
		t.Fatalf("failed receipt = %+v %v", item, err)
	}
	// receipt without deposit logs has no coins
	rcpt := &etypes.Receipt{Status: 1, EffectiveGasPrice: big.NewInt(1e9), GasUsed: 21000}
	if item, err := e.getTxInFromSmartContract(tx, rcpt, 0); item != nil || err != nil {
		t.Fatalf("no coins = %+v %v", item, err)
	}
	// conflicting deposit events (different vaults) are rejected
	deposit := e.vaultABI.Events["Deposit"]
	data, err := deposit.Inputs.NonIndexed().Pack(big.NewInt(1e18), "ADD:ETH.ETH")
	if err != nil {
		t.Fatal(err)
	}
	depositLog := func(to ecommon.Address) *etypes.Log {
		return &etypes.Log{Address: router, Topics: []ecommon.Hash{deposit.ID, paddedAddressHash(to), {}}, Data: data}
	}
	rcpt.Logs = []*etypes.Log{depositLog(ecommon.HexToAddress("0x01")), depositLog(ecommon.HexToAddress("0x02"))}
	if _, err = e.getTxInFromSmartContract(tx, rcpt, 0); err == nil || !strings.Contains(err.Error(), "fail to parse logs") {
		t.Fatalf("expected parse error, got %v", err)
	}
	// single deposit to the vault via the expected router is observed
	vault := ecommon.HexToAddress(env.vaultAddr.String())
	rcpt.Logs = []*etypes.Log{depositLog(vault)}
	item, err := e.getTxInFromSmartContract(tx, rcpt, 0)
	if err != nil || item == nil || item.Memo != "ADD:ETH.ETH" || !item.Coins[0].Amount.Equal(cosmos.NewUint(common.One)) {
		t.Fatalf("deposit = %+v %v", item, err)
	}
	// deposit to the vault through a different router is ignored
	whitelistRouter := ecommon.HexToAddress(whitelistSmartContractAddress[0].String())
	wrong := depositLog(vault)
	wrong.Address = whitelistRouter
	rcpt.Logs = []*etypes.Log{wrong}
	if item, err = e.getTxInFromSmartContract(tx, rcpt, 0); err != nil || item != nil {
		t.Fatalf("wrong router deposit = %+v %v", item, err)
	}

	// failed tx helper only reports vault outbounds
	if item := e.getTxInFromFailedTransaction(tx, &etypes.Receipt{Status: 1}); item != nil {
		t.Fatal("successful tx should not be reported as failed")
	}
	if item := e.getTxInFromFailedTransaction(tx, &etypes.Receipt{Status: 0, EffectiveGasPrice: big.NewInt(1)}); item != nil {
		t.Fatal("non-vault sender should be ignored")
	}
	unsigned := etypes.NewTransaction(0, router, big.NewInt(0), 1, big.NewInt(1), nil)
	if item := e.getTxInFromFailedTransaction(unsigned, &etypes.Receipt{Status: 0}); item != nil {
		t.Fatal("unsigned tx should be ignored")
	}
	if item, err := e.getTxInFromTransaction(unsigned, rcpt); item != nil || err == nil {
		t.Fatal("unsigned tx should error")
	}
}

// TestCovGetDecimalsUint256ABI: with an ABI declaring decimals() as uint256 the
// result decodes to *big.Int, whose value is used (the bundled ABI declares
// uint8); values beyond uint64 are rejected rather than truncated.
func TestCovGetDecimalsUint256ABI(t *testing.T) {
	env := newCovEnv(t, nil)
	e := env.client.ethScanner
	bigABI, err := abi.JSON(strings.NewReader(`[{"constant":true,"inputs":[],"name":"decimals","outputs":[{"name":"","type":"uint256"}],"type":"function"}]`))
	if err != nil {
		t.Fatal(err)
	}
	e.erc20ABI = &bigABI

	decimals := big.NewInt(9)
	env.rpc.set("eth_call", func([]json.RawMessage) (any, error) { return covUint256(decimals), nil })
	token := "0x" + strings.Repeat("ab", 20)
	if d, err := e.getDecimals(token); err != nil || d != 9 {
		t.Fatalf("decimals = %d %v, want 9", d, err)
	}

	decimals = new(big.Int).Lsh(big.NewInt(1), 64)
	if d, err := e.getDecimals(token); err == nil || !strings.Contains(err.Error(), "invalid decimals") || d != defaultDecimals {
		t.Fatalf("decimals = %d %v, want default with invalid decimals error", d, err)
	}
}
