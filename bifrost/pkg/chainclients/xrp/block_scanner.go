package xrp

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	sdkmath "cosmossdk.io/math"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"

	xrplcommon "github.com/Peersyst/xrpl-go/xrpl/queries/common"
	"github.com/Peersyst/xrpl-go/xrpl/queries/ledger"
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
	// We do not need to set the network id since this is query-only
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
		c.logger.Info().Interface("flat tx in", rawTx).Msg("processTxs()")
		
		// Ignore failed transactions
		metaData, ok := rawTx["metaData"].(map[string]any)
		if !ok {
			continue
		}
		if metaData["TransactionResult"] != "tesSUCCESS" {
			continue
		}

		// Ignore any txs other than payments
		if rawTx["TransactionType"] != "Payment" {
			continue
		}

		ctxLog := c.logger.Info().Interface("tx", rawTx)
		feeStr, ok := rawTx["Fee"].(string)
		if !ok {
			ctxLog.Msg("skipping tx, cannot cast fee to string")
			continue
		}
		feeAmount, err := cosmos.ParseUint(feeStr)
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot convert fee string to uint")
			continue
		}
		fee, err := fromXrpToThorchain(txtypes.XRPCurrencyAmount(feeAmount.Uint64()))
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot convert xrp fee to thorchain fee")
			continue
		}
		c.updateFeeCache(fee)

		var memoData []byte
		if rawTx["Memos"] != nil {
			// trunk-ignore(golangci-lint/govet): shadow
			memos, ok := rawTx["Memos"].([]interface{})
			if !ok {
				ctxLog.Msg("skipping tx, cannot cast memos to []interface{}")
				continue
			}
			if len(memos) != 1 {
				ctxLog.Msg("skipping tx, more than 1 memo exists")
				continue
			}
			memoObj, ok := memos[0].(map[string]interface{})
			if !ok {
				ctxLog.Msg("skipping tx, cannot cast memos[0] to map[string]interface{}")
				continue
			}
			memo, ok := memoObj["Memo"].(map[string]interface{})
			if !ok {
				ctxLog.Msg("skipping tx, cannot cast memo to map[string]interface{}")
				continue
			}
			memoDataHex, ok := memo["MemoData"].(string)
			if !ok {
				ctxLog.Msg("skipping tx, cannot cast MemoData to string")
				continue
			}
			if memoDataHex == "" {
				ctxLog.Msg("skipping tx, MemoData is empty")
				continue
			}
			memoData, err = hex.DecodeString(memoDataHex)
			if err != nil {
				ctxLog.AnErr("error", err).Msg("skipping tx, cannot decode memo data")
				continue
			}
		}

		amountJson, err := json.Marshal(rawTx["Amount"])
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot json marshal amount")
			continue
		}
		amount, err := txtypes.UnmarshalCurrencyAmount(amountJson)
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot unmarshal currency amount")
			continue
		}
		coin, err := fromXrpToThorchain(amount)
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot convert xrp amount to thorchain amount")
			continue
		}
		coins := common.Coins{coin}

		sender, ok := rawTx["Account"].(string)
		if !ok {
			ctxLog.Msg("skipping tx, cannot cast sender/account to string")
			continue
		}

		to, ok := rawTx["Destination"].(string)
		if !ok {
			ctxLog.Msg("skipping tx, cannot cast to/destination to string")
			continue
		}

		hash, ok := rawTx["hash"].(string)
		if !ok {
			ctxLog.Msg("skipping tx, cannot cast hash to string")
			continue
		}

		txIn = append(txIn, types.TxInItem{
			Tx:          hash,
			BlockHeight: height,
			Memo:        string(memoData),
			Sender:      sender,
			To:          to,
			Coins:       coins,
			Gas:         []common.Coin{fee},
		})
	}

	return txIn, nil
}

func (c *XrpBlockScanner) FetchTxs(height, chainHeight int64) (types.TxIn, error) {
	ledger, err := c.rpcClient.GetLedger(&ledger.Request{
		LedgerIndex:  xrplcommon.LedgerIndex(height),
		Transactions: true,
		Expand:       true,
	})
	if err != nil {
		return types.TxIn{}, err
	}

	// Verify ledger has been validated, it should be
	if !ledger.Validated {
		return types.TxIn{}, btypes.ErrUnavailableBlock
	}

	txs, err := c.processTxs(height, ledger.Ledger.Transactions)
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
