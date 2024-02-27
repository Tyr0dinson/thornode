package types

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/openapi/gen"
)

// QueryKeygenBlock query keygen, displays signed keygen requests
type QueryKeygenBlock struct {
	KeygenBlock KeygenBlock `json:"keygen_block"`
	Signature   string      `json:"signature"`
}

// String implement fmt.Stringer
func (n QueryKeygenBlock) String() string {
	return n.KeygenBlock.String()
}

// QueryKeysign query keysign result
type QueryKeysign struct {
	Keysign   TxOut  `json:"keysign"`
	Signature string `json:"signature"`
}

// QueryYggdrasilVaults query yggdrasil vault result
// TODO remove on hard fork
type QueryYggdrasilVaults struct {
	BlockHeight           int64               `json:"block_height,omitempty"`
	PubKey                common.PubKey       `json:"pub_key,omitempty"`
	Coins                 common.Coins        `json:"coins"`
	Type                  VaultType           `json:"type,omitempty"`
	StatusSince           int64               `json:"status_since,omitempty"`
	Membership            []string            `json:"membership,omitempty"`
	Chains                []string            `json:"chains,omitempty"`
	InboundTxCount        int64               `json:"inbound_tx_count,omitempty"`
	OutboundTxCount       int64               `json:"outbound_tx_count,omitempty"`
	PendingTxBlockHeights []int64             `json:"pending_tx_block_heights,omitempty"`
	Routers               []ChainContract     `json:"routers"`
	Status                NodeStatus          `json:"status"`
	Bond                  cosmos.Uint         `json:"bond"`
	TotalValue            cosmos.Uint         `json:"total_value"`
	Addresses             []QueryChainAddress `json:"addresses"`
}

// QueryVaultResp used represent the informat return to client for query asgard
type QueryVaultResp struct {
	BlockHeight           int64               `json:"block_height,omitempty"`
	PubKey                common.PubKey       `json:"pub_key,omitempty"`
	Coins                 common.Coins        `json:"coins"`
	Type                  VaultType           `json:"type,omitempty"`
	Status                VaultStatus         `json:"status"`
	StatusSince           int64               `json:"status_since,omitempty"`
	Membership            []string            `json:"membership,omitempty"`
	Chains                []string            `json:"chains,omitempty"`
	InboundTxCount        int64               `json:"inbound_tx_count,omitempty"`
	OutboundTxCount       int64               `json:"outbound_tx_count,omitempty"`
	PendingTxBlockHeights []int64             `json:"pending_tx_block_heights,omitempty"`
	Routers               []ChainContract     `json:"routers"`
	Addresses             []QueryChainAddress `json:"addresses"`
	Frozen                []string            `json:"frozen,omitempty"`
}

type QueryChainAddress struct {
	Chain   common.Chain   `json:"chain"`
	Address common.Address `json:"address"`
}

// QueryBlockTx overrides the openapi type with a custom Tx field for marshaling.
type QueryBlockTx struct {
	openapi.BlockTx
	Tx json.RawMessage `json:"tx,omitempty"`
}

func (q QueryBlockTx) MarshalJSON() ([]byte, error) {
	toSerialize := map[string]interface{}{}
	toSerialize["hash"] = q.Hash
	toSerialize["tx"] = q.Tx
	toSerialize["result"] = q.Result
	return json.Marshal(toSerialize)
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

// QueryMimirV2IDs contains the information about the mimir V2s currently being voted on
type QueryMimirV2IDs struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	VoteKey   string          `json:"vote_key"`
	LegacyKey string          `json:"legacy_key"`
	Type      string          `json:"type"`
	Votes     map[int64]int64 `json:"votes"`
}

// QueryVaultsPubKeys represent the result for query vaults pubkeys
type QueryVaultsPubKeys struct {
	Asgard    []openapi.VaultInfo `json:"asgard"`
	Yggdrasil []openapi.VaultInfo `json:"yggdrasil"` // TODO remove on hard fork
	Inactive  []openapi.VaultInfo `json:"inactive"`
}
