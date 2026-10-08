//go:build !mocknet
// +build !mocknet

package config

import (
	"net/http"
	"net/http/httptest"

	. "gopkg.in/check.v1"
)

// A broken or unavailable seed endpoint is not fatal in getSeedAddrs: it
// returns an error with no seeds, and the caller exits only when there is no
// address book or persistent peer to fall back to.
func (Test) TestGetSeedAddrsEndpointFailuresAreNotFatal(c *C) {
	defer func(prev string) { config.Thornode.SeedNodesEndpoint = prev }(config.Thornode.SeedNodesEndpoint)

	for name, handler := range map[string]http.HandlerFunc{
		"500 with JSON error body": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":2,"message":"panic"}`))
		},
		"200 with non-array body": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":2}`))
		},
	} {
		srv := httptest.NewServer(handler)
		config.Thornode.SeedNodesEndpoint = srv.URL
		seeds, err := getSeedAddrs()
		c.Check(err, NotNil, Commentf(name))
		c.Check(seeds, HasLen, 0, Commentf(name))
		srv.Close()
	}

	// unreachable endpoint
	config.Thornode.SeedNodesEndpoint = "http://127.0.0.1:1"
	seeds, err := getSeedAddrs()
	c.Check(err, NotNil)
	c.Check(seeds, HasLen, 0)

	// no endpoint configured
	config.Thornode.SeedNodesEndpoint = ""
	_, err = getSeedAddrs()
	c.Check(err, NotNil)

	// a healthy endpoint still yields the active nodes' IPs
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"status":"Active","ip_address":"1.2.3.4"},{"status":"Standby","ip_address":"5.6.7.8"}]`))
	}))
	defer srv.Close()
	config.Thornode.SeedNodesEndpoint = srv.URL
	seeds, err = getSeedAddrs()
	c.Check(err, IsNil)
	c.Check(seeds, DeepEquals, []string{"1.2.3.4"})
}
