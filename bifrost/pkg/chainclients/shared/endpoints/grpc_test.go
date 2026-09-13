package endpoints_test

import (
	"context"
	"net"
	"sync/atomic"

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
