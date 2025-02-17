package solana

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

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
	pubkeyMgr          pubkeymanager.PubKeyValidator
	currentBlockHeight int64
	solvencyReporter   SolvencyReporter
	signerCacheManager *signercache.CacheManager

	// fee rate
	feeRateCache        []uint64
	feeRate             uint64
	lastReportedFeeRate uint64

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
		cfg:                 cfg,
		logger:              log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		errCounter:          m.GetCounterVec(metrics.BlockScanError(cfg.ChainID)),
		db:                  storage,
		m:                   m,
		solRpc:              rpc,
		blockMetaAccessor:   blockMetaAccessor,
		bridge:              bridge,
		pubkeyMgr:           pubkeyMgr,
		solvencyReporter:    solvencyReporter,
		signerCacheManager:  signerCacheManager,
		latestBlockHash:     "",
		feeRate:             0,
		lastReportedFeeRate: 0,
		feeRateCache:        []uint64{},
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
	block, err := s.solRpc.GetBlock(height)
	if err != nil {
		return stypes.TxIn{}, err
	}
	if block == nil {
		// skip empty blocks
		return stypes.TxIn{}, nil
	}

	txIn, err := s.processBlock(block)
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

	s.updateNetworkFee(height)

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

	// Stores each transaction's fee rate (lamports per signature)
	txFeeRates := []uint64{}

	// Stores each transaction's priority fee (txn.Meta.Fee - baseFee)
	priorityFees := []uint64{}

	for _, txn := range block.Transactions {
		// Skip transactions with no signatures or no fee
		if len(txn.Transaction.Signatures) == 0 || txn.Meta.Fee == 0 {
			continue
		}

		txItem := s.getTxInItem(txn, int64(block.ParentSlot))
		if txItem == nil {
			continue
		}

		txIn.TxArray = append(txIn.TxArray, *txItem)

		// Calculate the fee rate (lamports per signature)
		numSigs := uint64(len(txn.Transaction.Signatures))
		baseFee := BaseFeePerSigLamports * numSigs
		feeRate := txn.Meta.Fee / numSigs
		if feeRate < BaseFeePerSigLamports {
			feeRate = BaseFeePerSigLamports
		}

		txFeeRates = append(txFeeRates, feeRate)

		// Priority fee is any fee in excess of the base fee
		var priorityFee uint64
		if txn.Meta.Fee == baseFee || txn.Meta.Fee < baseFee {
			priorityFee = 0
		} else {
			priorityFee = txn.Meta.Fee - baseFee
		}

		priorityFees = append(priorityFees, priorityFee)
	}

	if len(txFeeRates) == 1 {
		s.feeRateCache = append(s.feeRateCache, txFeeRates[0])
	} else if len(txFeeRates) > 1 {
		medianFeeRate := calculateMedian(txFeeRates)
		s.feeRateCache = append(s.feeRateCache, medianFeeRate)
	} else {
		// If no transactions in the block, just add the base fee
		s.feeRateCache = append(s.feeRateCache, BaseFeePerSigLamports)
	}

	txIn.Count = strconv.Itoa(len(txIn.TxArray))
	return txIn
}

func (s *SOLScanner) updateNetworkFee(height int64) {
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
	if roundedFeeRate != s.feeRate {
		// post to thorchain
		if _, err := s.bridge.PostNetworkFee(height, s.cfg.ChainID, 1, roundedFeeRate); err != nil {
			s.logger.Err(err).Msg("failed to post SOL chain single transfer fee to THORNode")
		} else {
			s.feeRate = roundedFeeRate
			s.logger.Info().Int("feeRate", int(s.feeRate)).Msg("Updated fee rate")
		}
	}
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

type Token struct {
	Mint     string `json:"mint"`
	Symbol   string `json:"symbol"`
	Decimals int    `json:"decimals"`
}

// mint_to_symbol is a map of mint addresses to their corresponding symbol
var mintToSymbol = map[string]Token{
	"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": Token{
		Mint:     "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		Symbol:   "USDC",
		Decimals: 6,
	},
	"3bRTivrVsitbmCTGtqwp7hxXPsybkjn4XLNtPsHqa3zR": Token{
		Mint:     "3bRTivrVsitbmCTGtqwp7hxXPsybkjn4XLNtPsHqa3zR",
		Symbol:   "LIKE",
		Decimals: 9,
	},
}

// Create a TxInItem for a SOL transfer transaction. This function will only return a
// TxInItem if the transaction contains a transfer of SOL. Otherwise, it will return
// nil.
func (s *SOLScanner) getTxInItem(txn rpc.RPCTransaction, blockHeight int64) *stypes.TxInItem {
	memo := ""
	netBalanceChange := make(map[string]int64)
	fee := txn.Meta.Fee
	splTransfers := []common.Coin{}

	instructions := txn.GetInstructions()
	allInstructions := append(instructions, s.getInnerInstructions(txn)...)
	tokenBalanceChanges := s.getTokenBalanceChanges(txn)
	signers := getTransactionSigners(txn)
	s.logger.Info().Str("signers", jsonString(signers)).Msg("signers")

	for _, instruction := range allInstructions {
		programID := txn.GetAccountAtIndex(instruction.ProgramIdIndex)

		// Check for Memo Program
		if types.MemoProgramID.EqualsString(programID) {
			memoBytes, err := common.Base58Decode(instruction.Data)
			if err == nil {
				memo = string(memoBytes)
			} else {
				s.logger.Error().Err(err).Str("memoData", instruction.Data).Msg("Failed to decode memo")
			}
		}

		// SOL Transfers (System Program)
		if types.SystemProgramID.EqualsString(programID) {
			data, err := common.Base58Decode(instruction.Data)
			if err != nil {
				s.logger.Error().Err(err).Str("data", instruction.Data).Msg("Failed to decode instruction data")
				continue
			}

			if len(data) >= 1 && data[0] == 2 { // Transfer opcode
				if len(instruction.Accounts) < 2 {
					s.logger.Debug().Msg("Instruction does not contain enough accounts for transfer")
					continue
				}

				fromIdx := instruction.Accounts[0]
				toIdx := instruction.Accounts[1]

				if fromIdx >= len(txn.Transaction.Message.AccountKeys) || toIdx >= len(txn.Transaction.Message.AccountKeys) {
					continue
				}

				fromAccount := txn.Transaction.Message.AccountKeys[fromIdx]
				toAccount := txn.Transaction.Message.AccountKeys[toIdx]

				if fromIdx >= len(txn.Meta.PreBalances) || fromIdx >= len(txn.Meta.PostBalances) {
					continue
				}

				amountMoved := txn.Meta.PreBalances[fromIdx] - txn.Meta.PostBalances[fromIdx]

				// Adjust for fee if sender is the fee payer
				if fromAccount == txn.Transaction.Message.AccountKeys[0] {
					amountMoved -= fee
				}

				netBalanceChange[fromAccount] -= int64(amountMoved)
				netBalanceChange[toAccount] += int64(amountMoved)
			}
		}

		// SPL Token Transfers
		if types.TokenProgramID.EqualsString(programID) {
			s.logger.Info().Str("tx id", txn.Transaction.Signatures[0]).Msg("Parsing SPL Token TX")
			data, err := common.Base58Decode(instruction.Data)
			if err != nil {
				s.logger.Error().Err(err).Str("data", instruction.Data).Msg("Failed to decode SPL instruction data")
				continue
			}

			// Process Transfer or TransferChecked opcodes
			if len(data) >= 1 && (data[0] == 2 || data[0] == 3) {
				if len(instruction.Accounts) < 3 {
					s.logger.Debug().Msg("Instruction does not contain enough accounts for SPL transfer")
					continue
				}

				s.logger.Info().Str("instruction.Accounts", jsonString(instruction.Accounts)).Msg("SPL transfer accounts")
				// print each account
				for i, account := range instruction.Accounts {
					s.logger.Info().Int("index", i).Str("account", txn.Transaction.Message.AccountKeys[account]).Msg("instr account")
				}

				fromIdx := instruction.Accounts[0]
				toIdx := instruction.Accounts[1]
				mintIdx := instruction.Accounts[2]

				if fromIdx >= len(txn.Transaction.Message.AccountKeys) ||
					toIdx >= len(txn.Transaction.Message.AccountKeys) ||
					mintIdx >= len(txn.Transaction.Message.AccountKeys) {
					continue
				}

				// fromAccount := txn.Transaction.Message.AccountKeys[fromIdx]
				// toAccount := txn.Transaction.Message.AccountKeys[toIdx]
				mintAddress := txn.Transaction.Message.AccountKeys[mintIdx]

				// Retrieve amount from token balance changes
				amountChange, ok := tokenBalanceChanges[fromIdx][toIdx][mintAddress]
				if !ok || amountChange.Amount == 0 {
					continue
				}

				// Check if the token is in the approved list
				token, exists := mintToSymbol[mintAddress]
				if !exists {
					continue
				}
				decimals := token.Decimals

				// Construct asset with proper chain & ticker
				asset := common.Asset{
					Chain:  common.SOLChain,
					Ticker: common.Ticker(token.Symbol),
					Symbol: common.Symbol(strings.ToUpper(fmt.Sprintf("%s-%s", token.Symbol, mintAddress))),
				}

				// Convert amount to sdk.Uint
				amountUint := convertTokenAmount(amountChange.Amount, decimals)

				tokenCoin := common.NewCoin(asset, amountUint)
				tokenCoin.Decimals = int64(decimals)
				splTransfers = append(splTransfers, tokenCoin)
			}
		}
	}

	// Identify net SOL movement
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

	// Skip if no SOL or SPL movement
	if netAmount == 0 && len(splTransfers) == 0 {
		return nil
	}

	// Add SOL transfer if applicable
	coins := splTransfers
	if netAmount > 0 {
		solCoin := common.NewCoin(common.SOLAsset, sdk.NewUint(uint64(netAmount)))
		solCoin.Decimals = 9
		coins = append(coins, solCoin)
	}

	gasCoin := common.NewCoin(common.SOLAsset, sdk.NewUint(uint64(fee)))
	gasCoin.Decimals = 9

	return &stypes.TxInItem{
		BlockHeight: blockHeight,
		Tx:          txn.Transaction.Signatures[0],
		Memo:        memo,
		Sender:      netSender,
		To:          netReceiver,
		Coins:       coins,
		Gas:         common.Gas{gasCoin},
	}
}

// --------------------------------- helpers ---------------------------------

func getTransactionSigners(txn rpc.RPCTransaction) []string {
	message := txn.Transaction.Message
	signers := []string{}

	// Number of required signatures (these accounts must sign)
	requiredSigners := message.Header.NumRequiredSignatures

	// Extract signers from the first `requiredSigners` in accountKeys
	for i := 0; i < int(requiredSigners) && i < len(message.AccountKeys); i++ {
		signers = append(signers, message.AccountKeys[i])
	}

	return signers
}

// Convert raw token amount to sdk.Uint
func convertTokenAmount(amount int64, decimals int) sdk.Uint {
	amountScaled := sdk.NewUint(uint64(amount))
	if decimals > 8 {
		amountScaled = amountScaled.Quo(sdk.NewUint(uint64(math.Pow10(decimals - 8))))
	} else if decimals < 8 {
		amountScaled = amountScaled.Mul(sdk.NewUint(uint64(math.Pow10(8 - decimals))))
	}
	return amountScaled
}

// Extract inner instructions
func (s *SOLScanner) getInnerInstructions(txn rpc.RPCTransaction) []rpc.RPCInstruction {
	var innerInstructions []rpc.RPCInstruction

	for _, inner := range txn.Meta.InnerInstructions {
		innerInstructions = append(innerInstructions, inner.Instructions...)
	}

	return innerInstructions
}

// Define a struct for tracking token balance changes
type TokenBalanceChange struct {
	Amount int64
}

// Extract token balance changes
func (s *SOLScanner) getTokenBalanceChanges(txn rpc.RPCTransaction) map[int]map[int]map[string]TokenBalanceChange {
	balanceChanges := make(map[int]map[int]map[string]TokenBalanceChange)
	preBalances := make(map[int]map[string]int64)

	// Process pre-token balances
	for _, pre := range txn.Meta.PreTokenBalances {
		amount, err := strconv.ParseInt(pre.Amount.Amount, 10, 64)
		if err != nil {
			s.logger.Error().Err(err).Str("mint", pre.Mint).Msg("Failed to parse pre-token balance amount")
			continue
		}
		if preBalances[pre.AccountIndex] == nil {
			preBalances[pre.AccountIndex] = make(map[string]int64)
		}
		preBalances[pre.AccountIndex][pre.Mint] = amount
	}

	// Compute post-token balance changes
	for _, post := range txn.Meta.PostTokenBalances {
		amount, err := strconv.ParseInt(post.Amount.Amount, 10, 64)
		if err != nil {
			s.logger.Error().Err(err).Str("mint", post.Mint).Msg("Failed to parse post-token balance amount")
			continue
		}

		for fromIdx, mints := range preBalances {
			if prevAmount, ok := mints[post.Mint]; ok {
				amountMoved := prevAmount - amount
				if amountMoved == 0 {
					continue
				}
				if balanceChanges[fromIdx] == nil {
					balanceChanges[fromIdx] = make(map[int]map[string]TokenBalanceChange)
				}
				balanceChanges[fromIdx][post.AccountIndex] = map[string]TokenBalanceChange{
					post.Mint: {Amount: amountMoved},
				}
			}
		}
	}

	return balanceChanges
}

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

func jsonString(data interface{}) string {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Printf("Error pretty printing JSON: %v\n", err)
		return ""
	}
	return string(jsonData)
}
