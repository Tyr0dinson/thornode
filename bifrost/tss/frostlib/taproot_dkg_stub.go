//go:build !stagenet && !mocknet
// +build !stagenet,!mocknet

package frostlib

// NewTaprootDKGEngine always fails: mainnet and chainnet builds do not contain
// the go-frost library or any of the FROST keygen code.
func NewTaprootDKGEngine(TaprootDKGConfig) (TaprootDKGEngine, error) {
	return nil, ErrNotLinked
}

// JudgeShareDispute always fails in builds without the FROST keygen code.
func JudgeShareDispute(ShareDisputeCase) (ShareDisputeVerdict, []byte, error) {
	return 0, nil, ErrNotLinked
}
