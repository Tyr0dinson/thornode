package abci

import (
	"context"
	"errors"
	"testing"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func newRegistryCov() codectypes.InterfaceRegistry {
	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	return registry
}

func protoSizeCov(tx []byte) int64 {
	return cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{tx})
}

func testCtxCov() sdk.Context {
	return sdk.Context{}.WithLogger(log.NewNopLogger()).WithBlockHeight(10)
}

func TestPrepareProposalNoInjectTxs(t *testing.T) {
	txs := [][]byte{make([]byte, 10), make([]byte, 200), make([]byte, 30)}
	const maxBytes = int64(1000)

	var seenMax int64
	next := func(_ sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		seenMax = req.MaxTxBytes
		return &abci.ResponsePrepareProposal{Txs: req.Txs}, nil
	}
	h := NewProposalHandler(nil, nil, newRegistryCov(), next, nil)
	require.NotNil(t, h.decoder)

	resp, err := h.PrepareProposal(testCtxCov(), &abci.RequestPrepareProposal{Txs: txs, MaxTxBytes: maxBytes})
	require.NoError(t, err)

	// upstream handler sees the limit reduced by the proto framing overhead
	var overhead int64
	for _, tx := range txs {
		overhead += protoSizeCov(tx) - int64(len(tx))
	}
	require.Equal(t, maxBytes-overhead, seenMax)
	require.Equal(t, txs, resp.Txs)
}

func TestPrepareProposalDropsOversizedTxs(t *testing.T) {
	small := make([]byte, 10)
	big := make([]byte, 100)
	maxBytes := protoSizeCov(small) + protoSizeCov(small) + 5

	// upstream ignores the limit and returns too many txs
	next := func(_ sdk.Context, _ *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		return &abci.ResponsePrepareProposal{Txs: [][]byte{small, big, small}}, nil
	}
	h := NewProposalHandler(nil, nil, newRegistryCov(), next, nil)
	resp, err := h.PrepareProposal(testCtxCov(), &abci.RequestPrepareProposal{MaxTxBytes: maxBytes})
	require.NoError(t, err)
	require.Equal(t, [][]byte{small, small}, resp.Txs)
}

func TestPrepareProposalUpstreamError(t *testing.T) {
	next := func(_ sdk.Context, _ *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		return nil, errors.New("boom")
	}
	h := NewProposalHandler(nil, nil, newRegistryCov(), next, nil)
	resp, err := h.PrepareProposal(testCtxCov(), &abci.RequestPrepareProposal{MaxTxBytes: 100})
	require.ErrorContains(t, err, "boom")
	require.Nil(t, resp)
}

func TestPrepareAndProcessProposalWithInjectTxs(t *testing.T) {
	registry := newRegistryCov()
	cdc := codec.NewProtoCodec(registry)
	eb := ebifrost.NewEnshrinedBifrost(cdc, log.NewNopLogger(), ebifrost.EBifrostConfig{Enable: true, Address: "localhost:0"})
	require.NotNil(t, eb)

	nf := &common.QuorumNetworkFee{
		NetworkFee: &common.NetworkFee{Chain: common.BTCChain, Height: 100, TransactionSize: 250, TransactionRate: 10},
		Attestations: []*common.Attestation{
			{PubKey: []byte("pk1"), Signature: []byte("sig1")},
		},
	}
	_, err := eb.SendQuorumNetworkFee(context.Background(), nf)
	require.NoError(t, err)

	userTx := make([]byte, 20)
	const maxBytes = int64(100000)
	var seenMax int64
	next := func(_ sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		seenMax = req.MaxTxBytes
		return &abci.ResponsePrepareProposal{Txs: req.Txs}, nil
	}

	processed := 0
	nextProcess := func(_ sdk.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
		processed = len(req.Txs)
		return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil
	}

	h := NewProposalHandler(nil, eb, registry, next, nextProcess)
	resp, err := h.PrepareProposal(testCtxCov(), &abci.RequestPrepareProposal{Txs: [][]byte{userTx}, MaxTxBytes: maxBytes})
	require.NoError(t, err)
	require.Len(t, resp.Txs, 2)
	injected := resp.Txs[0]
	require.Equal(t, userTx, resp.Txs[1])

	// upstream limit is reduced by the injected tx size and the user tx framing
	expected := maxBytes - protoSizeCov(injected) - (protoSizeCov(userTx) - int64(len(userTx)))
	require.Equal(t, expected, seenMax)

	// the injected tx decodes through the ebifrost-aware decoder
	pResp, err := h.ProcessProposal(testCtxCov(), &abci.RequestProcessProposal{Txs: [][]byte{injected}})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, pResp.Status)
	require.Equal(t, 1, processed)

	// undecodable bytes abort before the next handler runs
	processed = -1
	pResp, err = h.ProcessProposal(testCtxCov(), &abci.RequestProcessProposal{Txs: [][]byte{injected, {0xff, 0xff, 0xff}}})
	require.Error(t, err)
	require.Nil(t, pResp)
	require.Equal(t, -1, processed)
}
