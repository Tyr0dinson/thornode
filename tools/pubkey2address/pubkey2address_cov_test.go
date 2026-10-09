//go:build mocknet
// +build mocknet

package main

import (
	"flag"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// pk2aCovRunMain runs main() with the given args and returns captured stdout
// and any recovered panic value.
func pk2aCovRunMain(t *testing.T, args ...string) (out string, recovered any) {
	t.Helper()

	pk2aCovUnsealSDKConfig()
	oldArgs, oldFlags, oldStdout := os.Args, flag.CommandLine, os.Stdout
	defer func() {
		os.Args, flag.CommandLine, os.Stdout = oldArgs, oldFlags, oldStdout
	}()
	os.Args = append([]string{"pubkey2address"}, args...)
	flag.CommandLine = flag.NewFlagSet("pubkey2address", flag.ContinueOnError)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	func() {
		defer func() { recovered = recover() }()
		main()
	}()

	require.NoError(t, w.Close())
	out = <-done
	return out, recovered
}

func TestPubKey2AddressMainCov(t *testing.T) {
	// No pubkey: panics before touching the global sdk config.
	_, rec := pk2aCovRunMain(t)
	require.Equal(t, "no pubkey provided", rec)

	// Malformed pubkey: header printed, then NewPubKey's error is the panic.
	out, rec := pk2aCovRunMain(t, "-p", "not-a-pubkey")
	require.Equal(t, "THORChain mocknet:\n", out)
	_, isErr := rec.(error)
	require.True(t, isErr, "expected error panic, got %v", rec)

	// Valid mocknet secp256k1 pubkey: prints the network header and the
	// address for every chain up to SOL, then panics because SOL (and the
	// later TAO/DOT entries) require a 32-byte ed25519 key. No single pubkey
	// can satisfy every entry in common.AllChains, so the tool always panics.
	pk := "tthorpub1addwnpepqv3e9cvwcurmesn2kztc2qcgsmttlp5wqm9kha9rmfuwxzch9nck2fc9vka"
	out, rec = pk2aCovRunMain(t, "-p", pk)
	recErr, ok := rec.(error)
	require.True(t, ok, "expected error panic, got %v", rec)
	require.EqualError(t, recErr, "invalid pubkey length 33")
	require.True(t, strings.HasPrefix(out, "THORChain mocknet:\n"), out)

	pubKey, err := common.NewPubKey(pk)
	require.NoError(t, err)
	for _, chain := range common.AllChains {
		if chain.Equals(common.SOLChain) {
			require.NotContains(t, out, chain.String()+" Address:")
			break
		}
		addr, addrErr := pubKey.GetAddress(chain)
		require.NoError(t, addrErr)
		require.Contains(t, out, chain.String()+" Address: "+addr.String()+"\n")
	}
	thorAddr, err := pubKey.GetAddress(common.THORChain)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(thorAddr.String(), "tthor1"), thorAddr.String())
}

// pk2aCovUnsealSDKConfig resets the cosmos-sdk global config seal so main(),
// which sets bech32 prefixes and seals, can run more than once per process.
func pk2aCovUnsealSDKConfig() {
	v := reflect.ValueOf(cosmos.GetConfig()).Elem()
	sealed := v.FieldByName("sealed")
	reflect.NewAt(sealed.Type(), unsafe.Pointer(sealed.UnsafeAddr())).Elem().SetBool(false) //nolint:gosec
	ch := v.FieldByName("sealedch")
	reflect.NewAt(ch.Type(), unsafe.Pointer(ch.UnsafeAddr())).Elem(). //nolint:gosec
										Set(reflect.ValueOf(make(chan struct{})))
}
