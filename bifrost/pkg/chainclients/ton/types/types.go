package types

type BlockIdExt struct {
	Type      string `json:"@type"`
	Workchain int64  `json:"workchain"`
	Shard     string `json:"shard"`
	Seqno     uint64 `json:"seqno"`
}

type MasterchainInfo struct {
	Last BlockIdExt `json:"last"`
}
