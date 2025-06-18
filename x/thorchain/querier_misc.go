// querier_misc.go contains miscellaneous query handlers.
// This includes queries for ragnarok, blocks, TCY stakers/claimers, and codes.

package thorchain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Ragnarok Query ============

func (qs queryServer) queryRagnarok(ctx cosmos.Context, _ *types.QueryRagnarokRequest) (*types.QueryRagnarokResponse, error) {
	ragnarokInProgress := qs.mgr.Keeper().RagnarokInProgress(ctx)
	return &types.QueryRagnarokResponse{InProgress: ragnarokInProgress}, nil
}

// ============ Block Query ============

func (qs queryServer) queryBlock(ctx cosmos.Context, req *types.QueryBlockRequest) (*types.QueryBlockResponse, error) {
	initTendermintOnce.Do(initTendermint)
	if tendermintClient == nil {
		return nil, fmt.Errorf("tendermint client is not initialized")
	}

	height, err := strconv.ParseInt(req.Height, 10, 64)
	if err != nil {
		ctx.Logger().Error("fail to parse block height", "error", err)
		return nil, fmt.Errorf("fail to parse block height: %w", err)
	}

	resultBlock, err := tendermintClient.Block(ctx, &height)
	if err != nil {
		return nil, fmt.Errorf("fail to get block: %w", err)
	}

	resultBlockResults, err := tendermintClient.BlockResults(ctx, &height)
	if err != nil {
		return nil, fmt.Errorf("fail to get block results: %w", err)
	}

	block := &types.QueryBlockResponse{
		Header: &types.BlockHeader{
			// Basic block info
			Version: &types.BlockConsensus{
				Block: uint64(resultBlock.Block.Version.Block),
				App:   uint64(resultBlock.Block.Version.App),
			},
			ChainId: resultBlock.Block.ChainID,
			Height:  resultBlock.Block.Height,
			Time:    resultBlock.Block.Time.String(),

			// Prev block info
			LastBlockId: &types.BlockPartSetHeader{
				Total: int32(resultBlock.Block.LastBlockID.PartSetHeader.Total),
				Hash:  hex.EncodeToString(resultBlock.Block.LastBlockID.PartSetHeader.Hash),
			},

			// Hashes of block data
			LastCommitHash:     hex.EncodeToString(resultBlock.Block.LastCommitHash),
			DataHash:           hex.EncodeToString(resultBlock.Block.DataHash),
			ValidatorsHash:     hex.EncodeToString(resultBlock.Block.ValidatorsHash),
			NextValidatorsHash: hex.EncodeToString(resultBlock.Block.NextValidatorsHash),
			ConsensusHash:      hex.EncodeToString(resultBlock.Block.ConsensusHash),
			AppHash:            hex.EncodeToString(resultBlock.Block.AppHash),
			LastResultsHash:    hex.EncodeToString(resultBlock.Block.LastResultsHash),
			EvidenceHash:       hex.EncodeToString(resultBlock.Block.EvidenceHash),
			ProposerAddress:    resultBlock.Block.ProposerAddress.String(),
		},
		Txs: []string{},
	}

	// extract txs
	beginTxs := []string{}
	endTxs := []string{}
	consensusEvts := map[string]string{}
	for _, event := range resultBlockResults.BeginBlockEvents {
		e := blockEvent(event)
		if e == nil {
			continue
		}
		evtMap := eventMap(e)
		if evtMap["type"] == "consensus" {
			consensusEvts = evtMap
			continue
		}
		et, err := EventTypeFromABCIEvent(e)
		if err != nil {
			continue
		}
		ne, err := NewEventFromABCIEvent(e, et)
		if err != nil {
			continue
		}
		beginTxs = append(beginTxs, ne.ID().String())
	}
	for idx, tx := range resultBlockResults.TxsResults {
		for _, event := range tx.Events {
			e := blockEvent(event)
			if e == nil {
				continue
			}
			et, err := EventTypeFromABCIEvent(e)
			if err != nil {
				continue
			}
			// DEPRACATED: these memo less observed txs are no longer valid, but could still
			// exist in the blockchain history
			if et == InboundEventType {
				m := eventMap(e)
				// skip observed transactions with no memo
				if !strings.Contains(m["memo"], ":") {
					continue
				}
			}
			ne, err := NewEventFromABCIEvent(e, et)
			if err != nil {
				ctx.Logger().Debug("fail to parse event", "error", err, "idx", idx)
				continue
			}
			block.Txs = append(block.Txs, ne.ID().String())
		}
	}
	for _, event := range resultBlockResults.EndBlockEvents {
		e := blockEvent(event)
		if e == nil {
			continue
		}
		et, err := EventTypeFromABCIEvent(e)
		if err != nil {
			continue
		}
		ne, err := NewEventFromABCIEvent(e, et)
		if err != nil {
			continue
		}
		endTxs = append(endTxs, ne.ID().String())
	}

	// prepend the begin block txs to the beginning of the list
	block.Txs = append(beginTxs, block.Txs...)
	// append the end block txs to the end of the list
	block.Txs = append(block.Txs, endTxs...)

	// update header with consensus events
	if len(consensusEvts) > 0 {
		if consensusEvts["mint"] != "" {
			mint := big.NewInt(0)
			mint, _ = mint.SetString(consensusEvts["mint"], 10)
			block.Header.InflationAmount = mint.Uint64()
		}
		if consensusEvts["pool_rewards"] != "" {
			poolRewards := big.NewInt(0)
			poolRewards, _ = poolRewards.SetString(consensusEvts["pool_rewards"], 10)
			block.Header.PoolRewards = poolRewards.Uint64()
		}
		if consensusEvts["node_rewards"] != "" {
			nodeRewards := big.NewInt(0)
			nodeRewards, _ = nodeRewards.SetString(consensusEvts["node_rewards"], 10)
			block.Header.NodeRewards = nodeRewards.Uint64()
		}
		if consensusEvts["reserve_rewards"] != "" {
			reserveRewards := big.NewInt(0)
			reserveRewards, _ = reserveRewards.SetString(consensusEvts["reserve_rewards"], 10)
			block.Header.ReserveRewards = reserveRewards.Uint64()
		}
		if consensusEvts["pol_emit"] != "" {
			polEmit := big.NewInt(0)
			polEmit, _ = polEmit.SetString(consensusEvts["pol_emit"], 10)
			block.Header.PolEmit = polEmit.Uint64()
		}
		if consensusEvts["pol_withdraw"] != "" {
			polWithdraw := big.NewInt(0)
			polWithdraw, _ = polWithdraw.SetString(consensusEvts["pol_withdraw"], 10)
			block.Header.PolWithdraw = polWithdraw.Uint64()
		}
		if consensusEvts["effective_security_bond"] != "" {
			effectiveSecurityBond := big.NewInt(0)
			effectiveSecurityBond, _ = effectiveSecurityBond.SetString(consensusEvts["effective_security_bond"], 10)
			block.Header.EffectiveSecurityBond = effectiveSecurityBond.Uint64()
		}
		if consensusEvts["available_pools_rune"] != "" {
			availablePoolsRune := big.NewInt(0)
			availablePoolsRune, _ = availablePoolsRune.SetString(consensusEvts["available_pools_rune"], 10)
			block.Header.AvailablePoolsRune = availablePoolsRune.Uint64()
		}
		if consensusEvts["node_rewards_payout"] != "" {
			nodeRewardsPayout := big.NewInt(0)
			nodeRewardsPayout, _ = nodeRewardsPayout.SetString(consensusEvts["node_rewards_payout"], 10)
			block.Header.NodeRewardsPayout = nodeRewardsPayout.Uint64()
		}
		if consensusEvts["valid_pools"] != "" {
			validPools := big.NewInt(0)
			validPools, _ = validPools.SetString(consensusEvts["valid_pools"], 10)
			block.Header.ValidPools = validPools.Uint64()
		}
		if consensusEvts["available_reserve_rune"] != "" {
			availableReserveRune := big.NewInt(0)
			availableReserveRune, _ = availableReserveRune.SetString(consensusEvts["available_reserve_rune"], 10)
			block.Header.AvailableReserveRune = availableReserveRune.Uint64()
		}
	}

	return block, nil
}

// ============ TCY Queries ============

func (qs queryServer) queryTCYStakers(ctx cosmos.Context, req *types.QueryTCYStakersRequest) (*types.QueryTCYStakersResponse, error) {
	page, limit := req.GetPagination()
	result := make([]*types.QueryTCYStakerResponse, 0)
	iterator := qs.mgr.Keeper().GetAffiliateCollectorIterator(ctx, page, limit)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var col types.AffiliateCollector
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &col)
		result = append(result, &types.QueryTCYStakerResponse{
			RuneAddress: col.RuneAddress.String(),
			RuneAmount:  col.RuneAmount.String(),
		})
	}
	return &types.QueryTCYStakersResponse{Stakers: result}, nil
}

func (qs queryServer) queryTCYStaker(ctx cosmos.Context, req *types.QueryTCYStakerRequest) (*types.QueryTCYStakerResponse, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("rune address not provided")
	}
	runeAddr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to parse rune address", "error", err)
		return nil, fmt.Errorf("fail to parse rune address: %w", err)
	}
	acc, err := qs.mgr.Keeper().GetAffiliateCollectorByAddress(ctx, runeAddr)
	if err != nil {
		ctx.Logger().Error("fail to get affiliate collector", "error", err)
		return nil, fmt.Errorf("fail to get affiliate collector: %w", err)
	}
	return &types.QueryTCYStakerResponse{
		RuneAddress: acc.RuneAddress.String(),
		RuneAmount:  acc.RuneAmount.String(),
	}, nil
}

func (qs queryServer) queryTCYClaimers(ctx cosmos.Context, req *types.QueryTCYClaimersRequest) (*types.QueryTCYClaimersResponse, error) {
	page, limit := req.GetPagination()
	result := make([]*types.QueryTCYClaimerResponse, 0)
	iterator := qs.mgr.Keeper().GetTCYIterator(ctx, page, limit)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var tcy types.ThorchainYieldCollector
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &tcy)
		result = append(result, &types.QueryTCYClaimerResponse{
			Depositor:   tcy.RuneAddress.String(),
			Tcid:        tcy.TCID,
			RuneAmount:  tcy.RuneAmount.String(),
			AssetAmount: tcy.AssetAmount.String(),
		})
	}
	return &types.QueryTCYClaimersResponse{Claimers: result}, nil
}

func (qs queryServer) queryTCYClaimer(ctx cosmos.Context, req *types.QueryTCYClaimerRequest) (*types.QueryTCYClaimerResponse, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("rune address not provided")
	}
	runeAddr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to parse rune address", "error", err)
		return nil, fmt.Errorf("fail to parse rune address: %w", err)
	}
	tcys, err := qs.mgr.Keeper().GetTCYByAddress(ctx, runeAddr)
	if err != nil {
		ctx.Logger().Error("fail to get tcy", "error", err)
		return nil, fmt.Errorf("fail to get tcy: %w", err)
	}
	if len(tcys) == 0 {
		return nil, fmt.Errorf("tcy not found")
	}
	// just return the first one, though there could be multiple if someone deposits with
	// different TCIDs
	return &types.QueryTCYClaimerResponse{
		Depositor:   tcys[0].RuneAddress.String(),
		Tcid:        tcys[0].TCID,
		RuneAmount:  tcys[0].RuneAmount.String(),
		AssetAmount: tcys[0].AssetAmount.String(),
	}, nil
}

// ============ Codes Query ============

func (qs queryServer) queryCodes(_ cosmos.Context, _ *types.QueryCodesRequest) (*types.QueryCodesResponse, error) {
	return &types.QueryCodesResponse{Codes: CodeMapping}, nil
}