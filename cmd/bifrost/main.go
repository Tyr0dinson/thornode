package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	golog "github.com/ipfs/go-log"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	flag "github.com/spf13/pflag"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/observer"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/signer"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	btss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/cmd"
	tcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

const (
	serverIdentity = "bifrost"
)

// Startup dependencies, overridable in tests so main can run without real p2p
// networking, TSS pre-parameter generation or chain RPC endpoints.
var (
	startP2P   = p2p.StartP2PWithBridge
	newTss     = tss.NewTss
	loadChains = chainclients.LoadChains
)

func printVersion() {
	fmt.Printf("%s v%s, rev %s\n", serverIdentity, constants.Version, constants.GitCommit)
}

func main() {
	showVersion := flag.Bool("version", false, "Shows version")
	logLevel := flag.StringP("log-level", "l", "info", "Log Level")
	pretty := flag.BoolP("pretty-log", "p", false, "Enables unstructured prettified logging. This is useful for local debugging")
	deckDump := flag.String("deck-dump", "", "Path to a deck dump file")
	flag.Parse()

	if *showVersion {
		printVersion()
		return
	}

	initPrefix()
	initLog(*logLevel, *pretty)
	config.Init()
	config.InitBifrost()
	cfg := config.GetBifrost()

	// metrics
	m, err := metrics.NewMetrics(cfg.Metrics)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create metric instance")
	}
	if err = m.Start(); err != nil {
		log.Fatal().Err(err).Msg("fail to start metric collector")
	}
	if len(cfg.Thorchain.SignerName) == 0 {
		log.Fatal().Msg("signer name is empty")
	}
	if len(cfg.Thorchain.SignerPasswd) == 0 {
		log.Fatal().Msg("signer password is empty")
	}
	kb, _, err := thorclient.GetKeyringKeybase(cfg.Thorchain.ChainHomeFolder, cfg.Thorchain.SignerName, cfg.Thorchain.SignerPasswd)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to get keyring keybase")
	}

	k := thorclient.NewKeysWithKeybase(kb, cfg.Thorchain.SignerName, cfg.Thorchain.SignerPasswd)
	// thorchain bridge
	thorchainBridge, err := thorclient.NewThorchainBridge(cfg.Thorchain, m, k)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create new thorchain bridge")
	}
	if err = thorchainBridge.EnsureNodeWhitelistedWithTimeout(); err != nil {
		log.Fatal().Err(err).Msg("node account is not whitelisted, can't start")
	}
	// PubKey Manager
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(thorchainBridge, m)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create pubkey manager")
	}
	if err = pubkeyMgr.Start(); err != nil {
		log.Fatal().Err(err).Msg("fail to start pubkey manager")
	}

	// Legacy shares back the process-wide TSS stack and remain a startup
	// prerequisite. XMR FROST shares are isolated by exact-vault readiness, so a
	// missing historical XMR vault is retried without disabling healthy chains.
	// A definitively-unrecoverable keyshare (any vault — recovery no longer depends
	// on signing status) does not block startup:
	// bifrost starts degraded (that vault's keysigns fail, everything else —
	// observations included — keeps working) while recovery retries in the
	// background.
	startBifrost, degraded, recoverAtHeight := recoverLegacyKeysharesBeforeStartup(cfg, thorchainBridge)
	if !startBifrost {
		return
	}
	recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
	// Exactly ONE legacy-recovery background path runs — never both — because each
	// re-runs the FULL recovery (every vault) and writes the same localstate files;
	// two concurrently would race on those writes.
	switch {
	case degraded:
		// A backup is permanently unavailable from this node (pruned / never posted).
		// The supervisor re-runs the full recovery on a backoff and, when the error
		// set also carries a not-yet-synced vault, picks that up too once the node
		// syncs — so it subsumes the one-shot trigger for the mixed case.
		go superviseLegacyKeyshareRecovery(recoveryCtx, cfg, thorchainBridge)
	case recoverAtHeight > 0:
		// Pure not-yet-synced: the only failure is a backup above our synced height.
		// Don't block startup or time-backoff; recover exactly once when the node
		// syncs past the backup height (one compare per THORChain block, bounded by
		// a max wait, with a supervisor fallback inside on failure/timeout).
		go awaitHeightAndRecoverLegacyKeyshares(recoveryCtx, cfg, thorchainBridge, recoverAtHeight)
	}
	go superviseFrostKeyshareRecovery(recoveryCtx, cfg, thorchainBridge)

	// setup TSS signing
	priKey, err := k.GetPrivateKey()
	if err != nil {
		log.Fatal().Err(err).Msg("fail to get secp256k1 private key")
	}

	tmPrivateKey := tcommon.CosmosPrivateKeyToTMPrivateKey(priKey)

	priKeyEddsa, err := k.GetPrivateKeyEDDSA()
	if err != nil {
		log.Fatal().Err(err).Msg("fail to get eddsa private key")
	}

	tmPrivateKeyEddsa := tcommon.CosmosPrivateKeyToTMPrivateKey(priKeyEddsa)

	consts := constants.NewConstantValue()
	jailTimeKeysign := time.Duration(consts.GetInt64Value(constants.JailTimeKeysign)) * constants.ThorchainBlockTime
	if err = checkKeygenTimeout(cfg.Signer.KeygenTimeout, consts); err != nil {
		log.Fatal().Err(err).Msg("keygen timeout must be shorter than jail time")
	}
	// FrostKeygenTimeout is intentionally NOT bound by the keygen jail times.
	// FROST DKG is a 3-round p2p protocol whose round-2 message exchange
	// can legitimately exceed the aggressive jail times used in test
	// environments. The 60s
	// floor inside NewFrostKeygenCoordinator + per-environment overrides
	// in docker-compose are the relevant safety nets here.
	if cfg.Signer.KeysignTimeout >= jailTimeKeysign {
		log.Fatal().
			Stringer("keysignTimeout", cfg.Signer.KeysignTimeout).
			Stringer("keysignJail", jailTimeKeysign).
			Msg("keysign timeout must be shorter than jail time")
	}

	// Start P2P with bonded node gating enabled
	comm, stateManager, err := startP2P(
		cfg.TSS,
		tmPrivateKey,
		app.DefaultNodeHome,
		thorchainBridge,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to start p2p")
	}

	tssIns, err := newTss(
		comm,
		stateManager,
		tmPrivateKey,
		tmPrivateKeyEddsa,
		common.TssConfig{
			EnableMonitor:      true,
			KeyGenTimeout:      cfg.Signer.KeygenTimeout,
			FrostKeygenTimeout: cfg.Signer.FrostKeygenTimeout,
			KeySignTimeout:     cfg.Signer.KeysignTimeout,
			PartyTimeout:       cfg.Signer.PartyTimeout,
			PreParamTimeout:    cfg.Signer.PreParamTimeout,
		},
		nil,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create tss instance")
	}

	if err = tssIns.Start(); err != nil {
		log.Err(err).Msg("fail to start tss instance")
	}

	cfgChains := cfg.GetChains()

	// ensure we have a protocol for chain RPC Hosts
	for _, chainCfg := range cfgChains {
		if chainCfg.Disabled {
			continue
		}
		if len(chainCfg.RPCHost) == 0 {
			log.Fatal().Err(err).Stringer("chain", chainCfg.ChainID).Msg("missing chain RPC host")
			return
		}
		if !strings.HasPrefix(chainCfg.RPCHost, "http") {
			chainCfg.RPCHost = fmt.Sprintf("http://%s", chainCfg.RPCHost)
		}
	}
	poolMgr := thorclient.NewPoolMgr(thorchainBridge)
	chains, restart := loadChains(k, cfgChains, tssIns, thorchainBridge, m, pubkeyMgr, poolMgr)
	if len(chains) == 0 {
		log.Fatal().Msg("fail to load any chains")
	}
	tssKeysignMetricMgr := metrics.NewTssKeysignMetricMgr()
	healthServer := NewHealthServer(cfg.TSS.InfoAddress, tssIns, chains)
	go func() {
		defer log.Info().Msg("health server exit")
		if err = healthServer.Start(); err != nil {
			log.Error().Err(err).Msg("fail to start health server")
		}
	}()

	ctx := context.Background()

	// start observer notifier
	ag, err := observer.NewAttestationGossip(comm.GetHost(), k, cfg.Thorchain.ChainEBifrost, thorchainBridge, m, cfg.AttestationGossip)

	// start observer
	obs, err := observer.NewObserver(pubkeyMgr, chains, thorchainBridge, m, cfgChains[tcommon.BTCChain].BlockScanner.DBPath, tssKeysignMetricMgr, ag, *deckDump)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create observer")
	}
	// Register committed-event callbacks before Start launches the gossip event
	// client. NewObserver also does this defensively for non-main call sites.
	ag.SetObserverHandleObservedTxCommitted(obs)
	ag.SetObserverHandleErrataTxCommitted(obs)
	if xmrClient, ok := chains[tcommon.XMRChain].(*monero.Client); ok {
		ag.SetObserverApplySpentOutputRefs(xmrClient.ApplySpentOutputRefsWithOutcome)
		ag.SetObserverApplyCommittedSpentRefs(xmrClient.ApplyCommittedSpentOutputRefs)
	}
	if err = obs.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("fail to start observer")
	}

	// start signer
	sign, err := signer.NewSigner(cfg, thorchainBridge, k, pubkeyMgr, tssIns, chains, m, tssKeysignMetricMgr, obs)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create instance of signer")
	}
	if err = sign.Start(); err != nil {
		log.Fatal().Err(err).Msg("fail to start signer")
	}

	// wait....
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ch:
	case <-restart:
	}
	log.Info().Msg("stop signal received")
	cancelRecovery()
	// stop observer
	if err = obs.Stop(); err != nil {
		log.Fatal().Err(err).Msg("fail to stop observer")
	}
	// stop signer
	if err = sign.Stop(); err != nil {
		log.Fatal().Err(err).Msg("fail to stop signer")
	}
	// stop go tss
	tssIns.Stop()
	if err = healthServer.Stop(); err != nil {
		log.Fatal().Err(err).Msg("fail to stop health server")
	}
}

// checkKeygenTimeout ensures the keygen timeout is shorter than every jail
// period a failed keygen can produce, so a jailed node is never released
// while the keygen that blamed it may still be running. JailTimeKeygen
// covers the flat jail and JailTimeKeygenBase the first escalated jail of a
// non-active node.
func checkKeygenTimeout(keygenTimeout time.Duration, consts constants.ConstantValues) error {
	for _, name := range []constants.ConstantName{constants.JailTimeKeygen, constants.JailTimeKeygenBase} {
		jail := time.Duration(consts.GetInt64Value(name)) * constants.ThorchainBlockTime
		if keygenTimeout >= jail {
			return fmt.Errorf("keygen timeout %s is not shorter than %s (%s)", keygenTimeout, name, jail)
		}
	}
	return nil
}

// recoverLegacyKeysharesFn is the legacy keyshare recovery entrypoint behind a
// package var so the startup barrier, the one-shot height trigger, and the
// background supervisor can be unit-tested without a live thornode. Production
// always uses btss.RecoverLegacyKeyShares (identical behavior); only tests
// override it.
var recoverLegacyKeysharesFn = btss.RecoverLegacyKeyShares

// legacyRecoveryTickInterval is the per-THORChain-block cadence shared by the
// pre-startup retry loop and the one-shot height trigger. It is a var defaulting
// to the real block time so tests can tick fast; production behavior is unchanged.
var legacyRecoveryTickInterval = constants.ThorchainBlockTime

// recoverLegacyKeysharesBeforeStartup returns (start, degraded, recoverAtHeight).
// start is false only when a shutdown signal interrupts the retry loop. degraded
// is true when a backup is permanently unavailable from this node (pruned below
// the block-store floor, or never posted in the fully-available scan window) —
// startup proceeds without those shares and the background supervisor keeps
// retrying (an operator restore / archive RPC is the resolution). recoverAtHeight
// is non-zero when a backup's block is above our current synced height: startup
// proceeds, and a one-shot trigger recovers it once the node syncs past that
// height (see awaitHeightAndRecoverLegacyKeyshares). The two are mutually
// exclusive: a permanent member returns (degraded=true, recoverAtHeight=0) because
// the background supervisor re-runs the FULL recovery and subsumes the one-shot
// trigger (so any not-yet-synced sibling is covered there too), while a purely
// not-yet-synced result returns (degraded=false, recoverAtHeight>0). Only a
// genuinely transient failure (RPC/disk) retries here before TSS startup — a
// permanent or not-yet-synced result never wedges the loop (the 3.20.0
// signing-stall class).
func recoverLegacyKeysharesBeforeStartup(cfg config.Bifrost, bridge thorclient.ThorchainBridge) (start, degraded bool, recoverAtHeight int64) {
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	for attempt := 1; ; attempt++ {
		err := recoverLegacyKeysharesFn(cfg, bridge)
		if err == nil {
			return true, false, 0
		}
		// A permanently-unavailable vault (pruned below the block-store floor, or never
		// posted in the fully-available scan window) can NEVER be fixed by a foreground
		// retry, so start DEGRADED immediately and let the background supervisor re-run
		// the FULL recovery — which also retries any transient or not-yet-synced sibling
		// carried in the same joined result. Blocking startup while a transient /
		// unclassified sibling clears would re-introduce the wedge: a persistent
		// non-sentinel error coexisting with a permanent one would loop the pre-startup
		// barrier forever and never start TSS (the 3.20.0 signing-stall class). develop
		// starts degraded on any permanent member for exactly this reason.
		if btss.IsPermanentKeyshareRecoveryFailure(err) {
			log.Error().Err(err).Msg("legacy keyshares for a vault are unrecoverable by retrying (pruned or never posted); starting bifrost DEGRADED and retrying recovery in the background")
			return true, true, 0
		}
		// Purely not-yet-synced (no transient member left to clear): don't busy-retry
		// the full scan while the node is still catching up — start degraded and arm the
		// one-shot height trigger that recovers once local height reaches the backup
		// height. A transient sibling (HasRetryableKeyshareComponent) takes the
		// foreground-retry path below instead, so it clears promptly.
		if syncHeight, notYetSynced := btss.NotYetSyncedKeyshareHeight(err); notYetSynced && !btss.HasRetryableKeyshareComponent(err) {
			log.Error().Err(err).Int64("recover_at_height", syncHeight).Msg("legacy keyshare backup is above the node's synced height; starting bifrost DEGRADED and arming a one-shot recovery once the node syncs past it")
			return true, false, syncHeight
		}
		// A genuinely transient member (RPC/disk flake), possibly alongside a
		// not-yet-synced sibling: retry in the foreground so it clears quickly before
		// TSS startup rather than being abandoned to the slower background supervisor.
		log.Error().Err(err).Int("attempt", attempt).Msg("legacy keyshare recovery incomplete (transient); retrying before TSS startup")
		timer := time.NewTimer(legacyRecoveryTickInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return false, false, 0
		}
	}
}

// legacyRecoverySyncMaxWait bounds the one-shot not-yet-synced recovery wait so an
// unreachable height (misconfigured RPC, wrong vault height) cannot leave it armed
// forever — it then hands off to the background supervisor rather than silently
// skip. The height is compared once per THORChain block (constants.ThorchainBlockTime,
// a var not a const), not in a busy loop.
var legacyRecoverySyncMaxWait = 6 * time.Hour

// awaitHeightAndRecoverLegacyKeyshares runs legacy recovery exactly once, when the
// node's THORChain height first reaches recoverAtHeight — the block carrying a
// backup that was above our synced height at startup. It compares once per
// THORChain block (no tight poll, no time-backoff) and, if the height is never
// reached within legacyRecoverySyncMaxWait, loudly alerts and hands off to the
// background supervisor instead of giving up silently. If recovery runs once the
// height is reached but a vault is permanently unavailable, it also hands off (only
// an operator restore / archive can resolve that); a transient failure instead
// keeps retrying on the next block, and a freshly not-yet-synced result re-arms on
// the new height — so a not-yet-synced vault always has a retry safety net.
func awaitHeightAndRecoverLegacyKeyshares(ctx context.Context, cfg config.Bifrost, bridge thorclient.ThorchainBridge, recoverAtHeight int64) {
	deadline := time.NewTimer(legacyRecoverySyncMaxWait)
	defer deadline.Stop()
	ticker := time.NewTicker(legacyRecoveryTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			log.Error().Int64("recover_at_height", recoverAtHeight).Dur("waited", legacyRecoverySyncMaxWait).Msg(
				"BIFROST DEGRADED: node never synced to the legacy keyshare backup height within the max wait — that vault's keysigns still fail; check THORNode sync/RPC. Handing off to the background recovery supervisor")
			superviseLegacyKeyshareRecovery(ctx, cfg, bridge)
			return
		case <-ticker.C:
			height, err := bridge.GetBlockHeight()
			if err != nil {
				log.Warn().Err(err).Msg("one-shot keyshare recovery: fail to read THORChain height while waiting to sync to the backup height")
				continue
			}
			if height < recoverAtHeight {
				continue
			}
			if recErr := recoverLegacyKeysharesFn(cfg, bridge); recErr != nil {
				// A permanently-unavailable vault (pruned / never-posted) can never be
				// fixed by waiting for a sync height, so hand it straight to the
				// supervisor rather than re-running the full scan every block until the
				// deadline. Judge per leaf (same decomposition the startup barrier uses)
				// so a not-yet-synced sibling — e.g. a fresh membership after a churn
				// while waiting — cannot mask the permanent member.
				if btss.IsPermanentKeyshareRecoveryFailure(recErr) {
					log.Error().Err(recErr).Int64("height", height).Msg(
						"one-shot keyshare recovery reached the backup height but a vault is permanently unavailable; handing off to the background recovery supervisor")
					superviseLegacyKeyshareRecovery(ctx, cfg, bridge)
					return
				}
				// A transient member (an RPC/disk flake on another vault) needs a
				// prompt retry, not a wait — keep re-running every block so it clears
				// quickly. Do NOT advance the target past it: advancing would suppress
				// that vault's retry until the unrelated sync height (or the deadline).
				// Only a result with no transient member advances the wait.
				if btss.HasRetryableKeyshareComponent(recErr) {
					continue
				}
				// Purely not-yet-synced (GetBlockHeight is cached and may be served by a
				// peer/LB ahead of the locally-connected node, whereas recovery re-reads
				// the authoritative /status): keep the responsive per-block wait, but
				// advance the target to the newly-reported height so we do not re-run the
				// full recovery on every block until the node reaches it.
				if next, syncing := btss.NotYetSyncedKeyshareHeight(recErr); syncing {
					if next > recoverAtHeight {
						recoverAtHeight = next
					}
					continue
				}
				log.Error().Err(recErr).Int64("height", height).Int64("recover_at_height", recoverAtHeight).Msg(
					"one-shot keyshare recovery ran at the synced height but did not complete; handing off to the background recovery supervisor")
				superviseLegacyKeyshareRecovery(ctx, cfg, bridge)
				return
			}
			log.Info().Int64("height", height).Msg("one-shot keyshare recovery completed after the node synced to the backup height; this node signs for the recovered vault from the next keysign")
			return
		}
	}
}

// legacyRecoveryRetryInitial/Max bound the background recovery backoff. The
// doubling cap keeps the worst case (a found-but-empty backup, where each
// re-attempt rescans blocks for the TssPool message) to ~48 scans/day while
// still noticing an operator fix — restored localstate short-circuits before
// any scan — within a minute at first. legacyRecoveryRetryInitial is a var (not a
// const) only so tests can shrink the supervisor's first-attempt delay; production
// behavior is unchanged.
var legacyRecoveryRetryInitial = time.Minute

const legacyRecoveryRetryMax = 30 * time.Minute

// superviseLegacyKeyshareRecovery re-attempts legacy keyshare recovery in the
// background after bifrost started degraded. The moment an attempt succeeds —
// the operator restores localstate-<pubkey>.json from a backup, points the
// THORNode RPC at an archive node with deeper history, or block backfill passes
// the prune floor — the shares land on disk and the very next keysign picks them
// up, because go-tss reads keyshares from disk per keysign; no restart needed.
// Until then it screams on every cycle so the degraded state cannot be missed.
func superviseLegacyKeyshareRecovery(ctx context.Context, cfg config.Bifrost, bridge thorclient.ThorchainBridge) {
	recovered := retryRecoveryUntilDone(ctx, legacyRecoveryRetryInitial, legacyRecoveryRetryMax,
		func() error { return recoverLegacyKeysharesFn(cfg, bridge) },
		func(err error, attempt int, next time.Duration) {
			// The supervisor handles two causes; give the operator the right remedy.
			// A purely not-yet-synced result (the sync-timeout handoff from
			// awaitHeightAndRecoverLegacyKeyshares) means nothing is missing — the node
			// just hasn't reached the backup height — so the restore/archive advice
			// would be wrong. Any permanent member means a backup really is gone.
			if _, syncing := btss.NotYetSyncedKeyshareHeight(err); syncing && !btss.IsPermanentKeyshareRecoveryFailure(err) {
				log.Error().Err(err).Int("attempt", attempt).Dur("next_retry_in", next).Msg(
					"BIFROST RUNNING DEGRADED: a vault's keyshare backup is above this node's synced height; its keysigns fail until the node catches up — no restore needed, this clears as the node syncs; recovery keeps retrying in the background")
				return
			}
			log.Error().Err(err).Int("attempt", attempt).Dur("next_retry_in", next).Msg(
				"BIFROST RUNNING DEGRADED: keyshares for a vault are missing, its keysigns fail — restore localstate-<pubkey>.json from backup or point THORNode RPC at an archive node; recovery keeps retrying in the background")
		})
	if recovered {
		log.Info().Msg("legacy keyshare recovery completed in the background; this node signs for the recovered vault from the next keysign")
	}
}

// retryRecoveryUntilDone runs attempt on a doubling backoff capped at maxDelay,
// waiting BEFORE each attempt (the caller just failed one). onFailure observes
// every failed attempt along with the delay chosen for the next. It returns true
// once attempt succeeds, or false when ctx ends first.
func retryRecoveryUntilDone(ctx context.Context, initial, maxDelay time.Duration, attempt func() error, onFailure func(err error, attempt int, next time.Duration)) bool {
	delay := initial
	for n := 1; ; n++ {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return false
		}
		err := attempt()
		if err == nil {
			return true
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
		onFailure(err, n, delay)
	}
}

func superviseFrostKeyshareRecovery(ctx context.Context, cfg config.Bifrost, bridge thorclient.ThorchainBridge) {
	for attempt := 1; ; attempt++ {
		if err := btss.RecoverFrostKeyShares(cfg, bridge); err == nil {
			return
		} else {
			log.Error().Err(err).Int("attempt", attempt).Msg("XMR FROST keyshare recovery incomplete; healthy vaults remain available while retrying")
		}

		timer := time.NewTimer(constants.ThorchainBlockTime)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func initPrefix() {
	cosmosSDKConfg := cosmos.GetConfig()
	cosmosSDKConfg.SetBech32PrefixForAccount(cmd.Bech32PrefixAccAddr, cmd.Bech32PrefixAccPub)
	cosmosSDKConfg.Seal()
}

func initLog(level string, pretty bool) {
	l, err := zerolog.ParseLevel(level)
	if err != nil {
		log.Warn().Msgf("%s is not a valid log-level, falling back to 'info'", level)
	}
	var out io.Writer = os.Stdout
	if pretty {
		out = zerolog.ConsoleWriter{Out: os.Stdout}
	}
	zerolog.SetGlobalLevel(l)
	log.Logger = log.Output(out).With().Caller().Str("service", serverIdentity).Logger()

	logLevel := golog.LevelInfo
	switch l {
	case zerolog.DebugLevel:
		logLevel = golog.LevelDebug
	case zerolog.InfoLevel:
		logLevel = golog.LevelInfo
	case zerolog.ErrorLevel:
		logLevel = golog.LevelError
	case zerolog.FatalLevel:
		logLevel = golog.LevelFatal
	case zerolog.PanicLevel:
		logLevel = golog.LevelPanic
	}
	golog.SetAllLoggers(logLevel)
	if err = golog.SetLogLevel("tss-lib", level); err != nil {
		log.Fatal().Err(err).Msg("fail to set tss-lib loglevel")
	}
}
