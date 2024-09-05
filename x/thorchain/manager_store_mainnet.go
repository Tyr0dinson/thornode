//go:build !stagenet && !mocknet && !regtest
// +build !stagenet,!mocknet,!regtest

package thorchain

import "gitlab.com/thorchain/thornode/common/cosmos"

func migrateStoreV136(ctx cosmos.Context, mgr *Mgrs) {
	defer func() {
		if err := recover(); err != nil {
			ctx.Logger().Error("fail to migrate store to v136", "error", err)
		}
	}()
}
