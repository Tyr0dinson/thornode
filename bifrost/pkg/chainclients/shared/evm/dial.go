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

// DialProvider dials a single provider using the appropriate auth method.
func DialProvider(p config.ChainProviderConfig) (*ethclient.Client, error) {
	host := p.RPCHost
	switch {
	case p.AuthorizationBearer != "":
		authFn := func(h http.Header) error {
			h.Set("Authorization", fmt.Sprintf("Bearer %s", p.AuthorizationBearer))
			return nil
		}
		rc, err := rpc.DialOptions(context.Background(), host, rpc.WithHTTPAuth(authFn))
		if err != nil {
			return nil, fmt.Errorf("dial bearer %s: %w", host, err)
		}
		return ethclient.NewClient(rc), nil

	case p.UserName != "" && p.Password != "":
		authFn := func(h http.Header) error {
			auth := base64.StdEncoding.EncodeToString([]byte(p.UserName + ":" + p.Password))
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

// DialWithFallbacks tries providers in slice order and returns the first that
// connects successfully. Providers should be pre-sorted by priority (highest first).
// Providers with empty RPCHost are skipped.
func DialWithFallbacks(providers []config.ChainProviderConfig) (*ethclient.Client, error) {
	var lastErr error
	for _, p := range providers {
		if p.RPCHost == "" {
			continue
		}
		cl, err := DialProvider(p)
		if err != nil {
			lastErr = err
			continue
		}
		return cl, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no EVM RPC providers configured")
}
