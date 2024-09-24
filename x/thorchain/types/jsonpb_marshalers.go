package types

import (
	"encoding/json"
	"fmt"

	"github.com/cosmos/gogoproto/jsonpb"

	openapi "gitlab.com/thorchain/thornode/openapi/gen"
)

func jsonify(r any) ([]byte, error) {
	res, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("fail to marshal response to json: %w", err)
	}
	return res, nil
}

// ProtoMessages where no override is necessary based on openapi swagger
// if wrong, check openapi swagger and open an issue to correct or a nested object, like kv pair could be it
// i.e. I think /thorchain/trade/unit/{address} is incorrectly implemented or openapi swagger is wrong
// QueryPoolResponse
func (m *QueryPoolResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryDerivedPoolResponse
func (m *QueryDerivedPoolResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryLiquidityProviderResponse
func (m *QueryLiquidityProviderResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryRunePoolResponse -- Not needed, no int64s
// func (m *QueryRunePoolResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryRuneProviderResponse
func (m *QueryRuneProviderResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QuerySaverResponse
func (m *QuerySaverResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryBorrowerResponse
func (m *QueryBorrowerResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryTxResponse
func (m *QueryTxResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// ObservedTxVoter
func (m *ObservedTxVoter) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
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

// QueryNodeResponse
func (m *QueryNodeResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryVaultResponse
func (m *QueryVaultResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryVaultsPubkeysResponse -- No int64s
// func (m *QueryVaultsPubkeysResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryNetworkResponse -- No int64s
// func (m *QueryNetworkResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryVersionResponse
func (m *QueryVersionResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// BanVoter
func (m *BanVoter) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryStreamingSwapResponse
func (m *QueryStreamingSwapResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// SwapperClout
func (m *SwapperClout) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryTradeUnitResponse -- no int64s
// func (m *QueryTradeUnitResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryQueueResponse
func (m *QueryQueueResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryKeysignResponse
func (m *QueryKeysignResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryKeygenResponse
func (m *QueryKeygenResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryTssMetricResponse
func (m *QueryTssMetricResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryThornameResponse
func (m *QueryThornameResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryMimirNodesAllValuesResponse
func (m *QueryMimirNodesAllValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryQuoteSwapResponse
func (m *QueryQuoteSwapResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
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

// QueryQuoteLoanOpenResponse
func (m *QueryQuoteLoanOpenResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryQuoteLoanCloseResponse
func (m *QueryQuoteLoanCloseResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}

// QueryInvariantResponse -- no int64s
// func (m *QueryInvariantResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryInvariantsResponse -- no int64s
// func (m *QueryInvariantsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
// 	return jsonify(m)
// }

// QueryBalanceModuleResponse
func (m *QueryBalanceModuleResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m)
}


// ---------- custom json marshalling below is to support non-json-proto objects ---------

func (m *QueryLastBlocksResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.LastBlocks)
}

func (m *QueryPoolsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Pools)
}

func (m *QueryDerivedPoolsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Pools)
}

func (m *QueryLiquidityProvidersResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.LiquidityProviders)
}

func (m *QueryRuneProvidersResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Providers)
}

func (m *QuerySaversResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Savers)
}

func (m *QueryBorrowersResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Borrowers)
}

func (m *QueryNodesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Nodes)
}

func (m *QueryOutboundFeesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.OutboundFees)
}

func (m *QueryAsgardVaultsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.AsgardVaults)
}

func (m *QueryInboundAddressesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.InboundAddresses)
}

func (m *QueryConstantValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	type constantVals struct {
		Int64Values map[string]int64 `json:"int_64_values"`
		BoolValues map[string]bool `json:"bool_values"`
		StringValues map[string]string `json:"string_values"`
	}

	c := constantVals{
		Int64Values: make(map[string]int64),
		BoolValues: make(map[string]bool),
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

func (m *QueryRagnarokResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.InProgress)
}

func (m *QueryStreamingSwapsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.StreamingSwaps)
}

func (m *QueryTradeUnitsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.TradeUnits)
}

func (m *QueryTradeAccountsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.TradeAccounts)
}

func (m *QuerySwapQueueResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.SwapQueue)
}

func (m *QueryOutboundResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.TxOutItems)
}

func (m *QueryTssKeygenMetricResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.Metrics)
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

func (m *QueryMimirAdminValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	v := make(map[string]int64)

	for _, kv := range m.AdminMimirs {
		v[kv.Key] = kv.Value
	}

	return jsonify(v)
}

func (m *QueryMimirNodesValuesResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	v := make(map[string]int64)

	for _, kv := range m.Mimirs {
		v[kv.Key] = kv.Value
	}

	return jsonify(v)
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
		res.Txs[i].Result.Code = wrapInt64(tx.Result.Code)
		res.Txs[i].Result.Data = wrapString(tx.Result.Data)
		res.Txs[i].Result.Log = wrapString(tx.Result.Log)
		res.Txs[i].Result.Info = wrapString(tx.Result.Info)
		res.Txs[i].Result.GasWanted = wrapString(tx.Result.GasWanted)
		res.Txs[i].Result.GasUsed = wrapString(tx.Result.GasUsed)
		res.Txs[i].Result.Events = []map[string]string{}
		for _, event := range tx.Result.Events{
			res.Txs[i].Result.Events = append(res.Txs[i].Result.Events, eventMap(event))
		}
	}
		
	return jsonify(res)
}

func (m *QueryPoolSlipsResponse) MarshalJSONPB(_ *jsonpb.Marshaler) ([]byte, error) {
	return jsonify(m.PoolSlips)
}

func eventMap(e *BlockEvent) map[string]string {
	blockEventMap := make(map[string]string)
	for _, kvpair := range e.EventKvPair{
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

func wrapInt64(d int64) *int64 {
	if d == 0 {
		return nil
	}
	return &d
}