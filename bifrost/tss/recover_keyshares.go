package tss

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/itchio/lzma"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var (
	keyshareRecoveryNodeHome = app.DefaultNodeHome
	recoveryArtifactMu       sync.Mutex
)

const (
	maxFrostRecoveryFileBytes  = int64(1024 * 1024)
	frostRecoveryEnvelopeMagic = "THOR-XMR-FROST-RECOVERY\x02"
	frostRecoveryProtocol      = "thorchain-xmr-frost-recovery"
	frostRecoveryVersion       = 2
)

type frostRecoveryEnvelope struct {
	Protocol    string `json:"protocol"`
	Version     int    `json:"version"`
	Network     string `json:"network"`
	VaultPubKey string `json:"vault_pub_key"`
	GroupKey    string `json:"group_key"`
	KeyBytesB64 string `json:"key_bytes_b64"`
}

func RecoverKeyShares(conf config.Bifrost, thorchain thorclient.ThorchainBridge) error {
	return recoverKeyShares(conf, thorchain, nil)
}

// RecoverLegacyKeyShares restores key material used by the process-wide TSS
// stack. Bifrost must not start that stack while this recovery is incomplete.
func RecoverLegacyKeyShares(conf config.Bifrost, thorchain thorclient.ThorchainBridge) error {
	kind := recoveryTargetLegacy
	return recoverKeyShares(conf, thorchain, &kind)
}

// RecoverFrostKeyShares restores XMR vault material consumed by the sidecar.
// Exact-vault readiness can isolate a missing FROST share, so callers may retry
// this scope without holding all of Bifrost at startup.
func RecoverFrostKeyShares(conf config.Bifrost, thorchain thorclient.ThorchainBridge) error {
	kind := recoveryTargetFrost
	return recoverKeyShares(conf, thorchain, &kind)
}

func recoverKeyShares(conf config.Bifrost, thorchain thorclient.ThorchainBridge, selectedKind *recoveryTargetKind) error {
	tctx := thorchain.GetContext()

	// fetch the node account
	na, err := thorchain.GetNodeAccount(tctx.FromAddress.String())
	if err != nil {
		return fmt.Errorf("fail to get node account: %w", err)
	}

	// Recover every membership still assigned to this node. A retiring XMR vault
	// may still need migration or refund signatures after its member leaves the
	// active set, so node status must not suppress recovery.
	membership := na.GetSignerMembership()
	if len(membership) == 0 {
		log.Info().Msgf("no signer membership for %s, skipping key shares recovery", na.NodeAddress)
		return nil
	}
	// get all vaults
	vaults, err := thorchain.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards: %w", err)
	}
	// GetAsgards only returns Active + Retiring. A vault in signer_membership
	// may already be Inactive after a churn — fetch each missing entry
	// individually so the recovery target lookup below doesn't send bifrost
	// startup into an infinite retry (cmd/bifrost/main.go loops without bound).
	vaults = augmentVaultsWithMissingMembership(membership, vaults, func(pk common.PubKey) (types.Vault, error) {
		return thorchain.GetVault(pk.String())
	})

	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, selectedKind)
	for _, target := range targets {
		var targetErr error
		if target.kind == recoveryTargetFrost {
			targetErr = recoverFrostKeyShares(conf, thorchain, na.NodeAddress, target)
		} else {
			targetErr = recoverLegacyKeyShares(conf, thorchain, na.NodeAddress, target, vaults)
		}
		if targetErr != nil {
			recoveryErrs = append(recoveryErrs, fmt.Errorf("recover keyshares for %s: %w", target.pubKey, targetErr))
		}
	}
	return errors.Join(recoveryErrs...)
}

// augmentVaultsWithMissingMembership fetches each membership entry that isn't
// present in the supplied vault list via the provided closure and appends the
// result. Fetch failures are logged and skipped — recovery still surfaces the
// missing vault via keyshareRecoveryTargets, and the caller's outer retry loop
// handles transient thorchain read failures.
func augmentVaultsWithMissingMembership(
	membership common.PubKeys,
	vaults types.Vaults,
	fetch func(common.PubKey) (types.Vault, error),
) types.Vaults {
	for _, m := range membership {
		if _, found := findKeyshareRecoveryTarget(m, vaults); found {
			continue
		}
		v, err := fetch(m)
		if err != nil {
			log.Warn().Err(err).Msgf("signer_membership includes vault %s not returned by GetAsgards, and per-vault lookup failed", m)
			continue
		}
		vaults = append(vaults, v)
	}
	return vaults
}

// keyshareRecoveryTargets resolves every distinct membership still assigned to
// the node. It deliberately does not collapse to the newest target of each key
// type: an older retiring vault may still need migration or refund signatures.
func keyshareRecoveryTargets(membership common.PubKeys, vaults types.Vaults, selectedKind *recoveryTargetKind) ([]keyshareRecoveryTarget, []error) {
	seen := make(map[string]struct{}, len(membership))
	targets := make([]keyshareRecoveryTarget, 0, len(membership))
	var recoveryErrs []error
	for _, vault := range membership {
		target, ok := findKeyshareRecoveryTarget(vault, vaults)
		if !ok {
			// The legacy startup barrier owns unknown memberships because it is
			// unsafe to assume an unclassified share is XMR-only.
			if selectedKind == nil || *selectedKind == recoveryTargetLegacy {
				recoveryErrs = append(recoveryErrs, fmt.Errorf("fail to get creation height of %s", vault))
			}
			continue
		}
		if selectedKind != nil && target.kind != *selectedKind {
			continue
		}
		key := fmt.Sprintf("%d:%s", target.kind, target.pubKey)
		if _, ok = seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return targets, recoveryErrs
}

type recoveryTargetKind int

const (
	recoveryTargetLegacy recoveryTargetKind = iota
	recoveryTargetFrost
)

type keyshareRecoveryTarget struct {
	kind            recoveryTargetKind
	pubKey          common.PubKey
	blockHeight     int64
	poolPubKeyEddsa common.PubKey
	// needsSecp/needsEddsa record which components are still MISSING locally, set
	// from legacyKeyshareState before the scan. archiveBackupVerified requires only
	// these (every present component is still verified), so a partial backup that
	// completes recovery given what is already on disk is accepted rather than
	// rejected (which would wrongly spend the one-shot archive attempt).
	needsSecp  bool
	needsEddsa bool
}

func findKeyshareRecoveryTarget(vault common.PubKey, vaults types.Vaults) (keyshareRecoveryTarget, bool) {
	for _, v := range vaults {
		if v.PubKeyFrost.Equals(vault) {
			return keyshareRecoveryTarget{
				kind:        recoveryTargetFrost,
				pubKey:      v.PubKeyFrost,
				blockHeight: v.BlockHeight,
			}, true
		}
		if v.PubKey.Equals(vault) {
			return keyshareRecoveryTarget{
				kind:            recoveryTargetLegacy,
				pubKey:          v.PubKey,
				blockHeight:     v.BlockHeight,
				poolPubKeyEddsa: v.PubKeyEddsa,
			}, true
		}
	}
	return keyshareRecoveryTarget{}, false
}

func recoverFrostKeyShares(conf config.Bifrost, thorchain thorclient.ThorchainBridge, signer ctypes.AccAddress, target keyshareRecoveryTarget) error {
	frostRecoveryPath := frostKeyshareRecoveryPath(target.pubKey)
	network, err := frostRecoveryNetwork(conf)
	if err != nil {
		return err
	}
	seedPhrase, err := recoverySeedPhrase()
	if err != nil {
		return err
	}
	exists, err := recoveryFileExists(frostRecoveryPath)
	if err != nil {
		return fmt.Errorf("failed to stat frost key shares for %s: %w", target.pubKey, err)
	}
	if exists {
		if validateErr := validateFrostRecoveryFile(frostRecoveryPath, seedPhrase, network, target.pubKey); validateErr == nil {
			log.Info().Msgf("valid encrypted frost key shares for %s already exist, skipping recovery", target.pubKey)
			return nil
		} else {
			log.Error().Err(validateErr).Str("path", frostRecoveryPath).Msg("quarantining invalid frost recovery artifact")
			if quarantineErr := quarantineRecoveryFile(frostRecoveryPath); quarantineErr != nil {
				return fmt.Errorf("failed to quarantine invalid frost recovery artifact: %w", quarantineErr)
			}
		}
	}

	backups, err := findRecoveryBackups(conf, thorchain, signer, target)
	if isFrostRecoveryBackupMissing(backups, err) {
		log.Warn().
			Err(err).
			Msgf("frost key shares backup for %s not found; continuing because local signer state may already exist", target.pubKey)
		return nil
	}
	if err != nil {
		return err
	}

	if err = writeRecoveredRawKeyshares(frostRecoveryPath, backups.frost, seedPhrase, network, target.pubKey); err != nil {
		return fmt.Errorf("failed to recover frost key shares: %w", err)
	}

	log.Info().Str("path", frostRecoveryPath).Msgf("recovered frost key shares for %s", target.pubKey)
	return nil
}

func recoverLegacyKeyShares(conf config.Bifrost, thorchain thorclient.ThorchainBridge, signer ctypes.AccAddress, target keyshareRecoveryTarget, vaults types.Vaults) error {
	state, err := legacyKeyshareRecoveryState(target, vaults)
	if err != nil {
		return err
	}
	logExistingLegacyKeyshares(target, state)
	if state.recovered() {
		return nil
	}

	// Tell the archive verifier which components are still missing locally, so it
	// requires only those (an archive backup that supplies the missing share is
	// usable even if it lacks one this node already holds on disk).
	target.needsSecp = !state.ecdsaExists
	target.needsEddsa = state.hasEddsa && !state.eddsaExists

	// Recovery is attempted for EVERY vault regardless of status — never skipped
	// on a status test. An Inactive vault still signs automatic refunds of late
	// inbounds (handler_common_outbound / manager_txout), and an Initializing
	// vault is about to become Active and sign, so dropping either's shares would
	// make this node blame/fail those keysigns. The recovery error is classified
	// by the startup barrier (cmd/bifrost/main.go): a pruned or never-posted
	// backup starts bifrost DEGRADED + background retry, a not-yet-synced backup
	// starts degraded + a one-shot height trigger, and only a genuinely transient
	// failure retries before TSS startup.
	backups, err := findRecoveryBackups(conf, thorchain, signer, target)
	if err != nil {
		return err
	}
	return recoverLegacyKeySharesFromBackups(signer, target, vaults, backups)
}

type legacyKeyshareState struct {
	ecdsaPath   string
	ecdsaExists bool
	eddsaPath   string
	eddsaExists bool
	hasEddsa    bool
}

func (s legacyKeyshareState) recovered() bool {
	return s.ecdsaExists && (!s.hasEddsa || s.eddsaExists)
}

func recoverLegacyKeySharesFromBackups(signer ctypes.AccAddress, target keyshareRecoveryTarget, vaults types.Vaults, backups recoveryBackups) error {
	state, err := legacyKeyshareRecoveryState(target, vaults)
	if err != nil {
		return err
	}
	logExistingLegacyKeyshares(target, state)
	if state.recovered() {
		return nil
	}

	needsSeed := (!state.ecdsaExists && len(backups.secp256k1) > 0) ||
		(state.hasEddsa && !state.eddsaExists && len(backups.eddsa) > 0)
	seedPhrase := ""
	if needsSeed {
		seedPhrase, err = recoverySeedPhrase()
		if err != nil {
			return err
		}
	}

	var resultErr error
	if !state.ecdsaExists {
		if len(backups.secp256k1) == 0 {
			// Message found but the backup is empty (keygen ran with backups off or
			// the encryption failed): definitively unrecoverable from this node, same
			// found-but-empty class the FROST path already skips via
			// isFrostRecoveryBackupMissing. Wrap the permanent sentinel so startup
			// degrades to the background supervisor instead of wedging the retry loop.
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: ecdsa key shares backup empty for %s", errRecoveryBackupUnrecoverable, target.pubKey))
		} else if err = writeRecoveredDecompressedKeyshares(state.ecdsaPath, backups.secp256k1, seedPhrase); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("failed to recover ecdsa key shares: %w", err))
		} else {
			log.Info().Str("path", state.ecdsaPath).Msgf("recovered ecdsa key shares for %s", signer)
		}
	}

	if state.hasEddsa && !state.eddsaExists {
		if len(backups.eddsa) == 0 {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: eddsa key shares backup empty for %s", errRecoveryBackupUnrecoverable, target.poolPubKeyEddsa))
		} else if err = writeRecoveredDecompressedKeyshares(state.eddsaPath, backups.eddsa, seedPhrase); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("failed to recover eddsa key shares: %w", err))
		} else {
			log.Info().Str("path", state.eddsaPath).Msgf("recovered eddsa key shares for %s", signer)
		}
	}

	return resultErr
}

func legacyKeyshareRecoveryState(target keyshareRecoveryTarget, vaults types.Vaults) (legacyKeyshareState, error) {
	state := legacyKeyshareState{
		ecdsaPath: localstateKeysharePath(target.pubKey),
	}

	var err error
	state.ecdsaExists, err = recoveryFileExists(state.ecdsaPath)
	if err != nil {
		return legacyKeyshareState{}, fmt.Errorf("failed to stat ecdsa key shares for %s: %w", target.pubKey, err)
	}

	if target.poolPubKeyEddsa.IsEmpty() {
		return state, nil
	}
	state.hasEddsa = true
	state.eddsaPath = localstateKeysharePath(target.poolPubKeyEddsa)

	lastVaultHeight := int64(0)
	for _, v := range vaults {
		if v.PubKeyEddsa.Equals(target.poolPubKeyEddsa) {
			lastVaultHeight = v.BlockHeight
			break
		}
	}
	if lastVaultHeight == 0 {
		return legacyKeyshareState{}, fmt.Errorf("fail to get creation height of %s", target.poolPubKeyEddsa)
	}

	state.eddsaExists, err = recoveryFileExists(state.eddsaPath)
	if err != nil {
		return legacyKeyshareState{}, fmt.Errorf("failed to stat eddsa key shares for %s: %w", target.poolPubKeyEddsa, err)
	}

	return state, nil
}

func logExistingLegacyKeyshares(target keyshareRecoveryTarget, state legacyKeyshareState) {
	if state.ecdsaExists {
		log.Info().Msgf("ecdsa key shares for %s already exist, skipping recovery", target.pubKey)
	}
	if state.hasEddsa && state.eddsaExists {
		log.Info().Msgf("eddsa key shares for %s already exist, skipping recovery", target.poolPubKeyEddsa)
	}
}

func localstateKeysharePath(pubKey common.PubKey) string {
	return filepath.Join(keyshareRecoveryNodeHome, fmt.Sprintf("localstate-%s.json", pubKey))
}

func frostKeyshareRecoveryPath(pubKey common.PubKey) string {
	return filepath.Join(keyshareRecoveryNodeHome, fmt.Sprintf("frost-recovery-%s.hex", pubKey))
}

func recoveryFileExists(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("recovery path is not a regular file: %s", path)
	}
	return fi.Size() > 0, nil
}

func validateFrostRecoveryFile(path, seedPhrase, network string, vaultPubKey common.PubKey) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("recovery artifact is not a regular file")
	}
	if fi.Size() <= 0 || fi.Size() > maxFrostRecoveryFileBytes {
		return fmt.Errorf("recovery artifact size %d is outside 1..%d", fi.Size(), maxFrostRecoveryFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return fmt.Errorf("recovery artifact changed during validation")
	}
	data := make([]byte, opened.Size())
	if _, err = io.ReadFull(f, data); err != nil {
		return err
	}
	plaintext, err := storage.DecryptAtRest(storage.DeriveAtRestKey(seedPhrase), data)
	if err != nil {
		return fmt.Errorf("recovery artifact authentication failed: %w", err)
	}
	defer func() {
		for i := range plaintext {
			plaintext[i] = 0
		}
	}()
	keyBytes, err := decodeFrostRecoveryEnvelope(plaintext, network, vaultPubKey)
	zeroBytes(keyBytes)
	return err
}

func quarantineRecoveryFile(path string) error {
	recoveryArtifactMu.Lock()
	defer recoveryArtifactMu.Unlock()
	return quarantineRecoveryFileLocked(path)
}

func quarantineRecoveryFileLocked(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("recovery artifact is not a regular file")
	}
	quarantined := fmt.Sprintf("%s.invalid-%d", path, time.Now().UnixNano())
	if renameErr := os.Rename(path, quarantined); renameErr != nil {
		return renameErr
	}
	moved, err := os.Lstat(quarantined)
	if err != nil {
		return fmt.Errorf("failed to inspect quarantined recovery artifact: %w", err)
	}
	if !moved.Mode().IsRegular() || !os.SameFile(info, moved) {
		return fmt.Errorf("recovery artifact changed while being quarantined")
	}
	return syncRecoveryDirectory(filepath.Dir(path))
}

type recoveryBackups struct {
	secp256k1 []byte
	eddsa     []byte
	frost     []byte
}

// errRecoveryBackupNotFound marks a COMPLETE scan (reliable bounds, full window
// including above the vault height actually covered) that did not find the TssPool
// message: the backup was never posted in available history. It is permanent for
// pre-startup purposes — rescanning the same window cannot find what is not there,
// so the barrier starts degraded and hands off to the background supervisor rather
// than looping (the 3.20.0 stall class). An INCOMPLETE scan does NOT use this: a
// bounds-read failure or a window upper-capped by a still-syncing height returns a
// transient error or a not-yet-synced error instead (see findRecoveryBackups).
var errRecoveryBackupNotFound = errors.New("key share recovery backup not found")

// errRecoveryBackupUnrecoverable marks a backup that definitively cannot be obtained
// from THIS node: the source blocks are pruned below the block-store floor, or the
// TssPool message was found but carries an empty backup (keygen ran with backups
// disabled or the encryption failed).
var errRecoveryBackupUnrecoverable = errors.New("key share recovery backup unrecoverable from this node")

// ErrRecoveryBackupUnrecoverable is the exported alias of the permanent
// (pruned / empty-backup) recovery sentinel. It lets cross-package tests — notably
// cmd/bifrost's startup-barrier test — construct a permanently-unavailable leaf that
// IsPermanentKeyshareRecoveryFailure recognizes, so the barrier's routing on a
// [permanent + transient] result can be asserted from package main (which cannot see
// the unexported sentinels). It is the same value; production classification is
// unchanged.
var ErrRecoveryBackupUnrecoverable = errRecoveryBackupUnrecoverable

// recoveryBackupNotYetSyncedError marks a backup whose block height is ABOVE the
// node's current synced height: the vault was created after the point this node
// has caught up to, so the TssPool message is not yet in local history. This is
// neither pruned (errRecoveryBackupUnrecoverable) nor permanently missing
// (errRecoveryBackupNotFound) — it becomes available once the node syncs past
// requiredHeight. Startup must NOT block on it (degraded start); the caller
// registers a one-shot height trigger that re-runs recovery when local height
// reaches requiredHeight (see cmd/bifrost/main.go).
type recoveryBackupNotYetSyncedError struct {
	requiredHeight int64
	currentHeight  int64
	target         common.PubKey
}

// HasRetryableKeyshareComponent reports whether err (possibly an errors.Join)
// contains a member that is neither a permanent failure (pruned / never-posted)
// nor a not-yet-synced wait — i.e. a genuinely transient failure (RPC/disk) that a
// foreground retry can fix. The startup barrier consults it AFTER handling any
// permanent member (which starts bifrost degraded immediately, handing recovery to
// the background supervisor): when the remaining result is not-yet-synced, a
// still-present transient component keeps the FOREGROUND retry going so it clears
// quickly — not abandoned to the slower background path for the sake of an unrelated
// not-yet-synced sibling — rather than arming the one-shot height trigger. The
// one-shot height trigger uses it the same way.
func HasRetryableKeyshareComponent(err error) bool {
	return anyKeyshareErrorLeaf(err, isRetryableKeyshareLeaf)
}

// anyKeyshareErrorLeaf walks the FULL error tree — descending every Unwrap() error
// (fmt.Errorf %w) and Unwrap() []error (errors.Join) layer to the leaves — and
// reports whether pred holds for any leaf. recoverKeyShares joins per-target
// errors, each wrapped with fmt.Errorf around its own errors.Join, and a caller
// may add further context wraps; classifying on the whole with errors.Is/As lets a
// terminal or wait leaf on one vault mask a different-class leaf on another. Judging
// per leaf keeps each vault's result independent regardless of how deeply the tree
// is nested, so the startup-barrier routing can't be flipped by an extra wrap.
func anyKeyshareErrorLeaf(err error, pred func(error) bool) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if anyKeyshareErrorLeaf(e, pred) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if inner := wrapped.Unwrap(); inner != nil {
			return anyKeyshareErrorLeaf(inner, pred)
		}
	}
	return pred(err)
}

// forEachKeyshareErrorLeaf walks the FULL error tree the same way as
// anyKeyshareErrorLeaf (descending every Unwrap() error and Unwrap() []error layer)
// and calls visit on each leaf. Used to fold across all leaves — e.g. the lowest
// not-yet-synced height — rather than short-circuit on the first match.
func forEachKeyshareErrorLeaf(err error, visit func(error)) {
	if err == nil {
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			forEachKeyshareErrorLeaf(e, visit)
		}
		return
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if inner := wrapped.Unwrap(); inner != nil {
			forEachKeyshareErrorLeaf(inner, visit)
			return
		}
	}
	visit(err)
}

// isRetryableKeyshareLeaf: a single error is foreground-retryable unless it is a
// terminal (pruned/never-posted) or a not-yet-synced wait.
func isRetryableKeyshareLeaf(err error) bool {
	if errors.Is(err, errRecoveryBackupUnrecoverable) || errors.Is(err, errRecoveryBackupNotFound) {
		return false
	}
	if _, syncing := NotYetSyncedKeyshareHeight(err); syncing {
		return false
	}
	return true
}

// isPermanentKeyshareLeaf: a single error is a permanent (supervisor-handled)
// failure when it is pruned/never-posted and not a not-yet-synced wait.
func isPermanentKeyshareLeaf(err error) bool {
	if _, syncing := NotYetSyncedKeyshareHeight(err); syncing {
		return false
	}
	return errors.Is(err, errRecoveryBackupUnrecoverable) || errors.Is(err, errRecoveryBackupNotFound)
}

func (e *recoveryBackupNotYetSyncedError) Error() string {
	return fmt.Sprintf(
		"key share recovery backup for %s not yet synced: vault/backup height %d is above the node's current height %d",
		e.target, e.requiredHeight, e.currentHeight,
	)
}

// NotYetSyncedKeyshareHeight returns the block height a not-yet-synced recovery
// is waiting for (ok=false when err is not a not-yet-synced error). The startup
// barrier uses it to arm a one-shot height trigger instead of blocking or
// busy-retrying while the node is still catching up to the vault's height.
//
// When a joined result carries SEVERAL not-yet-synced vaults at different heights,
// it returns the LOWEST required height — not whichever leaf errors.As finds first
// in tree order. The trigger re-runs the FULL recovery once that height is reached,
// recovering every vault now in range and re-arming for any still above it; waking
// at the minimum recovers the earliest-available vault as soon as possible instead
// of needlessly stalling it behind a higher-height sibling.
func NotYetSyncedKeyshareHeight(err error) (int64, bool) {
	lowest := int64(-1)
	forEachKeyshareErrorLeaf(err, func(leaf error) {
		var e *recoveryBackupNotYetSyncedError
		if errors.As(leaf, &e) && (lowest < 0 || e.requiredHeight < lowest) {
			lowest = e.requiredHeight
		}
	})
	if lowest < 0 {
		return 0, false
	}
	return lowest, true
}

// IsPermanentKeyshareRecoveryFailure reports whether err is a recovery failure
// that foreground pre-startup retries cannot fix on their own — the source
// blocks are pruned below this node's floor (unrecoverable) OR the backup was
// never seen in the fully-available scan window (not-found). Both must start
// bifrost DEGRADED and hand recovery to the background supervisor rather than
// wedge the pre-startup loop forever (the 3.20.0 signing-stall class). A
// not-yet-synced error is deliberately NOT permanent — it has its own one-shot
// trigger — so it is excluded here.
// It decomposes a joined/wrapped error and judges each leaf independently (via
// anyKeyshareErrorLeaf): a not-yet-synced sibling must NOT mask a permanent member,
// or a mixed [permanent + not-yet-synced] result would classify as non-permanent
// and the startup barrier would arm only the one-shot height trigger — postponing
// the permanently-unavailable vault's recovery to an unrelated sync height (or the
// supervisor's deadline) instead of handing it to the background supervisor.
func IsPermanentKeyshareRecoveryFailure(err error) bool {
	return anyKeyshareErrorLeaf(err, isPermanentKeyshareLeaf)
}

// nodeStatusTimeout bounds the /status RPC used to read the node's block-height
// range during recovery, so a hung endpoint cannot stall bifrost startup.
const nodeStatusTimeout = 10 * time.Second

func findRecoveryBackups(conf config.Bifrost, thorchain thorclient.ThorchainBridge, signer ctypes.AccAddress, target keyshareRecoveryTarget) (recoveryBackups, error) {
	cdc := thorclient.MakeCodec()
	dec := ebifrost.TxDecoder(cdc, tx.DefaultTxDecoder(cdc))
	// Legacy vaults were created at heights the node has long since pruned (e.g. a
	// vault at ~17.4M vs a node block-store floor of ~20.4M). Requesting a pruned
	// height below fails, and because legacy recovery is a hard startup barrier that
	// fatal error wedges bifrost into an unbounded retry so it never starts TSS and
	// never signs (the 3.20.0 network-wide signing stall). Floor the scan at the
	// node's earliest locally-available block; if the whole range is below it the
	// backup is unrecoverable from this node.
	//
	// One /status call yields both bounds from a single snapshot: the current height
	// (scan upper bound) and the earliest locally-available block (prune floor). On
	// failure the bounds are unreliable: the scan falls back to the vault height and
	// cannot look above it, so a miss is reported as the transient "scan incomplete"
	// error (retried once bounds recover) — NOT the now-permanent not-found, and not
	// a false "unrecoverable".
	currentHeight, earliest, err := nodeBlockBounds(thorchain)
	boundsReliable := err == nil
	if err != nil {
		log.Warn().Err(err).Msg("fail to get thorchain block bounds for key share recovery; scanning from vault height without a prune floor")
		currentHeight = target.blockHeight
		earliest = 0
	}
	// The vault (and its TssPool backup, posted at creation) is above the height
	// this node has synced to: we are still catching up, the message is not yet in
	// local history, and scanning now would falsely report it missing. Surface a
	// not-yet-synced error so the startup barrier starts bifrost degraded and arms
	// a one-shot trigger to recover once local height reaches the vault height,
	// rather than busy-retrying or wedging startup while syncing.
	if err == nil && target.blockHeight > currentHeight {
		return recoveryBackups{}, &recoveryBackupNotYetSyncedError{
			requiredHeight: target.blockHeight,
			currentHeight:  currentHeight,
			target:         target.pubKey,
		}
	}

	scanLowerBound, scanUpperBound := recoverScanBounds(target.blockHeight, currentHeight, conf.TSS.MaxKeyshareRecoverScanBlocks)
	// Remember the window bottom before the prune floor raises it, so a clamped miss
	// can hand the pruned sub-window [origLowerBound, earliest-1] to the archive.
	origLowerBound := scanLowerBound
	// When the floor advances mid-scan, the scan has already covered (i, scanUpperBound]
	// locally, so only heights <= i were never checked. Record that i so the archive
	// fetch targets exactly the never-scanned sub-window instead of re-fetching the
	// already-covered part (which can exhaust the archive timeout). -1 = not set.
	midScanUnscannedUpper := int64(-1)

	clamped := false
	if earliest > 0 {
		var unrecoverable bool
		scanLowerBound, clamped, unrecoverable = applyPruneFloor(scanLowerBound, scanUpperBound, earliest)
		if unrecoverable {
			// The whole scan window is below this node's block-store floor (pruned).
			// If the operator configured an archive endpoint with deeper history, make
			// ONE bounded, verify-only attempt to fetch the backup from it before
			// giving up to a degraded start. The endpoint is never trusted: the fetched
			// backup is accepted only if it decrypts with this node's key and the
			// recovered share's pubkey equals the vault pubkey from THORChain.
			// Frost targets are excluded — the frost share is sealed with a
			// network-scoped envelope that is not bound by archiveBackupVerified, so
			// we refuse to fetch it and stay degraded rather than write it unverified.
			if backups, ok := tryArchiveFallback(conf, signer, target, scanLowerBound, scanUpperBound); ok {
				return backups, nil
			}
			return recoveryBackups{}, fmt.Errorf(
				"%w: TssPool message for %s is below the earliest locally-available block %d (vault height %d, older blocks pruned)",
				errRecoveryBackupUnrecoverable, signer, earliest, target.blockHeight,
			)
		}
	}

	for i := scanUpperBound; i > scanLowerBound; i-- {
		if i%1000 == 0 {
			log.Info().Msgf("scanning block %d for TssPool message to recover key shares", i)
		}

		b, err := thorchain.GetContext().Client.Block(context.Background(), &i)
		if err != nil {
			// The floor was sampled once, before a scan that can run for thousands
			// of blocks: pruning advances meanwhile, and an LB may answer /status
			// from a different instance than the one serving Block(). Re-read the
			// floor before trusting this failure. The scan walks DOWNWARD, so if i
			// is now below the fresh floor every remaining height is too — stop and
			// report a clamped miss (unrecoverable => degraded start + background
			// retry) instead of a transient error that wedges the startup loop.
			if _, freshEarliest, boundsErr := nodeBlockBounds(thorchain); blockPrunedMidScan(i, freshEarliest, boundsErr) {
				log.Warn().Err(err).Int64("height", i).Int64("earliest", freshEarliest).Msg(
					"block was pruned during the key share recovery scan; the node's history floor advanced past the remaining window")
				earliest = freshEarliest
				clamped = true
				midScanUnscannedUpper = i // only heights <= i were never scanned locally
				break
			}
			return recoveryBackups{}, fmt.Errorf("fail to get block: %w", err)
		}

		for _, txb := range b.Block.Txs {
			if backups, ok := backupFromRawTx(txb, dec, signer, target); ok {
				return backups, nil
			}
		}
	}

	// A miss is only a definitive not-found when the scan was COMPLETE. If the
	// block-bounds read failed, the scan upper bound fell back to the vault height
	// and could not look ABOVE it, so a backup posted after vault creation (a late
	// poster, past 2/3 consensus) would be missed. Report that as transient (not the
	// not-found sentinel) so the startup barrier retries once bounds recover, rather
	// than wrongly treating an incomplete scan as permanent and degrading.
	if !boundsReliable && !clamped {
		return recoveryBackups{}, fmt.Errorf(
			"key share recovery scan for %s incomplete: thorchain block bounds unavailable, scan could not cover heights above vault height %d within %d blocks",
			signer, target.blockHeight, conf.TSS.MaxKeyshareRecoverScanBlocks,
		)
	}
	// The scan window reaches up to the vault height + MaxKeyshareRecoverScanBlocks
	// so a backup posted AFTER vault creation (a late poster past 2/3 consensus) is
	// covered. When the node has not yet synced to the top of that window, the upper
	// bound was capped at the current height, so a miss is NOT definitive — the
	// backup may sit in [currentHeight+1, wantedUpper], which this node has not seen
	// yet. Treat it as not-yet-synced (degraded start + one-shot trigger that
	// recovers once synced) instead of a permanent not-found. A fully-synced node
	// recovering an old legacy vault has currentHeight far above wantedUpper, so this
	// never fires for the genuine never-posted case.
	if boundsReliable && !clamped && conf.TSS.MaxKeyshareRecoverScanBlocks > 0 {
		if wantedUpper := target.blockHeight + conf.TSS.MaxKeyshareRecoverScanBlocks; currentHeight < wantedUpper {
			return recoveryBackups{}, &recoveryBackupNotYetSyncedError{
				requiredHeight: wantedUpper,
				currentHeight:  currentHeight,
				target:         target.pubKey,
			}
		}
	}
	// A clamped miss means part of the window — the never-locally-scanned sub-window
	// (origLowerBound, earliest-1] below this node's floor (set at start, or advanced
	// mid-scan) — could hold the backup. The operator may have configured an archive
	// with that deeper history, so make one bounded, verify-only attempt over just
	// that sub-window before degrading (same helper, trust model, and one-shot
	// guarantee as the fully-pruned branch).
	if clamped {
		// Target the never-locally-scanned sub-window only. If the floor advanced
		// mid-scan, that is (origLowerBound, i] — heights above i were already fetched,
		// and the fresh floor can even exceed scanUpperBound, so re-fetching from
		// earliest-1 would waste the archive timeout on already-covered blocks. For a
		// start-of-scan clamp (no mid-scan stop), the never-scanned part is
		// (origLowerBound, earliest-1], capped at the scan ceiling.
		archiveUpper := midScanUnscannedUpper
		if archiveUpper < 0 {
			archiveUpper = earliest - 1
			if archiveUpper > scanUpperBound {
				archiveUpper = scanUpperBound
			}
		}
		if backups, ok := tryArchiveFallback(conf, signer, target, origLowerBound, archiveUpper); ok {
			return backups, nil
		}
	}
	miss := fmt.Errorf(
		"%w: "+
			"TssPool message from %s not found within %d blocks before or after vault height %d",
		scanMissSentinel(clamped),
		signer,
		conf.TSS.MaxKeyshareRecoverScanBlocks,
		target.blockHeight,
	)
	if clamped {
		miss = fmt.Errorf("%w (scan window was clamped to the node's earliest available block %d — the message may be in the pruned part)", miss, earliest)
	}
	return recoveryBackups{}, miss
}

// keyshareArchiveTimeout bounds the single archive fetch attempt per vault at
// startup so a slow or hostile endpoint cannot stall bifrost.
const keyshareArchiveTimeout = 60 * time.Second

// tryArchiveFallback makes the single verify-only archive fetch over the
// never-locally-scanned window (lower, upper] — lower EXCLUSIVE, matching the scan
// loops and verifiedArchiveBackup — when an archive endpoint is configured and the
// target is not a frost vault (whose network-scoped envelope archiveBackupVerified
// does not bind). It is the one place the two miss paths (fully pruned, and
// clamped) share, so the trust model, the frost exclusion, the one-shot budget, and
// the exclusive-bound convention cannot drift between them. An empty window
// (upper <= lower) is skipped so it does not spend the one-shot budget for nothing.
func tryArchiveFallback(conf config.Bifrost, signer ctypes.AccAddress, target keyshareRecoveryTarget, lower, upper int64) (recoveryBackups, bool) {
	if conf.TSS.KeyshareArchiveRPC == "" || target.kind == recoveryTargetFrost || upper <= lower {
		return recoveryBackups{}, false
	}
	return verifiedArchiveBackup(conf.TSS.KeyshareArchiveRPC, signer, target, lower, upper)
}

// verifiedArchiveBackup makes ONE bounded attempt to fetch a legacy keyshare
// backup from an operator-configured archive RPC (deeper history than this node)
// when the backup is pruned locally. It scans (lower, upper] — lower EXCLUSIVE,
// matching the scan loop below and tryArchiveFallback (the never-locally-scanned
// window below this node's floor) — for the matching MsgTssPool and returns it
// ONLY if it passes
// archiveBackupVerified. Any failure — bad client, RPC error, timeout, not found,
// or failed verification — logs a Warn and returns ok=false so the caller stays
// degraded. No retry loop: the whole attempt is one pass bounded by a timeout.
func verifiedArchiveBackup(archiveRPC string, signer ctypes.AccAddress, target keyshareRecoveryTarget, lower, upper int64) (result recoveryBackups, found bool) {
	// One attempt per vault per process, but only count an attempt that did NOT yield
	// a usable verified backup. The degraded-start supervisor re-runs full recovery
	// each cycle; marking a permanently-missing or hostile endpoint (found=false)
	// stops it being re-scanned forever. A SUCCESSFUL fetch is deliberately not
	// marked, so if writing the fetched backup to disk then fails transiently the
	// supervisor can re-fetch and retry the publish rather than losing it for the
	// life of the process.
	archiveKey := target.pubKey.String()
	if _, attempted := archiveFetchAttempted.Load(archiveKey); attempted {
		return recoveryBackups{}, false
	}
	// Verifying a fetched backup needs the recovery seed (archiveBackupVerified).
	// If it cannot be read, that is TRANSIENT (e.g. the seed file is briefly
	// unavailable) and no backup can be verified, so make no attempt and stay
	// degraded WITHOUT spending the one-shot — otherwise a transient seed-read
	// failure on a matched backup would hit the definitive-reject path below and
	// disable archive recovery for this vault until the process restarts, the same
	// mistake the transient transport failures are careful to avoid.
	if _, err := recoverySeedPhrase(); err != nil {
		log.Warn().Err(err).Msgf(
			"keyshare archive: cannot read recovery seed to verify a fetched backup for %s; staying degraded without spending the one-shot attempt", target.pubKey)
		return recoveryBackups{}, false
	}
	// Only a DEFINITIVE non-success spends the one-shot attempt: verification failed,
	// or the archive was fully scanned with no matching backup. Transient transport
	// errors (timeout, connection refused, block-fetch error, bad client) leave the
	// vault unmarked so the background supervisor can retry on a later cycle rather
	// than disabling the archive for that vault until a restart.
	definitive := false
	defer func() {
		if !found && definitive {
			archiveFetchAttempted.Store(archiveKey, struct{}{})
		}
	}()
	redactedRPC := redactArchiveRPC(archiveRPC)
	log.Info().Str("archive_rpc", redactedRPC).Int64("from", upper).Int64("to", lower).Msgf(
		"legacy keyshare backup for %s is pruned locally; making one verify-only fetch attempt from the configured archive endpoint", target.pubKey)
	// The endpoint is never trusted, so cap each block response body: a hostile
	// endpoint could otherwise return a multi-GB body and OOM bifrost at startup
	// before any verification runs. keyshareArchiveTimeout bounds elapsed time; this
	// bounds memory. Use a DEDICATED transport clone (not the shared
	// http.DefaultTransport) so the untrusted host's idle sockets never pool in the
	// global transport, and CloseIdleConnections — forwarded by bodyLimitedTransport
	// — actually drops them once the single attempt is done.
	var baseTransport http.RoundTripper = &http.Transport{}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		baseTransport = dt.Clone()
	}
	httpClient := &http.Client{Transport: bodyLimitedTransport{rt: baseTransport, max: maxArchiveBlockBytes}}
	defer httpClient.CloseIdleConnections()
	client, err := rpchttp.NewWithClient(archiveRPC, "/websocket", httpClient)
	if err != nil {
		// Do NOT log err: CometBFT's constructor error embeds the full raw endpoint,
		// which may carry credentials (user:pass@host). Log only the redacted form.
		log.Warn().Str("archive_rpc", redactedRPC).Msg("keyshare archive: cannot create RPC client for the configured endpoint (check the URL); staying degraded")
		return recoveryBackups{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyshareArchiveTimeout)
	defer cancel()
	cdc := thorclient.MakeCodec()
	dec := ebifrost.TxDecoder(cdc, tx.DefaultTxDecoder(cdc))
	for i := upper; i > lower; i-- {
		if ctx.Err() != nil {
			log.Warn().Err(ctx.Err()).Msg("keyshare archive: fetch did not complete within the timeout; staying degraded")
			return recoveryBackups{}, false
		}
		b, blockErr := client.Block(ctx, &i)
		if blockErr != nil {
			// Do NOT log blockErr: like the constructor error above, a CometBFT
			// transport error embeds the full raw endpoint (e.g. `post failed: Post
			// "http://user:pass@host"`), which may carry credentials. Log only the
			// redacted endpoint + height.
			log.Warn().Str("archive_rpc", redactedRPC).Int64("height", i).Msg("keyshare archive: block fetch failed; staying degraded")
			return recoveryBackups{}, false
		}
		if b == nil || b.Block == nil {
			// An untrusted endpoint can return a successful JSON-RPC response with a
			// null block (`"result":{"block":null}`); dereferencing b.Block.Txs would
			// panic and crash bifrost (neither the startup caller nor the supervisor
			// recovers it). Treat an absent block as a failed attempt and stay degraded.
			log.Warn().Int64("height", i).Msg("keyshare archive: endpoint returned an empty block; staying degraded")
			return recoveryBackups{}, false
		}
		for _, txb := range b.Block.Txs {
			backups, ok := backupFromRawTx(txb, dec, signer, target)
			if !ok {
				continue
			}
			if !archiveBackupVerified(backups, target) {
				log.Warn().Msgf(
					"keyshare archive: fetched backup for %s failed verification (did not decrypt with this node's key, or recovered pubkey != vault pubkey); rejecting and staying degraded", target.pubKey)
				definitive = true // a matched-but-invalid backup is a definitive reject
				return recoveryBackups{}, false
			}
			log.Info().Int64("height", i).Msgf("keyshare archive: fetched and verified backup for %s", target.pubKey)
			return backups, true
		}
	}
	// The full window was scanned (no timeout/transport error short-circuited the
	// loop) and no matching backup exists at the endpoint — a definitive miss.
	definitive = true
	log.Warn().Msgf("keyshare archive: no matching TssPool backup for %s found at the archive endpoint; staying degraded", target.pubKey)
	return recoveryBackups{}, false
}

// archiveBackupVerified gates an archive-fetched backup on cryptographic proof,
// never on trust in the endpoint. EVERY component the backup carries must decrypt
// with this node's recovery seed (DecryptRawKeyshares is AEAD-authenticated, so a
// component not sealed with our key fails here) AND the recovered share's stored
// pubkey must equal the vault pubkey this node already holds from THORChain for
// THAT component (secp256k1 -> vault pubkey, eddsa -> eddsa pool pubkey). Binding
// only secp256k1 would let a hostile endpoint splice a different vault's eddsa
// share — which rides along in the same fetched backup and is written to disk by
// recoverLegacyKeySharesFromBackups — past the gate. A component with no known
// pubkey to check against is rejected (fail-closed), as is any frost component
// (frost archive recovery is not yet supported; see the caller's kind guard).
func archiveBackupVerified(backups recoveryBackups, target keyshareRecoveryTarget) bool {
	// Require the components still MISSING locally (needsSecp/needsEddsa). A backup
	// lacking one of those cannot complete recovery, so rejecting it (and marking the
	// one-shot attempt) is correct — otherwise it would be re-fetched every supervisor
	// cycle. But a backup lacking a component this node ALREADY holds on disk is still
	// usable, so we do not require it. Every component that IS present is verified
	// below regardless.
	if target.needsSecp && len(backups.secp256k1) == 0 {
		return false
	}
	if target.needsEddsa && len(backups.eddsa) == 0 {
		return false
	}
	seedPhrase, err := recoverySeedPhrase()
	if err != nil {
		log.Warn().Err(err).Msg("keyshare archive: cannot read recovery seed to verify fetched backup")
		return false
	}
	verified := false
	if len(backups.secp256k1) > 0 {
		if target.pubKey.IsEmpty() || !rawKeyshareMatchesPubKey(backups.secp256k1, seedPhrase, target.pubKey) {
			return false
		}
		verified = true
	}
	if len(backups.eddsa) > 0 {
		if target.poolPubKeyEddsa.IsEmpty() || !rawKeyshareMatchesPubKey(backups.eddsa, seedPhrase, target.poolPubKeyEddsa) {
			return false
		}
		verified = true
	}
	// Frost shares are sealed with a network-scoped envelope, not a localstate
	// pub_key bound to a vault pubkey here, so refuse any that rode along rather
	// than write them unverified.
	if len(backups.frost) > 0 {
		return false
	}
	return verified
}

// rawKeyshareMatchesPubKey returns true iff the encrypted raw keyshare decrypts
// with seedPhrase (AEAD-authenticated) and the recovered localstate's stored
// pubkey equals want.
func rawKeyshareMatchesPubKey(encrypted []byte, seedPhrase string, want common.PubKey) bool {
	raw, err := DecryptRawKeyshares(encrypted, seedPhrase)
	if err != nil {
		return false
	}
	defer zeroBytes(raw)
	var ls struct {
		PubKey string `json:"pub_key"`
	}
	if err := json.Unmarshal(raw, &ls); err != nil {
		return false
	}
	return ls.PubKey != "" && ls.PubKey == want.String()
}

// maxArchiveBlockBytes caps the body of a single block response from the
// never-trusted archive endpoint so it cannot OOM bifrost at startup.
const maxArchiveBlockBytes = 32 * 1024 * 1024

// archiveFetchAttempted records vaults whose archive fetch reached a DEFINITIVE
// outcome (verification failed, or a complete scan found no backup), so the
// degraded-start supervisor's repeated recovery cycles do not re-scan the endpoint —
// the "one DEFINITIVE attempt per vault" guarantee. Transient failures (transport
// error, timeout, empty/garbage response, unreadable seed) are deliberately NOT
// recorded here, so a transiently-unavailable endpoint is retried on a later cycle.
var archiveFetchAttempted sync.Map

// bodyLimitedTransport wraps an http.RoundTripper to bound each response body.
type bodyLimitedTransport struct {
	rt  http.RoundTripper
	max int64
}

func (t bodyLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	resp.Body = limitedReadCloser{Reader: io.LimitReader(resp.Body, t.max), closer: resp.Body}
	return resp, nil
}

// CloseIdleConnections forwards to the wrapped transport so http.Client's
// CloseIdleConnections actually reaches it (the client only calls it on a
// transport that implements this method, which the bare wrapper otherwise hides).
func (t bodyLimitedTransport) CloseIdleConnections() {
	if c, ok := t.rt.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (l limitedReadCloser) Close() error { return l.closer.Close() }

// redactArchiveRPC strips any embedded credentials (userinfo, query, fragment)
// from the archive URL before it is logged, so operator secrets in the configured
// endpoint never land in bifrost logs.
func redactArchiveRPC(raw string) string {
	// A scheme-less endpoint such as "user:pass@archive:26657" is mis-split by
	// url.Parse (Scheme="user", Opaque="pass@archive:26657", User=nil), so clearing
	// u.User alone would leave the credentials intact in the returned string. Parse it
	// as a scheme-relative URL ("//host") so userinfo/host land in the right fields and
	// are actually redacted; keep an explicit scheme untouched.
	s, addedPrefix := raw, false
	if !strings.Contains(raw, "://") {
		s, addedPrefix = "//"+raw, true
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		// Could not parse into a host we can scrub. Never echo the raw endpoint — it
		// may carry credentials (userinfo, or a secret in a path/opaque segment) we
		// cannot reliably locate — so log a safe placeholder instead.
		return "[redacted archive_rpc]"
	}
	u.User = nil
	// Strip the path too: some RPC providers carry the API key in the path
	// (e.g. https://host/v1/<KEY> or https://host/<KEY>), not in userinfo or the
	// query. We cannot tell a secret path segment from a harmless route, so drop
	// the whole path; host:port is the only part worth logging for diagnostics.
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	out := u.String()
	if addedPrefix {
		out = strings.TrimPrefix(out, "//")
	}
	return out
}

func isFrostRecoveryBackupMissing(backups recoveryBackups, err error) bool {
	if err != nil {
		return errors.Is(err, errRecoveryBackupNotFound) || errors.Is(err, errRecoveryBackupUnrecoverable)
	}
	return len(backups.frost) == 0
}

// backupFromRawTx returns the recovery backup carried by a single raw block tx
// when it is a MsgTssPool matching the target, else ok=false. A tx bifrost's
// codec cannot decode — e.g. a plain cosmos bank MsgSend in old history whose
// type URL is not registered here — can never be the MsgTssPool backup, so it is
// skipped (ok=false) instead of aborting the scan. This keeps a wide scan from
// wedging startup in the unbounded legacy-recovery retry on one undecodable tx;
// a genuine block-fetch failure (handled by the caller) stays fatal and retryable.
func backupFromRawTx(txb []byte, dec func([]byte) (ctypes.Tx, error), signer ctypes.AccAddress, target keyshareRecoveryTarget) (backups recoveryBackups, ok bool) {
	// Decoding and message extraction run on UNTRUSTED bytes when scanning an
	// archive endpoint: a crafted tx can decode (e.g. an InjectTx wrapping an empty
	// Any) yet panic in GetMsgs on a nil cached message. A panic here would crash
	// bifrost before verification, so recover and treat the tx as a non-match.
	defer func() {
		if r := recover(); r != nil {
			log.Warn().Interface("panic", r).Msg("keyshare recovery: recovered from a panic decoding a tx (malformed or hostile data); skipping it and continuing the scan")
			backups = recoveryBackups{}
			ok = false
		}
	}()
	tx, err := dec(txb)
	if err != nil {
		// An undecodable tx can never be the MsgTssPool backup, so the scan skips it
		// (ok=false) instead of aborting — one legacy tx bifrost's codec cannot decode
		// must not wedge the unbounded startup-recovery loop. An unregistered type URL
		// is expected in old history (benign → Debug); any other decode error is
		// unexpected (possible corruption or a codec bug) and is surfaced at Warn so
		// it is not silently swallowed, while the scan still continues.
		if isUnregisteredTypeDecodeError(err) {
			log.Debug().Err(err).Msg("skipping tx with an unregistered type while scanning for keyshare backup")
		} else {
			log.Warn().Err(err).Msg("skipping tx that failed to decode unexpectedly while scanning for keyshare backup; continuing scan")
		}
		return recoveryBackups{}, false
	}
	for _, msg := range tx.GetMsgs() {
		if m, ok := msg.(*types.MsgTssPool); ok && msgMatchesRecoveryTarget(m, signer, target) {
			return recoveryBackups{
				secp256k1: m.KeysharesBackup,
				eddsa:     m.KeysharesBackupEddsa,
				frost:     m.KeysharesBackupFrost,
			}, true
		}
	}
	return recoveryBackups{}, false
}

// isUnregisteredTypeDecodeError reports whether a tx decode failure is the
// expected "this type URL is not registered in bifrost's codec" case (a legacy
// message kind bifrost never needs to decode), as opposed to an unexpected
// decode error worth surfacing at a higher log level.
func isUnregisteredTypeDecodeError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unable to resolve type URL") ||
		strings.Contains(msg, "no concrete type registered") ||
		strings.Contains(msg, "no message type registered") ||
		strings.Contains(msg, "unknown message")
}

func msgMatchesRecoveryTarget(m *types.MsgTssPool, signer ctypes.AccAddress, target keyshareRecoveryTarget) bool {
	if !m.Signer.Equals(signer) {
		return false
	}
	switch target.kind {
	case recoveryTargetFrost:
		return m.KeygenType == types.KeygenType_XMRMonovaultKeygen && m.PoolPubKeyFrost.Equals(target.pubKey)
	case recoveryTargetLegacy:
		return m.PoolPubKey.Equals(target.pubKey)
	default:
		return false
	}
}

func recoverScanLowerBound(lastVaultHeight, maxScanBlocks int64) int64 {
	if lastVaultHeight <= 0 {
		return 0
	}
	lowerBound := lastVaultHeight - maxScanBlocks
	if lowerBound < 0 {
		return 0
	}
	return lowerBound
}

func recoverScanBounds(lastVaultHeight, currentHeight, maxScanBlocks int64) (int64, int64) {
	if lastVaultHeight <= 0 {
		return 0, 0
	}
	lowerBound := recoverScanLowerBound(lastVaultHeight, maxScanBlocks)
	if maxScanBlocks <= 0 {
		return lowerBound, lastVaultHeight
	}

	upperBound := lastVaultHeight
	if currentHeight > lastVaultHeight {
		upperBound = currentHeight
		maxUpperBound := lastVaultHeight + maxScanBlocks
		if upperBound > maxUpperBound {
			upperBound = maxUpperBound
		}
	}
	return lowerBound, upperBound
}

// applyPruneFloor clamps a scan range to the node's earliest locally-available
// block. It returns the adjusted lower bound, whether the floor actually clamped
// the range (part of the intended window is pruned), and whether the whole range
// is below the floor (i.e. the backup is unrecoverable from this node's pruned
// history). earliest <= 0 disables the floor (height unknown). The scan loop
// uses the condition i > lowerBound, so the returned bound is earliest-1 to keep
// earliest itself in range.
func applyPruneFloor(scanLowerBound, scanUpperBound, earliest int64) (lower int64, clamped, unrecoverable bool) {
	if earliest <= 0 {
		return scanLowerBound, false, false
	}
	if scanUpperBound < earliest {
		return scanLowerBound, false, true
	}
	if lowestFetched := earliest - 1; scanLowerBound < lowestFetched {
		return lowestFetched, true, false
	}
	return scanLowerBound, false, false
}

// blockPrunedMidScan reports whether a failed block fetch at height is explained
// by the node's history floor having advanced past it since the scan started
// (pruning during a long scan, or /status and Block() served by different
// instances behind a load balancer). Only a freshly-read floor counts: if that
// read failed or returned no floor, the fetch failure stays transient and
// retryable rather than being misreported as permanently unrecoverable.
func blockPrunedMidScan(height, freshEarliest int64, boundsErr error) bool {
	if boundsErr != nil || freshEarliest <= 0 {
		return false
	}
	return height < freshEarliest
}

// scanMissSentinel picks the error class for a scan that found no TssPool
// message, where the scan bounds were reliable and the full intended window was
// covered (incomplete scans — a bounds-read failure or a window upper-capped by a
// still-syncing height — are handled by the caller and never reach here). Both
// outcomes are permanent for pre-startup purposes, so bifrost starts degraded and
// the background supervisor keeps retrying rather than wedging the foreground
// loop: an unclamped miss means the backup was never posted in available history
// (errRecoveryBackupNotFound), and a clamped miss means the message may sit in the
// pruned part below the node's floor (errRecoveryBackupUnrecoverable). Repointing
// RPC at an archive node widens the window again.
func scanMissSentinel(clamped bool) error {
	if clamped {
		return errRecoveryBackupUnrecoverable
	}
	return errRecoveryBackupNotFound
}

// nodeBlockBounds returns the connected thornode's current height and its earliest
// locally-available (block-store base) height from a single /status call, bounded by
// a timeout so a hung RPC cannot stall startup. Both come from one snapshot, so the
// prune floor and the scan upper bound are consistent. Heights below the earliest are
// pruned and cannot be fetched, so keyshare recovery must not scan below it.
func nodeBlockBounds(thorchain thorclient.ThorchainBridge) (current, earliest int64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), nodeStatusTimeout)
	defer cancel()
	status, err := thorchain.GetContext().Client.Status(ctx)
	if err != nil {
		return 0, 0, err
	}
	return status.SyncInfo.LatestBlockHeight, status.SyncInfo.EarliestBlockHeight, nil
}

func recoverySeedPhrase() (string, error) {
	seedPhrase, err := common.GetSignerSeedPhrase()
	if err != nil {
		return "", fmt.Errorf("failed to get signer seed phrase: %w", err)
	}
	seedPhrase = strings.TrimSpace(seedPhrase)
	if seedPhrase == "" {
		return "", fmt.Errorf("signer seed phrase is empty")
	}
	return seedPhrase, nil
}

func writeRecoveredDecompressedKeyshares(path string, encrypted []byte, seedPhrase string) error {
	if len(encrypted) == 0 {
		return fmt.Errorf("encrypted key shares backup is empty")
	}

	decrypted, err := DecryptKeyshares(encrypted, seedPhrase)
	if err != nil {
		return fmt.Errorf("failed to decrypt key shares: %w", err)
	}

	rawKeyshares, err := io.ReadAll(lzma.NewReader(bytes.NewReader(decrypted)))
	if err != nil {
		return fmt.Errorf("failed to decompress key shares: %w", err)
	}

	encryptedAtRest, err := encryptAtRestForRecovery(rawKeyshares, seedPhrase)
	if err != nil {
		return err
	}
	return writeRecoveredBytes(path, encryptedAtRest)
}

func writeRecoveredRawKeyshares(path string, encrypted []byte, seedPhrase, network string, vaultPubKey common.PubKey) error {
	if len(encrypted) == 0 {
		return fmt.Errorf("encrypted raw key shares backup is empty")
	}

	rawKeyshares, err := DecryptRawKeyshares(encrypted, seedPhrase)
	if err != nil {
		return fmt.Errorf("failed to decrypt raw key shares: %w", err)
	}
	defer zeroBytes(rawKeyshares)

	envelope, err := encodeFrostRecoveryEnvelope(rawKeyshares, network, vaultPubKey)
	if err != nil {
		return err
	}
	defer zeroBytes(envelope)

	encryptedAtRest, err := encryptAtRestForRecovery(envelope, seedPhrase)
	if err != nil {
		return err
	}
	return writeRecoveredBytes(path, encryptedAtRest)
}

func frostRecoveryNetwork(conf config.Bifrost) (string, error) {
	network := strings.TrimSpace(conf.GetChains()[common.XMRChain].SignerExpectedNetwork)
	switch network {
	case "mocknet", "chainnet", "stagenet", "mainnet":
		return network, nil
	default:
		return "", fmt.Errorf("xmr signer expected network must be one of mocknet, chainnet, stagenet, or mainnet")
	}
}

func frostRecoveryGroupKey(vaultPubKey common.PubKey) (string, error) {
	if vaultPubKey.IsEmpty() {
		return "", fmt.Errorf("frost recovery vault pubkey is empty")
	}
	pk, err := vaultPubKey.Decode()
	if err != nil {
		return "", fmt.Errorf("fail to decode frost recovery vault pubkey: %w", err)
	}
	raw := pk.Bytes()
	if len(raw) != 32 {
		return "", fmt.Errorf("unexpected frost recovery group key length %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

func encodeFrostRecoveryEnvelope(keyBytes []byte, network string, vaultPubKey common.PubKey) ([]byte, error) {
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("frost recovery key material is empty")
	}
	if err := validateFrostRecoveryNetwork(network); err != nil {
		return nil, err
	}
	groupKey, err := frostRecoveryGroupKey(vaultPubKey)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(frostRecoveryEnvelope{
		Protocol:    frostRecoveryProtocol,
		Version:     frostRecoveryVersion,
		Network:     network,
		VaultPubKey: vaultPubKey.String(),
		GroupKey:    groupKey,
		KeyBytesB64: base64.StdEncoding.EncodeToString(keyBytes),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode frost recovery envelope: %w", err)
	}
	return append([]byte(frostRecoveryEnvelopeMagic), payload...), nil
}

func decodeFrostRecoveryEnvelope(data []byte, expectedNetwork string, expectedVault common.PubKey) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte(frostRecoveryEnvelopeMagic)) {
		return nil, fmt.Errorf("frost recovery envelope magic/version mismatch")
	}
	if err := validateFrostRecoveryNetwork(expectedNetwork); err != nil {
		return nil, err
	}
	groupKey, err := frostRecoveryGroupKey(expectedVault)
	if err != nil {
		return nil, err
	}

	var envelope frostRecoveryEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data[len(frostRecoveryEnvelopeMagic):]))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("invalid frost recovery envelope: %w", err)
	}
	var extra any
	if trailingErr := decoder.Decode(&extra); !errors.Is(trailingErr, io.EOF) {
		if trailingErr == nil {
			return nil, fmt.Errorf("invalid frost recovery envelope: trailing JSON value")
		}
		return nil, fmt.Errorf("invalid frost recovery envelope trailer: %w", trailingErr)
	}
	if envelope.Protocol != frostRecoveryProtocol || envelope.Version != frostRecoveryVersion {
		return nil, fmt.Errorf("frost recovery protocol/version mismatch")
	}
	if envelope.Network != expectedNetwork {
		return nil, fmt.Errorf("frost recovery network mismatch")
	}
	if envelope.VaultPubKey != expectedVault.String() {
		return nil, fmt.Errorf("frost recovery vault pubkey mismatch")
	}
	if envelope.GroupKey != groupKey {
		return nil, fmt.Errorf("frost recovery group key mismatch")
	}
	keyBytes, err := base64.StdEncoding.Strict().DecodeString(envelope.KeyBytesB64)
	if err != nil {
		return nil, fmt.Errorf("invalid frost recovery key material encoding: %w", err)
	}
	if len(keyBytes) == 0 || int64(len(keyBytes)) > maxFrostRecoveryFileBytes {
		zeroBytes(keyBytes)
		return nil, fmt.Errorf("frost recovery key material size is outside bounds")
	}
	return keyBytes, nil
}

func validateFrostRecoveryNetwork(network string) error {
	switch network {
	case "mocknet", "chainnet", "stagenet", "mainnet":
		return nil
	default:
		return fmt.Errorf("invalid frost recovery network")
	}
}

func zeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}

// encryptAtRestForRecovery at-rest encrypts recovered key material before it is
// written to disk. It fails hard rather than falling back to plaintext: the xmr
// sidecar rejects plaintext keyshares on restore, and key material must never be
// persisted or transported in the clear.
func encryptAtRestForRecovery(data []byte, seedPhrase string) ([]byte, error) {
	atRestKey := storage.DeriveAtRestKey(seedPhrase)
	encrypted, err := storage.EncryptAtRest(atRestKey, data)
	if err != nil {
		return nil, fmt.Errorf("failed to at-rest encrypt recovered keyshares: %w", err)
	}
	return encrypted, nil
}

func writeRecoveredBytes(path string, data []byte) error {
	recoveryArtifactMu.Lock()
	defer recoveryArtifactMu.Unlock()

	if len(data) == 0 || int64(len(data)) > maxFrostRecoveryFileBytes {
		return fmt.Errorf("recovery artifact size %d is outside 1..%d", len(data), maxFrostRecoveryFileBytes)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to open recovery temp file: %w", err)
	}

	tmpPath := tmp.Name()
	success := false
	defer func() {
		_ = tmp.Close()
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("failed to set recovery temp file permissions: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("failed to write recovery temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("failed to sync recovery temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close recovery temp file: %w", err)
	}

	if fi, statErr := os.Lstat(path); statErr == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("recovery destination is not a regular file: %s", path)
		}
		if fi.Size() != 0 {
			return fmt.Errorf("recovery file already exists: %s", path)
		}
		// A zero-byte regular file is not a recovered keyshare, but leaving it
		// in place makes every retry fail the no-overwrite guard below. Preserve
		// it for inspection, sync the rename, then publish the authenticated
		// replacement through the same temp-file path as a missing artifact.
		if err := quarantineRecoveryFileLocked(path); err != nil {
			return fmt.Errorf("failed to quarantine empty recovery file: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to inspect recovery destination: %w", statErr)
	}
	// Recovery writers in this process are serialized by recoveryArtifactMu;
	// the node home is a single-process ownership boundary. Rename publishes a
	// single durable name, avoiding a crash state that leaves a second hard link
	// to key material under the temporary filename.
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to publish recovery file: %w", err)
	}
	if err := syncRecoveryDirectory(filepath.Dir(path)); err != nil {
		return err
	}

	success = true
	return nil
}

func syncRecoveryDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open recovery directory: %w", err)
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("failed to sync recovery directory: %w", err)
	}
	return nil
}
