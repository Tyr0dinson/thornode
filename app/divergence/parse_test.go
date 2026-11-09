package divergence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/stretchr/testify/require"
)

// cometErrors builds the two validation errors exactly as CometBFT v0.39
// state/validation.go does (HexBytes, %X for state, %v for the block).
func cometErrors() (appHash, resultsHash error) {
	local := cmtbytes.HexBytes{0xde, 0xad, 0xbe, 0xef}
	network := cmtbytes.HexBytes{0xca, 0xfe, 0xba, 0xbe}
	appHash = fmt.Errorf("wrong Block.Header.AppHash.  Expected %X, got %v", local, network)
	resultsHash = fmt.Errorf("wrong Block.Header.LastResultsHash.  Expected %X, got %v", local, network)
	return appHash, resultsHash
}

func TestParseMismatch(t *testing.T) {
	appErr, resErr := cometErrors()

	m, ok := ParseMismatch(appErr.Error())
	require.True(t, ok)
	require.Equal(t, &Mismatch{Type: HashTypeAppHash, Local: "DEADBEEF", Network: "CAFEBABE"}, m)

	m, ok = ParseMismatch(resErr.Error())
	require.True(t, ok)
	require.Equal(t, HashTypeLastResultsHash, m.Type)
	require.Equal(t, "DEADBEEF", m.Local)
	require.Equal(t, "CAFEBABE", m.Network)

	// the consensus panic wraps it (consensus/state.go finalizeCommit)
	wrapped := fmt.Errorf("+2/3 committed an invalid block: %w", appErr)
	m, ok = ParseMismatch(wrapped.Error())
	require.True(t, ok)
	require.Equal(t, "CAFEBABE", m.Network)
	require.True(t, IsHashMismatch(wrapped.Error()))
	require.True(t, IsHashMismatch(resErr.Error()))

	// empty hashes still parse
	m, ok = ParseMismatch("wrong Block.Header.AppHash.  Expected , got ")
	require.True(t, ok)
	require.Empty(t, m.Local)

	_, ok = ParseMismatch("wrong Block.Header.ValidatorsHash.  Expected AA, got BB")
	require.False(t, ok)
	require.False(t, IsHashMismatch("wrong Block.Header.ValidatorsHash"))
}

func TestErrFromKeyvals(t *testing.T) {
	appErr, _ := cometErrors()
	require.Equal(t, appErr.Error(), ErrFromKeyvals([]any{"err", appErr, "stack", "..."}))
	require.Equal(t, "s", ErrFromKeyvals([]any{"x", 1, "err", "s"}))
	require.Equal(t, "7", ErrFromKeyvals([]any{"err", 7}))
	require.Equal(t, "AB", ErrFromKeyvals([]any{"err", cmtbytes.HexBytes{0xab}}))
	require.Empty(t, ErrFromKeyvals([]any{"err", nil}))
	require.Empty(t, ErrFromKeyvals([]any{"err"}))
	require.Empty(t, ErrFromKeyvals(nil))
	require.Equal(t, "e", errString(errors.New("e")))
}

func TestDivergentHeight(t *testing.T) {
	// block 101's header carries the app hash of committed height 100
	require.Equal(t, int64(100), DivergentHeight(101))
	require.Equal(t, int64(1), DivergentHeight(2))
	require.Equal(t, int64(0), DivergentHeight(1))
	require.Equal(t, int64(0), DivergentHeight(0))
}

func cometDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-json", "github.com/cometbft/cometbft").Output()
	require.NoError(t, err)
	var mod struct{ Dir string }
	require.NoError(t, json.Unmarshal(out, &mod))
	require.NotEmpty(t, mod.Dir)
	return mod.Dir
}

// TestCometMessagesUnchanged fails when a CometBFT upgrade changes the strings
// the parser and the logger hook depend on.
func TestCometMessagesUnchanged(t *testing.T) {
	dir := cometDir(t)
	src, err := os.ReadFile(filepath.Join(dir, "state", "validation.go"))
	require.NoError(t, err)
	for _, needle := range []string{
		`"wrong Block.Header.AppHash.  Expected %X, got %v"`,
		`"wrong Block.Header.LastResultsHash.  Expected %X, got %v"`,
	} {
		require.True(t, strings.Contains(string(src), needle), "state/validation.go lost %s", needle)
	}
	src, err = os.ReadFile(filepath.Join(dir, "blocksync", "reactor.go"))
	require.NoError(t, err)
	// any keyvals may precede "err"; the hook only needs both in one call
	require.Regexp(t, `Logger\.Error\("Error in validation",[^\n]*"err", err\)`, string(src))
	src, err = os.ReadFile(filepath.Join(dir, "consensus", "state.go"))
	require.NoError(t, err)
	require.Contains(t, string(src), `"+2/3 committed an invalid block: %w"`)
	require.Contains(t, string(src), `cs.Logger.Error("CONSENSUS FAILURE!!!", "err", r`)
}
