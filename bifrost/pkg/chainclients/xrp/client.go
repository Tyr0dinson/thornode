package xrp

import (
	"context"
	"crypto/sha512"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/xrp/keymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/xrp/keymanager/secp256k1"

	"gitlab.com/thorchain/thornode/v3/bifrost/reporter"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"

	binarycodec "github.com/Peersyst/xrpl-go/binary-codec"
	"github.com/Peersyst/xrpl-go/xrpl/hash"
	"github.com/Peersyst/xrpl-go/xrpl/queries/account"
	qcommon "github.com/Peersyst/xrpl-go/xrpl/queries/common"
	requests "github.com/Peersyst/xrpl-go/xrpl/queries/transactions"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	transactions "github.com/Peersyst/xrpl-go/xrpl/transaction"
	txtypes "github.com/Peersyst/xrpl-go/xrpl/transaction/types"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// Client is a structure to sign and broadcast tx to XRP chain used by signer mostly
type Client struct {
	logger              zerolog.Logger
	cfg                 config.BifrostChainConfiguration
	tssKeyManager       *tss.KeySign
	localKeyManager     *keymanager.KeyManager
	thorchainBridge     thorclient.ThorchainBridge
	storage             *blockscanner.BlockScannerStorage
	blockScanner        *blockscanner.BlockScanner
	signerCacheManager  *signercache.CacheManager
	xrpScanner          *XrpBlockScanner
	globalSolvencyQueue chan stypes.Solvency
	// Serializes ReportSolvency when ordered scan callbacks and SolvencyCheckRunner can both enter.
	solvencyLock sync.Mutex
	wg           *sync.WaitGroup
	stopchan     chan struct{}
	rpcClient    *rpc.Client
	networkID    uint32

	// vaultLocks serializes one node's SignTx+BroadcastTx per vault (the signer holds the
	// lock GetVaultLock returns across the whole sign+broadcast lifecycle — see
	// chainVaultLock in bifrost/signer/sign.go), so a vault's outbounds are processed one
	// at a time within this node rather than concurrently. It does NOT by itself keep two
	// outbounds off the same account sequence — the lock releases when an item finishes
	// and across nodes there is no shared lock. Closing the VISIBLE in-flight reuse window
	// is the serial-drain gate's job (signingMetadata defers while the open ledger is ahead
	// of the validated one); a tx still mid-keysign is in no ledger yet, so neither the lock
	// nor the gate closes that window (the accepted stale-pin case). The lock just avoids
	// intra-node races on the sequence read.
	vaultLocksMu sync.Mutex
	vaultLocks   map[string]*sync.Mutex
}

// NewClient creates a new instance of an XRP-based chain client
func NewClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	thorchainBridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
) (*Client, error) {
	logger := log.With().Str("module", cfg.ChainID.String()).Logger()

	tssKm, err := tss.NewKeySign(server, thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("fail to create tss signer: %w", err)
	}

	priv, err := thorKeys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	if thorchainBridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}

	localKm, err := keymanager.NewKeyManager(priv)
	if err != nil {
		return nil, fmt.Errorf("fail to create key manager: %w", err)
	}

	networkID := uint64(1)
	if cfg.ChainNetwork != "" {
		networkID, err = strconv.ParseUint(cfg.ChainNetwork, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("error parsing chain network as uint, %w", err)
		}
	}

	// Override the library's default HTTP client (a hard 1s timeout, no
	// transport retries) with the chain's configured block-scanner timeout and
	// transient-error retry policy — see retryHTTPClient.
	rpcConfig, err := rpc.NewClientConfig(cfg.RPCHost,
		rpc.WithHTTPClient(newRetryHTTPClient(cfg.BlockScanner.HTTPRequestTimeout, cfg.BlockScanner.MaxHTTPRequestRetry, logger)))
	if err != nil {
		return nil, fmt.Errorf("unable to create rpc config for client, %w", err)
	}
	rpcClient := rpc.NewClient(rpcConfig)

	c := &Client{
		logger:          logger,
		cfg:             cfg,
		tssKeyManager:   tssKm,
		localKeyManager: localKm,
		thorchainBridge: thorchainBridge,
		wg:              &sync.WaitGroup{},
		stopchan:        make(chan struct{}),
		rpcClient:       rpcClient,
		networkID:       uint32(networkID),
		vaultLocks:      make(map[string]*sync.Mutex),
	}

	var path string // if not set later, will in memory storage
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}
	c.storage, err = blockscanner.NewBlockScannerStorage(path, c.cfg.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scan storage: %w", err)
	}

	c.xrpScanner, err = NewXrpBlockScanner(
		c.cfg.RPCHost,
		c.cfg.BlockScanner,
		c.storage,
		c.thorchainBridge,
		m,
		c.ReportSolvency,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cosmos scanner: %w", err)
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(c.cfg.BlockScanner, c.storage, m, c.thorchainBridge, c.xrpScanner)
	if err != nil {
		return nil, fmt.Errorf("failed to create block scanner: %w", err)
	}

	signerCacheManager, err := signercache.NewSignerCacheManager(c.storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache manager")
	}
	c.signerCacheManager = signerCacheManager

	return c, nil
}

// Start Xrp chain client
func (c *Client) Start(globalTxsQueue chan stypes.TxIn, globalErrataQueue chan stypes.ErrataBlock, globalNetworkFeeQueue chan common.NetworkFee, reporterDispatcher reporter.Dispatcher) {
	c.xrpScanner.globalNetworkFeeQueue = globalNetworkFeeQueue
	c.tssKeyManager.Start()
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue, reporterDispatcher)
}

// Stop Xrp chain client
func (c *Client) Stop() {
	c.tssKeyManager.Stop()
	c.blockScanner.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

// GetConfig return the configuration used by Xrp chain client
func (c *Client) GetConfig() config.BifrostChainConfiguration {
	return c.cfg
}

func (c *Client) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

func (c *Client) GetChain() common.Chain {
	return c.cfg.ChainID
}

func (c *Client) GetHeight() (int64, error) {
	return c.xrpScanner.GetHeight()
}

// GetBlockScannerHeight returns blockscanner height
func (c *Client) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

// RollbackBlockScanner rolls back the block scanner to the last observed block
func (c *Client) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(stypes.InboundCacheKey(vault, c.GetChain().String()))
	if err != nil {
		return "", "", err
	}
	lastBroadCasted, err := c.signerCacheManager.GetLatestRecordedTx(stypes.BroadcastCacheKey(vault, c.GetChain().String()))
	return lastObserved, lastBroadCasted, err
}

// GetAddress return current signer address, it will be bech32 encoded address
func (c *Client) GetAddress(poolPubKey common.PubKey) string {
	addr, err := poolPubKey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get pool address")
		return ""
	}
	return addr.String()
}

func (c *Client) GetAccount(pkey common.PubKey, height *big.Int) (common.Account, error) {
	addr, err := pkey.GetAddress(c.GetChain())
	if err != nil {
		return common.Account{}, fmt.Errorf("failed to convert address (%s) from bech32: %w", pkey, err)
	}
	return c.GetAccountByAddress(addr.String(), height)
}

func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	aiReq := account.InfoRequest{
		Account:     txtypes.Address(address),
		LedgerIndex: qcommon.Current, // Query current/non-closed/non-validated ledger
	}
	if height != nil && height.Sign() > 0 {
		aiReq.LedgerIndex = qcommon.LedgerIndex(height.Int64())
	}
	return c.accountInfo(aiReq)
}

// accountInfo runs an account_info request and converts the response to a
// common.Account. Shared by the current-ledger (GetAccountByAddress) and
// validated-ledger (getValidatedAccount) readers so the balance/coins/sequence
// conversion lives in one place.
func (c *Client) accountInfo(aiReq account.InfoRequest) (common.Account, error) {
	aiResp, err := c.rpcClient.GetAccountInfo(&aiReq)
	if err != nil {
		return common.Account{}, err
	}
	balance := sdkmath.NewUint(aiResp.AccountData.Balance.Uint64())
	coins, err := fromXrpToThorchain(txtypes.XRPCurrencyAmount(balance.Uint64()))
	if err != nil {
		return common.Account{}, err
	}
	return common.Account{
		Sequence:      int64(aiResp.AccountData.Sequence),
		AccountNumber: 0,
		Coins:         common.NewCoins(coins),
	}, nil
}

// getValidatedAccount reads the account from the VALIDATED ledger. Signing must use
// this, never the current/open ledger (GetAccountByAddress's default): the open
// ledger includes tentatively-applied, not-yet-validated txs and advances before
// consensus, so it differs between nodes. Reading the validated ledger removes that
// per-node source of divergence. NOTE: "validated" is each node's LATEST validated
// ledger, so nodes straddling a validation boundary can still briefly differ — full
// cross-node determinism needs a consensus-agreed validated snapshot, tracked as a
// develop follow-up; this is the safe patch-level step.
func (c *Client) getValidatedAccount(pkey common.PubKey) (common.Account, error) {
	addr, err := pkey.GetAddress(c.GetChain())
	if err != nil {
		return common.Account{}, fmt.Errorf("failed to convert address (%s) from bech32: %w", pkey, err)
	}
	return c.accountInfo(account.InfoRequest{
		Account:     txtypes.Address(addr.String()),
		LedgerIndex: qcommon.Validated,
	})
}

func (c *Client) processOutboundTx(tx stypes.TxOutItem) (*transactions.Payment, error) {
	fromAddr, err := tx.VaultPubKey.GetAddress(c.GetChain())
	if err != nil {
		return nil, fmt.Errorf("failed to convert address (%s) to bech32: %w", tx.VaultPubKey.String(), err)
	}

	if len(tx.Coins) != 1 {
		return nil, fmt.Errorf("cannot send more than 1 set of coins, trying %d set coins", len(tx.Coins))
	}

	signingPubKey, err := tx.VaultPubKey.Secp256K1()
	if err != nil {
		return nil, err
	}

	coin, err := fromThorchainToXrp(tx.Coins[0])
	if err != nil {
		return nil, err
	}

	payment := transactions.Payment{
		BaseTx: transactions.BaseTx{
			Account:       txtypes.Address(fromAddr),
			SigningPubKey: hex.EncodeToString(signingPubKey.SerializeCompressed()),
		},
		Amount:      coin,
		Destination: txtypes.Address(tx.ToAddress.String()),
	}

	// Network id is required when > 1024 (i.e. mocknet/standalone) and must not be included for mainnet/testnet
	if c.networkID > 1024 {
		payment.BaseTx.NetworkID = c.networkID
	}

	return &payment, nil
}

// verifyBroadcast is the signer-cache verifyFn: it reports whether the recorded
// broadcast sig is on chain, via the rippled `tx` query (bounded by a timeout).
// A validated tx is present; txnNotFound with searched_all is definitively absent
// (txnNotFound without searched_all — a pruned online_delete node — is ambiguous);
// any RPC/parse error or in-flight response is ambiguous (fail-closed).
func (c *Client) verifyBroadcast(sig string) signercache.LookupResult {
	// Wrap the lookup in an explicit timeout to bound the signer goroutine.
	type lookupRes struct {
		res rpc.XRPLResponse
		err error
	}
	resultCh := make(chan lookupRes, 1)
	go func() {
		res, rpcErr := c.rpcClient.Request(&requests.TxRequest{Transaction: sig})
		resultCh <- lookupRes{res: res, err: rpcErr}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res rpc.XRPLResponse
	var rpcErr error
	select {
	case r := <-resultCh:
		res, rpcErr = r.res, r.err
	case <-ctx.Done():
		rpcErr = ctx.Err()
	}
	// The xrpl-go client surfaces a rippled result-level error (e.g. txnNotFound for
	// a missing tx) as a non-nil rpcErr with a NIL response — so we cannot recover
	// searched_all to distinguish a genuinely-absent tx from a pruned (online_delete)
	// node. Treating txnNotFound as definitive absence would risk clearing the cache
	// and re-signing a tx that a pruned node simply couldn't find (double-spend), so
	// any error is fail-closed ambiguous. XRP recovers a dropped broadcast via a
	// manual/operator cache clear rather than this verify path.
	if rpcErr != nil || res == nil {
		return signercache.LookupAmbiguous
	}
	var parsed struct {
		Validated bool `json:"validated"`
	}
	if parseErr := res.GetResult(&parsed); parseErr != nil {
		return signercache.LookupAmbiguous
	}
	if parsed.Validated {
		// rippled reports the tx in a validated (closed) ledger — the recorded
		// broadcast is definitively on chain, so respect the cache and record it as
		// present (PhaseVerifiedFound) rather than merely ambiguous.
		return signercache.LookupFound
	}
	// Non-validated (in flight) or unrecognized response — respect cache.
	return signercache.LookupAmbiguous
}

// signingMetadata resolves the XRP account sequence (and block height) to sign an
// outbound with.
//
// A checkpoint from a prior attempt pins the sequence, and the pin is NEVER moved:
// re-signing the exact same transaction can never double-spend. Lowering a pinned
// sequence is unsafe — an above-validated pin can carry a signature a peer sub-party
// already combined (this node's keysign failing does not prove the party's did), and
// XRP outbounds currently carry no LastLedgerSequence, so that floating signature
// could re-validate when the account later reaches its sequence (a second delivery).
// The backlog of divergent checkpoints is cleared out-of-band, not by moving
// pins here. A pin that unmarshals to a non-positive sequence is treated as absent (a
// zero sequence is never a real signed XRP sequence, so it can carry no signature and
// nothing can double-spend) and falls through to a fresh read — so a malformed
// checkpoint self-heals instead of wedging the outbound at sequence 0 forever.
//
// Without a usable checkpoint the sequence is read from the VALIDATED ledger, never
// the open/current ledger whose tentatively-applied txs differ between nodes. This
// removes the per-node divergence behind the telCAN_NOT_QUEUE backlog.
//
// A serial-drain gate then reads the CURRENT (open) ledger from the same rippled: if its
// sequence is ahead of the validated one, this vault already has an applied-but-unvalidated
// outbound, so signing now would reuse that sequence — the gate returns ErrTxPending (no
// signature, no checkpoint) and the item re-reads next round. The signed sequence still
// comes only from the validated ledger. The gate holds no node-local state and cannot
// wedge: if the in-flight tx is dropped, the open ledger falls back to the validated
// sequence and the wait ends.
//
// This is still not a full determinism guarantee: "validated" is each node's latest
// validated ledger (nodes straddling a validation boundary can briefly differ), and
// XRP outbounds carry no LastLedgerSequence. Full cross-node determinism + pipelining
// need a consensus-agreed validated snapshot + a LastLedgerSequence + a bounded re-sync,
// tracked for develop as a follow-up.
func (c *Client) signingMetadata(tx stypes.TxOutItem, currentHeight int64) (XrpMetadata, error) {
	if tx.Checkpoint != nil {
		meta := XrpMetadata{}
		if err := json.Unmarshal(tx.Checkpoint, &meta); err != nil {
			c.logger.Err(err).Msg("fail to unmarshal checkpoint")
			return XrpMetadata{}, err
		}
		if meta.SeqNumber > 0 {
			return meta, nil
		}
		// Malformed/empty checkpoint (non-positive sequence): it can carry no valid
		// signature, so ignore it and read the validated ledger rather than pin 0.
		c.logger.Error().RawJSON("checkpoint", tx.Checkpoint).
			Msg("ignoring xrp checkpoint with non-positive sequence; reading validated ledger")
	}

	acc, err := c.getValidatedAccount(tx.VaultPubKey)
	if err != nil {
		return XrpMetadata{}, fmt.Errorf("fail to get validated account info: %w", err)
	}
	// Serial-drain gate: read the current (open) ledger from the same rippled. If either
	// read fails we return the error (never sign on a half-read). A current sequence ahead
	// of the validated one means a prior outbound from this vault is applied but not yet
	// validated; defer with ErrTxPending (no signature, no checkpoint) so the item re-reads
	// next round once that tx validates — or, if it is dropped, once the open ledger falls
	// back. A loser that slips through the validation-boundary race (a node whose open
	// ledger had not yet seen the prior tx) blocks only its own vault's queue until the item
	// goes stale (SigningTransactionPeriod less the reschedule buffer) and THORChain
	// reschedules it; its broadcast fails tefPAST_SEQ meanwhile and is never cached as sent.
	cur, err := c.GetAccount(tx.VaultPubKey, nil)
	if err != nil {
		return XrpMetadata{}, fmt.Errorf("fail to get current account info: %w", err)
	}
	if cur.Sequence > acc.Sequence {
		c.logger.Info().
			Str("vault", tx.VaultPubKey.String()).
			Int64("validated_seq", acc.Sequence).
			Int64("current_seq", cur.Sequence).
			Msg("xrp serial drain: prior outbound applied but not yet validated; deferring new sign")
		return XrpMetadata{}, chainclienttypes.ErrTxPending
	}
	return XrpMetadata{
		SeqNumber:   acc.Sequence,
		BlockHeight: currentHeight,
	}, nil
}

// SignTx signs the given TxArrayItem.
func (c *Client) SignTx(tx stypes.TxOutItem, thorchainHeight int64) (signedTx, checkpoint []byte, _ *stypes.TxInItem, err error) {
	defer func() {
		if err != nil {
			// The serial-drain gate defers a new outbound with ErrTxPending before any
			// keysign: it is normal back-pressure, not a signing failure, so don't log it
			// as "failed to sign tx" or post a keysign-failure to THORChain.
			if errors.Is(err, chainclienttypes.ErrTxPending) {
				return
			}
			var keysignError tss.KeysignError
			if errors.As(err, &keysignError) {
				if len(keysignError.Blame.BlameNodes) == 0 {
					c.logger.Err(err).Msg("TSS doesn't know which node to blame")
					return
				}

				// key sign error forward the keysign blame to thorchain
				var txID common.TxID
				txID, err = c.thorchainBridge.PostKeysignFailure(keysignError.Blame, thorchainHeight, tx.Memo, tx.Coins, tx.VaultPubKey)
				if err != nil {
					c.logger.Err(err).Msg("fail to post keysign failure to THORChain")
					return
				}
				c.logger.Info().Str("tx_id", txID.String()).Msgf("post keysign failure to thorchain")
			}
			c.logger.Err(err).Msg("failed to sign tx")
			return
		}
	}()

	mimirKey := fmt.Sprintf(constants.MimirTemplateSkipSignerCache, c.cfg.ChainID)
	gateFn := func() bool {
		skipCache, mimirErr := c.thorchainBridge.GetMimir(mimirKey)
		return mimirErr == nil && skipCache > 0
	}
	check := c.signerCacheManager.SignTxCacheCheck(tx.CacheHash(), gateFn, c.verifyBroadcast)
	if check.Phase != signercache.PhaseNoEntry {
		ev := c.logger.Info()
		if check.Noteworthy() {
			ev = c.logger.Warn()
		}
		ev.Stringer("phase", check.Phase).
			Str("sig", check.Sig).
			Interface("tx", tx).
			Msg(check.Phase.String())
	}
	if check.SkipSign {
		return nil, nil, nil, nil
	}

	msg, err := c.processOutboundTx(tx)
	if err != nil {
		c.logger.Err(err).Msg("failed to process outbound tx")
		return nil, nil, nil, err
	}

	currentHeight, err := c.xrpScanner.GetHeight()
	if err != nil {
		c.logger.Err(err).Msg("fail to get current block height")
		return nil, nil, nil, err
	}

	meta, err := c.signingMetadata(tx, currentHeight)
	if err != nil {
		return nil, nil, nil, err
	}

	// serialize the checkpoint for later
	checkpointBytes, err := json.Marshal(meta)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to marshal checkpoint: %w", err)
	}

	// gas rate from gas rate units to THORChain decimals
	gasCoin := c.cfg.ChainID.NativeGasToThorCoin(cosmos.NewUint(uint64(tx.GasRate)))
	if gasCoin.Amount.IsZero() {
		gasCoin.Amount = cosmos.OneUint()
	}

	// TODO: Optionally
	// (to avoid overpaying for already-queued TxOutItems when changing units),
	// rather than assuming Transaction Size 1 here,
	// use the tx.MaxGas instead like GAIA?

	feeCurrency, err := fromThorchainToXrp(gasCoin)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to get fee: %w", err)
	}

	fee, ok := feeCurrency.(txtypes.XRPCurrencyAmount)
	if !ok {
		return nil, nil, nil, fmt.Errorf("fail to cast fee to xrp currency amount")
	}

	msg.Sequence = uint32(meta.SeqNumber)
	msg.Fee = fee
	// For memoless outbounds, skip adding memo to the transaction
	if tx.Memo != "" {
		msg.BaseTx.Memos = []txtypes.MemoWrapper{
			{
				Memo: txtypes.Memo{
					MemoData: hex.EncodeToString([]byte(tx.Memo)),
				},
			},
		}
	}

	txBytes, err := c.signMsg(msg, tx.VaultPubKey, thorchainHeight)
	if err != nil {
		if errors.Is(err, tss.ErrNotSelectedForKeysign) {
			return nil, nil, nil, nil
		}
		// A keysign failing on a PINNED checkpoint is worth surfacing: the pin is never
		// moved (a peer sub-party may already hold a combined signature at that sequence),
		// so a split keysign re-signs the same pinned sequence every retry and the item
		// stalls its vault's queue until it goes stale / THORChain reschedules it. The
		// pinned sequence/height are not visible otherwise (TxOutItem.Checkpoint is
		// json:"-"), so log them to make a stalled vault visible. Only log for a GENUINE
		// pin: a malformed checkpoint self-heals to a fresh validated read (meta is not the
		// pin then), so re-read the checkpoint and log its own sequence, not meta's.
		if tx.Checkpoint != nil {
			var pin XrpMetadata
			if json.Unmarshal(tx.Checkpoint, &pin) == nil && pin.SeqNumber > 0 {
				c.logger.Warn().
					Str("vault", tx.VaultPubKey.String()).
					Int64("pinned_seq", pin.SeqNumber).
					Int64("pinned_block_height", pin.BlockHeight).
					Err(err).
					Msg("keysign failed on pinned xrp checkpoint")
			}
		}
		return nil, checkpointBytes, nil, fmt.Errorf("failed to sign message: %w", err)
	}

	// create instant observation
	fromAddr, err := tx.VaultPubKey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).Msg("error retrieving from address for instant observation")
		return txBytes, nil, nil, nil
	}
	blobHex := hex.EncodeToString(txBytes)
	txID, err := hash.SignTxBlob(blobHex)
	if err != nil {
		c.logger.Err(err).Msg("error retrieving txid for instant observation")
		return txBytes, nil, nil, nil
	}
	txIn := stypes.NewTxInItem(
		currentHeight,
		txID,
		tx.Memo,
		fromAddr.String(),
		tx.ToAddress.String(),
		tx.Coins,
		common.Gas([]common.Coin{gasCoin}),
		tx.VaultPubKey,
		"",
		"",
		nil,
	)

	return txBytes, nil, txIn, nil
}

// signMsg takes a payment msg and signs it using either private key or TSS.
func (c *Client) signMsg(
	payment *transactions.Payment,
	pubkey common.PubKey,
	thorchainHeight int64,
) ([]byte, error) {
	xrpPubKey, err := pubkey.Secp256K1()
	if err != nil {
		return nil, err
	}

	flatTx := payment.Flatten()
	encodedTx, err := binarycodec.EncodeForSigning(flatTx)
	if err != nil {
		return nil, err
	}

	signBytes, err := hex.DecodeString(encodedTx)
	if err != nil {
		return nil, err
	}

	var derSignature []byte
	if c.localKeyManager.PublicKeyHex == hex.EncodeToString(xrpPubKey.SerializeCompressed()) {
		derSignature, err = c.localKeyManager.Sign(signBytes)
		if err != nil {
			return nil, fmt.Errorf("unable to sign using localKeyManager: %w", err)
		}
	} else {
		hashedMsg := sha512.Sum512(signBytes)
		var signature []byte
		signature, _, err = c.tssKeyManager.RemoteSign(hashedMsg[:32], common.SigningAlgoSecp256k1, pubkey.String(), thorchainHeight)
		if err != nil {
			c.logger.Err(err).Msg("xrp remote sign")
			return nil, fmt.Errorf("error, xrp remote sign: %w", err)
		}
		if signature == nil {
			return nil, tss.ErrNotSelectedForKeysign
		}
		if len(signature) < 64 {
			c.logger.Error().Msg("xrp remote sign, signature is <64 bytes")
			return nil, fmt.Errorf("error, xrp remote sign, signature is <64 bytes")
		}
		// Extract R and S from the signature
		// The signature is in the format R || S || V where V is the recovery ID
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:64])

		// Create an ECDSASignature struct for ASN.1 DER encoding
		sig := secp256k1.ECDSASignature{
			R: r,
			S: s,
		}

		// Encode the signature in DER format
		derSignature, err = asn1.Marshal(sig)
		if err != nil {
			return nil, fmt.Errorf("failed to DER encode signature: %w", err)
		}
	}
	flatTx["TxnSignature"] = hex.EncodeToString(derSignature) // use flatTx so we don't need to call autofill again

	// Ensure the signature is valid
	if !verifySignature(signBytes, derSignature, xrpPubKey.SerializeCompressed()) {
		return nil, fmt.Errorf("unable to verify signature with secpPubKey")
	}

	txHex, err := binarycodec.Encode(flatTx)
	if err != nil {
		return nil, err
	}

	txBytes, err := hex.DecodeString(txHex)
	if err != nil {
		return nil, err
	}

	return txBytes, nil
}

func verifySignature(signBytes, signature, compressedPubKey []byte) bool {
	// Hash the transaction data
	messageHashFull := sha512.Sum512(signBytes)
	messageHash := messageHashFull[:32]

	// Parse the DER signature
	var sig secp256k1.ECDSASignature
	_, err := asn1.Unmarshal(signature, &sig)
	if err != nil {
		return false
	}

	// Prepare signature in the format expected by VerifySignature
	// Ensure R and S are padded to 32 bytes
	rBytes := secp256k1.PaddedBytes(sig.R, 32)
	sBytes := secp256k1.PaddedBytes(sig.S, 32)

	// Verify the signature
	return ethcrypto.VerifySignature(
		compressedPubKey,
		messageHash,
		append(rBytes, sBytes...),
	)
}

// broadcastTx submits the blob and returns the engine result (so the caller can
// distinguish the ambiguous tefPAST_SEQ from a definite success). It errors only on a
// result that is neither a success nor a retryable "already seen this sequence/tx".
func (c *Client) broadcastTx(txBlob string) (engineResult string, err error) {
	response, err := c.rpcClient.Submit(txBlob, true)
	if err != nil {
		return "", fmt.Errorf("broadcast msg failed, %w", err)
	}

	// So for other scenario , like transaction already in mempool , invalid account sequence # , the transaction can be rescheduled , and retried
	// If we get tefPAST_SEQ, tx is already in current ledger, most likely by another validator.
	// tefALREADY means this exact tx was already applied, e.g. a retried submit.
	if response.EngineResult != "tesSUCCESS" && response.EngineResult != "tefPAST_SEQ" &&
		response.EngineResult != "tefALREADY" {
		c.logger.Info().Interface("broadcastRes", response).Msg("XRP BroadcastTx failed")
		return response.EngineResult, fmt.Errorf("transaction failed to submit with engine result: %s", response.EngineResult)
	}
	c.logger.Info().Interface("broadcastRes", response).Msg("XRP BroadcastTx success")
	return response.EngineResult, nil
}

// xrpTxStatus is the resolved on-chain state of a signed XRP tx, used to decide whether
// a broadcast is truly sent, provably dead (so the signer must re-sign at a fresh
// sequence), or still unknown.
type xrpTxStatus int

const (
	// xrpTxUnknown: the RPC/parse failed, or the tx is seen but not yet validated — no
	// definite answer, so retry/wait rather than acting.
	xrpTxUnknown xrpTxStatus = iota
	// xrpTxNotFound: the RPC answered and the tx is on no ledger (txnNotFound).
	xrpTxNotFound
	// xrpTxValidatedSuccess: validated in a closed ledger with a tesSUCCESS result.
	xrpTxValidatedSuccess
	// xrpTxValidatedFailed: validated with a tec* result — the sequence is burned and the
	// fee spent, but it delivered nothing, so the outbound obligation is NOT met.
	xrpTxValidatedFailed
)

// xrpTxStatusOf resolves a tx hash to one of the xrpTxStatus states. It distinguishes a
// definitive txnNotFound (the node answered: not on chain) from a transport/parse error
// (unknown), so callers can retire a provably-dead tx without acting on a flaky lookup.
func (c *Client) xrpTxStatusOf(txHash string) xrpTxStatus {
	res, err := c.rpcClient.Request(&requests.TxRequest{Transaction: txHash})
	if err != nil {
		var ce *rpc.ClientError
		if errors.As(err, &ce) && ce.ErrorString == "txnNotFound" {
			return xrpTxNotFound
		}
		// transport, timeout, or any other error result — we do not know.
		return xrpTxUnknown
	}
	var txResponse *requests.TxResponse
	if gErr := res.GetResult(&txResponse); gErr != nil || txResponse == nil {
		return xrpTxUnknown
	}
	if !txResponse.Validated {
		// seen (e.g. still in the open ledger / mempool) but not yet validated.
		return xrpTxUnknown
	}
	meta, ok := txResponse.Meta.(map[string]any)
	if !ok {
		return xrpTxUnknown
	}
	result, ok := meta["TransactionResult"].(string)
	if !ok {
		// present but missing / non-string TransactionResult (API/shape differences):
		// do NOT assume failure — an unrecognized shape must fail SAFE as unknown, never
		// drive a re-sign that could double-pay a tx that actually succeeded.
		return xrpTxUnknown
	}
	switch {
	case strings.HasPrefix(result, "tes"): // tesSUCCESS
		return xrpTxValidatedSuccess
	case strings.HasPrefix(result, "tec"): // claimed-cost failure: sequence burned, delivered nothing
		return xrpTxValidatedFailed
	default:
		// a validated tx never carries a tef/ter/tem/tel result, so an unrecognized code
		// is an unexpected response — fail safe as unknown rather than as a failure.
		return xrpTxUnknown
	}
}

// cacheSent records an outbound in the signer cache so it is not re-signed. Reached on a
// validated tesSUCCESS (confirmed delivered) or on a fresh submit-time tesSUCCESS (accepted
// optimistically, as the prior code did — THORChain reschedules it if it never validates).
func (c *Client) cacheSent(tx stypes.TxOutItem, txHash string) {
	expiry := time.Now().Add(c.getSignerCacheExpiry()).UnixMilli()
	if err := c.signerCacheManager.SetSignedWithExpiry(tx.CacheHash(), tx.CacheVault(c.GetChain()), txHash, expiry); err != nil {
		c.logger.Err(err).Msg("fail to set signer cache")
	}
}

// BroadcastTx submits the signed XRP tx (or confirms a prior submit) and reports it to
// the signer. An AMBIGUOUS accept — tefPAST_SEQ ("the sequence is already past") or
// tefALREADY ("this exact tx already seen") — is treated as sent ONLY if this exact tx
// hash is confirmed validated with tesSUCCESS; a tec* failure burns the sequence but pays
// nothing, and a bare txnNotFound is not proof of anything (a pruned node drops a tx it
// delivered). A fresh submit-time tesSUCCESS is still accepted optimistically, as the prior
// code did (THORChain reschedules it if it never validates).
//
// It returns ErrResignRequired ONLY when the tx is proven dead by a POSITIVE on-chain
// result — our exact hash validated as a tec* failure — so re-signing at the next free
// sequence can never double-pay (the tec burned the sequence and delivered nothing). It
// never re-signs on a mere absence, which would risk paying a delivered tx twice.
func (c *Client) BroadcastTx(tx stypes.TxOutItem, txBytes []byte) (string, error) {
	txBlob := hex.EncodeToString(txBytes)
	txHash, err := hash.SignTxBlob(txBlob)
	if err != nil {
		c.logger.Error().Msg("error hashing txBlob on broadcast")
		return "", err
	}

	// Has this tx already resolved on chain (a prior submit / another node)? Decide from
	// its result before re-submitting.
	switch c.xrpTxStatusOf(txHash) {
	case xrpTxValidatedSuccess:
		c.cacheSent(tx, txHash)
		return txHash, nil
	case xrpTxValidatedFailed:
		c.logger.Warn().Str("txHash", txHash).
			Msg("xrp outbound validated with a non-tesSUCCESS result; re-signing at a fresh sequence")
		return "", stypes.ErrResignRequired
	}
	// xrpTxNotFound (never submitted) or xrpTxUnknown (lookup flaked) — (re-)submit.

	engineResult, bErr := c.broadcastTx(txBlob)
	if bErr != nil {
		return "", bErr
	}

	// tefPAST_SEQ ("sequence already past") and tefALREADY ("this exact tx already seen")
	// are NOT proof of delivery. Confirm on chain before caching as sent.
	switch engineResult {
	case "tefPAST_SEQ":
		switch c.xrpTxStatusOf(txHash) {
		case xrpTxValidatedSuccess:
			// our tx is the one that consumed the sequence and it succeeded.
		case xrpTxValidatedFailed:
			// our exact tx is on chain as a tec* failure: the sequence is burned and it
			// delivered nothing, so re-signing at a fresh sequence cannot double-pay.
			c.logger.Warn().Str("txHash", txHash).
				Msg("xrp tefPAST_SEQ and this tx validated as a tec* failure; re-signing at a fresh sequence")
			return "", stypes.ErrResignRequired
		default: // xrpTxNotFound or xrpTxUnknown
			// A txnNotFound here is NOT proof that a DIFFERENT tx consumed the sequence: a
			// pruned/lagging rippled node reports txnNotFound for a tx it no longer holds —
			// even one we DELIVERED — so re-signing on this absence could double-pay. Fail
			// closed exactly as verifyBroadcast and the tefALREADY branch do: retain + re-
			// check next cycle. A sequence genuinely consumed by another tx is recovered by
			// THORChain's reschedule, never by auto-re-signing on an unconfirmable absence.
			return "", fmt.Errorf("xrp tefPAST_SEQ: cannot confirm tx %s is a validated success; retrying", txHash)
		}
	case "tefALREADY":
		// The same-hash tx is already seen; it may still validate at its sequence, so NEVER
		// retire on absence (re-signing at a new sequence while the original is pending
		// would double-pay). Only a confirmed result is actionable.
		switch c.xrpTxStatusOf(txHash) {
		case xrpTxValidatedSuccess:
			// delivered — cache as sent.
		case xrpTxValidatedFailed:
			return "", stypes.ErrResignRequired
		default: // xrpTxNotFound / xrpTxUnknown — wait and re-check, do not cache.
			return "", fmt.Errorf("xrp tefALREADY: cannot confirm tx %s on chain yet; retrying", txHash)
		}
	}
	// tesSUCCESS (fresh optimistic accept), or a tefPAST_SEQ/tefALREADY confirmed as a
	// validated success above.
	c.cacheSent(tx, txHash)
	return txHash, nil
}

// GetVaultLock returns the per-vault mutex the signer holds across SignTx+BroadcastTx,
// implementing the vaultLocker interface in bifrost/signer/sign.go. It serializes a
// single node's same-vault outbounds so they are processed one at a time (no intra-node
// race on the sequence read); it does NOT by itself keep two outbounds off the same
// account sequence — that is the serial-drain gate's job (see signingMetadata). The map
// holds one small mutex per vault pubkey and is not pruned; vault cardinality is bounded
// by churn (~daily), so the growth is negligible over process lifetime.
func (c *Client) GetVaultLock(vaultPubKey string) *sync.Mutex {
	c.vaultLocksMu.Lock()
	defer c.vaultLocksMu.Unlock()
	lock, ok := c.vaultLocks[vaultPubKey]
	if !ok {
		lock = &sync.Mutex{}
		c.vaultLocks[vaultPubKey] = lock
	}
	return lock
}

// ConfirmationCountReady xrp chain has almost instant finality, so doesn't need to wait for confirmation
func (c *Client) ConfirmationCountReady(txIn stypes.TxIn) bool {
	return true
}

// GetConfirmationCount determine how many confirmations are required
// NOTE: Xrp chains are instant finality, so confirmations are not needed.
// If the transaction was successful, we know it is included in a block and thus immutable.
func (c *Client) GetConfirmationCount(txIn stypes.TxIn) int64 {
	return 0
}

// getSignerCacheExpiry returns the configured signer-cache expiry (config signer_cache_expiry).
func (c *Client) getSignerCacheExpiry() time.Duration {
	return c.cfg.SignerCacheExpiry
}

// GetGasEstimateForSolvency returns the current gas fee estimate used by solvency checks.
func (c *Client) GetGasEstimateForSolvency() cosmos.Uint {
	return c.cfg.ChainID.NativeGasToThorchain(c.xrpScanner.getLastFee())
}

// ReportSolvency is the legacy solvency flow retained for reviewer comparison.
//nolint:unused
func (c *Client) ReportSolvency(blockHeight int64) error {
	// The ordered scan loop and SolvencyCheckRunner can both call this.
	c.solvencyLock.Lock()
	defer c.solvencyLock.Unlock()

	if !c.ShouldReportSolvency(blockHeight) {
		return nil
	}

	// when block scanner is not healthy, only report from auto-unhalt SolvencyCheckRunner
	// (the scan hook passes PreviousHeight + 1, while SolvencyCheckRunner passes chainHeight)
	if !c.IsBlockScannerHealthy() && blockHeight == c.blockScanner.PreviousHeight()+1 {
		return nil
	}

	// fetch all asgard vaults
	asgardVaults, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards,err: %w", err)
	}

	// 1x estimated gas cost breathing room (transaction size 1), from gas rate units gas price to THORChain (1e8) format
	currentGasFee := c.GetGasEstimateForSolvency()

	// report insolvent asgard vaults,
	// or else all if the chain is halted and all are solvent
	chainVaults := asgardVaults[:0]
	for _, asgard := range asgardVaults {
		if asgard.HasFundsForChain(c.cfg.ChainID) {
			chainVaults = append(chainVaults, asgard)
		}
	}

	msgs := make([]stypes.Solvency, 0, len(chainVaults))
	solventMsgs := make([]stypes.Solvency, 0, len(chainVaults))
	processedVaults := 0
	for i := range chainVaults {
		var acct common.Account
		acct, err = c.GetAccount(chainVaults[i].PubKey, new(big.Int).SetInt64(blockHeight))
		if err != nil {
			c.logger.Err(err).Msgf("fail to get account balance")
			continue
		}

		msg := stypes.Solvency{
			Height: blockHeight,
			Chain:  c.cfg.ChainID,
			PubKey: chainVaults[i].PubKey,
			Coins:  acct.Coins,
		}
		processedVaults++

		if runners.IsVaultSolvent(c.cfg.ChainID, acct, chainVaults[i], currentGasFee) {
			solventMsgs = append(solventMsgs, msg) // Solvent-vault message
			continue
		}
		msgs = append(msgs, msg) // Insolvent-vault message
	}

	// Only if THORChain itself has this chain halted or solvency-halted, this node's block scanner
	// is paused for it (unhealthy), and all chain-specific vaults are solvent, report that all the
	// chain-specific vaults are solvent. That "all clear" exists to lift a network SolvencyHalt
	// (see SolvencyCheckRunner), so the network's halt state is the gate, not this node's own
	// block-scanner health: a scanner that is unhealthy for a local reason (RPC trouble, a stalled
	// fetch) must stay silent here, because an all-clear only one node sends can never reach
	// consensus and each one costs that node an unrefunded observation slash point. The paused
	// scanner is still required alongside the halt: the scanner re-reads the pause mimirs only once
	// per THORChain block, and reads a mimir error as not paused, so just after a halt takes effect
	// a healthy scanner can still reach here from the scan path. Its all-clear would carry the
	// scanned height rather than the runner's, a different voter, and so would be a lone vote too.
	// On a bridge error stay silent; the runner retries.
	// If there are any insolvent vaults, report only them.
	// Not reporting both solvent and insolvent vaults is to avoid noise (spam):
	// Reporting both could halt-and-unhalt SolvencyHalt in the same THOR block
	// (resetting its height), plus making it harder to know at a glance from solvency reports which vaults were insolvent.
	solvent := false
	allVaultsProcessed := len(chainVaults) > 0 && processedVaults == len(chainVaults)
	halted, haltErr := runners.IsHaltedOnChain(c.thorchainBridge, c.cfg.ChainID)
	if haltErr != nil {
		c.logger.Err(haltErr).Msg("fail to read chain halt state, skipping solvency all-clear")
	}
	if halted && !c.IsBlockScannerHealthy() && allVaultsProcessed && len(solventMsgs) == processedVaults {
		msgs = solventMsgs
		solvent = true
	}

	for i := range msgs {
		c.logger.Info().
			Stringer("asgard", msgs[i].PubKey).
			Interface("coins", msgs[i].Coins).
			Bool("solvent", solvent).
			Msg("reporting solvency")

		// send solvency to thorchain via global queue consumed by the observer
		select {
		case c.globalSolvencyQueue <- msgs[i]:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msgf("fail to send solvency info to THORChain, timeout")
		}
	}
	return nil
}

// ShouldReportSolvency is the legacy solvency flow retained for reviewer comparison.
//nolint:unused
func (c *Client) ShouldReportSolvency(height int64) bool {
	// Block time on XRP generally hovers between 3-5 seconds (15
	// blocks/min). Since the last fee is used as a buffer we also want to ensure that is
	// non-zero (enough blocks have been seen) before checking insolvency to avoid false
	// positives.
	return height%c.cfg.SolvencyBlocks == 0 && !c.xrpScanner.getLastFee().IsZero()
}

// OnObservedTxIn update the signer cache (in case we haven't already)
func (c *Client) OnObservedTxIn(txIn stypes.TxInItem, blockHeight int64) {
	m, err := memo.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// Debug log only as ParseMemo error is expected for THORName inbounds.
		c.logger.Debug().Err(err).Msgf("fail to parse memo: %s", txIn.Memo)
		return
	}
	if !m.IsOutbound() {
		return
	}
	if m.GetTxID().IsEmpty() {
		return
	}
	if err = c.signerCacheManager.SetSigned(txIn.CacheHash(c.GetChain(), m.GetTxID().String()), txIn.CacheVault(c.GetChain()), txIn.Tx); err != nil {
		c.logger.Err(err).Msg("fail to update signer cache")
	}
}
