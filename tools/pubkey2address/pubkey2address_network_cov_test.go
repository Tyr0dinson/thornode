//go:build mocknet
// +build mocknet

package main

import (
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// TestPubKey2AddressNetworksCov runs main once per network: each selects its
// own bech32 prefixes (and seals the sdk config) before decoding the key.
func TestPubKey2AddressNetworksCov(t *testing.T) {
	cfg := cosmos.GetConfig()
	oldAcc, oldAccPub := cfg.GetBech32AccountAddrPrefix(), cfg.GetBech32AccountPubPrefix()
	oldVal, oldValPub := cfg.GetBech32ValidatorAddrPrefix(), cfg.GetBech32ValidatorPubPrefix()
	oldCons, oldConsPub := cfg.GetBech32ConsensusAddrPrefix(), cfg.GetBech32ConsensusPubPrefix()
	oldNetwork := chainNetwork
	t.Cleanup(func() {
		chainNetwork = oldNetwork
		pk2aCovUnsealSDKConfig()
		cfg.SetBech32PrefixForAccount(oldAcc, oldAccPub)
		cfg.SetBech32PrefixForValidator(oldVal, oldValPub)
		cfg.SetBech32PrefixForConsensusNode(oldCons, oldConsPub)
	})

	// the same secp256k1 key as the mocknet test, re-encoded per network
	_, keyBytes, err := bech32.DecodeAndConvert("tthorpub1addwnpepqv3e9cvwcurmesn2kztc2qcgsmttlp5wqm9kha9rmfuwxzch9nck2fc9vka")
	require.NoError(t, err)

	for _, tc := range []struct {
		network common.ChainNetwork
		header  string
		prefix  string
	}{
		{common.MainNet, "THORChain mainnet:", "thor"},
		{common.StageNet, "THORChain stagenet:", "sthor"},
		{common.ChainNet, "THORChain chainnet:", "cthor"},
		{common.MockNet, "THORChain mocknet:", "tthor"},
	} {
		t.Run(tc.header, func(t *testing.T) {
			chainNetwork = tc.network
			pk, err := bech32.ConvertAndEncode(tc.prefix+"pub", keyBytes)
			require.NoError(t, err)

			out, rec := pk2aCovRunMain(t, "-p", pk)
			require.True(t, strings.HasPrefix(out, tc.header+"\n"), out)

			require.Equal(t, tc.prefix, cfg.GetBech32AccountAddrPrefix())
			require.Equal(t, tc.prefix+"pub", cfg.GetBech32AccountPubPrefix())
			require.Equal(t, tc.prefix+"v", cfg.GetBech32ValidatorAddrPrefix())
			require.Equal(t, tc.prefix+"vpub", cfg.GetBech32ValidatorPubPrefix())
			require.Equal(t, tc.prefix+"c", cfg.GetBech32ConsensusAddrPrefix())
			require.Equal(t, tc.prefix+"cpub", cfg.GetBech32ConsensusPubPrefix())
			// main seals the config after choosing the prefixes
			require.Panics(t, func() { cfg.SetBech32PrefixForAccount("x", "xpub") })

			// the key decodes under the network's prefix, the THOR address is
			// printed, and main still panics at the first ed25519-only chain
			require.Contains(t, out, "THOR Address: "+tc.prefix+"1")
			recErr, ok := rec.(error)
			require.True(t, ok, "expected error panic, got %v", rec)
			require.EqualError(t, recErr, "invalid pubkey length 33")
		})
	}

	// an unknown network leaves the sdk config untouched (no header, no seal),
	// so a key in the current prefix still decodes
	t.Run("unknown network", func(t *testing.T) {
		pk2aCovUnsealSDKConfig()
		cfg.SetBech32PrefixForAccount("tthor", "tthorpub")
		chainNetwork = common.ChainNetwork(99)
		pk, err := bech32.ConvertAndEncode("tthorpub", keyBytes)
		require.NoError(t, err)

		out, rec := pk2aCovRunMain(t, "-p", pk)
		require.False(t, strings.HasPrefix(out, "THORChain"), out)
		require.Contains(t, out, "THOR Address: tthor1")
		require.Equal(t, "tthor", cfg.GetBech32AccountAddrPrefix())
		require.NotPanics(t, func() { cfg.SetBech32PrefixForAccount("tthor", "tthorpub") }, "config was not sealed")
		_, ok := rec.(error)
		require.True(t, ok, "expected error panic, got %v", rec)
	})
}
