package scripts_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func runBifrostEntrypoint(t *testing.T, network, curlStub string) bool {
	t.Helper()
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatalf("failed to create mock bin directory: %v", err)
	}

	writeFile := func(name, contents string, executable bool) {
		t.Helper()
		fileName := filepath.Join(tempDir, name)
		if err := os.WriteFile(fileName, []byte(contents), 0o600); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
		if executable {
			if err := os.Chmod(fileName, 0o700); err != nil {
				t.Fatalf("failed to make %s executable: %v", name, err)
			}
		}
	}

	entrypoint, err := os.ReadFile("bifrost.sh")
	if err != nil {
		t.Fatalf("failed to read bifrost entrypoint: %v", err)
	}
	writeFile("bifrost.sh", string(entrypoint), true)
	writeFile("core.sh", `
resolve_signer_seed_phrase() { :; }
create_thor_user() { :; }
SIGNER_NAME=test
SIGNER_PASSWD=test
SIGNER_SEED_PHRASE=test
`, false)
	writeFile("wait-for-thorchain-api.sh", "#!/bin/bash\nexit 0\n", true)

	curlMarker := filepath.Join(tempDir, "curl-called")
	writeFile(filepath.Join("bin", "curl"), "#!/bin/bash\ntouch \"$CURL_MARKER\"\n"+curlStub, true)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	// The command is a test-owned copy in t.TempDir, never user-controlled input.
	//nolint:gosec
	cmd := exec.CommandContext(ctx, filepath.Join(tempDir, "bifrost.sh"), "/bin/bash", "-c", "exit 0")
	cmd.Env = append(os.Environ(),
		"NET="+network,
		"PEER=",
		"BIFROST_CHAINS_SOL_DISABLED=false",
		"BIFROST_CHAINS_SOL_RPC_HOST=http://unavailable-solana.invalid",
		"CURL_MARKER="+curlMarker,
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("%s Bifrost startup blocked waiting for the Solana RPC", network)
		}
		t.Fatalf("%s Bifrost entrypoint failed: %v\n%s", network, err, output)
	}
	if _, err := os.Stat(curlMarker); err == nil {
		return true
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed to inspect curl marker: %v", err)
	}
	return false
}

func TestProductionBifrostDoesNotWaitForSolanaRPC(t *testing.T) {
	if runBifrostEntrypoint(t, "mainnet", "exit 1\n") {
		t.Fatal("production Bifrost entrypoint contacted the Solana RPC before startup")
	}
}

func TestMocknetBifrostWaitsForSolanaRPC(t *testing.T) {
	if !runBifrostEntrypoint(t, "mocknet", "echo '{\"result\":1}'\n") {
		t.Fatal("mocknet Bifrost entrypoint did not wait for the Solana RPC")
	}
}
