package solana

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mr-tron/base58"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	shtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	sdk "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

const (
	BaseFeePerSigLamports = 5000 // "Base" fee per signature of a transaction

	NetFeeUpdateInterval = 1000 // update network fee every 1000 slots
)

////////////////////////////////////////////////////////////////////////////////////////
// SOLScanner
////////////////////////////////////////////////////////////////////////////////////////

type SOLScanner struct {
	cfg                config.BifrostBlockScannerConfiguration
	logger             zerolog.Logger
	db                 blockscanner.ScannerStorageSolana
	m                  *metrics.Metrics
	errCounter         *prometheus.CounterVec
	solRpc             *rpc.SolRPC
	bridge             thorclient.ThorchainBridge
	solvencyReporter   shtypes.SolvencyReporter
	signerCacheManager *signercache.CacheManager

	globalNetworkFeeQueue chan common.NetworkFee
	globalTxsQueue        chan stypes.TxIn

	// fee rate
	feeRateCache []uint64
	lastFeeRate  uint64

	healthy *atomic.Bool

	// local state
	latestBlockHash string
	latestBlockMu   sync.Mutex

	// network fee state
	netFeeSlotMultiple uint64
	netFeeInit         bool

	lastLog time.Time

	stopChan chan struct{}
}

// NewSOLScanner create a new instance of SOLScanner.
func NewSOLScanner(
	stopChan chan struct{},
	cfg config.BifrostBlockScannerConfiguration,
	storage blockscanner.ScannerStorageSolana,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	rpc *rpc.SolRPC,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	solvencyReporter shtypes.SolvencyReporter,
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

	return &SOLScanner{
		cfg:                cfg,
		logger:             log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		errCounter:         m.GetCounterVec(metrics.BlockScanError(cfg.ChainID)),
		db:                 storage,
		m:                  m,
		solRpc:             rpc,
		bridge:             bridge,
		solvencyReporter:   solvencyReporter,
		signerCacheManager: signerCacheManager,
		latestBlockHash:    "",
		lastFeeRate:        0,
		feeRateCache:       make([]uint64, 0),
		stopChan:           stopChan,
		healthy:            &atomic.Bool{},
	}, nil
}

func (s *SOLScanner) Start() {
	lastMimirCheck := time.Now().Add(-constants.ThorchainBlockTime)
	isChainPaused := false

	initialSlot, err := s.solRpc.GetSlot()
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to get initial slot")
		return
	}

	// initialize the network fee slot multiple to the closest multiple of NetFeeUpdateInterval beneath the initial slot
	s.netFeeSlotMultiple = initialSlot / NetFeeUpdateInterval

	// attempt to initialize the network fee. In simtest environment, we will not have enough tx history
	// so the initial netfee will be sent upon first tx(s) observation
	if err := s.checkRecentBlocksAndUpdateNetworkFee(s.netFeeSlotMultiple * NetFeeUpdateInterval); err != nil {
		s.logger.Error().Err(err).Msg("failed to check recent blocks and update network fee")
	}

	for {
		select {
		case <-s.stopChan:
			s.logger.Info().Msg("stopping SOL scanner")
			return
		default:
			if time.Since(lastMimirCheck) >= constants.ThorchainBlockTime {
				isChainPaused = blockscanner.IsChainPaused(s.cfg, s.logger, s.bridge)
				lastMimirCheck = time.Now()
			}

			if isChainPaused {
				s.healthy.Store(false)
				time.Sleep(constants.ThorchainBlockTime)
				continue
			}

			vaults, err := s.bridge.GetAsgards()
			if err != nil {
				s.logger.Error().Err(err).Msg("failed to get vaults")
				time.Sleep(time.Second)
				continue
			}

			var vaultAddrs []common.Address
			for _, vault := range vaults {
				if !vault.PubKeyEddsa.IsEmpty() {
					vaultAddrSol, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
					if err != nil {
						s.logger.Error().Err(err).Msg("failed to get vault address")
						continue
					}
					vaultAddrs = append(vaultAddrs, vaultAddrSol)
				}
			}

			if err := s.updateLatestBlockHash(); err != nil {
				s.logger.Error().Err(err).Msg("failed to update latest block hash")
				// don't need to continue here because this is not necessary for anything further in this block
			}

			currentSlot, err := s.solRpc.GetSlot()
			if err != nil {
				s.logger.Error().Err(err).Msg("failed to get current slot")
				time.Sleep(time.Second)
				continue
			}

			if currentSlot > (s.netFeeSlotMultiple+1)*NetFeeUpdateInterval {
				// we crossed the threshold for updating the network fee, so check the last N blocks
				//  up to the rounded slot for deterministic behavior across nodes.
				s.netFeeSlotMultiple = currentSlot / NetFeeUpdateInterval
				if err := s.checkRecentBlocksAndUpdateNetworkFee(s.netFeeSlotMultiple * NetFeeUpdateInterval); err != nil {
					s.logger.Error().Err(err).Msg("failed to check recent blocks and update network fee")
				}
			}

			lastSlot, _ := s.db.GetScanPos()

			if currentSlot == lastSlot {
				time.Sleep(200 * time.Millisecond)
				continue
			}

			var wg sync.WaitGroup
			wg.Add(len(vaultAddrs))

			healthy := true
			var mu sync.Mutex
			setUnhealthy := func() {
				mu.Lock()
				defer mu.Unlock()
				healthy = false
			}

			var newLastSigs = make([]string, len(vaultAddrs))
			var newLastSlots = make([]uint64, len(vaultAddrs))
			for i, vaultAddr := range vaultAddrs {
				lastSig, vaultLastSlot, _ := s.db.GetScanStatus(vaultAddr.String())

				i := i

				go func() {
					defer wg.Done()

					minContextSlot := max(lastSlot, vaultLastSlot, uint64(s.cfg.StartBlockHeight))

					res, err := s.solRpc.GetSignaturesForAddressUntil(vaultAddr.String(), minContextSlot, lastSig)
					if err != nil {
						s.logger.Error().Err(err).Msgf("failed to get signatures for address %s", vaultAddr.String())
						setUnhealthy()
						return
					}

					if len(res) == 0 {
						s.logger.Debug().Msgf("no new signatures for address %s", vaultAddr.String())
						return
					}

					var txWg sync.WaitGroup

					for _, sig := range res {
						sig := sig
						txWg.Add(1)
						go func() {
							defer txWg.Done()
							tx, err := s.solRpc.GetTransaction(sig.Signature)
							if err != nil {
								s.logger.Error().Err(err).Msgf("failed to get transaction %s", sig.Signature)
								setUnhealthy()
								return
							}

							if tx.Meta.Err != nil {
								s.logger.Debug().Msgf("transaction %s failed: %s", sig.Signature, tx.Meta.Err)
								return
							}

							s.globalTxsQueue <- s.getTxIn(sig.Slot, []*rpc.TransactionResult{tx})
						}()
					}
					txWg.Wait()

					newLastSigs[i] = res[0].Signature
					newLastSlots[i] = res[0].Slot
				}()
			}
			wg.Wait()

			s.healthy.Store(healthy)

			for i, vaultAddr := range vaultAddrs {
				sig := newLastSigs[i]
				if sig != "" {
					s.db.SetScanStatus(vaultAddr.String(), sig, newLastSlots[i])
				}
			}

			if !s.netFeeInit {
				// this should really only happen (and is only necessary) in test environments to initialize the network fee,
				// because we expect to have enough observed txs in the checkRecentBlocksAndUpdateNetworkFee above.
				s.sendNetworkFeeIfNecessary(currentSlot)
			}
			if err = s.solvencyReporter(int64(currentSlot)); err != nil {
				s.logger.Err(err).Msg("fail to send solvency to THORChain")
			}

			if (time.Since(s.lastLog) > (5 * time.Second)) || !healthy {
				s.lastLog = time.Now()
				s.logger.Info().
					Uint64("block height", currentSlot).
					Int("gap", 0).
					Bool("healthy", healthy).
					Msg("scan block")
			}

			if lastSlot > 0 {
				s.m.GetCounter(metrics.TotalBlockScanned).Add(float64(currentSlot - lastSlot))
			}

			if err := s.db.SetScanPos(currentSlot); err != nil {
				s.logger.Error().Err(err).Msg("failed to set scan position")
			}
		}
	}
}

// Since we don't scan all txs in every block, in order to get a good perspective of the network fee
// we need to check the last N blocks and calculate the median fee rate.
func (s *SOLScanner) checkRecentBlocksAndUpdateNetworkFee(slot uint64) error {
	defer s.sendNetworkFeeIfNecessary(slot)

	numTxs := 0

	for i := slot; i > 0; i-- {
		block, err := s.solRpc.GetBlock(i)
		if err != nil {
			s.logger.Error().Err(err).Msg("failed to get block to determine netfee")
			continue
		}

		txIn := s.getTxIn(i, block.Transactions)
		numTxs += len(txIn.TxArray)

		if numTxs > s.cfg.GasCacheBlocks {
			break
		}
	}

	if numTxs < s.cfg.GasCacheBlocks {
		return fmt.Errorf("not enough transactions")
	}

	return nil
}

// GetHeight returns the current block height of Solana
func (s *SOLScanner) GetHeight() (int64, error) {
	height, err := s.solRpc.GetSlot()
	if err != nil {
		return -1, err
	}
	return int64(height), nil
}

// ScanHeight returns the current scanned height
func (s *SOLScanner) ScanHeight() (int64, error) {
	lastSlot, _ := s.db.GetScanPos()
	return int64(lastSlot), nil
}

// ScanHeight returns the current scanned height
func (s *SOLScanner) IsHealthy() bool {
	return s.healthy.Load()
}

// GetLatestBlockHash returns the latest block hash
func (s *SOLScanner) GetLatestBlockHash() string {
	s.latestBlockMu.Lock()
	defer s.latestBlockMu.Unlock()
	return s.latestBlockHash
}

// UpdateLatestBlockHash updates the local latest block hash to be used in tx signing
func (s *SOLScanner) updateLatestBlockHash() error {
	s.latestBlockMu.Lock()
	defer s.latestBlockMu.Unlock()
	hash, err := s.solRpc.GetLatestBlockhash()
	if err != nil {
		return err
	}
	s.latestBlockHash = hash
	return nil
}

// GetNetworkFee returns current chain network fee according to Bifrost.
func (s *SOLScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	return 1, s.lastFeeRate
}

func (s *SOLScanner) sendNetworkFeeIfNecessary(height uint64) {
	if len(s.feeRateCache) == 0 {
		// Not enough txs yet
		return
	}

	if len(s.feeRateCache) < s.cfg.GasCacheBlocks && s.netFeeInit {
		// We are beyond the initial fee post, but not enough txs yet
		return
	}

	// Calculate the median fee rate from the gas cache

	medianFeeRate := calculateMedian(s.feeRateCache)

	// round medianFeeRate to the nearest resolution
	resolution := uint64(s.cfg.GasPriceResolution)
	roundedFeeRate := ((medianFeeRate + (resolution / 2)) / resolution) * resolution

	// truncate gas prices older than our max cached transactions
	if len(s.feeRateCache) > s.cfg.GasCacheBlocks {
		s.feeRateCache = s.feeRateCache[(len(s.feeRateCache) - s.cfg.GasCacheBlocks):]
	}

	// Update the fee rate if it has changed
	if roundedFeeRate == s.lastFeeRate {
		return
	}

	if roundedFeeRate == 0 {
		s.logger.Debug().Msg("network fee is 0")
		return
	}

	s.netFeeInit = true

	s.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           common.SOLChain,
		Height:          int64(height),
		TransactionSize: 1,
		TransactionRate: roundedFeeRate,
	}

	s.lastFeeRate = roundedFeeRate
	s.logger.Info().
		Uint64("fee", roundedFeeRate).
		Uint64("height", height).
		Msg("sent network fee to THORChain")
}

// --------------------------------- extraction ---------------------------------

// Gets the TxIn struct for a block
func (s *SOLScanner) getTxIn(slot uint64, transactions []*rpc.TransactionResult) stypes.TxIn {
	txIn := stypes.TxIn{
		Chain:                s.cfg.ChainID,
		TxArray:              make([]*stypes.TxInItem, 0),
		MemPool:              false,
		ConfirmationRequired: 1, // Assuming 1 confirmation is required
	}

	// Stores each transaction's fee rate (lamports per signature)
	txFeeRates := []uint64{}

	for _, txn := range transactions {
		// Skip transactions with no signatures or no fee
		if len(txn.Transaction.Signatures) == 0 || txn.Meta.Fee == 0 {
			continue
		}

		txItem := s.getTxInItem(txn, int64(slot))
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
func (s *SOLScanner) getTxInItem(txn *rpc.TransactionResult, blockHeight int64) *stypes.TxInItem {
	memo := ""
	netBalanceChange := make(map[string]int64)
	fee := txn.Meta.Fee

	instructions := txn.Transaction.GetInstructions()

	for _, instruction := range instructions {
		programID := txn.Transaction.GetAccountAtIndex(instruction.ProgramIdIndex)

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
