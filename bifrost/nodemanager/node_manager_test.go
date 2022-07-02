package nodemanager

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/bifrost/config"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/x/thorchain"
)

func Test(t *testing.T) { TestingT(t) }

type NodeMgrSuite struct{}

var _ = Suite(&NodeMgrSuite{})

func httpTestHandler(c *C, rw http.ResponseWriter, fixture string) {
	var content []byte
	var err error

	switch fixture {
	case "500":
		rw.WriteHeader(http.StatusInternalServerError)
	default:
		content, err = ioutil.ReadFile(fixture)
		if err != nil {
			c.Fatal(err)
		}
	}

	rw.Header().Set("Content-Type", "application/json")
	if _, err := rw.Write(content); err != nil {
		c.Fatal(err)
	}
}

func (s *NodeMgrSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
}

func (s *NodeMgrSuite) TestFetchNodes(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		c.Logf("================>:%s", req.RequestURI)
		switch req.RequestURI {
		case "/thorchain/nodes":
			fixture := "../../test/fixtures/endpoints/nodeaccounts/template.json"
			httpTestHandler(c, rw, fixture)
		default:
			c.Errorf("invalid query: %s", req.RequestURI)
		}
	}))

	cfg := config.ClientConfiguration{
		ChainID:   "thorchain",
		ChainHost: server.Listener.Addr().String(),
	}

	bridge, err := thorclient.NewThorchainBridge(cfg, nil, nil)
	c.Assert(err, IsNil)
	mgr, err := NewNodeManager(bridge)
	c.Assert(err, IsNil)
	mgr.fetchNodeAccounts()
	nodes := mgr.GetNodeAccounts()
	c.Assert(nodes, HasLen, 1)
}
