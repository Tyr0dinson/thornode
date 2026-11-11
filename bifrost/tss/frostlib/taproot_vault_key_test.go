//go:build stagenet || mocknet
// +build stagenet mocknet

package frostlib

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	"gitlab.com/thorchain/tss/thor-go-frost/gofrost"

	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// A vault key from a real go-frost DKG round-trips through the LTC Taproot
// address model: stored as a common.PubKey (the compressed group key), it
// yields the P2TR address whose witness program is exactly the DKG's Taproot
// output key; every keyshare validates against the key read back out of that
// PubKey; and a threshold signature by the key verifies against the program.
func TestFrostVaultKeyMatchesTaprootAddress(t *testing.T) {
	participants := []string{"thorpub-c", "thorpub-a", "thorpub-b"}
	const minSigners = 2
	results := runKeygen(t, participants, minSigners)
	defer func() {
		for _, r := range results {
			gofrost.Wipe(r.Keyshare)
		}
	}()

	group := results[participants[0]]
	for p, r := range results {
		if !bytes.Equal(r.PublicKeyCompressed, group.PublicKeyCompressed) {
			t.Fatalf("participant %s derived a different group key", p)
		}
	}

	bech, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, &secp256k1.PubKey{Key: group.PublicKeyCompressed})
	if err != nil {
		t.Fatal(err)
	}
	vaultKey, err := common.NewPubKey(bech)
	if err != nil {
		t.Fatal(err)
	}
	if algo, algoErr := vaultKey.KeyAlgo(); algoErr != nil || algo != common.SigningAlgoSecp256k1 {
		t.Fatalf("vault key algo %q, err %v", algo, algoErr)
	}
	addr, err := vaultKey.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := vaultKey.GetAddress(common.LTCChain)
	if err != nil {
		t.Fatal(err)
	}
	if addr.Equals(legacy) {
		t.Fatalf("taproot address %s must differ from the P2WPKH address of the same key", addr)
	}

	decoded, err := ltctxscript.DecodeAddress(addr.String(), ltcNetParams(t))
	if err != nil {
		t.Fatalf("decode %s: %v", addr, err)
	}
	taproot, ok := decoded.(*ltctxscript.AddressTaproot)
	if !ok {
		t.Fatalf("%s decoded as %T, want a taproot address", addr, decoded)
	}
	program := taproot.ScriptAddress()
	if !bytes.Equal(program, group.OutputKeyXOnly) {
		t.Fatalf("address program %x is not the DKG taproot output key %x", program, group.OutputKeyXOnly)
	}

	for _, p := range participants {
		if _, err := gofrost.ValidateKeyshareForTaproot(results[p].Keyshare, p, program); err != nil {
			t.Fatalf("keyshare of %s does not validate against the vault key: %v", p, err)
		}
	}

	digest := sha256.Sum256([]byte("stand-in for a BIP-341 key-path sighash"))
	sig := runSign(t, participants[:minSigners], results, program, digest[:])
	if err := gofrost.VerifyTaproot(program, digest[:], sig); err != nil {
		t.Fatal(err)
	}
	if n := gofrost.SessionActiveCount(); n != 0 {
		t.Fatalf("%d go-frost sessions still active after the ceremonies", n)
	}
}

func ltcNetParams(t *testing.T) *ltcchaincfg.Params {
	t.Helper()
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &ltcchaincfg.RegressionNetParams
	case common.StageNet:
		return &ltcchaincfg.MainNetParams
	default:
		t.Fatalf("unexpected network %v in a stagenet/mocknet build", common.CurrentChainNetwork)
		return nil
	}
}

// runKeygen runs one in-process DKG with every participant and returns each
// participant's consumed result. Callers wipe the keyshares.
func runKeygen(t *testing.T, participants []string, minSigners uint16) map[string]gofrost.TaprootKeygenResult {
	t.Helper()
	sessionID := t.Name() + "/keygen"
	handles := make(map[string]gofrost.Handle, len(participants))
	for _, p := range participants {
		h, err := gofrost.KeygenSessionNew(sessionID, participants, p, minSigners)
		if err != nil {
			t.Fatal(err)
		}
		handles[p] = h
	}
	deliverUntilReady(t, handles)
	results := make(map[string]gofrost.TaprootKeygenResult, len(participants))
	for p, h := range handles {
		r, err := gofrost.FinishTaprootKeygenAndFree(h, p)
		if err != nil {
			t.Fatal(err)
		}
		results[p] = r
	}
	return results
}

// runSign signs digest with the given signers and returns the one signature
// they all produce.
func runSign(t *testing.T, signers []string, results map[string]gofrost.TaprootKeygenResult, outputKey, digest []byte) []byte {
	t.Helper()
	sessionID := t.Name() + "/sign"
	handles := make(map[string]gofrost.Handle, len(signers))
	for _, p := range signers {
		h, err := gofrost.SignSessionNewForTaproot(sessionID, signers, p, results[p].Keyshare, outputKey, digest)
		if err != nil {
			t.Fatal(err)
		}
		handles[p] = h
	}
	deliverUntilReady(t, handles)
	var sig []byte
	for _, p := range signers {
		r, err := gofrost.FinishTaprootSignAndFree(handles[p], sessionID, outputKey, digest)
		if err != nil {
			t.Fatal(err)
		}
		if sig == nil {
			sig = r.Signature
		} else if !bytes.Equal(sig, r.Signature) {
			t.Fatalf("signer %s produced a different signature", p)
		}
	}
	return sig
}

// deliverUntilReady moves every queued message to its listed receivers, with
// the sender taken from the sending handle as an authenticated transport would,
// until every session is ready to finish.
func deliverUntilReady(t *testing.T, handles map[string]gofrost.Handle) {
	t.Helper()
	for round := 0; round < 100; round++ {
		delivered := false
		for from, h := range handles {
			msgs, err := gofrost.DrainSessionOutputMessages(h)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range msgs {
				delivered = true
				for _, to := range m.Receivers {
					target, ok := handles[to]
					if !ok {
						t.Fatalf("message from %s to unknown participant %s", from, to)
					}
					if _, err := gofrost.InputBoundSessionMessage(target, m.Protocol, m.SessionID, from, m.Message); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		if delivered {
			continue
		}
		ready := true
		for _, h := range handles {
			progress, err := gofrost.GetSessionProgress(h)
			if err != nil {
				t.Fatal(err)
			}
			if !progress.ReadyToFinish {
				ready = false
			}
		}
		if ready {
			return
		}
	}
	t.Fatal("go-frost sessions did not finish")
}
