package types

import (
	"encoding/json"
	"fmt"

	"github.com/cosmos/gogoproto/jsonpb"

	openapi "gitlab.com/thorchain/thornode/openapi/gen"
)

// Implementation of JSONPBMarshaler for all query types that require it
// JSONPBMarshaler is a custom json marshaler
// We use it to marshal query responses into the expected openapi type
// It is necessary for query responses that are slices, contain maps, or contain int64 parameters.
// The proto marshaler marshals int64 parameter to strings while openapi expects int64

var (
	_ jsonpb.JSONPBMarshaler = &QueryAsgardVaultsResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryBalanceModuleResponse{}
	_ jsonpb.JSONPBMarshaler = &BanVoter{}
	_ jsonpb.JSONPBMarshaler = &QueryBlockResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryBorrowerResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryBorrowersResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryConstantValuesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryDerivedPoolResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryDerivedPoolsResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryInboundAddressesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryKeygenResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryKeysignResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryLastBlocksResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryLiquidityProviderResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryLiquidityProvidersResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryMimirAdminValuesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryMimirNodesAllValuesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryMimirNodesValuesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryMimirValuesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryMimirWithKeyResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryNodeResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryNodesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryObservedTxVoter{}
	_ jsonpb.JSONPBMarshaler = &QueryOutboundFeesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryOutboundResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryPoolResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryPoolSlipsResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryPoolsResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryQueueResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryQuoteLoanCloseResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryQuoteLoanOpenResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryQuoteSaverDepositResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryQuoteSaverWithdrawResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryQuoteSwapResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryRagnarokResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryRuneProviderResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryRuneProvidersResponse{}
	_ jsonpb.JSONPBMarshaler = &QuerySaverResponse{}
	_ jsonpb.JSONPBMarshaler = &QuerySaversResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryStreamingSwapResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryStreamingSwapsResponse{}
	_ jsonpb.JSONPBMarshaler = &SwapperClout{}
	_ jsonpb.JSONPBMarshaler = &QuerySwapQueueResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryThornameResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryTradeAccountsResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryTradeUnitsResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryTssKeygenMetricResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryTssMetricResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryTxResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryTxStagesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryTxStatusResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryUpgradeProposalResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryUpgradeProposalsResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryUpgradeVotesResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryVaultResponse{}
	_ jsonpb.JSONPBMarshaler = &QueryVersionResponse{}

	// Query responses that do not require a json marshaler override
	// _ jsonpb.JSONPBMarshaler = &QueryInvariantResponse{}
	// _ jsonpb.JSONPBMarshaler = &QueryInvariantsResponse{}
	// _ jsonpb.JSONPBMarshaler = &QueryNetworkResponse{}
	// _ jsonpb.JSONPBMarshaler = &QueryRunePoolResponse{}
	// _ jsonpb.JSONPBMarshaler = &QueryTradeUnitResponse{}
	// _ jsonpb.JSONPBMarshaler = &QueryVaultsPubkeysResponse{}

)

func jsonify(r any) ([]byte, error) {
	res, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("fail to marshal response to json: %w", err)
	}
	return res, nil
}

func (m *QueryAsgardVaultsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.AsgardVaults)
}

// QueryBalanceModuleResponse
func (m *QueryBalanceModuleResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// BanVoter
func (m *BanVoter) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryBorrowerResponse
func (m *QueryBorrowerResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryBorrowersResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Borrowers)
}

func (m *QueryConstantValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	type constantVals struct {
		Int64Values  map[string]int64  `json:"int_64_values"`
		BoolValues   map[string]bool   `json:"bool_values"`
		StringValues map[string]string `json:"string_values"`
	}

	c := constantVals{
		Int64Values:  make(map[string]int64),
		BoolValues:   make(map[string]bool),
		StringValues: make(map[string]string),
	}

	for _, kv := range m.BoolValues {
		c.BoolValues[kv.Name] = kv.Value
	}

	for _, kv := range m.Int_64Values {
		c.Int64Values[kv.Name] = kv.Value
	}

	for _, kv := range m.StringValues {
		c.StringValues[kv.Name] = kv.Value
	}

	return jsonify(c)
}

// QueryDerivedPoolResponse
func (m *QueryDerivedPoolResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryDerivedPoolsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Pools)
}

func (m *QueryInboundAddressesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.InboundAddresses)
}

// QueryInvariantResponse
// No override needed (contains no int64 parameters)
// func (m *QueryInvariantResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryInvariantsResponse
// No override needed (contains no int64 parameters)
// func (m *QueryInvariantsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryKeygenResponse
func (m *QueryKeygenResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryKeysignResponse
func (m *QueryKeysignResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryLastBlocksResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.LastBlocks)
}

// QueryLiquidityProviderResponse
func (m *QueryLiquidityProviderResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryLiquidityProvidersResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.LiquidityProviders)
}

func (m *QueryMimirAdminValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	v := make(map[string]int64)

	for _, kv := range m.AdminMimirs {
		v[kv.Key] = kv.Value
	}

	return jsonify(v)
}

// QueryMimirNodesAllValuesResponse
func (m *QueryMimirNodesAllValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryMimirNodesValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	v := make(map[string]int64)

	for _, kv := range m.Mimirs {
		v[kv.Key] = kv.Value
	}

	return jsonify(v)
}

func (m *QueryMimirValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	v := make(map[string]int64)

	for _, kv := range m.Mimirs {
		v[kv.Key] = kv.Value
	}

	return jsonify(v)
}

func (m *QueryMimirWithKeyResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Value)
}

// QueryNetworkResponse
// No override needed (contains no int64 parameters)
// func (m *QueryNetworkResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryNodeResponse
func (m *QueryNodeResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryNodesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Nodes)
}

// ObservedTxVoter
func (m *QueryObservedTxVoter) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryOutboundFeesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.OutboundFees)
}

func (m *QueryOutboundResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.TxOutItems)
}

// QueryPoolResponse
func (m *QueryPoolResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryPoolsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Pools)
}

func (m *QueryPoolSlipsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.PoolSlips)
}

// QueryQueueResponse
func (m *QueryQueueResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryQuoteLoanOpenResponse
func (m *QueryQuoteLoanOpenResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryQuoteLoanCloseResponse
func (m *QueryQuoteLoanCloseResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryQuoteSaverDepositResponse
func (m *QueryQuoteSaverDepositResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryQuoteSaverWithdrawResponse
func (m *QueryQuoteSaverWithdrawResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryQuoteSwapResponse
func (m *QueryQuoteSwapResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryRagnarokResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.InProgress)
}

// QueryRunePoolResponse
// No override needed (contains no int64 parameters)
// func (m *QueryRunePoolResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryRuneProviderResponse
func (m *QueryRuneProviderResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryRuneProvidersResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Providers)
}

// QuerySaverResponse
func (m *QuerySaverResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QuerySaversResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Savers)
}

// QueryStreamingSwapResponse
func (m *QueryStreamingSwapResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryStreamingSwapsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.StreamingSwaps)
}

// SwapperClout
func (m *SwapperClout) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QuerySwapQueueResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.SwapQueue)
}

// QueryThornameResponse
func (m *QueryThornameResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryTradeAccountsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.TradeAccounts)
}

// QueryTradeUnitResponse
// No override needed (contains no int64 parameters)
// func (m *QueryTradeUnitResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

func (m *QueryTradeUnitsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.TradeUnits)
}

func (m *QueryTssKeygenMetricResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Metrics)
}

// QueryTssMetricResponse
func (m *QueryTssMetricResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryTxResponse
func (m *QueryTxResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryTxStagesResponse
func (m *QueryTxStagesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryTxStatusResponse
func (m *QueryTxStatusResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryUpgradeProposalResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

func (m *QueryUpgradeProposalsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.UpgradeProposals)
}

func (m *QueryUpgradeVotesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.UpgradeVotes)
}

// QueryVaultResponse
func (m *QueryVaultResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryVaultsPubkeysResponse
// No override needed (contains no int64 parameters)
// func (m *QueryVaultsPubkeysResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryVersionResponse
func (m *QueryVersionResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryExportResponse
// RegressionTest only
func (m *QueryExportResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return m.Content, nil
}

// Backwards compatibility for /auth/accounts/{address} endpoint
func (m *QueryAccountResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	res := map[string]any{
		"result": map[string]any{
			"value": m,
		},
	}
	return jsonify(res)
}

func (m *QueryBalancesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	res := map[string]any{
		"result": m.Balances,
	}
	return jsonify(res)
}

// QueryOpenAPiBlockTx overrides the openapi type with a custom Tx field for marshaling.
type QueryOpenApiBlockTx struct {
	openapi.BlockTx
	Tx json.RawMessage `json:"tx,omitempty"`
}

// QueryOpenApiBlockResponse overrides the openapi type with a custom Txs field for marshaling.
type QueryOpenApiBlockResponse struct {
	openapi.BlockResponse
	Txs []QueryOpenApiBlockTx `json:"txs"`
}

func (m *QueryBlockResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	res := QueryOpenApiBlockResponse{
		BlockResponse: openapi.BlockResponse{
			Id: openapi.BlockResponseId{
				Hash: m.Id.Hash,
				Parts: openapi.BlockResponseIdParts{
					Total: m.Id.Parts.Total,
					Hash:  m.Id.Parts.Hash,
				},
			},
			Header: openapi.BlockResponseHeader{
				Version: openapi.BlockResponseHeaderVersion{
					Block: m.Header.Version.Block,
					App:   m.Header.Version.App,
				},
				ChainId: m.Header.ChainId,
				Height:  m.Header.Height,
				Time:    m.Header.Time,
				LastBlockId: openapi.BlockResponseId{
					Hash: m.Header.LastBlockId.Hash,
					Parts: openapi.BlockResponseIdParts{
						Total: m.Header.LastBlockId.Parts.Total,
						Hash:  m.Header.LastBlockId.Parts.Hash,
					},
				},
				LastCommitHash:     m.Header.LastCommitHash,
				DataHash:           m.Header.DataHash,
				ValidatorsHash:     m.Header.ValidatorsHash,
				NextValidatorsHash: m.Header.NextValidatorsHash,
				ConsensusHash:      m.Header.ConsensusHash,
				AppHash:            m.Header.AppHash,
				LastResultsHash:    m.Header.LastResultsHash,
				EvidenceHash:       m.Header.EvidenceHash,
				ProposerAddress:    m.Header.ProposerAddress,
			},
			BeginBlockEvents: []map[string]string{},
			EndBlockEvents:   []map[string]string{},
		},
		Txs: make([]QueryOpenApiBlockTx, len(m.Txs)),
	}

	for _, event := range m.BeginBlockEvents {
		res.BeginBlockEvents = append(res.BeginBlockEvents, eventMap(event))
	}

	for _, event := range m.EndBlockEvents {
		res.EndBlockEvents = append(res.EndBlockEvents, eventMap(event))
	}

	for i, tx := range m.Txs {
		res.Txs[i].Hash = tx.Hash
		res.Txs[i].Tx = tx.Tx
		res.Txs[i].Result.Code = &tx.Result.Code
		res.Txs[i].Result.Data = wrapString(tx.Result.Data)
		res.Txs[i].Result.Log = wrapString(tx.Result.Log)
		res.Txs[i].Result.Info = wrapString(tx.Result.Info)
		res.Txs[i].Result.GasWanted = wrapString(tx.Result.GasWanted)
		res.Txs[i].Result.GasUsed = wrapString(tx.Result.GasUsed)
		res.Txs[i].Result.Events = []map[string]string{}
		for _, event := range tx.Result.Events {
			res.Txs[i].Result.Events = append(res.Txs[i].Result.Events, eventMap(event))
		}
	}

	return jsonify(res)
}

func eventMap(e *BlockEvent) map[string]string {
	blockEventMap := make(map[string]string)
	for _, kvpair := range e.EventKvPair {
		blockEventMap[kvpair.Key] = kvpair.Value
	}

	return blockEventMap
}

func wrapString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
