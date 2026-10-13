package thorchain

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"

	"cosmossdk.io/core/comet"
	sdkmath "cosmossdk.io/math"
	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkgrpc "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/cosmos/cosmos-sdk/types/module"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type ModuleCovCSuite struct{}

var _ = Suite(&ModuleCovCSuite{})

// ---------------------------------------------------------------------------
// comet.BlockInfo fakes

type covCValidator struct{ addr []byte }

func (v covCValidator) Address() []byte { return v.addr }
func (v covCValidator) Power() int64    { return 100 }

type covCVoteInfo struct{ v covCValidator }

func (v covCVoteInfo) Validator() comet.Validator        { return v.v }
func (v covCVoteInfo) GetBlockIDFlag() comet.BlockIDFlag { return comet.BlockIDFlagCommit }

type covCVoteInfos []covCVoteInfo

func (v covCVoteInfos) Len() int                 { return len(v) }
func (v covCVoteInfos) Get(i int) comet.VoteInfo { return v[i] }

type covCCommitInfo struct{ votes covCVoteInfos }

func (c covCCommitInfo) Round() int32           { return 0 }
func (c covCCommitInfo) Votes() comet.VoteInfos { return c.votes }

type covCEvidenceList struct{}

func (covCEvidenceList) Len() int               { return 0 }
func (covCEvidenceList) Get(int) comet.Evidence { return nil }

type covCBlockInfo struct{ commit covCCommitInfo }

func (b covCBlockInfo) GetEvidence() comet.EvidenceList { return covCEvidenceList{} }
func (b covCBlockInfo) GetValidatorsHash() []byte       { return nil }
func (b covCBlockInfo) GetProposerAddress() []byte      { return nil }
func (b covCBlockInfo) GetLastCommit() comet.CommitInfo { return b.commit }

// ---------------------------------------------------------------------------
// gRPC fakes for the gateway / supply route tests

type covCQueryServer struct {
	types.UnimplementedQueryServer
	mu            sync.Mutex
	lastHeightMD  []string
	supply        *types.QuerySupplyResponse
	supplyErr     error
	stakeCoins    sdk.Coins
	stakeErr      error
	claimCoins    sdk.Coins
	claimErr      error
	contractBals  []*types.Amount
	contractError error
}

func (s *covCQueryServer) Supply(ctx context.Context, _ *types.QuerySupplyRequest) (*types.QuerySupplyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	md, _ := metadata.FromIncomingContext(ctx)
	s.lastHeightMD = md.Get(sdkgrpc.GRPCBlockHeightHeader)
	if s.supplyErr != nil {
		return nil, s.supplyErr
	}
	return s.supply, nil
}

func (s *covCQueryServer) BalanceModule(_ context.Context, req *types.QueryBalanceModuleRequest) (*types.QueryBalanceModuleResponse, error) {
	switch req.Name {
	case TCYStakeName:
		if s.stakeErr != nil {
			return nil, s.stakeErr
		}
		return &types.QueryBalanceModuleResponse{Name: req.Name, Coins: s.stakeCoins}, nil
	case TCYClaimingName:
		if s.claimErr != nil {
			return nil, s.claimErr
		}
		return &types.QueryBalanceModuleResponse{Name: req.Name, Coins: s.claimCoins}, nil
	}
	return &types.QueryBalanceModuleResponse{Name: req.Name}, nil
}

func (s *covCQueryServer) Balances(_ context.Context, _ *types.QueryBalancesRequest) (*types.QueryBalancesResponse, error) {
	if s.contractError != nil {
		return nil, s.contractError
	}
	return &types.QueryBalancesResponse{Balances: s.contractBals}, nil
}

type covCBankServer struct {
	banktypes.UnimplementedQueryServer
	amount int64
	err    error
}

func (b *covCBankServer) SupplyOf(_ context.Context, req *banktypes.QuerySupplyOfRequest) (*banktypes.QuerySupplyOfResponse, error) {
	if b.err != nil {
		return nil, b.err
	}
	return &banktypes.QuerySupplyOfResponse{Amount: sdk.NewCoin(req.Denom, sdkmath.NewInt(b.amount))}, nil
}

// covCStartGRPC starts an in-memory gRPC server with the given fakes and returns a
// client.Context wired to it.
func covCStartGRPC(c *C, qs *covCQueryServer, bs *covCBankServer) (client.Context, func()) {
	reg := cdctypes.NewInterfaceRegistry()
	std.RegisterInterfaces(reg)
	RegisterInterfaces(reg)
	grpcCodec := codec.NewProtoCodec(reg).GRPCCodec()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.ForceServerCodec(grpcCodec))
	types.RegisterQueryServer(srv, qs)
	banktypes.RegisterQueryServer(srv, bs)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(grpcCodec)),
	)
	c.Assert(err, IsNil)

	clientCtx := client.Context{}.WithGRPCClient(conn).WithInterfaceRegistry(reg)
	return clientCtx, func() {
		_ = conn.Close()
		srv.Stop()
	}
}

// ---------------------------------------------------------------------------

func (s *ModuleCovCSuite) TestAppModuleBasicCovC(c *C) {
	SetupConfigForTest()
	amb := AppModuleBasic{}
	c.Check(amb.Name(), Equals, ModuleName)

	amino := codec.NewLegacyAmino()
	amb.RegisterLegacyAminoCodec(amino)
	bz, err := amino.MarshalJSON(&MsgBan{})
	c.Assert(err, IsNil)
	c.Check(string(bz), Matches, `.*thorchain/MsgBan.*`)

	reg := cdctypes.NewInterfaceRegistry()
	amb.RegisterInterfaces(reg)
	resolved, err := reg.Resolve("/types.MsgDeposit")
	c.Assert(err, IsNil)
	_, ok := resolved.(*MsgDeposit)
	c.Check(ok, Equals, true)

	cdc := codec.NewProtoCodec(reg)
	raw := amb.DefaultGenesis(cdc)
	var gs GenesisState
	c.Assert(cdc.UnmarshalJSON(raw, &gs), IsNil)
	c.Check(gs.Pools, HasLen, 0)

	// default genesis validates
	c.Check(amb.ValidateGenesis(cdc, nil, raw), IsNil)
	// malformed json fails
	c.Check(amb.ValidateGenesis(cdc, nil, json.RawMessage(`{not-json`)), NotNil)
	// structurally valid json but semantically invalid genesis fails validation
	bad := DefaultGenesisState()
	bad.Pools = append(bad.Pools, Pool{})
	badRaw := cdc.MustMarshalJSON(&bad)
	c.Check(amb.ValidateGenesis(cdc, nil, badRaw), NotNil)

	qcmd := amb.GetQueryCmd()
	c.Assert(qcmd, NotNil)
	c.Check(qcmd.HasSubCommands(), Equals, true)
	tcmd := amb.GetTxCmd()
	c.Assert(tcmd, NotNil)
	c.Check(tcmd.HasSubCommands(), Equals, true)
}

func (s *ModuleCovCSuite) TestAppModuleBasicsCovC(c *C) {
	_, mgr := setupManagerForTest(c)
	am := NewAppModule(mgr, false, true)
	am.IsAppModule()
	am.IsOnePerModuleType()
	am.RegisterInvariants(nil)
	c.Check(am.ConsensusVersion(), Equals, uint64(20))
	c.Check(am.QuerierRoute(), Equals, types.QuerierRoute)
	c.Check(am.msgServer, NotNil)
	c.Check(am.queryServer, NotNil)
	c.Check(am.telemetryEnabled, Equals, false)
}

func (s *ModuleCovCSuite) TestRegisterServicesCovC(c *C) {
	_, mgr := setupManagerForTest(c)
	am := NewAppModule(mgr, false, true)

	reg := cdctypes.NewInterfaceRegistry()
	std.RegisterInterfaces(reg)
	RegisterInterfaces(reg)
	msr := baseapp.NewMsgServiceRouter()
	msr.SetInterfaceRegistry(reg)
	qr := baseapp.NewGRPCQueryRouter()
	qr.SetInterfaceRegistry(reg)

	cfg := module.NewConfigurator(codec.NewProtoCodec(reg), msr, qr)
	am.RegisterServices(cfg)
	c.Assert(cfg.Error(), IsNil)

	c.Check(msr.HandlerByTypeURL("/types.MsgDeposit"), NotNil)
	c.Check(msr.HandlerByTypeURL("/types.MsgSend"), NotNil)
	c.Check(qr.Route("/types.Query/Pools"), NotNil)
	c.Check(qr.Route("/cosmwasm.wasm.v1.Query/Params"), NotNil)

	// a migration already registered for version 15 makes RegisterServices panic
	msr2 := baseapp.NewMsgServiceRouter()
	msr2.SetInterfaceRegistry(reg)
	qr2 := baseapp.NewGRPCQueryRouter()
	qr2.SetInterfaceRegistry(reg)
	cfg2 := module.NewConfigurator(codec.NewProtoCodec(reg), msr2, qr2)
	m := NewMigrator(mgr)
	c.Assert(cfg2.RegisterMigration(types.ModuleName, 15, m.Migrate15to16), IsNil)
	c.Check(func() { am.RegisterServices(cfg2) }, PanicMatches, `failed to migrate x/thorchain from version 15 to 16.*`)
}

func (s *ModuleCovCSuite) TestBeginBlockCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	am := NewAppModule(mgr, false, true)
	k := mgr.Keeper()

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// seed an oracle price and observing addresses: both are cleared every block
	c.Assert(k.SetPrice(ctx, OraclePrice{Symbol: "BTC", Price: "100000"}), IsNil)
	c.Assert(k.AddObservingAddresses(ctx, []cosmos.AccAddress{GetRandomBech32Addr()}), IsNil)
	addrs, err := k.GetObservingAddresses(ctx)
	c.Assert(err, IsNil)
	c.Assert(addrs, HasLen, 1)

	info := covCBlockInfo{commit: covCCommitInfo{votes: covCVoteInfos{
		{v: covCValidator{addr: []byte("validator-address-0001")}},
		{v: covCValidator{addr: []byte("validator-address-0002")}},
	}}}
	ctx = ctx.WithCometInfo(info)

	c.Assert(am.BeginBlock(ctx), IsNil)

	_, err = k.GetPrice(ctx, "BTC")
	c.Check(err, NotNil)
	addrs, err = k.GetObservingAddresses(ctx)
	c.Assert(err, IsNil)
	c.Check(addrs, HasLen, 0)
}

func (s *ModuleCovCSuite) TestBeginBlockUnsupportedVersionCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	am := NewAppModule(mgr, false, true)
	ctx = ctx.WithCometInfo(covCBlockInfo{})
	na := GetRandomValidatorNode(NodeActive)
	na.Version = mgr.GetVersion().String()
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	c.Assert(mgr.GetVersion().GT(semver.MustParse("0.0.1")), Equals, true)

	orig := constants.SWVersion
	defer func() { constants.SWVersion = orig }()
	// pretend the local binary is older than the network consensus version
	constants.SWVersion = semver.MustParse("0.0.1")
	c.Check(func() { _ = am.BeginBlock(ctx) }, PanicMatches, `Unsupported Version.*`)
}

func (s *ModuleCovCSuite) TestEndBlockCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	vault := GetRandomVault()
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	for _, telemetryEnabled := range []bool{false, true} {
		am := NewAppModule(mgr, telemetryEnabled, true)
		updates, err := am.EndBlock(ctx)
		c.Assert(err, IsNil)
		// no churn happened, so there are no validator changes
		c.Check(updates, HasLen, 0)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	}

	// the active node is still active after the end blocks
	got, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, NodeActive)
}

func (s *ModuleCovCSuite) TestInitExportGenesisCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	am := NewAppModule(mgr, false, true)
	k := mgr.Keeper()

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	exported := am.ExportGenesis(ctx, nil)
	var gs GenesisState
	ModuleCdc.MustUnmarshalJSON(exported, &gs)
	c.Assert(gs.NodeAccounts, HasLen, 1)
	c.Check(gs.NodeAccounts[0].NodeAddress.String(), Equals, na.NodeAddress.String())
	c.Assert(gs.Pools, HasLen, 1)
	c.Check(gs.Pools[0].Asset.Equals(common.BTCAsset), Equals, true)

	// import into a fresh chain
	ctx2, mgr2 := setupManagerForTest(c)
	am2 := NewAppModule(mgr2, false, true)
	updates := am2.InitGenesis(ctx2, nil, exported)
	c.Assert(updates, HasLen, 1)
	c.Check(updates[0].Power, Equals, int64(100))

	got, err := mgr2.Keeper().GetPool(ctx2, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(got.BalanceRune.Uint64(), Equals, uint64(100*common.One))
	gotNa, err := mgr2.Keeper().GetNodeAccount(ctx2, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(gotNa.Status, Equals, NodeActive)
}

func (s *ModuleCovCSuite) TestGRPCGatewayRoutesAndHeightCovC(c *C) {
	qs := &covCQueryServer{supply: &types.QuerySupplyResponse{Circulating: 7, Total: 9}}
	clientCtx, stop := covCStartGRPC(c, qs, &covCBankServer{})
	defer stop()

	apiSvr := &api.Server{ClientCtx: clientCtx}
	CustomGRPCGatewayRouter(apiSvr)
	c.Assert(apiSvr.GRPCGatewayRouter, NotNil)
	AppModuleBasic{}.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)

	do := func(path string, hdr map[string]string) (int, string, []string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		apiSvr.GRPCGatewayRouter.ServeHTTP(rec, req)
		qs.mu.Lock()
		defer qs.mu.Unlock()
		return rec.Code, rec.Body.String(), qs.lastHeightMD
	}

	// valid height query param is forwarded as the block height header
	code, body, md := do("/thorchain/supply?height=42", nil)
	c.Assert(code, Equals, http.StatusOK, Commentf("body: %s", body))
	c.Check(body, Matches, `(?s).*"circulating": 7,.*"total": 9.*`)
	c.Check(md, DeepEquals, []string{"42"})

	// non-integer height is ignored
	code, _, md = do("/thorchain/supply?height=abc", nil)
	c.Check(code, Equals, http.StatusOK)
	c.Check(md, HasLen, 0)

	// no height
	code, _, md = do("/thorchain/supply", nil)
	c.Check(code, Equals, http.StatusOK)
	c.Check(md, HasLen, 0)

	// NOTE: net/http canonicalises header keys ("X-Cosmos-Block-Height"), so the
	// early return in the metadata annotator (which compares against the
	// lower-case GRPCBlockHeightHeader) does not fire for normal requests and the
	// query param height is appended after the header height.
	code, _, md = do("/thorchain/supply?height=42", map[string]string{sdkgrpc.GRPCBlockHeightHeader: "7"})
	c.Check(code, Equals, http.StatusOK)
	c.Check(md, DeepEquals, []string{"7", "42"})

	// a header stored under the exact lower-case key short-circuits the
	// annotator, so the height query param is ignored
	req := httptest.NewRequest(http.MethodGet, "/thorchain/supply?height=42", nil)
	req.Header[sdkgrpc.GRPCBlockHeightHeader] = []string{"7"}
	rec := httptest.NewRecorder()
	apiSvr.GRPCGatewayRouter.ServeHTTP(rec, req)
	c.Check(rec.Code, Equals, http.StatusOK)
	qs.mu.Lock()
	for _, h := range qs.lastHeightMD {
		c.Check(h, Not(Equals), "42")
	}
	qs.mu.Unlock()
}

func (s *ModuleCovCSuite) TestSupplyCMCRouteCovC(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: expected supply figures use mocknet TCY and RUNE constants")
	}
	SetupConfigForTest()
	qs := &covCQueryServer{
		supply:     &types.QuerySupplyResponse{Circulating: 300, Total: 500, Locked: &types.LockedSupply{Reserve: 200}},
		stakeCoins: sdk.NewCoins(sdk.NewCoin("tcy", sdkmath.NewInt(5*common.One)), sdk.NewCoin("rune", sdkmath.NewInt(99*common.One))),
		claimCoins: sdk.NewCoins(sdk.NewCoin("tcy", sdkmath.NewInt(20*common.One))),
		contractBals: []*types.Amount{
			{Denom: "tcy", Amount: "300000000"},
			{Denom: "tcy", Amount: "not-a-number"},
			{Denom: "rune", Amount: "100000000000"},
		},
	}
	bs := &covCBankServer{amount: 1000 * common.One}
	clientCtx, stop := covCStartGRPC(c, qs, bs)
	defer stop()

	router := mux.NewRouter()
	RegisterSupplyCMCRoute(router, clientCtx)

	get := func(query string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/thorchain/supply/cmc"+query, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	type tc struct {
		query string
		code  int
		body  string
	}
	cases := []tc{
		{"?type=circulating", http.StatusOK, "300"},
		{"?asset=RUNE&type=total", http.StatusOK, "500"},
		{"?asset=rune&type=locked", http.StatusOK, "200"},
		{"?asset=rune&type=bogus", http.StatusBadRequest, "type must be circulating, total, or locked\n"},
		{"?asset=tcy&type=total", http.StatusOK, "1000"},
		// 5 TCY in the stake module + 3 TCY in the staking contract
		{"?asset=tcy&type=staked", http.StatusOK, "8"},
		{"?asset=tcy&type=locked", http.StatusOK, "20"},
		{"?asset=tcy&type=circulating", http.StatusOK, "980"},
		{"?asset=tcy&type=nope", http.StatusBadRequest, "type must be total, staked, locked, or circulating\n"},
		{"?asset=eth&type=total", http.StatusBadRequest, "asset must be rune or tcy\n"},
	}
	for _, t := range cases {
		code, body := get(t.query)
		c.Check(code, Equals, t.code, Commentf("query %s", t.query))
		c.Check(body, Equals, t.body, Commentf("query %s", t.query))
	}

	// rune locked with no locked supply returns zero
	qs.supply = &types.QuerySupplyResponse{Circulating: 1, Total: 2}
	code, body := get("?type=locked")
	c.Check(code, Equals, http.StatusOK)
	c.Check(body, Equals, "0")

	// non-GET is not routed
	req := httptest.NewRequest(http.MethodPost, "/thorchain/supply/cmc", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	c.Check(rec.Code, Equals, http.StatusMethodNotAllowed)

	// contract balance query failures are skipped
	qs.contractError = errKaboom
	code, body = get("?asset=tcy&type=staked")
	c.Check(code, Equals, http.StatusOK)
	c.Check(body, Equals, "5")

	// error paths
	qs.claimErr = errKaboom
	code, body = get("?asset=tcy&type=locked")
	c.Check(code, Equals, http.StatusInternalServerError)
	c.Check(body, Equals, "failed to query tcy claim module\n")

	qs.stakeErr = errKaboom
	code, body = get("?asset=tcy&type=staked")
	c.Check(code, Equals, http.StatusInternalServerError)
	c.Check(body, Equals, "failed to query tcy stake module\n")

	bs.err = errKaboom
	code, body = get("?asset=tcy&type=total")
	c.Check(code, Equals, http.StatusInternalServerError)
	c.Check(body, Equals, "failed to query tcy supply\n")

	qs.supplyErr = errKaboom
	code, body = get("?asset=rune&type=total")
	c.Check(code, Equals, http.StatusInternalServerError)
	c.Check(body, Equals, "failed to query rune supply\n")
}

// failing manager wrappers: each records that it was invoked and returns an error
type covCCalls struct{ names []string }

func (r *covCCalls) hit(name string) error {
	r.names = append(r.names, name)
	return errKaboom
}

type covCFailSwapQ struct {
	SwapQueue
	r *covCCalls
}

func (f covCFailSwapQ) EndBlock(cosmos.Context, Manager) error { return f.r.hit("swapq") }

type covCFailAdvSwapQ struct {
	AdvSwapQueue
	r *covCCalls
}

func (f covCFailAdvSwapQ) EndBlock(cosmos.Context, Manager, bool) error { return f.r.hit("advswapq") }

type covCFailSlasher struct {
	Slasher
	r *covCCalls
}

func (f covCFailSlasher) LackSigning(cosmos.Context, Manager) error { return f.r.hit("lacksigning") }

type covCFailPoolMgr struct {
	PoolManager
	r *covCCalls
}

func (f covCFailPoolMgr) EndBlock(cosmos.Context, Manager) error { return f.r.hit("pool") }

type covCFailNetworkMgr struct {
	NetworkManager
	r *covCCalls
}

func (f covCFailNetworkMgr) UpdateNetwork(cosmos.Context, Manager) error {
	return f.r.hit("updatenetwork")
}
func (f covCFailNetworkMgr) EndBlock(cosmos.Context, Manager) error { return f.r.hit("network") }

type covCFailStableReserve struct {
	StableReserveManager
	r *covCCalls
}

func (f covCFailStableReserve) EndBlock(cosmos.Context, Manager) error {
	return f.r.hit("stablereserve")
}

type covCFailTxOut struct {
	TxOutStore
	r *covCCalls
}

func (f covCFailTxOut) EndBlock(cosmos.Context, Manager) error { return f.r.hit("txout") }

type covCFailVolume struct {
	VolumeManager
	r *covCCalls
}

func (f covCFailVolume) EndBlock(cosmos.Context) error { return f.r.hit("volume") }

type covCFailDynFee struct {
	DynamicFeeManager
	r *covCCalls
}

func (f covCFailDynFee) EndBlock(cosmos.Context) error { return f.r.hit("dynamicfee") }

type covCFailSchedMig struct {
	ScheduledMigrationManager
	r *covCCalls
}

func (f covCFailSchedMig) EndBlock(cosmos.Context, Manager) error { return f.r.hit("schedmig") }

func (s *ModuleCovCSuite) TestEndBlockManagerErrorsCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	r := &covCCalls{}
	mgr.swapQ = covCFailSwapQ{SwapQueue: mgr.swapQ, r: r}
	mgr.advSwapQueue = covCFailAdvSwapQ{AdvSwapQueue: mgr.advSwapQueue, r: r}
	mgr.slasher = covCFailSlasher{Slasher: mgr.slasher, r: r}
	mgr.poolMgr = covCFailPoolMgr{PoolManager: mgr.poolMgr, r: r}
	mgr.networkMgr = covCFailNetworkMgr{NetworkManager: mgr.networkMgr, r: r}
	mgr.stableReserveManager = covCFailStableReserve{StableReserveManager: mgr.stableReserveManager, r: r}
	mgr.txOutStore = covCFailTxOut{TxOutStore: mgr.txOutStore, r: r}
	mgr.volumeManager = covCFailVolume{VolumeManager: mgr.volumeManager, r: r}
	mgr.dynamicFeeManager = covCFailDynFee{DynamicFeeManager: mgr.dynamicFeeManager, r: r}
	mgr.scheduledMigrationManager = covCFailSchedMig{ScheduledMigrationManager: mgr.scheduledMigrationManager, r: r}

	am := NewAppModule(mgr, true, true)
	updates, err := am.EndBlock(ctx)
	// manager errors are logged and swallowed; every manager still runs in order
	c.Assert(err, IsNil)
	c.Check(updates, HasLen, 0)
	c.Check(r.names, DeepEquals, []string{
		"swapq", "advswapq", "lacksigning", "pool", "updatenetwork",
		"stablereserve", "network", "txout", "volume", "dynamicfee", "schedmig",
	})
}
