package ethereum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/bifrost/blockscanner"
	btypes "gitlab.com/thorchain/thornode/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/bifrost/config"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/ethereum/types"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

const (
	DefaultObserverLevelDBFolder = `observer_data`
	BlockCacheSize               = 6000
	DepositEvent                 = "Deposit(address,uint256,string)"
	TransferEvent                = "Transfer(address,address,uint256)"
	ETHToken                     = "0X0000000000000000000000000000000000000000"
)

// BlockScanner is to scan the blocks
type BlockScanner struct {
	cfg               config.BlockScannerConfiguration
	logger            zerolog.Logger
	db                blockscanner.ScannerStorage
	m                 *metrics.Metrics
	errCounter        *prometheus.CounterVec
	gasPrice          *big.Int
	client            *ethclient.Client
	blockMetaAccessor BlockMetaAccessor
	globalErrataQueue chan<- stypes.ErrataBlock
	ContractAddress   ecommon.Address
	ContractABI       *abi.ABI
	ContractOwner     string
	bridge            *thorclient.ThorchainBridge
}

// NewBlockScanner create a new instance of BlockScan
func NewBlockScanner(cfg config.BlockScannerConfiguration, storage blockscanner.ScannerStorage, chainID types.ChainID, client *ethclient.Client, bridge *thorclient.ThorchainBridge, m *metrics.Metrics) (*BlockScanner, error) {
	if storage == nil {
		return nil, errors.New("storage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics is nil")
	}
	if client == nil {
		return nil, errors.New("client is nil")
	}
	eipSigner = etypes.NewEIP155Signer(big.NewInt(int64(chainID)))
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		return nil, err
	}
	blockMetaAccessor, err := NewLevelDBBlockMetaAccessor(storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create block meta accessor: %w", err)
	}

	return &BlockScanner{
		cfg:               cfg,
		logger:            log.Logger.With().Str("module", "blockscanner").Str("chain", common.ETHChain.String()).Logger(),
		errCounter:        m.GetCounterVec(metrics.BlockScanError(common.ETHChain)),
		client:            client,
		db:                storage,
		m:                 m,
		gasPrice:          gasPrice,
		blockMetaAccessor: blockMetaAccessor,
		bridge:            bridge,
	}, nil
}

// GetGasPrice returns current gas price
func (e *BlockScanner) GetGasPrice() *big.Int {
	return e.gasPrice
}

func (e *BlockScanner) GetHeight() (int64, error) {
	block, err := e.client.BlockByNumber(context.Background(), nil)
	if err != nil {
		return -1, err
	}
	return block.Number().Int64(), nil
}

func (e *BlockScanner) FetchTxs(height int64) (stypes.TxIn, error) {
	block, err := e.getRPCBlock(height)
	if err != nil {
		return stypes.TxIn{}, err
	}
	rawTxs, err := e.getTransactionsFromBlock(block)
	if err != nil {
		return stypes.TxIn{}, err
	}

	txIn, err := e.processBlock(block, rawTxs)
	if err != nil {
		if errStatus := e.db.SetBlockScanStatus(blockscanner.Block{Height: height, Txs: rawTxs}, blockscanner.Failed); errStatus != nil {
			e.errCounter.WithLabelValues("fail_set_block_status", "").Inc()
			e.logger.Error().Err(err).Int64("height", height).Msg("fail to set block to fail status")
		}
		e.errCounter.WithLabelValues("fail_search_block", "").Inc()
		e.logger.Error().Err(err).Int64("height", height).Msg("fail to search tx in block")
		// THORNode will have a retry go routine to check it.
		return txIn, err
	}
	// set a block as success
	if err := e.db.RemoveBlockStatus(height); err != nil {
		e.errCounter.WithLabelValues("fail_remove_block_status", "").Inc()
		e.logger.Error().Err(err).Int64("block", height).Msg("fail to remove block status from data store, thus block will be re processed")
	}

	pruneHeight := height - BlockCacheSize
	if pruneHeight > 0 {
		defer func() {
			if err := e.blockMetaAccessor.PruneBlockMeta(pruneHeight); err != nil {
				e.logger.Err(err).Msgf("fail to prune block meta, height(%d)", pruneHeight)
			}
		}()
	}

	if _, err := e.bridge.PostNetworkFee(height, common.ETHChain, 1, cosmos.NewUintFromBigInt(e.GetGasPrice())); err != nil {
		e.logger.Err(err).Msg("fail to post ETH chain single transfer fee to THORNode")
	}
	return txIn, nil
}

func (e *BlockScanner) updateGasPrice() {
	gasPrice, err := e.client.SuggestGasPrice(context.Background())
	if err != nil {
		return
	}
	e.gasPrice = gasPrice
}

func (e *BlockScanner) parseDeposit(data []byte) (string, *big.Int, string, error) {
	event := struct {
		Asset ecommon.Address
		Value *big.Int
		Memo  string
	}{}
	err := e.ContractABI.Unpack(&event, "Deposit", data)
	if err != nil {
		e.logger.Error().Err(err).Msg("fail to parse deposit event")
		return "", nil, "", err
	}
	return event.Asset.String(), event.Value, event.Memo, nil
}

func (e *BlockScanner) parseTransfer(data []byte) (string, string, *big.Int, error) {
	event := struct {
		To    ecommon.Address
		Token ecommon.Address
		Value *big.Int
	}{}
	err := e.ContractABI.Unpack(&event, "Transfer", data)
	if err != nil {
		e.logger.Error().Err(err).Msg("fail to parse transfer event")
		return "", "", nil, err
	}
	return event.To.String(), event.Token.String(), event.Value, nil
}

func (e *BlockScanner) matchDeposit(sig ecommon.Hash) bool {
	if ecrypto.Keccak256Hash([]byte(DepositEvent)) == sig {
		return true
	}
	return false
}

func (e *BlockScanner) matchTransfer(sig ecommon.Hash) bool {
	if ecrypto.Keccak256Hash([]byte(TransferEvent)) == sig {
		return true
	}
	return false
}

// processBlock extracts transactions from block
func (e *BlockScanner) processBlock(block *etypes.Block, rawTxs []string) (stypes.TxIn, error) {
	noTx := stypes.TxIn{}

	height := int64(block.NumberU64())
	if err := e.db.SetBlockScanStatus(blockscanner.Block{Height: height, Txs: rawTxs}, blockscanner.Processing); err != nil {
		e.errCounter.WithLabelValues("fail_set_block_status", "").Inc()
		return noTx, fmt.Errorf("fail to set block scan status for block %d: %w", height, err)
	}

	// Update gas price
	e.updateGasPrice()

	if err := e.processReorg(block); err != nil {
		e.logger.Error().Err(err).Msgf("fail to process reorg for block %d", height)
		return noTx, err
	}

	if len(rawTxs) == 0 {
		e.m.GetCounter(metrics.BlockWithoutTx("ETH")).Inc()
		return noTx, nil
	}
	txIn, err := e.extractTxs(block)
	if err != nil {
		return noTx, err
	}

	blockMeta := types.NewBlockMeta(block)
	if err := e.blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta); err != nil {
		e.logger.Err(err).Msgf("fail to save block meta of height: %d ", blockMeta.Height)
	}
	return txIn, nil
}

func (e *BlockScanner) extractTxs(block *etypes.Block) (stypes.TxIn, error) {
	noTx := stypes.TxIn{}
	var txIn stypes.TxIn
	for _, tx := range block.Transactions() {
		if tx.To() == nil {
			continue
		}
		txInItem, err := e.fromTxToTxIn(tx)
		if err != nil {
			e.errCounter.WithLabelValues("fail_get_tx", "").Inc()
			e.logger.Error().Err(err).Str("hash", tx.Hash().Hex()).Msg("fail to get one tx from server")
			// if THORNode fail to get one tx hash from server, then THORNode should bail, because THORNode might miss tx
			// if THORNode bail here, then THORNode should retry later
			return noTx, fmt.Errorf("fail to get one tx from server: %w", err)
		}
		if txInItem != nil {
			txIn.TxArray = append(txIn.TxArray, *txInItem)
			e.m.GetCounter(metrics.BlockWithTxIn("ETH")).Inc()
			e.logger.Info().Str("hash", tx.Hash().Hex()).Msgf("%s got %d tx", e.cfg.ChainID, 1)
		}
	}
	if len(txIn.TxArray) == 0 {
		e.m.GetCounter(metrics.BlockNoTxIn("ETH")).Inc()
		e.logger.Debug().Int64("block", int64(block.NumberU64())).Msg("no tx need to be processed in this block")
		return noTx, nil
	}
	txIn.BlockHeight = block.Number().String()
	txIn.Count = strconv.Itoa(len(txIn.TxArray))
	txIn.Chain = common.ETHChain
	return txIn, nil
}

func (e *BlockScanner) processReorg(block *etypes.Block) error {
	previousHeight := int64(block.NumberU64()) - 1
	prevBlockMeta, err := e.blockMetaAccessor.GetBlockMeta(previousHeight)
	if err != nil {
		return fmt.Errorf("fail to get block meta of height(%d) : %w", previousHeight, err)
	}
	if prevBlockMeta == nil {
		return nil
	}
	// the block's previous hash need to be the same as the block hash chain client recorded in block meta
	// blockMetas[PreviousHeight].BlockHash == Block.PreviousHash
	if strings.EqualFold(prevBlockMeta.BlockHash, block.ParentHash().Hex()) {
		return nil
	}

	e.logger.Info().Msgf("re-org detected, current block height:%d ,previous block hash is : %s , however block meta at height: %d, block hash is %s", block.NumberU64(), block.ParentHash().Hex(), prevBlockMeta.Height, prevBlockMeta.BlockHash)
	return e.reprocessTxs()
}

// reprocessTx will be kicked off only when chain client detected a re-org on ethereum chain
// it will read through all the block meta data from local storage, and go through all the txs.
// For each transaction, it will send a RPC request to ethereuem chain, double check whether the TX exist or not
// if the tx still exist, then it is all good, if a transaction previous we detected, however doesn't exist anymore, that means
// the transaction had been removed from chain, chain client should report to thorchain
func (e *BlockScanner) reprocessTxs() error {
	blockMetas, err := e.blockMetaAccessor.GetBlockMetas()
	if err != nil {
		return fmt.Errorf("fail to get block metas from local storage: %w", err)
	}

	for _, blockMeta := range blockMetas {
		var errataTxs []stypes.ErrataTx
		for _, tx := range blockMeta.Transactions {
			if e.checkTransaction(tx.Hash) {
				e.logger.Info().Msgf("block height: %d, tx: %s still exist", blockMeta.Height, tx.Hash)
				continue
			}
			// this means the tx doesn't exist in chain ,thus should errata it
			errataTxs = append(errataTxs, stypes.ErrataTx{
				TxID:  common.TxID(tx.Hash),
				Chain: common.ETHChain,
			})
		}
		if len(errataTxs) == 0 {
			continue
		}
		e.globalErrataQueue <- stypes.ErrataBlock{
			Height: blockMeta.Height,
			Txs:    errataTxs,
		}
		// Let's get the block again to fix the block hash
		block, err := e.getBlock(blockMeta.Height)
		if err != nil {
			e.logger.Err(err).Msgf("fail to get block verbose tx result: %d", blockMeta.Height)
		}
		blockMeta.PreviousHash = block.ParentHash().Hex()
		blockMeta.BlockHash = block.Hash().Hex()
		if err := e.blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta); err != nil {
			e.logger.Err(err).Msgf("fail to save block meta of height: %d ", blockMeta.Height)
		}
	}
	return nil
}

func (e *BlockScanner) checkTransaction(hash string) bool {
	receipt, err := e.client.TransactionReceipt(context.Background(), ecommon.HexToHash(hash))
	if err != nil || receipt == nil {
		return false
	}
	return true
}

func (e *BlockScanner) getReceipt(hash string) (*etypes.Receipt, error) {
	return e.client.TransactionReceipt(context.Background(), ecommon.HexToHash(hash))
}

func (e *BlockScanner) getBlock(height int64) (*etypes.Block, error) {
	return e.client.BlockByNumber(context.Background(), big.NewInt(height))
}

func (e *BlockScanner) getRPCBlock(height int64) (*etypes.Block, error) {
	block, err := e.getBlock(height)
	if err == ethereum.NotFound {
		return nil, btypes.UnavailableBlock
	}
	if err != nil {
		e.logger.Error().Err(err).Int64("block", height).Msg("fail to fetch block")
		return nil, err
	}
	return block, nil
}

func (e *BlockScanner) getSymbol(from, token string) (string, error) {
	if token[2:] == ETHToken[2:] {
		return "ETH", nil
	}
	toAddr := &ecommon.Address{}
	*toAddr = ecommon.HexToAddress(token)
	res, err := e.client.CallContract(context.Background(), ethereum.CallMsg{
		From: ecommon.HexToAddress(from),
		To:   toAddr,
		Data: ecrypto.Keccak256([]byte("symbol()"))[:4],
	}, nil)
	if err != nil {
		return "", err
	}
	e.logger.Debug().Msgf("symbol %s", res)
	symbol := ""
	err = json.Unmarshal(res, symbol)
	if err != nil {
		e.logger.Debug().Msgf("symb %s", symbol)
	}
	res = ecommon.TrimLeftZeroes(res)
	res = ecommon.TrimRightZeroes(res)
	e.logger.Debug().Msgf("symbol %d", len(res))
	res = res[len(res)-3:]
	return string(res), nil
}

func (e *BlockScanner) getTransactionsFromBlock(block *etypes.Block) ([]string, error) {
	txs := make([]string, 0)
	for _, tx := range block.Transactions() {
		bytes, err := tx.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("fail to marshal tx from block: %w", err)
		}
		txs = append(txs, string(bytes))
	}
	return txs, nil
}

func (e *BlockScanner) fromTxToTxIn(tx *etypes.Transaction) (*stypes.TxInItem, error) {
	txInItem := &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:],
	}
	asset, err := common.NewAsset("ETH.ETH-" + ETHToken)
	if err != nil {
		e.errCounter.WithLabelValues("fail_create_ticker", "ETH").Inc()
		return nil, fmt.Errorf("fail to create asset, ETH is not valid: %w", err)
	}
	sender, err := eipSigner.Sender(tx)
	if err != nil {
		return nil, err
	}
	txInItem.Sender = strings.ToLower(sender.String())
	if tx.To() == nil {
		return nil, err
	}
	txInItem.To = strings.ToLower(tx.To().String())

	receipt, err := e.getReceipt(tx.Hash().Hex())
	if err != nil {
		return nil, err
	}
	value := tx.Value()
	e.logger.Debug().Msgf("logs %d", len(receipt.Logs))
	if len(receipt.Logs) > 0 {
		e.logger.Debug().Msgf("log addr %s", receipt.Logs[0].Address.String())
	}
	for _, log := range receipt.Logs {
		if log.Address != e.ContractAddress {
			continue
		}
		token := ETHToken
		if e.matchDeposit(log.Topics[0]) {
			memo := ""
			token, value, memo, err = e.parseDeposit(log.Data)
			if err != nil {
				return nil, err
			}
			parts := strings.Split(memo, ":")
			if len(parts) != 3 {
				return nil, err
			}
			index := strings.LastIndex(memo, ":")
			if index == -1 {
				return nil, fmt.Errorf("incorrect memo")
			}
			if strings.Index(memo, ":ETH.") != -1 {
				txInItem.Memo = memo[:index] + "-" + token + memo[index:]
			} else {
				txInItem.Memo = memo
			}
			txInItem.To = e.ContractOwner
		}
		if e.matchTransfer(log.Topics[0]) {
			to := ""
			to, token, value, err = e.parseTransfer(log.Data)
			if err != nil {
				return nil, err
			}
			txInItem.To = strings.ToLower(to)
		}
		symbol, err := e.getSymbol(tx.To().String(), token)
		if err != nil {
			return nil, err
		}
		asset, err = common.NewAsset("ETH." + symbol + "-" + strings.ToUpper(token))
		if err != nil {
			return nil, err
		}
	}

	e.logger.Debug().Msgf("loool %s %s %s", txInItem.Memo, txInItem.To, txInItem.Sender)
	txInItem.Coins = append(txInItem.Coins, common.NewCoin(asset, cosmos.NewUintFromBigInt(value)))
	txInItem.Gas = common.MakeETHGas(tx.GasPrice(), receipt.CumulativeGasUsed)
	return txInItem, nil
}
