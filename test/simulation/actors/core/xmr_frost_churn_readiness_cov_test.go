package core

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestGetMoneroDaemonBlockCountCovD(t *testing.T) {
	api := newCovThornode(t)
	var gotMethod, gotBody string
	api.Handle("/json_rpc", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		_, _ = w.Write([]byte(`{"result":{"count":1234}}`))
	})
	height, err := getMoneroDaemonBlockCount(api.URL() + "/json_rpc")
	if err != nil || height != 1234 {
		t.Fatalf("got %d, %v", height, err)
	}
	if gotMethod != http.MethodPost || !strings.Contains(gotBody, "get_block_count") {
		t.Fatalf("unexpected request %s %s", gotMethod, gotBody)
	}

	api.Status("/json_rpc", http.StatusBadRequest, "  busy  ")
	if _, err = getMoneroDaemonBlockCount(api.URL() + "/json_rpc"); err == nil || !strings.Contains(err.Error(), "unexpected status 400: busy") {
		t.Fatalf("expected status error, got %v", err)
	}
	api.JSON("/json_rpc", `not json`)
	if _, err = getMoneroDaemonBlockCount(api.URL() + "/json_rpc"); err == nil {
		t.Fatal("expected decode error")
	}
	api.JSON("/json_rpc", `{"error":{"code":-1,"message":"down"}}`)
	if _, err = getMoneroDaemonBlockCount(api.URL() + "/json_rpc"); err == nil || !strings.Contains(err.Error(), "json-rpc error") {
		t.Fatalf("expected json-rpc error, got %v", err)
	}
	if _, err = getMoneroDaemonBlockCount("://bad-url"); err == nil {
		t.Fatal("expected request construction error")
	}
	if _, err = getMoneroDaemonBlockCount("http://127.0.0.1:1/json_rpc"); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestHTTPEndpointReadyCovD(t *testing.T) {
	if err := httpEndpointReady(func(string) (*http.Response, error) { return nil, errBoomCovD }, "x"); !errors.Is(err, errBoomCovD) {
		t.Fatalf("expected transport error, got %v", err)
	}
}

func TestDefaultNodeAddressesCovD(t *testing.T) {
	if _, err := defaultNodeAddresses(nil); err == nil {
		t.Fatal("nil config must fail")
	}
	if _, err := defaultNodeAddresses(&OpConfig{}); err == nil {
		t.Fatal("empty config must fail")
	}
	dog, _ := newCovUser(t, 12, nil)
	if _, err := defaultNodeAddresses(&OpConfig{NodeUsers: []*User{dog, nil}}); err == nil || !strings.Contains(err.Error(), "validator user 1 is nil") {
		t.Fatalf("nil user must fail, got %v", err)
	}
	addrs, err := defaultNodeAddresses(&OpConfig{NodeUsers: []*User{dog}})
	if err != nil || len(addrs) != 1 || addrs[0].String() != covDThorAddr(t, dog) {
		t.Fatalf("unexpected addresses %v %v", addrs, err)
	}
}

func TestCheckXMRFrostChurnReadinessLookupFailuresCovD(t *testing.T) {
	deps := readyXMRFrostChurnDeps(t)
	deps.nodeAddresses = func(*OpConfig) ([]common.Address, error) { return nil, errBoomCovD }
	if res := checkXMRFrostChurnReadiness(nil, deps); res.ready || res.reason != "validator address lookup failed" {
		t.Fatalf("unexpected %+v", res)
	}
	deps = readyXMRFrostChurnDeps(t)
	deps.nodes = func() ([]openapi.Node, error) { return nil, errBoomCovD }
	if res := checkXMRFrostChurnReadiness(nil, deps); res.ready || res.reason != "thornode node lookup failed" || res.expectedValidators != 2 {
		t.Fatalf("unexpected %+v", res)
	}
	deps = readyXMRFrostChurnDeps(t)
	deps.nodes = func() ([]openapi.Node, error) {
		return []openapi.Node{{NodeAddress: "tthor1dog", Status: "Disabled", PeerId: "p", IpAddress: "1.2.3.4"}}, nil
	}
	res := checkXMRFrostChurnReadiness(nil, deps)
	if res.ready || res.reason != "validator nodes not ready" ||
		strings.Join(res.pendingValidators, ",") != "tthor1dog:Disabled,tthor1cat:missing" {
		t.Fatalf("unexpected %+v", res)
	}
	deps = readyXMRFrostChurnDeps(t)
	deps.httpGet = func(url string) (*http.Response, error) {
		if strings.HasSuffix(url, "/health") {
			return nil, errBoomCovD
		}
		return okHTTPResponse(), nil
	}
	if res = checkXMRFrostChurnReadiness(nil, deps); res.ready || res.reason != "xmr frost endpoint not ready" || res.endpointName != "dog-xmr-frost" {
		t.Fatalf("unexpected %+v", res)
	}
}

func TestWaitForXMRFrostChurnReadinessUsesEnvEndpointsCovD(t *testing.T) {
	api := newCovThornode(t)
	covDReadyEnv(t, api, 9)
	dog, _ := newCovUser(t, 12, nil)
	cat, _ := newCovUser(t, 13, nil)
	cfg := &OpConfig{NodeUsers: []*User{dog, cat}}

	// cat is missing its peer id
	nodes := covDNodes(t, []*User{dog, cat}, covDActive, covDStandby)
	nodes[1]["peer_id"] = " "
	api.JSON(covDPathNodes, nodes)
	if res := waitForXMRFrostChurnReadiness(covDNoop(), cfg); res.Continue || res.Finish {
		t.Fatalf("pending validator must wait: %+v", res)
	}

	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat}, covDActive, covDStandby))
	api.Status("/p2pid", http.StatusNotFound, "no")
	if res := waitForXMRFrostChurnReadiness(covDNoop(), cfg); res.Continue || res.Finish {
		t.Fatalf("bifrost endpoint must gate: %+v", res)
	}
	api.JSON("/p2pid", "ok")
	if res := waitForXMRFrostChurnReadiness(covDNoop(), cfg); !res.Continue {
		t.Fatalf("expected readiness: %+v", res)
	}
	if api.Hits("/json_rpc") != 1 {
		t.Fatalf("expected one monero height query, got %d", api.Hits("/json_rpc"))
	}
}

func TestEnvOrDefaultCovD(t *testing.T) {
	t.Setenv("COVD_ENV_OR_DEFAULT", "  set ")
	if got := envOrDefault("COVD_ENV_OR_DEFAULT", "fallback"); got != "set" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("COVD_ENV_OR_DEFAULT", "  ")
	if got := envOrDefault("COVD_ENV_OR_DEFAULT", "fallback"); got != "fallback" {
		t.Fatalf("got %q", got)
	}
}
