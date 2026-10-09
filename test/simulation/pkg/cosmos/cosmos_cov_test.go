package cosmos

import (
	"context"
	"net"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	atypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	btypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const mnemonicCov = "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

func keysCov(t *testing.T) *thorclient.Keys {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kr := keyring.NewInMemory(codec.NewProtoCodec(registry))
	if _, err := kr.NewAccount("dog", mnemonicCov, "", cmd.THORChainHDPath, hd.Secp256k1); err != nil {
		t.Fatal(err)
	}
	return thorclient.NewKeysWithKeybase(kr, "dog", "")
}

type fakeCosmosCov struct {
	balances   sdk.Coins
	balanceErr error
	account    *atypes.BaseAccount
	accountErr error
	badAccount bool

	broadcastCode uint32
	broadcastErr  error
	broadcasts    [][]byte
	lastAddress   string
	getTxHashes   []string
}

type bankSrvCov struct {
	btypes.UnimplementedQueryServer
	f *fakeCosmosCov
}

func (b *bankSrvCov) AllBalances(_ context.Context, req *btypes.QueryAllBalancesRequest) (*btypes.QueryAllBalancesResponse, error) {
	b.f.lastAddress = req.Address
	if b.f.balanceErr != nil {
		return nil, b.f.balanceErr
	}
	return &btypes.QueryAllBalancesResponse{Balances: b.f.balances}, nil
}

type authSrvCov struct {
	atypes.UnimplementedQueryServer
	f *fakeCosmosCov
}

func (a *authSrvCov) Account(_ context.Context, _ *atypes.QueryAccountRequest) (*atypes.QueryAccountResponse, error) {
	if a.f.accountErr != nil {
		return nil, a.f.accountErr
	}
	if a.f.badAccount {
		return &atypes.QueryAccountResponse{Account: &codectypes.Any{TypeUrl: "/x", Value: []byte{0xff, 0xff}}}, nil
	}
	bz, err := a.f.account.Marshal()
	if err != nil {
		return nil, err
	}
	return &atypes.QueryAccountResponse{Account: &codectypes.Any{TypeUrl: "/cosmos.auth.v1beta1.BaseAccount", Value: bz}}, nil
}

type txSrvCov struct {
	txtypes.UnimplementedServiceServer
	f *fakeCosmosCov
}

func (s *txSrvCov) BroadcastTx(_ context.Context, req *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
	s.f.broadcasts = append(s.f.broadcasts, req.TxBytes)
	if s.f.broadcastErr != nil {
		return nil, s.f.broadcastErr
	}
	return &txtypes.BroadcastTxResponse{TxResponse: &sdk.TxResponse{Code: s.f.broadcastCode, TxHash: "ABC", RawLog: "bad"}}, nil
}

func (s *txSrvCov) GetTx(_ context.Context, req *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
	s.f.getTxHashes = append(s.f.getTxHashes, req.Hash)
	return &txtypes.GetTxResponse{TxResponse: &sdk.TxResponse{TxHash: req.Hash}}, nil
}

func newClientCov(t *testing.T, chain common.Chain) (*Client, *fakeCosmosCov) {
	t.Helper()
	f := &fakeCosmosCov{
		balances: sdk.NewCoins(sdk.NewCoin("uatom", sdkmath.NewInt(5)), sdk.NewCoin("uusdc", sdkmath.NewInt(7)), sdk.NewCoin("other", sdkmath.NewInt(9))),
		account:  &atypes.BaseAccount{AccountNumber: 3, Sequence: 11},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	btypes.RegisterQueryServer(s, &bankSrvCov{f: f})
	atypes.RegisterQueryServer(s, &authSrvCov{f: f})
	txtypes.RegisterServiceServer(s, &txSrvCov{f: f})
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(s.Stop)

	lc, err := NewConstructor(ln.Addr().String())(chain, keysCov(t))
	if err != nil {
		t.Fatal(err)
	}
	c := lc.(*Client)
	t.Cleanup(func() { _ = c.grpc.Close() })
	return c, f
}

func TestNewClientChainsCov(t *testing.T) {
	c, _ := newClientCov(t, common.GAIAChain)
	if c.denom != "uatom" || c.chainId != "localgaia" || !strings.HasPrefix(c.address.String(), "cosmos1") {
		t.Fatalf("gaia client: %s %s %s", c.denom, c.chainId, c.address)
	}
	n, _ := newClientCov(t, common.NOBLEChain)
	if n.denom != "uusdc" || n.chainId != "localnoble" {
		t.Fatalf("noble client: %s %s", n.denom, n.chainId)
	}

	if _, err := NewClient(common.BTCChain, "127.0.0.1:1", keysCov(t)); err == nil || !strings.Contains(err.Error(), "unsupported chain") {
		t.Fatalf("expected unsupported chain, got %v", err)
	}
	empty := thorclient.NewKeysWithKeybase(keyring.NewInMemory(codec.NewProtoCodec(codectypes.NewInterfaceRegistry())), "x", "")
	if _, err := NewClient(common.GAIAChain, "127.0.0.1:1", empty); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("expected private key error, got %v", err)
	}
	if _, err := c.SignContractTx(SimContractTx{}); err == nil {
		t.Fatal("SignContractTx should be unimplemented")
	}
}

func TestGetAccountCov(t *testing.T) {
	c, f := newClientCov(t, common.GAIAChain)
	acct, err := c.GetAccount(nil)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Sequence != 11 || acct.AccountNumber != 3 {
		t.Fatalf("account = %+v", acct)
	}
	if len(acct.Coins) != 1 || !acct.Coins[0].Asset.Equals(common.ATOMAsset) || acct.Coins[0].Amount.Uint64() != 500 {
		t.Fatalf("coins = %v", acct.Coins)
	}
	if f.lastAddress != c.address.String() {
		t.Fatalf("queried %s", f.lastAddress)
	}

	// explicit pubkey resolves to the same address
	pk := c.pubKey
	if _, err = c.GetAccount(&pk); err != nil || f.lastAddress != c.address.String() {
		t.Fatalf("explicit pubkey: %v %s", err, f.lastAddress)
	}
	bad := common.PubKey("nope")
	if _, err = c.GetAccount(&bad); err == nil {
		t.Fatal("expected bad pubkey error")
	}

	// missing account => zero sequence with balances
	f.accountErr = status.Error(codes.NotFound, "account not found")
	acct, err = c.GetAccount(nil)
	if err != nil || acct.Sequence != 0 || len(acct.Coins) != 1 {
		t.Fatalf("not found: %+v %v", acct, err)
	}

	f.accountErr = status.Error(codes.Unavailable, "down")
	if _, err = c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "fail to get account") {
		t.Fatalf("expected account error, got %v", err)
	}

	f.accountErr = nil
	f.badAccount = true
	if _, err = c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("expected unmarshal error, got %v", err)
	}

	f.balanceErr = status.Error(codes.Internal, "x")
	if _, err = c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "balance") {
		t.Fatalf("expected balance error, got %v", err)
	}
}

func TestSignTxCov(t *testing.T) {
	c, f := newClientCov(t, common.GAIAChain)
	signed, err := c.SignTx(SimTx{
		Chain:     common.GAIAChain,
		ToAddress: c.address,
		Coin:      common.NewCoin(common.ATOMAsset, cosmos.NewUint(12345)),
		Memo:      "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(reg)
	reg.RegisterImplementations((*sdk.Msg)(nil), &btypes.MsgSend{})
	decCfg := authtx.NewTxConfig(codec.NewProtoCodec(reg), []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})
	decoded, err := decCfg.TxDecoder()(signed)
	if err != nil {
		t.Fatal(err)
	}
	msgs := decoded.GetMsgs()
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d", len(msgs))
	}
	send := msgs[0].(*btypes.MsgSend)
	if send.Amount.AmountOf("uatom").Int64() != 123 || send.FromAddress != c.address.String() {
		t.Fatalf("msg = %+v", send)
	}
	memoTx, ok := decoded.(sdk.TxWithMemo)
	if !ok || memoTx.GetMemo() != "hello" {
		t.Fatal("memo not set")
	}
	sigTx, ok := decoded.(interface {
		GetSignaturesV2() ([]signingtypes.SignatureV2, error)
	})
	if !ok {
		t.Fatal("decoded tx has no signatures")
	}
	sigs, err := sigTx.GetSignaturesV2()
	if err != nil || len(sigs) != 1 || sigs[0].Sequence != 11 {
		t.Fatalf("sigs = %+v %v", sigs, err)
	}

	f.balanceErr = status.Error(codes.Internal, "x")
	if _, err = c.SignTx(SimTx{Coin: common.NewCoin(common.ATOMAsset, cosmos.NewUint(1))}); err == nil || !strings.Contains(err.Error(), "fail to get account") {
		t.Fatalf("expected account error, got %v", err)
	}
}

func TestBroadcastTxErrorsCov(t *testing.T) {
	c, f := newClientCov(t, common.GAIAChain)

	f.broadcastCode = 5
	if _, err := c.BroadcastTx([]byte{1}); err == nil || !strings.Contains(err.Error(), "code 5") {
		t.Fatalf("expected code error, got %v", err)
	}
	f.broadcastErr = status.Error(codes.Unavailable, "down")
	if _, err := c.BroadcastTx([]byte{2}); err == nil || !strings.Contains(err.Error(), "fail to broadcast") {
		t.Fatalf("expected broadcast error, got %v", err)
	}
	if len(f.broadcasts) != 2 || f.broadcasts[1][0] != 2 {
		t.Fatalf("broadcasts = %v", f.broadcasts)
	}
}

// BroadcastTx polls GetTx after a fixed 500ms sleep; one poll is enough here.
func TestBroadcastTxWaitsForInclusionCov(t *testing.T) {
	c, f := newClientCov(t, common.GAIAChain)
	hash, err := c.BroadcastTx([]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	if hash != "ABC" || len(f.getTxHashes) != 1 || f.getTxHashes[0] != "ABC" {
		t.Fatalf("hash %s polls %v", hash, f.getTxHashes)
	}
}
