package thorchain

import (
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

func Fuzz(data []byte) int {
	ParseMemo(types.GetCurrentVersion(), string(data))
	return 0
}
