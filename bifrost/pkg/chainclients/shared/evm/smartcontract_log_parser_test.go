package evm

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type SmartContractLogParserTestSuite struct {
	abi *abi.ABI
}

var _ = Suite(&SmartContractLogParserTestSuite{})

func (t *SmartContractLogParserTestSuite) SetUpSuite(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)
	t.abi = vaultABI
}

func mockIsValidContractAddr(addr *ecommon.Address, _ bool) bool {
	return addr.String() == "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44"
}

const (
	errAssetToken = "0x983e2cC84Bb8eA7b75685F285A28Bde2b4D5aCDA" // nolint gosec:G101 not a hardcoded credential
	tknTestToken  = "0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483" // nolint gosec:G101 not a hardcoded credential
)

func mockAssetResolver(token string) (common.Asset, error) {
	if strings.EqualFold(token, NativeTokenAddr) {
		return common.ETHAsset, nil
	}
	if strings.EqualFold(token, errAssetToken) {
		return common.EmptyAsset, fmt.Errorf("fail to parse asset")
	}
	if strings.EqualFold(token, tknTestToken) {
		return common.NewAsset("ETH.TKN-0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483")
	}
	return common.NewAsset(token)
}

func mockTokenDecimalResolver(_ string) int64 {
	return 8
}

func mockAmountConverter(_ string, amt *big.Int) cosmos.Uint {
	return cosmos.NewUintFromBigInt(amt)
}

func (t *SmartContractLogParserTestSuite) getDepositEvent(smartContractAddr, to, asset string, amount *big.Int, memo string) *etypes.Log {
	evt, err := t.abi.EventByID(ecommon.HexToHash(depositEvent))
	if err != nil {
		return nil
	}
	depositData, err := evt.Inputs.NonIndexed().Pack(amount, memo)
	if err != nil {
		return nil
	}
	return &etypes.Log{
		Address: ecommon.HexToAddress(smartContractAddr),
		Topics: []ecommon.Hash{
			ecommon.HexToHash(depositEvent),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(to).Bytes(), ecommon.HashLength)),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(asset).Bytes(), ecommon.HashLength)),
		},
		Data: depositData,
	}
}

func (t *SmartContractLogParserTestSuite) getTransferOutEvent(smartContractAddr, vault, to, asset string, amount *big.Int, memo string) *etypes.Log {
	evt, err := t.abi.EventByID(ecommon.HexToHash(transferOutEvent))
	if err != nil {
		return nil
	}
	transferOutData, err := evt.Inputs.NonIndexed().Pack(ecommon.HexToAddress(asset), amount, memo)
	if err != nil {
		return nil
	}
	return &etypes.Log{
		Address: ecommon.HexToAddress(smartContractAddr),
		Topics: []ecommon.Hash{
			ecommon.HexToHash(transferOutEvent),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(vault).Bytes(), ecommon.HashLength)),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(to).Bytes(), ecommon.HashLength)),
		},
		Data: transferOutData,
	}
}

func (t *SmartContractLogParserTestSuite) getTransferAllowanceEvent(smartContractAddr, vault, to, asset string, amount *big.Int, memo string) *etypes.Log {
	evt, err := t.abi.EventByID(ecommon.HexToHash(transferAllowanceEvent))
	if err != nil {
		return nil
	}
	transferAllowanceData, err := evt.Inputs.NonIndexed().Pack(ecommon.HexToAddress(asset), amount, memo)
	if err != nil {
		return nil
	}
	return &etypes.Log{
		Address: ecommon.HexToAddress(smartContractAddr),
		Topics: []ecommon.Hash{
			ecommon.HexToHash(transferAllowanceEvent),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(vault).Bytes(), ecommon.HashLength)),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(to).Bytes(), ecommon.HashLength)),
		},
		Data: transferAllowanceData,
	}
}

func (t *SmartContractLogParserTestSuite) TestGetTxInItem_DepositEvents(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 2, true)

	// when logs are empty
	isVaultTransfer, err := parser.GetTxInItem(nil, &types.TxInItem{})
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(err, IsNil)

	// when log is not emit by router contract
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		{
			Address: ecommon.HexToAddress("0xe17d9cf3620ea447eed9089a22096a63bbd63eb4"),
			Topics:  nil,
		},
	}, &types.TxInItem{})
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(err, IsNil)

	// Deposit with zero amount should be ignored
	txInItem := &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", NativeTokenAddr, big.NewInt(0), "ADD:ETH.ETH:tthor16xxn0cadruuw6a2qwpv35av0mehryvdzzjz3af"),
	}, txInItem)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(err, IsNil)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")

	// Deposit, invalid asset should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", errAssetToken, big.NewInt(1024000), "ADD:ETH.ETH:tthor16xxn0cadruuw6a2qwpv35av0mehryvdzzjz3af"),
	}, txInItem)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(err, IsNil)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")

	// Normal Deposit
	txInItem = &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", NativeTokenAddr, big.NewInt(1024000), "ADD:ETH.ETH:tthor16xxn0cadruuw6a2qwpv35av0mehryvdzzjz3af"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "0x6C4a2eEB8531E3C18BcA51104Df7eb2377708263")
	c.Assert(txInItem.Memo, Equals, "ADD:ETH.ETH:tthor16xxn0cadruuw6a2qwpv35av0mehryvdzzjz3af")
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1024000)))), Equals, true)

	// multiple Deposit events , which has different to address should result in an error
	txInItem = &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			NativeTokenAddr,
			big.NewInt(1024000),
			"ADD:ETH.ETH:tthor16xxn0cadruuw6a2qwpv35av0mehryvdzzjz3af"),
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x6c4a2eeb8531e3c18bca51104df7eb2377708263",
			NativeTokenAddr,
			big.NewInt(1024000),
			"ADD:ETH.ETH:tthor16xxn0cadruuw6a2qwpv35av0mehryvdzzjz3af"),
	}, txInItem)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(err, NotNil)

	// multiple Deposit events , which has different memo should result in an error
	txInItem = &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			NativeTokenAddr,
			big.NewInt(1024000),
			"ADD:ETH.ETH:tthor16xxn0cadruuw6a2qwpv35av0mehryvdzzjz3af"),
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			NativeTokenAddr,
			big.NewInt(1024000),
			"whatever"),
	}, txInItem)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(err, NotNil)

	// multiple Deposit events , if one of the deposit event has an asset that can't be resolved , it should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			NativeTokenAddr,
			big.NewInt(1024000),
			"MIGRATE:1024"),
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			errAssetToken,
			big.NewInt(1024000),
			"MIGRATE:1024"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473")
	c.Assert(txInItem.Memo, Equals, "MIGRATE:1024")
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1024000)),
	)), Equals, true)

	// multiple Deposit events , same
	txInItem = &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			NativeTokenAddr,
			big.NewInt(1024000),
			"MIGRATE:1024"),
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			"0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483",
			big.NewInt(2048000),
			"MIGRATE:1024"),
	}, txInItem)
	c.Assert(err, IsNil)
	tknAsset, err := common.NewAsset("ETH.TKN-0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483")
	c.Assert(err, IsNil)
	c.Assert(tknAsset.IsEmpty(), Equals, false)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473")
	c.Assert(txInItem.Memo, Equals, "MIGRATE:1024")
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1024000)),
		common.NewCoin(tknAsset, cosmos.NewUint(2048000)),
	)), Equals, true)
}

func (t *SmartContractLogParserTestSuite) TestGetTxInItem_TransferOutEvents(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 2, true)
	// corrupted transferOutEvent should be ignored
	txInItem := &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}
	logItem := t.getTransferOutEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", tknTestToken, big.NewInt(1024000), "OUT:"+types2.GetRandomTxHash().String())
	logItem.Data = []byte("whatever")
	isVaultTransfer, err := parser.GetTxInItem([]*etypes.Log{
		logItem,
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")

	// invalid asset in transfer out should result in an error
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferOutEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", errAssetToken, big.NewInt(1024000), "OUT:"+types2.GetRandomTxHash().String()),
	}, txInItem)
	c.Assert(err, NotNil)
	c.Assert(isVaultTransfer, Equals, false)

	// invalid memo in transfer out should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferOutEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", NativeTokenAddr, big.NewInt(1024000), "whatever"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)
	// incorrect memo in transfer out should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferOutEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", NativeTokenAddr, big.NewInt(1024000), "ADD:ETH.ETH"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)
}

func (t *SmartContractLogParserTestSuite) TestGetTxInItem_TransferAllowance(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 2, true)
	// corrupted transferAllowance should be ignored
	txInItem := &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}
	logItem := t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", tknTestToken, big.NewInt(1024000), "MIGRATE:1024")
	logItem.Data = []byte("whatever")
	isVaultTransfer, err := parser.GetTxInItem([]*etypes.Log{
		logItem,
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")

	// invalid asset in transferAllowance should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", errAssetToken, big.NewInt(1024000), "MIGRATE:1024"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")

	// TransferAllowance with zero amount should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", NativeTokenAddr, big.NewInt(0), "MIGRATE:1024"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")

	// TransferAllowance with different sender, should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", NativeTokenAddr, big.NewInt(102400), "MIGRATE:1024"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")

	// TransferAllowance with different to address, should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
		To:     "0x9EcA25ee04FDCc9d9CDFF377aa8da019Dba38437",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", NativeTokenAddr, big.NewInt(102400), "MIGRATE:1024"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "0x9EcA25ee04FDCc9d9CDFF377aa8da019Dba38437")
	c.Assert(txInItem.Memo, Equals, "")

	// TransferAllowance with different sender, should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", NativeTokenAddr, big.NewInt(102400), "MIGRATE:1024"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Memo, Equals, "")

	// TransferAllowance with different memo, should be ignored
	txInItem = &types.TxInItem{
		Sender: "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473",
	}

	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", NativeTokenAddr, big.NewInt(102400), "MIGRATE:1024"),
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x3fd2D4cE97B082d4BcE3f9fee2A3D60668D2f473", "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", NativeTokenAddr, big.NewInt(102400), "whatever"),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "0x9EcA25ee04FDCc9d9CDFF377aa8da019Dba38437")
	c.Assert(txInItem.Memo, Equals, "MIGRATE:1024")
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(
		common.NewCoin(common.ETHAsset, cosmos.NewUint(102400)),
	)), Equals, true)
}

func (t *SmartContractLogParserTestSuite) TestFakeDeposit(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 2, true)
	// When user deposit , if user use a malicious contract to trigger transferAllowance before Deposit
	// it should fail , because memo is not the same
	txInItem := &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	isVaultTransfer, err := parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263",
			tknTestToken, big.NewInt(102400), "MIGRATE:1024"),
		t.getDepositEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x6c4a2eeb8531e3c18bca51104df7eb2377708263", tknTestToken, big.NewInt(204800), "SWAP:BTC.BTC:tb1q2mwvhprtsgtamrte23mptehnsjwe3a7j4yvdn4"),
	}, txInItem)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(err, NotNil)
}

func (t *SmartContractLogParserTestSuite) TestFakeTransferOut(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 2, true)
	tknAsset, err := common.NewAsset("ETH.TKN-0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483")
	c.Assert(err, IsNil)

	// When transfer out , user put in a smart contract as destination address,
	// when the smart contract get called , it call in to Router's transferAllowance function , trigger a transferAllowance event
	// trying to trigger bifrost to ignore TransferOutEvent
	// the transferAllowance Event should be override by TransferOut, 1) Sender will not match, 2)
	txInItem := &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	outboundHash := types2.GetRandomTxHash().String()
	isVaultTransfer, err := parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			"0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", tknTestToken, big.NewInt(2048000), "MIGRATE:1024"),
		t.getTransferOutEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x6c4a2eeb8531e3c18bca51104df7eb2377708263",
			"0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", tknTestToken,
			big.NewInt(1024000), "OUT:"+outboundHash),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "0x9EcA25ee04FDCc9d9CDFF377aa8da019Dba38437")
	c.Assert(txInItem.Memo, Equals, "OUT:"+outboundHash)
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(
		common.NewCoin(tknAsset, cosmos.NewUint(1024000)),
	)), Equals, true)

	// This test a scenario that user triggered both transfer allowance, and a transfer out event
	// transfer out event should be observed
	txInItem = &types.TxInItem{
		Sender: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
	}
	outboundHash = types2.GetRandomTxHash().String()
	isVaultTransfer, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferAllowanceEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44", "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			"0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", tknTestToken, big.NewInt(2048000), "MIGRATE:1024"),
		t.getTransferOutEvent("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
			"0x6c4a2eeb8531e3c18bca51104df7eb2377708263",
			"0x9eca25ee04fdcc9d9cdff377aa8da019dba38437", tknTestToken,
			big.NewInt(1024000), "OUT:"+outboundHash),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.To, Equals, "0x9EcA25ee04FDCc9d9CDFF377aa8da019Dba38437")
	c.Assert(txInItem.Memo, Equals, "OUT:"+outboundHash)
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(
		common.NewCoin(tknAsset, cosmos.NewUint(1024000)),
	)), Equals, true)
}

func (t *SmartContractLogParserTestSuite) getTransferFailedEvent(smartContractAddr, vault, to, asset string, amount *big.Int, memo string) *etypes.Log {
	evt, err := t.abi.EventByID(ecommon.HexToHash(transferFailedEvent))
	if err != nil {
		return nil
	}
	data, err := evt.Inputs.NonIndexed().Pack(ecommon.HexToAddress(asset), amount, memo)
	if err != nil {
		return nil
	}
	return &etypes.Log{
		Address: ecommon.HexToAddress(smartContractAddr),
		Topics: []ecommon.Hash{
			ecommon.HexToHash(transferFailedEvent),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(vault).Bytes(), ecommon.HashLength)),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(to).Bytes(), ecommon.HashLength)),
		},
		Data: data,
	}
}

// TestGetTxInItem_BouncedOutbound covers router V6+ TransferFailed handling.
//
// _sendEth returns the ETH to the vault and emits TransferFailed with no memo,
// then the caller emits a full-value TransferOut anyway. Flagging that pairing is
// what stops THORNode debiting coins the vault still holds. The aggregator paths
// also emit TransferFailed, but there the funds DID reach the recipient - those
// must not be flagged.
func (t *SmartContractLogParserTestSuite) TestGetTxInItem_BouncedOutbound(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)

	const (
		router = "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44"
		vault  = "0x6c4a2eeb8531e3c18bca51104df7eb2377708263"
		to     = "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437"
	)
	amount := big.NewInt(1024000)
	memo := "OUT:" + types2.GetRandomTxHash().String()

	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 4, true)

	// bounce: TransferFailed(no memo, native asset) precedes a matching TransferOut.
	// The reported coins and recipient must be untouched, otherwise THORNode's
	// TxOutItem matching fails and the vault gets slashed instead.
	txInItem := &types.TxInItem{Sender: vault}
	isVaultTransfer, err := parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, amount, ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(isVaultTransfer, Equals, false)
	c.Assert(txInItem.Bounced, Equals, true)
	c.Assert(txInItem.To, Equals, ecommon.HexToAddress(to).String())
	c.Assert(txInItem.Memo, Equals, memo)
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1024000)))), Equals, true)

	// aggregator path: TransferFailed carries the outbound memo because the swap
	// failed but the funds were forwarded to the recipient. Not a bounce.
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, amount, memo),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)

	// a TransferFailed for a different amount belongs to another leg, not this one
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, big.NewInt(999), ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)

	// _sendEth only ever bounces the native asset
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, tknTestToken, amount, ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)

	// an ERC20 TransferOut never bounces, even if a native bounce in the same
	// receipt happens to share (to, amount)
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, amount, ""),
		t.getTransferOutEvent(router, vault, to, tknTestToken, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)

	// with the mimir gate off, nothing is flagged
	gated := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 4, false)
	txInItem = &types.TxInItem{Sender: vault}
	_, err = gated.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, amount, ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)
}

func (t *SmartContractLogParserTestSuite) getTransferOutAndCallEvent(smartContractAddr, vault, target, finalAsset, to string, amount, amountOutMin *big.Int, memo string) *etypes.Log {
	evt, err := t.abi.EventByID(ecommon.HexToHash(transferOutAndCallEvent))
	if err != nil {
		return nil
	}
	data, err := evt.Inputs.NonIndexed().Pack(ecommon.HexToAddress(target), amount, ecommon.HexToAddress(finalAsset), ecommon.HexToAddress(to), amountOutMin, memo)
	if err != nil {
		return nil
	}
	return &etypes.Log{
		Address: ecommon.HexToAddress(smartContractAddr),
		Topics: []ecommon.Hash{
			ecommon.HexToHash(transferOutAndCallEvent),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(vault).Bytes(), ecommon.HashLength)),
		},
		Data: data,
	}
}

// TestGetTxInItem_BouncedTransferOutAndCall covers transferOutAndCall on router
// V6+. When the aggregator call fails the router falls back to _sendEth; if that
// also fails the receipt is TransferFailed(no memo, ETH) from _sendEth, then
// TransferFailed(memo, finalToken) from transferOutAndCall, then
// TransferOutAndCall. Only that double failure is a bounce: with a single
// failure the ETH reached the recipient directly.
func (t *SmartContractLogParserTestSuite) TestGetTxInItem_BouncedTransferOutAndCall(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)

	const (
		router     = "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44"
		vault      = "0x6c4a2eeb8531e3c18bca51104df7eb2377708263"
		to         = "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437"
		aggregator = "0x1111111111111111111111111111111111111111"
	)
	amount := big.NewInt(1024000)
	memo := "OUT:" + types2.GetRandomTxHash().String()
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 4, true)

	// double failure: swap failed, then _sendEth to the recipient failed too
	txInItem := &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, amount, ""),
		t.getTransferFailedEvent(router, vault, to, tknTestToken, amount, memo),
		t.getTransferOutAndCallEvent(router, vault, aggregator, tknTestToken, to, amount, big.NewInt(0), memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, true)
	c.Assert(txInItem.To, Equals, ecommon.HexToAddress(to).String())
	c.Assert(txInItem.Memo, Equals, memo)
	c.Assert(txInItem.Aggregator, Equals, ecommon.HexToAddress(aggregator).String())
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1024000)))), Equals, true)

	// single failure: swap failed but _sendEth delivered the ETH to the recipient
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, tknTestToken, amount, memo),
		t.getTransferOutAndCallEvent(router, vault, aggregator, tknTestToken, to, amount, big.NewInt(0), memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)

	// success: no failures at all
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferOutAndCallEvent(router, vault, aggregator, tknTestToken, to, amount, big.NewInt(0), memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)
}

// TestGetTxInItem_BouncedBatchTransferOut covers batchTransferOut, which emits
// one _sendEth (TransferFailed on bounce) followed by TransferOut per leg. The
// parser stops at the first TransferOut, so only the first leg is observed and
// a later leg's bounce must not be attributed to it even when both legs share
// (to, amount).
func (t *SmartContractLogParserTestSuite) TestGetTxInItem_BouncedBatchTransferOut(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)

	const (
		router = "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44"
		vault  = "0x6c4a2eeb8531e3c18bca51104df7eb2377708263"
		to     = "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437"
	)
	amount := big.NewInt(1024000)
	memo1 := "OUT:" + types2.GetRandomTxHash().String()
	memo2 := "OUT:" + types2.GetRandomTxHash().String()
	parser := NewSmartContractLogParser(mockIsValidContractAddr, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 4, true)

	// leg 1 delivered, leg 2 (same recipient and amount) bounced
	txInItem := &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo1),
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, amount, ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo2),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Memo, Equals, memo1)
	c.Assert(txInItem.Bounced, Equals, false)

	// leg 1 bounced, leg 2 delivered
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, amount, ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo1),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo2),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Memo, Equals, memo1)
	c.Assert(txInItem.Bounced, Equals, true)
}

// TestGetTxInItem_BouncedRequiresSameVaultAndRouter: a _sendEth refund only
// establishes that that router returned the coins to that vault. GetTxInItem
// carries collected failures across skipped events and admits logs from every
// router GetContracts allows, so a receipt with nested router activity can hold a
// refund belonging to someone else. Pairing it with this outbound would suppress
// a vault debit for coins that did leave.
func (t *SmartContractLogParserTestSuite) TestGetTxInItem_BouncedRequiresSameVaultAndRouter(c *C) {
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)

	const (
		router      = "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44"
		otherRouter = "0x3fd2d4CE97b082D4bcE3f9fee2A3D60668D2F473"
		vault       = "0x6c4a2eeb8531e3c18bca51104df7eb2377708263"
		otherVault  = "0x9EcA25ee04FDCc9d9CDFF377aa8da019Dba38437"
		to          = "0x9eca25ee04fdcc9d9cdff377aa8da019dba38437"
	)
	amount := big.NewInt(1024000)
	memo := "OUT:" + types2.GetRandomTxHash().String()

	twoRouters := func(addr *ecommon.Address, _ bool) bool {
		return addr.String() == router || addr.String() == otherRouter
	}
	parser := NewSmartContractLogParser(twoRouters, mockAssetResolver, mockTokenDecimalResolver, mockAmountConverter, vaultABI, common.ETHAsset, 4, true)

	// same router, different vault: the refund went back to someone else's vault
	txInItem := &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, otherVault, to, NativeTokenAddr, amount, ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)

	// same vault, different router: a nested call's refund, not this one's
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(otherRouter, vault, to, NativeTokenAddr, amount, ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, false)

	// both match: still a bounce
	txInItem = &types.TxInItem{Sender: vault}
	_, err = parser.GetTxInItem([]*etypes.Log{
		t.getTransferFailedEvent(router, vault, to, NativeTokenAddr, amount, ""),
		t.getTransferOutEvent(router, vault, to, NativeTokenAddr, amount, memo),
	}, txInItem)
	c.Assert(err, IsNil)
	c.Assert(txInItem.Bounced, Equals, true)
}
