package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
)

type MainCovSuite struct{}

var _ = Suite(&MainCovSuite{})

// runMainCov runs main() with the given CLI args and stdin content, returning
// everything written to stdout. Global flag/os state is restored afterwards.
func runMainCov(c *C, args []string, stdin string) string {
	oldArgs, oldFlags, oldStdin, oldStdout := os.Args, flag.CommandLine, os.Stdin, os.Stdout
	oldLogger, oldLevel := zlog.Logger, zerolog.GlobalLevel()
	defer func() {
		os.Args, flag.CommandLine, os.Stdin, os.Stdout = oldArgs, oldFlags, oldStdin, oldStdout
		zlog.Logger = oldLogger
		zerolog.SetGlobalLevel(oldLevel)
	}()
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
	main()
	c.Assert(outW.Close(), IsNil)
	out := <-done
	c.Assert(outR.Close(), IsNil)
	return string(out)
}

func (MainCovSuite) TestMainHelp(c *C) {
	runMainCov(c, []string{"-h"}, "")
	c.Assert(help, Equals, true)
}

func (MainCovSuite) TestMainAuthSecretError(c *C) {
	out := runMainCov(c, []string{"-tss-auth-secret-file", "/nonexistent", "-tss-auth-prompt"}, "")
	c.Assert(help, Equals, false)
	c.Assert(out, Matches, "(?s).*error in get the tss auth secret: .*mutually exclusive.*")
}

func (MainCovSuite) TestMainSecretKeyError(c *C) {
	out := runMainCov(c, []string{"-loglevel", "error"}, "")
	c.Assert(out, Matches, "(?s).*error in get the secret key: .*")
	c.Assert(out, Not(Matches), "(?s).*tss auth secret.*")
}

func (MainCovSuite) TestParseFlagsValues(c *C) {
	oldArgs, oldFlags := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFlags }()
	flag.CommandLine = flag.NewFlagSet("tss", flag.ContinueOnError)
	os.Args = []string{
		"tss", "-tss-port", "127.0.0.1:9999", "-home", "/h", "-gentimeout", "3s",
		"-signtimeout", "4s", "-preparamtimeout", "5s", "-enablemonitor=false",
		"-rendezvous", "R", "-p2p-port", "1234", "-external-ip", "1.2.3.4",
		"-pretty-log", "-loglevel", "debug",
	}
	tssConf, p2pConf := parseFlags()
	c.Assert(tssAddr, Equals, "127.0.0.1:9999")
	c.Assert(baseFolder, Equals, "/h")
	c.Assert(logLevel, Equals, "debug")
	c.Assert(pretty, Equals, true)
	c.Assert(tssConf.KeyGenTimeout, Equals, 3*time.Second)
	c.Assert(tssConf.KeySignTimeout, Equals, 4*time.Second)
	c.Assert(tssConf.PreParamTimeout, Equals, 5*time.Second)
	c.Assert(tssConf.EnableMonitor, Equals, false)
	c.Assert(p2pConf.RendezvousString, Equals, "R")
	c.Assert(p2pConf.Port, Equals, 1234)
	c.Assert(p2pConf.ExternalIP, Equals, "1.2.3.4")
}

func (MainCovSuite) TestResolveTssAuthSecretMissingFile(c *C) {
	secret, err := resolveTssAuthSecret(filepath.Join(c.MkDir(), "missing"), false, bufio.NewReader(strings.NewReader("")))
	c.Assert(secret, Equals, "")
	c.Assert(err, ErrorMatches, "failed to read tss auth secret file.*")
}

func (MainCovSuite) TestMockTssServer(c *C) {
	m := &MockTssServer{}
	m.Stop()
	_ = m.GetLocalPeerID()
	c.Assert(m.GetKnownPeers(), HasLen, 0)

	all, err := m.KeygenAllAlgo(keygen.Request{})
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 2)
	c.Assert(all[0].Status, Equals, common.Success)

	fs, err := m.FrostKeySign(tss.FrostKeySignRequest{}, nil)
	c.Assert(err, IsNil)
	c.Assert(fs.TxHash, Equals, "mock-tx-hash")
	fk, err := m.FrostKeygen(tss.FrostKeygenRequest{}, nil)
	c.Assert(err, IsNil)
	c.Assert(fk.Address, Equals, "mock-address")

	m.failToKeyGen = true
	m.failToKeySign = true
	_, err = m.KeygenAllAlgo(keygen.Request{})
	c.Assert(err, NotNil)
	_, err = m.FrostKeySign(tss.FrostKeySignRequest{}, nil)
	c.Assert(err, NotNil)
	_, err = m.FrostKeygen(tss.FrostKeygenRequest{}, nil)
	c.Assert(err, NotNil)
	_, err = m.Keygen(keygen.Request{})
	c.Assert(err, NotNil)
	_, err = m.KeySign(keysign.Request{})
	c.Assert(err, NotNil)
}

func (MainCovSuite) TestKeygenAllHandler(c *C) {
	tssServer := &MockTssServer{}
	s := NewTssHttpServer("127.0.0.1:8080", tssServer)

	res := httptest.NewRecorder()
	s.keygenAllHandler(res, httptest.NewRequest(http.MethodGet, "/keygenall", nil))
	c.Assert(res.Code, Equals, http.StatusMethodNotAllowed)

	res = httptest.NewRecorder()
	s.keygenAllHandler(res, httptest.NewRequest(http.MethodPost, "/keygenall", bytes.NewBufferString("{bad")))
	c.Assert(res.Code, Equals, http.StatusBadRequest)

	res = httptest.NewRecorder()
	s.keygenAllHandler(res, httptest.NewRequest(http.MethodPost, "/keygenall", bytes.NewBufferString(normalKeygenRequest)))
	c.Assert(res.Code, Equals, http.StatusOK)
	var resps []keygen.Response
	c.Assert(json.Unmarshal(res.Body.Bytes(), &resps), IsNil)
	c.Assert(resps, HasLen, 2)

	// keygen failure is logged but still returns 200 with the response body
	tssServer.failToKeyGen = true
	res = httptest.NewRecorder()
	s.keygenAllHandler(res, httptest.NewRequest(http.MethodPost, "/keygenall", bytes.NewBufferString(normalKeygenRequest)))
	c.Assert(res.Code, Equals, http.StatusOK)

	// unauthorised
	sa := NewTssHttpServerWithAuth("127.0.0.1:8080", tssServer, "sec")
	res = httptest.NewRecorder()
	sa.keygenAllHandler(res, httptest.NewRequest(http.MethodPost, "/keygenall", bytes.NewBufferString(normalKeygenRequest)))
	c.Assert(res.Code, Equals, http.StatusUnauthorized)
}

func (MainCovSuite) TestAuthOnAllHandlers(c *C) {
	s := NewTssHttpServerWithAuth("127.0.0.1:8080", &MockTssServer{}, " sec ")
	c.Assert(s.authSecret, Equals, "sec")
	h := s.tssNewHandler()
	for _, tc := range []struct {
		method, path, auth string
		code               int
	}{
		{http.MethodGet, "/ping", "", http.StatusUnauthorized},
		{http.MethodGet, "/ping", "Basic sec", http.StatusUnauthorized},
		{http.MethodGet, "/ping", "Bearer sec", http.StatusOK},
		{http.MethodGet, "/p2pid", "", http.StatusUnauthorized},
		{http.MethodGet, "/p2pid", "Bearer sec", http.StatusOK},
		{http.MethodPost, "/keysign", "Bearer nope", http.StatusUnauthorized},
		{http.MethodGet, "/metrics", "", http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		c.Check(res.Code, Equals, tc.code, Commentf("%s %s %q", tc.method, tc.path, tc.auth))
	}
}

func (MainCovSuite) TestStartErrors(c *C) {
	s := &TssHttpServer{}
	c.Assert(s.Start(), ErrorMatches, "invalid http server instance")

	c.Assert(validateLoopbackListenAddr("no-port"), ErrorMatches, "invalid bind address.*")
	c.Assert(validateLoopbackListenAddr("LocalHost:1"), IsNil)

	// port already in use -> listen error is surfaced
	l, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, IsNil)
	defer l.Close()
	s = NewTssHttpServerWithAuth(l.Addr().String(), &MockTssServer{}, "sec")
	c.Assert(s.Start(), ErrorMatches, "fail to start http server.*")
}
