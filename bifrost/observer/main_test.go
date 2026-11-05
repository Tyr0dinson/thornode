package observer

import (
	"os"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"

	"gitlab.com/thorchain/thornode/v3/bifrost/db"
	"gitlab.com/thorchain/thornode/v3/config"
)

// TestMain opens observer storage without fsync. The stores sync every XMR,
// errata and certificate write so a crashed node keeps them, and the package
// spent a third of its wall time in fsync; no unit test can observe an OS
// crash. Write order, write options and reopen-after-Close are unchanged.
func TestMain(m *testing.M) {
	openObserverLevelDB = openLevelDBWithoutFsync
	os.Exit(m.Run())
}

// openLevelDBWithoutFsync is db.NewLevelDB with opt.Options.NoSync set.
func openLevelDBWithoutFsync(path string, opts config.LevelDBOptions) (*leveldb.DB, error) {
	if path == "" {
		return db.NewLevelDB(path, opts)
	}
	o := opts.Options()
	o.NoSync = true
	ldb, err := leveldb.OpenFile(path, o)
	if err != nil {
		return nil, err
	}
	if opts.CompactOnInit {
		if err = ldb.CompactRange(util.Range{}); err != nil {
			_ = ldb.Close()
			return nil, err
		}
	}
	return ldb, nil
}
