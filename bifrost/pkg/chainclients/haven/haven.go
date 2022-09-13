package haven

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	btypes "gitlab.com/thorchain/thornode/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/bifrost/pubkeymanager"
	ttypes "gitlab.com/thorchain/thornode/x/thorchain/types"
	"go.uber.org/atomic"

	tssp "github.com/akildemir/moneroTss/tss"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/haven-protocol-org/go-haven-rpc-client/wallet"
	"github.com/haven-protocol-org/monero-go-utils/crypto"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/bifrost/tss"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/config"
	"gitlab.com/thorchain/thornode/constants"
	mem "gitlab.com/thorchain/thornode/x/thorchain/memo"
)

// Client observes bitcoin chain and allows to sign and broadcast tx
type Client struct {
	logger                    zerolog.Logger
	cfg                       config.BifrostChainConfiguration
	walletAddr                common.Address
	processedMemPool          map[string]bool
	signedTxOut               map[string]TxOutWitness
	signedTxOutDelete         map[string]int64
	asgardNames               map[common.PubKey]string
	memPoolLock               *sync.Mutex
	signedTxOutLock           *sync.Mutex
	clientLock                *sync.Mutex
	asgardNamesLock           *sync.Mutex
	lastMemPoolScan           time.Time
	blockScanner              *blockscanner.BlockScanner
	temporalStorage           *utxo.TemporalStorage
	bridge                    *thorclient.ThorchainBridge
	globalErrataQueue         chan<- types.ErrataBlock
	globalSolvencyQueue       chan<- types.Solvency
	asgardAddresses           []common.Address
	nodePubKey                common.PubKey
	lastAsgard                time.Time
	asgardPassword            string
	pkm                       pubkeymanager.PubKeyValidator
	poolMgr                   thorclient.PoolManager
	signingOrKeygenInProgress *atomic.Bool
	stopScanTxOuts            *atomic.Bool
	currentBlockHeight        *atomic.Int64
	supportedAssets           []string
	tssKm                     *tss.KeySign
	client                    wallet.Client
}

type TxVout struct {
	Address string
	Amount  uint64
	Asset   string
	ind     int
}

type TxOutWitness struct {
	TxID     string
	TxKey    string
	Coin     common.Coin
	Receiver string
	Sender   string
	Memo     string
	Asset    common.Asset
	Vault    ttypes.VaultType
	Signed   bool
}

// BlockCacheSize the number of block meta that get store in storage.
const (
	DecimalDiff        = uint64(10000) // haven has 12 atomic units and thorchain uses 8. This is the 4 difference.
	BlockCacheSize     = 144
	MaxAsgardAddresses = 100
	// EstimateAverageTxSize for THORChain the estimate tx size is hard code to 1000 here , as most of time it will spend 1 input, have 3 output
	// which is average at 250 vbytes , however asgard will consolidate UTXOs , which will take up to 1000 vbytes
	EstimateAverageTxSize = uint64(1000)
	AverageFeeRate        = uint64(31401)
)

// NewClient generates a new Client
func NewClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	bridge *thorclient.ThorchainBridge,
	pkm pubkeymanager.PubKeyValidator,
	poolMgr thorclient.PoolManager,
	m *metrics.Metrics,
) (*Client, error) {
	// set the daemon address
	DaemonHost = cfg.RPCHost

	// create the wallet rpc client
	client := wallet.New(wallet.Config{
		Address: cfg.WalletRPCHost,
	})

	// create haven tss server
	tssKm, err := tss.NewKeySignMn(server, bridge)
	if err != nil {
		return nil, fmt.Errorf("fail to create tss signer: %w", err)
	}

	// Get thor keys
	thorPrivKey, err := thorKeys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get THORChain private key: %w", err)
	}
	temp, err := codec.ToTmPubKeyInterface(thorPrivKey.PubKey())
	if err != nil {
		return nil, fmt.Errorf("fail to get tm pub key: %w", err)
	}
	thorPubKey, err := common.NewPubKeyFromCrypto(temp)
	if err != nil {
		return nil, fmt.Errorf("fail to get the node pubkey: %w", err)
	}

	// generate private keys for local haven wallet
	havenPrivViewKey, havenPrivSpendKey := getHavenPrivateKey(thorPrivKey)

	// generate wallet address
	walletAddr, err := generateWalletAddress(&havenPrivSpendKey, &havenPrivViewKey)
	if err != nil {
		return nil, fmt.Errorf("fail to generate a haven wallet address: %+v", err)
	}

	// try to generate a haven wallet
	walletResp, err := client.CreateWalletFromKeys(&wallet.RequestCreateWalletFromKeys{
		Filename: walletAddr.String(),
		Password: cfg.Password,
		SpendKey: hex.EncodeToString(havenPrivSpendKey[:]),
		ViewKey:  hex.EncodeToString(havenPrivViewKey[:]),
		Address:  walletAddr.String(),
	})
	if err != nil {
		b, walletError := wallet.GetWalletError(err)
		if b && walletError.Code == -1 {
			// wallet already exist, try to login with the password
			err = client.OpenWallet(&wallet.RequestOpenWallet{
				Filename: walletAddr.String(),
				Password: cfg.Password,
			})
			if err != nil {
				return nil, fmt.Errorf("fail to generate a haven wallet %s, wallet already exist but can't login with the given password: %+v", walletAddr.String(), err)
			}

			// verify the addresses match
			addr, err := client.GetAddress(&wallet.RequestGetAddress{
				AccountIndex: 0,
			})
			if err != nil {
				return nil, fmt.Errorf("fail to generate a haven wallet, wallet already exist but can't get the address: %+v", err)
			}
			if addr.Address != walletAddr.String() {
				return nil, fmt.Errorf("fail to generate a haven wallet, wallet already exist but has different address than bifrost: %+v", err)
			}
		} else {
			return nil, fmt.Errorf("fail to generate a haven wallet: %+v", err)
		}
	} else if len(walletResp.Address) == 0 {
		return nil, fmt.Errorf("unexpected error when generating the haven wallet: %s", walletResp.Info)
	}

	if pkm == nil {
		return nil, errors.New("pubkey manager is nil")
	}
	if poolMgr == nil {
		return nil, errors.New("pool manager is nil")
	}

	c := &Client{
		logger:                    log.Logger.With().Str("module", "haven").Logger(),
		cfg:                       cfg,
		bridge:                    bridge,
		pkm:                       pkm,
		poolMgr:                   poolMgr,
		nodePubKey:                thorPubKey,
		walletAddr:                common.Address(walletResp.Address),
		memPoolLock:               &sync.Mutex{},
		signedTxOutLock:           &sync.Mutex{},
		clientLock:                &sync.Mutex{},
		asgardNamesLock:           &sync.Mutex{},
		client:                    client,
		tssKm:                     tssKm,
		supportedAssets:           []string{"XHV", "XUSD", "XBTC", "XEUR", "XGBP", "XCHF", "XAUD", "XAU", "XAG", "XCNY"},
		asgardPassword:            hex.EncodeToString(common.CosmosPrivateKeyToTMPrivateKey(thorPrivKey).Bytes()),
		processedMemPool:          make(map[string]bool),
		signingOrKeygenInProgress: atomic.NewBool(false),
		stopScanTxOuts:            atomic.NewBool(false),
		signedTxOut:               make(map[string]TxOutWitness),
		signedTxOutDelete:         make(map[string]int64),
		asgardNames:               make(map[common.PubKey]string),
		currentBlockHeight:        atomic.NewInt64(0),
	}

	var path string // if not set later, will in memory storage
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}
	storage, err := blockscanner.NewBlockScannerStorage(path)
	if err != nil {
		return c, fmt.Errorf("fail to create blockscanner storage: %w", err)
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(c.cfg.BlockScanner, storage, m, bridge, c)
	if err != nil {
		return c, fmt.Errorf("fail to create block scanner: %w", err)
	}

	c.temporalStorage, err = utxo.NewTemporalStorage(storage.GetInternalDb())
	if err != nil {
		return c, fmt.Errorf("fail to create temporal storage: %w", err)
	}

	c.logger.Info().Msgf("local vault Haven address %s", c.walletAddr.String())
	c.logger.Info().Msgf("Haven Daemon IP address %s", DaemonHost)
	c.logger.Info().Msgf("Haven Wallet-Rpc IP address %s", cfg.WalletRPCHost)
	return c, nil
}

// Start starts the block scanner
func (c *Client) Start(globalTxsQueue chan types.TxIn, globalErrataQueue chan types.ErrataBlock, globalSolvencyQueue chan types.Solvency) {
	c.globalErrataQueue = globalErrataQueue
	c.globalSolvencyQueue = globalSolvencyQueue
	c.blockScanner.Start(globalTxsQueue)
	go c.scanTxOuts()
}

// Stop stops the block scanner
func (c *Client) Stop() {
	c.blockScanner.Stop()
	c.tssKm.Stop()
	c.stopScanTxOuts.Store(true)
}

// GetConfig - get the chain configuration
func (c *Client) GetConfig() config.BifrostChainConfiguration {
	return c.cfg
}

// GetChain returns haven Chain
func (c *Client) GetChain() common.Chain {
	return common.XHVChain
}

// GetHeight returns current block height
func (c *Client) GetHeight() (int64, error) {
	height, err := GetChainHeight()
	if err != nil {
		return 0, fmt.Errorf("failed to get height: %+v", err)
	}
	return int64(height), nil
}

// GetAddress return current local haven vault address
func (c *Client) GetAddress(poolPubKey common.PubKey) string {
	addr, err := c.getAddrFromCndata(c.pkm.GetCnData(common.XHVChain, poolPubKey))
	if err != nil {
		c.logger.Error().Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get pool address")
		return ""
	}
	return addr.String()
}

// GetAccountByAddress return empty account for now
func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	return common.Account{}, nil
}

func (c *Client) openWallet(walletName, password string) error {
	c.clientLock.Lock()
	defer c.clientLock.Unlock()
	return c.client.OpenWallet(&wallet.RequestOpenWallet{
		Filename: walletName,
		Password: password,
	})
}

func (c *Client) SetAsgardWallet(keygenHeight int64, pk common.PubKey) {
	c.asgardNamesLock.Lock()
	defer c.asgardNamesLock.Unlock()
	c.asgardNames[pk] = strconv.FormatInt(keygenHeight, 10)
	c.logger.Info().Msgf("saved asgard keygen height %s for pk: %s", c.asgardNames[pk], pk.String())
}

func (c *Client) KeygenInProgress(val bool) {
	c.signingOrKeygenInProgress.Store(val)
}

// loginToWallet logs into wallet with a given pubKey. `fromSignTx` must be false
// for all calls to the function outside of signTx().
func (c *Client) loginToWallet(pk common.PubKey, fromSignTx bool) error {
	// cant login to wallet while signing is in progress
	if !fromSignTx && c.signingOrKeygenInProgress.Load() {
		return fmt.Errorf("cant login to wallet. Signing is in progress")
	}

	// check whether we already logged into the wallet
	c.clientLock.Lock()
	adrResp, err := c.client.GetAddress(&wallet.RequestGetAddress{
		AccountIndex: 0,
	})
	c.clientLock.Unlock()
	if err != nil {
		c.logger.Error().Msgf("fail go check for logged in address: %+v", err)
	} else {
		// TODO: not a good idea to calculate the address each time.
		localAddr, _ := c.getAddrFromCndata(c.pkm.GetCnData(common.XHVChain, pk))
		if adrResp.Address == localAddr.String() {
			return nil
		}
	}

	// open the wallet if not
	if c.isYggdrasil(pk) {
		err := c.openWallet(c.walletAddr.String(), c.cfg.Password)
		if err != nil {
			return err
		}
		c.logger.Info().Msgf("Switched wallet to yggdrasil %s", c.walletAddr.String())
	} else {

		// check whether the node is active or not
		nodeStatus, err := c.bridge.FetchNodeStatus()
		if err != nil {
			return err
		}
		if nodeStatus != ttypes.NodeStatus_Active {
			return fmt.Errorf("node is not active. No asgard wallet available")
		}

		// get the wallet name
		c.asgardNamesLock.Lock()
		asgardWalletName := c.nodePubKey.String() + "-" + c.asgardNames[pk] + ".mo"
		c.asgardNamesLock.Unlock()

		err = c.openWallet(asgardWalletName, c.asgardPassword)
		if err != nil {
			return err
		}
		c.logger.Info().Msgf("Switched wallet to asgard %s", asgardWalletName)
	}
	return nil
}

// GetAccount returns account with balance for an address
func (c *Client) GetAccount(pkey common.PubKey, height *big.Int) (common.Account, error) {
	if height != nil {
		c.logger.Error().Msg("height was provided but will be ignored")
	}

	acct := common.Account{}
	if pkey.IsEmpty() {
		return acct, errors.New("pubkey can't be empty")
	}

	// login to wallet
	err := c.loginToWallet(pkey, false)
	if err != nil {
		return acct, fmt.Errorf("fail to login to wallet: %w", err)
	}

	// refresh before checking for balance
	c.clientLock.Lock()
	defer c.clientLock.Unlock()
	var refresh *wallet.ResponseRefresh
	for i := 0; i < 3; i++ {
		refresh, err = c.client.Refresh(&wallet.RequestRefresh{})
		if err != nil {
			return acct, fmt.Errorf("error when refreshing wallet %w", err)
		}
		if refresh.BlocksFetched > 0 {
			break
		}
	}

	// get the spendable balance of each asset
	var coins []common.Coin
	for _, asset := range c.supportedAssets {
		resp, err := c.client.GetBalance(&wallet.RequestGetBalance{
			AccountIndex: 0,
			AssetType:    asset,
		})
		if err != nil {
			return acct, fmt.Errorf("fail to get the balance: %w", err)
		}
		if resp.Balance != 0 {
			a, err := common.NewAsset("XHV." + asset)
			if err != nil {
				return acct, fmt.Errorf("fail to construct asset: %w", err)
			}
			coins = append(coins, common.NewCoin(a, cosmos.NewUint(resp.Balance/DecimalDiff)))
		}
	}

	// return a new Account with the total amount spendable.
	return common.NewAccount(0, 0, coins, false), nil
}

func (c *Client) getTotalTransactionValue(txIn types.TxIn, excludeFrom []common.Address) cosmos.Uint {
	total := cosmos.ZeroUint()
	if len(txIn.TxArray) == 0 {
		return total
	}
	for _, item := range txIn.TxArray {
		fromAsgard := false
		for _, fromAddress := range excludeFrom {
			if strings.EqualFold(fromAddress.String(), item.Sender) {
				fromAsgard = true
				break
			}
		}
		if fromAsgard {
			continue
		}
		// if from address is yggdrasil , exclude the value from confirmation counting
		ok, _ := c.pkm.IsValidPoolAddress(item.Sender, common.XHVChain)
		if ok {
			continue
		}
		for _, coin := range item.Coins {
			if coin.IsEmpty() {
				continue
			}
			amount := coin.Amount
			if !coin.Asset.Equals(common.XHVAsset) {
				var err error
				amount, err = c.poolMgr.GetValue(coin.Asset, common.XHVAsset, coin.Amount)
				if err != nil {
					c.logger.Err(err).Msgf("fail to get value for %s", coin.Asset)
					continue
				}

			}
			total = total.Add(amount)
		}
	}
	return total
}

func (c *Client) getCoinbaseValue(blockHeight int64) (cosmos.Uint, error) {
	// get block
	block, err := GetBlock(blockHeight)
	if err != nil {
		return cosmos.Uint{}, fmt.Errorf("fail to get block verbose tx: %v", err)
	}

	total := cosmos.ZeroUint()
	for _, reward := range block.BlockHeader.Rewards {
		amount := cosmos.NewUint(reward.Amount / DecimalDiff)
		if reward.AssetType != common.XHVChain.String() {
			asset, err := common.NewAsset("XHV." + reward.AssetType)
			if err != nil {
				c.logger.Err(err).Msgf("fail to make asset for %s", reward.AssetType)
				continue
			}

			amount, err = c.poolMgr.GetValue(asset, common.XHVAsset, amount)
			if err != nil {
				c.logger.Err(err).Msgf("fail to get value for %s", asset)
				continue
			}
		}
		total = total.Add(amount)
	}

	return total, nil
}

func (c *Client) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

// getBlockRequiredConfirmation find out how many confirmation the given txIn need to have before it can be send to THORChain
func (c *Client) getBlockRequiredConfirmation(txIn types.TxIn, height int64) (int64, error) {
	totalTxValue := c.getTotalTransactionValue(txIn, c.asgardAddresses)
	totalFeeAndSubsidy, err := c.getCoinbaseValue(height)
	if err != nil {
		return 0, fmt.Errorf("fail to get coinbase value: %w", err)
	}
	confirm := totalTxValue.MulUint64(2).Quo(totalFeeAndSubsidy).Uint64()
	c.logger.Info().Msgf("totalTxValue:%s,total fee and Subsidy:%s,confirmation:%d", totalTxValue, totalFeeAndSubsidy, confirm)
	return int64(confirm), nil
}

// GetConfirmationCount return the number of blocks the tx need to wait before processing in THORChain
func (c *Client) GetConfirmationCount(txIn types.TxIn) int64 {
	if len(txIn.TxArray) == 0 {
		return 0
	}
	// MemPool items doesn't need confirmation
	if txIn.MemPool {
		return 0
	}
	blockHeight := txIn.TxArray[0].BlockHeight

	confirm, err := c.getBlockRequiredConfirmation(txIn, blockHeight)
	c.logger.Info().Msgf("confirmation required: %d", confirm)
	if err != nil {
		c.logger.Err(err).Msg("fail to get block confirmation ")
		return 0
	}
	return confirm
}

// ConfirmationCountReady will be called by observer before send the txIn to thorchain
// confirmation counting is on block level
func (c *Client) ConfirmationCountReady(txIn types.TxIn) bool {
	if len(txIn.TxArray) == 0 {
		return true
	}
	// MemPool items doesn't need confirmation
	if txIn.MemPool {
		return true
	}
	blockHeight := txIn.TxArray[0].BlockHeight
	confirm := txIn.ConfirmationRequired
	// every tx in txIn already have at least 1 confirmation
	return (c.currentBlockHeight.Load() - blockHeight) >= confirm
}

// OnObservedTxIn gets called from observer when we have a valid observation
// For bitcoin chain client we want to save the utxo we can spend later to sign
func (c *Client) OnObservedTxIn(txIn types.TxInItem, blockHeight int64) {
	blockMeta, err := c.temporalStorage.GetBlockMeta(blockHeight)
	if err != nil {
		c.logger.Err(err).Msgf("fail to get block meta on block height(%d)", blockHeight)
		return
	}
	if blockMeta == nil {
		blockMeta = utxo.NewBlockMeta("", blockHeight, "")
	}
	if _, err := c.temporalStorage.TrackObservedTx(txIn.Tx); err != nil {
		c.logger.Err(err).Msgf("fail to add hash (%s) to observed tx cache", txIn.Tx)
	}
	if c.isAsgardAddress(txIn.Sender) {
		c.logger.Debug().Msgf("add hash %s as self transaction,block height:%d", txIn.Tx, blockHeight)
		blockMeta.AddSelfTransaction(txIn.Tx)
	} else {
		// add the transaction to block meta
		blockMeta.AddCustomerTransaction(txIn.Tx)
	}
	if err := c.temporalStorage.SaveBlockMeta(blockHeight, blockMeta); err != nil {
		c.logger.Err(err).Msgf("fail to save block meta to storage,block height(%d)", blockHeight)
	}
}

func (c *Client) getAsgardAddress() ([]common.Address, error) {
	if time.Since(c.lastAsgard) < constants.ThorchainBlockTime && c.asgardAddresses != nil {
		return c.asgardAddresses, nil
	}
	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		return nil, fmt.Errorf("fail to get asgards : %w", err)
	}

	for _, v := range vaults {
		addr, err := common.PubKey(v.Cryptonote).GetAddress(common.XHVChain)
		if err != nil {
			c.logger.Err(err).Msg("fail to get address")
			continue
		}
		found := false
		for _, item := range c.asgardAddresses {
			if item.Equals(addr) {
				found = true
				break
			}
		}
		if !found {
			c.asgardAddresses = append(c.asgardAddresses, addr)
		}
	}
	if len(c.asgardAddresses) > MaxAsgardAddresses {
		startIdx := len(c.asgardAddresses) - MaxAsgardAddresses
		c.asgardAddresses = c.asgardAddresses[startIdx:]
	}
	c.lastAsgard = time.Now()
	return c.asgardAddresses, nil
}

func (c *Client) isAsgardAddress(addressToCheck string) bool {
	asgards, err := c.getAsgardAddress()
	if err != nil {
		c.logger.Err(err).Msg("fail to get asgard addresses")
		return false
	}
	for _, addr := range asgards {
		if strings.EqualFold(addr.String(), addressToCheck) {
			return true
		}
	}
	return false
}

// FetchTxs retrieves txs for a block height
func (c *Client) FetchTxs(height int64) (types.TxIn, error) {
	txIn := types.TxIn{
		Chain:   common.XHVChain,
		TxArray: nil,
	}
	block, err := GetBlock(height)
	if err != nil {
		return txIn, btypes.ErrUnavailableBlock
	}

	// if somehow the block is not valid
	if block.BlockHeader.Hash == "" && block.BlockHeader.PrevHash == "" {
		return txIn, fmt.Errorf("fail to get block: %w", err)
	}
	c.currentBlockHeight.Store(height)
	c.logger.Info().Int64("block height", c.currentBlockHeight.Load()).Msg("fetch txs")
	reScannedTxs, err := c.processReorg(block)
	if err != nil {
		c.logger.Err(err).Msg("fail to process bitcoin re-org")
	}
	if len(reScannedTxs) > 0 {
		for _, item := range reScannedTxs {
			if len(item.TxArray) == 0 {
				continue
			}
			txIn.TxArray = append(txIn.TxArray, item.TxArray...)
		}
	}

	// update block meta
	blockMeta, err := c.temporalStorage.GetBlockMeta(int64(block.BlockHeader.Height))
	if err != nil {
		return txIn, fmt.Errorf("fail to get block meta from storage: %w", err)
	}
	if blockMeta == nil {
		blockMeta = utxo.NewBlockMeta(block.BlockHeader.PrevHash, int64(block.BlockHeader.Height), block.BlockHeader.Hash)
	} else {
		blockMeta.PreviousHash = block.BlockHeader.PrevHash
		blockMeta.BlockHash = block.BlockHeader.Hash
	}

	if err := c.temporalStorage.SaveBlockMeta(int64(block.BlockHeader.Height), blockMeta); err != nil {
		return txIn, fmt.Errorf("fail to save block meta into storage: %w", err)
	}
	pruneHeight := height - BlockCacheSize
	if pruneHeight > 0 {
		defer func() {
			if err := c.temporalStorage.PruneBlockMeta(pruneHeight, c.canDeleteBlock); err != nil {
				c.logger.Err(err).Msgf("fail to prune block meta, height(%d)", pruneHeight)
			}
		}()
	}

	txInBlock, err := c.extractTxs(block)
	if err != nil {
		return types.TxIn{}, fmt.Errorf("fail to extract txIn from block: %w", err)
	}
	if len(txInBlock.TxArray) > 0 {
		txIn.TxArray = append(txIn.TxArray, txInBlock.TxArray...)
	}
	if err := c.sendNetworkFee(height); err != nil {
		c.logger.Err(err).Msg("fail to send network fee")
	}

	if err := c.reportSolvency(height); err != nil {
		c.logger.Err(err).Msgf("fail to send solvency info to THORChain")
	}
	txIn.Count = strconv.Itoa(len(txIn.TxArray))
	return txIn, nil
}

func (c *Client) canDeleteBlock(blockMeta *utxo.BlockMeta) bool {
	if blockMeta == nil {
		return true
	}
	for _, tx := range blockMeta.SelfTransactions {
		if c.txInThePool(tx) {
			c.logger.Info().Msgf("tx: %s still in mempool , block can't be deleted", tx)
			return false
		}
	}
	return true
}

func (c *Client) sendNetworkFee(height int64) error {
	txid, err := c.bridge.PostNetworkFee(height, common.XHVChain, EstimateAverageTxSize, AverageFeeRate)
	if err != nil {
		return fmt.Errorf("fail to post network fee to thornode: %w", err)
	}
	c.logger.Debug().Str("txid", txid.String()).Msg("send network fee to THORNode successfully")
	return nil
}

func (c *Client) processReorg(block Block) ([]types.TxIn, error) {
	previousHeight := int64(block.BlockHeader.Height - 1)
	prevBlockMeta, err := c.temporalStorage.GetBlockMeta(previousHeight)
	if err != nil {
		return nil, fmt.Errorf("fail to get block meta of height(%d) : %w", previousHeight, err)
	}
	if prevBlockMeta == nil {
		return nil, nil
	}
	// the block's previous hash need to be the same as the block hash chain client recorded in block meta
	// blockMetas[PreviousHeight].BlockHash == Block.PreviousHash
	if strings.EqualFold(prevBlockMeta.BlockHash, block.BlockHeader.PrevHash) {
		return nil, nil
	}

	c.logger.Info().Msgf("re-org detected, current block height:%d ,previous block hash is : %s , however block meta at height: %d, block hash is %s",
		block.BlockHeader.Height, block.BlockHeader.PrevHash,
		prevBlockMeta.Height, prevBlockMeta.BlockHash)
	blockHeights, err := c.reConfirmTx()
	if err != nil {
		c.logger.Err(err).Msgf("fail to reprocess all txs")
	}
	var txIns []types.TxIn
	for _, item := range blockHeights {
		c.logger.Info().Msgf("re-scan block height: %d", item)
		b, err := GetBlock(item)
		if err != nil {
			c.logger.Err(err).Msgf("fail to get block from RPC for height:%d", item)
			continue
		}
		txIn, err := c.extractTxs(b)
		if err != nil {
			c.logger.Err(err).Msgf("fail to extract txIn from block")
			continue
		}

		if len(txIn.TxArray) == 0 {
			continue
		}
		txIns = append(txIns, txIn)
	}
	return txIns, nil
}

// reConfirmTx will be kicked off only when chain client detected a re-org on bitcoin chain
// it will read through all the block meta data from local storage , and go through all the UTXOs.
// For each UTXO , it will send a RPC request to bitcoin chain , double check whether the TX exist or not
// if the tx still exist , then it is all good, if a transaction previous we detected , however doesn't exist anymore , that means
// the transaction had been removed from chain,  chain client should report to thorchain
func (c *Client) reConfirmTx() ([]int64, error) {
	blockMetas, err := c.temporalStorage.GetBlockMetas()
	if err != nil {
		return nil, fmt.Errorf("fail to get block metas from local storage: %w", err)
	}
	var rescanBlockHeights []int64
	for _, blockMeta := range blockMetas {
		var errataTxs []types.ErrataTx
		for _, txHash := range blockMeta.CustomerTransactions {
			if c.confirmTx(txHash) {
				c.logger.Info().Msgf("block height: %d, tx: %s still exist", blockMeta.Height, txHash)
				continue
			}
			// this means the tx doesn't exist in chain ,thus should errata it
			errataTxs = append(errataTxs, types.ErrataTx{
				TxID:  common.TxID(txHash),
				Chain: common.XHVChain,
			})
			// remove the UTXO from block meta , so signer will not spend it
			blockMeta.RemoveCustomerTransaction(txHash)
		}
		if len(errataTxs) > 0 {
			c.globalErrataQueue <- types.ErrataBlock{
				Height: blockMeta.Height,
				Txs:    errataTxs,
			}
		}
		// Let's get the block again to fix the block hash
		r, err := GetBlock(blockMeta.Height)
		if err != nil {
			c.logger.Err(err).Msgf("fail to get block verbose tx result: %d", blockMeta.Height)
		}
		if !strings.EqualFold(blockMeta.BlockHash, r.BlockHeader.Hash) {
			rescanBlockHeights = append(rescanBlockHeights, blockMeta.Height)
		}
		blockMeta.PreviousHash = r.BlockHeader.PrevHash
		blockMeta.BlockHash = r.BlockHeader.Hash
		if err := c.temporalStorage.SaveBlockMeta(blockMeta.Height, blockMeta); err != nil {
			c.logger.Err(err).Msgf("fail to save block meta of height: %d ", blockMeta.Height)
		}
	}
	return rescanBlockHeights, nil
}

func (c *Client) txInThePool(txHash string) bool {
	// get pool txs
	poolTxs, err := GetPoolTxs()
	if err != nil {
		c.logger.Err(err).Msgf("fail to get pool txs")
		return false
	}

	// check if the tx is in the pool.
	for _, tx := range poolTxs {
		if tx.Hash == txHash {
			return true
		}
	}
	return false
}

// confirmTx check a tx is valid on chain post reorg
func (c *Client) confirmTx(txHash string) bool {
	// first check if tx is in mempool, just signed it for example
	// if no error it means its valid mempool tx and move on
	if c.txInThePool(txHash) {
		return true
	}

	// then get raw tx and check if it has confirmations or not
	// if no confirmation and not in mempool then invalid
	txs, missedTxs, err := GetTxs([]string{txHash})
	if err != nil {
		c.logger.Err(err).Msgf("Error Getting Tx: %s", txHash)
		return false
	}
	// no such tx in the chain
	if len(missedTxs) > 0 {
		return false
	}

	// check if the tx has confirmations
	currentHeight, err := GetChainHeight()
	if err != nil {
		c.logger.Err(err).Msgf("Error getting current height")
		return false
	}
	if currentHeight > txs[0].BlockHeight {
		return true
	}

	return false
}

func (c *Client) isFromAsgardAndMember(pk common.PubKey) (bool, bool, error) {
	isAsgard := false
	isMember := false
	// get the vaults
	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		return isAsgard, isMember, fmt.Errorf("fail to get asgards : %w", err)
	}
	for _, v := range vaults {
		if v.PubKey == pk {
			isAsgard = true
			for _, m := range v.Membership {
				if m == c.nodePubKey.String() {
					isMember = true
					break
				}
			}
		}
	}

	return isAsgard, isMember, nil
}

// getTxOutHash has to return exactly the same hash hash
func (c *Client) getTxOutHash(tx ttypes.TxOutItem) string {
	coins := common.NewCoins(tx.Coin)
	str := fmt.Sprintf("%s|%s|%s|%s|%s|%s", tx.Chain, tx.ToAddress, tx.VaultPubKey, coins, tx.Memo, tx.InHash)
	return fmt.Sprintf("%X", sha256.Sum256([]byte(str)))
}

func (c *Client) scanTxOuts() {
	// save to be sent from FetchTxs()
	for {

		outs, err := c.bridge.GetOutboundTxs()
		if err != nil {
			c.logger.Err(err).Msg("fail to get outbound txs from thorchain")
		}

		if len(outs) > 0 {
			for _, o := range outs {
				if o.Chain != common.XHVChain {
					continue
				}

				// skip if we already have a witness saved for this out item.
				hash := c.getTxOutHash(o)
				c.signedTxOutLock.Lock()
				if _, ok := c.signedTxOut[hash]; ok {
					c.signedTxOutLock.Unlock()
					continue
				}
				c.signedTxOutLock.Unlock()

				var vaultType ttypes.VaultType = ttypes.VaultType_YggdrasilVault
				isAsgard, isMember, err := c.isFromAsgardAndMember(o.VaultPubKey)
				if err != nil {
					c.logger.Err(err).Msg("fail to get asgard info")
					continue
				}
				if isAsgard {
					vaultType = ttypes.VaultType_AsgardVault
				}

				// get the walletAddr for tx vault Pubkey
				vaultAddr, err := c.getAddrFromCndata(c.pkm.GetCnData(common.XHVChain, o.VaultPubKey))
				if err != nil {
					c.logger.Err(err).Msg("fail to get vaut adddress from tx vaultPubKey")
					continue
				}

				wtx := TxOutWitness{
					TxID:     "",
					TxKey:    "",
					Coin:     o.Coin,
					Receiver: o.ToAddress.String(),
					Sender:   vaultAddr.String(),
					Memo:     o.Memo,
					Vault:    vaultType,
					Signed:   isAsgard && isMember,
				}
				c.signedTxOutLock.Lock()
				c.signedTxOut[hash] = wtx
				c.signedTxOutLock.Unlock()
			}
		}

		if c.stopScanTxOuts.Load() {
			break
		}

		time.Sleep(3 * time.Second)
	}
}

func (c *Client) processOutgoing(tx *RawTx, height int64, inBlock bool) types.TxInItem {
	c.signedTxOutLock.Lock()
	defer c.signedTxOutLock.Unlock()

	// delete the old signed txOuts
	for k, v := range c.signedTxOutDelete {
		if height >= v {
			delete(c.signedTxOut, k)
			delete(c.signedTxOutDelete, k) // this operation is surprisingly safe in golang. :).
		}
	}

	var txInItem types.TxInItem
	var txOutHash string

	// check for outgoing txs that we submitted
	for h, w := range c.signedTxOut {
		if len(w.TxID) == 0 {
			// query tc to get the txkey for this tx
			observedTxs, err := c.bridge.GetTx(tx.Hash)
			if err != nil {
				c.logger.Err(err).Msg("error getting witness tx from thorhcain")
				continue
			}
			if observedTxs == nil {
				// this is expected here till the node that signed the
				// outgoing tx send it is witness to thorhcain.
				continue
			}
			tx := observedTxs[0].Tx

			// check whether this witness is for this tx
			if tx.FromAddress.String() == w.Sender && tx.ToAddress.String() == w.Receiver && tx.Coins[0].Equals(w.Coin) && tx.Memo == w.Memo {
				w.TxID = strings.ToLower(tx.ID.String())
				w.TxKey = tx.TxKey
				c.signedTxOut[h] = w
			} else {
				continue
			}
		}

		// check whether this witness is for this tx
		if w.TxID != tx.Hash || len(w.TxKey) == 0 {
			continue
		}

		// check if we can verifty the txIn if we are not the sender
		verified := false
		if (w.Vault == ttypes.VaultType_YggdrasilVault && w.Sender != c.walletAddr.String()) ||
			(w.Vault == ttypes.VaultType_AsgardVault && !w.Signed) {

			c.clientLock.Lock()
			respCheck, err := c.client.CheckTxKey(&wallet.RequestCheckTxKey{
				TxID:    w.TxID,
				TxKey:   w.TxKey,
				Address: w.Receiver, // it is important to take this from our own cache because sender might change it.
			})
			c.clientLock.Unlock()
			if err != nil {
				c.logger.Error().Msgf("error checking for txkey for tx %s, err: %v", tx.Hash, err)
				break
			}

			// we expect that address to receive only 1 asset type, thus length of ReceivedAmounts should be 1
			if len(respCheck.ReceivedAmounts) == 1 && len(respCheck.ReceivedAssets) == 1 &&
				(respCheck.ReceivedAmounts[0]/DecimalDiff) == w.Coin.Amount.Uint64() &&
				respCheck.ReceivedAssets[0] == w.Coin.Asset.Ticker.String() {
				verified = true
				c.logger.Info().Msgf("We were able to verify the witness tx. Sending to Thorhcain")
			}
		}

		if (w.Vault == ttypes.VaultType_YggdrasilVault && w.Sender == c.walletAddr.String()) ||
			(w.Vault == ttypes.VaultType_AsgardVault && w.Signed) || verified {
			txInItem = types.TxInItem{
				BlockHeight: height,
				Tx:          w.TxID,
				TxKey:       w.TxKey,
				Memo:        w.Memo,
				Sender:      w.Sender,
				To:          w.Receiver,
				Coins:       []common.Coin{w.Coin},
				Gas: common.Gas{
					common.NewCoin(w.Coin.Asset, cosmos.NewUint(tx.RctSignatures.TxnFee/DecimalDiff)),
				},
			}
			txOutHash = h
			break
		}
	}

	// dont delete the witness if the tx is still in the mempool
	// we will need the same witness when the tx is in a block.
	// save and delete it in the next block since, it might get re-saved in scanTxOuts()
	// since it takes a while thornode to remove it from endpoint.
	if !txInItem.IsEmpty() && inBlock {
		c.signedTxOutDelete[txOutHash] = height + 1
	}

	return txInItem
}

// extractTxs extracts txs from a block to type TxIn
func (c *Client) extractTxs(block Block) (types.TxIn, error) {
	// prepare the TxIn
	txIn := types.TxIn{
		Chain: c.GetChain(),
	}

	// get txs from daemon, we aren't interested if a given hash can't be found here.
	txs, _, err := GetTxs(block.TxHashes)
	if err != nil {
		return txIn, fmt.Errorf("failed to get txs from daemon: %w", err)
	}

	// populate txItems
	var txItems []types.TxInItem
	for i, tx := range txs {
		// remove from pool cache
		c.removeFromMemPoolCache(tx.Hash)

		// get txInItem
		inBlock := true
		txInItem, err := c.getTxIn(&txs[i], int64(block.BlockHeader.Height), inBlock)
		if err != nil {
			c.logger.Err(err).Msg("fail to get TxInItem")
			continue
		}
		if txInItem.IsEmpty() {
			c.logger.Info().Msgf("ignoring Tx with empty TxInItem: %s", tx.Hash)
			continue
		}

		// append to txItems
		txItems = append(txItems, txInItem)
	}
	txIn.TxArray = txItems
	txIn.Count = strconv.Itoa(len(txItems))
	return txIn, nil
}

func (c *Client) getTxIn(tx *RawTx, height int64, inBlock bool) (types.TxInItem, error) {
	// check whether it is a outgoing tx from us first
	txInTem := c.processOutgoing(tx, height, inBlock)
	if !txInTem.IsEmpty() {
		where := ""
		if inBlock {
			where = "block"
		} else {
			where = "pool"
		}
		c.logger.Info().Msgf("We found tx with hash %s that we sent in the %s. Sending withness from cache.", txInTem.Tx, where)
		return txInTem, nil
	}

	// parse tx extra
	parsedTxExtra, err := c.parseTxExtra(tx.Extra)
	if err != nil {
		return types.TxInItem{}, fmt.Errorf("error Parsing Tx Extra: %w", err)
	}

	// get tx public key
	var txPubKey [32]byte
	if len(parsedTxExtra[1]) != 1 {
		c.logger.Info().Msg("ignoring a tx with more than one tx key")
		return types.TxInItem{}, nil
	}
	copy(txPubKey[:], parsedTxExtra[1][0][0:32])

	// get the output
	output, err := c.getOutput(tx, &txPubKey)
	if err != nil {
		return types.TxInItem{}, fmt.Errorf("error decrypting tx output: %w", err)
	}
	if output == (TxVout{}) {
		// we couldn't decode any output from this tx. Which means we don't own any anyway.
		return types.TxInItem{}, nil
	}
	fee := tx.RctSignatures.TxnFee / DecimalDiff

	// get the coins
	asset, err := common.NewAsset("XHV." + output.Asset)
	if err != nil {
		return types.TxInItem{}, fmt.Errorf("ignoring a tx with invalid asset type: %w", err)
	}
	coins := common.Coins{
		common.Coin{
			Asset:    asset,
			Amount:   cosmos.NewUint(output.Amount / DecimalDiff),
			Decimals: int64(12),
		},
	}

	// get the memo
	memoStr := ""
	if val, ok := parsedTxExtra[0x18]; ok {
		memoStr = string(val[0])
	} else {
		c.logger.Info().Msg("ignoring a tx we could not parse the memo")
		return types.TxInItem{}, nil
	}

	// check it is a valid memo
	memo, err := mem.ParseMemo(common.LatestVersion, memoStr)
	if err != nil {
		return types.TxInItem{}, fmt.Errorf("ignoring a tx with invalid memo: %w", err)
	}

	// retrieve the sender address
	var sender string
	if memo.GetType() == mem.TxAdd {
		sender = memo.GetSender().String()
	} else {
		// dummy address
		chainNetwork := common.GetCurrentChainNetwork()
		switch chainNetwork {
		case common.MockNet, common.MainNet:
			sender = "hvxy6EXfx8fMxuiy71rNXD1DZA1he33dB2VqZKPZFqVtRvE1AUDMo865iY96CwpDMrDF5NcQ6FumH4kTmZej5cRn5scZCjb4r8"
		case common.TestNet:
			sender = "hvta8SzCkbTLNFDeJcRL51FrTci7YwGzLGHwJYukhCVAivU43WQVFde9c6n6WmZ8y5fDH3SwajsS5Yy3HUcfFCm63uF1eNspce"
		case common.StageNet:
			sender = "hvsaePXufZF2ny3fZ2khsQan33mzeoxyGRMyDWW2KUWZMZFP9pm8cuuQPxNu4XAPZXDo399D2dno9ipGxccoAk3raUcsCf9TBb2"
		}
	}

	// all incomings must have a sender. All outgoings + internals should  be filtered in processOutgoing
	if len(sender) == 0 {
		return types.TxInItem{}, fmt.Errorf("ignoring a tx we cant know the sender")
	}

	// check unlock time is 0(default 10 block)
	if tx.OutputUnlockTimes[output.ind] != 0 {
		return types.TxInItem{}, fmt.Errorf("ignoring a tx with non-default unlock time")
	}

	return types.TxInItem{
		BlockHeight: height,
		Tx:          tx.Hash,
		Sender:      sender,
		To:          output.Address,
		Coins:       coins,
		Memo:        memoStr,
		Gas: common.Gas{
			common.Coin{
				Asset:    asset,
				Amount:   cosmos.NewUint(fee),
				Decimals: int64(12),
			},
		},
	}, nil
}

func (c *Client) getOutput(tx *RawTx, txPubKey *[32]byte) (TxVout, error) {
	// get all vaults keys and check for outputs
	asgards, err := c.bridge.GetAsgards()
	if err != nil {
		return TxVout{}, fmt.Errorf("failed to get the asgard vaults: %w", err)
	}
	for _, asgard := range asgards {
		if len(asgard.Cryptonote) == 0 {
			c.logger.Info().Msg("the vault cryptonote data is empty")
			continue
		}

		// get the privViewKey and pubSpendKey of the vault
		privViewKey, pubSpendKey, err := c.decodeCnData(asgard.Cryptonote)
		if err != nil {
			return TxVout{}, fmt.Errorf("error decoding cryotonote data for vault: %s : %w", asgard.PubKey, err)
		}

		sharedSecretYgg, err := crypto.GenerateKeyDerivation(txPubKey, &privViewKey)
		if err != nil {
			return TxVout{}, fmt.Errorf("error creating shared secret: %w", err)
		}

		for ind, vout := range tx.Vout {

			var targetKey [32]byte
			assetType := ""
			if len(vout.Target.Key) != 0 {
				targetRaw, _ := hex.DecodeString(vout.Target.Key)
				copy(targetKey[:], targetRaw)
				assetType = "XHV"
			}
			if len(vout.Target.Offshore) != 0 {
				targetRaw, _ := hex.DecodeString(vout.Target.Offshore)
				copy(targetKey[:], targetRaw)
				assetType = "XUSD"
			}
			if len(vout.Target.Xasset) != 0 {
				targetRaw, _ := hex.DecodeString(vout.Target.Xasset)
				copy(targetKey[:], targetRaw)
				assetType = vout.Target.AssetType
			}
			if len(assetType) == 0 {
				c.logger.Info().Msgf("invalid tx output found, skipping..")
				continue
			}

			// derive the spent key
			derivedPublicSpendKeyYgg, err := crypto.SubSecretFromTarget((*sharedSecretYgg)[:], uint64(ind), &targetKey)
			if err != nil {
				return TxVout{}, fmt.Errorf("error deriving ygg public spend Key: %w", err)
			}

			found := false
			if *derivedPublicSpendKeyYgg == pubSpendKey {
				found = true
				c.logger.Info().Msgf("found an output belongs to vault = %s", asgard.PubKey)
			}

			if found {
				// decode the tx amount and mask
				scalar := crypto.DerivationToScalar((*sharedSecretYgg)[:], uint64(ind))
				ecdhInfo := crypto.EcdhDecode(tx.RctSignatures.EcdhInfo[ind], *scalar)

				// Calculate the amount commitment from decoded ecdh info
				var C, Ctmp [32]byte
				success := crypto.AddKeys2(&Ctmp, ecdhInfo.Mask, ecdhInfo.Amount, crypto.H)

				if success {
					Craw, _ := hex.DecodeString(tx.RctSignatures.OutPk[ind])

					// check if the provided output commitment mathces with the one we calculated
					copy(C[:], Craw)
					if crypto.EqualKeys(C, Ctmp) {
						// we can ignnore the error because we know cn data is legit at this point.
						addr, _ := c.getAddrFromCndata(asgard.Cryptonote)
						// We can just skip the rest of the outputs and return here because we expect we only own 1 output
						return TxVout{
							Address: addr.String(),
							Amount:  crypto.H2d(ecdhInfo.Amount),
							Asset:   assetType,
							ind:     ind,
						}, nil
					} else {
						c.logger.Info().Msgf("invalid commitment for ouptut = %d  of tx %s skipiing..", ind, tx.Hash)
					}
				} else {
					c.logger.Info().Msgf("calculation of the commitment failed for output index = %d of tx %s skipping..", ind, tx.Hash)
				}
			}
		}
	}

	c.logger.Info().Msgf("returning empty tx vout")
	return TxVout{}, nil
}

func (c *Client) getAddrFromCndata(cnData string) (common.Address, error) {
	if len(cnData) == 0 {
		return common.NoAddress, fmt.Errorf("can not get address from empty cn data")
	}
	walletAddr, err := common.PubKey(cnData).GetAddress(common.XHVChain)
	if err != nil {
		return "", fmt.Errorf("failed to get the wallet address: %w", err)
	}
	return walletAddr, nil
}

func (c *Client) decodeCnData(cnData string) (privViewKey [32]byte, pubSpendKey [32]byte, err error) {
	if len(cnData) == 0 {
		return privViewKey, pubSpendKey, fmt.Errorf("empty cryptonote data")
	}
	asByte, err := hex.DecodeString(cnData)
	if err != nil {
		return privViewKey, pubSpendKey, err
	}
	copy(privViewKey[:], asByte[:32])
	copy(pubSpendKey[:], asByte[32:])
	return
}

func (c *Client) removeFromMemPoolCache(hash string) {
	c.memPoolLock.Lock()
	defer c.memPoolLock.Unlock()
	delete(c.processedMemPool, hash)
}

func (c *Client) tryAddToMemPoolCache(hash string) bool {
	if c.processedMemPool[hash] {
		return false
	}
	c.memPoolLock.Lock()
	defer c.memPoolLock.Unlock()
	c.processedMemPool[hash] = true
	return true
}

// FetchMemPool retrieves txs from mempool
func (c *Client) FetchMemPool(height int64) (types.TxIn, error) {
	// make sure client doesn't scan mempool too much
	diff := time.Since(c.lastMemPoolScan)
	if diff < constants.ThorchainBlockTime {
		return types.TxIn{}, nil
	}
	c.lastMemPoolScan = time.Now()
	return c.getMemPool(height)
}

func (c *Client) getMemPool(height int64) (types.TxIn, error) {
	txs, err := GetPoolTxs()
	if err != nil {
		return types.TxIn{}, fmt.Errorf("fail to get tx hashes from mempool: %w", err)
	}
	txIn := types.TxIn{
		Chain:   c.GetChain(),
		MemPool: true,
	}
	for i, tx := range txs {
		// this hash had been processed before , ignore it
		if !c.tryAddToMemPoolCache(tx.Hash) {
			c.logger.Debug().Msgf("%s had been processed , ignore", tx.Hash)
			continue
		}

		c.logger.Debug().Msgf("process hash %s", tx.Hash)
		inBlock := false
		txInItem, err := c.getTxIn(&txs[i], height, inBlock)
		if err != nil {
			c.logger.Error().Err(err).Msg("fail to get TxInItem")
			continue
		}
		if txInItem.IsEmpty() {
			continue
		}
		txIn.TxArray = append(txIn.TxArray, txInItem)
	}
	txIn.Count = strconv.Itoa(len(txIn.TxArray))
	return txIn, nil
}

// SignTx is going to generate the outbound transaction, and also sign it
func (c *Client) SignTx(tx types.TxOutItem, thorchainHeight int64) ([]byte, error) {
	c.signingOrKeygenInProgress.Store(true)
	defer c.signingOrKeygenInProgress.Store(false)
	c.asgardNamesLock.Lock()
	keygenHeight := c.asgardNames[tx.VaultPubKey]
	c.asgardNamesLock.Unlock()

	// check if the chain is correct
	if !tx.Chain.Equals(common.XHVChain) {
		return nil, errors.New("tx is not for XHV chain")
	}

	if tx.ToAddress.IsEmpty() {
		return nil, fmt.Errorf("to address is empty")
	}
	if tx.VaultPubKey.IsEmpty() {
		return nil, fmt.Errorf("vault public key is empty")
	}

	if len(tx.Memo) == 0 {
		return nil, fmt.Errorf("can't sign tx when it doesn't have memo")
	}

	memo, err := mem.ParseMemo(common.LatestVersion, tx.Memo)
	if err != nil {
		return nil, fmt.Errorf("fail to parse memo(%s):%w", tx.Memo, err)
	}
	if memo.IsInbound() {
		return nil, fmt.Errorf("inbound memo should not be used for outbound tx")
	}

	// get the walletAddr for tx vault Pubkey
	vaultAddr, err := c.getAddrFromCndata(c.pkm.GetCnData(common.XHVChain, tx.VaultPubKey))
	if err != nil {
		return nil, fmt.Errorf("fail to get vaut adddress from tx vaultPubKey: %w", err)
	}

	// get the amount asset type
	if len(tx.Coins) != 1 {
		return nil, fmt.Errorf("can't sing tx: Haven doesn't support sending multiple asset types in a single transaction")
	}
	amount := tx.Coins[0].Amount.Uint64() * DecimalDiff
	outputAsset := tx.Coins[0].Asset.Ticker.String()

	// set the tx
	dst := wallet.Destination{
		Amount:  amount,
		Address: tx.ToAddress.String(),
	}
	t := wallet.RequestTransfer{
		Destinations:  []*wallet.Destination{&dst},
		GetTxHex:      true,
		GetTxKey:      true,
		GetTxMetadata: true,
		RingSize:      11,
		Memo:          tx.Memo,
		AssetType:     outputAsset,
	}
	var txID string
	var txKey string
	var res []byte
	sign := false
	var vaultType ttypes.VaultType = ttypes.VaultType_YggdrasilVault
	if c.isYggdrasil(tx.VaultPubKey) {
		// we are the one who signing this tx.
		c.logger.Info().Msgf("Creating an outbound tx from yggrasil Amount: %d %s for %s", amount, outputAsset, tx.ToAddress.String())

		// try to login wallet
		if err := c.loginToWallet(tx.VaultPubKey, true); err != nil {
			return nil, fmt.Errorf("fail to login to wallet: %w", err)
		}

		// sign tx
		c.clientLock.Lock()
		signedTx, err := c.client.Transfer(&t)
		if err != nil {
			return nil, fmt.Errorf("fail to make a outgoing transaction: %w", err)
		}
		c.clientLock.Unlock()

		res, err = hex.DecodeString(signedTx.TxBlob)
		if err != nil {
			return nil, err
		}
		txID = signedTx.TxHash
		txKey = signedTx.TxKey
	} else {

		isAsgard, isMember, err := c.isFromAsgardAndMember(tx.VaultPubKey)
		if err != nil {
			return nil, err
		}

		if isAsgard {
			vaultType = ttypes.VaultType_AsgardVault
		}

		if isMember {
			sign = true
			// close any open wallet, since MSS will try to login itself.
			c.clientLock.Lock()
			err = c.client.CloseWallet()
			c.clientLock.Unlock()
			if err != nil {
				return nil, err
			}

			c.logger.Info().Msgf("Creating an outbound from asgard tx Amount: %d %s for %s", amount, outputAsset, tx.ToAddress.String())
			// tss sign
			msg, err := json.Marshal(t)
			if err != nil {
				return nil, err
			}
			txKey, txID, err = c.tssKm.RemoteSignCryptonote(msg, tx.VaultPubKey.String(), keygenHeight, c.cfg.WalletRPCHost)
			// login back to closed wallet above.
			err2 := c.loginToWallet(tx.VaultPubKey, true)
			if err != nil {
				return nil, err
			}
			// we dont care about login error if the signing was successsfull.
			// it will retried when necessary anyways.
			if err2 != nil {
				return nil, nil
			}

			// at this point we expect RemoteSignCryptonote() to complete tx construction and submit to haven daemon.
			// as well check whether it is legit or not.
		}
	}

	// save to be processed as outgoing
	c.signedTxOutLock.Lock()
	defer c.signedTxOutLock.Unlock()
	wtx := TxOutWitness{
		TxID:     txID,
		TxKey:    txKey,
		Coin:     tx.Coins[0],
		Receiver: tx.ToAddress.String(),
		Sender:   vaultAddr.String(),
		Memo:     tx.Memo,
		Vault:    vaultType,
		Signed:   sign,
	}
	// this intentionally overwrites the version of witness we get in from scanTxOuts()
	// to add it the tx key and the id.
	c.signedTxOut[tx.Hash()] = wtx

	return res, nil
}

// BroadcastTx will broadcast the given payload to XHV chain
func (c *Client) BroadcastTx(txOut types.TxOutItem, payload []byte) (string, error) {
	// get txOut hash
	c.signedTxOutLock.Lock()
	txHash := c.signedTxOut[txOut.Hash()].TxID
	c.signedTxOutLock.Unlock()
	if len(txHash) == 0 {
		return "", fmt.Errorf("can't broadcast a tx without tx hash")
	}
	if len(payload) == 0 {
		return "", fmt.Errorf("can't broadcast an empty tx")
	}

	// get the block meta
	height, err := c.GetHeight()
	if err != nil {
		return "", fmt.Errorf("fail to get block height: %w", err)
	}
	bm, err := c.temporalStorage.GetBlockMeta(height)
	if err != nil {
		c.logger.Err(err).Msgf("fail to get blockmeta for heigth: %d", height)
	}
	if bm == nil {
		bm = utxo.NewBlockMeta("", height, "")
	}
	defer func() {
		if err := c.temporalStorage.SaveBlockMeta(height, bm); err != nil {
			c.logger.Err(err).Msg("fail to save block metadata")
		}
	}()

	// broadcast tx if we signed it. If it we did mss, it already broadcasted
	// and BroadcastTx should not be called anyways since payload will be empty in that case.
	resp := SendRawTransaction(hex.EncodeToString(payload))
	if resp.Status != "OK" {
		return "", fmt.Errorf("fail to broadcast transaction to XHV chain: %s", resp.Reason)
	}

	// save tx id to block meta in case we need to errata later
	bm.AddSelfTransaction(txHash)
	c.logger.Info().Str("hash", txHash).Msg("broadcast to XHV chain successfully")
	return txHash, nil
}

func (c *Client) reportSolvency(blockHeight int64) error {
	asgardVaults, err := c.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards,err: %w", err)
	}
	for _, asgard := range asgardVaults {
		acct, err := c.GetAccount(asgard.PubKey, nil)
		if err != nil {
			c.logger.Err(err).Msgf("fail to get account balance")
			continue
		}
		select {
		case c.globalSolvencyQueue <- types.Solvency{
			Height: blockHeight,
			Chain:  common.XHVChain,
			PubKey: asgard.PubKey,
			Coins:  acct.Coins,
		}:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msgf("fail to send solvency info to THORChain, timeout")
		}
	}
	return nil
}

// isYggdrasil - when the pubkey and node pubkey is the same that means it is signing from yggdrasil
func (c *Client) isYggdrasil(key common.PubKey) bool {
	return key.Equals(c.nodePubKey)
}

func (c *Client) parseTxExtra(extra []byte) (map[byte][][]byte, error) {
	parsedTxExtra := make(map[byte][][]byte)
	len := len(extra)
	for ind := 0; ind < len; ind++ {
		switch extra[ind] {
		case 0:
			// Padding
			ind++
			ind += int(extra[ind])
		case 1:
			// Pubkey - 32 byte key (fixed length)
			if len-ind <= 32 {
				return nil, fmt.Errorf("tx pubKey has insufficient length")
			}
			ba := extra[ind+1 : ind+33]
			parsedTxExtra[0x01] = append(parsedTxExtra[0x01], ba)
			ind += 32
		case 2:
			// Nonce
			ind++
			ind += int(extra[ind])
		case 3:
			// Merge mining key
			ind += 40
		case 4:
			// Additional pubkeys
			ind++
			ind += 32 * int(extra[ind])
		case 0xde:
			// miner gate tag
			ind++
			ind += int(extra[ind])
		case 0x17:
			// Offshore data
			ind++
			ind += int(extra[ind])
		case 0x18:
			// Thorchain memo data
			ind++
			length := int(extra[ind])
			if len-ind <= length {
				return nil, fmt.Errorf("thorchain memo data has insufficient length")
			}
			var ba []byte
			if length > 127 {
				// we have 1 extra byte between the actual data an the length byte hence (ind + 2)
				ba = extra[ind+2 : ind+2+length]
				ind += length + 1
			} else {
				ba = extra[ind+1 : ind+1+length]
				ind += length
			}
			parsedTxExtra[0x18] = append(parsedTxExtra[0x18], ba)
		default:
			c.logger.Info().Msgf("err ind: %d char: %d", ind, extra[ind])
			return nil, fmt.Errorf("unknown tx extra tag. Parse Failed")
		}
	}

	return parsedTxExtra, nil
}
