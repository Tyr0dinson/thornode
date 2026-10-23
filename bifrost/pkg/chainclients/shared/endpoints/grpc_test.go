package endpoints_test

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type GRPCSuite struct{}

var _ = Suite(&GRPCSuite{})

// healthServer answers Check with SERVING, or with the configured gRPC error.
type healthServer struct {
	healthpb.UnimplementedHealthServer
	err   error
	calls atomic.Int32
}

func (h *healthServer) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	h.calls.Add(1)
	if h.err != nil {
		return nil, h.err
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func bufServer(c *C, hs *healthServer) (*grpc.ClientConn, func()) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	c.Assert(err, IsNil)
	return conn, func() { _ = conn.Close(); srv.Stop() }
}

func grpcMgr(c *C, threshold int) *endpoints.Manager {
	cfgEps := []config.ChainEndpointConfig{{Name: "primary", CosmosGRPCHost: "p", RPCHost: "http://p", Priority: 1}, {Name: "backup", CosmosGRPCHost: "b", RPCHost: "http://b", Priority: 2}}
	m, err := endpoints.NewManager(common.GAIAChain, cfgEps, fc(threshold, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range m.Entries() {
		e.SetIdentity("cosmoshub-4")
	}
	return m
}

func (s *GRPCSuite) TestInvokeFailsOverOnUnavailable(c *C) {
	primary := &healthServer{err: status.Error(codes.Unavailable, "down")}
	backup := &healthServer{}
	pc, closeP := bufServer(c, primary)
	defer closeP()
	bc, closeB := bufServer(c, backup)
	defer closeB()
	m := grpcMgr(c, 1)
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc, "backup": bc}, []string{"/cosmos.tx.v1beta1.Service/BroadcastTx"}, 0, zerolog.Nop())
	res, err := healthpb.NewHealthClient(g).Check(context.Background(), &healthpb.HealthCheckRequest{})
	c.Assert(err, IsNil)
	c.Assert(res.Status, Equals, healthpb.HealthCheckResponse_SERVING)
	c.Assert(primary.calls.Load(), Equals, int32(1))
	c.Assert(backup.calls.Load(), Equals, int32(1))
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Active().Name, Equals, "backup")
	c.Assert(g.Conn("primary"), Equals, pc)
}

func (s *GRPCSuite) TestApplicationErrorsIncludingUnknownAreNotFailover(c *C) {
	for _, code := range []codes.Code{codes.NotFound, codes.Unknown, codes.InvalidArgument} {
		primary := &healthServer{err: status.Error(code, "app error")}
		backup := &healthServer{}
		pc, closeP := bufServer(c, primary)
		bc, closeB := bufServer(c, backup)
		m := grpcMgr(c, 1)
		g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc, "backup": bc}, nil, 0, zerolog.Nop())
		_, err := healthpb.NewHealthClient(g).Check(context.Background(), &healthpb.HealthCheckRequest{})
		c.Assert(status.Code(err), Equals, code)
		c.Assert(backup.calls.Load(), Equals, int32(0))
		c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
		closeP()
		closeB()
	}
}

func (s *GRPCSuite) TestWriteMethodIsNeverRetried(c *C) {
	primary := &healthServer{err: status.Error(codes.Unavailable, "down")}
	backup := &healthServer{}
	pc, closeP := bufServer(c, primary)
	defer closeP()
	bc, closeB := bufServer(c, backup)
	defer closeB()
	m := grpcMgr(c, 5)
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc, "backup": bc}, []string{"/grpc.health.v1.Health/Check"}, 0, zerolog.Nop())
	_, err := healthpb.NewHealthClient(g).Check(context.Background(), &healthpb.HealthCheckRequest{})
	c.Assert(status.Code(err), Equals, codes.Unavailable)
	c.Assert(backup.calls.Load(), Equals, int32(0))
}

func (s *GRPCSuite) TestPinnedInvokeUsesNamedEndpoint(c *C) {
	primary := &healthServer{}
	backup := &healthServer{}
	pc, closeP := bufServer(c, primary)
	defer closeP()
	bc, closeB := bufServer(c, backup)
	defer closeB()
	m := grpcMgr(c, 1)
	m.RecordError("backup", endpoints.ErrorKindTransport)
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc, "backup": bc}, nil, 0, zerolog.Nop())
	_, err := healthpb.NewHealthClient(g).Check(endpoints.PinContext(context.Background(), "backup"), &healthpb.HealthCheckRequest{})
	c.Assert(err, IsNil)
	c.Assert(primary.calls.Load(), Equals, int32(0))
	c.Assert(backup.calls.Load(), Equals, int32(1))
}

func (s *GRPCSuite) TestResourceExhaustedIsCooldown(c *C) {
	primary := &healthServer{err: status.Error(codes.ResourceExhausted, "rate")}
	backup := &healthServer{}
	pc, closeP := bufServer(c, primary)
	defer closeP()
	bc, closeB := bufServer(c, backup)
	defer closeB()
	m := grpcMgr(c, 5)
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc, "backup": bc}, nil, 0, zerolog.Nop())
	_, err := healthpb.NewHealthClient(g).Check(context.Background(), &healthpb.HealthCheckRequest{})
	c.Assert(err, IsNil)
	c.Assert(m.Entry("primary").InCooldown(), Equals, true)
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
	c.Assert(g.Close(), IsNil)
}

func (s *GRPCSuite) TestCallerCancellationRecordsNothing(c *C) {
	primary := &healthServer{}
	pc, closeP := bufServer(c, primary)
	defer closeP()
	m := grpcMgr(c, 1)
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc}, nil, 0, zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := healthpb.NewHealthClient(g).Check(ctx, &healthpb.HealthCheckRequest{})
	c.Assert(err, NotNil)
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
}

func (s *GRPCSuite) TestCredentials(c *C) {
	creds := endpoints.GRPCCredentials(config.ChainEndpointConfig{AuthorizationBearer: "tok"}, false)
	md, err := creds.GetRequestMetadata(context.Background())
	c.Assert(err, IsNil)
	c.Assert(md["authorization"], Equals, "Bearer tok")
	c.Assert(creds.RequireTransportSecurity(), Equals, false)
	basic := endpoints.GRPCCredentials(config.ChainEndpointConfig{UserName: "u", Password: "p"}, true)
	md, _ = basic.GetRequestMetadata(context.Background())
	c.Assert(md["authorization"], Equals, "Basic dTpw")
	c.Assert(basic.RequireTransportSecurity(), Equals, true)
	c.Assert(endpoints.GRPCCredentials(config.ChainEndpointConfig{}, false), IsNil)
}

// slowHealthServer blocks until the request context ends or delay passes.
type slowHealthServer struct {
	healthpb.UnimplementedHealthServer
	delay time.Duration
}

func (h *slowHealthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	select {
	case <-time.After(h.delay):
		return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *GRPCSuite) TestAttemptBudgetLeavesTimeForNextCandidate(c *C) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, &slowHealthServer{delay: 2 * time.Second})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	pc, err := grpc.NewClient("passthrough:///slow",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	c.Assert(err, IsNil)
	defer func() { _ = pc.Close() }()
	backup := &healthServer{}
	bc, closeB := bufServer(c, backup)
	defer closeB()
	m := grpcMgr(c, 5)
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc, "backup": bc}, nil, 10*time.Second, zerolog.Nop())
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	res, err := healthpb.NewHealthClient(g).Check(ctx, &healthpb.HealthCheckRequest{})
	c.Assert(err, IsNil)
	c.Assert(res.Status, Equals, healthpb.HealthCheckResponse_SERVING)
	c.Assert(backup.calls.Load(), Equals, int32(1))
	c.Assert(m.Entry("primary").Snapshot().InlineErrors, Equals, 1) // the attempt timeout counted against it
}

func (s *GRPCSuite) TestWritesRefusedWhenEveryEndpointDisabled(c *C) {
	primary := &healthServer{}
	pc, closeP := bufServer(c, primary)
	defer closeP()
	m := grpcMgr(c, 1)
	m.Disable("backup", "identity")
	m.Disable("primary", "identity")
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc}, []string{"/grpc.health.v1.Health/Check"}, 0, zerolog.Nop())
	_, err := healthpb.NewHealthClient(g).Check(context.Background(), &healthpb.HealthCheckRequest{})
	c.Assert(err, ErrorMatches, ".*no permitted write endpoint.*")
	c.Assert(primary.calls.Load(), Equals, int32(0))
}

// grpc-go reports an HTTP 403 as PermissionDenied (a revoked or exhausted API key):
// the read fails over like a 401. An HTTP 429 arrives as Unavailable with the status
// in the message: a cooldown, never a strike.
func (s *GRPCSuite) TestHTTPStatusesFromProxiesAreClassified(c *C) {
	denied := &healthServer{err: status.Error(codes.PermissionDenied, "unexpected HTTP status code received from server: 403 (Forbidden)")}
	backup := &healthServer{}
	pc, closeP := bufServer(c, denied)
	defer closeP()
	bc, closeB := bufServer(c, backup)
	defer closeB()
	m := grpcMgr(c, 1)
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"primary": pc, "backup": bc}, nil, 0, zerolog.Nop())
	_, err := healthpb.NewHealthClient(g).Check(context.Background(), &healthpb.HealthCheckRequest{})
	c.Assert(err, IsNil)
	c.Assert(backup.calls.Load(), Equals, int32(1))
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateUnhealthy)

	limited := &healthServer{err: status.Error(codes.Unavailable, "unexpected HTTP status code received from server: 429 (Too Many Requests)")}
	lc, closeL := bufServer(c, limited)
	defer closeL()
	m2 := grpcMgr(c, 1)
	g2 := endpoints.NewGRPCConn(m2, map[string]*grpc.ClientConn{"primary": lc, "backup": bc}, nil, 0, zerolog.Nop())
	_, err = healthpb.NewHealthClient(g2).Check(context.Background(), &healthpb.HealthCheckRequest{})
	c.Assert(err, IsNil)
	c.Assert(m2.Entry("primary").InCooldown(), Equals, true)
	c.Assert(m2.Entry("primary").State(), Equals, endpoints.StateHealthy)

	// an HTTP status grpc-go has no code for (a proxy's 502, a gateway's 52x) arrives as
	// Unknown with the status in the message: the endpoint failed, the backup serves
	gateway := &healthServer{err: status.Error(codes.Unknown, "unexpected HTTP status code received from server: 502 (Bad Gateway)")}
	gc, closeG := bufServer(c, gateway)
	defer closeG()
	m3 := grpcMgr(c, 1)
	g3 := endpoints.NewGRPCConn(m3, map[string]*grpc.ClientConn{"primary": gc, "backup": bc}, nil, 0, zerolog.Nop())
	_, err = healthpb.NewHealthClient(g3).Check(context.Background(), &healthpb.HealthCheckRequest{})
	c.Assert(err, IsNil)
	c.Assert(m3.Entry("primary").State(), Equals, endpoints.StateUnhealthy)

	// the Cosmos SDK's own Unknown errors are the application's answer: returned as is
	sdkErr := &healthServer{err: status.Error(codes.Unknown, "codespace sdk code 5: insufficient funds")}
	sc, closeS := bufServer(c, sdkErr)
	defer closeS()
	m4 := grpcMgr(c, 1)
	g4 := endpoints.NewGRPCConn(m4, map[string]*grpc.ClientConn{"primary": sc, "backup": bc}, nil, 0, zerolog.Nop())
	_, err = healthpb.NewHealthClient(g4).Check(context.Background(), &healthpb.HealthCheckRequest{})
	c.Assert(status.Code(err), Equals, codes.Unknown)
	c.Assert(m4.Entry("primary").State(), Equals, endpoints.StateHealthy)
}

// The only endpoint keeps the caller's whole deadline (no attempt cap), and when that
// deadline runs out waiting for it, the endpoint is charged.
func (s *GRPCSuite) TestLastCandidateKeepsTheDeadlineAndIsChargedOnExpiry(c *C) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, &slowHealthServer{delay: 400 * time.Millisecond})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	pc, err := grpc.NewClient("passthrough:///slow",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	c.Assert(err, IsNil)
	defer func() { _ = pc.Close() }()
	m, err := endpoints.NewManager(common.GAIAChain, []config.ChainEndpointConfig{{Name: "only", CosmosGRPCHost: "o", RPCHost: "http://o", Priority: 1}}, fc(1, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	g := endpoints.NewGRPCConn(m, map[string]*grpc.ClientConn{"only": pc}, nil, 100*time.Millisecond, zerolog.Nop())

	// slower than attempt_timeout but within the caller's deadline: served
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = healthpb.NewHealthClient(g).Check(ctx, &healthpb.HealthCheckRequest{})
	cancel()
	c.Assert(err, IsNil)
	c.Assert(m.Entry("only").State(), Equals, endpoints.StateHealthy)

	// the caller's deadline runs out waiting: charged
	ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, err = healthpb.NewHealthClient(g).Check(ctx, &healthpb.HealthCheckRequest{})
	cancel()
	c.Assert(err, NotNil)
	c.Assert(m.Entry("only").State(), Equals, endpoints.StateUnhealthy)
}
