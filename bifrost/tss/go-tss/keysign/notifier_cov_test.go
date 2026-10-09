package keysign

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	cosed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32"
	"github.com/stretchr/testify/require"
	tsslibcommon "gitlab.com/thorchain/tss/tss-lib-private.git/v3/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
)

const notifierCovSecpPoolPubKey = `thorpub1addwnpepq0ul3xt882a6nm6m7uhxj4tk2n82zyu647dyevcs5yumuadn4uamqx7neak`

func loadNotifierCovSig(t *testing.T, file string) *tsslibcommon.SignatureData {
	t.Helper()
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	var sig tsslibcommon.SignatureData
	require.NoError(t, json.Unmarshal(content, &sig))
	return &sig
}

// tamperedNotifierCovSig returns the valid fixture signature with S flipped,
// so it is well-formed but does not verify.
func tamperedNotifierCovSig(t *testing.T) *tsslibcommon.SignatureData {
	t.Helper()
	sig := loadNotifierCovSig(t, "../test_data/signature_notify/sig1.json")
	sig.S = append([]byte(nil), sig.S...)
	sig.S[len(sig.S)-1] ^= 0x01
	return sig
}

func notifierCovSecpMsg(t *testing.T) []byte {
	t.Helper()
	buf, err := base64.StdEncoding.DecodeString("yhEwrxWuNBGnPT/L7PNnVWg7gFWNzCYTV+GuX3tKRH8=")
	require.NoError(t, err)
	return buf
}

func TestNotifierCovVerifySignatureSecp256k1(t *testing.T) {
	conversion.SetupBech32Prefix()
	msg := notifierCovSecpMsg(t)
	n, err := newNotifier("msg-id", [][]byte{msg}, notifierCovSecpPoolPubKey, nil)
	require.NoError(t, err)

	valid := loadNotifierCovSig(t, "../test_data/signature_notify/sig1.json")
	require.NoError(t, n.verifySignature(valid, msg))

	invalid := tamperedNotifierCovSig(t)
	err = n.verifySignature(invalid, msg)
	require.ErrorContains(t, err, "secp256k1 signature did not verify")

	// the valid signature must not verify against a different digest
	other := sha256.Sum256([]byte("another message"))
	require.ErrorContains(t, n.verifySignature(valid, other[:]), "did not verify")
}

func TestNotifierCovVerifySignatureEd25519(t *testing.T) {
	conversion.SetupBech32Prefix()
	priv := cosed25519.GenPrivKey()
	poolPubKey, err := sdk.MarshalPubKey(sdk.AccPK, priv.PubKey()) //nolint:staticcheck
	require.NoError(t, err)

	msg := []byte("ed25519 notifier coverage message")
	sigBytes, err := priv.Sign(msg)
	require.NoError(t, err)

	n, err := newNotifier("msg-id", [][]byte{msg}, poolPubKey, nil)
	require.NoError(t, err)

	require.NoError(t, n.verifySignature(&tsslibcommon.SignatureData{Signature: sigBytes}, msg))

	bad := append([]byte(nil), sigBytes...)
	bad[0] ^= 0xff
	require.ErrorContains(t, n.verifySignature(&tsslibcommon.SignatureData{Signature: bad}, msg),
		"ed25519 signature did not verify")

	// processSignature verifies and publishes the accepted signatures
	data := []*tsslibcommon.SignatureData{{Signature: sigBytes}}
	require.NoError(t, n.processSignature(data))
	require.True(t, n.processed)
	require.False(t, n.readyToProcess())
	select {
	case got := <-n.resp:
		require.Equal(t, data, got)
	default:
		t.Fatal("expected signatures to be delivered on resp")
	}
}

func TestNotifierCovProcessSignatureCountMismatch(t *testing.T) {
	conversion.SetupBech32Prefix()
	msg := notifierCovSecpMsg(t)
	n, err := newNotifier("msg-id", [][]byte{msg}, notifierCovSecpPoolPubKey, nil)
	require.NoError(t, err)

	sig := loadNotifierCovSig(t, "../test_data/signature_notify/sig1.json")
	err = n.processSignature([]*tsslibcommon.SignatureData{sig, sig})
	require.ErrorContains(t, err, "signature count (2) != message count (1)")
	require.False(t, n.processed)

	// a wrong signature is rejected with the index in the error
	invalid := tamperedNotifierCovSig(t)
	err = n.processSignature([]*tsslibcommon.SignatureData{invalid})
	require.ErrorContains(t, err, "error verifying signature (1 of 1)")
	require.False(t, n.processed)

	// a valid one completes processing
	require.NoError(t, n.processSignature([]*tsslibcommon.SignatureData{sig}))
	require.True(t, n.processed)
	require.Len(t, <-n.resp, 1)
}

func TestNotifierCovReadyToProcessAndUpdateUnset(t *testing.T) {
	n, err := newNotifier("msg-id", nil, "", nil)
	require.NoError(t, err)
	require.Equal(t, defaultNotifierTTL, n.ttl)
	require.False(t, n.readyToProcess())

	sigs := []*tsslibcommon.SignatureData{{Signature: []byte("sig")}}
	msgs := [][]byte{[]byte("m")}
	n.updateUnset(msgs, "pool", sigs)
	require.Equal(t, msgs, n.messages)
	require.Equal(t, "pool", n.poolPubKey)
	require.Equal(t, sigs, n.signatures)
	require.True(t, n.readyToProcess())

	// already-set fields are not overwritten
	n.updateUnset([][]byte{[]byte("other")}, "other", []*tsslibcommon.SignatureData{})
	require.Equal(t, msgs, n.messages)
	require.Equal(t, "pool", n.poolPubKey)
	require.Equal(t, sigs, n.signatures)

	n.processed = true
	require.False(t, n.readyToProcess())
}

func TestNotifierCovErrorPaths(t *testing.T) {
	conversion.SetupBech32Prefix()
	n, err := newNotifier("", nil, "", nil)
	require.ErrorContains(t, err, "messageID is empty")
	require.Nil(t, n)

	n, err = newNotifier("msg-id", [][]byte{[]byte("m")}, "not-a-bech32-pubkey", nil)
	require.NoError(t, err)
	err = n.verifySignature(&tsslibcommon.SignatureData{Signature: []byte("s")}, []byte("m"))
	require.ErrorContains(t, err, "fail to get pubkey from bech32 pubkey string(not-a-bech32-pubkey)")

	// empty data is a no-op: nothing processed and nothing delivered
	require.NoError(t, n.processSignature(nil))
	require.False(t, n.processed)
	require.Len(t, n.resp, 0)

	// a nil signature is a keysign failure
	err = n.processSignature([]*tsslibcommon.SignatureData{{}})
	require.ErrorContains(t, err, "keysign failed with nil signature")
	require.False(t, n.processed)
}
