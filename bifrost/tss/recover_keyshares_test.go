package tss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	rpcclient "github.com/cometbft/cometbft/rpc/client"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	cmttypes "github.com/cometbft/cometbft/types"
	cosmosclient "github.com/cosmos/cosmos-sdk/client"
	ctypes "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	protov2 "google.golang.org/protobuf/proto"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type RecoverKeysharesSuite struct{}

var _ = Suite(&RecoverKeysharesSuite{})

func (s *RecoverKeysharesSuite) TestRecoverScanLowerBound(c *C) {
	c.Assert(recoverScanLowerBound(0, 100), Equals, int64(0))
	c.Assert(recoverScanLowerBound(50, 100), Equals, int64(0))
	c.Assert(recoverScanLowerBound(500, 100), Equals, int64(400))
}

func (s *RecoverKeysharesSuite) TestRecoverScanBoundsIncludesBoundedForwardWindow(c *C) {
	lower, upper := recoverScanBounds(648, 680, 100)
	c.Check(lower, Equals, int64(548))
	c.Check(upper, Equals, int64(680))

	lower, upper = recoverScanBounds(648, 900, 100)
	c.Check(lower, Equals, int64(548))
	c.Check(upper, Equals, int64(748))

	lower, upper = recoverScanBounds(648, 600, 100)
	c.Check(lower, Equals, int64(548))
	c.Check(upper, Equals, int64(648))
}

func (s *RecoverKeysharesSuite) TestApplyPruneFloor(c *C) {
	// Whole range below the node floor → unrecoverable, lower bound unchanged.
	// (legacy vault ~17.4M vs node floor ~20.4M: the exact wedge this fixes.)
	lower, clamped, unrecoverable := applyPruneFloor(17_400_000, 18_300_000, 20_395_001)
	c.Check(unrecoverable, Equals, true)
	c.Check(clamped, Equals, false)
	c.Check(lower, Equals, int64(17_400_000))

	// Range straddles the floor → clamp lower to earliest-1, still scannable —
	// but the pruned part of the window is gone, so a subsequent scan miss must
	// be classified unrecoverable (retrying only loses more history), not as a
	// transient not-found that wedges the foreground retry loop.
	lower, clamped, unrecoverable = applyPruneFloor(500, 800, 601)
	c.Check(unrecoverable, Equals, false)
	c.Check(clamped, Equals, true)
	c.Check(lower, Equals, int64(600))

	// Range fully above the floor → untouched.
	lower, clamped, unrecoverable = applyPruneFloor(700, 900, 601)
	c.Check(unrecoverable, Equals, false)
	c.Check(clamped, Equals, false)
	c.Check(lower, Equals, int64(700))

	// Floor unknown (earliest <= 0) → disabled, no change, never unrecoverable.
	lower, clamped, unrecoverable = applyPruneFloor(500, 800, 0)
	c.Check(unrecoverable, Equals, false)
	c.Check(clamped, Equals, false)
	c.Check(lower, Equals, int64(500))
}

func (s *RecoverKeysharesSuite) TestScanMissSentinelDependsOnClamping(c *C) {
	// Full window scanned, nothing found → possibly transient (flaky RPC collapsed
	// the window, message just outside) → retryable.
	c.Check(errors.Is(scanMissSentinel(false), errRecoveryBackupNotFound), Equals, true)
	// Part of the window was pruned away → the message may sit in the lost part and
	// retrying can only lose more history → definitively unrecoverable from this
	// node (degraded start + background retry, where repointing at an archive
	// endpoint widens the window again).
	c.Check(errors.Is(scanMissSentinel(true), errRecoveryBackupUnrecoverable), Equals, true)
	c.Check(errors.Is(scanMissSentinel(true), errRecoveryBackupNotFound), Equals, false)
}

// fakeRecoveryTx is a minimal sdk.Tx for exercising backupFromRawTx's message
// matching without building a real signed tx.
type fakeRecoveryTx struct{ msgs []ctypes.Msg }

func (f fakeRecoveryTx) GetMsgs() []ctypes.Msg                 { return f.msgs }
func (f fakeRecoveryTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func (s *RecoverKeysharesSuite) TestNotYetSyncedKeyshareHeight(c *C) {
	syncing := &recoveryBackupNotYetSyncedError{requiredHeight: 1234, currentHeight: 1000, target: types.GetRandomPubKey()}
	h, ok := NotYetSyncedKeyshareHeight(syncing)
	c.Check(ok, Equals, true)
	c.Check(h, Equals, int64(1234))
	// Still recognised when wrapped or joined alongside other per-vault errors.
	h, ok = NotYetSyncedKeyshareHeight(fmt.Errorf("wrap: %w", syncing))
	c.Check(ok, Equals, true)
	c.Check(h, Equals, int64(1234))
	h, ok = NotYetSyncedKeyshareHeight(errors.Join(fmt.Errorf("transient"), syncing))
	c.Check(ok, Equals, true)
	c.Check(h, Equals, int64(1234))
	// Several not-yet-synced vaults at different heights -> the LOWEST is returned, so
	// the one-shot trigger wakes at the earliest recoverable height (the re-run then
	// re-arms for any still above it) rather than stalling behind a higher sibling,
	// regardless of tree order (higher listed first here).
	hi := &recoveryBackupNotYetSyncedError{requiredHeight: 5000, currentHeight: 1000, target: types.GetRandomPubKey()}
	lo := &recoveryBackupNotYetSyncedError{requiredHeight: 2500, currentHeight: 1000, target: types.GetRandomPubKey()}
	h, ok = NotYetSyncedKeyshareHeight(errors.Join(fmt.Errorf("wrap-hi: %w", hi), fmt.Errorf("wrap-lo: %w", lo)))
	c.Check(ok, Equals, true)
	c.Check(h, Equals, int64(2500))
	// Non-sync errors are not not-yet-synced.
	_, ok = NotYetSyncedKeyshareHeight(fmt.Errorf("wrap: %w", errRecoveryBackupUnrecoverable))
	c.Check(ok, Equals, false)
	_, ok = NotYetSyncedKeyshareHeight(nil)
	c.Check(ok, Equals, false)
}

func (s *RecoverKeysharesSuite) TestIsPermanentKeyshareRecoveryFailure(c *C) {
	// Pruned below the block-store floor → permanent: degraded start + background
	// supervisor (an operator restore / archive RPC is the resolution, not a retry).
	c.Check(IsPermanentKeyshareRecoveryFailure(fmt.Errorf("wrap: %w", errRecoveryBackupUnrecoverable)), Equals, true)
	// Backup never posted in the fully-available scan window → permanent, and must
	// NOT loop the pre-startup barrier forever (the 3.20.0 signing-stall class):
	// rescanning the same window cannot find what is not there.
	c.Check(IsPermanentKeyshareRecoveryFailure(fmt.Errorf("wrap: %w", errRecoveryBackupNotFound)), Equals, true)
	// Any permanent member in a join is enough to start degraded.
	c.Check(IsPermanentKeyshareRecoveryFailure(errors.Join(fmt.Errorf("transient"), errRecoveryBackupNotFound)), Equals, true)
	// A not-yet-synced backup is explicitly NOT permanent — it has its own one-shot
	// height trigger and must not be routed to the permanent supervisor path.
	syncing := &recoveryBackupNotYetSyncedError{requiredHeight: 9, currentHeight: 1, target: types.GetRandomPubKey()}
	c.Check(IsPermanentKeyshareRecoveryFailure(syncing), Equals, false)
	// MIXED permanent + not-yet-synced: the syncing member must NOT mask the
	// permanent one, or startup would arm only the one-shot height trigger and
	// postpone the permanent vault's recovery to an unrelated sync height.
	c.Check(IsPermanentKeyshareRecoveryFailure(errors.Join(errRecoveryBackupUnrecoverable, syncing)), Equals, true)
	// Same via the per-target wrap shape recoverKeyShares builds
	// (errors.Join of fmt.Errorf("...: %w", per-target errors.Join)).
	c.Check(IsPermanentKeyshareRecoveryFailure(errors.Join(
		fmt.Errorf("recover keyshares for a: %w", errors.Join(errRecoveryBackupNotFound)),
		fmt.Errorf("recover keyshares for b: %w", errors.Join(syncing)),
	)), Equals, true)
	// A wrapped join of ONLY not-yet-synced stays non-permanent (height trigger).
	c.Check(IsPermanentKeyshareRecoveryFailure(fmt.Errorf("recover keyshares for b: %w", errors.Join(syncing))), Equals, false)
	// DOUBLY-wrapped join (a context wrap around the per-target wrap): an extra
	// layer must not let the syncing sibling mask the permanent member — the walk
	// descends every wrap to the leaves.
	c.Check(IsPermanentKeyshareRecoveryFailure(
		fmt.Errorf("ctx: %w", fmt.Errorf("recover keyshares for a: %w", errors.Join(errRecoveryBackupUnrecoverable, syncing))),
	), Equals, true)
	// A purely transient failure (RPC/disk) → not permanent; the pre-startup loop
	// retries it because a retry can still fix it.
	c.Check(IsPermanentKeyshareRecoveryFailure(fmt.Errorf("fail to get block")), Equals, false)
	c.Check(IsPermanentKeyshareRecoveryFailure(nil), Equals, false)
}

func (s *RecoverKeysharesSuite) TestHasRetryableKeyshareComponent(c *C) {
	syncing := &recoveryBackupNotYetSyncedError{requiredHeight: 2, currentHeight: 1, target: types.GetRandomPubKey()}
	// A genuinely transient leaf (RPC/disk) is foreground-retryable.
	c.Check(HasRetryableKeyshareComponent(errors.New("fail to get block")), Equals, true)
	// Terminal / wait classes are not retryable in the foreground.
	c.Check(HasRetryableKeyshareComponent(fmt.Errorf("w: %w", errRecoveryBackupUnrecoverable)), Equals, false)
	c.Check(HasRetryableKeyshareComponent(fmt.Errorf("w: %w", errRecoveryBackupNotFound)), Equals, false)
	c.Check(HasRetryableKeyshareComponent(syncing), Equals, false)
	// A transient joined with a terminal/wait member keeps the WHOLE result
	// foreground-retryable, so the transient vault is not abandoned to a background
	// path just because another vault is permanent or not-yet-synced.
	c.Check(HasRetryableKeyshareComponent(errors.Join(errRecoveryBackupNotFound, errors.New("disk full"))), Equals, true)
	c.Check(HasRetryableKeyshareComponent(errors.Join(syncing, errors.New("rpc flake"))), Equals, true)
	// An all-terminal join is not retryable.
	c.Check(HasRetryableKeyshareComponent(errors.Join(errRecoveryBackupUnrecoverable, errRecoveryBackupNotFound)), Equals, false)
	// recoverKeyShares wraps a target's errors.Join as fmt.Errorf("...: %w", join).
	// A single wrap around a mixed join must still be retryable (the terminal branch
	// must not mask the transient one via errors.Is on the whole).
	c.Check(HasRetryableKeyshareComponent(fmt.Errorf("recover keyshares for x: %w", errors.Join(errRecoveryBackupUnrecoverable, errors.New("disk full")))), Equals, true)
	// A single wrap around an all-terminal join stays non-retryable.
	c.Check(HasRetryableKeyshareComponent(fmt.Errorf("recover keyshares for x: %w", errors.Join(errRecoveryBackupUnrecoverable, errRecoveryBackupNotFound))), Equals, false)
	// Top-level join of such wrapped targets is decomposed per target.
	c.Check(HasRetryableKeyshareComponent(errors.Join(
		fmt.Errorf("recover keyshares for a: %w", errRecoveryBackupNotFound),
		fmt.Errorf("recover keyshares for b: %w", errors.Join(errRecoveryBackupUnrecoverable, errors.New("rpc flake"))),
	)), Equals, true)
	c.Check(HasRetryableKeyshareComponent(nil), Equals, false)
}

func (s *RecoverKeysharesSuite) TestArchiveBackupVerified(c *C) {
	// Point the signer seed at the known-good test mnemonic for the duration.
	orig, had := os.LookupEnv("SIGNER_SEED_PHRASE_FILE")
	seedFile := filepath.Join(c.MkDir(), "seed-phrase")
	c.Assert(os.WriteFile(seedFile, []byte(Mnemonic), 0o600), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", seedFile), IsNil)
	defer func() {
		if had {
			_ = os.Setenv("SIGNER_SEED_PHRASE_FILE", orig)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE_FILE")
		}
	}()
	seed, err := recoverySeedPhrase()
	c.Assert(err, IsNil)

	pk := types.GetRandomPubKey()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, needsSecp: true}

	// Valid: a backup sealed with THIS node's seed whose recovered share pubkey == vault pubkey.
	validEnc, err := EncryptRawKeyshares([]byte(`{"pub_key":"`+pk.String()+`"}`), seed)
	c.Assert(err, IsNil)
	c.Check(archiveBackupVerified(recoveryBackups{secp256k1: validEnc}, target), Equals, true)

	// Forged: decrypts fine (sealed with our seed) but the recovered pubkey is a
	// DIFFERENT vault — a malicious archive serving someone else's backup — rejected.
	otherEnc, err := EncryptRawKeyshares([]byte(`{"pub_key":"`+types.GetRandomPubKey().String()+`"}`), seed)
	c.Assert(err, IsNil)
	c.Check(archiveBackupVerified(recoveryBackups{secp256k1: otherEnc}, target), Equals, false)

	// Garbage / not sealed with our key → AEAD authentication fails → rejected.
	c.Check(archiveBackupVerified(recoveryBackups{secp256k1: []byte("not-our-ciphertext")}, target), Equals, false)

	// Empty backup → rejected.
	c.Check(archiveBackupVerified(recoveryBackups{}, target), Equals, false)

	// Eddsa component binding: a vault that also has an eddsa pool key.
	eddsaPK := types.GetRandomPubKey()
	eddsaTarget := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, poolPubKeyEddsa: eddsaPK, needsSecp: true, needsEddsa: true}
	validEddsa, err := EncryptRawKeyshares([]byte(`{"pub_key":"`+eddsaPK.String()+`"}`), seed)
	c.Assert(err, IsNil)
	// Both components present and bound to their respective vault pubkeys → verified.
	c.Check(archiveBackupVerified(recoveryBackups{secp256k1: validEnc, eddsa: validEddsa}, eddsaTarget), Equals, true)

	// SPLICE ATTACK: secp256k1 is this vault's real backup (passes), but the eddsa
	// component is a DIFFERENT vault's eddsa share riding along. Binding only
	// secp256k1 would let it through and write the wrong eddsa share to disk; every
	// component must be bound, so this is rejected.
	spliceEddsa, err := EncryptRawKeyshares([]byte(`{"pub_key":"`+types.GetRandomPubKey().String()+`"}`), seed)
	c.Assert(err, IsNil)
	c.Check(archiveBackupVerified(recoveryBackups{secp256k1: validEnc, eddsa: spliceEddsa}, eddsaTarget), Equals, false)

	// Eddsa present but the target carries no eddsa pubkey to bind against → fail-closed.
	c.Check(archiveBackupVerified(recoveryBackups{secp256k1: validEnc, eddsa: validEddsa}, target), Equals, false)

	// Incomplete backup: the vault needs an eddsa share but the fetched backup has
	// only secp256k1 → rejected, so it is not accepted-then-left-partial and the
	// one-shot archive attempt is not wasted re-fetching the same empty component.
	c.Check(archiveBackupVerified(recoveryBackups{secp256k1: validEnc}, eddsaTarget), Equals, false)

	// Frost component riding along → always rejected (frost archive not supported).
	c.Check(archiveBackupVerified(recoveryBackups{secp256k1: validEnc, frost: []byte("anything")}, target), Equals, false)

	// Needs-based: a vault that already holds its ECDSA share locally (needsSecp=false)
	// but is missing EdDSA accepts a backup carrying only the EdDSA component — a
	// partial backup that completes recovery must not be rejected (which would wrongly
	// spend the one-shot archive attempt).
	onlyEddsaNeeded := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, poolPubKeyEddsa: eddsaPK, needsEddsa: true}
	c.Check(archiveBackupVerified(recoveryBackups{eddsa: validEddsa}, onlyEddsaNeeded), Equals, true)
	// ...but the present EdDSA component is still verified: a different vault's share is rejected.
	c.Check(archiveBackupVerified(recoveryBackups{eddsa: spliceEddsa}, onlyEddsaNeeded), Equals, false)
}

func (s *RecoverKeysharesSuite) TestRedactArchiveRPC(c *C) {
	// Credentials in userinfo, query string, AND path must never reach the logs:
	// the whole path is dropped since a provider may carry the API key there
	// (https://host/v1/<KEY>), leaving only scheme://host:port.
	c.Check(redactArchiveRPC("https://user:secret@archive.example/rpc"), Equals, "https://archive.example")
	c.Check(redactArchiveRPC("https://archive.example/rpc?token=abc123"), Equals, "https://archive.example")
	c.Check(redactArchiveRPC("https://archive.example/v1/supersecretkey"), Equals, "https://archive.example")
	c.Check(redactArchiveRPC("https://archive.example:26657"), Equals, "https://archive.example:26657")
	// A SCHEME-LESS endpoint with credentials is mis-split by url.Parse (the creds land
	// in Scheme/Opaque, not User) — the redaction must still strip them, never echo
	// the password.
	out := redactArchiveRPC("user:secret@archive.example:26657")
	c.Check(strings.Contains(out, "secret"), Equals, false)
	c.Check(out, Equals, "archive.example:26657")
	// A scheme-less endpoint with no credentials is returned as-is.
	c.Check(redactArchiveRPC("archive.example:26657"), Equals, "archive.example:26657")
	// An endpoint that cannot be parsed into a scrubbable host is never echoed raw —
	// it could hide a secret we cannot locate — so a safe placeholder is returned.
	c.Check(redactArchiveRPC("http:///nohost"), Equals, "[redacted archive_rpc]")
}

// panicTx decodes successfully but panics on message extraction, modelling the
// crafted/hostile archive tx (e.g. an InjectTx wrapping an empty Any whose cached
// message is nil) described in the :866 finding.
type panicTx struct{}

func (panicTx) GetMsgs() []ctypes.Msg                 { panic("boom: nil cached message") }
func (panicTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func (s *RecoverKeysharesSuite) TestBackupFromRawTxRecoversPanic(c *C) {
	// A tx whose GetMsgs panics must be recovered and treated as a non-match, never
	// crashing the scan — otherwise a hostile archive could terminate bifrost.
	panicDec := func([]byte) (ctypes.Tx, error) { return panicTx{}, nil }
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey()}
	_, ok := backupFromRawTx([]byte("x"), panicDec, types.GetRandomBech32Addr(), target)
	c.Check(ok, Equals, false)
}

func (s *RecoverKeysharesSuite) TestVerifiedArchiveBackupOneShotPerVault(c *C) {
	// The one-shot attempt is spent only on a DEFINITIVE outcome (verification failure
	// or a complete no-match scan). A transient failure — here an unreachable endpoint
	// (connection refused) — must NOT mark the vault, so the background supervisor can
	// retry the archive on a later cycle rather than being disabled until restart.
	_, cleanup := setupRecoveryKeysharesTest(c) // readable seed so the attempt reaches the (refused) dial
	defer cleanup()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey()}
	signer := types.GetRandomBech32Addr()
	_, ok := verifiedArchiveBackup("http://127.0.0.1:1", signer, target, 0, 1)
	c.Check(ok, Equals, false)
	_, loaded := archiveFetchAttempted.Load(target.pubKey.String())
	c.Check(loaded, Equals, false)
}

func (s *RecoverKeysharesSuite) TestVerifiedArchiveBackupEndpointDown(c *C) {
	// An unreachable archive endpoint must fall back (ok=false), never block startup.
	_, cleanup := setupRecoveryKeysharesTest(c) // readable seed so the attempt reaches the (unreachable) dial
	defer cleanup()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey()}
	signer := types.GetRandomBech32Addr()
	_, ok := verifiedArchiveBackup("http://127.0.0.1:1", signer, target, 0, 1)
	c.Check(ok, Equals, false)
}

func (s *RecoverKeysharesSuite) TestIsUnregisteredTypeDecodeError(c *C) {
	// Old history carries messages whose type URL bifrost's codec does not register;
	// those are the EXPECTED, benign decode failures (logged at Debug).
	c.Check(isUnregisteredTypeDecodeError(errors.New("no concrete type registered for type URL /cosmos.bank.v1beta1.MsgSend")), Equals, true)
	c.Check(isUnregisteredTypeDecodeError(errors.New("unable to resolve type URL /foo.Bar")), Equals, true)
	// Any other decode failure is UNEXPECTED (possible corruption / codec bug) and
	// is surfaced at a higher level rather than swallowed silently.
	c.Check(isUnregisteredTypeDecodeError(errors.New("proto: wire-format corruption")), Equals, false)
}

func (s *RecoverKeysharesSuite) TestBackupFromRawTx(c *C) {
	pk := types.GetRandomPubKey()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk}
	signer := types.GetRandomBech32Addr()

	// (1) An undecodable tx with an unregistered type URL (an old bank MsgSend in
	// legacy history) is skipped (ok=false) — never aborts the scan (the stall the
	// fix removes) — and is the expected, benign case.
	unreg := func([]byte) (ctypes.Tx, error) {
		return nil, errors.New("no concrete type registered for type URL /cosmos.bank.v1beta1.MsgSend")
	}
	_, ok := backupFromRawTx([]byte("x"), unreg, signer, target)
	c.Check(ok, Equals, false)

	// (2) An UNEXPECTED decode error is likewise skipped (ok=false, scan continues)
	// but classified as unexpected so it is logged loudly rather than at Debug.
	unexpected := func([]byte) (ctypes.Tx, error) { return nil, errors.New("proto: wire corruption") }
	_, ok = backupFromRawTx([]byte("x"), unexpected, signer, target)
	c.Check(ok, Equals, false)

	// (3) A decodable tx that carries no matching MsgTssPool → ok=false, no abort.
	nonMatch := func([]byte) (ctypes.Tx, error) { return fakeRecoveryTx{}, nil }
	_, ok = backupFromRawTx([]byte("x"), nonMatch, signer, target)
	c.Check(ok, Equals, false)

	// (4) A decodable MsgTssPool matching the target signer+pool → returns its backup.
	match := &types.MsgTssPool{
		Signer:               signer,
		PoolPubKey:           pk,
		KeysharesBackup:      []byte("secp-backup"),
		KeysharesBackupEddsa: []byte("eddsa-backup"),
		KeysharesBackupFrost: []byte("frost-backup"),
	}
	matchTx := func([]byte) (ctypes.Tx, error) { return fakeRecoveryTx{msgs: []ctypes.Msg{match}}, nil }
	got, ok := backupFromRawTx([]byte("x"), matchTx, signer, target)
	c.Check(ok, Equals, true)
	c.Check(got.secp256k1, DeepEquals, match.KeysharesBackup)
	c.Check(got.eddsa, DeepEquals, match.KeysharesBackupEddsa)
	c.Check(got.frost, DeepEquals, match.KeysharesBackupFrost)
}

func (s *RecoverKeysharesSuite) TestFrostRecoveryBackupMissingIsSkippable(c *C) {
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{}, fmt.Errorf("wrapped: %w", errRecoveryBackupNotFound)), Equals, true)
	// Definitively unrecoverable (pruned below the node floor / found-but-empty) is
	// just as absent as not-found from the FROST supervisor's perspective — without
	// this the background FROST retry loop would hammer and error-log forever on a
	// vault whose TssPool message is below the prune floor.
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{}, fmt.Errorf("wrapped: %w", errRecoveryBackupUnrecoverable)), Equals, true)
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{}, nil), Equals, true)
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{frost: []byte("backup")}, nil), Equals, false)
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{}, fmt.Errorf("decode failed")), Equals, false)
}

func (s *RecoverKeysharesSuite) TestFindKeyshareRecoveryTargetPrefersFrostPubKey(c *C) {
	legacyPubKey := types.GetRandomPubKey()
	frostPubKey := types.GetRandomEd25519PubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	vaults := types.Vaults{
		{
			PubKey:      legacyPubKey,
			PubKeyEddsa: eddsaPubKey,
			PubKeyFrost: frostPubKey,
			BlockHeight: 123,
		},
	}

	target, ok := findKeyshareRecoveryTarget(frostPubKey, vaults)
	c.Assert(ok, Equals, true)
	c.Check(target.kind, Equals, recoveryTargetFrost)
	c.Check(target.pubKey.Equals(frostPubKey), Equals, true)
	c.Check(target.blockHeight, Equals, int64(123))

	target, ok = findKeyshareRecoveryTarget(legacyPubKey, vaults)
	c.Assert(ok, Equals, true)
	c.Check(target.kind, Equals, recoveryTargetLegacy)
	c.Check(target.pubKey.Equals(legacyPubKey), Equals, true)
	c.Check(target.poolPubKeyEddsa.Equals(eddsaPubKey), Equals, true)

	_, ok = findKeyshareRecoveryTarget(types.GetRandomPubKey(), vaults)
	c.Check(ok, Equals, false)
}

func (s *RecoverKeysharesSuite) TestAugmentVaultsWithMissingMembershipFetchesMissing(c *C) {
	active := types.Vault{PubKey: types.GetRandomPubKey(), BlockHeight: 100}
	inactive := types.Vault{PubKey: types.GetRandomPubKey(), BlockHeight: 50}
	membership := common.PubKeys{active.PubKey, inactive.PubKey}
	vaults := types.Vaults{active}

	fetched := common.PubKeys{}
	result := augmentVaultsWithMissingMembership(membership, vaults, func(pk common.PubKey) (types.Vault, error) {
		fetched = append(fetched, pk)
		return inactive, nil
	})

	c.Assert(fetched, HasLen, 1)
	c.Check(fetched[0].Equals(inactive.PubKey), Equals, true, Commentf("only the missing vault should be fetched"))
	c.Assert(result, HasLen, 2)
	c.Check(result[1].PubKey.Equals(inactive.PubKey), Equals, true)
}

func (s *RecoverKeysharesSuite) TestAugmentVaultsWithMissingMembershipSkipsFetchFailure(c *C) {
	missing := types.GetRandomPubKey()
	membership := common.PubKeys{missing}

	result := augmentVaultsWithMissingMembership(membership, types.Vaults{}, func(pk common.PubKey) (types.Vault, error) {
		return types.Vault{}, fmt.Errorf("thorchain unreachable")
	})

	// Fetch failure is skipped; keyshareRecoveryTargets still surfaces the
	// missing entry so the outer retry loop keeps working.
	c.Assert(result, HasLen, 0)
}

func (s *RecoverKeysharesSuite) TestKeyshareRecoveryTargetsResolvesEveryMembership(c *C) {
	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	frostPubKey := types.GetRandomEd25519PubKey()

	// A node that churned through Monero support is a member of both a legacy
	// asgard vault and a separate XMR monovault (PubKey == PubKeyFrost).
	vaults := types.Vaults{
		{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 100},
		{PubKey: frostPubKey, PubKeyFrost: frostPubKey, BlockHeight: 200},
	}
	membership := common.PubKeys{legacyPubKey, frostPubKey}

	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, nil)
	c.Assert(recoveryErrs, HasLen, 0)
	c.Assert(targets, HasLen, 2)
	c.Check(targets[0].kind, Equals, recoveryTargetLegacy)
	c.Check(targets[0].pubKey.Equals(legacyPubKey), Equals, true)
	c.Check(targets[0].poolPubKeyEddsa.Equals(eddsaPubKey), Equals, true)
	c.Check(targets[1].kind, Equals, recoveryTargetFrost)
	c.Check(targets[1].pubKey.Equals(frostPubKey), Equals, true)
}

func (s *RecoverKeysharesSuite) TestKeyshareRecoveryTargetsFiltersRecoveryScope(c *C) {
	legacyPubKey := types.GetRandomPubKey()
	frostPubKey := types.GetRandomEd25519PubKey()
	unknownPubKey := types.GetRandomPubKey()
	vaults := types.Vaults{
		{PubKey: legacyPubKey, BlockHeight: 100},
		{PubKey: frostPubKey, PubKeyFrost: frostPubKey, BlockHeight: 200},
	}
	membership := common.PubKeys{unknownPubKey, legacyPubKey, frostPubKey}

	frostKind := recoveryTargetFrost
	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, &frostKind)
	c.Assert(recoveryErrs, HasLen, 0)
	c.Assert(targets, HasLen, 1)
	c.Check(targets[0].kind, Equals, recoveryTargetFrost)

	legacyKind := recoveryTargetLegacy
	targets, recoveryErrs = keyshareRecoveryTargets(membership, vaults, &legacyKind)
	c.Assert(recoveryErrs, HasLen, 1)
	c.Assert(targets, HasLen, 1)
	c.Check(targets[0].kind, Equals, recoveryTargetLegacy)
}

func (s *RecoverKeysharesSuite) TestKeyshareRecoveryTargetsRetainsOlderVaults(c *C) {
	oldPubKey := types.GetRandomPubKey()
	newPubKey := types.GetRandomPubKey()
	vaults := types.Vaults{
		{PubKey: oldPubKey, BlockHeight: 100},
		{PubKey: newPubKey, BlockHeight: 300},
	}
	// The older vault remains a recovery target because it may still need to
	// migrate funds or issue refunds while retiring.
	membership := common.PubKeys{newPubKey, oldPubKey}

	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, nil)
	c.Assert(recoveryErrs, HasLen, 0)
	c.Assert(targets, HasLen, 2)
	c.Check(targets[0].kind, Equals, recoveryTargetLegacy)
	c.Check(targets[0].pubKey.Equals(newPubKey), Equals, true)
	c.Check(targets[0].blockHeight, Equals, int64(300))
	c.Check(targets[1].pubKey.Equals(oldPubKey), Equals, true)
	c.Check(targets[1].blockHeight, Equals, int64(100))
}

func (s *RecoverKeysharesSuite) TestKeyshareRecoveryTargetsReportsUnknownMembership(c *C) {
	legacyPubKey := types.GetRandomPubKey()
	vaults := types.Vaults{{PubKey: legacyPubKey, BlockHeight: 100}}
	membership := common.PubKeys{types.GetRandomPubKey(), legacyPubKey}

	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, nil)
	c.Assert(targets, HasLen, 1)
	c.Assert(recoveryErrs, HasLen, 1)
	c.Check(targets[0].pubKey.Equals(legacyPubKey), Equals, true)

	// No membership entry matches a known vault.
	targets, recoveryErrs = keyshareRecoveryTargets(common.PubKeys{types.GetRandomPubKey()}, vaults, nil)
	c.Assert(targets, HasLen, 0)
	c.Assert(recoveryErrs, HasLen, 1)
}

func (s *RecoverKeysharesSuite) TestMsgMatchesRecoveryTargetRequiresExactKeyType(c *C) {
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	otherSigner, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	legacyPubKey := types.GetRandomPubKey()
	frostPubKey := types.GetRandomEd25519PubKey()

	xmrMsg := &types.MsgTssPool{
		Signer:          signer,
		KeygenType:      types.KeygenType_XMRMonovaultKeygen,
		PoolPubKey:      legacyPubKey,
		PoolPubKeyFrost: frostPubKey,
	}
	frostTarget := keyshareRecoveryTarget{kind: recoveryTargetFrost, pubKey: frostPubKey}
	legacyTarget := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: legacyPubKey}

	c.Check(msgMatchesRecoveryTarget(xmrMsg, signer, frostTarget), Equals, true)
	c.Check(msgMatchesRecoveryTarget(xmrMsg, signer, legacyTarget), Equals, true)
	c.Check(msgMatchesRecoveryTarget(xmrMsg, otherSigner, frostTarget), Equals, false)
	c.Check(msgMatchesRecoveryTarget(xmrMsg, signer, keyshareRecoveryTarget{kind: recoveryTargetFrost, pubKey: common.EmptyPubKey}), Equals, false)

	legacyMsg := &types.MsgTssPool{
		Signer:     signer,
		KeygenType: types.KeygenType_AsgardKeygen,
		PoolPubKey: legacyPubKey,
	}
	c.Check(msgMatchesRecoveryTarget(legacyMsg, signer, legacyTarget), Equals, true)
	c.Check(msgMatchesRecoveryTarget(legacyMsg, signer, frostTarget), Equals, false)
}

func (s *RecoverKeysharesSuite) TestRecoverySeedPhraseUsesSharedSignerSeed(c *C) {
	originalSeedPhrase, hadOriginalSeedPhrase := os.LookupEnv("SIGNER_SEED_PHRASE")
	originalSeedPhraseFile, hadOriginalSeedPhraseFile := os.LookupEnv("SIGNER_SEED_PHRASE_FILE")
	seedPhraseFile := filepath.Join(c.MkDir(), "seed-phrase")
	defer func() {
		if hadOriginalSeedPhrase {
			_ = os.Setenv("SIGNER_SEED_PHRASE", originalSeedPhrase)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE")
		}
		if hadOriginalSeedPhraseFile {
			_ = os.Setenv("SIGNER_SEED_PHRASE_FILE", originalSeedPhraseFile)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE_FILE")
		}
	}()

	_ = os.Unsetenv("SIGNER_SEED_PHRASE")
	_ = os.Unsetenv("SIGNER_SEED_PHRASE_FILE")
	seedPhrase, err := recoverySeedPhrase()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "failed to get signer seed phrase: could not load signer seed phrase: SIGNER_SEED_PHRASE_FILE is unset or invalid (SIGNER_SEED_PHRASE env var is only honored on mocknet)")
	c.Assert(seedPhrase, Equals, "")

	c.Assert(os.WriteFile(seedPhraseFile, []byte("  "+Mnemonic+"  "), 0o600), IsNil)
	err = os.Setenv("SIGNER_SEED_PHRASE_FILE", seedPhraseFile)
	c.Assert(err, IsNil)
	seedPhrase, err = recoverySeedPhrase()
	c.Assert(err, IsNil)
	c.Assert(seedPhrase, Equals, Mnemonic)
}

func (s *RecoverKeysharesSuite) TestRecoverLegacyKeySharesRecoversMissingEddsaWhenECDSAExists(c *C) {
	dir, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	original := readLocalStateTestFile(c)
	c.Assert(os.WriteFile(localstateKeysharePath(legacyPubKey), original, 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{
		eddsa: encryptedLocalStateTestFile(c),
	})
	c.Assert(err, IsNil)
	assertRecoveredLocalState(c, filepath.Join(dir, "localstate-"+eddsaPubKey.String()+".json"), original)
}

func (s *RecoverKeysharesSuite) TestRecoverLegacyKeySharesRecoversMissingECDSAWhenEddsaExists(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	original := readLocalStateTestFile(c)
	c.Assert(os.WriteFile(localstateKeysharePath(eddsaPubKey), original, 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{
		secp256k1: encryptedLocalStateTestFile(c),
	})
	c.Assert(err, IsNil)
	assertRecoveredLocalState(c, localstateKeysharePath(legacyPubKey), original)
}

func (s *RecoverKeysharesSuite) TestRecoverLegacyKeySharesBothExistingSkipsBackupsAndSeed(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE"), IsNil)

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	original := readLocalStateTestFile(c)
	c.Assert(os.WriteFile(localstateKeysharePath(legacyPubKey), original, 0o600), IsNil)
	c.Assert(os.WriteFile(localstateKeysharePath(eddsaPubKey), original, 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{})
	c.Assert(err, IsNil)
}

func (s *RecoverKeysharesSuite) TestRecoverLegacyKeySharesMissingEddsaBackupReturnsError(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	c.Assert(os.WriteFile(localstateKeysharePath(legacyPubKey), readLocalStateTestFile(c), 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{})
	c.Assert(err, NotNil)
	// A found-but-empty backup is now classified as definitively unrecoverable, so a
	// non-active vault can be skipped (continue startup) instead of wedging on it.
	c.Assert(errors.Is(err, errRecoveryBackupUnrecoverable), Equals, true)
	c.Assert(err.Error(), Matches, ".*eddsa key shares backup empty for "+eddsaPubKey.String())
	_, statErr := os.Stat(localstateKeysharePath(eddsaPubKey))
	c.Assert(os.IsNotExist(statErr), Equals, true)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredDecompressedKeysharesWritesOwnerOnlyFile(c *C) {
	encrypted, err := EncryptKeyshares(LocalStateTestFile, Mnemonic)
	c.Assert(err, IsNil)

	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "localstate-recovered.json")
	err = writeRecoveredDecompressedKeyshares(path, encrypted, Mnemonic)
	c.Assert(err, IsNil)

	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().Perm(), Equals, os.FileMode(0o600))

	original, err := os.ReadFile(LocalStateTestFile)
	c.Assert(err, IsNil)
	recoveredRaw, err := os.ReadFile(path)
	c.Assert(err, IsNil)

	// recovered file should be at-rest encrypted
	c.Assert(storage.IsEncryptedAtRest(recoveredRaw), Equals, true)
	atRestKey := storage.DeriveAtRestKey(Mnemonic)
	recovered, err := storage.DecryptAtRest(atRestKey, recoveredRaw)
	c.Assert(err, IsNil)
	c.Assert(recovered, DeepEquals, original)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredDecompressedKeysharesDoesNotLeaveOrphanOnDecryptFailure(c *C) {
	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "localstate-recovered.json")
	err = writeRecoveredDecompressedKeyshares(path, []byte("broken"), Mnemonic)
	c.Assert(err, NotNil)

	_, err = os.Stat(path)
	c.Assert(os.IsNotExist(err), Equals, true)

	entries, err := os.ReadDir(dir)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 0)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredRawKeysharesWritesEncryptedOwnerOnlyFile(c *C) {
	rawKeyshares := []byte{0x07, 0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}
	vaultPubKey := types.GetRandomEd25519PubKey()
	encrypted, err := EncryptRawKeyshares(rawKeyshares, Mnemonic)
	c.Assert(err, IsNil)

	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "frost-recovery.hex")
	err = writeRecoveredRawKeyshares(path, encrypted, Mnemonic, "mocknet", vaultPubKey)
	c.Assert(err, IsNil)

	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().Perm(), Equals, os.FileMode(0o600))

	recoveredRaw, err := os.ReadFile(path)
	c.Assert(err, IsNil)

	// recovered file should be at-rest encrypted
	c.Assert(storage.IsEncryptedAtRest(recoveredRaw), Equals, true)
	atRestKey := storage.DeriveAtRestKey(Mnemonic)
	recoveredEnvelope, err := storage.DecryptAtRest(atRestKey, recoveredRaw)
	c.Assert(err, IsNil)
	recovered, err := decodeFrostRecoveryEnvelope(recoveredEnvelope, "mocknet", vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(recovered, DeepEquals, rawKeyshares)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "mocknet", vaultPubKey), IsNil)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "chainnet", vaultPubKey), NotNil)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "mocknet", types.GetRandomEd25519PubKey()), NotNil)
}

func (s *RecoverKeysharesSuite) TestFrostRecoveryEnvelopeRejectsLegacyAndTampering(c *C) {
	vaultPubKey := types.GetRandomEd25519PubKey()
	_, err := decodeFrostRecoveryEnvelope([]byte("legacy-raw-keyshare"), "mocknet", vaultPubKey)
	c.Assert(err, NotNil)

	envelope, err := encodeFrostRecoveryEnvelope([]byte("secret-keyshare"), "mocknet", vaultPubKey)
	c.Assert(err, IsNil)
	recovered, err := decodeFrostRecoveryEnvelope(envelope, "mocknet", vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(recovered, DeepEquals, []byte("secret-keyshare"))

	tampered := append([]byte(nil), envelope...)
	tampered[len(tampered)-1] ^= 1
	_, err = decodeFrostRecoveryEnvelope(tampered, "mocknet", vaultPubKey)
	c.Assert(err, NotNil)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredBytesDoesNotReplaceExisting(c *C) {
	dir, err := os.MkdirTemp("", "recover-keyshares-publish-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "frost-recovery-test.hex")
	want := []byte("existing-authenticated-envelope")
	c.Assert(os.WriteFile(path, want, 0o600), IsNil)
	c.Assert(writeRecoveredBytes(path, []byte("replacement")), NotNil)
	got, err := os.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(got, DeepEquals, want)
	entries, err := os.ReadDir(dir)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 1)
}

func (s *RecoverKeysharesSuite) TestLegacyRecoveryReplacesEmptyArtifacts(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	ecdsaPath := localstateKeysharePath(legacyPubKey)
	eddsaPath := localstateKeysharePath(eddsaPubKey)
	c.Assert(os.WriteFile(ecdsaPath, nil, 0o600), IsNil)
	c.Assert(os.WriteFile(eddsaPath, nil, 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{
		secp256k1: encryptedLocalStateTestFile(c),
		eddsa:     encryptedLocalStateTestFile(c),
	})
	c.Assert(err, IsNil)
	original := readLocalStateTestFile(c)
	assertRecoveredLocalState(c, ecdsaPath, original)
	assertRecoveredLocalState(c, eddsaPath, original)
	assertEmptyRecoveryArtifactQuarantined(c, ecdsaPath)
	assertEmptyRecoveryArtifactQuarantined(c, eddsaPath)
}

func (s *RecoverKeysharesSuite) TestFrostRecoveryReplacesEmptyArtifact(c *C) {
	rawKeyshares := []byte("deadbeefcafebabe")
	vaultPubKey := types.GetRandomEd25519PubKey()
	encrypted, err := EncryptRawKeyshares(rawKeyshares, Mnemonic)
	c.Assert(err, IsNil)

	dir, err := os.MkdirTemp("", "recover-keyshares-empty-frost-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "frost-recovery.hex")
	c.Assert(os.WriteFile(path, nil, 0o600), IsNil)
	exists, err := recoveryFileExists(path)
	c.Assert(err, IsNil)
	c.Assert(exists, Equals, false)
	c.Assert(writeRecoveredRawKeyshares(path, encrypted, Mnemonic, "mocknet", vaultPubKey), IsNil)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "mocknet", vaultPubKey), IsNil)
	assertEmptyRecoveryArtifactQuarantined(c, path)
}

func assertEmptyRecoveryArtifactQuarantined(c *C, path string) {
	matches, err := filepath.Glob(path + ".invalid-*")
	c.Assert(err, IsNil)
	c.Assert(matches, HasLen, 1)
	fi, err := os.Stat(matches[0])
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().IsRegular(), Equals, true)
	c.Assert(fi.Size(), Equals, int64(0))
}

func setupRecoveryKeysharesTest(c *C) (string, func()) {
	dir, err := os.MkdirTemp("", "recover-keyshares-home-*")
	c.Assert(err, IsNil)

	originalHome := keyshareRecoveryNodeHome
	originalSeedPhrase, hadOriginalSeedPhrase := os.LookupEnv("SIGNER_SEED_PHRASE")
	originalSeedPhraseFile, hadOriginalSeedPhraseFile := os.LookupEnv("SIGNER_SEED_PHRASE_FILE")

	keyshareRecoveryNodeHome = dir
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE"), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", writeRecoverySeedPhraseFile(c, dir, Mnemonic)), IsNil)

	return dir, func() {
		keyshareRecoveryNodeHome = originalHome
		if hadOriginalSeedPhrase {
			_ = os.Setenv("SIGNER_SEED_PHRASE", originalSeedPhrase)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE")
		}
		if hadOriginalSeedPhraseFile {
			_ = os.Setenv("SIGNER_SEED_PHRASE_FILE", originalSeedPhraseFile)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE_FILE")
		}
		_ = os.RemoveAll(dir)
	}
}

func writeRecoverySeedPhraseFile(c *C, dir, seedPhrase string) string {
	path := filepath.Join(dir, "signer-seed-phrase")
	c.Assert(os.WriteFile(path, []byte(seedPhrase), 0o600), IsNil)
	return path
}

func encryptedLocalStateTestFile(c *C) []byte {
	encrypted, err := EncryptKeyshares(LocalStateTestFile, Mnemonic)
	c.Assert(err, IsNil)
	return encrypted
}

func readLocalStateTestFile(c *C) []byte {
	original, err := os.ReadFile(LocalStateTestFile)
	c.Assert(err, IsNil)
	return original
}

func assertRecoveredLocalState(c *C, path string, original []byte) {
	recovered, err := os.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(storage.IsEncryptedAtRest(recovered), Equals, true)
	atRestKey := storage.DeriveAtRestKey(Mnemonic)
	decrypted, err := storage.DecryptAtRest(atRestKey, recovered)
	c.Assert(err, IsNil)
	c.Assert(decrypted, DeepEquals, original)
	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().Perm(), Equals, os.FileMode(0o600))
}

func (s *RecoverKeysharesSuite) TestBlockPrunedMidScan(c *C) {
	// The floor is sampled once before a scan that can run for thousands of
	// blocks. Pruning advances during that scan, and an LB may answer /status
	// from a different instance than the one serving Block() — so a fetch
	// failure must be re-checked against a FRESH floor before it is trusted.

	// Height fell below the fresh floor → pruned out from under the scan.
	c.Check(blockPrunedMidScan(600, 700, nil), Equals, true)
	// Height still above the fresh floor → a genuine RPC failure, stays transient.
	c.Check(blockPrunedMidScan(800, 700, nil), Equals, false)
	// Exactly at the floor is still fetchable.
	c.Check(blockPrunedMidScan(700, 700, nil), Equals, false)
	// Fresh floor unavailable → cannot claim pruning; stay transient/retryable.
	c.Check(blockPrunedMidScan(600, 700, errors.New("status rpc failed")), Equals, false)
	c.Check(blockPrunedMidScan(600, 0, nil), Equals, false)
}

// Note: recovery is no longer gated on vault status (an Inactive vault still
// signs late-inbound refunds and an Initializing vault is about to sign), so the
// old status×error-class skip matrix was removed. Classification is now by error
// kind only — see TestIsPermanentKeyshareRecoveryFailure and
// TestNotYetSyncedKeyshareHeight.

// --- e96ea228: integration coverage for findRecoveryBackups' error-class routing ---

// fakeRecoveryRPC is a CometRPC stub: it embeds the full cometbft client
// interface (so it satisfies cosmos-sdk's CometRPC without hand-stubbing every
// method) and overrides only Status and Block, which is all findRecoveryBackups /
// nodeBlockBounds call.
type fakeRecoveryRPC struct {
	rpcclient.Client
	latest    int64
	earliest  int64
	statusErr error
	blockTxs  [][]byte

	// earliestAfter, when > 0, is the earliest (prune-floor) height Status reports
	// from the SECOND call onward, simulating the node's history floor advancing
	// mid-scan (the floor is sampled once up front, then re-read on a block-fetch
	// failure — see findRecoveryBackups' blockPrunedMidScan branch).
	earliestAfter int64
	// failBelow, when > 0, makes Block return an error for any requested height
	// strictly below it, so a scan can be driven into that re-read-the-floor branch.
	failBelow int64
	// statusCalls counts Status invocations so earliestAfter can kick in after the
	// first snapshot.
	statusCalls int
}

func (f *fakeRecoveryRPC) Status(context.Context) (*coretypes.ResultStatus, error) {
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	earliest := f.earliest
	if f.earliestAfter > 0 && f.statusCalls > 1 {
		earliest = f.earliestAfter
	}
	return &coretypes.ResultStatus{SyncInfo: coretypes.SyncInfo{
		LatestBlockHeight:   f.latest,
		EarliestBlockHeight: earliest,
	}}, nil
}

func (f *fakeRecoveryRPC) Block(_ context.Context, height *int64) (*coretypes.ResultBlock, error) {
	if f.failBelow > 0 && height != nil && *height < f.failBelow {
		return nil, fmt.Errorf("fail to get block %d: pruned", *height)
	}
	txs := make(cmttypes.Txs, 0, len(f.blockTxs))
	for _, t := range f.blockTxs {
		txs = append(txs, cmttypes.Tx(t))
	}
	return &coretypes.ResultBlock{Block: &cmttypes.Block{Data: cmttypes.Data{Txs: txs}}}, nil
}

// fakeRecoveryBridge embeds the ThorchainBridge interface (nil) and overrides only
// GetContext, the one method findRecoveryBackups uses.
type fakeRecoveryBridge struct {
	thorclient.ThorchainBridge
	ctx cosmosclient.Context
}

func (f fakeRecoveryBridge) GetContext() cosmosclient.Context { return f.ctx }

func bridgeWith(rpc *fakeRecoveryRPC) fakeRecoveryBridge {
	return fakeRecoveryBridge{ctx: cosmosclient.Context{}.WithClient(rpc)}
}

func (s *RecoverKeysharesSuite) TestFindRecoveryBackupsClassification(c *C) {
	// Readable seed so the clamped-miss archive sub-case (case 4) reaches the dial and
	// exercises the connection-refused -> transient -> not-marked path, rather than
	// short-circuiting on the seed pre-check in verifiedArchiveBackup.
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()
	signer := types.GetRandomBech32Addr()
	conf := config.Bifrost{}
	conf.TSS.MaxKeyshareRecoverScanBlocks = 50
	// one undecodable tx in every scanned block — must be skipped, never abort the
	// scan (the 3.20.0 wedge), so the scan completes to a classified miss.
	bogus := [][]byte{[]byte("not-a-decodable-tx")}

	// (1) Vault height above the node's synced height -> not-yet-synced (height trigger).
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey(), blockHeight: 200}
	_, err := findRecoveryBackups(conf, bridgeWith(&fakeRecoveryRPC{latest: 100, earliest: 1, blockTxs: bogus}), signer, target)
	h, ok := NotYetSyncedKeyshareHeight(err)
	c.Check(ok, Equals, true)
	c.Check(h, Equals, int64(200))
	c.Check(IsPermanentKeyshareRecoveryFailure(err), Equals, false)

	// (2) Whole window below the node's floor -> permanent unrecoverable (no archive).
	target = keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey(), blockHeight: 100}
	_, err = findRecoveryBackups(conf, bridgeWith(&fakeRecoveryRPC{latest: 10000, earliest: 9000, blockTxs: bogus}), signer, target)
	c.Check(IsPermanentKeyshareRecoveryFailure(err), Equals, true)
	c.Check(errors.Is(err, errRecoveryBackupUnrecoverable), Equals, true)

	// (3) Bounds unreadable -> incomplete scan reported transient (not permanent, not
	// not-yet-synced), so the barrier retries rather than wrongly degrading.
	target = keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey(), blockHeight: 100}
	_, err = findRecoveryBackups(conf, bridgeWith(&fakeRecoveryRPC{statusErr: errors.New("status down"), blockTxs: bogus}), signer, target)
	c.Check(IsPermanentKeyshareRecoveryFailure(err), Equals, false)
	_, ok = NotYetSyncedKeyshareHeight(err)
	c.Check(ok, Equals, false)
	c.Check(HasRetryableKeyshareComponent(err), Equals, true)

	// (4) Clamped miss (floor inside the window) with an archive configured: the
	// pruned sub-window is handed to the archive (5b1a6065). The dead endpoint fails
	// at the block fetch (connection refused) — a TRANSIENT failure — so the result is
	// still permanent, but the vault must NOT be marked attempted: a transient archive
	// outage leaves it retryable on a later supervisor cycle (:806; the one-shot is
	// spent only on a definitive verification failure or a complete no-match scan).
	clampedPK := types.GetRandomPubKey()
	target = keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: clampedPK, blockHeight: 100}
	confArchive := conf
	confArchive.TSS.KeyshareArchiveRPC = "http://127.0.0.1:1"
	_, err = findRecoveryBackups(confArchive, bridgeWith(&fakeRecoveryRPC{latest: 10000, earliest: 120, blockTxs: bogus}), signer, target)
	c.Check(errors.Is(err, errRecoveryBackupUnrecoverable), Equals, true)
	_, attempted := archiveFetchAttempted.Load(clampedPK.String())
	c.Check(attempted, Equals, false)

	// (5) Same clamped miss WITHOUT an archive configured -> permanent, no archive call.
	target = keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey(), blockHeight: 100}
	_, err = findRecoveryBackups(conf, bridgeWith(&fakeRecoveryRPC{latest: 10000, earliest: 120, blockTxs: bogus}), signer, target)
	c.Check(errors.Is(err, errRecoveryBackupUnrecoverable), Equals, true)
}

// TestFindRecoveryBackupsFloorAdvancesMidScan drives the branch at
// recover_keyshares.go ~696-700: the scan starts with the window fully above the
// node's floor (not clamped up front), then a block fetch fails and a FRESH
// /status read reports a floor that has advanced past the remaining window, so
// the scan stops with a clamped (unrecoverable) miss instead of a transient error
// that would wedge the startup loop. The height-respecting fakeRecoveryRPC
// (failBelow + earliestAfter) is what makes this reachable.
func (s *RecoverKeysharesSuite) TestFindRecoveryBackupsFloorAdvancesMidScan(c *C) {
	// Readable seed so the clamped mid-scan archive sub-case (case b) reaches the dial
	// and exercises the connection-refused -> transient -> not-marked path rather than
	// short-circuiting on the seed pre-check in verifiedArchiveBackup.
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()
	signer := types.GetRandomBech32Addr()
	conf := config.Bifrost{}
	conf.TSS.MaxKeyshareRecoverScanBlocks = 50
	bogus := [][]byte{[]byte("not-a-decodable-tx")}

	// vault height 100, current 10000, max 50 -> scan window [50,150]. earliest=1 is
	// below the window, so applyPruneFloor does NOT clamp at start. failBelow=200
	// makes the first block fetch (i=150) fail; the re-read floor (earliestAfter)
	// then jumps above the whole remaining window, so blockPrunedMidScan fires.
	newRPC := func() *fakeRecoveryRPC {
		return &fakeRecoveryRPC{latest: 10000, earliest: 1, earliestAfter: 10000, failBelow: 200, blockTxs: bogus}
	}

	// (a) No archive configured: the clamped mid-scan miss is a permanent,
	// unrecoverable result (degraded start + background retry), NOT transient and
	// NOT not-yet-synced.
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey(), blockHeight: 100}
	_, err := findRecoveryBackups(conf, bridgeWith(newRPC()), signer, target)
	c.Check(errors.Is(err, errRecoveryBackupUnrecoverable), Equals, true)
	c.Check(IsPermanentKeyshareRecoveryFailure(err), Equals, true)
	_, syncing := NotYetSyncedKeyshareHeight(err)
	c.Check(syncing, Equals, false)
	c.Check(HasRetryableKeyshareComponent(err), Equals, false)

	// (b) Archive configured: on a mid-scan floor advance the never-locally-scanned
	// sub-window is handed to the archive with its upper set to midScanUnscannedUpper
	// (the height i where the scan broke, 150 here) — NOT earliest-1, which with the
	// freshly-advanced floor (10000) would be far above the window. The dead endpoint
	// fails transiently (connection refused), so the result stays permanent/
	// unrecoverable and the vault is left unmarked, staying retryable (:806); the
	// bounded upper keeps that attempt from scanning thousands of unrelated blocks and
	// wasting the timeout before reaching the window.
	clampedPK := types.GetRandomPubKey()
	target = keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: clampedPK, blockHeight: 100}
	confArchive := conf
	confArchive.TSS.KeyshareArchiveRPC = "http://127.0.0.1:1"
	_, err = findRecoveryBackups(confArchive, bridgeWith(newRPC()), signer, target)
	c.Check(errors.Is(err, errRecoveryBackupUnrecoverable), Equals, true)
	_, attempted := archiveFetchAttempted.Load(clampedPK.String())
	c.Check(attempted, Equals, false)
}

// --- starsquid (tn!5288 :886 / priv!151): httptest-backed archive-endpoint coverage ---
//
// Before this, the archive tests only pointed verifiedArchiveBackup at a refused
// connection (http://127.0.0.1:1), so every run stopped at the first block-fetch
// error and nothing past it was exercised: the nil-block guard, the body-size cap,
// a verified success, the definitive-miss marking + second-call short-circuit, the
// frost refusal, the empty-window skip, and the archive call from findRecoveryBackups'
// fully-pruned branch. These stand up a real CometBFT JSON-RPC endpoint so each of
// those guards runs against actual wire bytes (incl. hostile/oversized/nil bodies).

// archiveStub is a minimal CometBFT JSON-RPC endpoint: it answers the single
// `block` call verifiedArchiveBackup makes, returning whatever respond yields for
// the requested height. respond returns either a ResultBlock (marshaled into the
// RPC envelope with the request's echoed ID) or a raw body written verbatim — used
// to model a hostile/non-JSON response. callCount is atomic so a short-circuit test
// can assert the endpoint was (not) hit again on a second call.
type archiveStub struct {
	srv       *httptest.Server
	callCount int32
}

func newArchiveStub(respond func(height int64) (result *coretypes.ResultBlock, rawBody []byte)) *archiveStub {
	st := &archiveStub{}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&st.callCount, 1)
		body, _ := io.ReadAll(r.Body)
		var req rpctypes.RPCRequest
		_ = json.Unmarshal(body, &req)
		result, raw := respond(archiveReqHeight(req.Params))
		w.Header().Set("Content-Type", "application/json")
		if raw != nil {
			_, _ = w.Write(raw)
			return
		}
		resp := rpctypes.NewRPCSuccessResponse(req.ID, result)
		out, _ := json.Marshal(resp)
		_, _ = w.Write(out)
	}))
	return st
}

func (st *archiveStub) url() string { return st.srv.URL }
func (st *archiveStub) close()      { st.srv.Close() }
func (st *archiveStub) hits() int32 { return atomic.LoadInt32(&st.callCount) }

// archiveReqHeight pulls the height out of a CometBFT `block` params object
// ({"height":"123"}); CometBFT encodes the int64 as a quoted string.
func archiveReqHeight(params json.RawMessage) int64 {
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil {
		return 0
	}
	raw, ok := m["height"]
	if !ok {
		return 0
	}
	n, _ := strconv.ParseInt(strings.Trim(string(raw), `"`), 10, 64)
	return n
}

// blockWithTxs wraps raw tx bytes in the minimal ResultBlock shape the scan reads
// (only Block.Data.Txs is dereferenced by backupFromRawTx).
func blockWithTxs(txs ...[]byte) *coretypes.ResultBlock {
	cmtTxs := make(cmttypes.Txs, 0, len(txs))
	for _, t := range txs {
		cmtTxs = append(cmtTxs, cmttypes.Tx(t))
	}
	return &coretypes.ResultBlock{Block: &cmttypes.Block{Data: cmttypes.Data{Txs: cmtTxs}}}
}

// encodeTssPoolTx encodes a MsgTssPool into cosmos tx bytes that
// ebifrost.TxDecoder(cdc, tx.DefaultTxDecoder(cdc)) — the decoder the scan uses —
// can decode back into the message. No signatures are needed: the scan only
// decodes and matches the message, it does not verify the tx signature.
func encodeTssPoolTx(c *C, msg *types.MsgTssPool) []byte {
	cdc := thorclient.MakeCodec()
	txCfg := authtx.NewTxConfig(cdc, []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})
	tb := txCfg.NewTxBuilder()
	c.Assert(tb.SetMsgs(msg), IsNil)
	raw, err := txCfg.TxEncoder()(tb.GetTx())
	c.Assert(err, IsNil)
	return raw
}

func (s *RecoverKeysharesSuite) TestVerifiedArchiveBackupEndpointVerifiedSuccess(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()
	seed, err := recoverySeedPhrase()
	c.Assert(err, IsNil)

	pk := types.GetRandomPubKey()
	signer := types.GetRandomBech32Addr()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, needsSecp: true}

	// A backup sealed with THIS node's seed whose recovered share pubkey == vault
	// pubkey, carried by a MsgTssPool from the expected signer for this pool.
	enc, err := EncryptRawKeyshares([]byte(`{"pub_key":"`+pk.String()+`"}`), seed)
	c.Assert(err, IsNil)
	txb := encodeTssPoolTx(c, &types.MsgTssPool{Signer: signer, PoolPubKey: pk, KeysharesBackup: enc})

	st := newArchiveStub(func(int64) (*coretypes.ResultBlock, []byte) { return blockWithTxs(txb), nil })
	defer st.close()

	got, ok := verifiedArchiveBackup(st.url(), signer, target, 0, 3)
	c.Assert(ok, Equals, true)
	c.Check(got.secp256k1, DeepEquals, enc)
	// A SUCCESSFUL fetch must not spend the one-shot attempt (so a transient
	// disk-write failure afterwards can be retried).
	_, marked := archiveFetchAttempted.Load(pk.String())
	c.Check(marked, Equals, false)
}

func (s *RecoverKeysharesSuite) TestVerifiedArchiveBackupEndpointNilBlockGuard(c *C) {
	// A hostile endpoint can return a successful JSON-RPC response carrying a null
	// block; dereferencing b.Block would panic and crash bifrost. The guard must
	// treat it as a failed attempt and stay degraded (no panic). The null-block
	// branch returns WITHOUT setting definitive (recover_keyshares.go:886-893) — it
	// is treated like a transport failure, not a complete scan — so the one-shot
	// attempt is NOT spent and the vault stays retryable on a later supervisor cycle.
	_, cleanup := setupRecoveryKeysharesTest(c) // readable seed so the fetch proceeds past the seed pre-check
	defer cleanup()
	pk := types.GetRandomPubKey()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, needsSecp: true}
	signer := types.GetRandomBech32Addr()
	st := newArchiveStub(func(int64) (*coretypes.ResultBlock, []byte) {
		return &coretypes.ResultBlock{Block: nil}, nil
	})
	defer st.close()

	_, ok := verifiedArchiveBackup(st.url(), signer, target, 0, 2)
	c.Check(ok, Equals, false)
	c.Check(st.hits() > 0, Equals, true)
	_, marked := archiveFetchAttempted.Load(pk.String())
	c.Check(marked, Equals, false)
}

func (s *RecoverKeysharesSuite) TestVerifiedArchiveBackupEndpointHostileBody(c *C) {
	// A non-JSON (garbage) response body is a transport-level failure: the block
	// fetch errors, the attempt stays degraded, and — being transient — the vault is
	// NOT marked, so the supervisor can retry on a later cycle.
	_, cleanup := setupRecoveryKeysharesTest(c) // readable seed so the fetch reaches the endpoint
	defer cleanup()
	pk := types.GetRandomPubKey()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, needsSecp: true}
	signer := types.GetRandomBech32Addr()
	st := newArchiveStub(func(int64) (*coretypes.ResultBlock, []byte) {
		return nil, []byte("this is not a json-rpc response")
	})
	defer st.close()

	_, ok := verifiedArchiveBackup(st.url(), signer, target, 0, 2)
	c.Check(ok, Equals, false)
	_, marked := archiveFetchAttempted.Load(pk.String())
	c.Check(marked, Equals, false)
}

func (s *RecoverKeysharesSuite) TestVerifiedArchiveBackupEndpointDefinitiveMissAndShortCircuit(c *C) {
	// A complete scan that finds no matching backup is a DEFINITIVE miss: it spends
	// the one-shot attempt, and a second call for the same vault must short-circuit
	// without touching the endpoint again (the "one attempt per vault" guarantee).
	_, cleanup := setupRecoveryKeysharesTest(c) // readable seed so the scan proceeds to a definitive no-match
	defer cleanup()
	pk := types.GetRandomPubKey()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, needsSecp: true}
	signer := types.GetRandomBech32Addr()
	st := newArchiveStub(func(int64) (*coretypes.ResultBlock, []byte) {
		// A well-formed but empty block (no txs) at every height -> complete no-match.
		return blockWithTxs(), nil
	})
	defer st.close()

	_, ok := verifiedArchiveBackup(st.url(), signer, target, 0, 3)
	c.Check(ok, Equals, false)
	_, marked := archiveFetchAttempted.Load(pk.String())
	c.Check(marked, Equals, true)
	firstHits := st.hits()
	c.Check(firstHits > 0, Equals, true)

	_, ok = verifiedArchiveBackup(st.url(), signer, target, 0, 3)
	c.Check(ok, Equals, false)
	// No new endpoint request on the second call: the short-circuit held.
	c.Check(st.hits(), Equals, firstHits)
}

func (s *RecoverKeysharesSuite) TestVerifiedArchiveBackupTransientSeedDoesNotSpendOneShot(c *C) {
	// If the recovery seed cannot be read, NO backup can be verified and the failure
	// is transient (e.g. the seed file is briefly unavailable), so verifiedArchiveBackup
	// must stay degraded WITHOUT spending the one-shot — a matched-but-unverifiable
	// outcome driven by a transient seed error must not disable archive recovery for
	// the vault until restart. It must also not even reach the endpoint.
	for _, k := range []string{"SIGNER_SEED_PHRASE", "SIGNER_SEED_PHRASE_FILE"} {
		if orig, had := os.LookupEnv(k); had {
			defer os.Setenv(k, orig)
			c.Assert(os.Unsetenv(k), IsNil)
		}
	}
	// Confirm the seed really is unreadable in this environment.
	_, seedErr := recoverySeedPhrase()
	c.Assert(seedErr, NotNil)

	pk := types.GetRandomPubKey()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, needsSecp: true}
	signer := types.GetRandomBech32Addr()
	// The stub must never be contacted when the seed is unreadable; that is asserted
	// by st.hits()==0 on the TEST goroutine below (a gocheck Fatalf inside the
	// httptest handler would runtime.Goexit the server goroutine, not fail the test).
	st := newArchiveStub(func(int64) (*coretypes.ResultBlock, []byte) { return nil, nil })
	defer st.close()

	_, ok := verifiedArchiveBackup(st.url(), signer, target, 0, 3)
	c.Check(ok, Equals, false)
	c.Check(st.hits(), Equals, int32(0))
	_, marked := archiveFetchAttempted.Load(pk.String())
	c.Check(marked, Equals, false)
}

func (s *RecoverKeysharesSuite) TestTryArchiveFallbackSkipsWithoutEndpointCall(c *C) {
	signer := types.GetRandomBech32Addr()
	st := newArchiveStub(func(int64) (*coretypes.ResultBlock, []byte) { return blockWithTxs(), nil })
	defer st.close()
	conf := config.Bifrost{}
	conf.TSS.KeyshareArchiveRPC = st.url()

	// Frost targets are excluded (the network-scoped frost envelope is not bound by
	// archiveBackupVerified) -> no fetch, endpoint untouched.
	frost := keyshareRecoveryTarget{kind: recoveryTargetFrost, pubKey: types.GetRandomPubKey()}
	_, ok := tryArchiveFallback(conf, signer, frost, 0, 10)
	c.Check(ok, Equals, false)

	// An empty window (upper <= lower) is skipped so it does not spend the one-shot
	// budget for nothing -> endpoint untouched (both the equal and inverted cases).
	legacy := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: types.GetRandomPubKey(), needsSecp: true}
	_, ok = tryArchiveFallback(conf, signer, legacy, 10, 10)
	c.Check(ok, Equals, false)
	_, ok = tryArchiveFallback(conf, signer, legacy, 10, 5)
	c.Check(ok, Equals, false)

	c.Check(st.hits(), Equals, int32(0))
}

func (s *RecoverKeysharesSuite) TestFindRecoveryBackupsFullyPrunedBranchUsesArchive(c *C) {
	// The fully-pruned branch (recover_keyshares.go ~690): the whole scan window is
	// below the node's block-store floor, so findRecoveryBackups hands the window to
	// the configured archive. A verified backup served there completes recovery
	// instead of a degraded start. This exercises the archive call end-to-end through
	// findRecoveryBackups, not just verifiedArchiveBackup in isolation.
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()
	seed, err := recoverySeedPhrase()
	c.Assert(err, IsNil)

	pk := types.GetRandomPubKey()
	signer := types.GetRandomBech32Addr()
	enc, err := EncryptRawKeyshares([]byte(`{"pub_key":"`+pk.String()+`"}`), seed)
	c.Assert(err, IsNil)
	txb := encodeTssPoolTx(c, &types.MsgTssPool{Signer: signer, PoolPubKey: pk, KeysharesBackup: enc})

	st := newArchiveStub(func(int64) (*coretypes.ResultBlock, []byte) { return blockWithTxs(txb), nil })
	defer st.close()

	conf := config.Bifrost{}
	conf.TSS.MaxKeyshareRecoverScanBlocks = 50
	conf.TSS.KeyshareArchiveRPC = st.url()
	// vault height 100, floor (earliest) 9000 -> scan window [50,100] is entirely
	// below the floor -> applyPruneFloor reports unrecoverable -> archive branch.
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: pk, needsSecp: true, blockHeight: 100}
	bogus := [][]byte{[]byte("not-a-decodable-tx")}
	got, err := findRecoveryBackups(conf, bridgeWith(&fakeRecoveryRPC{latest: 10000, earliest: 9000, blockTxs: bogus}), signer, target)
	c.Assert(err, IsNil)
	c.Check(got.secp256k1, DeepEquals, enc)
	c.Check(st.hits() > 0, Equals, true)
}

func (s *RecoverKeysharesSuite) TestBodyLimitedTransportCapsResponseBody(c *C) {
	// bodyLimitedTransport is the memory guard behind the archive fetch: a hostile
	// endpoint could return a multi-GB body and OOM bifrost at startup before any
	// verification runs. The cap is maxArchiveBlockBytes (32 MiB) in production; this
	// proves the exact truncation guarantee with a small max (no 32 MiB allocation per
	// run) and that Close still reaches the underlying body.
	big := strings.Repeat("A", 1000)
	closed := false
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       readCloserFunc{Reader: strings.NewReader(big), onClose: func() error { closed = true; return nil }},
		}, nil
	})
	rt := bodyLimitedTransport{rt: base, max: 16}
	resp, err := rt.RoundTrip(nil)
	c.Assert(err, IsNil)
	got, err := io.ReadAll(resp.Body)
	c.Assert(err, IsNil)
	c.Check(len(got), Equals, 16) // capped, not the full 1000 bytes
	c.Assert(resp.Body.Close(), IsNil)
	c.Check(closed, Equals, true) // Close forwarded to the wrapped body
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type readCloserFunc struct {
	io.Reader
	onClose func() error
}

func (r readCloserFunc) Close() error { return r.onClose() }
