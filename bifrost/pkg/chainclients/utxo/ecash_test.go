package utxo

import (
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/gcash/bchutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	utxorpc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type ECashSuite struct{}

var _ = Suite(&ECashSuite{})

func newXECAmountTestClient(c *C) *Client {
	rpcClient, err := utxorpc.NewClient(
		"http://127.0.0.1:1",
		"",
		"",
		0,
		time.Second,
		common.XECChain,
		zerolog.Nop(),
	)
	c.Assert(err, IsNil)
	client := &Client{rpc: rpcClient}
	client.cfg.ChainID = common.XECChain
	return client
}

func (s *ECashSuite) TestNormalizeCashAddressMatchesXECVaultDerivation(c *C) {
	client := &Client{}
	client.cfg.ChainID = common.XECChain

	pk := common.PubKey("thorpub1addwnpepqt7qug8vk9r3saw8n4r803ydj2g3dqwx0mvq5akhnze86fc536xcy2cr8a2")
	if common.CurrentChainNetwork == common.MockNet {
		pk = common.PubKey("tthorpub1addwnpepqwznsrgk2t5vn2cszr6ku6zned6tqxknugzw3vhdcjza284d7djp5rql6vn")
	}
	vaultAddr, err := pk.GetAddress(common.XECChain)
	c.Assert(err, IsNil)

	// THORChain's canonical XEC form includes its network-specific prefix.
	c.Check(common.IsValidXECAddress(vaultAddr.String()), Equals, true)

	params := common.GetXECAddressParams(common.CurrentChainNetwork)
	c.Assert(params, NotNil)
	bare := vaultAddr.String()[len(params.CashAddressPrefix)+1:]
	c.Check(common.IsValidXECAddress(bare), Equals, false)

	// Daemon output must use the same form as the vault address comparison.
	c.Check(client.normalizeCashAddress(bare), Equals, vaultAddr.String())

	// Canonical input remains stable across repeated normalization.
	c.Check(client.normalizeCashAddress(vaultAddr.String()), Equals, vaultAddr.String())

	// The signer round-trip uses the same canonical form as address validation.
	decoded, err := bchutil.DecodeAddress(vaultAddr.String(), params)
	c.Assert(err, IsNil)
	c.Check(client.normalizeCashAddress(decoded.String()), Equals, vaultAddr.String())
}

func (s *ECashSuite) TestNormalizeCashAddressLeavesNonCashAddrAlone(c *C) {
	client := &Client{}
	client.cfg.ChainID = common.XECChain

	c.Check(client.normalizeCashAddress(""), Equals, "")

	// Legacy addresses are not converted into CashAddr payloads.
	legacy := "mkVvHDUJgxRvvBBFkTZTgSthQGgnBRJTQV"
	c.Check(client.normalizeCashAddress(legacy), Equals, legacy)

	// A foreign network prefix must not be rewritten as an XEC prefix.
	other := "bitcoincash:qqsqgupa7tajw5wkunpjezjjnkmmuevcmgs7saqa99"
	c.Check(client.normalizeCashAddress(other), Equals, other)
}

func (s *ECashSuite) TestXECAmountBoundaries(c *C) {
	client := newXECAmountTestClient(c)

	baseUnits, err := client.rpc.ToBaseUnits(1)
	c.Assert(err, IsNil)
	c.Check(baseUnits.Equal(cosmos.NewUint(100)), Equals, true)

	coin, err := client.rpcAmountToThorCoin(1)
	c.Assert(err, IsNil)
	c.Check(coin.Amount.Equal(cosmos.NewUint(common.One)), Equals, true)
	c.Check(coin.Decimals, Equals, int64(2))

	largeCoin := client.toThorCoin(cosmos.NewUintFromString("2000000000000000"))
	c.Check(largeCoin.Amount.Equal(cosmos.NewUintFromString("2000000000000000000000")), Equals, true)
	c.Check(largeCoin.Decimals, Equals, int64(2))

	wireAmount, err := common.XECChain.ThorchainToNativeGasExact(cosmos.NewUint(common.One))
	c.Assert(err, IsNil)
	c.Check(wireAmount.Equal(cosmos.NewUint(100)), Equals, true)

	_, err = common.XECChain.ThorchainToNativeGasExact(cosmos.NewUint(common.One + 1))
	c.Check(err, NotNil)

	tx := stypes.TxOutItem{
		Chain: common.XECChain,
		Coins: common.NewCoins(common.NewCoin(common.XECAsset, cosmos.NewUint(common.One))),
	}
	payment, err := client.getPaymentAmount(tx)
	c.Assert(err, IsNil)
	c.Check(int64(payment), Equals, int64(100))

	tx.Coins[0].Amount = tx.Coins[0].Amount.Add(cosmos.OneUint())
	_, err = client.getPaymentAmount(tx)
	c.Check(err, NotNil)

	// 20 trillion XEC exceeds uint64 in THORChain's normalized 1e8 form,
	// while its native cent amount still fits the transaction wire int64.
	tx.Coins[0].Amount = cosmos.NewUintFromString("2000000000000000000000")
	payment, err = client.getPaymentAmount(tx)
	c.Assert(err, IsNil)
	c.Check(int64(payment), Equals, int64(2_000_000_000_000_000))
}

func (s *ECashSuite) TestXECConsolidationNormalizesNativeUTXOs(c *C) {
	if common.CurrentChainNetwork != common.MockNet {
		c.Skip("XEC address params are network specific")
	}

	client := newXECAmountTestClient(c)
	client.lastFeeRate.Store(2)
	vault := ttypes.Vault{
		PubKey: common.PubKey("tthorpub1addwnpepqwznsrgk2t5vn2cszr6ku6zned6tqxknugzw3vhdcjza284d7djp5rql6vn"),
	}

	txOut, err := client.buildConsolidateTxOutItem(vault, []btcjson.ListUnspentResult{{Amount: 1}})
	c.Assert(err, IsNil)
	coin := txOut.Coins.GetCoin(common.XECAsset)
	c.Check(coin.Amount.Equal(cosmos.NewUint(common.One)), Equals, true)
	c.Check(coin.Decimals, Equals, int64(2))
}
