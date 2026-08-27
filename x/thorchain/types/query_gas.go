package types

const (
	// QueryGasRequestMetadata opts a gRPC query into reporting its consumed gas.
	QueryGasRequestMetadata = "x-thorchain-query-gas"
	// QueryGasUsedMetadata carries the consumed query gas in gRPC response metadata.
	QueryGasUsedMetadata = "x-thorchain-query-gas-used"
)
