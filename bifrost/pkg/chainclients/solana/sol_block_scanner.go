package solana

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/signercache"
	. "gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/config"
)

////////////////////////////////////////////////////////////////////////////////////////
// SOLScanner
////////////////////////////////////////////////////////////////////////////////////////

type SOLScanner struct {
	cfg                  config.BifrostBlockScannerConfiguration
	logger               zerolog.Logger
	db                   blockscanner.ScannerStorage
	m                    *metrics.Metrics
	errCounter           *prometheus.CounterVec
	gasPriceChanged      bool
	gasPrice             *big.Int
	lastReportedGasPrice uint64
	solRpc               *rpc.SolRPC
	blockMetaAccessor    evm.BlockMetaAccessor
	bridge               thorclient.ThorchainBridge
	pubkeyMgr            pubkeymanager.PubKeyValidator
	currentBlockHeight   int64
	gasCache             []*big.Int
	solvencyReporter     SolvencyReporter
	signerCacheManager   *signercache.CacheManager

	// metrics
	totalDuration time.Duration
	blockCount    int
	avgDuration   time.Duration
}

// NewSOLScanner create a new instance of SOLScanner.
func NewSOLScanner(cfg config.BifrostBlockScannerConfiguration,
	storage blockscanner.ScannerStorage,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	rpc *rpc.SolRPC,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	solvencyReporter SolvencyReporter,
	signerCacheManager *signercache.CacheManager,
) (*SOLScanner, error) {
	// check required arguments
	if storage == nil {
		return nil, errors.New("storage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics manager is nil")
	}
	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}

	// set storage prefixes
	prefixBlockMeta := fmt.Sprintf("%s-blockmeta-", strings.ToLower(cfg.ChainID.String()))
	prefixSignedMeta := fmt.Sprintf("%s-signedtx-", strings.ToLower(cfg.ChainID.String()))

	// create block meta accessor
	blockMetaAccessor, err := evm.NewLevelDBBlockMetaAccessor(
		prefixBlockMeta, prefixSignedMeta, storage.GetInternalDb(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create block meta accessor: %w", err)
	}

	return &SOLScanner{
		cfg:                  cfg,
		logger:               log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		errCounter:           m.GetCounterVec(metrics.BlockScanError(cfg.ChainID)),
		db:                   storage,
		m:                    m,
		solRpc:               rpc,
		gasPrice:             big.NewInt(0),
		lastReportedGasPrice: 0,
		gasPriceChanged:      false,
		blockMetaAccessor:    blockMetaAccessor,
		bridge:               bridge,
		pubkeyMgr:            pubkeyMgr,
		gasCache:             make([]*big.Int, 0),
		solvencyReporter:     solvencyReporter,
		signerCacheManager:   signerCacheManager,
	}, nil
}

// FetchTxs extracts all relevant transactions from the block at the provided height.
func (s *SOLScanner) FetchTxs(height, chainHeight int64) (stypes.TxIn, error) {
	// log height every 1000 blocks
	if height%1000 == 0 {
		s.logger.Info().Int64("height", height).Msg("fetching txs for height")
	}

	if chainHeight%10 == 0 {
		if _, err := s.bridge.PostNetworkFee(height, common.SOLChain, 10000, 1); err != nil {
			s.logger.Err(err).Msg("fail to post SOL chain single transfer fee to THORNode")
		}
	}

	// process all transactions in the block
	s.currentBlockHeight = height
	block, err := s.solRpc.GetBlock(height)
	if err != nil {
		return stypes.TxIn{}, err
	}
	if block == nil {
		// skip empty blocks
		return stypes.TxIn{}, nil
	}

	// time how long it takes to process the block
	start := time.Now()
	txIn, err := s.processBlock(block)
	if err != nil {
		return stypes.TxIn{}, err
	}
	duration := time.Since(start)
	s.totalDuration += duration
	s.blockCount++
	s.avgDuration = s.totalDuration / time.Duration(s.blockCount)
	s.logger.Info().Str("duration", duration.String()).Str("avg_duration", s.avgDuration.String()).Msg("block processed")

	return txIn, nil
}

// GetHeight returns the current block height of Solana
func (s *SOLScanner) GetHeight() (int64, error) {
	height, err := s.solRpc.GetBlockHeight()
	s.logger.Info().Int64("height", int64(height)).Msg("block height fetched")
	if err != nil {
		return -1, err
	}
	return int64(height), nil
}

// GetLatestBlockHash returns the latest block hash
func (s *SOLScanner) GetLatestBlockHash() (string, error) {
	hash, err := s.solRpc.GetLatestBlockhash()
	s.logger.Info().Str("hash", hash).Msg("latest block hash fetched")
	if err != nil {
		return "", err
	}
	return hash, nil
}

// FetchMemPool returns all transactions in the mempool.
func (s *SOLScanner) FetchMemPool(_ int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

// --------------------------------- extraction ---------------------------------

func (s *SOLScanner) processBlock(block *rpc.BlockResult) (stypes.TxIn, error) {

	// skip empty blocks
	if len(block.Transactions) == 0 {
		return stypes.TxIn{}, nil
	}

	txIn := s.getTxIn(block)
	// prettyPrint(txIn)

	s.logger.Info().Int64("block num", int64(block.ParentSlot)).Int64("total txs", int64(len(block.Transactions))).Int64("num tx ins", int64(len(txIn.TxArray))).Msg("sol block")

	return txIn, nil
}

// Gets the TxIn struct for a block
func (s *SOLScanner) getTxIn(block *rpc.BlockResult) stypes.TxIn {
	txIn := stypes.TxIn{
		Chain:                s.cfg.ChainID,
		TxArray:              []stypes.TxInItem{},
		Filtered:             false,
		MemPool:              false,
		SentUnFinalised:      false,
		Finalised:            true, // Assuming transactions in a block are finalised
		ConfirmationRequired: 1,    // Assuming 1 confirmation is required
	}

	for _, txn := range block.Transactions {
		txItem := s.getTxInItem(txn, int64(block.ParentSlot))
		if txItem == nil {
			continue
		}
		txIn.TxArray = append(txIn.TxArray, *txItem)
	}

	txIn.Count = strconv.Itoa(len(txIn.TxArray))
	return txIn
}

// Create a TxInItem for a SOL transfer transaction
func (s *SOLScanner) getTxInItem(txn rpc.Transaction, blockHeight int64) *stypes.TxInItem {
	memo := ""
	netBalanceChange := make(map[string]int64)
	systemProgramID := "11111111111111111111111111111111"
	memoProgramID := "MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr"
	fee := int64(txn.Meta.Fee)

	for _, instruction := range txn.Transaction.Message.Instructions {
		programID := txn.Transaction.Message.AccountKeys[instruction.ProgramIdIndex]

		// Check if this instruction is the memo
		if programID == memoProgramID {
			memoBytes, err := rpc.Base58Decode(instruction.Data)
			if err == nil {
				memo = string(memoBytes)
			} else {
				s.logger.Error().Err(err).Str("memoData", instruction.Data).Msg("Failed to decode memo")
			}
		}

		// Check if this instruction is a sol transfer
		if programID != systemProgramID {
			continue
		}

		// Log the data for debugging
		// s.logger.Debug().Str("data", instruction.Data).Msg("Instruction data before decoding")

		// Decode the data
		data, err := rpc.Base58Decode(instruction.Data)
		if err != nil {
			s.logger.Error().Err(err).Str("data", instruction.Data).Msg("Failed to decode instruction data")
			continue
		}

		// Check if the instruction is a transfer
		if len(data) >= 1 && data[0] == 2 { // `2` indicates a System Program transfer instruction
			if len(instruction.Accounts) < 2 {
				s.logger.Error().Msg("Instruction does not contain enough accounts for transfer")
				continue
			}
			for i := 0; i < len(instruction.Accounts)-1; i += 2 {
				if i+1 >= len(instruction.Accounts) {
					s.logger.Error().Msg("Index out of range when accessing instruction accounts")
					continue
				}
				fromIdx := instruction.Accounts[i]
				toIdx := instruction.Accounts[i+1]
				if fromIdx >= len(txn.Transaction.Message.AccountKeys) || toIdx >= len(txn.Transaction.Message.AccountKeys) {
					s.logger.Error().Msg("Account index out of range")
					continue
				}
				fromAccount := txn.Transaction.Message.AccountKeys[fromIdx]
				toAccount := txn.Transaction.Message.AccountKeys[toIdx]
				if fromIdx >= len(txn.Meta.PreBalances) || fromIdx >= len(txn.Meta.PostBalances) {
					s.logger.Error().Msg("Balance index out of range")
					continue
				}
				amountMoved := txn.Meta.PreBalances[fromIdx] - txn.Meta.PostBalances[fromIdx]

				// Adjust amountMoved for fee if the fromAccount is the sender
				if fromAccount == txn.Transaction.Message.AccountKeys[0] { // Assuming the first account is the fee payer
					amountMoved -= fee
				}

				// Log the amount moved for debugging
				// s.logger.Debug().Str("fromAccount", fromAccount).Str("toAccount", toAccount).Int64("amountMoved", amountMoved).Msg("Amount moved in lamports")

				netBalanceChange[fromAccount] -= amountMoved
				netBalanceChange[toAccount] += amountMoved
			}
		}
	}

	// Find the net sender and receiver
	var netSender, netReceiver string
	var netAmount int64
	for account, change := range netBalanceChange {
		if change < 0 {
			netSender = account
			netAmount = -change
		} else if change > 0 {
			netReceiver = account
		}
	}

	// Skip if net SOL movement is 0
	if netAmount == 0 {
		return nil
	}

	solCoin := common.NewCoin(common.SOLAsset, convertLamportsToCosmosUint(netAmount))
	gasCoin := common.NewCoin(common.SOLAsset, convertLamportsToCosmosUint(fee))

	return &stypes.TxInItem{
		BlockHeight: blockHeight,
		Tx:          txn.Transaction.Signatures[0],
		Memo:        memo,
		Sender:      netSender,
		To:          netReceiver,
		Coins:       []common.Coin{solCoin},
		Gas:         common.Gas{gasCoin},
	}
}

// --------------------------------- helpers ---------------------------------

// Function to convert amountMoved (lamports) to sdk.Uint with 8 decimal places
func convertLamportsToCosmosUint(amountMoved int64) sdk.Uint {
	amountMovedBig := big.NewInt(amountMoved)
	decimalMultiplier := big.NewInt(1) // 1 for lamports to SOL conversion
	return sdk.NewUintFromBigInt(amountMovedBig.Mul(amountMovedBig, decimalMultiplier))
}

func prettyPrint(data interface{}) {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Printf("Error pretty printing JSON: %v\n", err)
		return
	}
	fmt.Println(string(jsonData))
}

func jsonString(data interface{}) string {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Printf("Error pretty printing JSON: %v\n", err)
		return ""
	}
	return string(jsonData)
}
