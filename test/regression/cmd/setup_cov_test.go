package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// regressionDirCov is the test/regression directory. The package init() in
// template.go parses "templates/*.yaml" relative to the working directory, which
// `go test` sets to the package directory (test/regression/cmd). Package-level
// variable initialisation runs before any init() function, so we move into the
// parent directory here - the same working directory the regtest binary uses.
var regressionDirCov = func() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if filepath.Base(wd) == "cmd" {
		wd = filepath.Dir(wd)
		if err = os.Chdir(wd); err != nil {
			panic(err)
		}
	}
	return wd
}()

// asCov asserts the dynamic type of v.
func asCov[T any](t testing.TB, v any) T {
	t.Helper()
	r, ok := v.(T)
	if !ok {
		t.Fatalf("unexpected type %T, want %T", v, r)
	}
	return r
}

// mapCov asserts v is a JSON object.
func mapCov(t testing.TB, v any) map[string]any {
	t.Helper()
	return asCov[map[string]any](t, v)
}

// portCov returns the TCP port of a listener address.
func portCov(t testing.TB, addr net.Addr) int {
	t.Helper()
	return asCov[*net.TCPAddr](t, addr).Port
}

// chdirCov changes the working directory for the duration of the test.
func chdirCov(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func TestSetupCov(t *testing.T) {
	if templates == nil || templates.Lookup("btc-pool-state.yaml") == nil {
		t.Fatal("expected templates to be parsed from test/regression/templates")
	}
	if filepath.Base(regressionDirCov) != "regression" {
		t.Fatalf("unexpected regression dir: %s", regressionDirCov)
	}
}
