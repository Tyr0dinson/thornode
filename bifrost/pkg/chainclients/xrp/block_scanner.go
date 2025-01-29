package xrp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	sdkmath "cosmossdk.io/math"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"

	xrplcommon "github.com/Peersyst/xrpl-go/xrpl/queries/common"
	"github.com/Peersyst/xrpl-go/xrpl/queries/ledger"
	requests "github.com/Peersyst/xrpl-go/xrpl/queries/transactions"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/transaction"
	txtypes "github.com/Peersyst/xrpl-go/xrpl/transaction/types"
)

// SolvencyReporter is to report solvency info to THORNode
type SolvencyReporter func(int64) error

const (
	// FeeUpdatePeriodBlocks is the block interval at which we report fee changes.
	FeeUpdatePeriodBlocks = 10

	// FeeCacheTransactions is the number of transactions over which we compute an average
	// (mean) fee price to use for outbound transactions. Note that only transactions
	// using the chain fee asset will be considered.
	FeeCacheTransactions = 100
)

var (
	ErrInvalidScanStorage = errors.New("scan storage is empty or nil")
	ErrInvalidMetrics     = errors.New("metrics is empty or nil")
	ErrEmptyTx            = errors.New("empty tx")
)

// XrpBlockScanner is to scan the blocks
type XrpBlockScanner struct {
	cfg              config.BifrostBlockScannerConfiguration
	logger           zerolog.Logger
	db               blockscanner.ScannerStorage
	bridge           thorclient.ThorchainBridge
	solvencyReporter SolvencyReporter
	rpcClient        *rpc.Client

	// feeCache contains a rolling window of suggested fees.
	// Fees are stored at 100x the values on the observed chain due to compensate for the
	// difference in base chain decimals (thorchain:1e8, gaia:1e6).
	feeCache []sdkmath.Uint
	lastFee  sdkmath.Uint
}

// NewXrpBlockScanner create a new instance of BlockScan
func NewXrpBlockScanner(rpcHost string,
	cfg config.BifrostBlockScannerConfiguration,
	scanStorage blockscanner.ScannerStorage,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	solvencyReporter SolvencyReporter,
) (*XrpBlockScanner, error) {
	if scanStorage == nil {
		return nil, errors.New("scanStorage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics is nil")
	}

	logger := log.Logger.With().Str("module", "blockscanner").Str("chain", cfg.ChainID.String()).Logger()

	rpcConfig, err := rpc.NewClientConfig(rpcHost)
	if err != nil {
		return nil, fmt.Errorf("unable to create rpc config, %w", err)
	}
	rpcClient := rpc.NewClient(rpcConfig)
	return &XrpBlockScanner{
		cfg:              cfg,
		logger:           logger,
		db:               scanStorage,
		rpcClient:        rpcClient,
		feeCache:         make([]sdkmath.Uint, 0),
		lastFee:          sdkmath.NewUint(0),
		bridge:           bridge,
		solvencyReporter: solvencyReporter,
	}, nil
}

// GetHeight returns the index of the most recently validated ledger.
func (c *XrpBlockScanner) GetHeight() (int64, error) {
	ledgerIndex, err := c.rpcClient.GetLedgerIndex()
	if err != nil {
		return 0, err
	}

	return int64(ledgerIndex.Int()), nil
}

// FetchMemPool returns nothing since we are only concerned about finalized transactions in Xrp
func (c *XrpBlockScanner) FetchMemPool(height int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (c *XrpBlockScanner) updateFeeCache(fee common.Coin) {
	// sanity check to ensure fee is non-zero
	err := fee.Valid()
	if err != nil {
		c.logger.Err(err).Interface("fee", fee).Msg("transaction with zero fee")
		return
	}

	// add the fee to our cache
	c.feeCache = append(c.feeCache, fee.Amount)

	// truncate fee prices older than our max cached transactions
	if len(c.feeCache) > FeeCacheTransactions {
		c.feeCache = c.feeCache[(len(c.feeCache) - FeeCacheTransactions):]
	}
}

func (c *XrpBlockScanner) averageFee() sdkmath.Uint {
	// avoid divide by zero
	if len(c.feeCache) == 0 {
		return sdkmath.NewUint(0)
	}

	// compute mean
	sum := sdkmath.NewUint(0)
	for _, val := range c.feeCache {
		sum = sum.Add(val)
	}
	mean := sum.Quo(sdkmath.NewUint(uint64(len(c.feeCache))))

	return mean
}

func (c *XrpBlockScanner) updateFees(height int64) error {
	// post the gas fee over every cache period when we have a full gas cache
	if height%FeeUpdatePeriodBlocks == 0 && len(c.feeCache) == FeeCacheTransactions {
		avgFee := c.averageFee()

		// sanity check the fee is not zero
		if avgFee.IsZero() {
			return errors.New("suggested gas fee was zero")
		}

		// skip fee update if it has not changed
		if c.lastFee.Equal(avgFee) {
			return nil
		}

		// NOTE: We post the fee to the network instead of the transaction rate, and set the
		// transaction size 1 to ensure the MaxGas in the generated TxOut contains the
		// correct fee. We cannot pass the proper size and rate without a deeper change to
		// Thornode, as the rate on Cosmos chains is less than 1 and cannot be represented
		// by the uint.
		feeTx, err := c.bridge.PostNetworkFee(height, c.cfg.ChainID, 1, avgFee.Uint64())
		if err != nil {
			return err
		}
		c.lastFee = avgFee
		c.logger.Info().
			Str("tx", feeTx.String()).
			Uint64("fee", avgFee.Uint64()).
			Int64("height", height).
			Msg("sent network fee to THORChain")
	}

	return nil
}

func (c *XrpBlockScanner) processTxs(height int64, rawTxs []transaction.FlatTransaction) ([]types.TxInItem, error) {
	var txIn []types.TxInItem
	for _, rawTx := range rawTxs {
		//c.logger.Info().Interface("raw tx in", rawTx).Msg("processTxs()")

		// tx is nil, maybe not have been validated
		if rawTx == nil {
			continue
		}

		meta, err := c.decodeMetaBlobIfNecessary(rawTx)
		if err != nil {
			c.logger.Info().AnErr("error", err).Msg("fail to decode meta")
			continue
		}
		//c.logger.Info().Interface("meta", meta).Msg("processTxs() meta decoded")

		// Ignore failed transactions
		if meta["TransactionResult"] != "tesSUCCESS" {
			continue
		}

		ctxLog := c.logger.Info().Interface("tx", rawTx)
		flatTx, err := c.decodeTxBlobIfNecessary(rawTx)
		if err != nil {
			ctxLog.AnErr("error", err).Msg("fail to decode tx blob")
			continue
		}
		//c.logger.Info().Interface("flat tx in", flatTx).Msg("processTxs()")

		payment, err := c.processPayment(flatTx)
		if payment == nil && err == nil {
			// This was not a payment tx
			continue
		}
		if err != nil {
			ctxLog.AnErr("reason", err).Msg("skipping payment tx")
			continue
		}

		fee, err := fromXrpToThorchain(payment.Fee)
		if err != nil {
			return nil, fmt.Errorf("cannot convert xrp fee to thorchain fee: %w", err)
		}
		c.updateFeeCache(fee)

		hash, ok := rawTx["hash"].(string)
		if !ok {
			ctxLog.Msg("skipping tx, cannot cast hash to string")
			continue
		}

		memo := ""
		if len(payment.Memos) == 1 {
			memo = payment.Memos[0].Memo.MemoData
		}

		amount, err := c.getDeliveredAmount(flatTx, meta)
		if err != nil {
			ctxLog.AnErr("reason", err).Msg("fail getting delivered amount")
			continue
		}

		// Verify that the amount is of type XRP, issuer currencies are not currently supported.
		// Without any trust lines, this shouldn't be possible.
		if amount.Kind() != txtypes.XRP {
			ctxLog.Msg("skipping tx, amount is not of type XRP")
			continue
		}
		coin, err := fromXrpToThorchain(amount)
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot convert xrp amount to thorchain amount")
			continue
		}
		coins := common.Coins{coin}

		txIn = append(txIn, types.TxInItem{
			Tx:          hash,
			BlockHeight: height,
			Memo:        memo,
			Sender:      payment.Account.String(),
			To:          payment.Destination.String(),
			Coins:       coins,
			Gas:         []common.Coin{fee},
		})
	}

	return txIn, nil
}

// The expected response from the ledger method.
type LedgerResponseWithTxHashes struct {
	Ledger struct {
		Transactions []string `json:"transactions,omitempty"`
	} `json:"ledger"`
	LedgerHash  string                 `json:"ledger_hash"`
	LedgerIndex xrplcommon.LedgerIndex `json:"ledger_index"`
	Validated   bool                   `json:"validated,omitempty"`
}

func (c *XrpBlockScanner) FetchTxs(height, chainHeight int64) (types.TxIn, error) {
	// First retrieve all transaction hashes in block
	res, err := c.rpcClient.Request(&ledger.Request{
		LedgerIndex:  xrplcommon.LedgerIndex(height),
		Transactions: true,
		Expand:       false,
	})
	if err != nil {
		return types.TxIn{}, err
	}
	var ledgerTxHashes LedgerResponseWithTxHashes
	err = res.GetResult(&ledgerTxHashes)
	if err != nil {
		return types.TxIn{}, err
	}

	// Verify ledger has been validated, it should be
	if !ledgerTxHashes.Validated {
		return types.TxIn{}, btypes.ErrUnavailableBlock
	}

	// Next, get all transactions in block
	ledger, err := c.rpcClient.GetLedger(&ledger.Request{
		LedgerIndex:  xrplcommon.LedgerIndex(height),
		Transactions: true,
		Expand:       true,
		Binary:       true,
	})
	if err != nil {
		return types.TxIn{}, err
	}

	// Verify ledger has been validated, it should be
	if !ledger.Validated {
		return types.TxIn{}, btypes.ErrUnavailableBlock
	}

	// Verify that we could fetch all transactions, if not, fetch remaining individually.
	flatTransactions := ledger.Ledger.Transactions
	if len(ledgerTxHashes.Ledger.Transactions) > len(flatTransactions) {
		c.logger.Info().Int("from ledger with just hashes", len(ledgerTxHashes.Ledger.Transactions)).
			Int("from ledger with expanded txs", len(flatTransactions)).Msg("number of transactions don't match")
		// We did not get all transactions, most likely due to a server side limitation,
		// i.e.: max response size setting, new tx amout limitation for binary==true, remote/centralized server.
		// Retrieve each tx individually, enough load could get us behind, but better behind than missing txs.
		// TODO SAM127: pass in ledger with expanded txs, match first and don't query those
		flatTransactions, err = c.fetchTxsByHash(ledgerTxHashes.Ledger.Transactions, flatTransactions)
		if err != nil {
			return types.TxIn{}, err
		}
	}

	txs, err := c.processTxs(height, flatTransactions)
	if err != nil {
		return types.TxIn{}, err
	}

	txIn := types.TxIn{
		Count:    strconv.Itoa(len(txs)),
		Chain:    c.cfg.ChainID,
		TxArray:  txs,
		Filtered: false,
		MemPool:  false,
	}

	// skip reporting network fee and solvency if block more than flexibility blocks from tip
	if chainHeight-height > c.cfg.ObservationFlexibilityBlocks {
		return txIn, nil
	}

	err = c.updateFees(height)
	if err != nil {
		c.logger.Err(err).Int64("height", height).Msg("unable to update network fee")
	}

	if err = c.solvencyReporter(height); err != nil {
		c.logger.Err(err).Msg("fail to send solvency to THORChain")
	}

	return txIn, nil
}

const (
	workerBatchSize = 10
)

func (c *XrpBlockScanner) fetchTxsByHash(txHashes []string, txs []transaction.FlatTransaction) ([]transaction.FlatTransaction, error) {
	// Collect results while maintaining order
	results := make(map[string]transaction.FlatTransaction)
	
	// Populate with transactions that we have and don't want to query again
	for _, tx := range txs {
		// We verified we got this tx from a validated ledger, set per tx
		tx["validated"] = true
		hash, ok := tx["hash"].(string)
		if !ok {
			continue
		}
		results[hash] = tx
	}

	ctx := context.Background()
	// Create channels for parallel processing
	txChan := make(chan string, len(txHashes))
	resultChan := make(chan *txResult, len(txHashes))

	// Start worker pool
	workerCount := min(workerBatchSize, len(txHashes))
	var wg sync.WaitGroup

	// Launch workers
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go c.txWorker(ctx, &wg, txChan, resultChan)
	}

	// Feed transaction hashes to workers
	go func() {
		for _, hash := range txHashes {
			// Only feed in hashes for transaction we don't already have.
			if results[hash] != nil {
				continue
			}
			txChan <- hash
		}
		close(txChan)
	}()

	// Wait for workers in a separate goroutine
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var firstError error
	for i := 0; i < len(txHashes); i++ {
		select {
		case result := <-resultChan:
			if result != nil {
				if result.err != nil && firstError == nil {
					firstError = result.err
					continue
				}
				results[result.hash] = result.tx
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if firstError != nil {
		return nil, firstError
	}

	// Preserve original transaction order
	transactions := make([]transaction.FlatTransaction, len(txHashes))
	for i, hash := range txHashes {
		result := results[hash]
		validated, ok := result["validated"].(bool)
		if ok && validated {
			transactions[i] = result
		}
	}

	return transactions, nil
}

type txResult struct {
	hash string
	tx   transaction.FlatTransaction
	err  error
}

func (c *XrpBlockScanner) txWorker(ctx context.Context, wg *sync.WaitGroup, txChan <-chan string, resultChan chan<- *txResult) {
	defer wg.Done()

	for txHash := range txChan {
		select {
		case <-ctx.Done():
			return
		default:
			var txResponse transaction.FlatTransaction

			result := &txResult{
				hash: txHash,
			}
			// Request the transaction from the server
			res, err := c.rpcClient.Request(&requests.TxRequest{
				Transaction: txHash,
				Binary:      false,
			})
			if err == nil {
				err = res.GetResult(&txResponse)
				if err == nil {
					result.tx = txResponse
				} else {
					c.logger.Err(err).Str("hash", txHash).Msg("error parsing tx results in txWorker")
				}
			} else {
				c.logger.Err(err).Str("hash", txHash).Msg("error requesting tx in txWorker")
			}
			result.err = err
			resultChan <- result
		}
	}
}
