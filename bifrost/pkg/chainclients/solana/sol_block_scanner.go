package solana

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/mr-tron/base58"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	. "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	sdk "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	LatestHashUpdateInterval = 100  // update latest block hash every 100 blocks
	BaseFeePerSigLamports    = 5000 // "Base" fee per signature of a transaction
)

////////////////////////////////////////////////////////////////////////////////////////
// SOLScanner
////////////////////////////////////////////////////////////////////////////////////////

type SOLScanner struct {
	cfg                config.BifrostBlockScannerConfiguration
	logger             zerolog.Logger
	db                 blockscanner.ScannerStorage
	m                  *metrics.Metrics
	errCounter         *prometheus.CounterVec
	solRpc             *rpc.SolRPC
	blockMetaAccessor  evm.BlockMetaAccessor
	bridge             thorclient.ThorchainBridge
	currentBlockHeight int64
	solvencyReporter   SolvencyReporter
	signerCacheManager *signercache.CacheManager
	geyser             *SolGeyser

	globalNetworkFeeQueue chan common.NetworkFee

	// fee rate
	feeRateCache []uint64
	lastFeeRate  uint64

	// metrics
	totalDuration time.Duration
	blockCount    int
	avgDuration   time.Duration

	// local state
	latestBlockHash string
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
		cfg:                cfg,
		logger:             log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		errCounter:         m.GetCounterVec(metrics.BlockScanError(cfg.ChainID)),
		db:                 storage,
		m:                  m,
		solRpc:             rpc,
		blockMetaAccessor:  blockMetaAccessor,
		bridge:             bridge,
		solvencyReporter:   solvencyReporter,
		signerCacheManager: signerCacheManager,
		latestBlockHash:    "",
		lastFeeRate:        0,
		feeRateCache:       make([]uint64, 0),
	}, nil
}

// FetchTxs extracts all relevant transactions from the block at the provided height.
func (s *SOLScanner) FetchTxs(height, chainHeight int64) (stypes.TxIn, error) {
	// log height every 1000 blocks
	if height%1000 == 0 {
		s.logger.Info().Int64("height", height).Msg("fetching txs for height")
	}

	// Update latest block hash every LatestHashUpdateInterval blocks
	if height%LatestHashUpdateInterval == 0 {
		if err := s.updateLatestBlockHash(); err != nil {
			s.logger.Err(err).Msg("failed to update latest block hash")
		}
	}

	// time how long it takes to process the block
	start := time.Now()

	// process all transactions in the block
	s.currentBlockHeight = height

	var block *rpc.BlockResult
	var err error

	// First try to get block from geyser cache if available
	if s.geyser != nil {
		if cachedBlock, exists := s.geyser.GetBlock(uint64(height)); exists {
			s.logger.Debug().Msgf("%s: Found block in geyser cache for height: %d", time.Now().Format(time.UnixDate), height)
			block = cachedBlock
			// Remove from cache since we've processed it
			s.geyser.RemoveBlock(uint64(height))
		}
	}

	// Fall back to RPC if block not in cache
	if block == nil {
		s.logger.Info().Msgf("%s: Fetching block from RPC for height: %d", time.Now().Format(time.UnixDate), height)
		block, err = s.solRpc.GetBlock(height)
		if err != nil {
			return stypes.TxIn{}, err
		}
	}

	if block == nil {
		// skip empty blocks
		return stypes.TxIn{}, nil
	}

	txIn, err := s.ProcessBlock(block)
	if err != nil {
		return stypes.TxIn{}, err
	}

	duration := time.Since(start)
	s.totalDuration += duration
	s.blockCount++
	s.avgDuration = s.totalDuration / time.Duration(s.blockCount)

	if height%1000 == 0 {
		// TODO: remove or make debug
		s.logger.Info().Str("duration", duration.String()).Str("avg_duration", s.avgDuration.String()).Msg("sol block processed")
	}

	s.UpdateNetworkFee(height)

	return txIn, nil
}

// GetHeight returns the current block height of Solana
func (s *SOLScanner) GetHeight() (int64, error) {
	height, err := s.solRpc.GetBlockHeight()
	if err != nil {
		return -1, err
	}
	return int64(height), nil
}

// GetLatestBlockHash returns the latest block hash
func (s *SOLScanner) GetLatestBlockHash() string {
	return s.latestBlockHash
}

// UpdateLatestBlockHash updates the local latest block hash to be used in tx signing
func (s *SOLScanner) updateLatestBlockHash() error {
	hash, err := s.solRpc.GetLatestBlockhash()
	if err != nil {
		return err
	}
	s.latestBlockHash = hash
	return nil
}

// GetNetworkFee returns current chain network fee according to Bifrost.
func (c *SOLScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	return 1, c.lastFeeRate
}

// FetchMemPool returns all transactions in the mempool.
func (s *SOLScanner) FetchMemPool(_ int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

func (s *SOLScanner) ProcessBlock(block *rpc.BlockResult) (stypes.TxIn, error) {
	// skip empty blocks
	if len(block.Transactions) == 0 {
		return stypes.TxIn{}, nil
	}

	txIn := s.getTxIn(block)

	return txIn, nil
}

func (s *SOLScanner) UpdateNetworkFee(height int64) {
	// Not enough blocks yet
	if len(s.feeRateCache) < s.cfg.GasCacheBlocks {
		return
	}

	// Calculate the median fee rate from the gas cache
	medianFeeRate := calculateMedian(s.feeRateCache)

	// round medianFeeRate to the nearest resolution
	resolution := uint64(s.cfg.GasPriceResolution)
	roundedFeeRate := ((medianFeeRate + (resolution / 2)) / resolution) * resolution

	// Update the fee rate if it has changed
	if roundedFeeRate == s.lastFeeRate {
		return
	}

	s.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           s.cfg.ChainID,
		Height:          height,
		TransactionSize: 1,
		TransactionRate: roundedFeeRate,
	}

	s.lastFeeRate = roundedFeeRate
	s.logger.Info().
		Uint64("fee", roundedFeeRate).
		Int64("height", height).
		Msg("sent network fee to THORChain")
}

// --------------------------------- extraction ---------------------------------

// Gets the TxIn struct for a block
func (s *SOLScanner) getTxIn(block *rpc.BlockResult) stypes.TxIn {
	txIn := stypes.TxIn{
		Chain:                s.cfg.ChainID,
		TxArray:              make([]*stypes.TxInItem, 0),
		MemPool:              false,
		ConfirmationRequired: 1, // Assuming 1 confirmation is required
	}

	// Stores each transaction's fee rate (lamports per signature)
	txFeeRates := []uint64{}

	// Stores each transaction's priority fee (txn.Meta.Fee - baseFee)
	// priorityFees := []uint64{}

	for _, txn := range block.Transactions {
		// Skip transactions with no signatures or no fee
		if len(txn.Transaction.Signatures) == 0 || txn.Meta.Fee == 0 {
			continue
		}

		txItem := s.getTxInItem(txn, int64(block.ParentSlot))
		if txItem == nil {
			continue
		}

		txIn.TxArray = append(txIn.TxArray, txItem)

		// Calculate the fee rate (lamports per signature)
		numSigs := uint64(len(txn.Transaction.Signatures))
		// baseFee := BaseFeePerSigLamports * numSigs
		feeRate := txn.Meta.Fee / numSigs
		if feeRate < BaseFeePerSigLamports {
			feeRate = BaseFeePerSigLamports
		}

		txFeeRates = append(txFeeRates, feeRate)

		// Priority fee is any fee in excess of the base fee
		// var priorityFee uint64
		// if txn.Meta.Fee == baseFee || txn.Meta.Fee < baseFee {
		// 	priorityFee = 0
		// } else {
		// 	priorityFee = txn.Meta.Fee - baseFee
		// }

		// priorityFees = append(priorityFees, priorityFee)
	}

	switch {
	case len(txFeeRates) == 1:
		s.feeRateCache = append(s.feeRateCache, txFeeRates[0])
	case len(txFeeRates) > 1:
		medianFeeRate := calculateMedian(txFeeRates)
		s.feeRateCache = append(s.feeRateCache, medianFeeRate)
	case len(txFeeRates) == 0:
		// If no transactions in the block, just add the base fee
		s.feeRateCache = append(s.feeRateCache, BaseFeePerSigLamports)
	}

	return txIn
}

// Calculates the median of a slice of uint64
func calculateMedian(arr []uint64) uint64 {
	sort.Slice(arr, func(i, j int) bool { return arr[i] < arr[j] })
	n := len(arr)
	if n%2 == 0 {
		return (arr[n/2-1] + arr[n/2]) / 2
	}
	return arr[n/2]
}

// Create a TxInItem for a SOL transfer transaction. This function will only return a
// TxInItem if the transaction contains a transfer of SOL. Otherwise, it will return
// nil.
func (s *SOLScanner) getTxInItem(txn rpc.RPCTransaction, blockHeight int64) *stypes.TxInItem {
	memo := ""
	netBalanceChange := make(map[string]int64)
	fee := txn.Meta.Fee

	instructions := txn.GetInstructions()

	for _, instruction := range instructions {
		programID := txn.GetAccountAtIndex(instruction.ProgramIdIndex)

		// Check if this instruction is the memo
		if types.MemoProgramID.EqualsString(programID) {
			memoBytes, err := base58.Decode(instruction.Data)
			if err == nil {
				memo = string(memoBytes)
			} else {
				s.logger.Error().Err(err).Str("memoData", instruction.Data).Msg("Failed to decode memo")
			}
		}

		// SOL transfers using the systemProgram, so skip if program is something else.
		if !types.SystemProgramID.EqualsString(programID) {
			continue
		}

		// Decode the data
		data, err := base58.Decode(instruction.Data)
		if err != nil {
			s.logger.Error().Err(err).Str("data", instruction.Data).Msg("Failed to decode instruction data")
			continue
		}

		// Check if the instruction is a transfer
		if len(data) >= 1 && data[0] == 2 { // `2` indicates a System Program transfer instruction
			if len(instruction.Accounts) < 2 {
				s.logger.Debug().Msg("Instruction does not contain enough accounts for transfer")
				continue
			}
			for i := 0; i < len(instruction.Accounts)-1; i += 2 {
				if i+1 >= len(instruction.Accounts) {
					s.logger.Debug().Msg("Index out of range when accessing instruction accounts")
					continue
				}
				fromIdx := instruction.Accounts[i]
				toIdx := instruction.Accounts[i+1]
				if fromIdx >= len(txn.Transaction.Message.AccountKeys) || toIdx >= len(txn.Transaction.Message.AccountKeys) {
					// Account index out of range, meaning the instruction references an account
					// from an account lookup table. This instruction can be skipped since
					// transfer instructions must reference accounts in the primary account keys
					// array
					continue
				}
				fromAccount := txn.Transaction.Message.AccountKeys[fromIdx]
				toAccount := txn.Transaction.Message.AccountKeys[toIdx]
				if fromIdx >= len(txn.Meta.PreBalances) || fromIdx >= len(txn.Meta.PostBalances) {
					// Account index out of range, meaning the instruction references an account
					// from an account lookup table. This instruction can be skipped since
					// transfer instructions must reference accounts in the primary account keys
					// array
					continue
				}
				amountMoved := txn.Meta.PreBalances[fromIdx] - txn.Meta.PostBalances[fromIdx]

				// Adjust amountMoved for fee if the fromAccount is the sender
				if fromAccount == txn.Transaction.Message.AccountKeys[0] { // Assuming the first account is the fee payer
					amountMoved -= fee
				}

				netBalanceChange[fromAccount] -= int64(amountMoved)
				netBalanceChange[toAccount] += int64(amountMoved)
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

	solCoin := common.NewCoin(common.SOLAsset, convertLamportsToTHORChain(netAmount))
	gasCoin := common.NewCoin(common.SOLAsset, convertLamportsToTHORChain(int64(fee)))

	// set coin decimals
	solCoin.Decimals = 9
	gasCoin.Decimals = 9

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

// Function to convert amountMoved (lamports) to sdk.Uintt
func convertLamportsToTHORChain(amountMoved int64) sdk.Uint {
	amountMovedBig := big.NewInt(amountMoved)
	return sdk.NewUintFromBigInt(amountMovedBig.Quo(amountMovedBig, big.NewInt(10)))
}

func prettyPrint(data interface{}) {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Printf("Error pretty printing JSON: %v\n", err)
		return
	}
	fmt.Println(string(jsonData))
}

// Add setter for geyser
func (s *SOLScanner) SetGeyser(g *SolGeyser) {
	s.geyser = g
}
