package evm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// FailoverSuite is run by the package's existing TestingT call. Do NOT add a func Test.
type FailoverSuite struct{}

var _ = Suite(&FailoverSuite{})

func evmMock(down *atomic.Bool, chainID, height string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		var res string
		switch req.Method {
		case "eth_chainId":
			res = `"` + chainID + `"`
		case "eth_blockNumber":
			res = `"` + height + `"`
		default:
			res = `"0x0"`
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + res + `}`))
	}))
}

func failoverCfg(eps ...config.ChainEndpointConfig) config.BifrostChainConfiguration {
	cfg := config.BifrostChainConfiguration{ChainID: common.AVAXChain, Endpoints: eps}
	cfg.Failover.ErrorThreshold = 1
	cfg.Failover.HealthCheckTimeout = 500 * time.Millisecond
	return cfg
}

func (s *FailoverSuite) TestNewFailoverClientRoutesAndFailsOver(c *C) {
	var pDown, bDown atomic.Bool
	p := evmMock(&pDown, "0x539", "0x10")
	defer p.Close()
	b := evmMock(&bDown, "0x539", "0x11")
	defer b.Close()
	cfg := failoverCfg(
		config.ChainEndpointConfig{Name: "local", RPCHost: p.URL, Priority: 1},
		config.ChainEndpointConfig{Name: "backup", RPCHost: b.URL, Priority: 2},
	)
	fc, err := evm.NewFailoverClient(context.Background(), cfg, time.Second, zerolog.Nop())
	c.Assert(err, IsNil)
	defer fc.Manager.Stop()
	defer fc.Client.Close()
	c.Assert(fc.Manager.Active().Name, Equals, "local")
	c.Assert(fc.ChainID.Int64(), Equals, int64(1337))

	id, err := fc.Client.ChainID(context.Background())
	c.Assert(err, IsNil)
	c.Assert(id.Int64(), Equals, int64(1337))

	pDown.Store(true)
	h, err := fc.Client.BlockNumber(context.Background())
	c.Assert(err, IsNil)
	c.Assert(h, Equals, uint64(0x11)) // served by backup
	c.Assert(fc.Manager.Entry("local").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(fc.Manager.Active().Name, Equals, "backup")
}

func (s *FailoverSuite) TestNewFailoverClientDisablesWrongNetworkFallback(c *C) {
	var down atomic.Bool
	p := evmMock(&down, "0x539", "0x10")
	defer p.Close()
	other := evmMock(&down, "0x1", "0x10")
	defer other.Close()
	cfg := failoverCfg(
		config.ChainEndpointConfig{Name: "local", RPCHost: p.URL, Priority: 1},
		config.ChainEndpointConfig{Name: "wrong", RPCHost: other.URL, Priority: 2},
	)
	fc, err := evm.NewFailoverClient(context.Background(), cfg, time.Second, zerolog.Nop())
	c.Assert(err, IsNil)
	defer fc.Manager.Stop()
	c.Assert(fc.Manager.Entry("wrong").State(), Equals, endpoints.StateDisabled)
	c.Assert(fc.ChainID.Int64(), Equals, int64(1337))
}

func (s *FailoverSuite) TestNewFailoverClientExpectedIdentityIsFatalForPrimary(c *C) {
	var down atomic.Bool
	p := evmMock(&down, "0x539", "0x10")
	defer p.Close()
	cfg := failoverCfg(config.ChainEndpointConfig{Name: "local", RPCHost: p.URL, Priority: 1})
	cfg.ExpectedIdentity = "0xa86a"
	_, err := evm.NewFailoverClient(context.Background(), cfg, time.Second, zerolog.Nop())
	c.Assert(err, ErrorMatches, "(?s).*priority-1 endpoint local reports identity \"0x539\".*")
}

func (s *FailoverSuite) TestNewFailoverClientRefusesWithoutVerifiedChainID(c *C) {
	cfg := failoverCfg(config.ChainEndpointConfig{Name: "dead", RPCHost: "http://127.0.0.1:1", Priority: 1})
	_, err := evm.NewFailoverClient(context.Background(), cfg, time.Second, zerolog.Nop())
	c.Assert(err, ErrorMatches, "(?s).*no endpoint reported a verified chain id.*")
}
