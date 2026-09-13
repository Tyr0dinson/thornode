package endpoints_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		c.Assert(json.NewDecoder(r.Body).Decode(&req), IsNil)
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
	srv := jsonrpcServer(c, map[string]string{
		"status": `{"node_info":{"network":"cosmoshub-4"},"sync_info":{"latest_block_height":"22000001"}}`,
		"block":  `{"block_id":{"hash":"DEADBEEF"}}`,
	}, nil)
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
}

func (s *ProbeSuite) TestSolanaProbeHasNoHash(c *C) {
	srv := jsonrpcServer(c, map[string]string{"getSlot": `123`, "getGenesisHash": `"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"`}, nil)
	defer srv.Close()
	p := endpoints.NewSolanaProbe(config.ChainEndpointConfig{Name: "a", RPCHost: srv.URL}, time.Second)
	h, err := p.Height(ctxBg())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(123))
	_, isHash := p.(endpoints.HashProbe)
	c.Assert(isHash, Equals, false)
}

func (s *ProbeSuite) TestXRPProbe(c *C) {
	srv := jsonrpcServer(c, map[string]string{
		"ledger_current": `{"ledger_current_index":99}`,
		"server_info":    `{"info":{"network_id":0}}`,
		"ledger":         `{"ledger":{"ledger_hash":"ABC"}}`,
	}, nil)
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

func (s *ProbeSuite) TestProbeErrorsAreRedacted(c *C) {
	p := endpoints.NewEVMProbe(config.ChainEndpointConfig{Name: "leaky", RPCHost: "http://127.0.0.1:1/v1/SECRETKEY?apikey=SECRET2", AuthorizationBearer: "SECRET3"}, 200*time.Millisecond)
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
