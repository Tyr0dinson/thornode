package ton

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

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
	"gitlab.com/thorchain/thornode/v3/constants"
	"golang.org/x/sync/errgroup"
)

const (
	minNetworkFee = 10_000_000
	extInLookback = 1000
)

type TonBlockScanner struct {
	config        config.BifrostBlockScannerConfiguration
	logger        zerolog.Logger
	bridge        thorclient.ThorchainBridge
	ctx           context.Context
	api           ton.APIClientWrapped
	lastShards    map[string]uint32
	lastShardsMtx sync.Mutex

	feeRateCache [][]uint64
	feeRateMtx   sync.Mutex
	feeRate      atomic.Uint64

	globalNetworkFeeQueue chan common.NetworkFee
}

func NewTonBlockScanner(
	cfg config.BifrostBlockScannerConfiguration,
	bridge thorclient.ThorchainBridge,
	api ton.APIClientWrapped,
) (*TonBlockScanner, error) {
	if cfg.GasCacheBlocks <= 0 {
		cfg.GasCacheBlocks = 10
	}

	if cfg.GasPriceResolution <= 0 {
		cfg.GasPriceResolution = 1_000_000
	}

	return &TonBlockScanner{
		config: cfg,
		logger: log.Logger.With().
			Str("module", "blockscanner").
			Str("chain", cfg.ChainID.String()).
			Logger(),
		bridge:        bridge,
		ctx:           context.Background(),
		api:           api,
		feeRateCache:  make([][]uint64, 0),
		feeRateMtx:    sync.Mutex{},
		lastShards:    make(map[string]uint32),
		lastShardsMtx: sync.Mutex{},
	}, nil
}

// GetHeight queries the master chain and returns its latest height
func (s *TonBlockScanner) GetHeight() (int64, error) {
	info, err := s.api.GetMasterchainInfo(s.ctx)
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
	return 1, s.feeRate.Load()
}

func (s *TonBlockScanner) sendNetworkFeeIfNecessary(height int64) {
	s.feeRateMtx.Lock()
	defer s.feeRateMtx.Unlock()

	if len(s.feeRateCache) == 0 {
		// Not enough txs yet
		return
	}

	if len(s.feeRateCache) < s.config.GasCacheBlocks {
		// not enough txs yet
		return
	}

	currentFeeRate := s.feeRate.Load()

	// Calculate the median fee rate from the gas cache

	var allRates []uint64
	for _, rates := range s.feeRateCache {
		allRates = append(allRates, rates...)
	}

	if len(allRates) < s.config.GasCacheBlocks {
		s.logger.Info().
			Int64("height", height).
			Int("rates", len(allRates)).
			Int("blocks", len(s.feeRateCache)).
			Msg("not enough fee rates available")
		return
	}

	medianFeeRate := common.GetMedianUint64(allRates)

	// round medianFeeRate to the nearest resolution
	var resolution uint64
	if s.config.GasPriceResolution > 0 {
		resolution = uint64(s.config.GasPriceResolution)
	} else {
		resolution = 1_000_000
	}

	roundedFeeRate := ((medianFeeRate + (resolution / 2)) / resolution) * resolution

	// truncate gas prices older than our max cached transactions
	if len(s.feeRateCache) > s.config.GasCacheBlocks {
		s.feeRateCache = s.feeRateCache[(len(s.feeRateCache) - s.config.GasCacheBlocks):]
	}

	roundedFeeRate = max(roundedFeeRate, minNetworkFee)

	// if it has changed
	if currentFeeRate == roundedFeeRate {
		s.logger.Debug().Uint64("fee", roundedFeeRate).Msg("network fee is already up to date")
		return
	}

	s.feeRate.Store(roundedFeeRate)

	select {
	case s.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           common.TONChain,
		Height:          height,
		TransactionSize: 1,
		TransactionRate: roundedFeeRate,
	}:
	case <-time.After(constants.ThorchainBlockTime):
		s.logger.Error().Msg("fail to send network fee to THORChain, timeout")
	}

	s.logger.Info().
		Uint64("fee", roundedFeeRate).
		Int64("height", height).
		Msg("sent network fee to THORChain")
}

func (s *TonBlockScanner) processMaster(masterSeqno uint32) ([]*types.TxInItem, error) {
	master, err := s.api.LookupBlock(
		s.ctx,
		masterChainId,
		masterShardId,
		masterSeqno,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup master block seq %d: %w", masterSeqno, err)
	}

	shards, err := s.api.GetBlockShardsInfo(s.ctx, master)
	if err != nil {
		return nil, fmt.Errorf("failed to get shards for master block %d: %w", masterSeqno, err)
	}

	sort.Slice(shards, func(i, j int) bool {
		return shards[i].Shard < shards[j].Shard
	})

	// add rates slice for the current master block
	s.feeRateMtx.Lock()
	s.feeRateCache = append(s.feeRateCache, []uint64{})
	s.feeRateMtx.Unlock()

	var txInItems []*types.TxInItem
	var mu sync.Mutex
	var eg errgroup.Group

	// s.lastShards is updated inside the spawned go routines, so use
	// lastShards to ensure correct seq no
	lastShards := map[string]uint32{}
	for k, v := range s.lastShards {
		lastShards[k] = v
	}

	for _, shard := range shards {
		shardSeqno, found := lastShards[shardId(shard)]

		if found {
			shardSeqno++
		} else {
			shardSeqno = shard.SeqNo
		}

		// process intermediate shards
		for ; shardSeqno <= shard.SeqNo; shardSeqno++ {
			sh := shard.Copy()
			sh.SeqNo = shardSeqno
			eg.Go(func() error {
				newTxInItems, shardErr := s.processShard(sh)
				if shardErr != nil {
					return fmt.Errorf("failed to process shard %d: %w", sh.SeqNo, shardErr)
				}
				if len(newTxInItems) == 0 {
					return nil
				}
				mu.Lock()
				defer mu.Unlock()
				// set block height to master seqno
				for i := range newTxInItems {
					newTxInItems[i].BlockHeight = int64(masterSeqno)
					txInItems = append(txInItems, &newTxInItems[i])
				}

				return nil
			})
		}
	}

	if err = eg.Wait(); err != nil {
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
	shard *ton.BlockIDExt,
) ([]types.TxInItem, error) {
	if shard.SeqNo == 0 {
		return nil, nil
	}

	block, err := s.api.LookupBlock(s.ctx, shard.Workchain, shard.Shard, shard.SeqNo)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup shard block %d: %w", shard.SeqNo, err)
	}

	data, err := s.api.GetBlockData(s.ctx, block)
	if err != nil {
		return nil, fmt.Errorf("failed to get shard block data %d: %w", shard.SeqNo, err)
	}

	info := data.BlockInfo

	txInfos, _, err := s.api.GetBlockTransactionsV2(
		s.ctx,
		block,
		500_000,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions %d: %w", shard.SeqNo, err)
	}

	// reverse order to have highest lt per tx first, so lookback cache needs
	// to fetch only once for each address

	txLookBackCache := map[string][]*tlb.Transaction{}

	var txInItems []types.TxInItem
	for _, txInfo := range txInfos {
		addr := address.NewAddress(0x17, 0x00, txInfo.Account)
		addr.SetBounce(false)
		addr.SetTestnetOnly(common.IsMocknet)

		var tx *tlb.Transaction
		tx, err = s.api.GetTransaction(s.ctx, block, addr, txInfo.LT)
		if err != nil {
			s.logger.Err(err).
				Str("shard", shardId(shard)).
				Str("account", addr.String()).
				Msg("failed getting tx info")
			return nil, fmt.Errorf("failed to get tx: %w", err)
		}

		skip, reason := skipTx(tx)
		if skip {
			s.logger.Debug().
				Str("hash", hex.EncodeToString(tx.Hash)).
				Str("shard", shardId(shard)).
				Uint32("block_height", block.SeqNo).
				Str("lt", fmt.Sprintf("%d", tx.LT)).
				Str("account", addr.String()).
				Str("reason", reason).
				Msg("skipping transaction")
			continue
		}

		s.logger.Debug().
			Str("hash", hex.EncodeToString(tx.Hash)).
			Str("shard", shardId(shard)).
			Uint32("block_height", block.SeqNo).
			Str("lt", fmt.Sprintf("%d", tx.LT)).
			Str("account", addr.String()).
			Msg("processing transaction")

		// this should be already filtered by skipTx(), just in case
		if tx.IO.In == nil {
			s.logger.Error().Msg("no in msgs found")
			continue
		}

		msg := tx.IO.In

		// we are just interested in fees of external in msgs, because this is
		// the kind of tx that is needed for sending founds out of a vault
		if msg.MsgType == tlb.MsgTypeExternalIn {
			s.feeRateMtx.Lock()
			index := len(s.feeRateCache) - 1
			s.feeRateCache[index] = append(s.feeRateCache[index], tx.TotalFees.Coins.Nano().Uint64())
			s.feeRateMtx.Unlock()
			continue
		}

		if msg.MsgType != tlb.MsgTypeInternal {
			continue
		}

		var txInItem types.TxInItem

		in := msg.AsInternal()

		src := in.SrcAddr
		txs, found := txLookBackCache[src.String()]
		if !found {
			var acc *tlb.Account
			acc, err = s.api.WaitForBlock(block.SeqNo).GetAccount(s.ctx, block, src)
			if err != nil {
				// possible by sending all funds from wallet
				s.logger.Err(err).Msg("source account not found")

				// assuming the account does not exist, exclude the address
				// from being queried again for this shard
				txLookBackCache[src.String()] = nil
				continue
			}

			lt := acc.LastTxLT
			hash := acc.LastTxHash

			txs, err = s.api.ListTransactions(s.ctx, src, extInLookback, lt, hash)
			if err != nil {
				s.logger.Err(err).
					Str("address", src.String()).
					Msg("failed to list transactions")
				// assuming there are no transactions, exclude the address
				// from being queried again for this shard
				txLookBackCache[src.String()] = nil
				continue
			}

			if len(txs) == 0 {
				// doesn't have any tx in this shard, don't query again
				txLookBackCache[src.String()] = nil
				continue
			}
		}

		txInItem, err = s.processMessage(tx, msg.AsInternal(), txs)
		if err != nil {
			s.logger.Err(err).Msg("failed to process message")
			continue
		}

		txInItems = append(txInItems, txInItem)
	}

	if info.AfterSplit || info.AfterMerge {
		s.logger.Debug().Msg("merged or split shard")

		parents, err := ton.GetParentBlocks(&info)
		if err != nil {
			s.logger.Err(err).Msg("failed to get parent blocks")
			return nil, err
		}
		for _, parent := range parents {
			s.lastShardsMtx.Lock()
			delete(s.lastShards, shardId(parent))
			s.lastShardsMtx.Unlock()
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
	tx *tlb.Transaction,
	msg *tlb.InternalMessage,
	txs []*tlb.Transaction,
) (txInItem types.TxInItem, err error) {
	if msg.Bounce {
		return txInItem, fmt.Errorf("bounce flag set")
	}

	if msg.Bounced {
		return txInItem, fmt.Errorf("message bounced")
	}

	memo := msg.Comment()
	if len(memo) > constants.MaxMemoSize {
		return txInItem, fmt.Errorf("memo exceeds %d bytes: %d", constants.MaxMemoSize, len(memo))
	}

	for _, c := range memo {
		if c > 127 {
			return txInItem, fmt.Errorf("memo contains invalid characters")
		}
	}

	src := msg.SrcAddr
	src.SetBounce(false)
	src.SetTestnetOnly(common.IsMocknet)

	dst := msg.DstAddr
	dst.SetBounce(false)
	dst.SetTestnetOnly(common.IsMocknet)

	// Try to find the external in message, that triggered the spending
	// wallet to transfer the funds. In case the message wasn't triggered
	// externally, use the "deposit" hash
	var in *tlb.Transaction
	in, err = s.findExternalInTx(msg, txs)
	if err != nil {
		s.logger.Err(err).Msg("failed to find previous transaction")
		return txInItem, err
	}

	var hash string
	var fees *big.Int

	if in != nil && in.IO.In != nil && in.IO.In.MsgType == tlb.MsgTypeExternalIn {
		// if external in msg is found
		hash = hex.EncodeToString(in.Hash)
		fees = in.TotalFees.Coins.Nano()
	} else {
		hash = hex.EncodeToString(tx.Hash)
		fees = tx.TotalFees.Coins.Nano()
	}

	if fees.Cmp(big.NewInt(0)) == 0 {
		fees = big.NewInt(1)
		s.logger.Warn().Str("hash", hash).Msg("gas is 0")
	}

	amount := msg.Amount.Nano()

	if amount.Cmp(big.NewInt(0)) <= 0 {
		err = fmt.Errorf("amount less than or equal to zero")
		s.logger.Err(err).
			Str("hash", hash).
			Str("amount", amount.String()).
			Msg("")
		return txInItem, err
	}

	decimals := common.TONChain.GetGasAssetDecimal()

	amountCoins, err := common.ConvertDecimals(
		amount, int(decimals), common.THORChainDecimals,
	)
	if err != nil {
		s.logger.Err(err).Msg("failed to convert amount to decimals")
		return txInItem, err
	}

	amountGas, err := common.ConvertDecimals(
		new(big.Int).Set(fees),
		int(decimals),
		common.THORChainDecimals,
	)
	if err != nil {
		s.logger.Err(err).Msg("failed to convert gas amount to decimals")
		return txInItem, err
	}

	txInItem = types.TxInItem{
		Tx:     hash,
		Memo:   msg.Comment(),
		Sender: src.String(),
		To:     dst.String(),
		Coins: common.Coins{{
			Asset:    common.TONAsset,
			Amount:   math.NewUintFromBigInt(amountCoins),
			Decimals: decimals,
		}},
		Gas: common.Gas{{
			Asset:    common.TONAsset,
			Amount:   math.NewUintFromBigInt(amountGas),
			Decimals: decimals,
		}},
	}

	return txInItem, nil
}

func (s *TonBlockScanner) findExternalInTx(
	in *tlb.InternalMessage,
	txs []*tlb.Transaction,
) (*tlb.Transaction, error) {
	for i := len(txs) - 1; i >= 0; i-- {
		tx := txs[i]

		if tx.IO.In == nil || tx.IO.Out == nil {
			continue
		}

		if tx.IO.In.MsgType != tlb.MsgTypeExternalIn {
			continue
		}

		msgs, err := tx.IO.Out.ToSlice()
		if err != nil {
			continue
		}

		// search newest msgs first
		for _, msg := range msgs {
			if msg.MsgType != tlb.MsgTypeInternal {
				continue
			}

			out := msg.AsInternal()

			if out.CreatedLT == in.CreatedLT &&
				out.Amount.Equals(in.Amount) &&
				out.SrcAddr.Equals(in.SrcAddr) &&
				out.DstAddr.Equals(in.DstAddr) &&
				out.Comment() == in.Comment() {

				return tx, nil
			}
		}
	}

	return nil, nil
}

func skipTx(tx *tlb.Transaction) (bool, string) {
	// no ordinary tx => skip
	desc, ok := tx.Description.(tlb.TransactionDescriptionOrdinary)
	if !ok {
		return true, "no ordinary transaction"
	}

	// no in msg => skip
	if tx.IO.In == nil {
		return true, "no incoming tx found"
	}

	// if tx is not aborted => don't skip
	if !desc.Aborted {
		return false, ""
	}

	// no credit phase, no funds credited => skip
	if desc.CreditPhase == nil {
		return true, "no credit phase found"
	}

	// zero funds credited => skip
	if desc.CreditPhase.Credit.Coins.IsZero() {
		return true, "zero funds credited"
	}

	if desc.Aborted && tx.EndStatus == tlb.AccountStatusUninit {
		return false, ""
	}

	return true, "default skip"
}

func shardId(block *ton.BlockIDExt) string {
	return fmt.Sprintf("%d,%x", block.Workchain, uint64(block.Shard))
}
