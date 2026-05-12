package blockchair

// StatsResponse is returned by GET /{chain}/stats.
type StatsResponse struct {
	Data StatsData `json:"data"`
}

type StatsData struct {
	Blocks int64 `json:"blocks"`
}

// BlocksResponse is returned by GET /{chain}/blocks?q=id(HEIGHT).
type BlocksResponse struct {
	Data []BlockRow `json:"data"`
}

type BlockRow struct {
	ID         int64  `json:"id"`
	Hash       string `json:"hash"`
	Time       string `json:"time"`
	MerkleRoot string `json:"merkle_root"`
	Nonce      uint64 `json:"nonce"`
	Version    int32  `json:"version"`
	Bits       string `json:"bits"`
	Size       int32  `json:"size"`
	Weight     int32  `json:"weight"`
}

// TransactionDashboardResponse is returned by GET /{chain}/dashboards/transaction/{TXID}.
type TransactionDashboardResponse struct {
	Data map[string]TransactionDashboard `json:"data"`
}

type TransactionDashboard struct {
	Transaction TxMeta  `json:"transaction"`
	Inputs      []TxIn  `json:"inputs"`
	Outputs     []TxOut `json:"outputs"`
}

type TxMeta struct {
	Hash    string `json:"hash"`
	BlockID int64  `json:"block_id"`
	Time    string `json:"time"`
	Fee     int64  `json:"fee"`
	Size    int32  `json:"size"`
}

type TxIn struct {
	TransactionHash string `json:"transaction_hash"`
	Index           uint32 `json:"index"`
	Value           int64  `json:"value"`
	Recipient       string `json:"recipient"`
	ScriptHex       string `json:"script_hex"`
	Type            string `json:"type"`
}

type TxOut struct {
	Index     uint32 `json:"index"`
	Value     int64  `json:"value"`
	Recipient string `json:"recipient"`
	Type      string `json:"type"`
	ScriptHex string `json:"script_hex"`
	IsSpent   bool   `json:"is_spent"`
}

// MempoolResponse is returned by GET /{chain}/mempool/transactions.
type MempoolResponse struct {
	Data []MempoolTx `json:"data"`
}

type MempoolTx struct {
	Hash string `json:"hash"`
	Fee  int64  `json:"fee"`
	Size int32  `json:"size"`
}

// BlockTransactionsResponse is returned by GET /{chain}/transactions?q=block_id(HEIGHT).
type BlockTransactionsResponse struct {
	Data []TxMeta `json:"data"`
}

// BlockDetailResponse is returned by GET /{chain}/dashboards/block/{HASH}.
type BlockDetailResponse struct {
	Data map[string]BlockDetail `json:"data"`
}

type BlockDetail struct {
	Block    BlockRow `json:"block"`
	TxHashes []string `json:"transactions"`
}
