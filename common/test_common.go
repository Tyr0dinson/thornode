package common

import (
	"io/ioutil"
	"path"
	"runtime"
	"strings"

	"github.com/blang/semver"
)

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
