package ton

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"sync"

	"cosmossdk.io/math"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"golang.org/x/sync/errgroup"
)

// var txOutRegexp = regexp.MustCompile("^(OUT|MIGRATE|RAGNAROK):.*")

type TonBlockScanner struct {
	config     config.BifrostBlockScannerConfiguration
	logger     zerolog.Logger
	bridge     thorclient.ThorchainBridge
	api        ton.APIClientWrapped
	lastShards map[string]uint32

	feeRateCache []uint64
	lastFeeRate  uint64
	netFeeInit   bool

	globalNetworkFeeQueue chan common.NetworkFee
	testnet               bool
}

func NewTonBlockScanner(
	cfg config.BifrostBlockScannerConfiguration,
	bridge thorclient.ThorchainBridge,
	api ton.APIClientWrapped,
	testnet bool,
) (*TonBlockScanner, error) {
	return &TonBlockScanner{
		config: cfg,
		logger: log.Logger.With().
			Str("module", "blockscanner").
			Str("chain", cfg.ChainID.String()).
			Logger(),
		bridge:       bridge,
		api:          api,
		testnet:      testnet,
		lastFeeRate:  0,
		feeRateCache: make([]uint64, 0),
		lastShards:   make(map[string]uint32),
	}, nil
}

// Return the latest block height from master chain
func (s *TonBlockScanner) GetHeight() (int64, error) {
	info, err := s.api.GetMasterchainInfo(context.Background())
	if err != nil {
		return 0, fmt.Errorf("failed to get masterchain info: %w", err)
	}

	// at least return current block height -1
	return int64(info.SeqNo) - max(ConfirmationBlocks, 1), nil
}

func (s *TonBlockScanner) FetchMemPool(_ int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (s *TonBlockScanner) FetchTxs(
	fetchHeight, _ int64,
) (types.TxIn, error) {
	var txInItems []*types.TxInItem

	if fetchHeight < 1 {
		return types.TxIn{}, nil
	}

	items, err := s.processMaster(uint32(fetchHeight))
	if err != nil {
		s.logger.
			Err(err).
			Int64("height", fetchHeight).
			Msg("failed to process master block")
		return types.TxIn{}, err
	}

	txInItems = append(txInItems, items...)

	for i := range txInItems {
		txInItems[i].BlockHeight = fetchHeight
	}

	txIn := types.TxIn{
		Chain:   s.config.ChainID,
		TxArray: txInItems,
	}

	if fetchHeight%20 == 0 {
		s.sendNetworkFeeIfNecessary(fetchHeight)
	}

	return txIn, nil
}

// GetNetworkFee returns current chain network fee according to Bifrost.
func (s *TonBlockScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	return 1, s.lastFeeRate
}

func (s *TonBlockScanner) sendNetworkFeeIfNecessary(height int64) {
	if len(s.feeRateCache) == 0 {
		// Not enough txs yet
		return
	}

	if len(s.feeRateCache) < s.config.GasCacheBlocks && s.netFeeInit {
		// We are beyond the initial fee post, but not enough txs yet
		return
	}

	// Calculate the median fee rate from the gas cache

	medianFeeRate := calculateMedian(s.feeRateCache)

	// round medianFeeRate to the nearest resolution
	resolution := uint64(s.config.GasPriceResolution)
	roundedFeeRate := ((medianFeeRate + (resolution / 2)) / resolution) * resolution

	// truncate gas prices older than our max cached transactions
	if len(s.feeRateCache) > s.config.GasCacheBlocks {
		s.feeRateCache = s.feeRateCache[(len(s.feeRateCache) - s.config.GasCacheBlocks):]
	}

	if roundedFeeRate == 0 {
		s.logger.Debug().Msg("network fee is 0")
		return
	}

	s.netFeeInit = true

	// Update the fee rate if it has changed
	if roundedFeeRate == s.lastFeeRate {
		s.logger.Info().Uint64("fee", roundedFeeRate).Msg("network fee is already up to date")
		return
	}

	s.lastFeeRate = roundedFeeRate

	s.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           common.TONChain,
		Height:          height,
		TransactionSize: 1,
		TransactionRate: roundedFeeRate,
	}

	s.logger.Info().
		Uint64("fee", roundedFeeRate).
		Int64("height", height).
		Msg("sent network fee to THORChain")
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

func (s *TonBlockScanner) processMaster(masterSeqno uint32) ([]*types.TxInItem, error) {
	master, err := s.api.LookupBlock(
		context.Background(),
		-1,
		-9223372036854775808,
		masterSeqno,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup master block seq %d: %w", masterSeqno, err)
	}

	shards, err := s.api.GetBlockShardsInfo(context.Background(), master)
	if err != nil {
		return nil, fmt.Errorf("failed to get shards for master block %d: %w", masterSeqno, err)
	}

	// sort.Slice(shards, func(i, j int) bool {
	// 	return shards[i].Shard < shards[j].Shard
	// })

	var txInItems []*types.TxInItem
	var mu sync.Mutex
	var eg errgroup.Group

	for _, shard := range shards {
		shardSeqno, found := s.lastShards[shardId(shard)]
		if found {
			shardSeqno++
		} else {
			shardSeqno = shard.SeqNo
		}

		for ; shardSeqno <= shard.SeqNo; shardSeqno++ {
			sh := shard.Copy()
			sh.SeqNo = shardSeqno
			eg.Go(func() error {
				items, err := s.processShard(masterSeqno, sh)
				if err != nil {
					return fmt.Errorf("failed to process shard %d: %w", sh.SeqNo, err)
				}
				if len(items) == 0 {
					return nil
				}
				mu.Lock()
				defer mu.Unlock()
				txInItems = append(txInItems, items...)
				return nil
			})
		}
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	for _, shard := range shards {
		s.lastShards[shardId(shard)] = shard.SeqNo
	}

	s.logger.Debug().
		Int32("workchain", master.Workchain).
		Str("shard", shardId(master)).
		Uint32("seqno", master.SeqNo).
		Int("txs", len(txInItems)).
		Msg("master block processed")

	return txInItems, nil
}

func (s *TonBlockScanner) processShard(
	masterSeqno uint32,
	shard *ton.BlockIDExt,
) ([]*types.TxInItem, error) {
	if shard.SeqNo == 0 {
		return nil, nil
	}

	block, err := s.api.LookupBlock(context.Background(), shard.Workchain, shard.Shard, shard.SeqNo)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup shard block %d: %w", shard.SeqNo, err)
	}

	data, err := s.api.GetBlockData(context.Background(), block)
	if err != nil {
		return nil, fmt.Errorf("failed to get shard block data %d: %w", shard.SeqNo, err)
	}

	info := data.BlockInfo

	txInfos, _, err := s.api.GetBlockTransactionsV2(
		context.TODO(),
		block,
		500_000,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions %d: %w", shard.SeqNo, err)
	}

	var txInItems []*types.TxInItem

	for _, txInfo := range txInfos {
		addr := address.NewAddress(0x17, 0x00, txInfo.Account)
		addr.SetBounce(false)
		addr.SetTestnetOnly(s.testnet)

		tx, err := s.api.GetTransaction(
			context.TODO(),
			block,
			addr,
			txInfo.LT,
		)
		if err != nil {
			s.logger.Err(err).Msg("failed getting tx info")
		}

		// we are interested in "ordinary" transactions only
		// https://docs.ton.org/v3/documentation/data-formats/tlb/transaction-layout
		txDescription, ok := tx.Description.(tlb.TransactionDescriptionOrdinary)
		if !ok {
			continue
		}

		if txDescription.Aborted {
			s.logger.Debug().
				Str("hash", hex.EncodeToString(tx.Hash)).
				Uint32("block_height", block.SeqNo).
				Str("lt", fmt.Sprintf("%d", tx.LT)).
				Str("account", addr.String()).
				Msg("skipping aborted transaction")
			continue
		}

		s.logger.Debug().
			Str("hash", hex.EncodeToString(tx.Hash)).
			Uint32("block_height", block.SeqNo).
			Str("lt", fmt.Sprintf("%d", tx.LT)).
			Str("account", addr.String()).
			Msg("processing transaction")

		isInternalOnly := true

		var msgs []tlb.Message
		// process out messages only, as they are the result after tx execution.
		if tx.IO.Out != nil {
			outMsgs, err := tx.IO.Out.ToSlice()
			if err != nil {
				s.logger.Err(err).Msg("failed to get out msgs")
			} else {
				msgs = outMsgs
			}
		}

		for _, m := range msgs {
			// we are interested in internal messages only
			if m.MsgType != tlb.MsgTypeInternal {
				isInternalOnly = false
				continue
			}

			message := m.AsInternal()

			// we do not support bounce (returnable) messages.
			if message.Bounce {
				s.logger.Debug().
					Msg("skipping bounce message")
				continue
			}
			if message.Bounced {
				s.logger.Debug().
					Msg("skipping bounced message")
				continue
			}

			items, err := s.processMessage(int64(masterSeqno), shard, tx, message)
			if err != nil {
				s.logger.Err(err).Msg("failed to process message")
				continue
			}

			txInItems = append(txInItems, items...)
		}

		if isInternalOnly && !txDescription.Aborted {
			s.feeRateCache = append(s.feeRateCache, tx.TotalFees.Coins.Nano().Uint64())
		}
	}

	if info.AfterSplit || info.AfterMerge {
		s.logger.Debug().Msg("merged or split shard")

		parents, err := ton.GetParentBlocks(&info)
		if err != nil {
			s.logger.Err(err).Msg("failed to get parent blocks")
			return nil, err
		}
		for _, parent := range parents {
			delete(s.lastShards, shardId(parent))
		}
	}

	s.logger.Debug().
		Bool("split", info.AfterSplit).
		Bool("merged", info.AfterMerge).
		Int32("workchain", shard.Workchain).
		Str("shard", shardId(shard)).
		Uint32("seqno", shard.SeqNo).
		Int("txs", len(txInItems)).
		Msg("Processed shard")

	return txInItems, nil
}

func (s *TonBlockScanner) processMessage(
	masterSeqno int64,
	shard *ton.BlockIDExt,
	tx *tlb.Transaction,
	message *tlb.InternalMessage,
) ([]*types.TxInItem, error) {
	var txInItems []*types.TxInItem

	hash := base64.StdEncoding.EncodeToString(tx.Hash)
	sender := message.SrcAddr
	dest := message.DstAddr

	sender.SetBounce(false)
	dest.SetBounce(false)
	sender.SetTestnetOnly(s.testnet)
	dest.SetTestnetOnly(s.testnet)

	amount := message.Amount.Nano()

	if amount.Cmp(tlb.ZeroCoins.Nano()) <= 0 {
		err := fmt.Errorf("amount less than or equal to zero")
		s.logger.Err(err).
			Str("hash", hash).
			Uint64("amount", amount.Uint64()).
			Msg("")
		return nil, err
	}

	decimals := common.TONChain.GetGasAssetDecimal()

	amountCoins, err := common.ConvertDecimals(
		amount, int(decimals), common.THORChainDecimals,
	)
	if err != nil {
		s.logger.Err(err).Msg("failed to convert amount to decimals")
		return nil, err
	}

	coins := common.Coins{{
		Asset:    common.TONAsset,
		Amount:   math.NewUintFromBigInt(amountCoins),
		Decimals: decimals,
	}}

	amountGas, err := common.ConvertDecimals(
		big.NewInt(tx.TotalFees.Coins.Nano().Int64()),
		int(decimals),
		common.THORChainDecimals,
	)
	if err != nil {
		s.logger.Err(err).Msg("failed to convert gas amount to decimals")
		return nil, err
	}

	gas := common.Gas{{
		Asset:    common.TONAsset,
		Amount:   math.NewUintFromBigInt(amountGas),
		Decimals: decimals,
	}}

	s.logger.Debug().
		Str("hash", hash).
		Str("sender", sender.String()).
		Str("dest", dest.String()).
		Uint64("amount", amount.Uint64()).
		Uint64("gas", tx.TotalFees.Coins.Nano().Uint64()).
		Str("memo", message.Comment()).
		Str("shard", shardId(shard)).
		Msg("processing message")

	txInItems = append(txInItems, &types.TxInItem{
		BlockHeight: masterSeqno,
		Tx:          hash,
		Memo:        message.Comment(),
		Sender:      sender.String(),
		To:          dest.String(),
		Coins:       coins,
		Gas:         gas,
	})

	return txInItems, nil
}

func shardId(block *ton.BlockIDExt) string {
	return fmt.Sprintf("%d,%x", block.Workchain, uint64(block.Shard))
}
