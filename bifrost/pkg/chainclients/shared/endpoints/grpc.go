package endpoints

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"gitlab.com/thorchain/thornode/v3/config"
)

// GRPCConn implements grpc.ClientConnInterface over one *grpc.ClientConn per
// endpoint, routing unary calls to the manager's active endpoint and retrying reads
// on transport-level failures. Streams always go to the active endpoint.
type GRPCConn struct {
	mgr            *Manager
	conns          map[string]*grpc.ClientConn
	writeMethods   map[string]bool
	attemptTimeout time.Duration
	log            zerolog.Logger
}

var _ grpc.ClientConnInterface = (*GRPCConn)(nil)

// NewGRPCConn wraps the per-endpoint connections (keyed by endpoint name; endpoints
// without a connection are skipped). attemptTimeout bounds each read attempt so a
// hung endpoint leaves budget for the next candidate (0 means defaultAttemptTimeout).
func NewGRPCConn(mgr *Manager, conns map[string]*grpc.ClientConn, writeMethods []string, attemptTimeout time.Duration, log zerolog.Logger) *GRPCConn {
	if attemptTimeout <= 0 {
		attemptTimeout = defaultAttemptTimeout
	}
	g := &GRPCConn{
		mgr:            mgr,
		conns:          conns,
		writeMethods:   make(map[string]bool, len(writeMethods)),
		attemptTimeout: attemptTimeout,
		log:            log.With().Str("module", "endpoint-grpc").Stringer("chain", mgr.Chain()).Logger(),
	}
	for _, m := range writeMethods {
		g.writeMethods[m] = true
	}
	return g
}

// Invoke implements grpc.ClientConnInterface.
func (g *GRPCConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	var candidates []*Entry
	pinned := false
	if name, ok := pinnedFrom(ctx); ok {
		e := g.mgr.Entry(name)
		if e == nil {
			return fmt.Errorf("chain %s: pinned endpoint %s not configured", g.mgr.Chain(), name)
		}
		if !e.Usable() {
			return fmt.Errorf("chain %s: pinned endpoint %s is disabled or unverified", g.mgr.Chain(), name)
		}
		candidates, pinned = []*Entry{e}, true
	} else if g.writeMethods[method] {
		wt := g.mgr.WriteTarget()
		if wt == nil {
			return fmt.Errorf("chain %s: no permitted write endpoint (active endpoint disabled or unverified)", g.mgr.Chain())
		}
		candidates = []*Entry{wt}
	} else {
		candidates = g.mgr.ReadCandidates()
	}
	var lastErr error
	for i, e := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, ok := g.conns[e.Name]
		if !ok {
			continue
		}
		actx, cancel := ctx, func() {}
		if remaining := len(candidates) - i - 1; !pinned && !g.writeMethods[method] && remaining > 0 {
			// leave budget for the remaining candidates inside the caller's deadline;
			// the last candidate keeps the caller's whole deadline
			actx, cancel = context.WithTimeout(ctx, attemptBudget(ctx, g.attemptTimeout, remaining))
		}
		err := conn.Invoke(actx, method, args, reply, opts...)
		cancel()
		if err == nil {
			g.mgr.RecordSuccess(e.Name)
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			// the caller's deadline ran out waiting for this endpoint: that is the
			// endpoint's failure; an explicit cancel is not
			if errors.Is(cerr, context.DeadlineExceeded) {
				g.mgr.RecordError(e.Name, ErrorKindHTTP)
			}
			return err
		}
		switch code := status.Code(err); {
		case code == codes.ResourceExhausted, code == codes.Unavailable && isHTTP429(err):
			// a rate limit, also when an HTTP proxy answered 429 (grpc-go maps that
			// to Unavailable): a cooldown, never a strike
			g.mgr.RecordRateLimited(e.Name)
			lastErr = fmt.Errorf("endpoint %s: %w", e.Name, err)
			continue
		case code == codes.Unavailable, code == codes.DeadlineExceeded, code == codes.Internal,
			code == codes.Unauthenticated, code == codes.PermissionDenied, code == codes.Unimplemented,
			code == codes.Unknown && isProxyHTTPStatus(err):
			// PermissionDenied is what grpc-go reports for an HTTP 403 (a revoked or
			// exhausted API key), so it fails over like a 401; an HTTP status it has no
			// gRPC code for (a proxy's 500, a gateway's 52x) arrives as Unknown
			g.mgr.RecordError(e.Name, ErrorKindHTTP)
			lastErr = fmt.Errorf("endpoint %s: %w", e.Name, err)
			continue
		default:
			// application-level status (NotFound, InvalidArgument, and Unknown, which
			// the Cosmos SDK uses for every registered sdk error): the endpoint is fine
			g.mgr.RecordSuccess(e.Name)
			return err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("chain %s: no gRPC endpoint available", g.mgr.Chain())
	}
	return lastErr
}

// isProxyHTTPStatus reports whether grpc-go built the error from a plain HTTP response
// (a proxy or gateway answered instead of the gRPC server). The Cosmos SDK's own
// Unknown errors never carry this text, so they stay application answers.
func isProxyHTTPStatus(err error) bool {
	return strings.Contains(status.Convert(err).Message(), "unexpected HTTP status code received from server")
}

// isHTTP429 reports whether a gRPC error came from an HTTP 429 response, which
// grpc-go converts to codes.Unavailable with the HTTP status in the message.
func isHTTP429(err error) bool {
	msg := status.Convert(err).Message()
	return strings.Contains(msg, "429") && strings.Contains(strings.ToLower(msg), "http")
}

// NewStream implements grpc.ClientConnInterface (active endpoint only, no retry).
func (g *GRPCConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	e := g.mgr.WriteTarget()
	if name, ok := pinnedFrom(ctx); ok {
		pe := g.mgr.Entry(name)
		if pe == nil || !pe.Usable() {
			return nil, fmt.Errorf("chain %s: pinned endpoint %s is not usable", g.mgr.Chain(), name)
		}
		e = pe
	}
	if e == nil {
		return nil, fmt.Errorf("chain %s: no permitted endpoint for streams (active endpoint disabled or unverified)", g.mgr.Chain())
	}
	conn, ok := g.conns[e.Name]
	if !ok {
		return nil, fmt.Errorf("chain %s: active endpoint %s has no gRPC connection", g.mgr.Chain(), e.Name)
	}
	return conn.NewStream(ctx, desc, method, opts...)
}

// Conn returns the raw connection of one endpoint (nil if absent). Used by
// compound probes that must check gRPC liveness of a specific endpoint.
func (g *GRPCConn) Conn(name string) *grpc.ClientConn { return g.conns[name] }

// Close closes every underlying connection.
func (g *GRPCConn) Close() error {
	var first error
	for _, c := range g.conns {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// grpcCreds sends a static Authorization header with every RPC.
type grpcCreds struct {
	value string
	tls   bool
}

func (c grpcCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": c.value}, nil
}

func (c grpcCreds) RequireTransportSecurity() bool { return c.tls }

// GRPCCredentials returns per-RPC credentials for an endpoint's bearer token or
// basic auth, or nil when it has neither. Plaintext (tls=false) is allowed because
// operators commonly run gRPC over a private network; the caller logs a warning.
func GRPCCredentials(e config.ChainEndpointConfig, tls bool) credentials.PerRPCCredentials {
	switch {
	case e.AuthorizationBearer != "":
		return grpcCreds{value: "Bearer " + e.AuthorizationBearer, tls: tls}
	case e.UserName != "" || e.Password != "":
		return grpcCreds{value: "Basic " + base64.StdEncoding.EncodeToString([]byte(e.UserName+":"+e.Password)), tls: tls}
	}
	return nil
}
