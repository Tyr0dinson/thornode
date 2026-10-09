package thorchain

import (
	"math"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	kmultisig "github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256r1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type AnteCovCSuite struct{}

var _ = Suite(&AnteCovCSuite{})

// covCSigTx is a SigVerifiableTx with configurable signatures and signer errors.
type covCSigTx struct {
	fakeSigVerifiableTx
	sigs       []signing.SignatureV2
	sigErr     error
	signersErr error
}

func (t covCSigTx) GetSignaturesV2() ([]signing.SignatureV2, error) { return t.sigs, t.sigErr }

func (t covCSigTx) GetSigners() ([][]byte, error) {
	if t.signersErr != nil {
		return nil, t.signersErr
	}
	return t.fakeSigVerifiableTx.GetSigners()
}

// covCPlainTx is not a SigVerifiableTx.
type covCPlainTx struct {
	sdk.Tx
	msgs []cosmos.Msg
}

func (t covCPlainTx) GetMsgs() []cosmos.Msg { return t.msgs }

func covCNextNotCalled(c *C) sdk.AnteHandler {
	return func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		c.Error("next ante handler must not be reached")
		return ctx, nil
	}
}

func (s *AnteCovCSuite) TestInfiniteGasDecoratorCovC(c *C) {
	ctx, k := setupKeeperForTest(c)
	d := NewGasDecorator(k)
	reached := false
	_, err := d.AnteHandle(ctx, covCPlainTx{}, false, func(inner sdk.Context, _ sdk.Tx, simulate bool) (sdk.Context, error) {
		reached = true
		c.Check(simulate, Equals, false)
		c.Check(inner.GasMeter().Limit(), Equals, uint64(math.MaxUint64))
		inner.GasMeter().ConsumeGas(math.MaxUint64/2, "test")
		c.Check(inner.GasMeter().IsOutOfGas(), Equals, false)
		return inner, nil
	})
	c.Assert(err, IsNil)
	c.Check(reached, Equals, true)
}

func (s *AnteCovCSuite) TestAnteHandleRejectionsCovC(c *C) {
	ctx, k := setupKeeperForTest(c)
	ad := NewAnteDecorator(k)
	signer := GetRandomBech32Addr()
	base := fakeSigVerifiableTx{signers: [][]byte{signer}}

	// two deposits in one tx
	tx := base
	tx.msgs = []cosmos.Msg{&types.MsgDeposit{Signer: signer}, &types.MsgDeposit{Signer: signer}}
	_, err := ad.AnteHandle(ctx, tx, false, covCNextNotCalled(c))
	c.Check(err, ErrorMatches, ".*only one deposit msg per tx.*")

	// a MsgSend to the thorchain module counts as a deposit too
	moduleAddr := k.GetModuleAccAddress(ModuleName)
	tx.msgs = []cosmos.Msg{
		&types.MsgDeposit{Signer: signer},
		&types.MsgSend{FromAddress: signer, ToAddress: moduleAddr},
	}
	_, err = ad.AnteHandle(ctx, tx, false, covCNextNotCalled(c))
	c.Check(err, ErrorMatches, ".*only one deposit msg per tx.*")
	tx.msgs = []cosmos.Msg{
		&types.MsgDeposit{Signer: signer},
		&banktypes.MsgSend{FromAddress: signer.String(), ToAddress: moduleAddr.String()},
	}
	_, err = ad.AnteHandle(ctx, tx, false, covCNextNotCalled(c))
	c.Check(err, ErrorMatches, ".*only one deposit msg per tx.*")

	// duplicate THORName operations
	tx.msgs = []cosmos.Msg{
		&types.MsgManageTHORName{Name: "bob", Signer: signer},
		&types.MsgManageTHORName{Name: "bob", Signer: signer},
	}
	_, err = ad.AnteHandle(ctx, tx, false, covCNextNotCalled(c))
	c.Check(err, ErrorMatches, ".*duplicate THORName operation in same tx: bob.*")

	// not a sig-verifiable tx
	_, err = ad.AnteHandle(ctx, covCPlainTx{msgs: []cosmos.Msg{&types.MsgMimir{}}}, false, covCNextNotCalled(c))
	c.Check(err, ErrorMatches, ".*invalid transaction type.*")
	_, err = txSigners(covCPlainTx{})
	c.Check(err, ErrorMatches, ".*invalid transaction type.*")

	// signatures cannot be read
	sigTx := covCSigTx{fakeSigVerifiableTx: base, sigErr: errKaboom}
	sigTx.msgs = []cosmos.Msg{&types.MsgMimir{}}
	_, err = ad.AnteHandle(ctx, sigTx, false, covCNextNotCalled(c))
	c.Check(err, Equals, errKaboom)

	// secp256r1 signer
	r1, err := secp256r1.GenPrivKey()
	c.Assert(err, IsNil)
	sigTx = covCSigTx{fakeSigVerifiableTx: base, sigs: []signing.SignatureV2{{PubKey: r1.PubKey()}}}
	sigTx.msgs = []cosmos.Msg{&types.MsgMimir{}}
	_, err = ad.AnteHandle(ctx, sigTx, false, covCNextNotCalled(c))
	c.Check(err, ErrorMatches, ".*secp256r1 keys not allowed.*")

	// multisig containing a secp256r1 key
	k1 := secp256k1.GenPrivKey().PubKey()
	multi := kmultisig.NewLegacyAminoPubKey(1, []cryptotypes.PubKey{k1, r1.PubKey()})
	sigTx.sigs = []signing.SignatureV2{{PubKey: multi}}
	_, err = ad.AnteHandle(ctx, sigTx, false, covCNextNotCalled(c))
	c.Check(err, ErrorMatches, ".*secp256r1 keys not allowed.*")

	// multisig made only of secp256k1 keys is fine; signer lookup failure aborts
	multiOK := kmultisig.NewLegacyAminoPubKey(1, []cryptotypes.PubKey{k1, secp256k1.GenPrivKey().PubKey()})
	c.Check(ad.rejectInvalidSigners(covCSigTx{sigs: []signing.SignatureV2{{PubKey: multiOK}, {PubKey: k1}}}), IsNil)
	sigTx = covCSigTx{fakeSigVerifiableTx: base, sigs: []signing.SignatureV2{{PubKey: k1}}, signersErr: errKaboom}
	sigTx.msgs = []cosmos.Msg{&types.MsgMimir{}}
	_, err = ad.AnteHandle(ctx, sigTx, false, covCNextNotCalled(c))
	c.Check(err, Equals, errKaboom)

	// message-level rejection (unknown message type) stops the chain
	tx = base
	tx.msgs = []cosmos.Msg{&banktypes.MsgMultiSend{}}
	_, err = ad.AnteHandle(ctx, tx, false, covCNextNotCalled(c))
	c.Check(err, ErrorMatches, ".*invalid message type.*")
}

func (s *AnteCovCSuite) TestAnteHandleMessageRoutingCovC(c *C) {
	ctx, k := setupKeeperForTest(c)
	ad := NewAnteDecorator(k)
	version := GetCurrentVersion()
	stranger := GetRandomBech32Addr()

	// node-only messages from a non-node signer are rejected
	rejected := map[string]cosmos.Msg{
		"ban":            &types.MsgBan{Signer: stranger, NodeAddress: GetRandomBech32Addr()},
		"errata":         &types.MsgErrataTx{Signer: stranger, TxID: GetRandomTxHash(), Chain: common.BTCChain},
		"networkfee":     &types.MsgNetworkFee{Signer: stranger, Chain: common.BTCChain},
		"observedin":     &types.MsgObservedTxIn{Signer: stranger},
		"observedout":    &types.MsgObservedTxOut{Signer: stranger},
		"solvency":       &types.MsgSolvency{Signer: stranger, Chain: common.BTCChain},
		"solnonce":       &types.MsgSolNonceAttest{Signer: stranger},
		"tsspool":        &types.MsgTssPool{Signer: stranger},
		"keysignfail":    &types.MsgTssKeysignFail{Signer: stranger},
		"ipaddress":      &types.MsgSetIPAddress{Signer: stranger, IPAddress: "1.2.3.4"},
		"mimir":          &types.MsgMimir{Signer: stranger, Key: "Foo", Value: 1},
		"pausechain":     &types.MsgNodePauseChain{Signer: stranger, Value: 1},
		"nodekeys":       &types.MsgSetNodeKeys{Signer: stranger},
		"version":        &types.MsgSetVersion{Signer: stranger, Version: "1.0.0"},
		"proposeupgrade": &types.MsgProposeUpgrade{Signer: stranger, Name: "x"},
		"approveupgrade": &types.MsgApproveUpgrade{Signer: stranger, Name: "x"},
		"rejectupgrade":  &types.MsgRejectUpgrade{Signer: stranger, Name: "x"},
		// unfunded deposit cannot pay the native fee
		"deposit": &types.MsgDeposit{Signer: stranger},
	}
	for name, msg := range rejected {
		_, err := ad.anteHandleMessage(ctx, version, msg)
		c.Check(err, NotNil, Commentf("%s should be rejected", name))
	}

	// bank sends are rejected while BankSendEnabled is off
	k.SetMimir(ctx, "BankSendEnabled", 0)
	_, err := ad.anteHandleMessage(ctx, version, &banktypes.MsgSend{
		FromAddress: stranger.String(),
		ToAddress:   GetRandomBech32Addr().String(),
	})
	c.Check(err, ErrorMatches, ".*bank sends are disabled.*")

	// wasm messages are passed through untouched
	for _, msg := range []cosmos.Msg{
		&wasmtypes.MsgStoreCode{}, &wasmtypes.MsgInstantiateContract{}, &wasmtypes.MsgInstantiateContract2{},
		&wasmtypes.MsgExecuteContract{}, &wasmtypes.MsgMigrateContract{}, &wasmtypes.MsgSudoContract{},
		&wasmtypes.MsgUpdateAdmin{}, &wasmtypes.MsgClearAdmin{},
	} {
		newCtx, msgErr := ad.anteHandleMessage(ctx, version, msg)
		c.Check(msgErr, IsNil)
		c.Check(newCtx.Priority(), Equals, ctx.Priority())
	}

	// upgrade votes from an active validator pass, paying the fee from bond
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	FundModule(c, ctx, k, BondName, 10*common.One)
	bondBefore := na.Bond
	_, err = ad.anteHandleMessage(ctx, version, &types.MsgApproveUpgrade{Signer: na.NodeAddress, Name: "x"})
	c.Assert(err, IsNil)
	after, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(after.Bond.LT(bondBefore), Equals, true)
}
