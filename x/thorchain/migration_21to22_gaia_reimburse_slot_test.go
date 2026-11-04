package thorchain

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// gaiaReimburseMigrationFrom is the single source of truth for the module
// version slot the tcso GAIA-vault reimbursement is registered at. If the a/b
// renumber (the collision with the private SL-THOR-031 migration) moves the
// reimbursement, bump this one constant together with the migration method name
// and its luviMigrations entry — every test keys off this constant alone.
const gaiaReimburseMigrationFrom = uint64(21)

// gaiaReimburseSlotRun returns the migration registered at
// gaiaReimburseMigrationFrom and how many slots carry that number (which must
// be exactly one). Running the reimbursement only through its registered slot
// is what the tests assert: the migration runs once, at its slot, and nowhere
// else.
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
