package thorchain

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// gaiaReimburseMigrationFrom is the module version slot the GAIA top-up
// reimbursement is registered at. The reimbursement is folded into Migrate20to21
// (replacing the old write-down), so it runs at slot 20.
const gaiaReimburseMigrationFrom = uint64(20)

// gaiaReimburseSlotRun returns the migration registered at
// gaiaReimburseMigrationFrom and how many slots carry that number (which must be
// exactly one): the reimbursement runs once, at its slot, and nowhere else.
func gaiaReimburseSlotRun(mgr *Mgrs) (func(sdk.Context) error, int) {
	var run func(sdk.Context) error
	count := 0
	for _, mig := range NewMigrator(mgr).luviMigrations() {
		if mig.from == gaiaReimburseMigrationFrom {
			run = mig.run
			count++
		}
	}
	return run, count
}
