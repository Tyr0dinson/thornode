package main

import (
	"fmt"
	"path/filepath"
	"syscall"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/viper"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

const (
	// defaultAppDBBlockCacheMiB sizes the cache of decompressed leveldb blocks.
	// The goleveldb default (8 MiB) is tiny next to the app db, so most IAVL
	// fast-node reads that miss the IAVL caches go to disk and snappy-decode a
	// block.
	defaultAppDBBlockCacheMiB = 512

	// flagAppDBBlockCacheMiB is the app.toml key that overrides
	// defaultAppDBBlockCacheMiB.
	flagAppDBBlockCacheMiB = "app-db-block-cache-mib"

	// appDBMaxOpenFiles caches open table files. The default (500) covers a
	// fraction of a mainnet app db (tens of thousands of 2 MiB tables), so
	// reads keep reopening tables.
	appDBMaxOpenFiles = 8192
)

// appDBBlockCacheBytes is the block cache openAppDB gives the application db.
// The start command sets it from app.toml (setAppDBBlockCache) before the db
// is opened.
var appDBBlockCacheBytes = defaultAppDBBlockCacheMiB * opt.MiB

// setAppDBBlockCache sets appDBBlockCacheBytes from app-db-block-cache-mib,
// keeping the default when the key is absent. The cache lives in the Go heap,
// so its size trades memory for GC work: a larger cache raises the heap the GC
// paces against and so makes collections rarer.
func setAppDBBlockCache(v *viper.Viper) error {
	appDBBlockCacheBytes = defaultAppDBBlockCacheMiB * opt.MiB
	if !v.IsSet(flagAppDBBlockCacheMiB) {
		return nil
	}
	mib := v.GetInt(flagAppDBBlockCacheMiB)
	if mib <= 0 {
		return fmt.Errorf("%s must be positive, got %d", flagAppDBBlockCacheMiB, mib)
	}
	appDBBlockCacheBytes = mib * opt.MiB
	return nil
}

// openAppDB opens the application db like the SDK default, but with larger
// goleveldb read caches and no seek-triggered compaction. These options are
// not persisted and do not change the table format; the stored data and every
// read result are the same.
func openAppDB(rootDir string, backendType dbm.BackendType) (dbm.DB, error) {
	dataDir := filepath.Join(rootDir, "data")
	if backendType != dbm.GoLevelDBBackend {
		return dbm.NewDB("application", backendType, dataDir)
	}
	return dbm.NewGoLevelDBWithOpts("application", dataDir, &opt.Options{
		Filter:                 filter.NewBloomFilter(10), // as dbm.NewGoLevelDB
		BlockCacheCapacity:     appDBBlockCacheBytes,
		OpenFilesCacheCapacity: maxOpenFiles(),
		// Reads that probe a table without finding the key spend its seek
		// budget; when it runs out the table is compacted. Every block writes
		// keys across the whole keyspace, so each flushed L0 table overlaps
		// all of L1, runs out of seeks quickly and is merged into all of L1.
		// Bloom filters already make those probes cheap, so the compaction
		// costs far more than it saves.
		DisableSeeksCompaction: true,
	})
}

// maxOpenFiles keeps the table cache well inside the process file limit.
func maxOpenFiles() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return opt.DefaultOpenFilesCacheCapacity
	}
	return max(opt.DefaultOpenFilesCacheCapacity, min(appDBMaxOpenFiles, int(rl.Cur/2)))
}
