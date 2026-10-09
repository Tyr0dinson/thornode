package tokenlist

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/avaxtokens"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/basetokens"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/bsctokens"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/ethtokens"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/poltokens"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/trontokens"
)

func TestMustUnmarshalCov(t *testing.T) {
	var list EVMTokenList
	mustUnmarshal([]byte(`{"name":"x","tokens":[{"address":"0xabc","symbol":"USDC","name":"USD Coin","decimals":6}]}`), &list)
	require.Equal(t, EVMTokenList{
		Name:   "x",
		Tokens: []ERC20Token{{Address: "0xabc", Symbol: "USDC", Name: "USD Coin", Decimals: 6}},
	}, list)

	require.PanicsWithError(t, "unexpected end of JSON input", func() {
		mustUnmarshal([]byte(`{`), &list)
	})
	require.Panics(t, func() {
		mustUnmarshal([]byte(`{"tokens":"not-a-list"}`), &list)
	})
}

// Each per-chain getter returns exactly the decoded embedded list, and
// GetEVMTokenList dispatches to it.
func TestEmbeddedTokenListsCov(t *testing.T) {
	cases := []struct {
		chain common.Chain
		raw   []byte
		get   func() EVMTokenList
	}{
		{common.ETHChain, ethtokens.ETHTokenListRaw, GetETHTokenList},
		{common.AVAXChain, avaxtokens.AVAXTokenListRaw, GetAVAXTokenList},
		{common.BSCChain, bsctokens.BSCTokenListRaw, GetBSCTokenList},
		{common.BASEChain, basetokens.BASETokenListRaw, GetBASETokenList},
		{common.TRONChain, trontokens.TRONTokenListRaw, GetTRONTokenList},
		{common.POLChain, poltokens.POLTokenListRaw, GetPOLTokenList},
	}
	for _, tc := range cases {
		var want EVMTokenList
		require.NoError(t, json.Unmarshal(tc.raw, &want), tc.chain.String())
		require.Equal(t, want, tc.get(), tc.chain.String())
		require.Equal(t, want, GetEVMTokenList(tc.chain), tc.chain.String())
	}
	require.Equal(t, EVMTokenList{}, GetEVMTokenList(common.BTCChain))
}
