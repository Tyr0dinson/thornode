package types

import "gitlab.com/thorchain/thornode/v3/common"

type ErrataBlock struct {
	Height     int64
	Txs        []ErrataTx
	DeliveryID string
	// Persisted optionally acknowledges that Observer durably stored the
	// errata delivery before the scanner advances its reorg journal. Producers
	// must provide a buffered channel with capacity for one result because
	// Observer never blocks on delivery.
	Persisted chan error
}

type ErrataTx struct {
	TxID  common.TxID
	Chain common.Chain
}
