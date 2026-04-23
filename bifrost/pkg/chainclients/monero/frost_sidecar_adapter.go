package monero

import (
	"fmt"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
)

// frostSignerAdapter adapts the Monero sidecar client to go-tss sidecar interfaces.
type frostSignerAdapter struct {
	signer *FrostSigner
}

func newFrostSignerAdapter(signer *FrostSigner) *frostSignerAdapter {
	return &frostSignerAdapter{signer: signer}
}

func (a *frostSignerAdapter) FrostSignPrepare(req tssp.FrostSidecarPrepareReq) (tssp.FrostSidecarPrepareResp, error) {
	if a == nil || a.signer == nil {
		return tssp.FrostSidecarPrepareResp{}, fmt.Errorf("frost signer adapter is not initialized")
	}
	resp, err := a.signer.FrostSignPrepare(xmrFrostSignPrepareRequest{
		MsgID:          req.MsgID,
		VaultPubKey:    req.VaultPubKey,
		ToAddress:      req.ToAddress,
		AmountAtomic:   req.AmountAtomic,
		Memo:           req.Memo,
		FeeRate:        req.FeeRate,
		DecoyContext:   req.DecoyContext,
		RequiredInputs: frostOutputRefsToSigner(req.RequiredInputs),
		MaxInputs:      req.MaxInputs,
		DustThreshold:  req.DustThreshold,
	})
	if err != nil {
		return tssp.FrostSidecarPrepareResp{}, err
	}
	return tssp.FrostSidecarPrepareResp{
		SessionID:  resp.SessionID,
		Preprocess: resp.Preprocess,
	}, nil
}

func frostOutputRefsToSigner(refs []tssp.FrostSpentOutputRef) []xmrFrostOutputRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]xmrFrostOutputRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, xmrFrostOutputRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: ref.SpendTxHash,
		})
	}
	return out
}

func (a *frostSignerAdapter) FrostSignPartial(req tssp.FrostSidecarPartialReq) (tssp.FrostSidecarPartialResp, error) {
	if a == nil || a.signer == nil {
		return tssp.FrostSidecarPartialResp{}, fmt.Errorf("frost signer adapter is not initialized")
	}
	resp, err := a.signer.FrostSignPartial(xmrFrostSignPartialRequest{
		MsgID:           req.MsgID,
		SessionID:       req.SessionID,
		AllPreprocesses: req.AllPreprocesses,
	})
	if err != nil {
		return tssp.FrostSidecarPartialResp{}, err
	}
	return tssp.FrostSidecarPartialResp{
		SignatureShare: resp.SignatureShare,
	}, nil
}

func (a *frostSignerAdapter) FrostSignCombine(req tssp.FrostSidecarCombineReq) (tssp.FrostSidecarCombineResp, error) {
	if a == nil || a.signer == nil {
		return tssp.FrostSidecarCombineResp{}, fmt.Errorf("frost signer adapter is not initialized")
	}
	resp, err := a.signer.FrostSignCombine(xmrFrostSignCombineRequest{
		MsgID:     req.MsgID,
		SessionID: req.SessionID,
		AllShares: req.AllShares,
	})
	if err != nil {
		return tssp.FrostSidecarCombineResp{}, err
	}
	var spentRefs []tssp.FrostSpentOutputRef
	for _, ref := range resp.SpentOutputRefs {
		spentRefs = append(spentRefs, tssp.FrostSpentOutputRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: ref.SpendTxHash,
		})
	}
	return tssp.FrostSidecarCombineResp{
		TxHash:          resp.TxHash,
		TxHex:           resp.TxHex,
		FeeAtomic:       resp.FeeAtomic,
		SpentOutputRefs: spentRefs,
	}, nil
}

func (a *frostSignerAdapter) FrostDKGRound1(req tssp.FrostSidecarDKGRound1Req) (tssp.FrostSidecarDKGRound1Resp, error) {
	if a == nil || a.signer == nil {
		return tssp.FrostSidecarDKGRound1Resp{}, fmt.Errorf("frost signer adapter is not initialized")
	}
	resp, err := a.signer.FrostDKGRound1(xmrFrostDKGRound1Request{
		VaultPubKey:      req.VaultPubKey,
		Threshold:        req.Threshold,
		N:                req.N,
		ParticipantIndex: req.ParticipantIndex,
		Context:          req.Context,
		SessionID:        req.SessionID,
	})
	if err != nil {
		return tssp.FrostSidecarDKGRound1Resp{}, err
	}
	return tssp.FrostSidecarDKGRound1Resp{Commitments: resp.Commitments}, nil
}

func (a *frostSignerAdapter) FrostDKGRound2(req tssp.FrostSidecarDKGRound2Req) (tssp.FrostSidecarDKGRound2Resp, error) {
	if a == nil || a.signer == nil {
		return tssp.FrostSidecarDKGRound2Resp{}, fmt.Errorf("frost signer adapter is not initialized")
	}
	resp, err := a.signer.FrostDKGRound2(xmrFrostDKGRound2Request{
		SessionID:      req.SessionID,
		AllCommitments: req.AllCommitments,
	})
	if err != nil {
		return tssp.FrostSidecarDKGRound2Resp{}, err
	}
	return tssp.FrostSidecarDKGRound2Resp{EncryptedShares: resp.EncryptedShares}, nil
}

func (a *frostSignerAdapter) FrostDKGRound3(req tssp.FrostSidecarDKGRound3Req) (tssp.FrostSidecarDKGRound3Resp, error) {
	if a == nil || a.signer == nil {
		return tssp.FrostSidecarDKGRound3Resp{}, fmt.Errorf("frost signer adapter is not initialized")
	}
	resp, err := a.signer.FrostDKGRound3(xmrFrostDKGRound3Request{
		SessionID: req.SessionID,
		AllShares: req.AllShares,
	})
	if err != nil {
		return tssp.FrostSidecarDKGRound3Resp{}, err
	}
	return tssp.FrostSidecarDKGRound3Resp{
		Address:  resp.Address,
		GroupKey: resp.GroupKey,
	}, nil
}

func (a *frostSignerAdapter) FrostDKGFinalize(req tssp.FrostSidecarDKGFinalizeReq) (tssp.FrostSidecarDKGFinalizeResp, error) {
	if a == nil || a.signer == nil {
		return tssp.FrostSidecarDKGFinalizeResp{}, fmt.Errorf("frost signer adapter is not initialized")
	}
	resp, err := a.signer.FrostDKGFinalize(xmrFrostDKGFinalizeRequest{
		SessionID: req.SessionID,
		GroupKey:  req.GroupKey,
		Address:   req.Address,
	})
	if err != nil {
		return tssp.FrostSidecarDKGFinalizeResp{}, err
	}
	return tssp.FrostSidecarDKGFinalizeResp{Success: resp.Success, KeyBytesHex: resp.KeyBytesHex}, nil
}

func (a *frostSignerAdapter) GetChainHeight() (int64, error) {
	if a == nil || a.signer == nil {
		return 0, fmt.Errorf("frost signer adapter is not initialized")
	}
	return a.signer.GetChainHeight()
}
