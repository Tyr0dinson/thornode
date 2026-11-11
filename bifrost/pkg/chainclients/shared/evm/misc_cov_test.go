package evm

import (
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////
// key sign wrapper (TSS path)
////////////////////////////////////////////////////////////////////////////////

type keyManagerCov struct {
	tss.MockThorchainKeyManager
	sign func(msg []byte) ([]byte, []byte, error)
}

func (k *keyManagerCov) RemoteSign(msg []byte, _ common.SigningAlgo, _ string, _ int64) ([]byte, []byte, error) {
	return k.sign(msg)
}

func TestKeySignWrapperTSSCov(t *testing.T) {
	ttypes.SetupConfigForTest()

	// the "pool" key that TSS signs with
	poolPriv := secp256k1.GenPrivKey()
	poolECDSA, err := ecrypto.ToECDSA(poolPriv.Bytes())
	require.NoError(t, err)
	poolCmt, err := cryptocodec.ToCmtPubKeyInterface(poolPriv.PubKey())
	require.NoError(t, err)
	poolPub, err := common.NewPubKeyFromCrypto(poolCmt)
	require.NoError(t, err)

	// the local node key (different from the pool key)
	nodePriv := secp256k1.GenPrivKey()
	nodeECDSA, err := GetPrivateKey(nodePriv)
	require.NoError(t, err)
	nodeCmt, err := cryptocodec.ToCmtPubKeyInterface(nodePriv.PubKey())
	require.NoError(t, err)
	nodePub, err := common.NewPubKeyFromCrypto(nodeCmt)
	require.NoError(t, err)

	km := &keyManagerCov{}
	w, err := NewKeySignWrapper(nodeECDSA, nodePub, km, big.NewInt(1), "ETH")
	require.NoError(t, err)

	tx := etypes.NewTransaction(0, ecommon.HexToAddress("0x02"), big.NewInt(1), 21000, big.NewInt(1), nil)

	// valid TSS signature
	km.sign = func(msg []byte) ([]byte, []byte, error) {
		sig, signErr := ecrypto.Sign(msg, poolECDSA)
		if signErr != nil {
			return nil, nil, signErr
		}
		return sig[:64], []byte{sig[64]}, nil
	}
	raw, err := w.signTSS(tx, poolPub.String(), 10)
	require.NoError(t, err)
	require.Len(t, raw, 65)
	enc, err := w.Sign(tx, poolPub, 10)
	require.NoError(t, err)
	signed := new(etypes.Transaction)
	require.NoError(t, signed.UnmarshalJSON(enc))
	from, err := etypes.Sender(w.signer, signed)
	require.NoError(t, err)
	require.Equal(t, ecrypto.PubkeyToAddress(poolECDSA.PublicKey), from)

	// unverifiable signature still returns 65 bytes with the recovery id
	km.sign = func([]byte) ([]byte, []byte, error) {
		return make([]byte, 64), []byte{1}, nil
	}
	raw, err = w.signTSS(tx, poolPub.String(), 10)
	require.NoError(t, err)
	require.Len(t, raw, 65)
	require.Equal(t, byte(1), raw[64])

	// not selected
	km.sign = func([]byte) ([]byte, []byte, error) { return nil, nil, tss.ErrNotSelectedForKeysign }
	_, err = w.signTSS(tx, poolPub.String(), 10)
	require.ErrorIs(t, err, tss.ErrNotSelectedForKeysign)

	// nil signature without error is treated as not selected
	km.sign = func([]byte) ([]byte, []byte, error) { return nil, nil, nil }
	_, err = w.Sign(tx, poolPub, 10)
	require.ErrorIs(t, err, tss.ErrNotSelectedForKeysign)

	// generic failure
	km.sign = func([]byte) ([]byte, []byte, error) { return nil, nil, errors.New("party timeout") }
	_, err = w.signTSS(tx, poolPub.String(), 10)
	require.ErrorContains(t, err, "fail to TSS sign")

	// invalid pool pubkey
	_, err = w.signTSS(tx, "not-a-pubkey", 10)
	require.ErrorContains(t, err, "fail to get pub key")
}

////////////////////////////////////////////////////////////////////////////////
// direct ERC20 transfers
////////////////////////////////////////////////////////////////////////////////

type emptyAssetResolverCov struct {
	mockDirectERC20TransferTokenResolver
}

func (emptyAssetResolverCov) GetAssetFromTokenAddress(string) (common.Asset, error) {
	return common.EmptyAsset, nil
}

// transferTxCov builds a transfer(recipient, 0) call to token; the scanner
// takes the inbound recipient from this calldata.
func transferTxCov(t *testing.T, key *[32]byte, token, recipient ecommon.Address) (*etypes.Transaction, etypes.Signer) {
	t.Helper()
	data := append([]byte{}, erc20TransferSelector...)
	data = append(data, ecommon.LeftPadBytes(recipient.Bytes(), 32)...)
	data = append(data, make([]byte, 32)...)
	tx := etypes.NewTransaction(1, token, big.NewInt(0), 100000, big.NewInt(1), data)
	signer := etypes.NewEIP155Signer(big.NewInt(1))
	if key == nil {
		return tx, signer
	}
	pk, err := ecrypto.ToECDSA(key[:])
	require.NoError(t, err)
	signed, err := etypes.SignTx(tx, signer, pk)
	require.NoError(t, err)
	return signed, signer
}

func TestGetTxInFromDirectERC20TransferCov(t *testing.T) {
	ttypes.SetupConfigForTest()
	pubkey := ttypes.GetRandomPubKey()
	cpi, err := common.NewChainPoolInfo(common.ETHChain, pubkey)
	require.NoError(t, err)
	vault := cpi.PoolAddress.String()
	token := ecommon.HexToAddress(tknTestToken)
	provider := mockPoolAddressProvider{pubkeys: common.PubKeys{pubkey}}

	keyBytes := [32]byte{1, 2, 3}
	pk, err := ecrypto.ToECDSA(keyBytes[:])
	require.NoError(t, err)
	sender := ecrypto.PubkeyToAddress(pk.PublicKey)

	signedTx, signer := transferTxCov(t, &keyBytes, token, ecommon.HexToAddress(vault))
	unsignedTx, _ := transferTxCov(t, nil, token, ecommon.HexToAddress(vault))
	okReceipt := func() *etypes.Receipt {
		return &etypes.Receipt{
			Status:            1,
			GasUsed:           50000,
			EffectiveGasPrice: big.NewInt(2_000_000_000),
			Logs:              []*etypes.Log{getERC20TransferEvent(tknTestToken, sender.String(), vault, big.NewInt(500))},
		}
	}
	baseOpts := func() DirectERC20TransferTxInOptions {
		return DirectERC20TransferTxInOptions{
			Chain:               common.ETHChain,
			Signer:              signer,
			MaxContractTxLogs:   10,
			PoolAddressProvider: provider,
			TokenResolver:       mockDirectERC20TransferTokenResolver{},
			Logger:              log.Logger,
		}
	}

	// ineligible inputs return nothing
	for _, tc := range []struct {
		tx *etypes.Transaction
		r  *etypes.Receipt
	}{
		{nil, okReceipt()},
		{signedTx, nil},
		{signedTx, &etypes.Receipt{Status: 0}},
		{etypes.NewContractCreation(1, big.NewInt(0), 1, big.NewInt(1), nil), okReceipt()},
	} {
		item, itemErr := GetTxInFromDirectERC20Transfer(tc.tx, tc.r, baseOpts())
		require.NoError(t, itemErr)
		require.Nil(t, item)
	}

	// signer recovers the sender
	item, err := GetTxInFromDirectERC20Transfer(signedTx, okReceipt(), baseOpts())
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, signedTx.Hash().Hex()[2:], item.Tx)
	require.Equal(t, strings.ToLower(sender.String()), item.Sender)
	require.True(t, strings.EqualFold(vault, item.To))
	require.Len(t, item.Coins, 1)
	require.Equal(t, cosmos.NewUint(500), item.Coins[0].Amount)
	require.Equal(t, common.MakeEVMGas(common.ETHChain, big.NewInt(2_000_000_000), 50000, nil), item.Gas)
	require.Empty(t, item.Memo)

	// explicit sender, nil effective gas price
	opts := baseOpts()
	opts.Signer = nil
	opts.Sender = &sender
	r := okReceipt()
	r.EffectiveGasPrice = nil
	item, err = GetTxInFromDirectERC20Transfer(unsignedTx, r, opts)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, common.MakeEVMGas(common.ETHChain, big.NewInt(0), 50000, nil), item.Gas)

	// no signer and no sender
	opts = baseOpts()
	opts.Signer = nil
	_, err = GetTxInFromDirectERC20Transfer(signedTx, okReceipt(), opts)
	require.EqualError(t, err, "signer is nil")

	// unsigned tx -> sender recovery fails (default and custom messages)
	_, err = GetTxInFromDirectERC20Transfer(unsignedTx, okReceipt(), baseOpts())
	require.ErrorContains(t, err, "failed to get sender")
	opts = baseOpts()
	opts.SenderErrorMessage = "custom sender failure"
	_, err = GetTxInFromDirectERC20Transfer(unsignedTx, okReceipt(), opts)
	require.ErrorContains(t, err, "custom sender failure")

	// log parsing error (no resolver) with default and custom messages
	opts = baseOpts()
	opts.TokenResolver = nil
	_, err = GetTxInFromDirectERC20Transfer(signedTx, okReceipt(), opts)
	require.ErrorContains(t, err, "failed to parse direct ERC20 transfer logs")
	opts.ParseLogsErrorMessage = "custom parse failure"
	_, err = GetTxInFromDirectERC20Transfer(signedTx, okReceipt(), opts)
	require.ErrorContains(t, err, "custom parse failure")

	// logs that do not match -> nothing observed
	r = okReceipt()
	r.Logs = []*etypes.Log{getERC20TransferEvent(tknTestToken, sender.String(), "0x0000000000000000000000000000000000000009", big.NewInt(5))}
	item, err = GetTxInFromDirectERC20Transfer(signedTx, r, baseOpts())
	require.NoError(t, err)
	require.Nil(t, item)
}

func TestParseDirectERC20TransferLogsEdgesCov(t *testing.T) {
	ttypes.SetupConfigForTest()
	pubkey := ttypes.GetRandomPubKey()
	cpi, err := common.NewChainPoolInfo(common.ETHChain, pubkey)
	require.NoError(t, err)
	vault := cpi.PoolAddress.String()
	pubkey2 := ttypes.GetRandomPubKey()
	cpi2, err := common.NewChainPoolInfo(common.ETHChain, pubkey2)
	require.NoError(t, err)
	vault2 := cpi2.PoolAddress.String()
	token := ecommon.HexToAddress(tknTestToken)
	sender := ecommon.HexToAddress("0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473")
	opts := DirectERC20TransferLogOptions{
		Chain:               common.ETHChain,
		MaxLogs:             3,
		PoolAddressProvider: mockPoolAddressProvider{pubkeys: common.PubKeys{pubkey, pubkey2}},
		TokenResolver:       mockDirectERC20TransferTokenResolver{},
		Logger:              log.Logger,
	}
	good := func(to string) *etypes.Log {
		return getERC20TransferEvent(tknTestToken, sender.String(), to, big.NewInt(10))
	}

	require.False(t, IsActivePoolAddress(common.ETHChain, nil, vault))

	recipient := ecommon.HexToAddress(vault)
	_, err = ParseDirectERC20TransferLogs(nil, nil, token, sender, recipient, opts)
	require.EqualError(t, err, "tx in item is nil")

	noResolver := opts
	noResolver.TokenResolver = nil
	_, err = ParseDirectERC20TransferLogs(nil, &stypes.TxInItem{}, token, sender, recipient, noResolver)
	require.EqualError(t, err, "direct ERC20 transfer token resolver is nil")

	ok, err := ParseDirectERC20TransferLogs(nil, &stypes.TxInItem{}, token, sender, recipient, opts)
	require.NoError(t, err)
	require.False(t, ok)

	// too many logs
	ok, err = ParseDirectERC20TransferLogs([]*etypes.Log{good(vault), good(vault), good(vault), good(vault)}, &stypes.TxInItem{}, token, sender, recipient, opts)
	require.NoError(t, err)
	require.False(t, ok)

	// a recipient that is not an active vault is ignored
	item := &stypes.TxInItem{}
	inactive := ecommon.HexToAddress("0x00000000000000000000000000000000000000aa")
	ok, err = ParseDirectERC20TransferLogs([]*etypes.Log{good(inactive.String())}, item, token, sender, inactive, opts)
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, item.Coins)

	// malformed logs are skipped
	badTopics := good(vault)
	badTopics.Topics = badTopics.Topics[:2]
	badData := good(vault)
	badData.Data = []byte{1}
	item = &stypes.TxInItem{}
	ok, err = ParseDirectERC20TransferLogs([]*etypes.Log{badTopics, badData, good(vault)}, item, token, sender, recipient, opts)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, item.Coins, 1)

	// only the transfer to the requested recipient counts; a transfer to
	// another vault in the same tx is ignored
	item = &stypes.TxInItem{}
	ok, err = ParseDirectERC20TransferLogs([]*etypes.Log{good(vault2), good(vault)}, item, token, sender, recipient, opts)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, item.Coins, 1)
	require.True(t, strings.EqualFold(item.To, vault), item.To)
	item = &stypes.TxInItem{}
	ok, err = ParseDirectERC20TransferLogs([]*etypes.Log{good(vault2)}, item, token, sender, recipient, opts)
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, item.Coins)

	// empty asset -> ignored
	emptyOpts := opts
	emptyOpts.TokenResolver = emptyAssetResolverCov{}
	item = &stypes.TxInItem{}
	ok, err = ParseDirectERC20TransferLogs([]*etypes.Log{good(vault)}, item, token, sender, recipient, emptyOpts)
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, item.Coins)
}

////////////////////////////////////////////////////////////////////////////////
// smart contract log parser: TransferOutAndCall and error paths
////////////////////////////////////////////////////////////////////////////////

func loadV4RouterABICov(t *testing.T) *abi.ABI {
	t.Helper()
	// the shared test router ABI predates TransferOutAndCall; use the evm one
	raw, err := os.ReadFile("../../evm/abi/router.json")
	require.NoError(t, err)
	a, _, err := GetContractABI(string(raw), erc20ContractABI)
	require.NoError(t, err)
	return a
}

func transferOutAndCallLogCov(t *testing.T, a *abi.ABI, vault, target, finalAsset, to string, amount, amountOutMin *big.Int, memoStr string) *etypes.Log {
	t.Helper()
	evt := a.Events["TransferOutAndCall"]
	require.Equal(t, transferOutAndCallEvent, evt.ID.Hex())
	data, err := evt.Inputs.NonIndexed().Pack(
		ecommon.HexToAddress(target), amount, ecommon.HexToAddress(finalAsset), ecommon.HexToAddress(to), amountOutMin, memoStr)
	require.NoError(t, err)
	return &etypes.Log{
		Address: ecommon.HexToAddress(routerCov),
		Topics: []ecommon.Hash{
			evt.ID,
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(vault).Bytes(), ecommon.HashLength)),
		},
		Data: data,
	}
}

func TestSmartContractLogParserTransferOutAndCallCov(t *testing.T) {
	ttypes.SetupConfigForTest()
	a := loadV4RouterABICov(t)
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, a, common.ETHAsset, 10)

	vault := "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473"
	target := "0x1111111111111111111111111111111111111111"
	final := "0x2222222222222222222222222222222222222222"
	to := "0x3333333333333333333333333333333333333333"
	outMemo := "OUT:" + strings.Repeat("A", 64)

	item := &stypes.TxInItem{}
	isVault, err := parser.GetTxInItem([]*etypes.Log{
		transferOutAndCallLogCov(t, a, vault, target, final, to, big.NewInt(1000), big.NewInt(7), outMemo),
	}, item)
	require.NoError(t, err)
	require.False(t, isVault)
	require.True(t, strings.EqualFold(to, item.To))
	require.True(t, strings.EqualFold(vault, item.Sender))
	require.True(t, strings.EqualFold(target, item.Aggregator))
	require.True(t, strings.EqualFold(final, item.AggregatorTarget))
	require.Equal(t, outMemo, item.Memo)
	require.NotNil(t, item.AggregatorTargetLimit)
	require.Equal(t, cosmos.NewUint(7), *item.AggregatorTargetLimit)
	require.Len(t, item.Coins, 1)
	require.Equal(t, common.ETHAsset, item.Coins[0].Asset)
	require.Equal(t, cosmos.NewUint(1000), item.Coins[0].Amount)

	// zero amountOutMin -> no limit
	item = &stypes.TxInItem{}
	_, err = parser.GetTxInItem([]*etypes.Log{
		transferOutAndCallLogCov(t, a, vault, target, final, to, big.NewInt(1000), big.NewInt(0), outMemo),
	}, item)
	require.NoError(t, err)
	require.Nil(t, item.AggregatorTargetLimit)
	require.Equal(t, outMemo, item.Memo)

	// unparsable memo and non-outbound memo are ignored
	for _, m := range []string{"NOT A MEMO :::", "REFUND:" + strings.Repeat("B", 64)} {
		item = &stypes.TxInItem{}
		_, err = parser.GetTxInItem([]*etypes.Log{
			transferOutAndCallLogCov(t, a, vault, target, final, to, big.NewInt(1000), big.NewInt(0), m),
		}, item)
		require.NoError(t, err, m)
		require.Empty(t, item.Memo, m)
		require.Empty(t, item.Coins, m)
	}

	// malformed event data is ignored for every router event type
	for _, topic := range []string{transferOutAndCallEvent, depositEvent, transferOutEvent, transferAllowanceEvent, vaultTransferEvent} {
		item = &stypes.TxInItem{}
		_, err = parser.GetTxInItem([]*etypes.Log{{
			Address: ecommon.HexToAddress(routerCov),
			Topics:  []ecommon.Hash{ecommon.HexToHash(topic), {}, {}},
			Data:    []byte{1, 2, 3},
		}}, item)
		require.NoError(t, err, topic)
		require.Empty(t, item.Coins, topic)
	}
}

func TestSmartContractLogParserUnpackErrorsCov(t *testing.T) {
	a := loadV4RouterABICov(t)
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, a, common.ETHAsset, 10)

	var evt vaultDepositEvent
	err := parser.unpackVaultLog(&evt, "Deposit", etypes.Log{})
	require.EqualError(t, err, "topics field in event log is empty")

	err = parser.unpackVaultLog(&evt, "Deposit", etypes.Log{Topics: []ecommon.Hash{ecommon.HexToHash(transferOutEvent)}})
	require.EqualError(t, err, "event signature mismatch")

	_, err = parser.parseDeposit(etypes.Log{})
	require.ErrorContains(t, err, "fail to unpack event")
}

func TestSmartContractLogParserTransferAllowanceErrorsCov(t *testing.T) {
	ttypes.SetupConfigForTest()
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	require.NoError(t, err)
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 10)
	h := &SmartContractLogParserTestSuite{abi: vaultABI}

	oldVault := "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473"
	newVault := "0x6c4a2eeb8531e3c18bca51104df7eb2377708263"

	// unparsable memo -> ignored
	item := &stypes.TxInItem{}
	_, err = parser.GetTxInItem([]*etypes.Log{
		h.getTransferAllowanceEvent(routerCov, oldVault, newVault, NativeTokenAddr, big.NewInt(10), "NOT A MEMO :::"),
	}, item)
	require.NoError(t, err)
	require.Empty(t, item.Coins)

	// asset resolver error -> ignored
	item = &stypes.TxInItem{}
	_, err = parser.GetTxInItem([]*etypes.Log{
		h.getTransferAllowanceEvent(routerCov, oldVault, newVault, errAssetToken, big.NewInt(10), "MIGRATE:10"),
	}, item)
	require.NoError(t, err)
	require.Empty(t, item.Coins)
}

////////////////////////////////////////////////////////////////////////////////
// leveldb stores
////////////////////////////////////////////////////////////////////////////////

func TestLevelDBTokenMetaEdgesCov(t *testing.T) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	tdb, err := NewLevelDBTokenMeta(db, "tm-")
	require.NoError(t, err)

	// corrupt entry -> unmarshal errors
	require.NoError(t, db.Put([]byte("tm-0XBAD"), []byte("{not json"), nil))
	_, err = tdb.GetTokenMeta("0xbad")
	require.ErrorContains(t, err, "fail to unmarshal token meta from json")
	_, err = tdb.GetTokens()
	require.ErrorContains(t, err, "fail to unmarshal token meta")
	require.NoError(t, db.Delete([]byte("tm-0XBAD"), nil))

	// empty values are skipped and duplicates are collapsed
	require.NoError(t, db.Put([]byte("tm-0XEMPTY"), []byte{}, nil))
	require.NoError(t, tdb.SaveTokenMeta("TKN", "0xabc", 18))
	require.NoError(t, db.Put([]byte("tm-dup"), []byte(`{"symbol":"tkn","address":"0XABC","decimal":18}`), nil))
	tokens, err := tdb.GetTokens()
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.Equal(t, "TKN", tokens[0].Symbol)

	// closed db -> errors
	require.NoError(t, db.Close())
	_, err = tdb.GetTokenMeta("0xabc")
	require.ErrorContains(t, err, "fail to check whether token meta")
}

func TestLevelDBBlockMetaAccessorEdgesCov(t *testing.T) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	acc, err := NewLevelDBBlockMetaAccessor("bm-", "st-", db)
	require.NoError(t, err)

	require.NoError(t, acc.SaveBlockMeta(5, evmtypes.NewBlockMeta(&etypes.Header{Number: big.NewInt(5)}, stypes.TxIn{})))
	// empty and corrupt block meta entries
	require.NoError(t, db.Put([]byte("bm-empty"), []byte{}, nil))
	metas, err := acc.GetBlockMetas()
	require.NoError(t, err)
	require.Len(t, metas, 1)
	require.NoError(t, acc.PruneBlockMeta(1))

	require.NoError(t, db.Put([]byte(acc.getBlockMetaKey(9)), []byte("{bad"), nil))
	_, err = acc.GetBlockMeta(9)
	require.ErrorContains(t, err, "fail to unmarshal block meta from json")
	_, err = acc.GetBlockMetas()
	require.ErrorContains(t, err, "fail to unmarshal block meta")
	err = acc.PruneBlockMeta(100)
	require.ErrorContains(t, err, "fail to unmarshal block meta")

	// signed tx items: empty skipped, corrupt -> error
	require.NoError(t, acc.AddSignedTxItem(evmtypes.SignedTxItem{Hash: "0xaa", Height: 1}))
	require.NoError(t, db.Put([]byte("st-empty"), []byte{}, nil))
	items, err := acc.GetSignedTxItems()
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, db.Put([]byte("st-bad"), []byte("{bad"), nil))
	_, err = acc.GetSignedTxItems()
	require.ErrorContains(t, err, "fail to unmarshal sign tx items")

	require.NoError(t, db.Close())
	_, err = acc.GetBlockMeta(5)
	require.ErrorContains(t, err, "fail to check whether block meta")
}
