package cardano

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

type Client struct {
	logger zerolog.Logger
	config config.BifrostChainConfiguration

	n2c                 ClientWrapper
	ttl                 uint64
	bridge              thorclient.ThorchainBridge
	signerCacheManager  *signercache.CacheManager
	storage             *blockscanner.BlockScannerStorageCardano
	blockScanner        *blockscanner.BlockScanner
	chainScanner        *Scanner
	tssKeyManager       tss.ThorchainKeyManager
	localKey            ed25519.PrivateKey
	localAddress        string
	globalSolvencyQueue chan types.Solvency
	globalNetworkQueue  chan common.NetworkFee
	wg                  *sync.WaitGroup
	stopchan            chan struct{}
}

func NewClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	thorchainBridge thorclient.ThorchainBridge,
	metrics *metrics.Metrics,
) (*Client, error) {
	if thorchainBridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}
	c := &Client{
		bridge:   thorchainBridge,
		logger:   log.With().Str("module", cfg.ChainID.String()).Logger(),
		config:   cfg,
		wg:       &sync.WaitGroup{},
		stopchan: make(chan struct{}),
	}

	var err error

	c.tssKeyManager, err = tss.NewKeySign(server, thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("failed to create tss signer: %w", err)
	}

	privKey, err := thorKeys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("fail to get local testing key: %w", err)
	}

	c.localKey = privKey.Key

	address, err := AddressFromBytes(privKey.PubKey().Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to create address: %w", err)
	}

	c.localAddress = address.String()

	var path string // if not set later, will in memory db
	if len(cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", cfg.BlockScanner.DBPath, cfg.BlockScanner.ChainID)
	}

	c.storage, err = blockscanner.NewBlockScannerStorageCardano(path, cfg.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scan db: %w", err)
	}

	c.signerCacheManager, err = signercache.NewSignerCacheManager(
		c.storage.GetInternalDb(),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache: %w", err)
	}

	// create n2c connection
	c.n2c, err = NewClientConnection(c.logger, cfg.APIHost)
	if err != nil {
		c.logger.Err(err).Msg("ouroboros network error")
		return nil, fmt.Errorf("fail to create client connection: %w", err)
	}

	// get slot coefficient
	var genesisConfig *localstatequery.GenesisConfigResult
	genesisConfig, err = c.n2c.GetGenesisConfig()
	if err != nil {
		return nil, fmt.Errorf("fail to get genesis config: %w", err)
	}

	coefficient := uint64(20)
	if len(genesisConfig.ActiveSlotsCoeff) == 2 {
		num, ok1 := genesisConfig.ActiveSlotsCoeff[0].(uint64)
		den, ok2 := genesisConfig.ActiveSlotsCoeff[1].(uint64)

		if !ok1 || !ok2 || num == 0 {
			c.logger.Warn().
				Uint64("default", coefficient).
				Msg("failed to calculate slot coefficient, using default")
		} else {
			coefficient = (num + den - 1) / num
		}
	}

	c.ttl = coefficient * transactionTimeout

	c.logger.Info().Msgf("creating blockscanner")

	c.chainScanner, err = NewScanner(
		c.logger,
		cfg.BlockScanner,
		c.bridge,
		c.storage,
		cfg.APIHost,
		cfg.RPCHost,
	)

	if err != nil {
		return nil, fmt.Errorf("fail to create cardano scanner: %w", err)
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(
		cfg.BlockScanner,
		c.storage,
		metrics,
		thorchainBridge,
		c.chainScanner,
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create block blockScanner: %w", err)
	}

	return c, nil
}

func (c *Client) Start(
	globalTxsQueue chan types.TxIn,
	globalErrataQueue chan types.ErrataBlock,
	globalSolvencyQueue chan types.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.globalSolvencyQueue = globalSolvencyQueue
	c.chainScanner.globalNetworkFeeQueue = globalNetworkFeeQueue
	c.chainScanner.globalErrataQueue = globalErrataQueue

	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.tssKeyManager.Start()

	c.wg.Add(1)
	go runners.SolvencyCheckRunner(
		c.GetChain(),
		c,
		c.bridge,
		c.stopchan,
		c.wg,
		constants.ThorchainBlockTime,
	)
}

func (c *Client) Stop() {
	c.tssKeyManager.Stop()
	c.blockScanner.Stop()
	c.n2c.Close()
	err := c.chainScanner.Stop()
	if err != nil {
		c.logger.Err(err).Msg("failed to stop cardano scanner")
	}
	close(c.stopchan)
	c.wg.Wait()
}

func (c *Client) BroadcastTx(txOutItem types.TxOutItem, txBytes []byte) (string, error) {
	tx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to decode transaction: %w", err)
	}

	if len(tx.Outputs()) < 1 || len(tx.Outputs()) > 2 {
		return "", fmt.Errorf("invalid number of transaction outputs: %d", len(tx.Outputs()))
	}

	err = c.n2c.SubmitTx(ledger.TxTypeConway, txBytes)
	if err != nil {
		if strings.Contains(err.Error(), "probably already been included") {
			c.logger.Info().Msg("transaction already sent by different node")
		} else {
			return "", fmt.Errorf("failed to submit transaction: %w", err)
		}
	}

	txHash := tx.Hash().String()

	var change uint64
	if len(tx.Outputs()) == 2 {
		change = tx.Outputs()[1].Amount().Uint64()
	}

	c.logger.Info().
		Str("hash", txHash).
		Uint64("amount", tx.Outputs()[0].Amount().Uint64()).
		Uint64("change", change).
		Uint64("fee", tx.Fee().Uint64()).
		Msg("BroadcastTx")

	err = c.signerCacheManager.SetSigned(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(c.GetChain()),
		txHash,
	)
	if err != nil {
		c.logger.Err(err).
			Interface("tx_out_item", txOutItem).
			Msg("failed to mark tx out item as signed")
	}

	return txHash, nil
}

func (c *Client) ConfirmationCountReady(txIn types.TxIn) bool {
	if len(txIn.TxArray) == 0 {
		return true
	}

	required := txIn.ConfirmationRequired
	c.logger.Info().
		Int64("height", txIn.TxArray[0].BlockHeight).
		Int64("required", required).
		Int("transactions", len(txIn.TxArray)).
		Msg("pending confirmations")

	currentHeight, err := c.GetHeight()
	if err != nil {
		c.logger.Err(err).Msg("failed to get current height")
		return false
	}

	return currentHeight-txIn.TxArray[0].BlockHeight >= required
}

func (c *Client) GetAccount(pubkey common.PubKey, height *big.Int) (common.Account, error) {
	addr, err := pubkey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).
			Str("pub_key", pubkey.String()).
			Msg("failed to get address")
		return common.Account{}, err
	}
	return c.GetAccountByAddress(addr.String(), height)
}

func (c *Client) getUtxosByAddress(
	address string,
	height *big.Int,
) ([]Utxo, error) {
	addr, err := ledger.NewAddress(address)
	if err != nil {
		return nil, fmt.Errorf("failed to create address: %w", err)
	}

	var point pcommon.Point

	var slot uint64
	var hash []byte

	if height != nil {
		_, slot, hash, err = c.storage.GetBlockId(height.Uint64())
		if err != nil {
			c.logger.Err(err).
				Uint64("height", height.Uint64()).
				Msg("failed to get block id for height, falling back to tip")
			return nil, err
		}

		point = pcommon.NewPoint(slot, hash)
	}

	if point.Hash == nil {
		_, slot, hash, err = c.storage.GetLastBlockId()
		if err != nil {
			return nil, err
		}
		point = pcommon.NewPoint(slot, hash)
	}

	result, err := c.n2c.GetUTxOByAddress([]ledger.Address{addr}, &point)
	if err != nil {
		return nil, fmt.Errorf("failed to get UTxO by address: %w", err)
	}

	var utxos []Utxo

	for id, output := range result.Results {
		key := getUtxoKey(id.Hash.Bytes(), uint32(id.Idx))

		var found bool
		found, err = c.storage.HasSpentUtxo(key)
		if err != nil {
			c.logger.Warn().Err(err).Msg("failed to check for spent utxo")
			continue
		}

		if found {
			continue
		}

		utxos = append(utxos, Utxo{
			Hash:   id.Hash.String(),
			Index:  id.Idx,
			Amount: output.Amount().Uint64(),
		})
	}

	return utxos, nil
}

func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	account := common.Account{}

	utxos, err := c.getUtxosByAddress(address, height)
	if err != nil {
		c.logger.Err(err).Msg("failed to get utxos by address")
		return account, err
	}

	total := uint64(0)
	for _, utxo := range utxos {
		total += utxo.Amount
	}

	decimals := c.config.ChainID.GetGasAssetDecimal()

	account.Coins = []common.Coin{{
		Asset:    common.ADAAsset,
		Amount:   sdkmath.NewUint(total * 100),
		Decimals: decimals,
	}}

	return account, nil
}

func (c *Client) GetAddress(pubkey common.PubKey) string {
	addr, err := pubkey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).Str("pub_key", pubkey.String()).Msg("fail to get address")
		return ""
	}
	return addr.String()
}

func (c *Client) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

// RollbackBlockScanner rolls back the block blockScanner to the last observed block
func (c *Client) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

func (c *Client) GetChain() common.Chain {
	return c.config.ChainID
}

func (c *Client) GetConfig() config.BifrostChainConfiguration {
	return c.config
}

func (c *Client) GetConfirmationCount(txIn types.TxIn) int64 {
	if len(txIn.TxArray) == 0 {
		return 0
	}

	//  n2c call may be slow, may also query leveldb if chain-sync is current
	tip, err := c.n2c.GetChainBlockNo()
	if err != nil {
		c.logger.Err(err).Msg("failed to get current tip")
		return 0
	}
	var height int64
	for _, tx := range txIn.TxArray {
		if tx.BlockHeight > height {
			height = tx.BlockHeight
		}
	}
	return tip - height
}

// GetHeight returns the current height of the chain.
// Implements both ChainClient and BlockScannerFetcher interfaces.
func (c *Client) GetHeight() (int64, error) {
	return c.chainScanner.GetHeight()
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(
		types.InboundCacheKey(vault, c.GetChain().String()),
	)
	if err != nil {
		return "", "", err
	}
	lastBroadCasted, err := c.signerCacheManager.GetLatestRecordedTx(
		types.BroadcastCacheKey(vault, c.GetChain().String()),
	)
	return lastObserved, lastBroadCasted, err
}

func (c *Client) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

func (c *Client) OnObservedTxIn(txIn types.TxInItem, _ int64) {
	m, err := mem.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// Debug log only as ParseMemo error is expected for THORName inbounds.
		c.logger.Debug().Err(err).Str("memo", txIn.Memo).Msg("fail to parse memo")
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

func (c *Client) SignTx(txOutItem types.TxOutItem, _ int64) ([]byte, []byte, *types.TxInItem, error) {
	if c.signerCacheManager.HasSigned(txOutItem.CacheHash()) {
		c.logger.Info().
			Interface("tx", txOutItem).
			Msg("transaction already signed, ignoring...")
		return nil, nil, nil, nil
	}

	coin := txOutItem.Coins.GetCoin(common.ADAAsset)

	if coin.IsEmpty() {
		err := fmt.Errorf("no ADA coins found")
		c.logger.Err(err).Interface("coins", txOutItem.Coins).Msg("")
		return nil, nil, nil, err
	}

	fromAddress, err := ledger.NewAddress(c.GetAddress(txOutItem.VaultPubKeyEddsa))
	if err != nil {
		c.logger.Err(err).Msg("failed to get address")
		return nil, nil, nil, err
	}

	toAddress, err := ledger.NewAddress(txOutItem.ToAddress.String())
	if err != nil {
		c.logger.Err(err).Msg("failed to get address")
		return nil, nil, nil, err
	}

	utxos, err := c.getUtxosByAddress(fromAddress.String(), nil)
	if err != nil {
		c.logger.Err(err).Msg("failed to get utxos by address")
		return nil, nil, nil, err
	}

	currentHeight, err := c.GetHeight()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get current height")
	}

	minFeeA, minFeeB := c.chainScanner.GetNetworkMinFees()

	maxFee := uint64(500_000)
	if !txOutItem.MaxGas.IsEmpty() {
		coins := txOutItem.MaxGas.ToCoins()
		maxFee = coins[0].Amount.Uint64() / 100
	}

	tx, err := CreateConwayTx(
		fromAddress,
		toAddress,
		coin.Amount.Uint64()/100, // 1e8 => 1e6
		minFeeA,
		minFeeB,
		maxFee,
		txOutItem.Memo,
		utxos,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create conway tx: %w", err)
	}

	_, slot, _, err := c.storage.GetLastBlockId()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get last block id: %w", err)
	}

	tx.Body.Ttl = slot + c.ttl

	encoded, err := cbor.Encode(tx.Body)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to encode transaction: %w", err)
	}

	hash := lcommon.Blake2b256Hash(encoded)

	signature, err := c.sign(hash[:], txOutItem)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	var pubKey cryptotypes.PubKey
	pubKey, err = cosmos.GetPubKeyFromBech32(
		cosmos.Bech32PubKeyTypeAccPub,
		txOutItem.VaultPubKeyEddsa.String(),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get public key: %w", err)
	}

	// verify
	ok := ed25519.Verify(pubKey.Bytes(), hash[:], signature)
	if !ok {
		return nil, nil, nil, fmt.Errorf("failed to verify signature")
	}

	tx.WitnessSet.VkeyWitnesses = cbor.NewSetType(
		[]lcommon.VkeyWitness{
			{
				Vkey:      pubKey.Bytes(),
				Signature: signature,
			},
		},
		false,
	)

	txBytes, err := cbor.Encode(tx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to encode transaction: %w", err)
	}

	var spentUtxos [][]byte
	for _, input := range tx.Inputs() {
		key := getUtxoKey(input.Id().Bytes(), input.Index())
		spentUtxos = append(spentUtxos, key)
	}
	err = c.storage.SetSpentUtxos(spentUtxos, uint64(currentHeight))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to set spent utxos")
	}

	txIn := types.NewTxInItem(
		currentHeight,
		tx.Hash().String(),
		txOutItem.Memo,
		fromAddress.String(),
		txOutItem.ToAddress.String(),
		[]common.Coin{common.NewCoin(
			common.ADAAsset,
			cosmos.NewUint(tx.Body.TxOutputs[0].Amount().Uint64()*100),
		)},
		[]common.Coin{common.NewCoin(
			common.ADAAsset,
			cosmos.NewUint(tx.Body.TxFee*100),
		)},
		txOutItem.VaultPubKeyEddsa,
		"",
		"",
		nil,
	)

	return txBytes, nil, txIn, nil
}

func (c *Client) sign(
	data []byte,
	txOutItem types.TxOutItem,
) ([]byte, error) {
	var err error
	var signature []byte

	vaultAddress := c.GetAddress(txOutItem.VaultPubKeyEddsa)
	if c.localAddress == vaultAddress {
		signature = ed25519.Sign(c.localKey, data)
	} else {
		signature, _, err = c.tssKeyManager.RemoteSign(
			data,
			common.SigningAlgoEd25519,
			txOutItem.VaultPubKeyEddsa.String(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to sign remote transaction: %w", err)
		}
	}

	return signature, nil
}

func (c *Client) ShouldReportSolvency(height int64) bool {
	if c.config.SolvencyBlocks <= 0 {
		return false
	}
	return height%c.config.SolvencyBlocks == 0
}

func (c *Client) ReportSolvency(height int64) error {
	if !c.ShouldReportSolvency(height) {
		return nil
	}

	// when block scanner is not healthy, only report from auto-unhalt SolvencyCheckRunner
	// (FetchTxs passes PreviousHeight + 1 from scanBlocks, while SolvencyCheckRunner passes chainHeight)
	if !c.IsBlockScannerHealthy() && height == c.blockScanner.PreviousHeight()+1 {
		return nil
	}

	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("failed to get asgards, err: %w", err)
	}

	_, rate := c.chainScanner.GetNetworkFee()

	fee, err := common.ConvertDecimals(
		big.NewInt(int64(rate)),
		int(c.config.ChainID.GetGasAssetDecimal()),
		common.THORChainDecimals,
	)
	if err != nil {
		return fmt.Errorf("failed to convert fee amount to decimals")
	}

	solventMsgs := make([]types.Solvency, 0, len(vaults))
	insolventMsgs := make([]types.Solvency, 0, len(vaults))

	for i, vault := range vaults {
		var acc common.Account
		acc, err = c.GetAccount(vault.PubKeyEddsa, big.NewInt(height))
		if err != nil {
			c.logger.Err(err).Msgf("failed to get account balance")
			continue
		}

		msg := types.Solvency{
			Height: height,
			Chain:  c.config.ChainID,
			PubKey: vaults[i].PubKeyEddsa,
			Coins:  acc.Coins,
		}

		if runners.IsVaultSolvent(acc, vaults[i], cosmos.NewUintFromBigInt(fee)) {
			solventMsgs = append(solventMsgs, msg)
			continue
		}

		insolventMsgs = append(insolventMsgs, msg)
	}

	msgs := insolventMsgs
	solvent := false

	if !c.IsBlockScannerHealthy() && len(solventMsgs) == len(vaults) {
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
			c.logger.Info().Msgf("fail to send solvency info to THORChain, timeout")
		}
	}

	return nil
}
