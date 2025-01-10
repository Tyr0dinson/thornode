package xrp

import (
	"encoding/hex"
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

	binarycodec "github.com/Peersyst/xrpl-go/binary-codec"
	"github.com/Peersyst/xrpl-go/xrpl/hash"
	xrplcommon "github.com/Peersyst/xrpl-go/xrpl/queries/common"
	"github.com/Peersyst/xrpl-go/xrpl/queries/ledger"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/transaction"
)

// SolvencyReporter is to report solvency info to THORNode
type SolvencyReporter func(int64) error

const (
	// GasUpdatePeriodBlocks is the block interval at which we report gas fee changes.
	GasUpdatePeriodBlocks = 10

	// GasPriceFactor is a multiplier applied to the gas amount before dividing by the gas
	// limit to determine the gas price, and later used as a divisor on the final fee -
	// this avoid the integer division going to zero, and can be thought of as the
	// reciprocal of the gas price precision.
	GasPriceFactor = uint64(1e9)

	// GasLimit is the default gas limit we will use for all outbound transactions.
	GasLimit = 200000

	// GasCacheTransactions is the number of transactions over which we compute an average
	// (mean) gas price to use for outbound transactions. Note that only transactions
	// using the chain fee asset will be considered.
	GasCacheTransactions = 100
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

	// feeCache contains a rolling window of suggested gas fees which are computed as the
	// gas price paid in each observed transaction multiplied by the default GasLimit.
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

// GetHeight returns the height from the latest block minus 1
// NOTE: we must lag by one block due to a race condition fetching the block results
// Since the GetLatestBlockRequests tells what transactions will be in the block at T+1
// TODO SAM127: review the above race condition for xrp
func (c *XrpBlockScanner) GetHeight() (int64, error) {
	// TODO SAM127: rpc client should timeout
	// ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	// defer cancel()

	currentLedger, err := c.rpcClient.GetCurrentLedger()
	if err != nil {
		return 0, err
	}

	return int64(currentLedger.LedgerCurrentIndex.Uint32() - 1), nil
}

// FetchMemPool returns nothing since we are only concerned about finalized transactions in Xrp
func (c *XrpBlockScanner) FetchMemPool(height int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (c *XrpBlockScanner) updateGasCache(fee common.Coin) {
	// sanity check to ensure fee is non-zero
	err := fee.Valid()
	if err != nil {
		c.logger.Err(err).Interface("fee", fee).Msg("transaction with zero fee")
		return
	}

	// add the fee to our cache
	c.feeCache = append(c.feeCache, fee.Amount)

	// truncate gas prices older than our max cached transactions
	if len(c.feeCache) > GasCacheTransactions {
		c.feeCache = c.feeCache[(len(c.feeCache) - GasCacheTransactions):]
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

	// round the price up to avoid fee noise
	resolution := sdkmath.NewUint(uint64(c.cfg.GasPriceResolution))
	if mean.LTE(resolution) {
		return resolution
	}
	mean = mean.Sub(sdkmath.NewUint(1))
	mean = mean.Quo(resolution)
	mean = mean.Add(sdkmath.NewUint(1))
	mean = mean.Mul(resolution)

	return mean
}

func (c *XrpBlockScanner) updateGasFees(height int64) error {
	// post the gas fee over every cache period when we have a full gas cache
	if height%GasUpdatePeriodBlocks == 0 && len(c.feeCache) == GasCacheTransactions {
		gasFee := c.averageFee()

		// sanity check the fee is not zero
		if gasFee.IsZero() {
			return errors.New("suggested gas fee was zero")
		}

		// skip fee if less than 1 resolution away from the last
		feeDelta := sdkmath.MaxUint(c.lastFee, gasFee).Sub(sdkmath.MinUint(c.lastFee, gasFee))
		if feeDelta.LTE(sdkmath.NewUint(uint64(c.cfg.GasPriceResolution))) {
			return nil
		}

		// NOTE: We post the fee to the network instead of the transaction rate, and set the
		// transaction size 1 to ensure the MaxGas in the generated TxOut contains the
		// correct fee. We cannot pass the proper size and rate without a deeper change to
		// Thornode, as the rate on Cosmos chains is less than 1 and cannot be represented
		// by the uint.
		feeTx, err := c.bridge.PostNetworkFee(height, c.cfg.ChainID, 1, gasFee.Uint64())
		if err != nil {
			return err
		}
		c.lastFee = gasFee
		c.logger.Info().
			Str("tx", feeTx.String()).
			Uint64("fee", gasFee.Uint64()).
			Int64("height", height).
			Msg("sent network fee to THORChain")
	}

	return nil
}

func (c *XrpBlockScanner) processTxs(height int64, rawTxs []transaction.FlatTransaction) ([]types.TxInItem, error) {
	var txIn []types.TxInItem
	for _, rawTx := range rawTxs {
		txBlob, err := binarycodec.Encode(rawTx)
		if err != nil {
			return nil, err
		}
		hash, err := hash.SignTxBlob(txBlob)
		if err != nil {
			return nil, err
		}

		// Ignore any txs other than payments
		// TODO: verify this
		if rawTx["TransactionType"] != "Payment" {
			continue
		}

		feeStr, ok := rawTx["Fee"].(string)
		if !ok {
			continue
		}
		fee := common.Coin{
			Asset: common.XRPAsset,
			Amount: cosmos.NewUintFromString(feeStr),
			Decimals: 6,
		}

		memos := rawTx["Memos"].([]map[string]interface{})
		if len(memos) != 1 {
			continue
		}
		memo := memos[0]["Memo"].(map[string]string)
		memoDataHex := memo["MemoData"]
		if memoDataHex == "" {
			continue
		}
		memoData, err := hex.DecodeString(memoDataHex)
		if err != nil {
			continue
		}
		c.updateGasCache(fee)

		// TODO: this only supports XRP, do we want to support other currencies
		amount, ok := rawTx["Amount"].(string)
		if !ok {
			continue
		}
		coins := common.Coins{
			{
				Asset: common.XRPAsset,
				Amount: cosmos.NewUintFromString(amount),
				Decimals: 6,
			},
		}

		// TODO: Do we need to verify there were any errors / error codes? Or if it exists, it was a successful tx?
		// TODO: verify all casting is okay
		txIn = append(txIn, types.TxInItem{
			Tx:          hash,
			BlockHeight: height,
			Memo:        string(memoData),
			Sender:      rawTx["Account"].(string),
			To:          rawTx["Destination"].(string),
			Coins:       coins,
			Gas:         []common.Coin{fee},
		})
	}

	return txIn, nil
}

func (c *XrpBlockScanner) FetchTxs(height, chainHeight int64) (types.TxIn, error) {
	ledger, err := c.rpcClient.GetLedger(&ledger.Request{
		LedgerIndex: xrplcommon.LedgerIndex(height),
		Transactions: true,
		Expand: true,

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

	err = c.updateGasFees(height)
	if err != nil {
		c.logger.Err(err).Int64("height", height).Msg("unable to update network fee")
	}

	if err = c.solvencyReporter(height); err != nil {
		c.logger.Err(err).Msg("fail to send solvency to THORChain")
	}

	return txIn, nil
}
