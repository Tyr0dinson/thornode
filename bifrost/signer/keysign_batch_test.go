package signer

import (
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// The fake TSS servers in these tests answer at once; keep the keysign
// batching but skip its real 1s wait for a quiet queue.
func init() {
	tss.KeySignBatchInterval = 10 * time.Millisecond
}

func TestKeySignUsesBatchInterval(t *testing.T) {
	// A key in the current bech32 config, so this test leaves the config alone.
	poolPubKey := ttypes.GetRandomPubKey()
	// The keygen self-signature is the signing intent that needs no scheduled
	// TxOut from the bridge, so the message is the pool pubkey itself.
	msg := poolPubKey.String()
	ks, err := tss.NewKeySign(newFakeTss(msg, true), fakeBridge{nil})
	if err != nil {
		t.Fatal(err)
	}
	ks.Start()
	defer ks.Stop()

	intent := tss.NewKeygenVerificationSigningIntent(poolPubKey)
	intent.Height = 1
	start := time.Now()
	sig, _, err := ks.RemoteSign([]byte(msg), common.SigningAlgoSecp256k1, poolPubKey.String(), intent)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature length %d, want 64", len(sig))
	}
	// The default interval alone takes a second.
	if elapsed >= time.Second {
		t.Fatalf("keysign took %s, want it to follow KeySignBatchInterval (%s)", elapsed, tss.KeySignBatchInterval)
	}
}
