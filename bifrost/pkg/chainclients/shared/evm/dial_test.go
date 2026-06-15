package evm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/config"
)

type DialTestSuite struct{}

var _ = Suite(&DialTestSuite{})

// newChainIDServer returns an httptest server that answers eth_chainId with the
// supplied hex chain id, echoing the JSON-RPC request id.
func newChainIDServer(chainIDHex string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"%s"}`, string(req.ID), chainIDHex)
	}))
}

// deadHost spins up a server, captures its URL, then closes it so the address
// refuses connections — simulating an unreachable endpoint.
func deadHost() string {
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := s.URL
	s.Close()
	return url
}

// A dead primary must be skipped and the live fallback returned.
func (s *DialTestSuite) TestDialWithFallbacksSkipsDeadPrimary(c *C) {
	good := newChainIDServer("0x539") // 1337
	defer good.Close()

	endpoints := []config.ChainEndpointConfig{
		{Name: "primary", RPCHost: deadHost(), Priority: 1},
		{Name: "backup", RPCHost: good.URL, Priority: 2},
	}

	cl, err := DialWithFallbacks(endpoints, dialProbeFloor)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)

	id, err := cl.ChainID(context.Background())
	c.Assert(err, IsNil)
	c.Assert(id.Int64(), Equals, int64(1337))
}

// A live primary must be used without touching the fallback.
func (s *DialTestSuite) TestDialWithFallbacksUsesPrimaryWhenLive(c *C) {
	primary := newChainIDServer("0x1") // 1
	defer primary.Close()

	endpoints := []config.ChainEndpointConfig{
		{Name: "primary", RPCHost: primary.URL, Priority: 1},
		{Name: "backup", RPCHost: deadHost(), Priority: 2},
	}

	cl, err := DialWithFallbacks(endpoints, dialProbeFloor)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)

	id, err := cl.ChainID(context.Background())
	c.Assert(err, IsNil)
	c.Assert(id.Int64(), Equals, int64(1))
}

// When every endpoint is unreachable an error is returned.
func (s *DialTestSuite) TestDialWithFallbacksAllDead(c *C) {
	endpoints := []config.ChainEndpointConfig{
		{Name: "primary", RPCHost: deadHost(), Priority: 1},
		{Name: "backup", RPCHost: deadHost(), Priority: 2},
	}

	cl, err := DialWithFallbacks(endpoints, dialProbeFloor)
	c.Assert(err, NotNil)
	c.Assert(cl, IsNil)
}

// Endpoints with empty RPCHost are skipped; the first non-empty live one wins.
func (s *DialTestSuite) TestDialWithFallbacksSkipsEmptyHost(c *C) {
	good := newChainIDServer("0x2a") // 42
	defer good.Close()

	endpoints := []config.ChainEndpointConfig{
		{Name: "empty", RPCHost: "", Priority: 1},
		{Name: "live", RPCHost: good.URL, Priority: 2},
	}

	cl, err := DialWithFallbacks(endpoints, dialProbeFloor)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)

	id, err := cl.ChainID(context.Background())
	c.Assert(err, IsNil)
	c.Assert(id.Int64(), Equals, int64(42))
}

// A non-positive probe timeout falls back to the floor and still connects.
func (s *DialTestSuite) TestDialWithFallbacksTimeoutFloor(c *C) {
	good := newChainIDServer("0x7") // 7
	defer good.Close()

	endpoints := []config.ChainEndpointConfig{
		{Name: "live", RPCHost: good.URL, Priority: 1},
	}

	cl, err := DialWithFallbacks(endpoints, 0)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)

	id, err := cl.ChainID(context.Background())
	c.Assert(err, IsNil)
	c.Assert(id.Int64(), Equals, int64(7))
}
