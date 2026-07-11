package types

import "gitlab.com/thorchain/thornode/v3/common"

type ErrataBlock struct {
	Height     int64
	Txs        []ErrataTx
	DeliveryID string
	// Persisted is an optional in-process durability acknowledgment. The XMR
	// reorg journal waits for ObserverStorage to persist the errata before it
	// advances its delivery phase.
	Persisted chan error
}

type ErrataTx struct {
	TxID  common.TxID
	Chain common.Chain
}
