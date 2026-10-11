package evm

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"

	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const covTknContract = "0x333C3310824B7C685133F2BeDb2CA4b8b4DF633d"

// covChain holds blocks, txs and receipts served by the fake RPC.
type covChain struct {
	mu       sync.Mutex
	blocks   map[string]json.RawMessage // hex number -> block json
	receipts map[string]json.RawMessage // lower tx hash -> receipt json
	txs      map[string]json.RawMessage // lower tx hash -> tx json
}

func newCovChain(rpc *covRPC) *covChain {
	ch := &covChain{
		blocks:   map[string]json.RawMessage{},
		receipts: map[string]json.RawMessage{},
		txs:      map[string]json.RawMessage{},
	}
	rpc.set("eth_getBlockByNumber", func(params []json.RawMessage) (any, error) {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		if b, ok := ch.blocks[covParamString(params, 0)]; ok {
			return b, nil
		}
		return nil, errors.New("block not found")
	})
	rpc.set("eth_getTransactionReceipt", func(params []json.RawMessage) (any, error) {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		if r, ok := ch.receipts[strings.ToLower(covParamString(params, 0))]; ok {
			return r, nil
		}
		return nil, errors.New("receipt not found")
	})
	rpc.set("eth_getTransactionByHash", func(params []json.RawMessage) (any, error) {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		if tx, ok := ch.txs[strings.ToLower(covParamString(params, 0))]; ok {
			return tx, nil
		}
		return nil, nil
	})
	return ch
}

func (ch *covChain) setBlock(t *testing.T, header *etypes.Header, txs []*etypes.Transaction) {
	t.Helper()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.blocks[hexutil.EncodeBig(header.Number)] = covBlockJSON(t, header, txs)
}

func (ch *covChain) setReceipt(t *testing.T, tx *etypes.Transaction, status uint64, logs []*etypes.Log) {
	t.Helper()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.receipts[strings.ToLower(tx.Hash().Hex())] = covReceiptJSON(t, tx, status, big.NewInt(25_000_000_000), logs)
}

func covAddr(t *testing.T, a common.Address) *ecommon.Address {
	t.Helper()
	addr := ecommon.HexToAddress(a.String())
	return &addr
}

func covDepositLog(t *testing.T, env *covEnv, router, to, asset ecommon.Address, amount *big.Int, memo string) *etypes.Log {
	t.Helper()
	ev := env.client.vaultABI.Events["Deposit"]
	data, err := ev.Inputs.NonIndexed().Pack(amount, memo)
	require.NoError(t, err)
	return &etypes.Log{
		Address: router,
		Topics:  []ecommon.Hash{ev.ID, paddedAddressHash(to), paddedAddressHash(asset)},
		Data:    data,
	}
}

func covTransferLog(token, from, to ecommon.Address, amount *big.Int) *etypes.Log {
	return &etypes.Log{
		Address: token,
		Topics:  []ecommon.Hash{ecommon.HexToHash(testERC20TransferEvent), paddedAddressHash(from), paddedAddressHash(to)},
		Data:    ecommon.LeftPadBytes(amount.Bytes(), 32),
	}
}

func covReceipt(tx *etypes.Transaction, status uint64, logs []*etypes.Log) *etypes.Receipt {
	return &etypes.Receipt{
		Status:            status,
		TxHash:            tx.Hash(),
		GasUsed:           21000,
		EffectiveGasPrice: big.NewInt(25_000_000_000),
		Logs:              logs,
	}
}

// ---------------------------------------------------------------------------

func TestCovScannerAccessors(t *testing.T) {
	if notMocknetCov() {
		t.Skip("mocknet only: expects the mocknet approval dust wei per chain")
	}
	env := newCovEnv(t, common.AVAXChain, nil)
	s := env.client.evmScanner

	s.lastReportedGasPrice.Store(77)
	size, rate := s.GetNetworkFee()
	require.Equal(t, uint64(100_000), size)
	require.Equal(t, uint64(77), rate)

	txIn, err := s.FetchMemPool(10)
	require.NoError(t, err)
	require.Empty(t, txIn.TxArray)

	h, err := s.GetHeight()
	require.NoError(t, err)
	require.Equal(t, int64(100), h)
	env.rpc.setError("eth_blockNumber", "down")
	h, err = s.GetHeight()
	require.Error(t, err)
	require.Equal(t, int64(-1), h)

	for chain, dust := range map[common.Chain]int64{
		common.AVAXChain: 1,
		common.BASEChain: 2,
		common.BSCChain:  3,
		common.POLChain:  4,
	} {
		s.cfg.ChainID = chain
		require.Equal(t, dust, s.ApprovalTxRateDustWei().Int64(), chain.String())
	}
	s.cfg.ChainID = common.AVAXChain

	// fixed gas rate overrides the cache
	s.cfg.FixedGasRate = 9
	require.Equal(t, int64(9), s.GetGasPrice().Int64())
	s.cfg.FixedGasRate = 0
	s.gasPrice.Store(nil)
	require.Equal(t, int64(0), s.GetGasPrice().Int64())

	require.False(t, s.isRelevantERC20Token(nil))
	require.False(t, s.isToValidContractAddress(nil, true))

	env.bridge.mimirErr = errors.New("mimir down")
	require.False(t, s.directERC20InboundEnabled())
	env.bridge.mimirErr = nil
	env.bridge.setMimir(fmt.Sprintf(constants.MimirTemplateEVMDirectERC20Inbound, common.AVAXChain), 1)
	require.True(t, s.directERC20InboundEnabled())
}

func TestCovScannerFetchTxsAndReorg(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, func(cfg *config.BifrostChainConfiguration) {
		cfg.BlockScanner.MaxReorgRescanBlocks = 2
	})
	s := env.client.evmScanner
	ch := newCovChain(env.rpc)
	errataQ := make(chan stypes.ErrataBlock, 10)
	feeQ := make(chan common.NetworkFee, 10)
	s.globalErrataQueue = errataQ
	s.globalNetworkFeeQueue = feeQ
	solvencyCalls := 0
	s.solvencyReporter = func(int64) error {
		solvencyCalls++
		return errors.New("solvency failed")
	}

	vaultKey := env.client.kw.GetPrivKey()
	vault := ecommon.HexToAddress(env.vaultAddr(t).String())
	extKey := covExternalKey(t)
	ext := crypto.PubkeyToAddress(extKey.PublicKey)
	other := ecommon.HexToAddress("0x000000000000000000000000000000000000bEEF")
	token := ecommon.HexToAddress(covTknContract)

	memo := "ADD:AVAX.AVAX"
	inbound := covSignTx(t, extKey, 0, &vault, big.NewInt(1e17), []byte(hex.EncodeToString([]byte(memo))))
	ignored := covSignTx(t, extKey, 1, &other, big.NewInt(1e17), nil)
	failedOut := covSignTx(t, vaultKey, 0, &other, big.NewInt(1e17), nil)
	creation := covSignTx(t, extKey, 2, nil, big.NewInt(0), []byte{0x60, 0x00})
	noReceipt := covSignTx(t, extKey, 3, &vault, big.NewInt(1e17), nil)
	cancel := covSignTx(t, vaultKey, 1, &vault, big.NewInt(0), nil)
	approve := covSignTx(t, vaultKey, 2, &token, big.NewInt(0), []byte{0x09, 0x5e, 0xa7, 0xb3, 0xff})

	ch.setReceipt(t, inbound, 1, nil)
	ch.setReceipt(t, ignored, 1, nil)
	ch.setReceipt(t, failedOut, 0, nil)
	ch.setReceipt(t, cancel, 1, nil)
	ch.setReceipt(t, approve, 1, nil)
	header5 := covHeader(5, ecommon.HexToHash("0x04"), true)
	ch.setBlock(t, header5, []*etypes.Transaction{inbound, ignored, failedOut, creation, noReceipt, cancel, approve})

	txIn, err := s.FetchTxs(5, 5)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 4)
	byTx := map[string]*stypes.TxInItem{}
	for _, item := range txIn.TxArray {
		require.Equal(t, int64(5), item.BlockHeight)
		byTx[item.Tx] = item
	}
	in := byTx[inbound.Hash().Hex()[2:]]
	require.NotNil(t, in)
	require.Equal(t, memo, in.Memo)
	require.Equal(t, strings.ToLower(ext.Hex()), in.Sender)
	require.Equal(t, uint64(1e7), in.Coins[0].Amount.Uint64())
	failedOutbound := byTx[failedOut.Hash().Hex()[2:]]
	require.NotNil(t, failedOutbound)
	require.True(t, strings.HasPrefix(failedOutbound.Memo, "OUT:"))
	require.Equal(t, uint64(1), failedOutbound.Coins[0].Amount.Uint64())
	cn := byTx[cancel.Hash().Hex()[2:]]
	require.NotNil(t, cn)
	require.Equal(t, common.AVAXChain.DustThreshold().Uint64(), cn.Coins[0].Amount.Uint64())
	ap := byTx[approve.Hash().Hex()[2:]]
	require.NotNil(t, ap)
	require.Equal(t, "", ap.Memo)

	// network fee reported from the block's effective gas price; solvency reporter invoked
	require.Len(t, feeQ, 1)
	fee := <-feeQ
	require.Equal(t, uint64(25), fee.TransactionRate)
	require.Equal(t, 1, solvencyCalls)

	// block meta stored for reorg handling
	meta, err := s.blockMetaAccessor.GetBlockMeta(5)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Equal(t, header5.Hash().Hex(), meta.BlockHash)

	// block 6 references a different parent -> reorg of block 5. Only the inbound
	// still exists on chain; block 5 was replaced by a version with only the inbound.
	ch.mu.Lock()
	ch.txs[strings.ToLower(inbound.Hash().Hex())] = covTxJSON(t, inbound, big.NewInt(5))
	ch.mu.Unlock()
	header5b := covHeader(5, ecommon.HexToHash("0x0404"), true)
	ch.setBlock(t, header5b, []*etypes.Transaction{inbound})
	inbound2 := covSignTx(t, extKey, 4, &vault, big.NewInt(2e17), nil)
	ch.setReceipt(t, inbound2, 1, nil)
	header6 := covHeader(6, header5b.Hash(), true)
	ch.setBlock(t, header6, []*etypes.Transaction{inbound2})

	// far from tip: no fee or solvency reporting
	txIn, err = s.FetchTxs(6, 100)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 2)
	require.Equal(t, inbound.Hash().Hex()[2:], txIn.TxArray[0].Tx)
	require.Equal(t, inbound2.Hash().Hex()[2:], txIn.TxArray[1].Tx)
	require.Empty(t, feeQ)
	require.Equal(t, 1, solvencyCalls)

	require.Len(t, errataQ, 1)
	errata := <-errataQ
	require.Equal(t, int64(5), errata.Height)
	require.Len(t, errata.Txs, 3)

	meta, err = s.blockMetaAccessor.GetBlockMeta(5)
	require.NoError(t, err)
	require.Equal(t, header5b.Hash().Hex(), meta.BlockHash)
	require.Len(t, meta.Transactions, 1)

	// block fetch failure
	_, err = s.FetchTxs(99, 99)
	require.Error(t, err)

	// empty block returns no txs
	ch.setBlock(t, covHeader(7, header6.Hash(), false), nil)
	txIn, err = s.FetchTxs(7, 7)
	require.NoError(t, err)
	require.Empty(t, txIn.TxArray)
}

func TestCovScannerReprocessHeaderFailure(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, func(cfg *config.BifrostChainConfiguration) {
		cfg.BlockScanner.MaxReorgRescanBlocks = 2
	})
	s := env.client.evmScanner
	ch := newCovChain(env.rpc)
	s.globalErrataQueue = make(chan stypes.ErrataBlock, 10)
	s.globalNetworkFeeQueue = make(chan common.NetworkFee, 10)

	extKey := covExternalKey(t)
	vault := ecommon.HexToAddress(env.vaultAddr(t).String())
	inbound := covSignTx(t, extKey, 0, &vault, big.NewInt(1e17), nil)
	ch.setReceipt(t, inbound, 1, nil)
	header := covHeader(10, ecommon.HexToHash("0x09"), true)
	ch.setBlock(t, header, []*etypes.Transaction{inbound})
	_, err := s.FetchTxs(10, 10)
	require.NoError(t, err)

	// header lookup for the stored block fails -> rescan anyway, but rescan also fails
	ch.mu.Lock()
	delete(ch.blocks, "0xa")
	ch.mu.Unlock()
	heights, err := s.reprocessTxs()
	require.NoError(t, err)
	require.Equal(t, []int64{10}, heights)

	header11 := covHeader(11, ecommon.HexToHash("0xdead"), true)
	in2 := covSignTx(t, extKey, 1, &vault, big.NewInt(1e17), nil)
	ch.setReceipt(t, in2, 1, nil)
	ch.setBlock(t, header11, []*etypes.Transaction{in2})
	txIns, err := s.processReorg(header11)
	require.NoError(t, err)
	require.Empty(t, txIns)
}

func TestCovScannerOptimizedReceipts(t *testing.T) {
	// metrics are only registered for AVAX, so keep the gas cache from filling.
	// Concurrency > 1 runs the receipt goroutines in parallel, so -race checks
	// they share no unsynchronized state.
	env := newCovEnv(t, common.BSCChain, func(cfg *config.BifrostChainConfiguration) {
		cfg.BlockScanner.GasCacheBlocks = 100
		cfg.BlockScanner.Concurrency = 4
	})
	s := env.client.evmScanner
	ch := newCovChain(env.rpc)
	s.globalNetworkFeeQueue = make(chan common.NetworkFee, 10)

	extKey := covExternalKey(t)
	vault := ecommon.HexToAddress(env.vaultAddr(t).String())
	other := ecommon.HexToAddress("0x000000000000000000000000000000000000bEEF")
	inbound := covSignTx(t, extKey, 0, &vault, big.NewInt(1e17), []byte("plain memo"))
	ignored := covSignTx(t, extKey, 1, &other, big.NewInt(1e17), nil)
	creation := covSignTx(t, extKey, 2, nil, big.NewInt(0), []byte{0x60})
	stray := covSignTx(t, extKey, 3, &vault, big.NewInt(1e17), nil)
	header := covHeader(3, ecommon.HexToHash("0x02"), true)
	ch.setBlock(t, header, []*etypes.Transaction{inbound, ignored, creation})

	receipts := []json.RawMessage{
		covReceiptJSON(t, inbound, 1, big.NewInt(3e9), nil),
		covReceiptJSON(t, ignored, 1, big.NewInt(1e9), nil),
		covReceiptJSON(t, creation, 1, big.NewInt(2e9), nil),
		covReceiptJSON(t, stray, 1, big.NewInt(9e9), nil),
	}
	env.rpc.setResult("eth_getBlockReceipts", receipts)

	txIn, err := s.FetchTxs(3, 3)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	require.Equal(t, "plain memo", txIn.TxArray[0].Memo)
	require.Equal(t, int64(3), txIn.TxArray[0].BlockHeight)
	// median of the 3 in-block receipts (stray is ignored) = 2 gwei
	require.Len(t, s.gasCache, 1)
	require.Equal(t, int64(2e9), s.gasCache[0].Int64())

	// a block with only irrelevant txs yields an empty TxIn
	header4 := covHeader(4, header.Hash(), true)
	ch.setBlock(t, header4, []*etypes.Transaction{ignored})
	env.rpc.setResult("eth_getBlockReceipts", []json.RawMessage{covReceiptJSON(t, ignored, 1, big.NewInt(1e9), nil)})
	txIn, err = s.FetchTxs(4, 4)
	require.NoError(t, err)
	require.Empty(t, txIn.TxArray)

	env.rpc.setError("eth_getBlockReceipts", "unsupported")
	_, err = s.FetchTxs(3, 3)
	require.ErrorContains(t, err, "failed to process block")
}

func TestCovScannerBatchError(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	s := env.client.evmScanner
	ch := newCovChain(env.rpc)
	extKey := covExternalKey(t)
	vault := ecommon.HexToAddress(env.vaultAddr(t).String())
	in := covSignTx(t, extKey, 0, &vault, big.NewInt(1e17), nil)
	header := covHeader(3, ecommon.HexToHash("0x02"), true)
	ch.setBlock(t, header, []*etypes.Transaction{in})
	block, err := s.ethRpc.GetBlock(3)
	require.NoError(t, err)

	// transport failure for the whole batch
	env.rpc.srv.Close()
	_, _, err = s.getTxIn(block)
	require.Error(t, err)
}

func TestCovReceiptToTxInItemNoWhitelist(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	s := env.client.evmScanner
	env.bridge.setMimir(constants.EVMDisableContractWhitelist.String(), 1)

	vaultKey := env.client.kw.GetPrivKey()
	vault := ecommon.HexToAddress(env.vaultAddr(t).String())
	extKey := covExternalKey(t)
	ext := crypto.PubkeyToAddress(extKey.PublicKey)
	router := *covAddr(t, covRouter)
	other := ecommon.HexToAddress("0x000000000000000000000000000000000000bEEF")
	token := ecommon.HexToAddress(covTknContract)

	// to vault
	tx := covSignTx(t, extKey, 0, &vault, big.NewInt(1e17), nil)
	item, err := s.receiptToTxInItem(tx, covReceipt(tx, 1, nil))
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, strings.ToLower(vault.Hex()), item.To)

	// to router, deposit with native value
	tx = covSignTx(t, extKey, 1, &router, big.NewInt(1e17), nil)
	logs := []*etypes.Log{covDepositLog(t, env, router, vault, ecommon.Address{}, big.NewInt(1e17), "ADD:AVAX.AVAX")}
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, logs))
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, "ADD:AVAX.AVAX", item.Memo)
	require.Equal(t, uint64(1e7), item.Coins[0].Amount.Uint64())
	require.NotEmpty(t, item.Gas)

	// to router without logs -> nothing observed
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, nil))
	require.NoError(t, err)
	require.Nil(t, item)

	// from vault to external
	tx = covSignTx(t, vaultKey, 0, &other, big.NewInt(1e17), nil)
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, nil))
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, strings.ToLower(other.Hex()), item.To)

	// ERC20 transfer of an unknown token
	transferData := erc20TransferCallData(vault, big.NewInt(1e18))
	tx = covSignTx(t, extKey, 2, &token, big.NewInt(0), transferData)
	transferLogs := []*etypes.Log{covTransferLog(token, ext, vault, big.NewInt(1e18))}
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, transferLogs))
	require.NoError(t, err)
	require.Nil(t, item)

	// known token, but no Transfer log
	require.NoError(t, s.tokenManager.SaveTokenMeta("TKN", covTknContract, 18))
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, nil))
	require.NoError(t, err)
	require.Nil(t, item)

	// known token with log, but direct inbounds disabled
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, transferLogs))
	require.NoError(t, err)
	require.Nil(t, item)

	// direct inbounds enabled -> memoless token inbound to the vault
	env.bridge.setMimir(fmt.Sprintf(constants.MimirTemplateEVMDirectERC20Inbound, common.AVAXChain), 1)
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, transferLogs))
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, strings.ToLower(vault.Hex()), strings.ToLower(item.To))
	require.Equal(t, "", item.Memo)

	// call to another contract with logs, none from the router
	tx = covSignTx(t, extKey, 3, &other, big.NewInt(0), []byte{1, 2, 3, 4, 5})
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, []*etypes.Log{covTransferLog(other, ext, vault, big.NewInt(1))}))
	require.NoError(t, err)
	require.Nil(t, item)

	// unrelated plain transfer
	tx = covSignTx(t, extKey, 4, &other, big.NewInt(1e17), nil)
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, nil))
	require.NoError(t, err)
	require.Nil(t, item)

	// mimir lookup error falls back to the whitelist path
	env.bridge.mimirErr = errors.New("mimir down")
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, nil))
	require.NoError(t, err)
	require.Nil(t, item)
}

func TestCovReceiptToTxInItemWhitelist(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	s := env.client.evmScanner
	vault := ecommon.HexToAddress(env.vaultAddr(t).String())
	extKey := covExternalKey(t)
	ext := crypto.PubkeyToAddress(extKey.PublicKey)
	router := *covAddr(t, covRouter)
	token := ecommon.HexToAddress(covTknContract)

	// unknown token transfer is ignored
	transferData := erc20TransferCallData(vault, big.NewInt(1000))
	tx := covSignTx(t, extKey, 0, &token, big.NewInt(0), transferData)
	transferLogs := []*etypes.Log{covTransferLog(token, ext, vault, big.NewInt(1000))}
	item, err := s.receiptToTxInItem(tx, covReceipt(tx, 1, transferLogs))
	require.NoError(t, err)
	require.Nil(t, item)

	// known token, missing Transfer log
	require.NoError(t, s.tokenManager.SaveTokenMeta("TKN", covTknContract, 18))
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, nil))
	require.NoError(t, err)
	require.Nil(t, item)

	// known token, direct inbound disabled
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, transferLogs))
	require.NoError(t, err)
	require.Nil(t, item)

	// deposit to a vault through a router that is not the vault's router is dropped
	wrongRouter := ecommon.HexToAddress("0x000000000000000000000000000000000000cafe")
	otherVault := ecommon.HexToAddress("0x000000000000000000000000000000000000f00d")
	env.pkm.add(ttypes.GetRandomPubKey(), common.Address(wrongRouter.Hex()))
	tx = covSignTx(t, extKey, 1, &wrongRouter, big.NewInt(0), []byte{1, 2, 3, 4})
	logs := []*etypes.Log{covDepositLog(t, env, wrongRouter, vault, ecommon.Address{}, big.NewInt(1e17), "ADD:AVAX.AVAX")}
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, logs))
	require.NoError(t, err)
	require.Nil(t, item)

	// deposit to a non-vault address via the right router is kept
	logs = []*etypes.Log{covDepositLog(t, env, router, otherVault, ecommon.Address{}, big.NewInt(1e17), "ADD:AVAX.AVAX")}
	tx = covSignTx(t, extKey, 2, &router, big.NewInt(0), []byte{1, 2, 3, 4})
	item, err = s.receiptToTxInItem(tx, covReceipt(tx, 1, logs))
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, otherVault.Hex(), item.To)

	// receipt status failure inside the smart contract parser
	item, err = s.getTxInFromSmartContract(tx, covReceipt(tx, 0, logs), 0)
	require.NoError(t, err)
	require.Nil(t, item)

	// conflicting deposit events surface as an error
	logs = append(logs, covDepositLog(t, env, router, vault, ecommon.Address{}, big.NewInt(1e17), "ADD:AVAX.AVAX"))
	_, err = s.getTxInFromSmartContract(tx, covReceipt(tx, 1, logs), 0)
	require.ErrorContains(t, err, "failed to parse logs")
}

func TestCovFailedAndPlainTransactions(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	s := env.client.evmScanner
	vaultKey := env.client.kw.GetPrivKey()
	extKey := covExternalKey(t)
	other := ecommon.HexToAddress("0x000000000000000000000000000000000000bEEF")

	// successful receipts are not handled as failures
	tx := covSignTx(t, vaultKey, 0, &other, big.NewInt(1), nil)
	require.Nil(t, s.getTxInFromFailedTransaction(tx, covReceipt(tx, 1, nil)))

	// failed tx from non-vault is ignored
	tx = covSignTx(t, extKey, 0, &other, big.NewInt(1), nil)
	require.Nil(t, s.getTxInFromFailedTransaction(tx, covReceipt(tx, 0, nil)))

	// failed vault tx with nil effective gas price
	tx = covSignTx(t, vaultKey, 0, &other, big.NewInt(1), nil)
	r := covReceipt(tx, 0, nil)
	r.EffectiveGasPrice = nil
	item := s.getTxInFromFailedTransaction(tx, r)
	require.NotNil(t, item)
	require.Equal(t, strings.ToLower(other.Hex()), item.To)

	// below-dust inbound is dropped; non-hex memo kept verbatim on outbound
	vault := ecommon.HexToAddress(env.vaultAddr(t).String())
	tx = covSignTx(t, extKey, 1, &vault, big.NewInt(1), nil)
	item, err := s.getTxInFromTransaction(tx, r)
	require.NoError(t, err)
	require.Nil(t, item)

	tx = covSignTx(t, vaultKey, 1, &other, big.NewInt(1e17), []byte("OUT:zz"))
	item, err = s.getTxInFromTransaction(tx, r)
	require.NoError(t, err)
	require.Equal(t, "OUT:zz", item.Memo)
	// a nil effective gas price still reports the minimum gas of 1 unit
	require.Equal(t, "1", item.Gas[0].Amount.String())

	// smart contract tx with nil gas price but valid deposit
	router := *covAddr(t, covRouter)
	tx = covSignTx(t, extKey, 2, &router, big.NewInt(0), []byte{1, 2, 3, 4})
	logs := []*etypes.Log{covDepositLog(t, env, router, vault, ecommon.Address{}, big.NewInt(1e17), "ADD:AVAX.AVAX")}
	rr := covReceipt(tx, 1, logs)
	rr.EffectiveGasPrice = nil
	item, err = s.getTxInFromSmartContract(tx, rr, 0)
	require.NoError(t, err)
	require.NotNil(t, item)

	// getTxInItem: nil/creation txs and missing receipts
	item, err = s.getTxInItem(nil)
	require.NoError(t, err)
	require.Nil(t, item)
	newCovChain(env.rpc)
	_, err = s.getTxInItem(tx)
	require.ErrorContains(t, err, "failed to get transaction receipt")
	env.rpc.setResult("eth_getTransactionReceipt", nil)
	item, err = s.getTxInItem(tx)
	require.NoError(t, err)
	require.Nil(t, item)
}
