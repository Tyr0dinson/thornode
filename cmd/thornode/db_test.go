package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func TestSetAppDBBlockCache(t *testing.T) {
	t.Cleanup(func() { appDBBlockCacheBytes = defaultAppDBBlockCacheMiB * opt.MiB })

	v := viper.New()
	appDBBlockCacheBytes = 1
	require.NoError(t, setAppDBBlockCache(v))
	require.Equal(t, 512*opt.MiB, appDBBlockCacheBytes, "absent key keeps the default")

	v.Set(flagAppDBBlockCacheMiB, 128)
	require.NoError(t, setAppDBBlockCache(v))
	require.Equal(t, 128*opt.MiB, appDBBlockCacheBytes)

	// an unquoted app.toml integer decodes as int64 (loaded as the SDK does)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.toml"), []byte("app-db-block-cache-mib = 64\n"), 0o600))
	fv := viper.New()
	fv.SetConfigType("toml")
	fv.SetConfigName("app")
	fv.AddConfigPath(dir)
	require.NoError(t, fv.MergeInConfig())
	require.IsType(t, int64(0), fv.Get(flagAppDBBlockCacheMiB))
	require.NoError(t, setAppDBBlockCache(fv))
	require.Equal(t, 64*opt.MiB, appDBBlockCacheBytes)

	// THORNODE_APP_DB_BLOCK_CACHE_MIB (like a quoted app.toml value) arrives as a string
	t.Setenv("THORNODE_APP_DB_BLOCK_CACHE_MIB", "256")
	ev := viper.New()
	ev.SetEnvPrefix("THORNODE")
	ev.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	ev.AutomaticEnv()
	require.IsType(t, "", ev.Get(flagAppDBBlockCacheMiB))
	require.NoError(t, setAppDBBlockCache(ev))
	require.Equal(t, 256*opt.MiB, appDBBlockCacheBytes)

	for _, bad := range []any{0, -1} {
		v.Set(flagAppDBBlockCacheMiB, bad)
		err := setAppDBBlockCache(v)
		require.ErrorContains(t, err, "app-db-block-cache-mib must be positive")
		require.Equal(t, 512*opt.MiB, appDBBlockCacheBytes, "a rejected value leaves the default")
	}
}

func TestAppTomlTemplateSetsAppDBBlockCache(t *testing.T) {
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "config", "app.toml.tmpl"))
	require.NoError(t, err)
	require.Contains(t, strings.Split(string(tmpl), "\n"), "app-db-block-cache-mib = 512",
		"app.toml should carry the default so operators can see and change it")
}

func TestOpenAppDBUsesBlockCacheSetting(t *testing.T) {
	t.Cleanup(func() { appDBBlockCacheBytes = defaultAppDBBlockCacheMiB * opt.MiB })
	appDBBlockCacheBytes = 2 * opt.MiB

	db, err := openAppDB(t.TempDir(), dbm.GoLevelDBBackend)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ldb, ok := db.(*dbm.GoLevelDB)
	require.True(t, ok)

	// ~8 MiB of values that snappy cannot shrink, flushed to tables, then all
	// read back: the cached blocks must fit the configured 2 MiB
	value := make([]byte, 4096)
	seed := uint64(1)
	for i := range 2048 {
		for j := range value {
			seed = seed*6364136223846793005 + 1442695040888963407
			value[j] = byte(seed >> 56)
		}
		require.NoError(t, db.Set(fmt.Appendf(nil, "key-%05d", i), value))
	}
	require.NoError(t, ldb.DB().CompactRange(util.Range{}))
	for i := range 2048 {
		got, getErr := db.Get(fmt.Appendf(nil, "key-%05d", i))
		require.NoError(t, getErr)
		require.Len(t, got, len(value))
	}
	cached, err := ldb.DB().GetProperty("leveldb.cachedblock")
	require.NoError(t, err)
	n, err := strconv.Atoi(cached)
	require.NoError(t, err)
	require.Positive(t, n)
	require.LessOrEqual(t, n, 2*opt.MiB)
}
