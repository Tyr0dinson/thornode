package common

import (
	"io/ioutil"
	"path"
	"runtime"
	"strings"
	"testing"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common/cosmos"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type CommonSuite struct{}

var _ = Suite(&CommonSuite{})

func (s CommonSuite) TestGetUncappedShare(c *C) {
	part := cosmos.NewUint(149506590)
	total := cosmos.NewUint(50165561086)
	alloc := cosmos.NewUint(50000000)
	share := GetUncappedShare(part, total, alloc)
	c.Assert(share.Equal(cosmos.NewUint(149013)), Equals, true)
}

func (s CommonSuite) TestGetSafeShare(c *C) {
	part := cosmos.NewUint(14950659000000000)
	total := cosmos.NewUint(50165561086)
	alloc := cosmos.NewUint(50000000)
	share := GetSafeShare(part, total, alloc)
	c.Assert(share.Equal(cosmos.NewUint(50000000)), Equals, true)
}

// GetCurrentVersion - intended for unit tests, fetches the current version of
// THORNode via `version` file
// #nosec G304 this is a method only used for test purpose
func GetCurrentVersion() semver.Version {
	_, filename, _, _ := runtime.Caller(0)
	dir := path.Join(path.Dir(filename), "../")
	dat, err := ioutil.ReadFile(path.Join(dir, "version"))
	if err != nil {
		panic(err)
	}
	v, err := semver.Make(strings.TrimSpace(string(dat)))
	if err != nil {
		panic(err)
	}
	return v
}
