package xrp

// XrpMetadata is the per-outbound signing checkpoint: the XRP account sequence the
// outbound was signed at, pinned so a re-sign of the same transaction never moves it.
type XrpMetadata struct {
	SeqNumber   int64
	BlockHeight int64
}
