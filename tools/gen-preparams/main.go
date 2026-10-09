// Regenerate bifrost/tss/go-tss/test_data/preParam_test.data with fresh
// LocalPreParams that satisfy tss-lib-private v3's tighter ValidateWithProof
// check (requires PaillierSK.P, PaillierSK.Q, Alpha, Beta, P, Q to be set).
// Run from repo root: go run ./tools/gen-preparams 4 > bifrost/tss/go-tss/test_data/preParam_test.data
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
)

// generatePreParams is overridden in tests to skip the slow safe-prime search.
var generatePreParams = keygen.GeneratePreParams

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	n := 4
	if len(args) > 0 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v <= 0 {
			fmt.Fprintf(stderr, "usage: gen-preparams [n]\n")
			return 2
		}
		n = v
	}

	for i := 0; i < n; i++ {
		start := time.Now()
		pp, err := generatePreParams(20 * time.Minute)
		if err != nil {
			fmt.Fprintf(stderr, "party %d: %v\n", i, err)
			return 1
		}
		if !pp.ValidateWithProof() {
			fmt.Fprintf(stderr, "party %d: generated preParams failed ValidateWithProof\n", i)
			return 1
		}
		bz, err := json.Marshal(pp)
		if err != nil {
			fmt.Fprintf(stderr, "party %d marshal: %v\n", i, err)
			return 1
		}
		fmt.Fprintln(stdout, hex.EncodeToString(bz))
		fmt.Fprintf(stderr, "party %d generated in %s\n", i, time.Since(start))
	}
	return 0
}
