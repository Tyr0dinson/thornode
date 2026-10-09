package main

import (
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
)

func lintWLCovCapture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	require.NoError(t, w.Close())
	return <-done
}

// With every EVM whitelist pointed (via env overrides) at tokens from the
// embedded token list for the build network, main() reports each token as
// present and returns without exiting.
//
// NOTE: under the mocknet build tag the default config whitelists mainnet
// token addresses that are absent from the mocknet token lists, so running the
// tool unmodified exits 1; the env overrides keep the test in-process.
func TestLintWhitelistTokensMainCov(t *testing.T) {
	lists := map[string]tokenlist.EVMTokenList{
		"BSC":  tokenlist.GetBSCTokenList(),
		"ETH":  tokenlist.GetETHTokenList(),
		"AVAX": tokenlist.GetAVAXTokenList(),
		"BASE": tokenlist.GetBASETokenList(),
		"POL":  tokenlist.GetPOLTokenList(),
	}
	expected := map[string]string{}
	for chain, tl := range lists {
		if len(tl.Tokens) == 0 {
			continue
		}
		// mixed case on purpose: the comparison is case-insensitive
		addr := strings.ToUpper(tl.Tokens[0].Address[:6]) + tl.Tokens[0].Address[6:]
		t.Setenv("BIFROST_CHAINS_"+chain+"_BLOCK_SCANNER_WHITELIST_TOKENS", addr)
		expected[chain] = "  " + addr + " in " + chain + " token list\n"
	}
	require.NotEmpty(t, expected)

	out := lintWLCovCapture(t, main)
	for _, line := range expected {
		require.Contains(t, out, line)
	}

	require.NotContains(t, out, "not in")
	require.NotContains(t, out, "unsupported chain")

	// every EVM chain with a scanner config is announced
	checked := 0
	for chain := range config.GetBifrost().GetChains() {
		if !chain.IsEVM() {
			continue
		}
		require.Contains(t, out, "checking "+chain.String()+" whitelist tokens...\n")
		checked++
	}
	require.Positive(t, checked)
	require.Equal(t, checked, strings.Count(out, "checking "))

	// every whitelisted token is reported as present
	lineRe := regexp.MustCompile(`^  0[xX][0-9a-fA-F]{40} in [A-Z]+ token list$`)
	found := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "checking ") {
			continue
		}
		require.Regexp(t, lineRe, line)
		found++
	}
	require.Positive(t, found)
}
