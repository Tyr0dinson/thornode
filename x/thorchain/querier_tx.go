// querier_tx.go contains all transaction and TSS-related query handlers.
// This includes queries for transaction voters, stages, status, and TSS operations.

package thorchain

import (
	"errors"
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Transaction Queries ============

func (qs queryServer) queryTxVoters(ctx cosmos.Context, req *types.QueryTxVotersRequest) (*types.QueryObservedTxVoter, error) {
	// First check the observed TxID map to see if it exists
	txid, err := common.NewTxID(req.TxId)
	if err != nil {
		ctx.Logger().Error("fail to parse tx id", "error", err)
		return nil, fmt.Errorf("fail to parse tx id: %w", err)
	}
	// If the TXID is for a Migration transaction from inbound observed transaction,
	// the voter information in the ObservedTxVoter tracker will be under
	// MIGRATExxxx.
	voter, err := qs.mgr.Keeper().GetObservedTxInVoter(ctx, txid)
	if err != nil {
		return nil, fmt.Errorf("fail to get observed tx voter: %w", err)
	}

	if len(voter.TxIDs) == 0 {
		return nil, fmt.Errorf("tx: %s doesn't exist", txid)
	}

	var result types.QueryObservedTxVoter

	voter.TxID = voter.TxIDs[0]
	if voter.Tx.IsEmpty() && !voter.TxID.IsEmpty() {
		// If there's no observed tx in the voter, but there is a TxID,
		// it's possible the tx was a migration in-TxID, and should be
		// looked up from the ObservedTxIn map by in-TxID.
		inTx, err := qs.mgr.Keeper().GetObservedTxIn(ctx, voter.TxID)
		if err == nil && !inTx.Tx.IsEmpty() {
			// Found tx in ObservedTxIn map, so use that instead.
			voter.Tx = inTx.Tx
		}
	}

	result.TxId = voter.TxID.String()
	// For an External Outbound Tx Voter, TxID will be the block height of the outbound.
	// There will be no Tx or Txs, but check OutboundHeight anyway.
	// Note that this outbound voter is distinct from a inbound voter with an empty Tx
	// (which will have a TxID like MIGRATExxxx).
	if voter.Tx.IsEmpty() && voter.OutboundHeight > 0 {
		// No data to get from ObservedTxIn map.
		// Populate result.Tx with only empty fields like the observed Tx chain, to_address, and coins.
		emptyTxs := ObservedTxs{
			ObservedTx{
				Tx: common.Tx{},
			},
		}
		result.Tx = castObservedTx(emptyTxs[0])
		result.Txs = make([]*types.ObservedTx, len(emptyTxs))
		for i := range emptyTxs {
			result.Txs[i] = castObservedTx(emptyTxs[i])
		}
	} else if !voter.Tx.IsEmpty() {
		result.Tx = castObservedTx(voter.Tx)
		result.Txs = make([]*types.ObservedTx, len(voter.Txs))
		for i := range voter.Txs {
			result.Txs[i] = castObservedTx(voter.Txs[i])
		}
	}
	result.Height = voter.Height
	result.UpdatedVault = voter.UpdatedVault
	result.FinalisedHeight = voter.FinalisedHeight
	result.OutboundHeight = voter.OutboundHeight
	// 0 is the gRPC default value, but 0 is also the placeholder value in place of nil in the voter struct,
	// so a 0 may be expected.
	result.Reverted = voter.Reverted
	// Ensure consistency between Tx and Txs[0].
	// If Tx is unset and Txs[0] is set, use Txs[0] as Tx.
	// (This shouldn't happen in current usage.)
	if (result.Tx == nil || result.Tx.BlockHeight == 0) && len(result.Txs) > 0 && result.Txs[0] != nil {
		result.Tx = result.Txs[0]
		result.TxId = result.Tx.TxHash
	}
	// If Txs is unset and Tx is set, use Tx as Txs[0].
	// (This shouldn't happen in current usage.)
	if (len(result.Txs) == 0 || result.Txs[0] == nil || result.Txs[0].BlockHeight == 0) && result.Tx != nil {
		result.Txs = []*types.ObservedTx{result.Tx}
	}
	if voter.Actions != nil {
		result.Actions = make([]*types.TxOutItem, len(voter.Actions))
		for i := range voter.Actions {
			result.Actions[i] = castTxOutItem(voter.Actions[i])
		}
	}

	// analyze-ignore(map-iteration)
	for _, signer := range voter.GetSigners() {
		result.Signers = append(result.Signers, signer.String())
	}

	return &result, nil
}

func (qs queryServer) queryTxStages(ctx cosmos.Context, req *types.QueryTxStagesRequest) (*types.QueryTxStagesResponse, error) {
	// Parse the inbound transaction ID (hash).
	inTxID, err := common.NewTxID(req.TxId)
	if err != nil {
		ctx.Logger().Error("fail to parse tx id", "error", err)
		return nil, fmt.Errorf("fail to parse tx id: %w", err)
	}

	voter, err := qs.mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	if err != nil {
		return nil, fmt.Errorf("fail to get observed tx voter: %w", err)
	}

	return newTxStagesResponse(ctx, qs.mgr, voter), nil
}

func (qs queryServer) queryTxStatus(ctx cosmos.Context, req *types.QueryTxStatusRequest) (*types.QueryTxStatusResponse, error) {
	// Parse the inbound transaction ID (hash).
	inTxID, err := common.NewTxID(req.TxId)
	if err != nil {
		ctx.Logger().Error("fail to parse tx id", "error", err)
		return nil, fmt.Errorf("fail to parse tx id: %w", err)
	}

	voter, err := qs.mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	if err != nil {
		return nil, fmt.Errorf("fail to get observed tx voter: %w", err)
	}

	stages := newTxStagesResponse(ctx, qs.mgr, voter)

	// Return a short-form status of the request.
	// This provides a simple way to check whether the entire request is complete.
	var complete = false
	var pending = true
	if stages.InboundFinalised != nil && stages.OutboundSigned != nil {
		complete = stages.InboundFinalised.Completed && stages.OutboundSigned.Completed
		pending = !complete
	}

	result := types.QueryTxStatusResponse{
		TxId:     inTxID.String(),
		Planned:  stages.SwapFinalised != nil && stages.SwapFinalised.Completed,
		Pending:  pending,
		Complete: complete,
	}
	return &result, nil
}

func (qs queryServer) queryTx(ctx cosmos.Context, req *types.QueryTxRequest) (*types.QueryTxResponse, error) {
	// default page
	page := 0
	// parse page
	if len(req.Page) > 0 {
		var err error
		page, err = strconv.Atoi(req.Page)
		if err != nil {
			ctx.Logger().Error("fail to parse page number", "page", req.Page, "error", err)
			return nil, fmt.Errorf("fail to parse page number: %w", err)
		}
	}

	inTxID, err := common.NewTxID(req.TxId)
	if err != nil {
		ctx.Logger().Error("fail to parse tx id", "error", err)
		return nil, fmt.Errorf("fail to parse tx id: %w", err)
	}

	// TODO: build all this logic into a keeper method
	voter, err := qs.mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	if err != nil {
		return nil, fmt.Errorf("fail to get observed tx voter: %w", err)
	}

	nodeAccounts, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}

	// collect all the signers names from node accounts
	var signers []*types.Signer

	// analyze-ignore(map-iteration)
	for _, item := range voter.GetSigners() {
		signer := types.Signer{Signed: true}
		for _, na := range nodeAccounts {
			if na.NodeAddress.Equals(item) {
				signer.Party = na.PubKeySet.Secp256k1.String()
			}
		}
		signers = append(signers, &signer)
	}

	result := types.QueryTxResponse{
		ObservedTx:      castObservedTx(voter.Tx),
		ConsensusHeight: voter.Height,
		FinalisedHeight: voter.FinalisedHeight,
		OutboundHeight:  voter.OutboundHeight,
		Signers:         signers,
		KeysignInfo:     voter.Tx.GetKeysignInfo(),
		Page:            strconv.Itoa(page),
	}

	// TODO: This could probably be a separate handler.
	// Fetch Keysign Information (Keysign->TssKeysign)
	keysignChan := make(chan map[common.Chain][]TssKeysignMetric, len(voter.OutTxs))
	// Collect the keysign metric concurrently.
	for _, tx := range voter.OutTxs {
		go func(tx *common.Tx) {
			m, err := getTssKeysignMetrics(ctx, qs.mgr, tx.ID)
			if err != nil {
				ctx.Logger().Error("fail to get tss keysign metric", "error", err)
			}
			keysignChan <- m
		}(tx)
	}

	keysignMetrics := make(map[common.Chain][]TssKeysignMetric)
	for i := 0; i < len(voter.OutTxs); i++ {
		m := <-keysignChan
		// analyze-ignore(map-iteration)
		for c, metrics := range m {
			keysignMetrics[c] = append(keysignMetrics[c], metrics...)
		}
	}

	result.KeysignMetrics = make([]*types.KeysignMetrics, 0)
	// analyze-ignore(map-iteration)
	for c, m := range keysignMetrics {
		// TODO: aggregate metrics, 1 keysign can be across multiple chains
		result.KeysignMetrics = append(result.KeysignMetrics, castKeysignMetrics(c, m...)...)
	}

	if !voter.OutboundHeight.IsZero() {
		result.OutTxs = make([]*types.TxResponse, len(voter.OutTxs))
		for i := range voter.OutTxs {
			result.OutTxs[i] = castTxResponse(voter.OutTxs[i], page)
		}
	}

	return &result, nil
}

// ============ TSS Queries ============

func (qs queryServer) queryKeygen(ctx cosmos.Context, req *types.QueryKeygenRequest) (*types.QueryKeygenResponse, error) {
	heightInt64, err := strconv.ParseInt(req.Height, 10, 64)
	if err != nil {
		ctx.Logger().Error("invalid height", "error", err)
		return nil, fmt.Errorf("invalid height: %w", err)
	}
	if heightInt64 > ctx.BlockHeight() {
		return nil, fmt.Errorf("block height not available yet")
	}
	// set a negative height to return the latest keygen
	if heightInt64 < 0 {
		k, err := qs.mgr.Keeper().GetKeygenBlock(ctx, ctx.BlockHeight())
		if err != nil {
			return nil, fmt.Errorf("fail to get keygen for latest block: %w", err)
		}
		heightInt64 = k.Height
	}
	keygenBlock, err := qs.mgr.Keeper().GetKeygenBlock(ctx, heightInt64)
	if err != nil {
		ctx.Logger().Error("fail to get keygen block", "error", err)
		return nil, fmt.Errorf("fail to get keygen block: %w", err)
	}
	// the pubkeys used during keygen
	var pk common.PubKeys
	if len(keygenBlock.Keygens) == 1 {
		pk = keygenBlock.Keygens[0].GetMembers()
	}
	keygen := types.QueryKeygenResponse{
		KeygenBlock: &types.KeygenBlock{
			Height: keygenBlock.Height,
		},
		Signature:   "",
		PubKeys:     pk,
		BlockHeight: ctx.BlockHeight(),
	}
	for _, k := range keygenBlock.Keygens {
		keygen.KeygenBlock.Keygens = append(keygen.KeygenBlock.Keygens, &types.Keygen{
			Type:    k.Type.String(),
			Members: k.Members,
		})
	}
	return &keygen, nil
}

func (qs queryServer) queryKeysign(ctx cosmos.Context, heightStr, pubKey string) (*types.QueryKeysignResponse, error) {
	var err error
	pkString := pubKey

	height, err := strconv.ParseInt(heightStr, 0, 64)
	if err != nil {
		ctx.Logger().Error("invalid block height", "error", err)
		return nil, fmt.Errorf("invalid block height: %w", err)
	}

	if height > ctx.BlockHeight() {
		return nil, fmt.Errorf("block height not available yet")
	}

	pk, err := common.NewPubKey(pkString)
	if err != nil {
		ctx.Logger().Error("invalid pool pub key", "error", err)
		return nil, fmt.Errorf("invalid pool pub key: %w", err)
	}

	if height == 0 {
		height = ctx.BlockHeight()
	}

	keygenHeight, err := qs.mgr.Keeper().GetLastKeygenHeight(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get last keygen height", "error", err)
		return nil, fmt.Errorf("could not get last keygen height: %w", err)
	}

	lastAsgardAddChainHeight, err := qs.mgr.Keeper().GetLastChainEventHeight(ctx, "asgard_add_*")
	if err != nil {
		ctx.Logger().Error("fail to get last asgard add chain height", "error", err)
		return nil, fmt.Errorf("could not get last asgard add chain height: %w", err)
	}

	if lastAsgardAddChainHeight > keygenHeight {
		keygenHeight = lastAsgardAddChainHeight
	}

	vault, err := qs.mgr.Keeper().GetVault(ctx, pk)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
		return nil, fmt.Errorf("could not get vault with pubkey(%s): %w", pk.String(), err)
	}
	if vault.IsEmpty() {
		return nil, errors.New("vault not found")
	}

	hasRouters := len(vault.Routers) > 0

	hs := func(height int64) bool {
		return ctx.BlockHeight() >= height
	}

	keysign := types.QueryKeysignResponse{
		KeygenBlock: &types.KeygenBlock{
			Height: keygenHeight,
		},
		ChainHeight: ctx.BlockHeight(),
		ChainRouterHasSet: map[string]bool{
			common.BASEChain.String(): hs(16893985) && hasRouters,
			common.BSCChain.String():  hs(16893985) && hasRouters,
			common.ETHChain.String():  hs(keygenHeight),
			common.AVAXChain.String(): hs(keygenHeight),
		},
	}

	keygenBlock, err := qs.mgr.Keeper().GetKeygenBlock(ctx, keygenHeight)
	if err != nil {
		ctx.Logger().Error("fail to get keygen block", "error", err)
		return nil, fmt.Errorf("fail to get keygen block: %w", err)
	}

	for _, k := range keygenBlock.Keygens {
		keysign.KeygenBlock.Keygens = append(keysign.KeygenBlock.Keygens, &types.Keygen{
			Type:    k.Type.String(),
			Members: k.Members,
		})
	}

	// set the ENS name for ETH
	ens, err := qs.mgr.Keeper().GetEnsRegisterName(ctx, pk)
	if err == nil {
		keysign.EnsName = ens
	}

	return &keysign, nil
}

func (qs queryServer) queryTssKeygenMetric(ctx cosmos.Context, req *types.QueryTssKeygenMetricRequest) (*types.QueryTssKeygenMetricResponse, error) {
	var pubKeys common.PubKeys
	if len(req.PubKey) > 0 {
		pkey, err := common.NewPubKey(req.PubKey)
		if err != nil {
			return nil, fmt.Errorf("fail to parse pubkey(%s) err:%w", req.PubKey, err)
		}
		pubKeys = append(pubKeys, pkey)
	}
	var keygenMetrics []*types.TssKeygenMetric
	for _, pkey := range pubKeys {
		m, err := qs.mgr.Keeper().GetTssKeygenMetric(ctx, pkey)
		if err != nil {
			return nil, fmt.Errorf("fail to get tss keygen metric for pubkey(%s) err:%w", pkey, err)
		}
		// exclude the keygen metric that doesn't have a valid node TSS time
		if len(m.NodeTssTimes) == 0 {
			continue
		}
		keygenMetrics = append(keygenMetrics, &m)
	}
	return &types.QueryTssKeygenMetricResponse{Metrics: keygenMetrics}, nil
}

func (qs queryServer) queryTssMetric(ctx cosmos.Context, _ *types.QueryTssMetricRequest) (*types.QueryTssMetricResponse, error) {
	keygenMetrics, err := qs.mgr.Keeper().GetAllTssKeygenMetric(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get all tss keygens ,err:%w", err)
	}
	var keygenMetricsPointers []*types.TssKeygenMetric
	for i := range keygenMetrics {
		keygenMetricsPointers = append(keygenMetricsPointers, &keygenMetrics[i])
	}
	keysignMetrics, err := qs.mgr.Keeper().GetAllTssKeysignMetric(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get all tss keysigns,err:%w", err)
	}
	var keysignMetricsPointers []*types.TssKeysignMetric
	for i := range keysignMetrics {
		keysignMetricsPointers = append(keysignMetricsPointers, &keysignMetrics[i])
	}
	return &types.QueryTssMetricResponse{
		Keygen:  keygenMetricsPointers,
		Keysign: keysignMetricsPointers,
	}, nil
}