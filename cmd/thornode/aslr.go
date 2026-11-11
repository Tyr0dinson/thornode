package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// checkASLR refuses to start on Linux hosts without full ASLR. The node binary
// is built as a position-independent executable, which only randomizes the
// process layout when the kernel has kernel.randomize_va_space=2.
// Set THORNODE_SKIP_ASLR_CHECK=1 to bypass.
func checkASLR() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if skip, err := strconv.ParseBool(os.Getenv("THORNODE_SKIP_ASLR_CHECK")); err == nil && skip {
		return nil
	}
	b, err := os.ReadFile("/proc/sys/kernel/randomize_va_space")
	if err != nil {
		// ponytail: unreadable procfs (restricted container) is not our call to make
		fmt.Fprintf(os.Stderr, "WARNING: ASLR check skipped, cannot read /proc/sys/kernel/randomize_va_space: %s\n", err)
		return nil
	}
	if v := strings.TrimSpace(string(b)); v != "2" {
		return fmt.Errorf("kernel.randomize_va_space=%s, full ASLR is required: run `sysctl -w kernel.randomize_va_space=2` on the host (or set THORNODE_SKIP_ASLR_CHECK=1)", v)
	}
	return nil
}
