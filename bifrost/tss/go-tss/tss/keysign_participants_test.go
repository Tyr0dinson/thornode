package tss

import (
	"strings"
	"testing"
)

func TestKeysignParticipantsUsesRequestedSubset(t *testing.T) {
	all := []string{"member-a", "member-b", "member-c", "member-d"}
	requested := []string{"member-a", "member-c", "member-d"}

	got, err := keysignParticipants(all, requested)
	if err != nil {
		t.Fatalf("keysignParticipants returned error: %v", err)
	}
	if strings.Join(got, ",") != "member-a,member-c,member-d" {
		t.Fatalf("expected requested subset order, got %v", got)
	}
}

func TestKeysignParticipantsDefaultsToAllParticipants(t *testing.T) {
	all := []string{"member-a", "member-b", "member-c"}

	got, err := keysignParticipants(all, nil)
	if err != nil {
		t.Fatalf("keysignParticipants returned error: %v", err)
	}
	if strings.Join(got, ",") != "member-a,member-b,member-c" {
		t.Fatalf("expected all participants, got %v", got)
	}
}

func TestKeysignParticipantsRejectsUnknownSigner(t *testing.T) {
	_, err := keysignParticipants([]string{"member-a", "member-b"}, []string{"member-a", "member-c"})
	if err == nil {
		t.Fatal("expected unknown signer error")
	}
	if !strings.Contains(err.Error(), "is not a vault participant") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestKeysignParticipantsRejectsDuplicateSigner(t *testing.T) {
	_, err := keysignParticipants([]string{"member-a", "member-b"}, []string{"member-a", "MEMBER-A"})
	if err == nil {
		t.Fatal("expected duplicate signer error")
	}
	if !strings.Contains(err.Error(), "duplicate requested keysign signer") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestContainsSignerPubKeyNormalizes(t *testing.T) {
	if !containsSignerPubKey([]string{" member-a "}, "MEMBER-A") {
		t.Fatal("expected normalized signer to match")
	}
}
