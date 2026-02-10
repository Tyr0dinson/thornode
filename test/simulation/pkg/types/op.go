package types

import (
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
)

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

// OpConfig is the configuration passed to each operation during execution.
type OpConfig struct {
	// NodeUsers is a slice clients for simulation validator keys.
	NodeUsers []*User

	// Users is a slice of clients for simulation user keys.
	Users []*User

	// solvencyHaltLogOnly controls whether solvency halt watcher should only log.
	solvencyHaltLogOnly atomic.Bool
}

// SetSolvencyHaltLogOnly sets the solvency halt watcher mode.
func (c *OpConfig) SetSolvencyHaltLogOnly(enabled bool) {
	c.solvencyHaltLogOnly.Store(enabled)
}

// SolvencyHaltLogOnly returns true when solvency halt watcher should only log.
func (c *OpConfig) SolvencyHaltLogOnly() bool {
	return c.solvencyHaltLogOnly.Load()
}

// OpResult is the result of an operation.
type OpResult struct {
	// Continue indicates that actor should continue to the next operation.
	Continue bool

	// Finish indicates that the actor should stop executing and return the error.
	Finish bool

	// Error is the error returned by the operation.
	Error error
}

// Op is an operation that can be executed by an actor.
type Op func(config *OpConfig) OpResult

// String returns the function name of the operation.
func (op Op) String() string {
	fqName := runtime.FuncForPC(reflect.ValueOf(op).Pointer()).Name()
	parts := strings.Split(fqName, "/")
	return strings.Split(parts[len(parts)-1], "-")[0]
}
