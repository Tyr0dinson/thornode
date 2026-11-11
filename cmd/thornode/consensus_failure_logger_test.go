package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/log"

	"gitlab.com/thorchain/thornode/v3/app/divergence"
)

// recordingLogger captures log calls for testing.
type recordingLogger struct {
	log.Logger
	errorCalls []string
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{Logger: log.NewNopLogger()}
}

func (r *recordingLogger) Error(msg string, keyvals ...interface{}) {
	r.errorCalls = append(r.errorCalls, msg)
}

func (r *recordingLogger) With(keyvals ...interface{}) log.Logger {
	return r
}

func (r *recordingLogger) Impl() interface{} {
	return nil
}

func TestConsensusFailureLogger_NormalErrors(t *testing.T) {
	inner := newRecordingLogger()
	logger := NewConsensusFailureLogger(inner)

	// Normal error messages should pass through without exiting.
	logger.Error("some error", "key", "value")
	logger.Error("another error")

	if len(inner.errorCalls) != 2 {
		t.Fatalf("expected 2 error calls, got %d", len(inner.errorCalls))
	}
	if inner.errorCalls[0] != "some error" {
		t.Errorf("expected first call to be 'some error', got %q", inner.errorCalls[0])
	}
	if inner.errorCalls[1] != "another error" {
		t.Errorf("expected second call to be 'another error', got %q", inner.errorCalls[1])
	}
}

func TestConsensusFailureLogger_WithPreservesWrapper(t *testing.T) {
	inner := newRecordingLogger()
	logger := NewConsensusFailureLogger(inner)

	// With() should return a ConsensusFailureLogger wrapping the sub-logger.
	subLogger := logger.With("module", "consensus")
	if _, ok := subLogger.(ConsensusFailureLogger); !ok {
		t.Fatalf("expected ConsensusFailureLogger from With(), got %T", subLogger)
	}
}

func TestConsensusFailureLogger_ImplDelegates(t *testing.T) {
	inner := newRecordingLogger()
	logger := NewConsensusFailureLogger(inner)

	// Impl() should delegate to the inner logger.
	if logger.Impl() != nil {
		t.Errorf("expected nil from Impl(), got %v", logger.Impl())
	}
}

// TestConsensusFailureMsg_MatchesCometBFT verifies that our consensusFailureMsg
// constant matches the string literal used in CometBFT's consensus/state.go.
// This will fail if a CometBFT upgrade changes the message, alerting us that
// the wrapper logger would silently stop detecting consensus failures.
func TestConsensusFailureMsg_MatchesCometBFT(t *testing.T) {
	// Locate the CometBFT module directory via `go list`.
	cmd := exec.Command("go", "list", "-m", "-json", "github.com/cometbft/cometbft")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m -json github.com/cometbft/cometbft: %v", err)
	}

	var mod struct{ Dir string }
	if err = json.Unmarshal(out, &mod); err != nil {
		t.Fatalf("unmarshal go list output: %v", err)
	}
	if mod.Dir == "" {
		t.Fatal("CometBFT module directory is empty — is the module downloaded?")
	}

	src, err := os.ReadFile(filepath.Join(mod.Dir, "consensus", "state.go"))
	if err != nil {
		t.Fatalf("read CometBFT consensus/state.go: %v", err)
	}

	// Look for our exact string as a Go string literal in the source.
	needle := `"` + consensusFailureMsg + `"`
	if !strings.Contains(string(src), needle) {
		t.Fatalf(
			"CometBFT consensus/state.go no longer contains %s — "+
				"the consensusFailureMsg constant needs updating",
			needle,
		)
	}
}

// TestConsensusFailureLogger_BundlesBeforeExit checks the divergence bundle is
// written before the process exits, and that the exit still happens.
func TestConsensusFailureLogger_BundlesBeforeExit(t *testing.T) {
	exitCode := -1
	exitProcess = func(code int) { exitCode = code }
	defer func() { exitProcess = os.Exit }()

	home := t.TempDir()
	d, err := divergence.New(divergence.Options{
		Config:     divergence.DefaultConfig(),
		HomeDir:    home,
		LastHeight: func() int64 { return 41 },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	divergence.SetActive(d)
	defer divergence.SetActive(nil)

	inner := newRecordingLogger()
	logger := NewConsensusFailureLogger(inner)
	panicErr := errors.New("+2/3 committed an invalid block: wrong Block.Header.AppHash.  Expected AA, got BB")
	logger.Error(consensusFailureMsg, "err", panicErr, "stack", "...")

	if exitCode != 1 {
		t.Fatalf("expected exit(1), got %d", exitCode)
	}
	matches, _ := filepath.Glob(filepath.Join(divergence.BundleDir(home), "bundle-41-*", divergence.SummaryFileName))
	if len(matches) != 1 {
		t.Fatalf("expected one bundle summary before exit, got %v", matches)
	}
	s, err := divergence.ReadSummary(filepath.Dir(matches[0]))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mismatch == nil || s.Mismatch.Local != "AA" || s.Mismatch.Network != "BB" || s.FailingHeight != 42 {
		t.Fatalf("unexpected summary: %+v", s)
	}
}

// TestConsensusFailureLogger_BlocksyncHashMismatch checks that a blocksync
// hash mismatch writes one bundle in the background and does not exit.
func TestConsensusFailureLogger_BlocksyncHashMismatch(t *testing.T) {
	exitProcess = func(code int) { t.Fatalf("unexpected exit(%d)", code) }
	defer func() { exitProcess = os.Exit }()

	home := t.TempDir()
	d, err := divergence.New(divergence.Options{
		Config:     divergence.DefaultConfig(),
		HomeDir:    home,
		LastHeight: func() int64 { return 7 },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	divergence.SetActive(d)
	defer divergence.SetActive(nil)

	inner := newRecordingLogger()
	logger := NewConsensusFailureLogger(inner).With("module", "blocksync")
	logger.Error(blocksyncValidationMsg, "err", errors.New("wrong Block.Header.LastResultsHash.  Expected AA, got BB"))
	logger.Error(blocksyncValidationMsg, "err", errors.New("unrelated"))

	deadline := time.Now().Add(10 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(divergence.BundleDir(home), "bundle-7-*", divergence.SummaryFileName))
		if len(matches) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no blocksync bundle written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(inner.errorCalls) != 2 {
		t.Fatalf("expected both errors logged, got %v", inner.errorCalls)
	}
}

// TestBlocksyncValidationMsg_MatchesCometBFT pins blocksyncValidationMsg to
// the literal in CometBFT's blocksync reactor.
func TestBlocksyncValidationMsg_MatchesCometBFT(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-json", "github.com/cometbft/cometbft").Output()
	if err != nil {
		t.Fatal(err)
	}
	var mod struct{ Dir string }
	if err = json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(mod.Dir, "blocksync", "reactor.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"`+blocksyncValidationMsg+`", "err", err`) {
		t.Fatalf("CometBFT blocksync/reactor.go no longer logs %q with an err keyval", blocksyncValidationMsg)
	}
}

func TestDebugCommandHasDivergenceDiff(t *testing.T) {
	for _, c := range debugCommand().Commands() {
		if c.Name() == "divergence-diff" {
			return
		}
	}
	t.Fatal("debug divergence-diff not registered")
}
