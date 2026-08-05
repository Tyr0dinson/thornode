//go:build !stagenet && !chainnet && !mocknet
// +build !stagenet,!chainnet,!mocknet

package utxo

import (
	"strings"

	"github.com/gcash/bchutil"
	. "gopkg.in/check.v1"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func (s *ECashSuite) TestMainnetAddressAndSignerPaths(c *C) {
	client := &Client{}
	client.cfg.ChainID = common.XECChain

	params := client.getChainCfgXEC()
	c.Assert(params, NotNil)
	c.Check(params.CashAddressPrefix, Equals, "ecash")

	pubkey := common.PubKey("thorpub1addwnpepqt7qug8vk9r3saw8n4r803ydj2g3dqwx0mvq5akhnze86fc536xcy2cr8a2")
	vaultAddress, err := pubkey.GetAddress(common.XECChain)
	c.Assert(err, IsNil)
	c.Assert(strings.Contains(vaultAddress.String(), ":"), Equals, false)

	bareAddress := vaultAddress.String()
	prefixedAddress := "ecash:" + bareAddress
	c.Check(client.normalizeCashAddress(prefixedAddress), Equals, vaultAddress.String())
	c.Check(client.normalizeCashAddress(vaultAddress.String()), Equals, vaultAddress.String())

	decoded, err := bchutil.DecodeAddress(vaultAddress.String(), params)
	c.Assert(err, IsNil)
	c.Check(client.normalizeCashAddress(decoded.String()), Equals, vaultAddress.String())

	// getSourceScript is the network-specific address-decoding step used by
	// the signer before it constructs and signs an XEC transaction.
	sourceScript, err := client.getSourceScript(stypes.TxOutItem{
		Chain:       common.XECChain,
		VaultPubKey: pubkey,
	})
	c.Assert(err, IsNil)
	c.Check(len(sourceScript) > 0, Equals, true)
}
