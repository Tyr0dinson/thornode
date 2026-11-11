package divergence

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// readRepoFile reads a file relative to the repository root, given this test
// file's path (app/divergence/<file>).
func readRepoFile(t *testing.T, testFile, rel string) string {
	t.Helper()
	root := filepath.Join(filepath.Dir(testFile), "..", "..")
	bz, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	return string(bz)
}

func TestFingerprint(t *testing.T) {
	t.Setenv("GOGC", "150")
	fp := Fingerprint(mapOpts{"inter-block-cache": true, "iavl-cache-size": 200000, "pruning": "nothing"})
	require.Equal(t, "true", fp["inter-block-cache"])
	require.Equal(t, "200000", fp["iavl-cache-size"])
	require.Equal(t, "nothing", fp["pruning"])
	require.Equal(t, "<unset>", fp["iavl-disable-fastnode"])
	require.Equal(t, "150", fp["env.GOGC"])
	require.Contains(t, fp, "thornode.telemetry.trace_handlers")
	require.Contains(t, fp, "runtime.gomaxprocs")
	for _, k := range fingerprintKeys {
		require.Contains(t, fp, k)
	}

	fp = Fingerprint(nil)
	require.Equal(t, "<unset>", fp["pruning"])
}

func TestCurrentBuild(t *testing.T) {
	b := CurrentBuild()
	require.Equal(t, runtime.Version(), b.GoVersion)
	require.Equal(t, runtime.GOOS, b.GOOS)
	require.Equal(t, runtime.GOARCH, b.GOARCH)
}
