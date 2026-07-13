package types

import "gitlab.com/thorchain/thornode/v3/common"

type ErrataBlock struct {
	Height     int64
	Txs        []ErrataTx
	DeliveryID string
	// Persisted optionally acknowledges that Observer durably stored the
	// errata delivery before the scanner advances its reorg journal.
	Persisted chan error
}

type ErrataTx struct {
	TxID  common.TxID
	Chain common.Chain
}
