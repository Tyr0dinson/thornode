package tss

import (
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
)

func TestKeysignLeaderRotationVersionGate(t *testing.T) {
	cases := []struct {
		version string
		want    bool
		wantErr bool
	}{
		{"3.20.3", false, false}, // pre-gate bifrost patch
		{"3.21.0", false, false}, // the release before the gate
		{"3.21.1", true, false},  // gate opens exactly here
		{"3.21.2", true, false},  // later patch
		{"3.22.0", true, false},  // later minor
		{"not-a-version", false, true},
	}
	for _, c := range cases {
		got, err := keysignLeaderRotationEnabled(c.version)
		if c.wantErr {
			if err == nil {
				t.Fatalf("version %q: expected parse error", c.version)
			}
			continue
		}
		if err != nil {
			t.Fatalf("version %q: unexpected error %v", c.version, err)
		}
		if got != c.want {
			t.Fatalf("version %q: enabled=%v, want %v", c.version, got, c.want)
		}
	}
}

func TestKeysignJoinAttemptIDDeterministicAttemptScopedAndAlgoDistinct(t *testing.T) {
	peers := []string{"peer-b", "peer-a", "peer-c"}
	first, err := keysignJoinAttemptID("stable-msg-id", "leader-a", peers, 0)
	if err != nil {
		t.Fatalf("first attempt ID: %v", err)
	}
	// deterministic + order-independent in the peer set.
	repeated, err := keysignJoinAttemptID("stable-msg-id", "leader-a", []string{"peer-c", "peer-b", "peer-a"}, 0)
	if err != nil {
		t.Fatalf("repeated attempt ID: %v", err)
	}
	if first != repeated {
		t.Fatalf("join attempt ID is not deterministic: %q != %q", first, repeated)
	}
	// marked ephemeral so the coordinator resets an early/stood-down request.
	if !strings.HasPrefix(first, p2p.FrostJoinAttemptIDPrefix) {
		t.Fatalf("join attempt ID %q is not marked ephemeral", first)
	}
	// a rotated leader / later attempt must use a fresh session id.
	next, err := keysignJoinAttemptID("stable-msg-id", "leader-b", peers, 1)
	if err != nil {
		t.Fatalf("next attempt ID: %v", err)
	}
	if first == next {
		t.Fatal("rotated leader reused the previous join session ID")
	}
	// domain separation: the GG20 id must differ from the FROST id for identical inputs,
	// so the two protocols can never collide on a shared msgID.
	frostID, err := frostJoinAttemptID("stable-msg-id", "leader-a", peers, 0)
	if err != nil {
		t.Fatalf("frost attempt ID: %v", err)
	}
	if first == frostID {
		t.Fatal("keysign and FROST join attempt IDs collide for identical inputs")
	}
	// input validation.
	if _, err := keysignJoinAttemptID("", "leader-a", peers, 0); err == nil {
		t.Fatal("expected empty msgID to be rejected")
	}
	if _, err := keysignJoinAttemptID("m", "", peers, 0); err == nil {
		t.Fatal("expected empty leader to be rejected")
	}
	if _, err := keysignJoinAttemptID("m", "leader-a", nil, 0); err == nil {
		t.Fatal("expected empty peer set to be rejected")
	}
	if _, err := keysignJoinAttemptID("m", "leader-a", []string{"p", "p"}, 0); err == nil {
		t.Fatal("expected duplicate peers to be rejected")
	}
}
