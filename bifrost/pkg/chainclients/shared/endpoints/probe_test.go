package endpoints_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/config"
)

type ProbeSuite struct{}

var _ = Suite(&ProbeSuite{})

func ctxBg() context.Context { return context.Background() }

// jsonrpcServer answers JSON-RPC 2.0 POSTs with canned results keyed by method.
// results values are raw JSON. Methods not present return a JSON-RPC error.
func jsonrpcServer(c *C, results map[string]string, seen *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		// Check, not Assert: an Assert failing on the server goroutine would Goexit
		// that goroutine and hang the test instead of failing it
		c.Check(json.NewDecoder(r.Body).Decode(&req), IsNil)
		if seen != nil {
			*seen = append(*seen, req.Method+" "+string(req.Params)+" auth="+r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		res, ok := results[req.Method]
		if !ok {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + res + `}`))
	}))
}

func hashProbe(c *C, p endpoints.Probe) endpoints.HashProbe {
	hp, ok := p.(endpoints.HashProbe)
	c.Assert(ok, Equals, true)
	return hp
}

func (s *ProbeSuite) TestEVMProbe(c *C) {
	var seen []string
	srv := jsonrpcServer(c, map[string]string{
		"eth_blockNumber":      `"0x10"`,
		"eth_chainId":          `"0x1"`,
		"eth_getBlockByNumber": `{"hash":"0xabc"}`,
	}, &seen)
	defer srv.Close()
	p := endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL, AuthorizationBearer: "tok"}, time.Second)
	h, err := p.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(16))
	id, err := p.Identity(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(id, Equals, "0x1")
	hash, err := hashProbe(c, p).HashAt(ctxBg(), 16)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "0xabc")
	c.Assert(seen[0], Equals, `eth_blockNumber [] auth=Bearer tok`)
	c.Assert(seen[2], Equals, `eth_getBlockByNumber ["0x10",false] auth=Bearer tok`)
}

func (s *ProbeSuite) TestUTXOProbeUsesGenesisHashAsIdentityAndSchemelessHost(c *C) {
	var seen []string
	srv := jsonrpcServer(c, map[string]string{
		"getblockcount": `800000`,
		"getblockhash":  `"000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"`,
	}, &seen)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	p := endpoints.NewUTXOProbe(config.ChainEndpointConfig{Name: "a", RPCHost: u.Host, UserName: "u", Password: "p"}, time.Second) // scheme-less
	h, err := p.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(800000))
	id, err := p.Identity(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(id, Equals, "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f")
	c.Assert(seen[1], Equals, `getblockhash [0] auth=Basic dTpw`)
}

func (s *ProbeSuite) TestURLUserinfoBecomesBasicAuth(c *C) {
	var seen []string
	srv := jsonrpcServer(c, map[string]string{"getblockcount": `1`}, &seen)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	p := endpoints.NewUTXOProbe(config.ChainEndpointConfig{Name: "a", RPCHost: "http://user:pw@" + u.Host}, time.Second)
	_, err := p.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(seen[0], Equals, `getblockcount [] auth=Basic dXNlcjpwdw==`)
}

func (s *ProbeSuite) TestCometProbe(c *C) {
	var seen []string
	srv := jsonrpcServer(c, map[string]string{
		"status": `{"node_info":{"network":"cosmoshub-4"},"sync_info":{"latest_block_height":"22000001"}}`,
		// commit, not block: the hash without the block's transactions
		"commit": `{"signed_header":{"commit":{"block_id":{"hash":"DEADBEEF"}}},"canonical":true}`,
	}, &seen)
	defer srv.Close()
	p := endpoints.NewCometProbe(config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL}, time.Second)
	h, err := p.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(22000001))
	id, err := p.Identity(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(id, Equals, "cosmoshub-4")
	hash, err := hashProbe(c, p).HashAt(ctxBg(), 5)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "DEADBEEF")
	c.Assert(seen, DeepEquals, []string{`status [] auth=`, `status [] auth=`, `commit {"height":"5"} auth=`})
}

func (s *ProbeSuite) TestSolanaProbeHasNoHash(c *C) {
	var seen []string
	srv := jsonrpcServer(c, map[string]string{"getSlot": `123`, "getGenesisHash": `"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"`}, &seen)
	defer srv.Close()
	p := endpoints.NewSolanaProbe(config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL}, time.Second)
	h, err := p.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(123))
	id, err := p.Identity(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(id, Equals, "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d")
	_, isHash := p.(endpoints.HashProbe)
	c.Assert(isHash, Equals, false)
	c.Assert(seen, DeepEquals, []string{`getSlot [{"commitment":"finalized"}] auth=`, `getGenesisHash [] auth=`})
}

func (s *ProbeSuite) TestXRPProbe(c *C) {
	var seen []string
	srv := jsonrpcServer(c, map[string]string{
		"server_info": `{"info":{"network_id":0}}`,
		"ledger":      `{"ledger_index":99,"ledger":{"ledger_hash":"ABC"}}`,
	}, &seen)
	defer srv.Close()
	p := endpoints.NewXRPProbe(config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL}, time.Second)
	h, err := p.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(99))
	id, err := p.Identity(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(id, Equals, "0")
	hash, err := hashProbe(c, p).HashAt(ctxBg(), 98)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "ABC")
	// rippled and clio answer HTTP 200 to an empty params array (checked against
	// public mainnet servers); only a non-empty array that is not one object is refused
	c.Assert(seen, DeepEquals, []string{`ledger [{"ledger_index":"validated"}] auth=`, `server_info [] auth=`, `ledger [{"ledger_index":98}] auth=`})
}

// The XRP probe reports the validated ledger, the height the scanner reads: a Clio
// endpoint forwards ledger_current to its rippled, so the open ledger keeps advancing
// while the validated data Clio serves itself has stalled.
func (s *ProbeSuite) TestXRPProbeReportsTheValidatedLedger(c *C) {
	var seen []string
	srv := jsonrpcServer(c, map[string]string{
		"ledger_current": `{"ledger_current_index":200}`,
		"ledger":         `{"ledger_index":100,"validated":true,"ledger":{"ledger_hash":"ABC"}}`,
	}, &seen)
	defer srv.Close()
	p := endpoints.NewXRPProbe(config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL}, time.Second)
	h, err := p.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(100))
	c.Assert(seen, DeepEquals, []string{`ledger [{"ledger_index":"validated"}] auth=`})
}

func (s *ProbeSuite) TestCompoundProbeFailsWhenExtraFails(c *C) {
	srv := jsonrpcServer(c, map[string]string{"eth_blockNumber": `"0x5"`, "eth_chainId": `"0x1"`}, nil)
	defer srv.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wallet/getnowblock" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer api.Close()
	e := config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL, APIHost: api.URL}
	good := endpoints.CompoundProbe{Probe: endpoints.NewEVMProbe(e, time.Second), Extra: endpoints.HTTPPostCheck(e, e.APIHost, "/wallet/getnowblock", time.Second)}
	h, err := good.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(5))
	_, err = hashProbe(c, good).HashAt(ctxBg(), 1) // delegates; mock returns method not found
	c.Assert(err, NotNil)
	bad := endpoints.CompoundProbe{Probe: endpoints.NewEVMProbe(e, time.Second), Extra: endpoints.HTTPPostCheck(e, e.APIHost, "/wallet/other", time.Second)}
	_, err = bad.Height(ctxBg())
	c.Assert(err, ErrorMatches, ".*endpoint a.*503.*")
}

// With ExtraHeight the endpoint is as far as the lower of its two hosts: a second host
// that stopped advancing makes the endpoint lag, and one that fails fails the probe.
func (s *ProbeSuite) TestCompoundProbeReportsTheLowerExtraHeight(c *C) {
	srv := jsonrpcServer(c, map[string]string{"eth_blockNumber": `"0x5"`, "eth_chainId": `"0x1"`}, nil)
	defer srv.Close()
	e := config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL}
	probe := func(extra int64, err error) endpoints.CompoundProbe {
		return endpoints.CompoundProbe{
			Probe:       endpoints.NewEVMProbe(e, time.Second),
			ExtraHeight: func(context.Context) (int64, error) { return extra, err },
		}
	}
	h, err := probe(3, nil).Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(3))
	h, err = probe(9, nil).Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(5))
	_, err = probe(0, errors.New("api down")).Height(ctxBg())
	c.Assert(err, ErrorMatches, ".*endpoint a.*api down.*")
}

func (s *ProbeSuite) TestProbeErrorsAreRedacted(c *C) {
	p := endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "leaky", RPCHost: "http://127.0.0.1:1/v1/SECRETKEY?apikey=SECRET2", AuthorizationBearer: "SECRET3"}, 200*time.Millisecond)
	_, err := p.Height(ctxBg())
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "(?s).*leaky.*")
	c.Assert(err.Error(), Not(Matches), "(?s).*SECRET.*")
}

// With credentials in the URL the request goes to the URL without them (they move to
// a header), and the error prints its path escaped: neither form may leak a key in
// the path.
func (s *ProbeSuite) TestProbeErrorsAreRedactedWithUserinfoAndEscapedPath(c *C) {
	p := endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "leaky", RPCHost: "http://user:SECRETPW@127.0.0.1:1/rpc/SECRETKEY%2Fx"}, 200*time.Millisecond)
	_, err := p.Height(ctxBg())
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "(?s).*leaky.*")
	c.Assert(err.Error(), Not(Matches), "(?s).*SECRET.*")
}

func (s *ProbeSuite) TestInvalidProbeURLFailsCleanly(c *C) {
	p := endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "bad", RPCHost: "http://[::1"}, time.Second)
	_, err := p.Height(ctxBg())
	c.Assert(err, ErrorMatches, ".*bad.*invalid url.*")
}

func (s *ProbeSuite) TestRedact(c *C) {
	u, _ := url.Parse("https://user:pass@x.example.com/v1/KEY?token=T")
	err := errors.New(`Post "https://user:pass@x.example.com/v1/KEY?token=T": dial tcp: connection refused`)
	red := endpoints.Redact(err, endpoints.SecretsOf(u, "BEARER"))
	c.Assert(red.Error(), Equals, `Post "***": dial tcp: connection refused`)
	c.Assert(errors.Unwrap(red), Equals, err)
	// host+path and path alone are scrubbed too (net/http prints user:***@host/path)
	red = endpoints.Redact(errors.New(`Post "https://user:xxxxx@x.example.com/v1/KEY": eof`), endpoints.SecretsOf(u))
	c.Assert(red.Error(), Not(Matches), ".*KEY.*")
	c.Assert(endpoints.Redact(nil, nil), IsNil)
}

func (s *ProbeSuite) TestProbeReportsRateLimits(c *C) {
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer limited.Close()
	p := endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "a", RPCHost: limited.URL}, time.Second)
	_, err := p.Height(ctxBg())
	c.Assert(errors.Is(err, endpoints.ErrRateLimited), Equals, true)

	byCode := jsonrpcServer(c, nil, nil) // method not found for everything
	defer byCode.Close()
	p = endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "a", RPCHost: byCode.URL}, time.Second)
	_, err = p.Height(ctxBg())
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, endpoints.ErrRateLimited), Equals, false)

	codeLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"limit"}}`))
	}))
	defer codeLimited.Close()
	p = endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "a", RPCHost: codeLimited.URL}, time.Second)
	_, err = p.Height(ctxBg())
	c.Assert(errors.Is(err, endpoints.ErrRateLimited), Equals, true)

	check := endpoints.HTTPPostCheck(config.ChainEndpointConfig{Name: "a"}, limited.URL, "/wallet/getnowblock", time.Second)
	c.Assert(errors.Is(check(ctxBg()), endpoints.ErrRateLimited), Equals, true)
}

// ExtraIdentity lets a second host of the endpoint take part in identity
// verification: it sees the base identity and decides what to report.
func (s *ProbeSuite) TestCompoundProbeExtraIdentity(c *C) {
	srv := jsonrpcServer(c, map[string]string{"eth_blockNumber": `"0x10"`, "eth_chainId": `"0x1"`}, nil)
	defer srv.Close()
	base := endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL}, time.Second)
	p := endpoints.CompoundProbe{Probe: base, ExtraIdentity: func(_ context.Context, id string) (string, error) {
		c.Assert(id, Equals, "0x1")
		return "other", nil
	}}
	id, err := p.Identity(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(id, Equals, "other")

	failing := endpoints.CompoundProbe{Probe: base, ExtraIdentity: func(context.Context, string) (string, error) {
		return "", errors.New("halves disagree")
	}}
	_, err = failing.Identity(ctxBg())
	c.Assert(err, ErrorMatches, "halves disagree")

	// a base identity failure is returned as-is, ExtraIdentity is not consulted
	dead := endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "a", RPCHost: "http://127.0.0.1:1"}, time.Second)
	called := false
	_, err = endpoints.CompoundProbe{Probe: dead, ExtraIdentity: func(context.Context, string) (string, error) {
		called = true
		return "x", nil
	}}.Identity(ctxBg())
	c.Assert(err, NotNil)
	c.Assert(called, Equals, false)
}

// Solana answers -32005 when the node is behind ("node is unhealthy"): its probe takes
// a strike, as the router charges it, while on other chains the same code is a
// provider rate limit (a cooldown, never a strike).
func (s *ProbeSuite) TestSolanaNodeUnhealthyIsAStrikeNotARateLimit(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"Node is unhealthy"}}`))
	}))
	defer srv.Close()
	ep := config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL}

	_, err := endpoints.NewSolanaProbe(ep, time.Second).Height(context.Background())
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, endpoints.ErrRateLimited), Equals, false)

	_, err = endpoints.NewEVMProbe(ep, time.Second).Height(context.Background())
	c.Assert(errors.Is(err, endpoints.ErrRateLimited), Equals, true)
}

// Without required fields the check is liveness only: any 200 answers, even one whose
// body is not JSON. With a required field, such a body fails the check.
func (s *ProbeSuite) TestHTTPPostCheckLivenessOnlyAcceptsAnyOK(c *C) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}))
	defer plain.Close()
	e := config.ChainEndpointConfig{Name: "a"}
	c.Assert(endpoints.HTTPPostCheck(e, plain.URL, "/health", time.Second)(ctxBg()), IsNil)
	c.Assert(endpoints.HTTPPostCheck(e, plain.URL, "/health", time.Second, "blockID")(ctxBg()), ErrorMatches, ".*not a JSON object.*")
}

// HTTPPostJSON keeps an escaped reserved character in the endpoint's path (a key
// with %2F) when it appends the API path.
func (s *ProbeSuite) TestHTTPPostJSONKeepsAnEscapedPath(c *C) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.RequestURI)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	post := endpoints.HTTPPostJSON(config.ChainEndpointConfig{Name: "a"}, srv.URL+"/api/KEY%2Fx", time.Second)
	_, err := post(ctxBg(), "/wallet/getnowblock", "{}")
	c.Assert(err, IsNil)
	c.Assert(got.Load(), Equals, "/api/KEY%2Fx/wallet/getnowblock")
}
