package utxo

import "time"

// finalitySnapshot is immutable once published; the scanner and signer read it
// concurrently through Client.finality.
type finalitySnapshot struct {
	blockHeight     int64
	chainLockHeight int64
	chainLockHash   string // Used by FIRO for continuity checks; empty for DASH.
	checkedAt       time.Time
}
