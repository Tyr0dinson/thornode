package ebifrost

import (
	"context"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestPostDecoratorConfirmsAllQuorumMsgTypes verifies that a successful inject
// tx carrying every quorum msg type clears the matching enshrined bifrost
// caches, and that the next handler is still invoked.
func TestPostDecoratorConfirmsAllQuorumMsgTypes(t *testing.T) {
	ebs, _ := newTestEBS()
	decorator := NewEnshrineBifrostPostDecorator(ebs)
	require.Equal(t, ebs, decorator.EnshrinedBifrost)

	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	att := createTestAttestation("pk1", "sig1")
	bg := context.Background()

	qtx := createTestQuorumTx(common.BTCChain, "tx1", true, []*common.Attestation{att})
	_, err := ebs.SendQuorumTx(bg, qtx)
	require.NoError(t, err)
	nf := createTestNetworkFee(common.BTCChain, 100, []*common.Attestation{att})
	_, err = ebs.SendQuorumNetworkFee(bg, nf)
	require.NoError(t, err)
	sol := createTestSolvency(common.BTCChain, 100, []*common.Attestation{att})
	_, err = ebs.SendQuorumSolvency(bg, sol)
	require.NoError(t, err)
	errata := createTestErrataTx(common.BTCChain, "err1", []*common.Attestation{att})
	_, err = ebs.SendQuorumErrataTx(bg, errata)
	require.NoError(t, err)
	feeds := []*common.QuorumPriceFeed{{Attestations: []*common.Attestation{att}}}
	_, err = ebs.SendQuorumPriceFeedBatch(bg, createTestPriceFeedBatch(feeds))
	require.NoError(t, err)

	require.Len(t, ebs.quorumTxCache.items, 1)
	require.Len(t, ebs.networkFeeCache.items, 1)
	require.Len(t, ebs.solvencyCache.items, 1)
	require.Len(t, ebs.errataCache.items, 1)
	require.Len(t, ebs.priceFeedCache.items, 1)

	itx := NewInjectTx(cdc, []sdk.Msg{
		&types.MsgObservedTxQuorum{QuoTx: qtx},
		&types.MsgNetworkFeeQuorum{QuoNetFee: nf},
		&types.MsgSolvencyQuorum{QuoSolvency: sol},
		&types.MsgErrataTxQuorum{QuoErrata: errata},
		&types.MsgPriceFeedQuorumBatch{QuoPriceFeeds: feeds},
		&types.MsgMimir{}, // ignored message type
	})

	ctx := createTestSDKContext(100)
	called := false
	next := func(ctx sdk.Context, tx sdk.Tx, simulate, success bool) (sdk.Context, error) {
		called = true
		require.False(t, simulate)
		require.True(t, success)
		return ctx.WithBlockHeight(101), nil
	}

	newCtx, err := decorator.PostHandle(ctx, itx, false, true, next)
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, int64(101), newCtx.BlockHeight())

	require.Len(t, ebs.quorumTxCache.items, 0)
	require.Len(t, ebs.networkFeeCache.items, 0)
	require.Len(t, ebs.solvencyCache.items, 0)
	require.Len(t, ebs.errataCache.items, 0)
	require.Len(t, ebs.priceFeedCache.items, 0)
}

// TestPostDecoratorSkipsConfirmationOnFailure ensures caches are untouched when
// the tx failed, even if it is an inject tx.
func TestPostDecoratorSkipsConfirmationOnFailure(t *testing.T) {
	ebs, _ := newTestEBS()
	decorator := NewEnshrineBifrostPostDecorator(ebs)

	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	att := createTestAttestation("pk1", "sig1")
	nf := createTestNetworkFee(common.BTCChain, 100, []*common.Attestation{att})
	_, err := ebs.SendQuorumNetworkFee(context.Background(), nf)
	require.NoError(t, err)

	itx := NewInjectTx(cdc, []sdk.Msg{&types.MsgNetworkFeeQuorum{QuoNetFee: nf}})
	next := func(ctx sdk.Context, tx sdk.Tx, simulate, success bool) (sdk.Context, error) {
		return ctx, nil
	}
	_, err = decorator.PostHandle(createTestSDKContext(100), itx, false, false, next)
	require.NoError(t, err)
	_, err = decorator.PostHandle(createTestSDKContext(100), itx, true, true, next)
	require.NoError(t, err)
	require.Len(t, ebs.networkFeeCache.items, 1)
}
