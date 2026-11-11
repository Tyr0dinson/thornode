package cosmos

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	btypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/gaia/liquidtypes"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestIsCosmosAccountNotFoundError(t *testing.T) {
	t.Run("matches grpc not found", func(t *testing.T) {
		err := status.Error(codes.NotFound, "account cosmos1abc not found")
		if !isCosmosAccountNotFoundError(err) {
			t.Fatalf("expected grpc not found to be treated as missing account")
		}
	})

	t.Run("ignores unrelated errors", func(t *testing.T) {
		err := errors.New("rpc unavailable")
		if isCosmosAccountNotFoundError(err) {
			t.Fatalf("expected unrelated error to remain fatal")
		}
	})
}

// newTestClient returns a client with a fresh key and no connection, enough for
// buildSignedTx, which makes no network calls.
func newTestClient(t *testing.T, chain common.Chain, denom, chainID string) *Client {
	t.Helper()

	privKey := secp256k1.GenPrivKey()
	pk, err := cryptocodec.ToCmtPubKeyInterface(privKey.PubKey())
	if err != nil {
		t.Fatalf("tm pub key: %v", err)
	}
	pubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		t.Fatalf("pub key: %v", err)
	}
	address, err := pubKey.GetAddress(chain)
	if err != nil {
		t.Fatalf("address: %v", err)
	}
	return &Client{
		chain:    chain,
		txConfig: newTxConfig(),
		privKey:  privKey,
		pubKey:   pubKey,
		address:  address,
		denom:    denom,
		chainId:  chainID,
	}
}

var testAccount = &common.Account{Sequence: 3, AccountNumber: 7}

func testTransfer(t *testing.T, chain common.Chain) SimTx {
	t.Helper()
	to, err := sdk.Bech32ifyAddressBytes("cosmos", bytes.Repeat([]byte{0x1}, 20))
	if err != nil {
		t.Fatalf("to address: %v", err)
	}
	return SimTx{
		Chain:     chain,
		ToAddress: common.Address(to),
		Coin:      common.NewCoin(chain.GetGasAsset(), sdkmath.NewUint(5_0000_0000)), // 5 ATOM in 1e8
		Memo:      "=:ETH.ETH:0x0000000000000000000000000000000000000001",
	}
}

// decodeSigned decodes signed tx bytes and checks the single signature against
// the sign bytes for the given account state.
func decodeSigned(t *testing.T, c *Client, signed []byte, account *common.Account) authsigning.Tx {
	t.Helper()

	// The client's own registry holds only the messages it sends. Decoding a
	// signed tx also needs the public key types, as a node's registry has.
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	btypes.RegisterInterfaces(registry)
	liquidtypes.RegisterInterfaces(registry)
	nodeConfig := tx.NewTxConfig(codec.NewProtoCodec(registry), []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})

	decoded, err := nodeConfig.TxDecoder()(signed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sigTx, ok := decoded.(authsigning.Tx)
	if !ok {
		t.Fatalf("decoded tx is %T", decoded)
	}

	sigs, err := sigTx.GetSignaturesV2()
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}
	if sigs[0].Sequence != uint64(account.Sequence) {
		t.Fatalf("expected sequence %d, got %d", account.Sequence, sigs[0].Sequence)
	}
	single, ok := sigs[0].Data.(*signingtypes.SingleSignatureData)
	if !ok {
		t.Fatalf("signature data is %T", sigs[0].Data)
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		context.Background(), nodeConfig.SignModeHandler(), signingtypes.SignMode_SIGN_MODE_DIRECT,
		authsigning.SignerData{
			ChainID:       c.chainId,
			AccountNumber: uint64(account.AccountNumber),
			Sequence:      uint64(account.Sequence),
		}, sigTx,
	)
	if err != nil {
		t.Fatalf("sign bytes: %v", err)
	}
	if !c.privKey.PubKey().VerifySignature(signBytes, single.Signature) {
		t.Fatalf("signature does not verify over the decoded tx")
	}
	return sigTx
}

func checkSend(t *testing.T, c *Client, msg sdk.Msg, transfer SimTx) {
	t.Helper()
	send, ok := msg.(*btypes.MsgSend)
	if !ok {
		t.Fatalf("expected MsgSend, got %T", msg)
	}
	if send.FromAddress != c.address.String() || send.ToAddress != transfer.ToAddress.String() {
		t.Fatalf("unexpected send %s -> %s", send.FromAddress, send.ToAddress)
	}
	want := sdk.NewCoins(sdk.NewCoin(c.denom, sdkmath.NewInt(5_000_000))) // 1e8 -> 1e6
	if !send.Amount.Equal(want) {
		t.Fatalf("expected amount %s, got %s", want, send.Amount)
	}
}

func TestBuildSignedTxSingleSend(t *testing.T) {
	c := newTestClient(t, common.GAIAChain, "uatom", "localgaia")
	transfer := testTransfer(t, common.GAIAChain)

	signed, err := c.buildSignedTx(transfer, testAccount)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	sigTx := decodeSigned(t, c, signed, testAccount)

	msgs := sigTx.GetMsgs()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	checkSend(t, c, msgs[0], transfer)
	if sigTx.GetMemo() != transfer.Memo {
		t.Fatalf("unexpected memo %q", sigTx.GetMemo())
	}
	if sigTx.GetGas() != 150_000 {
		t.Fatalf("expected gas 150000, got %d", sigTx.GetGas())
	}
	if fee := sigTx.GetFee(); !fee.Equal(sdk.NewCoins(sdk.NewCoin("uatom", sdkmath.NewInt(2000)))) {
		t.Fatalf("expected fee 2000uatom, got %s", fee)
	}
}

func TestWithLiquidBundleSignsLiquidMessageBeforeSend(t *testing.T) {
	c := newTestClient(t, common.GAIAChain, "uatom", "localgaia")
	transfer := testTransfer(t, common.GAIAChain)

	wrapped, err := WithLiquidBundle(c)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	bundle, ok := wrapped.(*liquidBundleClient)
	if !ok {
		t.Fatalf("wrapped client is %T", wrapped)
	}
	if bundle.Client != c {
		t.Fatalf("wrapper does not embed the original client")
	}

	// the message the wrapper's SignTx puts ahead of the MsgSend
	signed, err := c.buildSignedTx(transfer, testAccount, bundle.leading()...)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	sigTx := decodeSigned(t, c, signed, testAccount)

	msgs := sigTx.GetMsgs()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	liquid, ok := msgs[0].(*liquidtypes.MsgWithdrawAllTokenizeShareRecordReward)
	if !ok {
		t.Fatalf("expected the liquid message first, got %T", msgs[0])
	}
	if liquid.OwnerAddress != c.address.String() {
		t.Fatalf("expected owner %s, got %s", c.address, liquid.OwnerAddress)
	}
	checkSend(t, c, msgs[1], transfer)
	if sigTx.GetMemo() != transfer.Memo {
		t.Fatalf("unexpected memo %q", sigTx.GetMemo())
	}

	// gas and fee scale with the message count: same price per gas as a lone send
	if sigTx.GetGas() != 300_000 {
		t.Fatalf("expected gas 300000, got %d", sigTx.GetGas())
	}
	if fee := sigTx.GetFee(); !fee.Equal(sdk.NewCoins(sdk.NewCoin("uatom", sdkmath.NewInt(4000)))) {
		t.Fatalf("expected fee 4000uatom, got %s", fee)
	}
}

// otherClient is a LiteChainClient that is not this package's Client.
type otherClient struct{}

func (otherClient) GetAccount(*common.PubKey) (*common.Account, error) { return nil, nil }
func (otherClient) SignTx(SimTx) ([]byte, error)                       { return nil, nil }
func (otherClient) BroadcastTx([]byte) (string, error)                 { return "", nil }

func TestWithLiquidBundleRejectsOtherClients(t *testing.T) {
	// a Cosmos client for a chain without x/liquid
	noble := newTestClient(t, common.NOBLEChain, "uusdc", "localnoble")
	if _, err := WithLiquidBundle(noble); err == nil || !strings.Contains(err.Error(), "requires a GAIA client") {
		t.Fatalf("expected NOBLE client to be rejected, got %v", err)
	}

	// another chain's client type
	if _, err := WithLiquidBundle(otherClient{}); err == nil || !strings.Contains(err.Error(), "requires a GAIA client") {
		t.Fatalf("expected foreign client type to be rejected, got %v", err)
	}
}
