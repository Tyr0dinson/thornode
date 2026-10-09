package thorchain

import (
	"context"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type GrpcQueryCovQSuite struct{}

var _ = Suite(&GrpcQueryCovQSuite{})

func (s *GrpcQueryCovQSuite) TestCheckHeightParamCovQ(c *C) {
	c.Assert(checkHeightParam(""), IsNil)
	c.Assert(checkHeightParam("0"), IsNil)
	c.Assert(checkHeightParam("123"), IsNil)
	err := checkHeightParam("abc")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "invalid height param.*")
	err = checkHeightParam("-1")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "invalid height param, must not be negative")
}

// TestInvalidHeightRejectedCovQ ensures every height-aware gRPC endpoint
// rejects malformed and negative heights before touching any state (the
// server has no manager, so reaching a handler would panic).
func (s *GrpcQueryCovQSuite) TestInvalidHeightRejectedCovQ(c *C) {
	qs := &queryServer{}
	ctx := context.Background()
	for _, h := range []string{"-5", "notanumber"} {
		calls := covQHeightCalls(qs, ctx, h)
		c.Assert(len(calls), Equals, 91)
		for i, call := range calls {
			err := call()
			c.Assert(err, NotNil, Commentf("call %d height %s", i, h))
			c.Assert(err.Error(), Matches, "invalid height param.*", Commentf("call %d", i))
		}
	}

	_, err := qs.KeysignParty(ctx, &types.QueryKeysignPartyRequest{SigningHeight: "-1"})
	c.Assert(err, ErrorMatches, "invalid height param.*")
}

func covQHeightCalls(qs *queryServer, ctx context.Context, h string) []func() error {
	return []func() error{
		func() error { _, err := qs.Pool(ctx, &types.QueryPoolRequest{Height: h}); return err },
		func() error { _, err := qs.Pools(ctx, &types.QueryPoolsRequest{Height: h}); return err },
		func() error { _, err := qs.DerivedPool(ctx, &types.QueryDerivedPoolRequest{Height: h}); return err },
		func() error { _, err := qs.DerivedPools(ctx, &types.QueryDerivedPoolsRequest{Height: h}); return err },
		func() error {
			_, err := qs.LiquidityProvider(ctx, &types.QueryLiquidityProviderRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.LiquidityProviders(ctx, &types.QueryLiquidityProvidersRequest{Height: h})
			return err
		},
		func() error { _, err := qs.Saver(ctx, &types.QuerySaverRequest{Height: h}); return err },
		func() error { _, err := qs.Savers(ctx, &types.QuerySaversRequest{Height: h}); return err },
		func() error { _, err := qs.TradeUnit(ctx, &types.QueryTradeUnitRequest{Height: h}); return err },
		func() error { _, err := qs.TradeUnits(ctx, &types.QueryTradeUnitsRequest{Height: h}); return err },
		func() error { _, err := qs.TradeAccount(ctx, &types.QueryTradeAccountRequest{Height: h}); return err },
		func() error { _, err := qs.TradeAccounts(ctx, &types.QueryTradeAccountsRequest{Height: h}); return err },
		func() error { _, err := qs.SecuredAsset(ctx, &types.QuerySecuredAssetRequest{Height: h}); return err },
		func() error { _, err := qs.SecuredAssets(ctx, &types.QuerySecuredAssetsRequest{Height: h}); return err },
		func() error { _, err := qs.StableReserve(ctx, &types.QueryStableReserveRequest{Height: h}); return err },
		func() error {
			_, err := qs.StableReserveContributor(ctx, &types.QueryStableReserveContributorRequest{Height: h})
			return err
		},
		func() error { _, err := qs.Node(ctx, &types.QueryNodeRequest{Height: h}); return err },
		func() error { _, err := qs.Nodes(ctx, &types.QueryNodesRequest{Height: h}); return err },
		func() error { _, err := qs.PoolSlip(ctx, &types.QueryPoolSlipRequest{Height: h}); return err },
		func() error { _, err := qs.PoolSlips(ctx, &types.QueryPoolSlipsRequest{Height: h}); return err },
		func() error { _, err := qs.OutboundFee(ctx, &types.QueryOutboundFeeRequest{Height: h}); return err },
		func() error { _, err := qs.OutboundFees(ctx, &types.QueryOutboundFeesRequest{Height: h}); return err },
		func() error { _, err := qs.StreamingSwap(ctx, &types.QueryStreamingSwapRequest{Height: h}); return err },
		func() error {
			_, err := qs.StreamingSwaps(ctx, &types.QueryStreamingSwapsRequest{Height: h})
			return err
		},
		func() error { _, err := qs.Ban(ctx, &types.QueryBanRequest{Height: h}); return err },
		func() error { _, err := qs.Ragnarok(ctx, &types.QueryRagnarokRequest{Height: h}); return err },
		func() error { _, err := qs.RunePool(ctx, &types.QueryRunePoolRequest{Height: h}); return err },
		func() error { _, err := qs.RuneProvider(ctx, &types.QueryRuneProviderRequest{Height: h}); return err },
		func() error { _, err := qs.RuneProviders(ctx, &types.QueryRuneProvidersRequest{Height: h}); return err },
		func() error { _, err := qs.MimirValues(ctx, &types.QueryMimirValuesRequest{Height: h}); return err },
		func() error { _, err := qs.MimirWithKey(ctx, &types.QueryMimirWithKeyRequest{Height: h}); return err },
		func() error {
			_, err := qs.MimirAdminValues(ctx, &types.QueryMimirAdminValuesRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.MimirNodesAllValues(ctx, &types.QueryMimirNodesAllValuesRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.MimirNodesValues(ctx, &types.QueryMimirNodesValuesRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.MimirNodeValues(ctx, &types.QueryMimirNodeValuesRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{Height: h})
			return err
		},
		func() error { _, err := qs.Version(ctx, &types.QueryVersionRequest{Height: h}); return err },
		func() error { _, err := qs.Thorname(ctx, &types.QueryThornameRequest{Height: h}); return err },
		func() error { _, err := qs.Invariant(ctx, &types.QueryInvariantRequest{Height: h}); return err },
		func() error { _, err := qs.Invariants(ctx, &types.QueryInvariantsRequest{Height: h}); return err },
		func() error { _, err := qs.Network(ctx, &types.QueryNetworkRequest{Height: h}); return err },
		func() error { _, err := qs.BalanceModule(ctx, &types.QueryBalanceModuleRequest{Height: h}); return err },
		func() error { _, err := qs.QuoteSwap(ctx, &types.QueryQuoteSwapRequest{Height: h}); return err },
		func() error {
			_, err := qs.ConstantValues(ctx, &types.QueryConstantValuesRequest{Height: h})
			return err
		},
		func() error { _, err := qs.SwapQueue(ctx, &types.QuerySwapQueueRequest{Height: h}); return err },
		func() error {
			_, err := qs.SwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Height: h})
			return err
		},
		func() error { _, err := qs.SwapDetails(ctx, &types.QuerySwapDetailsRequest{Height: h}); return err },
		func() error { _, err := qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{Height: h}); return err },
		func() error {
			_, err := qs.LimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{Height: h})
			return err
		},
		func() error { _, err := qs.LastBlocks(ctx, &types.QueryLastBlocksRequest{Height: h}); return err },
		func() error {
			_, err := qs.ChainsLastBlock(ctx, &types.QueryChainsLastBlockRequest{Height: h})
			return err
		},
		func() error { _, err := qs.Vault(ctx, &types.QueryVaultRequest{Height: h}); return err },
		func() error {
			_, err := qs.XMRSpentOutputRefs(ctx, &types.QueryXMRSpentOutputRefsRequest{Height: h})
			return err
		},
		func() error { _, err := qs.SolVaultNonce(ctx, &types.QuerySolVaultNonceRequest{Height: h}); return err },
		func() error { _, err := qs.AsgardVaults(ctx, &types.QueryAsgardVaultsRequest{Height: h}); return err },
		func() error { _, err := qs.VaultsPubkeys(ctx, &types.QueryVaultsPubkeysRequest{Height: h}); return err },
		func() error { _, err := qs.VaultSolvency(ctx, &types.QueryVaultSolvencyRequest{Height: h}); return err },
		func() error { _, err := qs.TxStages(ctx, &types.QueryTxStagesRequest{Height: h}); return err },
		func() error { _, err := qs.TxStatus(ctx, &types.QueryTxStatusRequest{Height: h}); return err },
		func() error { _, err := qs.TxVoters(ctx, &types.QueryTxVotersRequest{Height: h}); return err },
		func() error { _, err := qs.TxVotersOld(ctx, &types.QueryTxVotersRequest{Height: h}); return err },
		func() error { _, err := qs.Tx(ctx, &types.QueryTxRequest{Height: h}); return err },
		func() error { _, err := qs.Clout(ctx, &types.QuerySwapperCloutRequest{Height: h}); return err },
		func() error { _, err := qs.Queue(ctx, &types.QueryQueueRequest{Height: h}); return err },
		func() error {
			_, err := qs.ScheduledOutbound(ctx, &types.QueryScheduledOutboundRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.PendingOutbound(ctx, &types.QueryPendingOutboundRequest{Height: h})
			return err
		},
		func() error { _, err := qs.Block(ctx, &types.QueryBlockRequest{Height: h}); return err },
		func() error {
			_, err := qs.TssKeygenMetric(ctx, &types.QueryTssKeygenMetricRequest{Height: h})
			return err
		},
		func() error { _, err := qs.TssMetric(ctx, &types.QueryTssMetricRequest{Height: h}); return err },
		func() error {
			_, err := qs.UpgradeProposal(ctx, &types.QueryUpgradeProposalRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.UpgradeProposals(ctx, &types.QueryUpgradeProposalsRequest{Height: h})
			return err
		},
		func() error { _, err := qs.UpgradeVotes(ctx, &types.QueryUpgradeVotesRequest{Height: h}); return err },
		func() error { _, err := qs.Export(ctx, &types.QueryExportRequest{Height: h}); return err },
		func() error { _, err := qs.Account(ctx, &types.QueryAccountRequest{Height: h}); return err },
		func() error { _, err := qs.Balances(ctx, &types.QueryBalancesRequest{Height: h}); return err },
		func() error { _, err := qs.TCYStaker(ctx, &types.QueryTCYStakerRequest{Height: h}); return err },
		func() error { _, err := qs.TCYStakers(ctx, &types.QueryTCYStakersRequest{Height: h}); return err },
		func() error { _, err := qs.TCYClaimer(ctx, &types.QueryTCYClaimerRequest{Height: h}); return err },
		func() error { _, err := qs.TCYClaimers(ctx, &types.QueryTCYClaimersRequest{Height: h}); return err },
		func() error {
			_, err := qs.TCYProtocolOwned(ctx, &types.QueryTCYProtocolOwnedRequest{Height: h})
			return err
		},
		func() error { _, err := qs.OraclePrices(ctx, &types.QueryOraclePricesRequest{Height: h}); return err },
		func() error { _, err := qs.OraclePrice(ctx, &types.QueryOraclePriceRequest{Height: h}); return err },
		func() error { _, err := qs.ReferenceMemo(ctx, &types.QueryReferenceMemoRequest{Height: h}); return err },
		func() error {
			_, err := qs.ReferenceMemoByHash(ctx, &types.QueryReferenceMemoByHashRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.ReferenceMemoPreflight(ctx, &types.QueryReferenceMemoPreflightRequest{Height: h})
			return err
		},
		func() error { _, err := qs.Supply(ctx, &types.QuerySupplyRequest{Height: h}); return err },
		func() error { _, err := qs.ContractInfo(ctx, &types.QueryContractInfoRequest{Height: h}); return err },
		func() error { _, err := qs.ContractInfos(ctx, &types.QueryContractInfosRequest{Height: h}); return err },
		func() error { _, err := qs.DynamicL1Fees(ctx, &types.QueryDynamicL1FeesRequest{Height: h}); return err },
		func() error {
			_, err := qs.DynamicL1FeesByThorname(ctx, &types.QueryDynamicL1FeesByThornameRequest{Height: h})
			return err
		},
		func() error {
			_, err := qs.DynamicL1FeesCurrent(ctx, &types.QueryDynamicL1FeesCurrentRequest{Height: h})
			return err
		},
	}
}
