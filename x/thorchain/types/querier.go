package types

import (
	"encoding/json"

	openapi "gitlab.com/thorchain/thornode/openapi/gen"
)

// QueryBlockTx overrides the openapi type with a custom Tx field for marshaling.
type QueryBlockTx struct {
	openapi.BlockTx
	Tx json.RawMessage `json:"tx,omitempty"`
}

// QueryBlockResponse overrides the openapi type with a custom Txs field for marshaling.
type QueryBlockResponse struct {
	openapi.BlockResponse
	Txs []QueryBlockTx `json:"txs"`
}

// MarshalJSON custom marshalling to add the custom Tx field.
func (q QueryBlockResponse) MarshalJSON() ([]byte, error) {
	toSerialize := map[string]interface{}{}
	toSerialize["id"] = q.Id
	toSerialize["header"] = q.Header
	toSerialize["begin_block_events"] = q.BeginBlockEvents
	toSerialize["end_block_events"] = q.EndBlockEvents
	toSerialize["txs"] = q.Txs
	return json.Marshal(toSerialize)
}
