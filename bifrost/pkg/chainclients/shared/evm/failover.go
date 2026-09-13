package evm

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	ethclient "github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/config"
)

// evmWriteMethods are never retried on a second endpoint once they may have been sent.
var evmWriteMethods = []string{"eth_sendRawTransaction", "eth_sendTransaction"}

// FailoverClient is the endpoint manager plus the routed ethclient for one EVM chain.
type FailoverClient struct {
	Manager *endpoints.Manager
	Client  *ethclient.Client
	// ChainID is the chain id every verified endpoint reports (the reference
	// identity). Signing uses it, so it never comes from an unverified endpoint.
	ChainID *big.Int
}

// AttemptTimeout derives the per-attempt header timeout from the chain's request
// timeout: half of it, capped at 10s, so a black-holed endpoint leaves budget for
// the next candidate.
func AttemptTimeout(requestTimeout time.Duration) time.Duration {
	if requestTimeout <= 0 {
		return 10 * time.Second
	}
	t := requestTimeout / 2
	if t > 10*time.Second {
		t = 10 * time.Second
	}
	return t
}

// NewFailoverClient builds the endpoint manager for an EVM chain, bootstraps it
// (identity + liveness for every endpoint), and returns an *ethclient.Client whose
// HTTP transport routes every JSON-RPC call to the active endpoint. The caller
// must call Manager.SetScannerHeight and Manager.Start once its scanner exists, and
// Manager.Stop on shutdown.
//
// It refuses to start without a verified chain id: signing with a chain id taken
// from an unverified endpoint could replay an outbound on another EVM network.
func NewFailoverClient(ctx context.Context, cfg config.BifrostChainConfiguration, timeout time.Duration, log zerolog.Logger) (*FailoverClient, error) {
	eps := cfg.GetEndpoints()
	fc := cfg.GetFailoverConfig()
	opts := []endpoints.Option{endpoints.WithMetrics(endpoints.DefaultMetrics())}
	if cfg.ExpectedIdentity != "" {
		opts = append(opts, endpoints.WithExpectedIdentity(strings.ToLower(cfg.ExpectedIdentity)))
	}
	mgr, err := endpoints.NewManager(cfg.ChainID, eps, fc, log, opts...)
	if err != nil {
		return nil, err
	}
	ordered := endpoints.OrderedEndpoints(mgr, eps)
	probes := make([]endpoints.Probe, len(ordered))
	for i, e := range ordered {
		probes[i] = endpoints.NewEVMProbe(e, fc.HealthCheckTimeout)
	}
	if err = mgr.Bootstrap(ctx, probes); err != nil {
		return nil, err
	}
	ref := mgr.ReferenceIdentity()
	if ref == "" {
		return nil, fmt.Errorf("chain %s: no endpoint reported a verified chain id (eth_chainId); refusing to start", cfg.ChainID)
	}
	chainID, ok := new(big.Int).SetString(strings.TrimPrefix(ref, "0x"), 16)
	if !ok || chainID.Sign() <= 0 {
		return nil, fmt.Errorf("chain %s: invalid chain id %q", cfg.ChainID, ref)
	}
	targets, err := endpoints.TargetsFrom(ordered, endpoints.RPCHost)
	if err != nil {
		return nil, err
	}
	base := endpoints.SyntheticBase(cfg.ChainID)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{
		Base:           base,
		Targets:        targets,
		WriteMethods:   evmWriteMethods,
		DialTimeout:    fc.HealthCheckTimeout,
		AttemptTimeout: AttemptTimeout(timeout),
	}, log)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Transport: router, Timeout: timeout}
	rc, err := rpc.DialOptions(ctx, base.String(), rpc.WithHTTPClient(hc))
	if err != nil {
		return nil, fmt.Errorf("dial EVM rpc: %w", err)
	}
	return &FailoverClient{Manager: mgr, Client: ethclient.NewClient(rc), ChainID: chainID}, nil
}
