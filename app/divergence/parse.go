package divergence

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	HashTypeAppHash         = "AppHash"
	HashTypeLastResultsHash = "LastResultsHash"
)

// mismatchRe matches CometBFT v0.38 state/validation.go:
//
//	wrong Block.Header.AppHash.  Expected %X, got %v
//	wrong Block.Header.LastResultsHash.  Expected %X, got %v
//
// Expected is this node's own state (state.AppHash / state.LastResultsHash);
// got is the block header's, i.e. the network's. Both print as uppercase hex
// (%v on HexBytes uses its String method).
var mismatchRe = regexp.MustCompile(`wrong Block\.Header\.(AppHash|LastResultsHash)\.\s+Expected ([0-9A-Fa-f]*), got ([0-9A-Fa-f]*)`)

// Mismatch is a parsed header-hash validation error.
type Mismatch struct {
	// Type is HashTypeAppHash or HashTypeLastResultsHash.
	Type string `json:"type"`
	// Local is this node's hash ("Expected" in the CometBFT message).
	Local string `json:"local"`
	// Network is the block header's hash ("got" in the CometBFT message).
	Network string `json:"network"`
}

// ParseMismatch extracts a Mismatch from any string that contains a CometBFT
// header hash validation error, such as the consensus panic
// "+2/3 committed an invalid block: wrong Block.Header.AppHash. ...".
func ParseMismatch(s string) (*Mismatch, bool) {
	m := mismatchRe.FindStringSubmatch(s)
	if m == nil {
		return nil, false
	}
	return &Mismatch{Type: m[1], Local: strings.ToUpper(m[2]), Network: strings.ToUpper(m[3])}, true
}

// IsHashMismatch reports whether s mentions an AppHash or LastResultsHash
// header validation failure.
func IsHashMismatch(s string) bool {
	return strings.Contains(s, "wrong Block.Header.AppHash") || strings.Contains(s, "wrong Block.Header.LastResultsHash")
}

// errString renders a logged err keyval (an error, a recovered panic value, or
// a string) as text.
func errString(v any) string {
	switch e := v.(type) {
	case nil:
		return ""
	case error:
		return e.Error()
	case string:
		return e
	case fmt.Stringer:
		return e.String()
	default:
		return fmt.Sprint(v)
	}
}

// ErrFromKeyvals returns the value logged under the "err" key, as text.
func ErrFromKeyvals(keyvals []any) string {
	for i := 0; i+1 < len(keyvals); i += 2 {
		if k, ok := keyvals[i].(string); ok && k == "err" {
			return errString(keyvals[i+1])
		}
	}
	return ""
}

// DivergentHeight maps the height of the block whose header failed validation
// to the committed height whose state diverged.
//
// CometBFT checks the app hash one block late: the header of block H carries
// AppHash and LastResultsHash as produced by executing and committing block
// H-1. When validating block H fails with "wrong Block.Header.AppHash", this
// node's state after committing H-1 differs from the network's (the state at
// H-2 still agreed, or block H-1's header would have failed instead). So the
// diverging committed height, whose write set and commit info explain the
// mismatch, is H-1, which is also this node's last committed height at the
// time of the failure.
func DivergentHeight(failingHeight int64) int64 {
	if failingHeight <= 1 {
		return 0
	}
	return failingHeight - 1
}
