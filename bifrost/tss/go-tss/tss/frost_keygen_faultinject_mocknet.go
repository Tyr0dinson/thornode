//go:build mocknet
// +build mocknet

package tss

import (
	"os"
	"strconv"
	"strings"
)

// frostKeygenTestRound3TimeoutMS reads the test-only round-3 wait override
// used by the xmr-keygen-jail simulation (see `make test-simulation-keygen-jail`).
// Returns 0 (no override) unless the environment variable holds a positive
// integer. Compiled into mocknet builds only; every other build uses the
// constant-zero stub, so the fault injection cannot exist in production
// binaries.
func frostKeygenTestRound3TimeoutMS() int {
	v := strings.TrimSpace(os.Getenv("XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS"))
	if v == "" {
		return 0
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms <= 0 {
		return 0
	}
	return ms
}
