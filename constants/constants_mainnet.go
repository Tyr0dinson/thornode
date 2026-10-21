//go:build !mocknet
// +build !mocknet

package constants

import (
	"time"
)

var ThorchainBlockTime = 6 * time.Second

// BlockTimeMsMin is the smallest BLOCKTIMEMS mimir value accepted: mainnet's
// consensus overhead alone is ~1.1s at p50 and ~1.6s at p90.
const BlockTimeMsMin = 1500
