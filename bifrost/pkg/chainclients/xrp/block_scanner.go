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

func (c *XrpBlockScanner) processPayment(flatTx map[string]any) (*transaction.Payment, error) {
	// Ignore any txs other than payments
	if flatTx["TransactionType"] != "Payment" {
		return nil, nil
	}

	feeStr, ok := flatTx["Fee"].(string)
	if !ok {
		return nil, fmt.Errorf("cannot cast fee to string")
	}
	feeAmount, err := cosmos.ParseUint(feeStr)
	if err != nil {
		return nil, fmt.Errorf("cannot convert fee string to uint: %w", err)
	}

	var memoData []byte
	if flatTx["Memos"] != nil {
		// trunk-ignore(golangci-lint/govet): shadow
		memos, ok := flatTx["Memos"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("cannot cast memos to []interface{}")
		}
		if len(memos) != 1 {
			return nil, fmt.Errorf("more than 1 memo exists")
		}
		memoObj, ok := memos[0].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("cannot cast memos[0] to map[string]interface{}")
		}
		memo, ok := memoObj["Memo"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("cannot cast memo to map[string]interface{}")
		}
		memoDataHex, ok := memo["MemoData"].(string)
		if !ok {
			return nil, fmt.Errorf("cannot cast MemoData to string")
		}
		if memoDataHex == "" {
			return nil, fmt.Errorf("MemoData is empty")
		}
		memoData, err = hex.DecodeString(memoDataHex)
		if err != nil {
			return nil, fmt.Errorf("cannot decode memo data")
		}
	}

	sender, ok := flatTx["Account"].(string)
	if !ok {
		return nil, fmt.Errorf("cannot cast sender/account to string")
	}

	to, ok := flatTx["Destination"].(string)
	if !ok {
		return nil, fmt.Errorf("cannot cast to/destination to string")
	}

	return &transaction.Payment{
		BaseTx: transaction.BaseTx{
			Account: txtypes.Address(sender),
			Fee: txtypes.XRPCurrencyAmount(feeAmount.Uint64()),
			Memos: []txtypes.MemoWrapper{
				{
					Memo: txtypes.Memo{
						MemoData: string(memoData),
					},
				},
			},
		},
		Destination: txtypes.Address(to),
	}, nil
}

func (c *XrpBlockScanner) processTxs(height int64, rawTxs []transaction.FlatTransaction) ([]types.TxInItem, error) {
	var txIn []types.TxInItem
	for _, rawTx := range rawTxs {
		c.logger.Info().Interface("flat tx in", rawTx).Msg("processTxs()")

		// double-check the tx has been validated
		if rawTx["validated"] != true {
			c.logger.Error().Interface("tx", rawTx).Msg("got a tx that was not validated")
			continue
		}

		// Ignore failed transactions
		meta, ok := rawTx["meta"].(map[string]any)
		if !ok {
			continue
		}
		if meta["TransactionResult"] != "tesSUCCESS" {
			continue
		}

		ctxLog := c.logger.Info().Interface("tx", rawTx)
		flatTx, ok := rawTx["tx_json"].(map[string]any)
		if !ok {
			ctxLog.Msg("skipping tx, cannot cast tx_json to map[string]any")
			continue
		}

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
		
		// Do not use the "Amount" field to find the amount transferred due to partial payment exploit.
		amountJson, err := json.Marshal(meta["delivered_amount"])
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot json marshal amount")
		}
		amount, err := txtypes.UnmarshalCurrencyAmount(amountJson)
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot unmarshal currency amount")
		}
		// Verify that the amount is of type XRP, issuer currencies are not currently supported.
		// Without any trust lines, this shouldn't be possible.
		if amount.Kind() != txtypes.XRP {
			ctxLog.Msg("skipping tx, amount is not of type XRP")
		}
		coin, err := fromXrpToThorchain(amount)
		if err != nil {
			ctxLog.AnErr("error", err).Msg("skipping tx, cannot convert xrp amount to thorchain amount")
		}
		coins := common.Coins{coin}

		hash, ok := rawTx["hash"].(string)
		if !ok {
			ctxLog.Msg("skipping tx, cannot cast hash to string")
			continue
		}

		memo := ""
		if len(payment.Memos) == 1 {
			memo = payment.Memos[0].Memo.MemoData
		}

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

func (c *XrpBlockScanner) FetchTxs(height, chainHeight int64) (types.TxIn, error) {
	ledger, err := c.rpcClient.GetLedger(&ledger.Request{
		LedgerIndex:  xrplcommon.LedgerIndex(height),
		Transactions: true,
		Expand:       true,
		//		Binary: true,
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
