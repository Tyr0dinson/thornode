package thorchain

import (
	"github.com/blang/semver"
)

type LadderDispatch struct {
	versions []semver.Version
	handlers []interface{}
}

// Register a handler for a specific version.
// Enforces descending order of registered versions.
func (l LadderDispatch) Register(version string, handler interface{}) LadderDispatch {
	ver := semver.MustParse(version)
	if len(l.versions) > 0 {
		if l.versions[len(l.versions)-1].LTE(ver) {
			panic("Versions out of order in handler registration")
		}
	}
	l.versions = append(l.versions, ver)
	l.handlers = append(l.handlers, handler)
	return l
}

// Return the most recent handler that supports the target version.
func (l LadderDispatch) Get(targetVersion semver.Version) interface{} {
	for idx, version := range l.versions {
		if targetVersion.GTE(version) {
			return l.handlers[idx]
		}
	}
	return nil
}
