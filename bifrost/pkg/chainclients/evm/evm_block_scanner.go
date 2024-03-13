package evm

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	_ "embed"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ethclient "github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/evm"
	evmtypes "gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/signercache"
	. "gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/common/tokenlist"
	"gitlab.com/thorchain/thornode/config"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/aggregators"
	memo "gitlab.com/thorchain/thornode/x/thorchain/memo"
)

var dexAggregators = []aggregators.Aggregator{
	// TSAggregatorPancakeSwap Ethereum V2
	{common.ETHChain, `0x35CF22003c90126528fbe95b21bB3ADB2ca8c53D`, 400_000},
	// TSAggregatorWoofi Avalanche V2
	{common.AVAXChain, `0x5505BE604dFA8A1ad402A71f8A357fba47F9bf5a`, 400_000},
	// TSAggregatorGeneric
	{common.ETHChain, `0xd31f7e39afECEc4855fecc51b693F9A0Cec49fd2`, 400_000},
	// RangoThorchainOutputAggUniV2
	{common.ETHChain, `0x2a7813412b8da8d18Ce56FE763B9eb264D8e28a8`, 400_000},
	// RangoThorchainOutputAggUniV3
	{common.ETHChain, `0xbB8De86F3b041B3C084431dcf3159fE4827c5F0D`, 400_000},
	// PangolinAggregator
	{common.AVAXChain, `0x7a68c37D8AFA3078f3Ad51D98eA23Fe57a8Ae21a`, 400_000},
	// TSAggregatorUniswapV2 - short notation
	{common.ETHChain, `0x86904eb2b3c743400d03f929f2246efa80b91215`, 400_000},
	// TSAggregatorSushiswap - short notation
	{common.ETHChain, `0xbf365e79aa44a2164da135100c57fdb6635ae870`, 400_000},
	// TSAggregatorUniswapV3 100 - short notation
	{common.ETHChain, `0xbd68cbe6c247e2c3a0e36b8f0e24964914f26ee8`, 400_000},
	// TSAggregatorUniswapV3 500 - short notation
	{common.ETHChain, `0xe4ddca21881bac219af7f217703db0475d2a9f02`, 400_000},
	// TSAggregatorUniswapV3 3000 - short notation
	{common.ETHChain, `0x11733abf0cdb43298f7e949c930188451a9a9ef2`, 400_000},
	// TSAggregatorUniswapV3 10000 - short notation
	{common.ETHChain, `0xb33874810e5395eb49d8bd7e912631db115d5a03`, 400_000},
	// TSAggregatorPangolin
	{common.AVAXChain, `0x942c6dA485FD6cEf255853ef83a149d43A73F18a`, 400_000},
	// TSAggregatorTraderJoe
	{common.AVAXChain, `0x3b7DbdD635B99cEa39D3d95Dbd0217F05e55B212`, 400_000},
	// TSAggregatorAvaxGeneric
	{common.AVAXChain, `0x7C38b8B2efF28511ECc14a621e263857Fb5771d3`, 400_000},
	// XDEFIAggregatorEthGeneric
	{common.ETHChain, `0x53E4DD4072A9a8ed56289e048f5BD5AA51c9Bf6E`, 400_000},
	// XDEFIAggregatorEthUniswapV2
	{common.ETHChain, `0xeEe520b0DA1F8a9e4a0480F92CC4c5f6C027ef1E`, 400_000},
	// XDEFIAggregatorAvaxGeneric
	{common.AVAXChain, `0xd0269244A876F7Bc600D1f38B03a9916864b73C6`, 400_000},
	// XDEFIAggregatorAvaxTraderJoe
	{common.AVAXChain, `0x4ab34123A077aE294A39844f3e8df418d2A3D8c4`, 400_000},
	// XDEFIAggregatorUniswapV3 100 - short notation
	{common.ETHChain, `0x88100E08e5287bA3445F95d448ABfF3113d82a4C`, 400_000},
	// XDEFIAggregatorUniswapV3 500 - short notation
	{common.ETHChain, `0xC1faA12981160945903E0725888828E2d6a15821`, 400_000},
	// XDEFIAggregatorUniswapV3 3000 - short notation
	{common.ETHChain, `0x7E019988299cd8038091D8d7fe38f7a1dd3f90F1`, 400_000},
	// XDEFIAggregatorUniswapV3 10000 - short notation
	{common.ETHChain, `0x95B6b888a9fCc5BCA4A3004Df5E9498B63195F48`, 400_000},
	// TSAggregatorGeneric
	{common.BSCChain, `0xB6fA6f1DcD686F4A573Fd243a6FABb4ba36Ba98c`, 400_000},
	// TSAggregatorPancakeV2 BinanceSmartChain
	{common.BSCChain, `0x30912B38618D3D37De3191A4FFE982C65a9aEC2E`, 400_000},
	// TSAggregatorStargate Ethereum gen2 V1
	{common.ETHChain, `0x1204b5Bf0D6d48E718B1d9753A4166A7258B8432`, 800_000},
	// LayerZero Executor Ethereum
	{common.ETHChain, `0xe93685f3bBA03016F02bD1828BaDD6195988D950`, 800_000},
	// LayerZero Executor Avalanche
	{common.AVAXChain, `0xe93685f3bBA03016F02bD1828BaDD6195988D950`, 800_000},
	// LayerZero Executor BinanceSmartChain
	{common.BSCChain, `0xe93685f3bBA03016F02bD1828BaDD6195988D950`, 800_000},
	// TSLedgerAdapter
	{common.ETHChain, `0xB81C7C2D2d078205D7FA515DDB2dEA3d896F4016`, 500_000},
	// SquidRouter MultiCall Ethereum
	{common.ETHChain, `0x4fd39C9E151e50580779bd04B1f7eCc310079fd3`, 800_000},
	// TSAggregatorStargate Ethereum gen2 V2
	{common.ETHChain, `0x48f68ff093b3b3A80D2FC97488EaD97E16b86283`, 800_000},
	// TSAggregatorUniswapV2 Ethereum gen2 V2.5 - tax tokens
	{common.ETHChain, `0x0fA226e8BCf45ec2f3c3163D2d7ba0d2aAD2eBcF`, 800_000},
	// RangoDiamond Ethereum
	{common.ETHChain, `0x69460570c93f9DE5E2edbC3052bf10125f0Ca22d`, 400_000},
	// RangoDiamond BSC
	{common.BSCChain, `0x69460570c93f9DE5E2edbC3052bf10125f0Ca22d`, 400_000},
	// RangoDiamond Avax
	{common.AVAXChain, `0x69460570c93f9DE5E2edbC3052bf10125f0Ca22d`, 400_000},
	// RangoThorchainOutputAggUniV3_COMPACT_Fee500
	{common.ETHChain, `0x70F75937546fB26c6FD3956eBBfb285f41526186`, 400_000},
	// RangoThorchainOutputAggUniV3_COMPACT_Fee3000
	{common.ETHChain, `0xd1687354CBA0e56facd0c44eD0F69D97F5734Dc1`, 400_000},
	// RangoThorchainOutputAggUniV3_COMPACT_Fee10000
	{common.ETHChain, `0xaFa4cBA6db85515f66E3ed7d6784e8cf5b689E2D`, 400_000},
	// RangoThorchainOutputAggUniV2_COMPACT_SUSHI
	{common.ETHChain, `0x0964347B0019eb227c901220ce7d66BB01479220`, 400_000},
	// RangoThorchainOutputAggUniV2_COMPACT_UNI
	{common.ETHChain, `0x6f281993AB68216F8898c593C4578C8a4a76F063`, 400_000},
	// RangoThorchainOutputAggUniV2_COMPACT_PANCAKE
	{common.BSCChain, `0xd0d7A5374ed70D5cB9E9034871F1d89F79De07Dd`, 400_000},
	// RangoThorchainOutputAggUniV3SwapRouter2_COMPACT_Fee500
	{common.BSCChain, `0x5bCAC8ac5f65623f8e151d676605EdE52E0Db532`, 400_000},
	// RangoThorchainOutputAggUniV3SwapRouter2_COMPACT_Fee3000
	{common.BSCChain, `0x36C29dC30E6728BC5524806EeA8897F6d8b9edE3`, 400_000},
	// RangoThorchainOutputAggUniV3SwapRouter2_COMPACT_Fee10000
	{common.BSCChain, `0xd1127EB3bc10a00434FfaD4fBA534212F1ba1165`, 400_000},
	// RangoThorchainOutputAggUniV2_COMPACT_TRADERJOE
	{common.AVAXChain, `0x892Fb7C2A23772f4A2FFC3DC82419147dC22021C`, 400_000},
	// RangoThorchainOutputAggUniV2_COMPACT_PANGOLIN
	{common.AVAXChain, `0xBd039a45e656221E28594d2761DDed8F6712AE46`, 400_000},
	// OKXRouter - ETH
	{common.ETHChain, `0xFc99f58A8974A4bc36e60E2d490Bb8D72899ee9f`, 800_000},
	// OKXRouter - BSC
	{common.BSCChain, `0xFc99f58A8974A4bc36e60E2d490Bb8D72899ee9f`, 800_000},
	// OKXRouter - AVAX
	{common.AVAXChain, `0xf956D9FA19656D8e5219fd6fa8bA6cb198094138`, 800_000},
	// SymbiosisProxy - ETH
	{common.ETHChain, `0x5523985926Aa12BA58DC5Ad00DDca99678D7227E`, 800_000},
	// SymbiosisProxy - AVAX
	{common.AVAXChain, `0x292fC50e4eB66C3f6514b9E402dBc25961824D62`, 800_000},
	// LiFi - ETH
	{common.ETHChain, `0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE`, 800_000},
	// LiFi - BSC
	{common.BSCChain, `0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE`, 800_000},
	// LiFi - AVAX
	{common.AVAXChain, `0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE`, 800_000},
	// LiFi Staging - ETH
	{common.ETHChain, `0xbEbCDb5093B47Cd7add8211E4c77B6826aF7bc5F`, 800_000},
	// LiFi Staging - BSC
	{common.BSCChain, `0xbEbCDb5093B47Cd7add8211E4c77B6826aF7bc5F`, 800_000},
	// LiFi Staging - AVAX
	{common.AVAXChain, `0xbEbCDb5093B47Cd7add8211E4c77B6826aF7bc5F`, 800_000},
}

////////////////////////////////////////////////////////////////////////////////////////
// EVMScanner
////////////////////////////////////////////////////////////////////////////////////////

type EVMScanner struct {
	cfg                  config.BifrostBlockScannerConfiguration
	logger               zerolog.Logger
	db                   blockscanner.ScannerStorage
	m                    *metrics.Metrics
	errCounter           *prometheus.CounterVec
	gasPriceChanged      bool
	gasPrice             *big.Int
	lastReportedGasPrice uint64
	ethClient            *ethclient.Client
	ethRpc               *evm.EthRPC
	blockMetaAccessor    evm.BlockMetaAccessor
	bridge               thorclient.ThorchainBridge
	pubkeyMgr            pubkeymanager.PubKeyValidator
	eipSigner            etypes.Signer
	currentBlockHeight   int64
	gasCache             []*big.Int
	solvencyReporter     SolvencyReporter
	whitelistTokens      []tokenlist.ERC20Token
	whitelistContracts   []common.Address
	signerCacheManager   *signercache.CacheManager
	tokenManager         *evm.TokenManager

	vaultABI *abi.ABI
	erc20ABI *abi.ABI
}

// NewEVMScanner create a new instance of EVMScanner.
func NewEVMScanner(cfg config.BifrostBlockScannerConfiguration,
	storage blockscanner.ScannerStorage,
	chainID *big.Int,
	ethClient *ethclient.Client,
	ethRpc *evm.EthRPC,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	solvencyReporter SolvencyReporter,
	signerCacheManager *signercache.CacheManager,
) (*EVMScanner, error) {
	// check required arguments
	if storage == nil {
		return nil, errors.New("storage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics manager is nil")
	}
	if ethClient == nil {
		return nil, errors.New("ETH RPC client is nil")
	}
	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}

	// set storage prefixes
	prefixBlockMeta := fmt.Sprintf("%s-blockmeta-", strings.ToLower(cfg.ChainID.String()))
	prefixSignedMeta := fmt.Sprintf("%s-signedtx-", strings.ToLower(cfg.ChainID.String()))
	prefixTokenMeta := fmt.Sprintf("%s-tokenmeta-", strings.ToLower(cfg.ChainID.String()))

	// create block meta accessor
	blockMetaAccessor, err := evm.NewLevelDBBlockMetaAccessor(
		prefixBlockMeta, prefixSignedMeta, storage.GetInternalDb(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create block meta accessor: %w", err)
	}

	// load ABIs
	vaultABI, erc20ABI, err := evm.GetContractABI(routerContractABI, erc20ContractABI)
	if err != nil {
		return nil, fmt.Errorf("failed to create contract abi: %w", err)
	}

	// load token list
	allTokens := tokenlist.GetEVMTokenList(cfg.ChainID, common.LatestVersion).Tokens
	var whitelistTokens []tokenlist.ERC20Token
	for _, addr := range cfg.WhitelistTokens {
		// find matching token in token list
		found := false
		for _, tok := range allTokens {
			if strings.EqualFold(addr, tok.Address) {
				whitelistTokens = append(whitelistTokens, tok)
				found = true
				break
			}
		}

		// all whitelisted tokens must be in the chain token list
		if !found {
			return nil, fmt.Errorf("whitelist token %s not found in token list", addr)
		}
	}

	// create token manager - storage is scoped to chain so assets should not collide
	tokenManager, err := evm.NewTokenManager(
		storage.GetInternalDb(),
		prefixTokenMeta,
		cfg.ChainID.GetGasAsset(),
		defaultDecimals,
		cfg.HTTPRequestTimeout,
		whitelistTokens,
		ethClient,
		routerContractABI,
		erc20ContractABI,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token helper: %w", err)
	}

	// store the token metadata for the chain gas asset
	err = tokenManager.SaveTokenMeta(
		cfg.ChainID.GetGasAsset().Symbol.String(), evm.NativeTokenAddr, defaultDecimals,
	)
	if err != nil {
		return nil, err
	}

	// load whitelist contracts for the chain
	whitelistContracts := []common.Address{}
	for _, agg := range dexAggregators {
		if agg.Chain.Equals(cfg.ChainID) {
			whitelistContracts = append(whitelistContracts, common.Address(agg.Address))
		}
	}

	return &EVMScanner{
		cfg:                  cfg,
		logger:               log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		errCounter:           m.GetCounterVec(metrics.BlockScanError(cfg.ChainID)),
		ethRpc:               ethRpc,
		ethClient:            ethClient,
		db:                   storage,
		m:                    m,
		gasPrice:             big.NewInt(0),
		lastReportedGasPrice: 0,
		gasPriceChanged:      false,
		blockMetaAccessor:    blockMetaAccessor,
		bridge:               bridge,
		vaultABI:             vaultABI,
		erc20ABI:             erc20ABI,
		eipSigner:            etypes.NewLondonSigner(chainID),
		pubkeyMgr:            pubkeyMgr,
		gasCache:             make([]*big.Int, 0),
		solvencyReporter:     solvencyReporter,
		whitelistTokens:      whitelistTokens,
		whitelistContracts:   whitelistContracts,
		signerCacheManager:   signerCacheManager,
		tokenManager:         tokenManager,
	}, nil
}

// --------------------------------- exported ---------------------------------

// GetGasPrice returns the current gas price.
func (e *EVMScanner) GetGasPrice() *big.Int {
	return e.gasPrice
}

// GetHeight returns the current block height.
func (e *EVMScanner) GetHeight() (int64, error) {
	height, err := e.ethRpc.GetBlockHeight()
	if err != nil {
		return -1, err
	}
	return height, nil
}

// GetNonce returns the nonce (including pending) for the given address.
func (e *EVMScanner) GetNonce(addr string) (uint64, error) {
	return e.ethRpc.GetNonce(addr)
}

// GetNonceFinalized returns the nonce for the given address.
func (e *EVMScanner) GetNonceFinalized(addr string) (uint64, error) {
	return e.ethRpc.GetNonceFinalized(addr)
}

// FetchMemPool returns all transactions in the mempool.
func (e *EVMScanner) FetchMemPool(_ int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

// GetTokens returns all token meta data.
func (e *EVMScanner) GetTokens() ([]*evmtypes.TokenMeta, error) {
	return e.tokenManager.GetTokens()
}

// FetchTxs extracts all relevant transactions from the block at the provided height.
func (e *EVMScanner) FetchTxs(height, chainHeight int64) (stypes.TxIn, error) {
	// log height every 100 blocks
	if height%100 == 0 {
		e.logger.Info().Int64("height", height).Msg("fetching txs for height")
	}

	// process all transactions in the block
	e.currentBlockHeight = height
	block, err := e.ethRpc.GetBlock(height)
	if err != nil {
		return stypes.TxIn{}, err
	}
	txIn, err := e.processBlock(block)
	if err != nil {
		e.logger.Error().Err(err).Int64("height", height).Msg("failed to search tx in block")
		return stypes.TxIn{}, fmt.Errorf("failed to process block: %d, err:%w", height, err)
	}

	// skip reporting network fee and solvency if block more than flexibility blocks from tip
	if chainHeight-height > e.cfg.ObservationFlexibilityBlocks {
		return txIn, nil
	}

	// report network fee and solvency
	e.reportNetworkFee(height)
	if e.solvencyReporter != nil {
		if err = e.solvencyReporter(height); err != nil {
			e.logger.Err(err).Msg("failed to report Solvency info to THORNode")
		}
	}

	return txIn, nil
}

// --------------------------------- extraction ---------------------------------

func (e *EVMScanner) processBlock(block *etypes.Block) (stypes.TxIn, error) {
	txIn := stypes.TxIn{
		Chain:           e.cfg.ChainID,
		TxArray:         nil,
		Filtered:        false,
		MemPool:         false,
		SentUnFinalised: false,
		Finalised:       false,
	}

	// skip empty blocks
	if block.Transactions().Len() == 0 {
		return txIn, nil
	}

	// collect gas prices of txs in current block
	var txsGas []*big.Int
	for _, tx := range block.Transactions() {
		txsGas = append(txsGas, tx.GasPrice())
	}
	e.updateGasPrice(txsGas)

	// collect all relevant transactions from the block
	txInBlock, err := e.getTxIn(block)
	if err != nil {
		return txIn, err
	}
	if len(txInBlock.TxArray) > 0 {
		txIn.TxArray = append(txIn.TxArray, txInBlock.TxArray...)
	}
	return txIn, nil
}

func (e *EVMScanner) getTxInOptimized(method string, block *etypes.Block) (stypes.TxIn, error) {
	// Use custom method name as it varies between implementation
	// It should be akin to: "fetch all transaction receipts within a block", e.g. getBlockReceipts
	// This has shown to be more efficient than getTransactionReceipt in a batch call
	txInbound := stypes.TxIn{
		Chain:    e.cfg.ChainID,
		Filtered: false,
		MemPool:  false,
	}

	// tx lookup for compatibility with shared evm functions
	txByHash := make(map[string]etypes.Transaction)
	for _, tx := range block.Transactions() {
		if tx == nil {
			continue
		}

		// best effort remove the tx from the signed txs (ok if it does not exist)
		if err := e.blockMetaAccessor.RemoveSignedTxItem(tx.Hash().String()); err != nil {
			e.logger.Err(err).Str("tx hash", tx.Hash().String()).Msg("failed to remove signed tx item")
		}

		txByHash[tx.Hash().String()] = *tx
	}

	var receipts []*etypes.Receipt
	blockNumStr := hexutil.EncodeBig(block.Number())
	err := e.ethClient.Client().Call(
		&receipts,
		method,
		blockNumStr,
	)
	if err != nil {
		e.logger.Error().Err(err).Msg("failed to fetch block receipts")
		return stypes.TxIn{}, err
	}

	for _, receipt := range receipts {
		txForReceipt, ok := txByHash[receipt.TxHash.String()]
		if !ok {
			e.logger.Warn().
				Str("txHash", receipt.TxHash.String()).
				Uint64("blockNumber", block.NumberU64()).
				Msg("receipt tx not in block.Transactions or nil, ignoring...")
			continue
		}

		// tx without to address is not valid
		if txForReceipt.To() == nil {
			continue
		}

		// extract the txInItem
		var txInItem *stypes.TxInItem
		txInItem, err = e.receiptToTxInItem(&txForReceipt, receipt)
		if err != nil {
			e.logger.Error().Err(err).Msg("failed to convert receipt to txInItem")
			continue
		}

		// skip invalid items
		if txInItem == nil {
			continue
		}
		if len(txInItem.To) == 0 {
			continue
		}
		if len([]byte(txInItem.Memo)) > constants.MaxMemoSize {
			continue
		}

		// add the txInItem to the txInbound
		txInItem.BlockHeight = block.Number().Int64()
		txInbound.TxArray = append(txInbound.TxArray, *txInItem)
	}

	if len(txInbound.TxArray) == 0 {
		e.logger.Debug().Uint64("block", block.NumberU64()).Msg("no tx need to be processed in this block")
		return stypes.TxIn{}, nil
	}
	txInbound.Count = strconv.Itoa(len(txInbound.TxArray))
	return txInbound, nil
}

func (e *EVMScanner) getTxIn(block *etypes.Block) (stypes.TxIn, error) {
	// CHANGEME: if an EVM chain supports some way of fetching all transaction receipts
	// within a block, register it here.
	// switch e.cfg.ChainID {
	// case common.BSCChain:
	// return e.getTxInOptimized("eth_getTransactionReceiptsByBlockNumber", block)
	// TODO: add ETH chain after giving BSC some time to bake on mainnet
	// case common.ETHChain:
	// 	return e.getTxInOptimized("eth_getBlockReceipts", block)
	// TODO: add AVAX chain when supported
	// case common.AVAXChain:
	// 	return e.getTxIn("TBD", block)
	// }

	if e.cfg.ChainID.IsBSCChain() {
		return e.getTxInOptimized("eth_getTransactionReceiptsByBlockNumber", block)
	}

	txInbound := stypes.TxIn{
		Chain:    e.cfg.ChainID,
		Filtered: false,
		MemPool:  false,
	}

	// collect all relevant transactions from the block into batches
	batches := [][]*etypes.Transaction{}
	batch := []*etypes.Transaction{}
	for _, tx := range block.Transactions() {
		if tx == nil || tx.To() == nil {
			continue
		}

		// best effort remove the tx from the signed txs (ok if it does not exist)
		if err := e.blockMetaAccessor.RemoveSignedTxItem(tx.Hash().String()); err != nil {
			e.logger.Err(err).Str("tx hash", tx.Hash().String()).Msg("failed to remove signed tx item")
		}

		batch = append(batch, tx)
		if len(batch) >= e.cfg.TransactionBatchSize {
			batches = append(batches, batch)
			batch = []*etypes.Transaction{}
		}
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}

	// process all batches
	for _, batch := range batches {
		// create the batch rpc request
		var rpcBatch []rpc.BatchElem
		for _, tx := range batch {
			rpcBatch = append(rpcBatch, rpc.BatchElem{
				Method: "eth_getTransactionReceipt",
				Args:   []interface{}{tx.Hash().String()},
				Result: &etypes.Receipt{},
			})
		}

		// send the batch rpc request
		err := e.ethClient.Client().BatchCall(rpcBatch)
		if err != nil {
			e.logger.Error().Int("size", len(batch)).Err(err).Msg("failed to batch fetch transaction receipts")
			return stypes.TxIn{}, err
		}

		// process the batch rpc response
		for i, elem := range rpcBatch {
			if elem.Error != nil {
				if !errors.Is(err, ethereum.NotFound) {
					e.logger.Error().Err(elem.Error).Msg("failed to fetch transaction receipt")
				}
				continue
			}

			// get the receipt
			receipt, ok := elem.Result.(*etypes.Receipt)
			if !ok {
				e.logger.Error().Msg("failed to cast to transaction receipt")
				continue
			}

			// extract the txInItem
			var txInItem *stypes.TxInItem
			txInItem, err = e.receiptToTxInItem(batch[i], receipt)
			if err != nil {
				e.logger.Error().Err(err).Msg("failed to convert receipt to txInItem")
				continue
			}

			// skip invalid items
			if txInItem == nil {
				continue
			}
			if len(txInItem.To) == 0 {
				continue
			}
			if len([]byte(txInItem.Memo)) > constants.MaxMemoSize {
				continue
			}

			// add the txInItem to the txInbound
			txInItem.BlockHeight = block.Number().Int64()
			txInbound.TxArray = append(txInbound.TxArray, *txInItem)
		}
	}

	if len(txInbound.TxArray) == 0 {
		e.logger.Debug().Uint64("block", block.NumberU64()).Msg("no tx need to be processed in this block")
		return stypes.TxIn{}, nil
	}
	txInbound.Count = strconv.Itoa(len(txInbound.TxArray))
	return txInbound, nil
}

// TODO: This is only used by unit tests now, but covers receiptToTxInItem internally -
// refactor so this logic is only in test code.
func (e *EVMScanner) getTxInItem(tx *etypes.Transaction) (*stypes.TxInItem, error) {
	if tx == nil || tx.To() == nil {
		return nil, nil
	}

	receipt, err := e.ethRpc.GetReceipt(tx.Hash().Hex())
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get transaction receipt: %w", err)
	}

	return e.receiptToTxInItem(tx, receipt)
}

func (e *EVMScanner) receiptToTxInItem(tx *etypes.Transaction, receipt *etypes.Receipt) (*stypes.TxInItem, error) {
	if receipt.Status != 1 {
		e.logger.Debug().Stringer("txid", tx.Hash()).Uint64("status", receipt.Status).Msg("tx failed")

		// remove failed transactions from signer cache so they are retried
		if e.signerCacheManager != nil {
			e.signerCacheManager.RemoveSigned(tx.Hash().String())
		}

		return e.getTxInFromFailedTransaction(tx, receipt), nil
	}

	if e.isToValidContractAddress(tx.To(), true) {
		return e.getTxInFromSmartContract(tx, receipt)
	}

	e.logger.Debug().Stringer("txid", tx.Hash()).Stringer("to", tx.To()).Msg("not a valid contract")
	return e.getTxInFromTransaction(tx)
}

// --------------------------------- gas ---------------------------------

// updateGasPrice calculates and stores the current gas price to reported to thornode
func (e *EVMScanner) updateGasPrice(prices []*big.Int) {
	// skip empty blocks
	if len(prices) == 0 {
		return
	}

	// find the median gas price in the block
	sort.Slice(prices, func(i, j int) bool { return prices[i].Cmp(prices[j]) == -1 })
	gasPrice := prices[len(prices)/2]

	// add to the cache
	e.gasCache = append(e.gasCache, gasPrice)
	if len(e.gasCache) > e.cfg.GasCacheBlocks {
		e.gasCache = e.gasCache[(len(e.gasCache) - e.cfg.GasCacheBlocks):]
	}

	// skip update unless cache is full
	if len(e.gasCache) < e.cfg.GasCacheBlocks {
		return
	}

	// compute the median of the median prices in the cache
	medians := []*big.Int{}
	medians = append(medians, e.gasCache...)
	sort.Slice(medians, func(i, j int) bool { return medians[i].Cmp(medians[j]) == -1 })
	median := medians[len(medians)/2]

	// round the price up to avoid fee noise
	resolution := big.NewInt(e.cfg.GasPriceResolution)
	if median.Cmp(resolution) != 1 {
		e.gasPrice = resolution
	} else {
		median.Sub(median, big.NewInt(1))
		median.Quo(median, big.NewInt(e.cfg.GasPriceResolution))
		median.Add(median, big.NewInt(1))
		median.Mul(median, big.NewInt(e.cfg.GasPriceResolution))
		e.gasPrice = median
	}

	// record metrics
	gasPriceFloat, _ := new(big.Float).SetInt64(e.gasPrice.Int64()).Float64()
	e.m.GetGauge(metrics.GasPrice(e.cfg.ChainID)).Set(gasPriceFloat)
	e.m.GetCounter(metrics.GasPriceChange(e.cfg.ChainID)).Inc()
}

// reportNetworkFee reports current network fee to thornode
func (e *EVMScanner) reportNetworkFee(height int64) {
	gasPrice := e.GetGasPrice()

	// skip posting if there is not yet a fee
	if gasPrice.Cmp(big.NewInt(0)) == 0 {
		return
	}

	// skip fee if less than 1 resolution away from the last
	feeDelta := new(big.Int).Sub(gasPrice, big.NewInt(int64(e.lastReportedGasPrice)))
	feeDelta.Abs(feeDelta)
	if e.lastReportedGasPrice != 0 && feeDelta.Cmp(big.NewInt(e.cfg.GasPriceResolution)) != 1 {
		skip := true

		// every 100 blocks send the fee if none is set
		if height%100 == 0 {
			hasNetworkFee, err := e.bridge.HasNetworkFee(e.cfg.ChainID)
			skip = err != nil || hasNetworkFee
		}

		if skip {
			return
		}
	}

	// gas price to 1e8
	tcGasPrice := new(big.Int).Div(gasPrice, big.NewInt(common.One*100))

	// post to thorchain
	if _, err := e.bridge.PostNetworkFee(height, e.cfg.ChainID, e.cfg.MaxGasLimit, tcGasPrice.Uint64()); err != nil {
		e.logger.Err(err).Msg("failed to post EVM chain single transfer fee to THORNode")
	} else {
		e.lastReportedGasPrice = gasPrice.Uint64()
	}
}

// --------------------------------- parse transaction ---------------------------------

func (e *EVMScanner) getTxInFromTransaction(tx *etypes.Transaction) (*stypes.TxInItem, error) {
	txInItem := &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:], // drop the "0x" prefix
	}

	sender, err := e.eipSigner.Sender(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get sender: %w", err)
	}
	txInItem.Sender = strings.ToLower(sender.String())
	txInItem.To = strings.ToLower(tx.To().String())

	// on native transactions the memo is hex encoded in the data field
	data := tx.Data()
	if len(data) > 0 {
		var memo []byte
		memo, err = hex.DecodeString(string(data))
		if err != nil {
			txInItem.Memo = string(data)
		} else {
			txInItem.Memo = string(memo)
		}
	}

	nativeValue := e.tokenManager.ConvertAmount(evm.NativeTokenAddr, tx.Value())
	txInItem.Coins = append(txInItem.Coins, common.NewCoin(e.cfg.ChainID.GetGasAsset(), nativeValue))
	txGasPrice := tx.GasPrice()
	if txGasPrice.Cmp(big.NewInt(tenGwei)) < 0 {
		txGasPrice = big.NewInt(tenGwei)
	}
	txInItem.Gas = common.MakeEVMGas(e.cfg.ChainID, txGasPrice, tx.Gas())
	txInItem.Gas[0].Asset = e.cfg.ChainID.GetGasAsset()

	if txInItem.Coins.IsEmpty() {
		if txInItem.Sender == txInItem.To {
			// When the Sender and To is the same then there's no balance chance whatever the Coins,
			// and for Tx-received Valid() a non-zero Amount is needed to observe
			// the transaction fees (THORChain gas cost) of unstuck.go's cancel transactions.
			observableAmount := e.cfg.ChainID.DustThreshold().Add(cosmos.OneUint()) // Adding 1 for if DustThreshold is 0.
			txInItem.Coins = common.NewCoins(common.NewCoin(txInItem.Gas[0].Asset, observableAmount))

			// remove the outbound from signer cache so it can be re-attempted
			e.signerCacheManager.RemoveSigned(tx.Hash().Hex())
		} else {
			e.logger.Debug().Msgf("there is no coin in this tx, ignore, %+v", txInItem)
			return nil, nil
		}
	}

	return txInItem, nil
}

// isToValidContractAddress this method make sure the transaction to address is to
// THORChain router or a whitelist address
func (e *EVMScanner) isToValidContractAddress(addr *ecommon.Address, includeWhiteList bool) bool {
	if addr == nil {
		return false
	}
	// get the smart contract used by thornode
	contractAddresses := e.pubkeyMgr.GetContracts(e.cfg.ChainID)
	if includeWhiteList {
		contractAddresses = append(contractAddresses, e.whitelistContracts...)
	}

	// combine the whitelist smart contract address
	for _, item := range contractAddresses {
		if strings.EqualFold(item.String(), addr.String()) {
			return true
		}
	}
	return false
}

// getTxInFromSmartContract returns txInItem
func (e *EVMScanner) getTxInFromSmartContract(tx *etypes.Transaction, receipt *etypes.Receipt) (*stypes.TxInItem, error) {
	txInItem := &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:], // drop the "0x" prefix
	}
	sender, err := e.eipSigner.Sender(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get sender: %w", err)
	}
	txInItem.Sender = strings.ToLower(sender.String())
	// 1 is Transaction success state
	if receipt.Status != 1 {
		e.logger.Debug().Stringer("txid", tx.Hash()).Uint64("status", receipt.Status).Msg("tx failed")
		return nil, nil
	}
	p := evm.NewSmartContractLogParser(e.isToValidContractAddress,
		e.tokenManager.GetAssetFromTokenAddress,
		e.tokenManager.GetTokenDecimalsForTHORChain,
		e.tokenManager.ConvertAmount,
		e.vaultABI,
		e.cfg.ChainID.GetGasAsset(),
	)

	// txInItem will be changed in p.getTxInItem function, so if the function return an
	// error txInItem should be abandoned
	isVaultTransfer, err := p.GetTxInItem(receipt.Logs, txInItem)
	if err != nil {
		return nil, fmt.Errorf("failed to parse logs, err: %w", err)
	}
	if isVaultTransfer {
		contractAddresses := e.pubkeyMgr.GetContracts(e.cfg.ChainID)
		isDirectlyToRouter := false
		for _, item := range contractAddresses {
			if strings.EqualFold(item.String(), tx.To().String()) {
				isDirectlyToRouter = true
				break
			}
		}
		if isDirectlyToRouter {
			// it is important to keep this part outside the above loop, as when we do router
			// upgrade, which might generate multiple deposit event, along with tx that has
			// native value in it
			nativeValue := cosmos.NewUintFromBigInt(tx.Value())
			if !nativeValue.IsZero() {
				nativeValue = e.tokenManager.ConvertAmount(evm.NativeTokenAddr, tx.Value())
				if txInItem.Coins.GetCoin(e.cfg.ChainID.GetGasAsset()).IsEmpty() && !nativeValue.IsZero() {
					txInItem.Coins = append(txInItem.Coins, common.NewCoin(e.cfg.ChainID.GetGasAsset(), nativeValue))
				}
			}
		}
	}
	e.logger.Info().Str("tx hash", txInItem.Tx).Str("gas price", tx.GasPrice().String()).Uint64("gas used", receipt.GasUsed).Uint64("tx status", receipt.Status).Msg("txInItem parsed from smart contract")

	// under no circumstance EVM gas price will be less than 1 Gwei, unless it is in dev environment
	txGasPrice := tx.GasPrice()
	if txGasPrice.Cmp(big.NewInt(tenGwei)) < 0 {
		txGasPrice = big.NewInt(tenGwei)
	}
	txInItem.Gas = common.MakeEVMGas(e.cfg.ChainID, txGasPrice, receipt.GasUsed)
	if txInItem.Coins.IsEmpty() {
		return nil, nil
	}
	return txInItem, nil
}

// getTxInFromFailedTransaction when a transaction failed due to out of gas, this method
// will check whether the transaction is an outbound it fake a txInItem if the failed
// transaction is an outbound , and report it back to thornode, thus the gas fee can be
// subsidised need to know that this will also cause the vault that send
// out the outbound to be slashed 1.5x gas it is for security purpose
func (e *EVMScanner) getTxInFromFailedTransaction(tx *etypes.Transaction, receipt *etypes.Receipt) *stypes.TxInItem {
	if receipt.Status == 1 {
		e.logger.Info().Str("hash", tx.Hash().String()).Msg("success transaction should not get into getTxInFromFailedTransaction")
		return nil
	}
	fromAddr, err := e.eipSigner.Sender(tx)
	if err != nil {
		e.logger.Err(err).Msg("failed to get from address")
		return nil
	}
	ok, cif := e.pubkeyMgr.IsValidPoolAddress(fromAddr.String(), e.cfg.ChainID)
	if !ok || cif.IsEmpty() {
		return nil
	}
	txGasPrice := tx.GasPrice()
	if txGasPrice.Cmp(big.NewInt(tenGwei)) < 0 {
		txGasPrice = big.NewInt(tenGwei)
	}
	txHash := tx.Hash().Hex()[2:]

	return &stypes.TxInItem{
		Tx:     txHash,
		Memo:   memo.NewOutboundMemo(common.TxID(txHash)).String(),
		Sender: strings.ToLower(fromAddr.String()),
		To:     strings.ToLower(tx.To().String()),
		Coins:  common.NewCoins(common.NewCoin(e.cfg.ChainID.GetGasAsset(), cosmos.NewUint(1))),
		Gas:    common.MakeEVMGas(e.cfg.ChainID, txGasPrice, tx.Gas()),
	}
}
