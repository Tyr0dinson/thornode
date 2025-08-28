package observer

import "testing"

func TestDetermineCollisionWinner_Simple(t *testing.T) {
    candidates := []CollisionCandidate{
        {ChainID: "BTC", VaultPubKey: "v1", Reference: "12345", InboundHeight: 100, TxID: "b"},
        {ChainID: "BTC", VaultPubKey: "v1", Reference: "12345", InboundHeight:  99, TxID: "z"},
        {ChainID: "BTC", VaultPubKey: "v1", Reference: "12345", InboundHeight: 100, TxID: "a"},
    }
    winner, losers := DetermineCollisionWinner(candidates)
    if winner.InboundHeight != 99 || winner.TxID != "z" {
        t.Fatalf("expected (99,z) winner, got (%d,%s)", winner.InboundHeight, winner.TxID)
    }
    if len(losers) != 2 {
        t.Fatalf("expected 2 losers, got %d", len(losers))
    }
}

func TestDetermineCollisionWinner_TieOnHeight(t *testing.T) {
    candidates := []CollisionCandidate{
        {InboundHeight: 100, TxID: "b"},
        {InboundHeight: 100, TxID: "a"},
        {InboundHeight: 100, TxID: "c"},
    }
    winner, _ := DetermineCollisionWinner(candidates)
    if winner.InboundHeight != 100 || winner.TxID != "a" {
        t.Fatalf("expected (100,a) winner, got (%d,%s)", winner.InboundHeight, winner.TxID)
    }
}

func TestDetermineCollisionWinner_Empty(t *testing.T) {
    var candidates []CollisionCandidate
    winner, losers := DetermineCollisionWinner(candidates)
    if (winner != CollisionCandidate{}) {
        t.Fatalf("expected zero-value winner, got %+v", winner)
    }
    if losers != nil {
        t.Fatalf("expected nil losers, got %#v", losers)
    }
}

func TestDetermineCollisionWinner_Permutations(t *testing.T) {
    base := []CollisionCandidate{
        {InboundHeight: 101, TxID: "c"},
        {InboundHeight:  99, TxID: "z"}, // expected winner
        {InboundHeight: 100, TxID: "a"},
        {InboundHeight: 100, TxID: "b"},
    }

    // Several permutations should yield the same winner
    perms := [][]CollisionCandidate{
        base,
        {base[3], base[2], base[1], base[0]},
        {base[1], base[0], base[3], base[2]},
        {base[2], base[1], base[0], base[3]},
    }

    for i, cands := range perms {
        winner, _ := DetermineCollisionWinner(cands)
        if winner.InboundHeight != 99 || winner.TxID != "z" {
            t.Fatalf("perm %d: expected (99,z) winner, got (%d,%s)", i, winner.InboundHeight, winner.TxID)
        }
    }
}
