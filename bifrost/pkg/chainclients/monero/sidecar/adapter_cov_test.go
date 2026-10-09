package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/config"
)

func newCovClient(t *testing.T, cfg config.BifrostChainConfiguration, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClientWithEndpoint(cfg, srv.URL)
	require.NoError(t, err)
	return c
}

func TestFrostAdapterNotInitializedCov(t *testing.T) {
	t.Parallel()

	for _, a := range []*FrostAdapter{nil, NewFrostAdapter(nil)} {
		_, err := a.FrostSignPrepare(tssp.FrostSidecarPrepareReq{})
		require.EqualError(t, err, "frost adapter is not initialized")
		_, err = a.FrostSignPartial(tssp.FrostSidecarPartialReq{})
		require.EqualError(t, err, "frost adapter is not initialized")
		_, err = a.FrostSignCombine(tssp.FrostSidecarCombineReq{})
		require.EqualError(t, err, "frost adapter is not initialized")
		_, err = a.FrostDKGRound1(tssp.FrostSidecarDKGRound1Req{})
		require.EqualError(t, err, "frost adapter is not initialized")
		_, err = a.FrostDKGRound2(tssp.FrostSidecarDKGRound2Req{})
		require.EqualError(t, err, "frost adapter is not initialized")
		_, err = a.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{})
		require.EqualError(t, err, "frost adapter is not initialized")
		_, err = a.FrostDKGFinalize(tssp.FrostSidecarDKGFinalizeReq{})
		require.EqualError(t, err, "frost adapter is not initialized")
		_, err = a.GetChainHeight()
		require.EqualError(t, err, "frost adapter is not initialized")
	}
}

func TestFrostAdapterNilContextDefaultsCov(t *testing.T) {
	t.Parallel()
	//nolint:staticcheck // deliberately exercising the nil-context guard
	a := NewFrostAdapterWithContext(nil, nil)
	require.NotNil(t, a.ctx)
	require.Equal(t, context.Background(), a.ctx)
}

func TestFrostAdapterDKGAndHeightCov(t *testing.T) {
	t.Parallel()

	var gotR1 DKGRound1Req
	var gotR3 DKGRound3Req
	var gotPrepare SignPrepareReq
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/dkg/round1":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotR1))
			_ = json.NewEncoder(w).Encode(DKGRound1Resp{Commitments: "cm"})
		case "/v1/monero/dkg/round3":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotR3))
			_ = json.NewEncoder(w).Encode(DKGRound3Resp{Address: "addr", GroupKey: "gk"})
		case "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(ChainHeightResp{Height: 77})
		case "/v1/monero/sign/prepare":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPrepare))
			_ = json.NewEncoder(w).Encode(SignPrepareResp{SessionID: "s", Preprocess: "p"})
		case "/v1/monero/sign/combine":
			_ = json.NewEncoder(w).Encode(SignCombineResp{
				TxHash: "th", TxKey: "tk", FeeAtomic: 5,
				SpentOutputRefs: []SpentRef{{TxHash: "in", OutputIndex: 2, KeyImage: "ki", SpendTxHash: "th"}},
			})
		default:
			http.Error(w, "nope", http.StatusInternalServerError)
		}
	})
	a := NewFrostAdapter(c)

	r1, err := a.FrostDKGRound1(tssp.FrostSidecarDKGRound1Req{
		VaultPubKey: "vpk", Threshold: 2, N: 3, ParticipantIndex: 1, Context: "ctx", SessionID: "sess",
	})
	require.NoError(t, err)
	require.Equal(t, "cm", r1.Commitments)
	require.Equal(t, DKGRound1Req{VaultPubKey: "vpk", Threshold: 2, N: 3, ParticipantIndex: 1, Context: "ctx", SessionID: "sess"}, gotR1)

	r3, err := a.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{SessionID: "sess", AllShares: map[string]string{"1": "x"}})
	require.NoError(t, err)
	require.Equal(t, "addr", r3.Address)
	require.Equal(t, "gk", r3.GroupKey)
	require.Equal(t, "sess", gotR3.SessionID)
	require.Equal(t, map[string]string{"1": "x"}, gotR3.AllShares)

	h, err := a.GetChainHeight()
	require.NoError(t, err)
	require.EqualValues(t, 77, h)

	// RequiredInputs are translated field-for-field.
	_, err = a.FrostSignPrepare(tssp.FrostSidecarPrepareReq{
		RequiredInputs: []tssp.FrostSpentOutputRef{{TxHash: "a", OutputIndex: 1, KeyImage: "k", SpendTxHash: "s"}},
	})
	require.NoError(t, err)
	require.Equal(t, []SpentRef{{TxHash: "a", OutputIndex: 1, KeyImage: "k", SpendTxHash: "s"}}, gotPrepare.RequiredInputs)

	comb, err := a.FrostSignCombine(tssp.FrostSidecarCombineReq{MsgID: "m", SessionID: "s"})
	require.NoError(t, err)
	require.Equal(t, uint64(5), comb.FeeAtomic)
	require.Equal(t, []tssp.FrostSpentOutputRef{{TxHash: "in", OutputIndex: 2, KeyImage: "ki", SpendTxHash: "th"}}, comb.SpentOutputRefs)

	// Errors from every remaining endpoint propagate.
	_, err = a.FrostDKGFinalize(tssp.FrostSidecarDKGFinalizeReq{SessionID: "s"})
	require.ErrorContains(t, err, "dkg finalize")
	_, err = a.FrostSignPartial(tssp.FrostSidecarPartialReq{SessionID: "s"})
	require.ErrorContains(t, err, "sign partial")
	_, err = a.FrostDKGRound2(tssp.FrostSidecarDKGRound2Req{SessionID: "s"})
	require.ErrorContains(t, err, "dkg round2")
}

func TestFrostAdapterErrorPathsCov(t *testing.T) {
	t.Parallel()
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	})
	a := NewFrostAdapter(c)

	_, err := a.FrostSignPrepare(tssp.FrostSidecarPrepareReq{})
	require.ErrorContains(t, err, "sign prepare")
	_, err = a.FrostSignCombine(tssp.FrostSidecarCombineReq{})
	require.ErrorContains(t, err, "sign combine")
	_, err = a.FrostDKGRound1(tssp.FrostSidecarDKGRound1Req{})
	require.ErrorContains(t, err, "dkg round1")
	_, err = a.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{})
	require.ErrorContains(t, err, "dkg round3")
	_, err = a.GetChainHeight()
	require.ErrorContains(t, err, "chain-height")
}

func TestSpentRefsConversionCov(t *testing.T) {
	t.Parallel()
	require.Nil(t, spentRefsFromTSS(nil))
	require.Nil(t, spentRefsToTSS(nil))
	in := []tssp.FrostSpentOutputRef{{TxHash: "a", OutputIndex: 9, KeyImage: "b", SpendTxHash: "c"}}
	require.Equal(t, in, spentRefsToTSS(spentRefsFromTSS(in)))
}
