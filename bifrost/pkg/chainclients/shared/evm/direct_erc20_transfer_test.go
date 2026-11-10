package evm

import (
	"math/big"
	"strings"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/rs/zerolog/log"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type DirectERC20TransferTestSuite struct{}

var _ = Suite(&DirectERC20TransferTestSuite{})

type mockPoolAddressProvider struct {
	pubkeys common.PubKeys
}

func (p mockPoolAddressProvider) IsActivePoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	for _, pk := range p.pubkeys {
		cpi, err := common.NewChainPoolInfo(chain, pk)
		if err != nil {
			continue
		}
		if strings.EqualFold(cpi.PoolAddress.String(), addr) {
			return true, cpi
		}
	}
	return false, common.EmptyChainPoolInfo
}

type mockDirectERC20TransferTokenResolver struct{}

func (r mockDirectERC20TransferTokenResolver) GetAssetFromTokenAddress(token string) (common.Asset, error) {
	return mockAssetResolver(token)
}

func (r mockDirectERC20TransferTokenResolver) GetTokenDecimalsForTHORChain(token string) int64 {
	return mockTokenDecimalResolver(token)
}

func (r mockDirectERC20TransferTokenResolver) ConvertAmount(token string, amt *big.Int) cosmos.Uint {
	return mockAmountConverter(token, amt)
}

func getERC20TransferEvent(token, from, to string, amount *big.Int) *etypes.Log {
	return &etypes.Log{
		Address: ecommon.HexToAddress(token),
		Topics: []ecommon.Hash{
			ecommon.HexToHash(erc20TransferEvent),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(from).Bytes(), ecommon.HashLength)),
			ecommon.BytesToHash(ecommon.LeftPadBytes(ecommon.HexToAddress(to).Bytes(), ecommon.HashLength)),
		},
		Data: ecommon.LeftPadBytes(amount.Bytes(), ecommon.HashLength),
	}
}

func (t *DirectERC20TransferTestSuite) TestGetTxInFromDirectERC20TransferRequiresTransferSelector(c *C) {
	token := ecommon.HexToAddress(tknTestToken)
	receipt := &etypes.Receipt{Status: 1}

	tx := etypes.NewTransaction(1, token, big.NewInt(0), 100000, big.NewInt(1), []byte{0x23, 0xb8, 0x72, 0xdd})
	txInItem, err := GetTxInFromDirectERC20Transfer(tx, receipt, DirectERC20TransferTxInOptions{})
	c.Assert(err, IsNil)
	c.Assert(txInItem, IsNil)

	truncated := make([]byte, 4+ecommon.HashLength)
	copy(truncated, erc20TransferSelector)
	for _, data := range [][]byte{erc20TransferSelector[:3], erc20TransferSelector, truncated} {
		tx = etypes.NewTransaction(1, token, big.NewInt(0), 100000, big.NewInt(1), data)
		txInItem, err = GetTxInFromDirectERC20Transfer(tx, receipt, DirectERC20TransferTxInOptions{})
		c.Assert(err, IsNil)
		c.Assert(txInItem, IsNil)
	}
}

func (t *DirectERC20TransferTestSuite) TestParseDirectERC20TransferLogs(c *C) {
	sender := ecommon.HexToAddress("0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473")
	otherSender := ecommon.HexToAddress("0x1fd2d4ce97b082d4bce3f9fee2a3d60668d2f473")
	pubkey := ttypes.GetRandomPubKey()
	cpi, err := common.NewChainPoolInfo(common.ETHChain, pubkey)
	c.Assert(err, IsNil)
	vault := ecommon.HexToAddress(cpi.PoolAddress.String())
	token := ecommon.HexToAddress(tknTestToken)
	tknAsset, err := common.NewAsset("ETH.TKN-0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483")
	c.Assert(err, IsNil)
	opts := DirectERC20TransferLogOptions{
		Chain:               common.ETHChain,
		MaxLogs:             4,
		PoolAddressProvider: mockPoolAddressProvider{pubkeys: common.PubKeys{pubkey}},
		TokenResolver:       mockDirectERC20TransferTokenResolver{},
		Logger:              log.Logger,
	}

	txInItem := &stypes.TxInItem{
		Tx:     "direct",
		Sender: strings.ToLower(sender.String()),
	}
	observed, err := ParseDirectERC20TransferLogs([]*etypes.Log{
		getERC20TransferEvent(token.String(), sender.String(), vault.String(), big.NewInt(2048000)),
	}, txInItem, token, sender, vault, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, true)
	c.Assert(txInItem.To, Equals, vault.String())
	c.Assert(txInItem.Memo, Equals, "")
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(
		common.NewCoin(tknAsset, cosmos.NewUint(2048000)),
	)), Equals, true)

	// The low 20 bytes match the vault, but the indexed topic is not its address.
	malformed := getERC20TransferEvent(token.String(), sender.String(), vault.String(), big.NewInt(2048000))
	malformed.Topics[2][0] = 1
	txInItem = &stypes.TxInItem{}
	observed, err = ParseDirectERC20TransferLogs([]*etypes.Log{malformed}, txInItem, token, sender, vault, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)

	// A valid Transfer to a vault is incidental if the call names someone else.
	txInItem = &stypes.TxInItem{}
	observed, err = ParseDirectERC20TransferLogs([]*etypes.Log{
		getERC20TransferEvent(token.String(), sender.String(), vault.String(), big.NewInt(2048000)),
	}, txInItem, token, sender, otherSender, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)

	// Matching calldata and log recipients still need to name an active vault.
	txInItem = &stypes.TxInItem{}
	observed, err = ParseDirectERC20TransferLogs([]*etypes.Log{
		getERC20TransferEvent(token.String(), sender.String(), otherSender.String(), big.NewInt(2048000)),
	}, txInItem, token, sender, otherSender, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)

	txInItem = &stypes.TxInItem{Tx: "duplicate-transfer", Sender: strings.ToLower(sender.String())}
	observed, err = ParseDirectERC20TransferLogs([]*etypes.Log{
		getERC20TransferEvent(token.String(), sender.String(), vault.String(), big.NewInt(2048000)),
		getERC20TransferEvent(token.String(), sender.String(), vault.String(), big.NewInt(4096000)),
	}, txInItem, token, sender, vault, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, true)
	c.Assert(txInItem.To, Equals, vault.String())
	c.Assert(txInItem.Coins, HasLen, 1)
	c.Assert(txInItem.Coins.EqualsEx(common.NewCoins(
		common.NewCoin(tknAsset, cosmos.NewUint(2048000)),
	)), Equals, true)

	txInItem = &stypes.TxInItem{Tx: "zero-transfer", Sender: strings.ToLower(sender.String())}
	observed, err = ParseDirectERC20TransferLogs([]*etypes.Log{
		getERC20TransferEvent(token.String(), sender.String(), vault.String(), big.NewInt(0)),
	}, txInItem, token, sender, vault, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	c.Assert(txInItem.To, Equals, "")
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)

	// transferFrom-style direct calls are ignored because refunding the tx signer
	// could differ from refunding the token source.
	txInItem = &stypes.TxInItem{Tx: "transfer-from", Sender: strings.ToLower(sender.String())}
	observed, err = ParseDirectERC20TransferLogs([]*etypes.Log{
		getERC20TransferEvent(token.String(), otherSender.String(), vault.String(), big.NewInt(2048000)),
	}, txInItem, token, sender, vault, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)

	// Internal Transfer logs from router or aggregator transactions are ignored:
	// the ERC20 log address must match tx.To().
	txInItem = &stypes.TxInItem{Tx: "router-transfer", Sender: strings.ToLower(sender.String())}
	observed, err = ParseDirectERC20TransferLogs([]*etypes.Log{
		getERC20TransferEvent(token.String(), sender.String(), vault.String(), big.NewInt(2048000)),
	}, txInItem, ecommon.HexToAddress("0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44"), sender, vault, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)

	txInItem = &stypes.TxInItem{Tx: "unsupported-token", Sender: strings.ToLower(sender.String())}
	observed, err = ParseDirectERC20TransferLogs([]*etypes.Log{
		getERC20TransferEvent(errAssetToken, sender.String(), vault.String(), big.NewInt(2048000)),
	}, txInItem, ecommon.HexToAddress(errAssetToken), sender, vault, opts)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	c.Assert(txInItem.Coins.IsEmpty(), Equals, true)
}
