package evm

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	ethclient "github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"gitlab.com/thorchain/thornode/v3/config"
)

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

// DialWithFallbacks tries endpoints in slice order and returns the first that connects
// successfully. Endpoints should be pre-sorted by priority (preferred first). Endpoints
// with empty RPCHost are skipped.
func DialWithFallbacks(endpoints []config.ChainEndpointConfig) (*ethclient.Client, error) {
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
		return cl, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no EVM RPC endpoints configured")
}
