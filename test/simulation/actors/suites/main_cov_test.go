package suites

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
)

// The thornode package captures its REST base URL from CHAIN_API in an init()
// func, before any test code can run. To point it at an in-process fake without
// touching production code, TestMain re-executes the test binary with CHAIN_API
// (and CHAIN_ID, needed by types.NewUser) set to a loopback listener that the
// child process then serves. Each test swaps the handler via setThornodeCov.

const childAddrEnvCov = "SIMCOV_THORNODE_ADDR"

var (
	thornodeMuCov      sync.Mutex
	thornodeHandlerCov http.Handler = http.NotFoundHandler()
)

// setThornodeCov installs h as the fake thornode handler for the duration of t.
func setThornodeCov(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	thornodeMuCov.Lock()
	thornodeHandlerCov = h
	thornodeMuCov.Unlock()
	t.Cleanup(func() {
		thornodeMuCov.Lock()
		thornodeHandlerCov = http.NotFoundHandler()
		thornodeMuCov.Unlock()
	})
}

func TestMain(m *testing.M) {
	if addr := os.Getenv(childAddrEnvCov); addr != "" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake thornode listen:", err)
			os.Exit(97)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			thornodeMuCov.Lock()
			h := thornodeHandlerCov
			thornodeMuCov.Unlock()
			h.ServeHTTP(w, r)
		})}
		go func() { _ = srv.Serve(ln) }()
		code := m.Run()
		_ = srv.Close()
		os.Exit(code)
	}

	// parent: pick a free loopback port and re-exec with it configured
	for attempt := 0; attempt < 5; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintln(os.Stderr, "reserve port:", err)
			os.Exit(1)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(),
			childAddrEnvCov+"="+addr,
			"CHAIN_API="+addr,
			"CHAIN_ID=thorchain",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Run()
		if err == nil {
			os.Exit(0)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 97 {
				continue // port was taken in between, retry
			}
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "re-exec test binary:", err)
		os.Exit(1)
	}
	os.Exit(1)
}
