package terra

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/grpc/tmservice"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"

	ctypes "github.com/cosmos/cosmos-sdk/types"
	btypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tendermint/tendermint/crypto/tmhash"
	tmtypes "github.com/tendermint/tendermint/proto/tendermint/types"
	grpc "google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/bifrost/config"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/common"
)

// SolvencyReporter is to report solvency info to THORNode
type SolvencyReporter func(int64) error

const (
	// GasUpdatePeriodBlocks is the block interval at which we report gas fee changes.
	GasUpdatePeriodBlocks = 10

	// GasPriceFactor is a multiplier applied to the gas amount before dividing by the gas
	// limit to determine the price, since Cosmos chains can have prices less than 1 which
	// cannot be stored in a uint. This can be thought of as the reciprocal of the gas
	// price precision.
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

// CosmosBlockScanner is to scan the blocks
type CosmosBlockScanner struct {
	cfg              config.BlockScannerConfiguration
	logger           zerolog.Logger
	feeAsset         common.Asset
	db               blockscanner.ScannerStorage
	cdc              *codec.ProtoCodec
	txClient         txtypes.ServiceClient
	txConfig         client.TxConfig
	gasCache         []ctypes.Uint
	tmService        tmservice.ServiceClient
	grpc             *grpc.ClientConn
	bridge           *thorclient.ThorchainBridge
	solvencyReporter SolvencyReporter
}

// NewCosmosBlockScanner create a new instance of BlockScan
func NewCosmosBlockScanner(cfg config.BlockScannerConfiguration,
	scanStorage blockscanner.ScannerStorage,
	bridge *thorclient.ThorchainBridge,
	m *metrics.Metrics,
	solvencyReporter SolvencyReporter) (*CosmosBlockScanner, error) {
	if scanStorage == nil {
		return nil, errors.New("scanStorage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics is nil")
	}

	logger := log.Logger.With().Str("module", "blockscanner").Str("chain", common.TERRAChain.String()).Logger()

	registry := bridge.GetContext().InterfaceRegistry
	btypes.RegisterInterfaces(registry)

	host := strings.ReplaceAll(cfg.RPCHost, "http://", "")
	conn, err := grpc.Dial(host, grpc.WithInsecure())
	if err != nil {
		logger.Fatal().Err(err).Msg("fail to dial")
	}

	tmService := tmservice.NewServiceClient(conn)
	cdc := codec.NewProtoCodec(registry)

	interfaceRegistry := codectypes.NewInterfaceRegistry()
	interfaceRegistry.RegisterImplementations((*ctypes.Msg)(nil), &btypes.MsgSend{})
	marshaler := codec.NewProtoCodec(interfaceRegistry)
	txConfig := tx.NewTxConfig(marshaler, []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})

	feeAsset := common.TERRAChain.GetGasAsset()
	return &CosmosBlockScanner{
		cfg:              cfg,
		logger:           logger,
		db:               scanStorage,
		feeAsset:         feeAsset,
		cdc:              cdc,
		txClient:         txtypes.NewServiceClient(conn),
		txConfig:         txConfig,
		tmService:        tmService,
		gasCache:         make([]ctypes.Uint, 0),
		grpc:             conn,
		bridge:           bridge,
		solvencyReporter: solvencyReporter,
	}, nil
}

func (c *CosmosBlockScanner) GetHeight() (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	resultHeight, err := c.tmService.GetLatestBlock(
		ctx,
		&tmservice.GetLatestBlockRequest{})
	if err != nil {
		return 0, err
	}

	return resultHeight.Block.Header.Height, nil
}

func (c *CosmosBlockScanner) FetchMemPool(height int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

// GetBlock returns a Tendermint block as a reference to a ResultBlock for a
// given height. An error is returned upon query failure.
func (c *CosmosBlockScanner) GetBlock(height int64) (*tmtypes.Block, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	resultBlock, err := c.tmService.GetBlockByHeight(
		ctx,
		&tmservice.GetBlockByHeightRequest{Height: height})
	if err != nil {
		c.logger.Error().Int64("height", height).Msgf("failed to get block: %v", err)
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	return resultBlock.Block, nil
}

func (c *CosmosBlockScanner) updateGasCache(tx ctypes.FeeTx) {
	fees := tx.GetFee()

	// only consider transactions that have a single fee
	if len(fees) != 1 {
		return
	}

	// only consider transactions with fee paid in uluna
	coin, err := fromCosmosToThorchain(fees[0])
	if err != nil || !coin.Asset.Equals(c.feeAsset) {
		return
	}

	// sanity check to ensure fee is non-zero
	err = coin.Valid()
	if err != nil {
		c.logger.Error().Err(err).Interface("fees", fees).Msg("transaction with zero fee")
		return
	}

	// divide fee amount by the transaction gas used to determine the price
	c.gasCache = append(c.gasCache, coin.Amount.Mul(ctypes.NewUint(GasPriceFactor)).Quo(ctypes.NewUint(tx.GetGas())))

	// truncate gas prices older than our max cached transactions
	if len(c.gasCache) > GasCacheTransactions {
		c.gasCache = c.gasCache[(len(c.gasCache) - GasCacheTransactions):]
	}
}

func (c *CosmosBlockScanner) averageGasPrice() ctypes.Uint {
	sum := ctypes.NewUint(0)
	for _, val := range c.gasCache {
		sum = sum.Add(val)
	}
	return sum.Quo(ctypes.NewUint(uint64(len(c.gasCache))))
}

func (c *CosmosBlockScanner) updateGasFees(height int64) error {
	// post the gas fee over every cache period when we have a full gas cache
	if height%GasUpdatePeriodBlocks == 0 && len(c.gasCache) == GasCacheTransactions {
		gasPrice := c.averageGasPrice()

		// sanity check the price is not zero
		if gasPrice.Equal(ctypes.NewUint(0)) {
			err := errors.New("suggested gas price was zero")
			c.logger.Error().Err(err).Msg(err.Error())
			return err
		}

		feeTx, err := c.bridge.PostNetworkFee(height, common.TERRAChain, GasLimit, gasPrice.Uint64())
		if err != nil {
			return err
		}
		c.logger.Info().
			Str("tx", feeTx.String()).
			Uint64("price", gasPrice.Uint64()).
			Int64("height", height).
			Msg("sent network fee to THORChain")
	}

	return nil
}

func (c *CosmosBlockScanner) processTxs(height int64, rawTxs [][]byte) ([]types.TxInItem, error) {
	decoder := tx.DefaultTxDecoder(c.cdc)
	var txs []types.TxInItem

	for _, rawTx := range rawTxs {
		hash := hex.EncodeToString(tmhash.Sum(rawTx))
		tx, err := decoder(rawTx)
		if err != nil {
			if strings.Contains(err.Error(), "unable to resolve type URL") {
				// couldn't find msg type in the interface registry, probably not relevant
				if !strings.Contains(err.Error(), "MsgSend") {
					// double check to make sure MsgSend isn't mentioned
					// if it's not, we can safely ignore
					continue
				}
			}
			// else we should log this as an error and continue
			c.logger.Error().Str("tx", string(rawTx)).Err(err).Msg("unable to decode msg")
			continue
		}

		memo := tx.(ctypes.TxWithMemo).GetMemo()
		fees := tx.(ctypes.FeeTx).GetFee()

		// record gas price across all transaction types
		c.updateGasCache(tx.(ctypes.FeeTx))

		for _, msg := range tx.GetMsgs() {
			switch msg := msg.(type) {
			case *btypes.MsgSend:
				coins := common.Coins{}
				for _, coin := range msg.Amount {

					if _, whitelisted := GetAssetByCosmosDenom(coin.Denom); !whitelisted {
						c.logger.Debug().Str("tx", hash).Interface("coins", c).Msg("coin is not whitelisted, skipping")
						continue
					}
					cCoin, err := fromCosmosToThorchain(coin)
					if err != nil {
						c.logger.Warn().Err(err).Interface("coins", c).Msg("wasn't able to convert coins that passed whitelist")
						continue
					}
					coins = append(coins, cCoin)
				}

				// ignore the tx when no coins exist
				if coins.IsEmpty() {
					continue
				}

				gasFees := common.Gas{}
				for _, fee := range fees {
					cCoin, err := fromCosmosToThorchain(fee)
					if err != nil {
						c.logger.Warn().Err(err).Interface("fees", fees).Msg("wasn't able to convert coins that passed whitelist")
						continue
					}
					gasFees = append(gasFees, cCoin)
				}

				txs = append(txs, types.TxInItem{
					Tx:          hash,
					BlockHeight: height,
					Memo:        memo,
					Sender:      msg.FromAddress,
					To:          msg.ToAddress,
					Coins:       coins,
					Gas:         gasFees,
				})

			default:
				continue
			}
		}
	}

	return txs, nil
}

func (c *CosmosBlockScanner) FetchTxs(height int64) (types.TxIn, error) {
	block, err := c.GetBlock(height)
	if err != nil {
		return types.TxIn{}, err
	}

	txs, err := c.processTxs(height, block.Data.Txs)
	if err != nil {
		c.logger.Err(err).Int64("height", height).Msg("failed to processTxs")
		return types.TxIn{}, err
	}

	txIn := types.TxIn{
		Count:    strconv.Itoa(len(txs)),
		Chain:    common.TERRAChain,
		TxArray:  txs,
		Filtered: false,
		MemPool:  false,
	}

	err = c.updateGasFees(height)
	if err != nil {
		c.logger.Err(err).Int64("height", height).Msg("unable to update network fee")
	}

	return txIn, nil
}
