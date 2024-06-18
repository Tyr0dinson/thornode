package app

import (
	tmlog "github.com/cometbft/cometbft/libs/log"
	"gitlab.com/thorchain/thornode/log"
)

var _ tmlog.Logger = (*log.TendermintLogWrapper)(nil)
