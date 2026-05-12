package blockcypher

// ChainResponse is returned by GET /v1/{coin}/{chain}.
type ChainResponse struct {
	Height int64  `json:"height"`
	Hash   string `json:"hash"`
}

// BlockResponse is returned by GET /v1/{coin}/{chain}/blocks/{hash_or_height}.
// TxIDs is only populated when the request includes txids=true.
// NextTxIDs is non-empty when the block has more txids than the requested limit.
type BlockResponse struct {
	Hash      string   `json:"hash"`
	Height    int64    `json:"height"`
	TxIDs     []string `json:"txids"`
	NextTxIDs string   `json:"next_txids"`
}

// TxResponse is returned by GET /v1/{coin}/{chain}/txs/{txid}.
// BlockHeight is -1 for unconfirmed transactions.
type TxResponse struct {
	Hash        string     `json:"hash"`
	BlockHeight int64      `json:"block_height"`
	Fees        int64      `json:"fees"`
	Size        int        `json:"size"`
	Inputs      []TxInput  `json:"inputs"`
	Outputs     []TxOutput `json:"outputs"`
}

type TxInput struct {
	PrevHash    string   `json:"prev_hash"`
	OutputIndex uint32   `json:"output_index"`
	OutputValue int64    `json:"output_value"`
	Addresses   []string `json:"addresses"`
	ScriptType  string   `json:"script_type"`
}

type TxOutput struct {
	Value      int64    `json:"value"`
	Addresses  []string `json:"addresses"`
	ScriptType string   `json:"script_type"`
}
