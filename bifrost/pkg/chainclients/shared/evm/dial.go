package evm

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	ethclient "github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"gitlab.com/thorchain/thornode/v3/config"
)

// dialProbeFloor is the fallback liveness-probe timeout used by DialWithFallbacks when
// the caller supplies a non-positive value. Dialing an http(s) endpoint with go-ethereum
// is lazy and never opens a connection, so an explicit request is required to verify
// reachability.
const dialProbeFloor = 5 * time.Second

// DialEndpoint dials a single endpoint using the appropriate auth method.
func DialEndpoint(e config.ChainEndpointConfig) (*ethclient.Client, error) {
	host := e.RPCHost
	switch {
	case e.AuthorizationBearer != "":
		authFn := func(h http.Header) error {
			h.Set("Authorization", fmt.Sprintf("Bearer %s", e.AuthorizationBearer))
			return nil
		}
		rc, err := rpc.DialOptions(context.Background(), host, rpc.WithHTTPAuth(authFn))
		if err != nil {
			return nil, fmt.Errorf("dial bearer %s: %w", host, err)
		}
		return ethclient.NewClient(rc), nil

	case e.UserName != "" && e.Password != "":
		authFn := func(h http.Header) error {
			auth := base64.StdEncoding.EncodeToString([]byte(e.UserName + ":" + e.Password))
			h.Set("Authorization", fmt.Sprintf("Basic %s", auth))
			return nil
		}
		rc, err := rpc.DialOptions(context.Background(), host, rpc.WithHTTPAuth(authFn))
		if err != nil {
			return nil, fmt.Errorf("dial basic-auth %s: %w", host, err)
		}
		return ethclient.NewClient(rc), nil

	default:
		cl, err := ethclient.Dial(host)
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", host, err)
		}
		return cl, nil
	}
}

// DialWithFallbacks tries endpoints in slice order and returns the first that is
// reachable. Endpoints should be pre-sorted by priority (preferred first). Endpoints
// with empty RPCHost are skipped. probeTimeout bounds the per-endpoint reachability
// probe and should match the caller's RPC request timeout; values <= 0 fall back to
// dialProbeFloor.
//
// Dialing an http(s) endpoint with go-ethereum is lazy: ethclient.Dial/rpc.DialOptions
// only build a client and never open a connection, so a syntactically valid but dead
// host would otherwise be accepted and the remaining fallbacks never tried. Each endpoint
// is therefore probed with a real eth_chainId request before being accepted.
func DialWithFallbacks(endpoints []config.ChainEndpointConfig, probeTimeout time.Duration) (*ethclient.Client, error) {
	if probeTimeout <= 0 {
		probeTimeout = dialProbeFloor
	}
	var lastErr error
	for _, e := range endpoints {
		if e.RPCHost == "" {
			continue
		}
		cl, err := DialEndpoint(e)
		if err != nil {
			lastErr = err
			continue
		}
		// Probe reachability — the dial above is lazy and never connected.
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		_, err = cl.ChainID(ctx)
		cancel()
		if err != nil {
			cl.Close()
			lastErr = fmt.Errorf("endpoint %s unreachable: %w", e.RPCHost, err)
			continue
		}
		return cl, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no EVM RPC endpoints configured")
}
