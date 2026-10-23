package tron

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	_ "embed"

	"cosmossdk.io/math"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	tcmetrics "gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tctss "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	tcconfig "gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

const (
	// TimestampValidity is the expiration duration for an outbound transaction. We use
	// the thornode block time of the outbound height as the timestamp to preserve
	// determinism, so the 20 minute expiration means the built transaction will be valid
	// to broadcast for the first 20 minutes of the 30 minute signing period.
	TimestampValidity = 20 * time.Minute

	ConfirmationBlocks int64 = 1
	FinalityBlocks     int64 = 19

	tronSignCheckpointVersion = 1
	maxTronCheckpointSize     = 256 * 1024
)

// tronSignCheckpoint pins the exact unsigned transaction after all
// deterministic TRON fields have been applied. A terminal TSS failure may
// expose a usable signature to another participant, so retries must never
// reconstruct or mutate the transaction being signed.
type tronSignCheckpoint struct {
	Version     int             `json:"version"`
	TxOutDigest string          `json:"tx_out_digest"`
	UnsignedTx  api.Transaction `json:"unsigned_tx"`
	Checksum    string          `json:"checksum"`
}

func tronJSONDigest(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func validateUnsignedTronTransaction(tx api.Transaction) error {
	if len(tx.Signature) != 0 || tx.Err != "" || len(tx.Ret) != 0 {
		return fmt.Errorf("TRON checkpoint transaction is not unsigned")
	}
	if strings.TrimSpace(tx.TxId) == "" || strings.TrimSpace(tx.RawDataHex) == "" {
		return fmt.Errorf("TRON checkpoint transaction is incomplete")
	}
	rawData, err := hex.DecodeString(tx.RawDataHex)
	if err != nil {
		return fmt.Errorf("fail to decode TRON checkpoint raw_data_hex: %w", err)
	}
	wantTxID := sha256.Sum256(rawData)
	if !strings.EqualFold(tx.TxId, hex.EncodeToString(wantTxID[:])) {
		return fmt.Errorf("TRON checkpoint txid does not match raw_data_hex")
	}

	// Rehash only a copy. Equality proves the structured deterministic fields
	// used by Rehash still describe the exact persisted signing preimage.
	validated := tx
	if err := validated.Rehash(); err != nil {
		return fmt.Errorf("fail to validate TRON checkpoint transaction: %w", err)
	}
	if !strings.EqualFold(validated.TxId, tx.TxId) ||
		!strings.EqualFold(validated.RawDataHex, tx.RawDataHex) {
		return fmt.Errorf("TRON checkpoint transaction fields do not match raw_data_hex")
	}
	return nil
}

func encodeTronSignCheckpoint(tx api.Transaction, txOutItem types.TxOutItem) ([]byte, error) {
	if err := validateUnsignedTronTransaction(tx); err != nil {
		return nil, err
	}
	txOutDigest, err := tronJSONDigest(txOutItem)
	if err != nil {
		return nil, fmt.Errorf("fail to bind TRON checkpoint to txout: %w", err)
	}
	checksum, err := tronJSONDigest(tx)
	if err != nil {
		return nil, fmt.Errorf("fail to checksum TRON checkpoint transaction: %w", err)
	}
	checkpoint := tronSignCheckpoint{
		Version:     tronSignCheckpointVersion,
		TxOutDigest: txOutDigest,
		UnsignedTx:  tx,
		Checksum:    checksum,
	}
	b, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, fmt.Errorf("fail to encode TRON sign checkpoint: %w", err)
	}
	if len(b) > maxTronCheckpointSize {
		return nil, fmt.Errorf("TRON sign checkpoint exceeds %d bytes", maxTronCheckpointSize)
	}
	return b, nil
}

func decodeTronSignCheckpoint(b []byte, txOutItem types.TxOutItem) (api.Transaction, error) {
	if len(b) == 0 || len(b) > maxTronCheckpointSize {
		return api.Transaction{}, fmt.Errorf("invalid TRON sign checkpoint size: %d", len(b))
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	var checkpoint tronSignCheckpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return api.Transaction{}, fmt.Errorf("fail to decode TRON sign checkpoint: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return api.Transaction{}, fmt.Errorf("TRON sign checkpoint contains trailing data")
	}
	if checkpoint.Version != tronSignCheckpointVersion {
		return api.Transaction{}, fmt.Errorf("unsupported TRON sign checkpoint version: %d", checkpoint.Version)
	}
	txOutDigest, err := tronJSONDigest(txOutItem)
	if err != nil {
		return api.Transaction{}, fmt.Errorf("fail to validate TRON checkpoint txout: %w", err)
	}
	if checkpoint.TxOutDigest != txOutDigest {
		return api.Transaction{}, fmt.Errorf("TRON sign checkpoint does not match txout")
	}
	if validateErr := validateUnsignedTronTransaction(checkpoint.UnsignedTx); validateErr != nil {
		return api.Transaction{}, validateErr
	}
	checksum, err := tronJSONDigest(checkpoint.UnsignedTx)
	if err != nil {
		return api.Transaction{}, fmt.Errorf("fail to validate TRON checkpoint checksum: %w", err)
	}
	if checkpoint.Checksum != checksum {
		return api.Transaction{}, fmt.Errorf("TRON sign checkpoint checksum mismatch")
	}
	return checkpoint.UnsignedTx, nil
}

//go:embed abi/trc20.json
var trc20ContractABI []byte

type TronClient struct {
	logger             zerolog.Logger
	config             tcconfig.BifrostChainConfiguration
	chainId            string
	blockScanner       *blockscanner.BlockScanner
	storage            *blockscanner.BlockScannerStorage
	signerCacheManager *signercache.CacheManager
	tssKeyManager      *tss.KeySign
	localKeyManager    *KeyManager
	tronScanner        *TronBlockScanner
	api                *api.TronApi
	rpc                *rpc.TronRpc
	endpointMgr        *endpoints.Manager
	abi                abi.ABI

	whitelist           map[string]tokenlist.ERC20Token
	bridge              thorclient.ThorchainBridge
	globalSolvencyQueue chan types.Solvency
	// Serializes ReportSolvency when ordered scan callbacks and SolvencyCheckRunner can both enter.
	solvencyLock sync.Mutex
	wg           *sync.WaitGroup
	stopchan     chan struct{}
}

// tronIdentityDepth is how far below the api_host tip the block compared across
// api_host and rpc_host is taken: deep enough that both hosts have it, shallow
// enough for lite full nodes that keep only recent history.
const tronIdentityDepth = 20

// tronIdentity combines the rpc_host chain id with the api_host cross-check into the
// identity the endpoint manager verifies: a TRON block id equals the JSON-RPC block
// hash of the same block, so the two hosts serve the same network exactly when they
// agree on it. With an expected identity a disagreement is reported as a distinct
// identity, so the endpoint is disabled (fatal at startup for priority 1); without
// one it is an endpoints.ErrIdentityMismatch, which disables the endpoint as well,
// so a misconfigured host pair never serves, even as the only endpoint.
func tronIdentity(chainID, rpcHash, apiBlockID, expected string) (string, error) {
	norm := func(h string) string { return strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "0X")) }
	if norm(rpcHash) == norm(apiBlockID) && norm(rpcHash) != "" {
		return chainID, nil
	}
	if expected != "" {
		return fmt.Sprintf("%s (api_host block %s != rpc_host %s)", chainID, norm(apiBlockID), norm(rpcHash)), nil
	}
	return "", fmt.Errorf("%w: api_host and rpc_host disagree on a recent block (api %q, rpc %q)", endpoints.ErrIdentityMismatch, norm(apiBlockID), norm(rpcHash))
}

// tronAPIIdentity is the CompoundProbe identity hook for one TRON endpoint: given
// the verified rpc_host chain id it reads both tips, takes the block
// tronIdentityDepth below the lower one (so both hosts have it, whichever network
// is ahead), and compares its id across api_host (/wallet/getblock, header only)
// and rpc_host (eth_getBlockByNumber). Hosts on different networks are therefore
// proven to differ whenever both answer; transport failures leave the endpoint
// unverified until a later cycle.
func tronAPIIdentity(name string, apiPost func(ctx context.Context, path, body string) (map[string]json.RawMessage, error), rpcProbe endpoints.Probe, expected string, logger zerolog.Logger) func(ctx context.Context, chainID string) (string, error) {
	hp, ok := rpcProbe.(endpoints.HashProbe)
	return func(ctx context.Context, chainID string) (string, error) {
		if !ok {
			return "", fmt.Errorf("endpoint %s: rpc probe cannot fetch block hashes", name)
		}
		now, err := apiPost(ctx, "/wallet/getblock", `{"detail":false}`)
		if err != nil {
			return "", fmt.Errorf("api identity: %w", err)
		}
		var hdr struct {
			BlockHeader struct {
				RawData struct {
					Number int64 `json:"number"`
				} `json:"raw_data"`
			} `json:"block_header"`
		}
		if err = json.Unmarshal(now["block_header"], &hdr.BlockHeader); err != nil || hdr.BlockHeader.RawData.Number <= 0 {
			return "", fmt.Errorf("api identity: getblock has no block number")
		}
		rpcTip, err := rpcProbe.Height(ctx)
		if err != nil {
			return "", fmt.Errorf("api identity: rpc tip: %w", err)
		}
		h := min(hdr.BlockHeader.RawData.Number, rpcTip) - tronIdentityDepth
		if h < 1 {
			h = 1
		}
		blk, err := apiPost(ctx, "/wallet/getblock", fmt.Sprintf(`{"id_or_num":"%d","detail":false}`, h))
		if err != nil {
			return "", fmt.Errorf("api identity: %w", err)
		}
		var apiID string
		if err = json.Unmarshal(blk["blockID"], &apiID); err != nil || apiID == "" {
			return "", fmt.Errorf("api identity: getblock %d has no blockID (node behind?)", h)
		}
		rpcHash, err := hp.HashAt(ctx, h)
		if err != nil {
			return "", fmt.Errorf("api identity: %w", err)
		}
		id, ierr := tronIdentity(chainID, rpcHash, apiID, expected)
		if ierr != nil {
			logger.Error().Err(ierr).Str("endpoint", name).Int64("height", h).
				Msg("endpoint api_host and rpc_host disagree on the network; disabling the endpoint")
		}
		return id, ierr
	}
}

// NewTronClient creates a new instance of a Tron chain client
func NewTronClient(
	thorKeys *thorclient.Keys,
	config tcconfig.BifrostChainConfiguration,
	server *tctss.TssServer,
	bridge thorclient.ThorchainBridge,
	metrics *tcmetrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	poolMgr thorclient.PoolManager,
) (*TronClient, error) {
	var err error

	logger := log.With().Str("module", config.ChainID.String()).Logger()

	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}

	tokens := tokenlist.GetEVMTokenList(config.ChainID).Tokens

	whitelist := map[string]tokenlist.ERC20Token{}
	for _, token := range tokens {
		for _, address := range config.BlockScanner.WhitelistTokens {
			if strings.EqualFold(address, token.Address) {
				whitelist[address] = token
			}
		}
	}

	// endpoint manager (#2828): one router for api_host (/wallet/*) and one for
	// rpc_host (JSON-RPC), both following the same active endpoint. Probes check
	// eth_blockNumber / eth_chainId on rpc_host and /wallet/getblock on api_host,
	// and identity verification proves both hosts serve the same network by
	// comparing a recent block id across them (tronAPIIdentity).
	failoverCfg := config.GetFailoverConfig()
	endpointCfgs := config.GetEndpoints()
	expectedIdentity := strings.ToLower(config.ExpectedIdentity)
	// a single legacy endpoint serves while its identity is unproven, as on develop
	// (where rpc_host only served TRC20 balance reads); a host pair proven to be on
	// different networks is disabled whatever the endpoint count (tronAPIIdentity)
	mgrOpts := []endpoints.Option{endpoints.WithMetrics(endpoints.DefaultMetrics())}
	if expectedIdentity != "" {
		mgrOpts = append(mgrOpts, endpoints.WithExpectedIdentity(expectedIdentity))
	}
	endpointMgr, err := endpoints.NewManager(config.ChainID, endpointCfgs, failoverCfg, logger, mgrOpts...)
	if err != nil {
		return nil, fmt.Errorf("fail to create endpoint manager: %w", err)
	}
	ordered := endpoints.OrderedEndpoints(endpointMgr, endpointCfgs)
	probes := make([]endpoints.Probe, len(ordered))
	for i, e := range ordered {
		rpcProbe := endpoints.NewEVMProbe(e, failoverCfg.HealthCheckTimeout)
		apiPost := endpoints.HTTPPostJSON(e, e.APIHost, failoverCfg.HealthCheckTimeout)
		probes[i] = endpoints.CompoundProbe{
			Probe: rpcProbe,
			// getblock without detail returns the latest block header only, so a
			// probe never downloads a full block (up to 2 MB) every cycle
			Extra:         endpoints.HTTPPostCheck(e, e.APIHost, "/wallet/getblock", failoverCfg.HealthCheckTimeout, "blockID"),
			ExtraIdentity: tronAPIIdentity(e.Name, apiPost, rpcProbe, expectedIdentity, logger),
		}
	}
	if err = endpointMgr.Bootstrap(context.Background(), probes); err != nil {
		return nil, fmt.Errorf("fail to bootstrap endpoints: %w", err)
	}
	apiTargets, err := endpoints.TargetsFrom(ordered, endpoints.APIHost)
	if err != nil {
		return nil, fmt.Errorf("fail to build api targets: %w", err)
	}
	rpcTargets, err := endpoints.TargetsFrom(ordered, endpoints.RPCHost)
	if err != nil {
		return nil, fmt.Errorf("fail to build rpc targets: %w", err)
	}
	timeout := config.BlockScanner.HTTPRequestTimeout
	attempt := evm.AttemptTimeout(timeout)
	apiRouter, err := endpoints.NewHTTPRouter(endpointMgr, endpoints.HTTPRouterConfig{
		Base:           endpoints.SyntheticBase(config.ChainID),
		Targets:        apiTargets,
		WritePaths:     []string{"/wallet/broadcasttransaction"},
		DialTimeout:    failoverCfg.HealthCheckTimeout,
		AttemptTimeout: attempt,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("fail to create api router: %w", err)
	}
	rpcRouter, err := endpoints.NewHTTPRouter(endpointMgr, endpoints.HTTPRouterConfig{
		Base:           endpoints.SyntheticBase(config.ChainID),
		Targets:        rpcTargets,
		DialTimeout:    failoverCfg.HealthCheckTimeout,
		AttemptTimeout: attempt,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("fail to create rpc router: %w", err)
	}
	base := endpoints.SyntheticBase(config.ChainID).String()
	apiClient := api.NewTronAPIWithClient(base, timeout, &http.Client{Transport: apiRouter, Timeout: timeout})
	rpcClient := rpc.NewTronRPCWithClient(base, timeout, &http.Client{Transport: rpcRouter, Timeout: timeout})

	client := TronClient{
		logger:      logger,
		chainId:     config.ChainID.String(),
		config:      config,
		bridge:      bridge,
		wg:          &sync.WaitGroup{},
		stopchan:    make(chan struct{}),
		whitelist:   whitelist,
		api:         apiClient,
		rpc:         rpcClient,
		endpointMgr: endpointMgr,
	}

	client.tssKeyManager, err = tss.NewKeySign(server, bridge)
	if err != nil {
		logger.Err(err).Msg("failed to create tss signer")
		return nil, err
	}

	client.localKeyManager, err = NewLocalKeyManager(thorKeys)
	if err != nil {
		logger.Err(err).Msg("failed to create local key manager")
		return nil, err
	}

	var path string // if not set later, will in memory storage
	if len(client.config.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf(
			"%s/%s", config.BlockScanner.DBPath, config.BlockScanner.ChainID,
		)
	}

	client.storage, err = blockscanner.NewBlockScannerStorage(
		path,
		client.config.ScannerLevelDB,
	)
	if err != nil {
		logger.Err(err).Msg("failed to create scan storage")
		return nil, err
	}

	client.tronScanner, err = NewTronBlockScannerWithAPI(
		config,
		apiClient,
		client.bridge,
		pubkeyMgr,
		client.ReportSolvency,
	)
	if err != nil {
		logger.Err(err).Msg("failed to create tron block scanner")
		return nil, err
	}

	client.blockScanner, err = blockscanner.NewBlockScanner(
		client.config.BlockScanner,
		client.storage,
		metrics,
		client.bridge,
		client.tronScanner,
	)
	if err != nil {
		logger.Err(err).Msg("failed to create block scanner")
		return nil, err
	}

	client.signerCacheManager, err = signercache.NewSignerCacheManager(
		client.storage.GetInternalDb(),
	)
	if err != nil {
		logger.Err(err).Msg("failed to create signer cache manager")
		return nil, err
	}

	client.abi, err = abi.JSON(bytes.NewReader(trc20ContractABI))
	if err != nil {
		logger.Err(err).Msg("failed to parse ABI")
		return nil, err
	}

	return &client, nil
}

// Start Tron chain client
func (c *TronClient) Start(
	globalTxsQueue chan types.TxIn,
	_ chan types.ErrataBlock,
	globalSolvencyQueue chan types.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.globalSolvencyQueue = globalSolvencyQueue
	c.tronScanner.globalNetworkFeeQueue = globalNetworkFeeQueue
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.tssKeyManager.Start()
	if c.endpointMgr != nil {
		c.endpointMgr.SetScannerHeight(c.blockScanner.PreviousHeight)
		c.endpointMgr.Start()
	}

	c.wg.Add(1)
	go runners.SolvencyCheckRunner(
		c.GetChain(),
		c,
		c.bridge,
		c.stopchan,
		c.wg,
		time.Second,
	)
}

// Stop Tron chain client
func (c *TronClient) Stop() {
	c.endpointMgr.Stop()
	c.tssKeyManager.Stop()
	c.blockScanner.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

// EndpointManager exposes live endpoint health to the bifrost health server.
func (c *TronClient) EndpointManager() *endpoints.Manager { return c.endpointMgr }

func (c *TronClient) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

// GetChain returns the chain.
func (c *TronClient) GetChain() common.Chain {
	return c.config.ChainID
}

// GetConfig returns the chain client configuration
func (c *TronClient) GetConfig() tcconfig.BifrostChainConfiguration {
	return c.config
}

// GetHeight returns the current height of the chain.
func (c *TronClient) GetHeight() (int64, error) {
	return c.tronScanner.GetHeight()
}

// GetAddress returns the address for the given public key.
func (c *TronClient) GetAddress(pubKey common.PubKey) string {
	address, err := pubKey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).Msg("failed to get pool address")
		return ""
	}

	return address.String()
}

// GetAccount returns the account for the given public key.
func (c *TronClient) GetAccount(
	pubKey common.PubKey,
	height *big.Int,
) (common.Account, error) {
	address, err := pubKey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).
			Str("pubkey", pubKey.String()).
			Msg("failed to get pool address")
		return common.Account{}, err
	}
	return c.GetAccountByAddress(address.String(), height)
}

// GetAccountByAddress returns the account for the given address.
func (c *TronClient) GetAccountByAddress(
	address string,
	_ *big.Int,
) (common.Account, error) {
	balance, err := c.api.GetBalance(address)
	if err != nil {
		c.logger.Err(err).Msg("failed to get account")
		return common.Account{}, err
	}

	coins := common.Coins{{
		Asset:    common.TRXAsset,
		Amount:   math.NewUint(balance).Mul(math.NewUint(100)), // 1e6 -> 1e8
		Decimals: 6,
	}}

	account := common.Account{
		Coins: coins,
	}

	if len(c.whitelist) == 0 {
		return account, nil
	}

	for contract, token := range c.whitelist {
		balance, err := c.getTokenBalance(address, contract)
		if err != nil {
			c.logger.Err(err).Msg("failed to get token balance")
			return account, err
		}

		balance, err = common.ConvertDecimals(
			balance, token.Decimals, common.THORChainDecimals,
		)
		if err != nil {
			c.logger.Err(err).Msg("failed to convert token balance")
			return account, err
		}

		coin := common.NewCoin(
			token.Asset(c.config.ChainID),
			math.NewUintFromBigInt(balance),
		)
		coin.Decimals = int64(token.Decimals)

		account.Coins = account.Coins.Add(coin)
	}

	return account, nil
}

// GetBlockScannerHeight returns block scanner height for chain
func (c *TronClient) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

// RollbackBlockScanner rolls back the block scanner to the last observed block
func (c *TronClient) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

// GetLatestTxForVault returns last observed and broadcasted tx for a particular vault and chain
func (c *TronClient) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(
		types.InboundCacheKey(vault, c.GetChain().String()),
	)
	if err != nil {
		return "", "", err
	}
	lastBroadcasted, err := c.signerCacheManager.GetLatestRecordedTx(
		types.BroadcastCacheKey(vault, c.GetChain().String()),
	)
	return lastObserved, lastBroadcasted, err
}

// GetConfirmationCount returns the confirmation count for the given tx.
func (c *TronClient) GetConfirmationCount(_ types.TxIn) int64 {
	// https://developers.tron.network/docs/tron-protocol-transaction#transaction-lifecycle
	// We are scanning 19 blocks behind the actual tip, so returning 0 here
	return 0
}

func (c *TronClient) ConfirmationCountReady(_ types.TxIn) bool {
	return true
}

// OnObservedTxIn is called when a new observed tx is received.
func (c *TronClient) OnObservedTxIn(_ types.TxInItem, _ int64) {}

// tronVerifyBroadcast is the signer-cache verifyFn. With an endpoint manager, every
// usable endpoint is asked with a pinned request: a tx known to any endpoint is
// Found, any transport error, no queried endpoint, or an endpoint left unasked
// (unverified) is Ambiguous, and only absence on every endpoint is NotFound. Without a manager it asks the single API.
func tronVerifyBroadcast(ctx context.Context, mgr *endpoints.Manager, tronAPI *api.TronApi, sig string, maxDown time.Duration) signercache.LookupResult {
	lookup := func(ctx context.Context) signercache.LookupResult {
		tx, txErr := tronAPI.GetTransactionWithContext(ctx, sig)
		switch {
		case txErr == nil && tx.TxId != "":
			return signercache.LookupFound
		case txErr == nil, isTronNotFoundErr(txErr):
			return signercache.LookupNotFound
		default:
			return signercache.LookupAmbiguous
		}
	}
	if mgr == nil {
		return lookup(ctx)
	}
	candidates, unverified := mgr.VerificationCandidates(maxDown)
	if len(candidates) == 0 {
		return signercache.LookupAmbiguous // absence needs at least one witness
	}
	perEndpoint := evm.VerificationBudget(ctx, len(candidates))
	ambiguous := false
	for _, e := range candidates {
		ectx, cancel := context.WithTimeout(endpoints.PinContext(ctx, e.Name), perEndpoint)
		res := lookup(ectx)
		cancel()
		switch res {
		case signercache.LookupFound:
			return signercache.LookupFound
		case signercache.LookupAmbiguous:
			ambiguous = true
		}
	}
	if ambiguous || unverified > 0 {
		return signercache.LookupAmbiguous
	}
	return signercache.LookupNotFound
}

// getSignerCacheExpiry returns the configured signer-cache expiry (config signer_cache_expiry).
func (c *TronClient) getSignerCacheExpiry() time.Duration {
	return c.config.SignerCacheExpiry
}

// SignTx returns the signed transaction.
func (c *TronClient) SignTx(
	txOutItem types.TxOutItem,
	thorchainHeight int64,
) ([]byte, []byte, *types.TxInItem, error) {
	// Signer-cache check, standardized across chains: respect the cache while fresh;
	// after the expiry window, only bypass when the operational SkipSignerCache<Chain>
	// mimir is set AND the recorded broadcast is verified absent on chain.
	mimirKey := fmt.Sprintf(constants.MimirTemplateSkipSignerCache, c.config.ChainID)
	gateFn := func() bool {
		skipCache, err := c.bridge.GetMimir(mimirKey)
		return err == nil && skipCache > 0
	}
	// gettransactionbyid reports presence regardless of confirmation, so the
	// verification path can positively decide whether the recorded broadcast is on
	// chain before clearing the cache: a populated TxId means present (respect the
	// cache), an empty result with no error means absent (safe to clear and
	// re-sign). Transport errors stay ambiguous (fail-closed, cache respected).
	verifyFn := func(sig string) signercache.LookupResult {
		return tronVerifyBroadcast(context.Background(), c.endpointMgr, c.api, sig, c.getSignerCacheExpiry())
	}
	check := c.signerCacheManager.SignTxCacheCheck(txOutItem.CacheHash(), gateFn, verifyFn)
	if check.Phase != signercache.PhaseNoEntry {
		ev := c.logger.Info()
		if check.Noteworthy() {
			ev = c.logger.Warn()
		}
		ev.Stringer("phase", check.Phase).
			Str("sig", check.Sig).
			Interface("tx", txOutItem).
			Msg(check.Phase.String())
	}
	if check.SkipSign {
		return nil, nil, nil, nil
	}

	if len(txOutItem.Coins) != 1 {
		err := fmt.Errorf("multiple or no coins found")
		c.logger.Err(err).Msg("")
		return nil, nil, nil, err
	}

	coin := txOutItem.Coins[0]
	if coin.IsEmpty() {
		err := fmt.Errorf("coin is empty")
		c.logger.Err(err).Msg("")
		return nil, nil, nil, err
	}

	var err error
	var tronTx api.Transaction
	memo := txOutItem.GetMemo()

	fromAddress := c.GetAddress(txOutItem.VaultPubKey)
	if len(txOutItem.Checkpoint) != 0 {
		tronTx, err = decodeTronSignCheckpoint(txOutItem.Checkpoint, txOutItem)
		if err != nil {
			c.logger.Err(err).Msg("failed to restore unsigned TRON transaction")
			return nil, nil, nil, err
		}
		c.logger.Info().Str("txid", tronTx.TxId).Msg("using checkpointed unsigned TRON transaction")
		return c.signTronTransaction(txOutItem, thorchainHeight, tronTx, txOutItem.Checkpoint, c.sign)
	}

	refBlocks := c.tronScanner.refBlockSnapshot()
	if len(refBlocks) == 0 {
		err = fmt.Errorf("no ref blocks found")
		c.logger.Err(err).Msg("")
		return nil, nil, nil, err
	}

	if coin.Asset == common.TRXAsset {
		// do trx transfer
		tronTx, err = c.api.CreateTransaction(
			fromAddress,
			txOutItem.ToAddress.String(),
			coin.Amount.Quo(math.NewUint(100)).Uint64(), // 1e8 -> 1e6
			memo,
		)
		if err != nil {
			c.logger.Err(err).Msg("failed to create tx")
			return nil, nil, nil, err
		}
	} else {
		found := false
		var contract string
		var token tokenlist.ERC20Token

		for contract, token = range c.whitelist {
			symbol := strings.ToUpper(token.Symbol + "-" + contract)
			if coin.Asset.Symbol.String() == symbol {
				found = true
				break
			}
		}

		if !found {
			notFoundErr := fmt.Errorf("token not whitelisted")
			c.logger.Err(notFoundErr).Msg("")
			return nil, nil, nil, notFoundErr
		}

		amount := coin.Amount.BigInt()
		amount, err = common.ConvertDecimals(
			amount, common.THORChainDecimals, token.Decimals,
		)
		if err != nil {
			c.logger.Err(err).Msg("failed to convert token balance")
			return nil, nil, nil, err
		}

		nativeFeeLimit := c.config.ChainID.ThorchainToNativeGas(txOutItem.MaxGas[0].Amount)
		tronTx, err = c.createTrc20Transaction(
			fromAddress,
			txOutItem.ToAddress.String(),
			contract,
			*amount,
			*nativeFeeLimit.BigInt(),
		)
		if err != nil {
			c.logger.Err(err).Msg("failed to create trc20 tx")
			return nil, nil, nil, err
		}
	}

	// Get the THORChain block timestamp for determinism - all nodes query the same
	// THORChain height and receive the same timestamp, preventing the race condition
	// where nodes at different scan heights would have different refBlocks lists.
	// Using time.Now() or scanner state would cause different transaction hashes
	// and prevent TSS signing consensus.
	thorTimestamp, err := c.bridge.GetBlockTimestamp(txOutItem.Height)
	if err != nil {
		c.logger.Err(err).Msg("failed to get THORChain block timestamp")
		return nil, nil, nil, err
	}

	// Look 2 minutes back to reduce race around the latest scanned ref block
	refTimestamp := thorTimestamp.Add(-2 * time.Minute)

	// Convert to milliseconds for TRON
	timestamp := thorTimestamp.UnixMilli()
	refTimestampMs := refTimestamp.UnixMilli()

	// Find the reference block closest to (but before) the THORChain timestamp
	// This ensures the ref block is deterministic while still being valid for TRON
	index := len(refBlocks) - 1
	for i := index; i >= 0; i-- {
		refBlock := refBlocks[i]
		if refBlock.Timestamp <= refTimestampMs {
			index = i
			break
		}
	}

	refBlock := refBlocks[index]

	refBlockHash := refBlock.Id[16:32]
	refBlockBytes := fmt.Sprintf("%016x", refBlock.Height)[12:16]

	tronTx.RawData.RefBlockBytes = refBlockBytes
	tronTx.RawData.RefBlockHash = refBlockHash
	tronTx.RawData.Timestamp = timestamp
	tronTx.RawData.Expiration = timestamp + TimestampValidity.Milliseconds()
	tronTx.RawData.Data = hex.EncodeToString([]byte(memo))

	err = tronTx.Rehash()
	if err != nil {
		c.logger.Err(err).Msg("failed to rehash transaction")
		return nil, nil, nil, err
	}

	checkpoint, err := encodeTronSignCheckpoint(tronTx, txOutItem)
	if err != nil {
		c.logger.Err(err).Msg("failed to checkpoint unsigned TRON transaction")
		return nil, nil, nil, err
	}
	return c.signTronTransaction(txOutItem, thorchainHeight, tronTx, checkpoint, c.sign)
}

func (c *TronClient) signTronTransaction(
	txOutItem types.TxOutItem,
	thorchainHeight int64,
	tronTx api.Transaction,
	checkpoint []byte,
	sign func([]byte, types.TxOutItem, int64) ([]byte, error),
) ([]byte, []byte, *types.TxInItem, error) {
	hash, err := hex.DecodeString(tronTx.TxId)
	if err != nil {
		c.logger.Err(err).Msg("failed to decode tx hash")
		return nil, checkpoint, nil, err
	}

	signature, err := sign(hash, txOutItem, thorchainHeight)
	if err != nil {
		if errors.Is(err, tss.ErrNotSelectedForKeysign) {
			return nil, nil, nil, nil
		}
		c.logger.Err(err).Msg("failed to sign tx")
		return nil, checkpoint, nil, err
	}

	tronTx.Signature = append(tronTx.Signature, hex.EncodeToString(signature))
	txBytes, err := json.Marshal(tronTx)
	if err != nil {
		c.logger.Err(err).Msg("failed to marshal tx")
		return nil, checkpoint, nil, err
	}

	currentHeight, err := c.tronScanner.GetHeight()
	if err != nil {
		c.logger.Err(err).Msg("failed to get latest block")
		return nil, checkpoint, nil, err
	}

	gasCoin := c.config.ChainID.NativeGasToThorCoin(cosmos.NewUint(uint64(txOutItem.GasRate)))
	txIn := types.NewTxInItem(
		currentHeight,
		tronTx.TxId,
		txOutItem.GetMemo(),
		c.GetAddress(txOutItem.VaultPubKey),
		txOutItem.ToAddress.String(),
		txOutItem.Coins,
		[]common.Coin{gasCoin},
		txOutItem.VaultPubKey,
		"",
		"",
		nil,
	)

	return txBytes, nil, txIn, nil
}

// BroadcastTx sends the transaction to Tron chain
func (c *TronClient) BroadcastTx(
	txOutItem types.TxOutItem,
	txBytes []byte,
) (string, error) {
	response, err := c.api.BroadcastTransaction(txBytes)
	if err != nil {
		c.logger.Err(err).Msg("failed to broadcast tx")
		return "", err
	}

	// treat dup transaction as success - the transaction was already accepted
	if !response.Result && response.Code == "DUP_TRANSACTION_ERROR" {
		var tx api.Transaction
		if unmarshalErr := json.Unmarshal(txBytes, &tx); unmarshalErr != nil {
			return "", fmt.Errorf("failed to unmarshal tx for dup txid: %w", unmarshalErr)
		}
		c.logger.Info().Str("txid", tx.TxId).Msg("dup transaction treated as success")
		response.Result = true
		response.TxId = tx.TxId
	}

	// check the response
	if !response.Result {
		err = fmt.Errorf("failed to broadcast tx: %s - %s", response.Code, response.Message)
		c.logger.Err(err).Msg("")
		return "", err
	}

	expiry := time.Now().Add(c.getSignerCacheExpiry()).UnixMilli()
	err = c.signerCacheManager.SetSignedWithExpiry(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(c.GetChain()),
		response.TxId,
		expiry,
	)
	if err != nil {
		c.logger.Err(err).
			Interface("tx_out_item", txOutItem).
			Msg("failed to mark tx out item as signed")
	}

	return response.TxId, nil
}

func isTronNotFoundErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "response status: 404")
}

// GetGasEstimateForSolvency returns the current gas fee estimate used by solvency checks.
func (c *TronClient) GetGasEstimateForSolvency() cosmos.Uint {
	fee := math.NewUint(c.tronScanner.currentFee.Load())
	return c.config.ChainID.NativeGasToThorchain(fee)
}

func (c *TronClient) ShouldReportSolvency(height int64) bool {
	return height%10 == 0
}

func (c *TronClient) ReportSolvency(height int64) error {
	// The ordered scan loop and SolvencyCheckRunner can both call this.
	c.solvencyLock.Lock()
	defer c.solvencyLock.Unlock()

	if !c.ShouldReportSolvency(height) {
		return nil
	}

	// when block scanner is not healthy, only report from auto-unhalt SolvencyCheckRunner
	// TRON passes chainHeight from both callers; at the tip this is PreviousHeight + 1.
	if !c.IsBlockScannerHealthy() && height == c.blockScanner.PreviousHeight()+1 {
		return nil
	}

	asgardVaults, err := c.bridge.GetAsgards()
	if err != nil {
		c.logger.Err(err).Msg("failed to get asgard vaults")
		return err
	}

	currentGasFee := c.GetGasEstimateForSolvency()

	// report insolvent asgard vaults,
	// or else all if the chain is halted and all are solvent
	chainVaults := asgardVaults[:0]
	for _, asgard := range asgardVaults {
		if asgard.HasFundsForChain(c.config.ChainID) {
			chainVaults = append(chainVaults, asgard)
		}
	}

	solventMsgs := make([]types.Solvency, 0, len(chainVaults))
	insolventMsgs := make([]types.Solvency, 0, len(chainVaults))
	processedVaults := 0
	for i, vault := range chainVaults {
		acc, err := c.GetAccount(vault.PubKey, big.NewInt(height))
		if err != nil {
			c.logger.Err(err).Msgf("failed to get account balance")
			continue
		}

		msg := types.Solvency{
			// Token balances reported from the RPC appear to be based on finalized state and
			// lagging the observations themselves. We report the finalized height with the
			// solvency message so the handler in thornode will disregard if there is a more
			// recent observation.
			Height: height - FinalityBlocks,
			Chain:  c.config.ChainID,
			PubKey: chainVaults[i].PubKey,
			Coins:  acc.Coins,
		}
		processedVaults++

		if runners.IsVaultSolvent(c.config.ChainID, acc, chainVaults[i], currentGasFee) {
			solventMsgs = append(solventMsgs, msg)
			continue
		}

		insolventMsgs = append(insolventMsgs, msg)
	}

	msgs := insolventMsgs
	solvent := false

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
	allVaultsProcessed := len(chainVaults) > 0 && processedVaults == len(chainVaults)
	halted, haltErr := runners.IsHaltedOnChain(c.bridge, c.config.ChainID)
	if haltErr != nil {
		c.logger.Err(haltErr).Msg("fail to read chain halt state, skipping solvency all-clear")
	}
	if halted && !c.IsBlockScannerHealthy() && allVaultsProcessed && len(solventMsgs) == processedVaults {
		msgs = solventMsgs
		solvent = true
	}

	for _, msg := range msgs {
		c.logger.Info().
			Str("asgard", msg.PubKey.String()).
			Interface("coins", msg.Coins).
			Bool("solvent", solvent).
			Msg("reporting solvency")
		// send solvency to thorchain via global queue consumed by the observer
		select {
		case c.globalSolvencyQueue <- msg:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msg("failed to send solvency info to THORChain")
		}
	}

	return nil
}

func (c *TronClient) sign(
	data []byte,
	txOutItem types.TxOutItem,
	thorchainHeight int64,
) ([]byte, error) {
	var err error
	var signature, recovery []byte

	pubkey := txOutItem.VaultPubKey

	if c.localKeyManager.Pubkey().Equals(pubkey) {
		signature, err = c.localKeyManager.Sign(data)
		if err != nil {
			c.logger.Err(err).Msg("unable to sign using localKeyManager")
			return nil, err
		}
	} else {
		signature, recovery, err = c.tssKeyManager.RemoteSign(data, common.SigningAlgoSecp256k1, pubkey.String(), thorchainHeight)
		if err == nil && signature != nil {
			return append(signature, recovery...), nil
		}
		if err == nil && signature == nil {
			return nil, tss.ErrNotSelectedForKeysign
		}

		var keysignError tss.KeysignError
		if errors.As(err, &keysignError) {
			if len(keysignError.Blame.BlameNodes) == 0 {
				// TSS doesn't know which node to blame
				c.logger.Err(err).Msg("failed to sign tx")
				return nil, err
			}

			// forward the keysign blame to thorchain
			txId, errPostKeysignFail := c.bridge.PostKeysignFailure(
				keysignError.Blame,
				txOutItem.Height,
				txOutItem.GetMemo(),
				txOutItem.Coins,
				txOutItem.VaultPubKey,
			)
			if errPostKeysignFail != nil {
				return nil, multierror.Append(err, errPostKeysignFail)
			}
			c.logger.Info().
				Str("tx_id", txId.String()).
				Msg("post keysign failure to THORChain")
		}

		c.logger.Err(err).Msg("failed to sign tx")
		return nil, err
	}

	sigPub, err := crypto.Ecrecover(data, signature)
	if err != nil {
		c.logger.Err(err).Msg("failed to get public key")
		return nil, err
	}

	secpPub, err := pubkey.Secp256K1()
	if err != nil {
		c.logger.Err(err).Msg("failed convert public key")
		return nil, err
	}

	ecdsaPub := crypto.FromECDSAPub(secpPub.ToECDSA())

	if !bytes.Equal(sigPub, ecdsaPub) {
		err := fmt.Errorf("unable to verify signature with secp pub key")
		c.logger.Err(err).Msg("")
		return nil, err
	}

	return signature, nil
}

func (c *TronClient) createTrc20Transaction(
	fromAddress, toAddress, contractAddress string,
	amount big.Int,
	feeLimit big.Int,
) (tx api.Transaction, err error) {
	toAddressHex, err := api.ConvertAddress(toAddress)
	if err != nil {
		c.logger.Err(err).Msg("failed to convert address")
		return api.Transaction{}, err
	}

	// convert to proper evm address, so go-ethereum is able to handle it
	toAddressHex = strings.Replace(toAddressHex, "41", "0x", 1)

	data, err := c.abi.Pack(
		"transfer", ethcommon.HexToAddress(toAddressHex), &amount,
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to pack data")
		return tx, err
	}

	tx, err = c.api.TriggerSmartContract(
		fromAddress,
		contractAddress,
		"transfer(address,uint256)",
		hex.EncodeToString(data[4:]),
		feeLimit.Uint64(),
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to create trc20 transfer tx")
		return tx, err
	}

	return tx, nil
}

func (c *TronClient) getTokenBalance(
	address, contract string,
) (*big.Int, error) {
	addresses := []string{address, contract}
	for i, address := range addresses {
		tmp, err := api.ConvertAddress(address)
		if err != nil {
			c.logger.Err(err).Msg("failed to convert address")
			return nil, err
		}

		// convert to proper evm address, so go-ethereum is able to handle it
		addresses[i] = strings.Replace(tmp, "41", "0x", 1)
	}

	data, err := c.abi.Pack(
		"balanceOf", ethcommon.HexToAddress(addresses[0]),
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to pack data")
		return nil, err
	}

	data, err = c.rpc.EthCall(addresses[1], fmt.Sprintf("%x", data))
	if err != nil {
		c.logger.Err(err).Msg("failed to process EthCall")
		return nil, err
	}

	var response rpc.Response
	err = json.Unmarshal(data, &response)
	if err != nil {
		c.logger.Err(err).Msg("failed to unmarshal response")
		return nil, err
	}

	if response.Result == "" {
		err = fmt.Errorf("response result is empty")
		c.logger.Err(err).Msg("")
		return nil, err
	}

	// omit the first two bytes ("0x")
	result, err := hex.DecodeString(response.Result[2:])
	if err != nil {
		c.logger.Err(err).Msg("failed to decode result data")
		return nil, err
	}

	unpacked, err := c.abi.Unpack("balanceOf", result)
	if err != nil {
		c.logger.Err(err).Msg("failed to unpack result data")
		return nil, err
	}

	balance, ok := unpacked[0].(*big.Int)
	if !ok {
		err := fmt.Errorf("failed to convert balance")
		c.logger.Err(err).Msg("")
		return nil, err
	}

	return balance, nil
}
