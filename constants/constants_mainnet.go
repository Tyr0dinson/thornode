//go:build !mocknet
// +build !mocknet

package constants

import (
	"time"
)

var ThorchainBlockTime = 6 * time.Second

// BlockTimeMimirEnabled gates the BLOCKTIMEMS mimir: MsgMimir rejects a
// nonzero value and the commit-wait hook never engages while it is false.
// Block-count settings (emission, churn, jail, bifrost margins) do not scale
// with block time yet, so a shorter target would speed them all up.
// TODO(duration-scaling): enable once block-count settings scale with BLOCKTIMEMS
const BlockTimeMimirEnabled = false

// BlockTimeMsMin is the smallest BLOCKTIMEMS mimir value accepted: mainnet's
// consensus overhead alone is ~1.1s at p50 and ~1.6s at p90.
const BlockTimeMsMin = 1500
