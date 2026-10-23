package main

import "testing"

// Legacy hosts keep their scheme, except tcp://, which the endpoint router reads as
// http:// (as CometBFT's own client does); a bare host gets http://.
func TestWithScheme(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"node:8545":         "http://node:8545",
		"tcp://gaia:26657":  "http://gaia:26657",
		"TCP://gaia:26657":  "http://gaia:26657",
		"http://node:8545":  "http://node:8545",
		"HTTPS://node:8545": "HTTPS://node:8545",
		"ws://node:8546":    "ws://node:8546", // rejected by ValidateEndpoints before this runs
		// a bare host whose query holds "://" has no scheme of its own
		"user:secret@node:8545/x?u=http://y": "http://user:secret@node:8545/x?u=http://y",
	} {
		if got := withScheme(in); got != want {
			t.Errorf("withScheme(%q) = %q, want %q", in, got, want)
		}
	}
}
