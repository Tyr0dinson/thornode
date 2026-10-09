package main

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
)

type MainSeamsCovSuite struct{}

var _ = Suite(&MainSeamsCovSuite{})

// fatalCov is panicked by the logFatal seam so main stops where the real
// binary would exit.
type fatalCov string

const privKeyHexCov = "6c1f4d1a3e3b6d2c1f0e9d8c7b6a59483726150413f2e1d0c9b8a7968574635f"

func privKeyInputCov() string {
	return base64.StdEncoding.EncodeToString([]byte(privKeyHexCov))
}

// mainSeamsCov captures the arguments main passes through the seams.
type mainSeamsCov struct {
	p2pCfg     p2p.P2PConfig
	p2pFolder  string
	p2pKey     tcrypto.PrivKey
	p2pErr     error
	tssKey     tcrypto.PrivKey
	tssConf    common.TssConfig
	tssErr     error
	tssServer  *MockTssServer
	onShutdown func()
}

// runMainSeamsCov runs main() with stubbed seams, returning stdout and the
// logFatal message (empty if main returned normally).
func runMainSeamsCov(c *C, m *mainSeamsCov, args []string, stdin string) (out, fatal string) {
	oldArgs, oldFlags, oldStdin, oldStdout := os.Args, flag.CommandLine, os.Stdin, os.Stdout
	oldLogger, oldLevel := zlog.Logger, zerolog.GlobalLevel()
	oldFatal, oldStart, oldNewTss, oldWait := logFatal, startP2P, newTss, waitForShutdown
	defer func() {
		os.Args, flag.CommandLine, os.Stdin, os.Stdout = oldArgs, oldFlags, oldStdin, oldStdout
		zlog.Logger = oldLogger
		zerolog.SetGlobalLevel(oldLevel)
		logFatal, startP2P, newTss, waitForShutdown = oldFatal, oldStart, oldNewTss, oldWait
	}()

	logFatal = func(v ...interface{}) { panic(fatalCov(fmt.Sprint(v...))) }
	startP2P = func(cfg p2p.P2PConfig, priKey tcrypto.PrivKey, baseFolder string) (*p2p.Communication, *storage.FileStateMgr, error) {
		m.p2pCfg, m.p2pKey, m.p2pFolder = cfg, priKey, baseFolder
		return nil, nil, m.p2pErr
	}
	newTss = func(comm *p2p.Communication, sm storage.LocalStateManager, priKey tcrypto.PrivKey, conf common.TssConfig) (tss.Server, error) {
		m.tssKey, m.tssConf = priKey, conf
		if m.tssErr != nil {
			return nil, m.tssErr
		}
		return m.tssServer, nil
	}
	waitForShutdown = func() {
		if m.onShutdown != nil {
			m.onShutdown()
		}
	}

	os.Args = append([]string{"tss"}, args...)
	flag.CommandLine = flag.NewFlagSet("tss", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)

	inR, inW, err := os.Pipe()
	c.Assert(err, IsNil)
	_, err = inW.WriteString(stdin)
	c.Assert(err, IsNil)
	c.Assert(inW.Close(), IsNil)
	os.Stdin = inR
	defer inR.Close()

	outR, outW, err := os.Pipe()
	c.Assert(err, IsNil)
	os.Stdout = outW
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(outR)
		done <- b
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				f, ok := r.(fatalCov)
				c.Assert(ok, Equals, true, Commentf("unexpected panic: %v", r))
				fatal = string(f)
			}
		}()
		main()
	}()
	c.Assert(outW.Close(), IsNil)
	out = string(<-done)
	c.Assert(outR.Close(), IsNil)
	return out, fatal
}

func freeLoopbackAddrCov(c *C) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, IsNil)
	addr := l.Addr().String()
	c.Assert(l.Close(), IsNil)
	return addr
}

func (MainSeamsCovSuite) TestDefaultSeams(c *C) {
	c.Assert(fmt.Sprintf("%p", logFatal), Equals, fmt.Sprintf("%p", log.Fatal))
	c.Assert(fmt.Sprintf("%p", startP2P), Equals, fmt.Sprintf("%p", p2p.StartP2P))
	c.Assert(newTss, NotNil)
	c.Assert(waitForShutdown, NotNil)
}

func (MainSeamsCovSuite) TestMainBadPrivateKey(c *C) {
	m := &mainSeamsCov{}
	_, fatal := runMainSeamsCov(c, m, []string{"-loglevel", "error"}, "not!base64\n")
	c.Assert(fatal, Matches, "fail to decode private key.*")
	c.Assert(m.p2pCfg, IsNil, Commentf("p2p must not start"))
}

func (MainSeamsCovSuite) TestMainStartP2PError(c *C) {
	m := &mainSeamsCov{p2pErr: errors.New("p2p boom")}
	home := c.MkDir()
	_, fatal := runMainSeamsCov(c, m, []string{
		"-loglevel", "error", "-home", home, "-p2p-port", "7777", "-rendezvous", "rv", "-external-ip", "1.2.3.4",
	}, privKeyInputCov()+"\n")
	c.Assert(fatal, Equals, "p2p boom")
	c.Assert(m.p2pFolder, Equals, home)
	c.Assert(m.p2pCfg.GetP2PPort(), Equals, 7777)
	c.Assert(m.p2pCfg.GetRendezvous(), Equals, "rv")
	c.Assert(m.p2pCfg.GetExternalIP(), Equals, "1.2.3.4")
	raw, err := hex.DecodeString(privKeyHexCov)
	c.Assert(err, IsNil)
	c.Assert(m.p2pKey.Bytes(), DeepEquals, raw)
	c.Assert(m.tssKey, IsNil, Commentf("tss must not be created"))
}

func (MainSeamsCovSuite) TestMainNewTssError(c *C) {
	m := &mainSeamsCov{tssErr: errors.New("tss boom")}
	_, fatal := runMainSeamsCov(c, m, []string{"-loglevel", "error", "-gentimeout", "7s", "-enablemonitor=false"}, privKeyInputCov()+"\n")
	c.Assert(fatal, Equals, "tss boom")
	c.Assert(m.tssKey.Bytes(), DeepEquals, m.p2pKey.Bytes())
	c.Assert(m.tssConf.KeyGenTimeout, Equals, 7*time.Second)
	c.Assert(m.tssConf.EnableMonitor, Equals, false)
}

func (MainSeamsCovSuite) TestMainServesUntilShutdown(c *C) {
	addr := freeLoopbackAddrCov(c)
	secretFile := filepath.Join(c.MkDir(), "secret")
	c.Assert(os.WriteFile(secretFile, []byte("s3cret\n"), 0o600), IsNil)

	var pingCodes []int
	m := &mainSeamsCov{tssServer: &MockTssServer{}}
	m.onShutdown = func() {
		// the HTTP server is started in a goroutine; wait until it serves,
		// then check auth is enforced before "receiving" the signal.
		get := func(auth string) (int, error) {
			req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/ping", nil)
			c.Assert(err, IsNil)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return 0, err
			}
			_ = resp.Body.Close()
			return resp.StatusCode, nil
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			code, err := get("Bearer s3cret")
			if err == nil {
				pingCodes = append(pingCodes, code)
				break
			}
			c.Assert(time.Now().Before(deadline), Equals, true, Commentf("server never came up: %v", err))
			time.Sleep(5 * time.Millisecond)
		}
		code, err := get("")
		c.Assert(err, IsNil)
		pingCodes = append(pingCodes, code)
	}
	out, fatal := runMainSeamsCov(c, m, []string{
		"-loglevel", "error", "-tss-port", addr, "-tss-auth-secret-file", secretFile,
	}, privKeyInputCov()+"\n")
	c.Assert(fatal, Equals, "")
	c.Assert(pingCodes, DeepEquals, []int{http.StatusOK, http.StatusUnauthorized})
	c.Assert(out, Equals, "stop \n<nil>\n")

	// the listener was shut down by s.Stop()
	_, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	c.Assert(err, NotNil)
}

func (MainSeamsCovSuite) TestDefaultWaitForShutdownReturnsOnSignal(c *C) {
	// Keep our own registration so a SIGINT that lands before
	// waitForShutdown registers cannot kill the test process.
	guard := make(chan os.Signal, 16)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	done := make(chan struct{})
	go func() {
		oldWaitForShutdownCov()
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		c.Assert(syscall.Kill(os.Getpid(), syscall.SIGINT), IsNil)
		select {
		case <-done:
			return
		case <-deadline:
			c.Fatal("waitForShutdown did not return on SIGINT")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// captured before any test swaps the seams
var (
	oldLogFatalCov        = logFatal
	oldWaitForShutdownCov = waitForShutdown
)
