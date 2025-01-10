package ton

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"math/big"
	"regexp"
	"strconv"
	"sync"
	"time"
)

type TonBlockScanner struct {
	config     config.BifrostBlockScannerConfiguration
	logger     zerolog.Logger
	bridge     thorclient.ThorchainBridge
	api        ton.APIClientWrapped
	lastShards map[string]uint32
	regexp     *regexp.Regexp
}

func NewTonBlockScanner(
	cfg config.BifrostBlockScannerConfiguration,
	bridge thorclient.ThorchainBridge,
	api ton.APIClientWrapped,
) (*TonBlockScanner, error) {
	var err error

	logger := log.Logger.With().
		Str("module", "blockscanner").
		Str("chain", cfg.ChainID.String()).
		Logger()

	scanner := TonBlockScanner{
		config:     cfg,
		logger:     logger,
		bridge:     bridge,
		api:        api,
		lastShards: map[string]uint32{},
	}

	scanner.regexp, err = regexp.Compile("^(MIGRATE|RAGNAROK):.*")
	if err != nil {
		logger.Err(err).Msg("failed to compile regex")
		return nil, err
	}

	return &scanner, nil
}

// Return the latest block height from master chain
func (s *TonBlockScanner) GetHeight() (int64, error) {
	info, err := s.api.GetMasterchainInfo(context.Background())
	if err != nil {
		s.logger.Err(err).Msg("")
		return 0, err
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
	txInItems := []types.TxInItem{}

	items, err := s.processMaster(uint32(fetchHeight))
	if err != nil {
		s.logger.Err(err).Msg("failed to process master block")
		return types.TxIn{}, err
	}

	txInItems = append(txInItems, items...)

	for i := range txInItems {
		txInItems[i].BlockHeight = fetchHeight
	}

	txIn := types.TxIn{
		Count:    strconv.Itoa(len(txInItems)),
		Chain:    s.config.ChainID,
		TxArray:  txInItems,
		Filtered: false,
		MemPool:  false,
	}

	if fetchHeight%20 == 0 {
		fee := toThor(big.NewInt(TxFee)).Uint64()

		tx, err := s.bridge.PostNetworkFee(
			fetchHeight, s.config.ChainID, 1, fee,
		)

		if err != nil {
			s.logger.Err(err).Msg("failed posting network fee")
		} else {
			s.logger.Debug().
				Str("hash", tx.String()).
				Uint64("fee", fee).
				Msg("updated network fee")
		}

	}

	return txIn, nil
}

func (s *TonBlockScanner) processMaster(masterSeqno uint32) ([]types.TxInItem, error) {
	start := time.Now()

	master, err := s.api.LookupBlock(
		context.Background(),
		-1,
		-9223372036854775808,
		masterSeqno,
	)
	if err != nil {
		s.logger.Err(err).Msg("failed to lookup block")
		return nil, err
	}

	txInItems := []types.TxInItem{}
	id := shardId(master)

	shards, err := s.api.GetBlockShardsInfo(context.Background(), master)
	if err != nil {
		s.logger.Err(err).Msg("failed to get shard blocks")
		return nil, err
	}

	var wg sync.WaitGroup
	var mtx sync.Mutex

	// sort.Slice(shards, func(i, j int) bool {
	// 	return shards[i].Shard < shards[j].Shard
	// })

	for _, shard := range shards {
		shardSeqno, found := s.lastShards[shardId(shard)]
		if found {
			shardSeqno++
		} else {
			shardSeqno = shard.SeqNo
		}

		for ; shardSeqno <= shard.SeqNo; shardSeqno++ {
			wg.Add(1)

			go func(shard *ton.BlockIDExt, shardSeqno uint32) {
				items, err_ := s.processShard(shard.Workchain, shard.Shard, shardSeqno)
				mtx.Lock()
				defer mtx.Unlock()
				if err_ != nil {
					s.logger.Err(err_).Msg("failed to process shard")
					err = err_
				} else {
					txInItems = append(txInItems, items...)
				}
				wg.Done()
			}(shard, shardSeqno)
		}
	}

	wg.Wait()

	if err != nil {
		return nil, err
	}

	// update internal history
	for _, shard := range shards {
		s.lastShards[shardId(shard)] = shard.SeqNo
	}

	s.logger.Info().
		Str("id", fmt.Sprintf("(%s,%d)", id, masterSeqno)).
		Int("txs", len(txInItems)).
		Str("duration", time.Since(start).String()).
		Msg("master block done")

	return txInItems, nil
}

func (s *TonBlockScanner) processShard(
	workchain int32,
	shard int64,
	seqno uint32,
) ([]types.TxInItem, error) {
	block, err := s.api.LookupBlock(context.Background(), workchain, shard, seqno)
	if err != nil {
		s.logger.Err(err).Msg("failed to lookup block")
		return nil, err
	}

	data, err := s.api.GetBlockData(context.Background(), block)
	if err != nil {
		s.logger.Err(err).Msg("failed to get block data")
		return nil, err
	}

	info := data.BlockInfo
	id := shardId(block)

	txInfos, _, err := s.api.GetBlockTransactionsV2(
		context.TODO(),
		block,
		1000,
		nil,
	)
	if err != nil {
		s.logger.Err(err).Msg("failed getting transactions")
		return nil, err
	}

	txInItems := []types.TxInItem{}

	for _, txInfo := range txInfos {
		addr := address.NewAddress(0x17, 0x00, txInfo.Account)

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
		txDescription, ok := tx.Description.Description.(tlb.TransactionDescriptionOrdinary)
		if !ok {
			continue
		}

		// get relevant messages going out from a TON wallet
		if tx.IO.Out != nil {
			messages, err := tx.IO.Out.ToSlice()
			if err != nil {
				s.logger.Err(err).Msg("failed to get out msgs")
			}

			for i := range messages {
				if messages[i].MsgType != tlb.MsgTypeInternal {
					continue
				}

				message := messages[i].AsInternal()

				// messages going out from THORChain don't have that flag set
				if message.Bounce {
					continue
				}

				if message.Comment() == "" {
					continue
				}

				// we are only interested in certain tx types
				if !s.regexp.MatchString(message.Comment()) {
					continue
				}

				items, err := s.processMessage(tx, message)
				if err != nil {
					s.logger.Err(err).Msg("failed to get outgoing txs")
					continue
				}

				txInItems = append(txInItems, items...)
			}
		}

		// get relevant message going into a TON wallet
		if tx.IO.In != nil {
			if tx.IO.In.MsgType != tlb.MsgTypeInternal {
				continue
			}

			if txDescription.Aborted {
				// double check if transaction is skipped...
				phaseSkipped, ok := txDescription.ComputePhase.Phase.(tlb.ComputePhaseSkipped)
				if !ok {
					continue
				}

				// ...and the reason is acceptable
				validReasons := map[tlb.ComputeSkipReasonType]struct{}{
					tlb.ComputeSkipReasonNoState: {},
					tlb.ComputeSkipReasonNoGas:   {},
				}

				_, ok = validReasons[phaseSkipped.Reason.Type]
				if !ok {
					continue
				}
			}

			message := tx.IO.In.AsInternal()

			// bounced messages are no valid deposits
			// TODO: Discuss if messages if "bouncable" flag should be accepted
			// 		 too, each wallet will probably set that by default
			if message.Bounced {
				continue
			}

			// we can't do anything without a memo
			if message.Comment() == "" {
				continue
			}

			// we are not interested in certain memos, that are only sent from
			// THORChain itself, to prevent double counting (outgoing tx should
			// have been observed already
			if s.regexp.MatchString(message.Comment()) {
				continue
			}

			items, err := s.processMessage(tx, message)
			if err != nil {
				s.logger.Err(err).Msg("failed to get incoming txs")
				return nil, err
			}

			txInItems = append(txInItems, items...)
		}
	}

	if info.AfterSplit || info.AfterMerge {
		s.logger.Debug().Msg("merged or split shard")
		parents, err := info.GetParentBlocks()
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
		Str("id", fmt.Sprintf("(%s,%d)", id, seqno)).
		Int("txs", len(txInItems)).
		Msg("")

	return txInItems, nil
}

func (s *TonBlockScanner) processMessage(
	tx *tlb.Transaction,
	message *tlb.InternalMessage,
) ([]types.TxInItem, error) {
	txInItems := []types.TxInItem{}

	hash := base64.StdEncoding.EncodeToString(tx.Hash)
	sender := message.SrcAddr
	dest := message.DestAddr()

	// TODO: Check if distinguishing between mainnet and testnet is needed
	sender.SetBounce(false)
	dest.SetBounce(false)

	amount := message.Amount.Nano()

	if amount.Cmp(tlb.ZeroCoins.Nano()) <= 0 {
		err := fmt.Errorf("amount less than or equal to zero")
		s.logger.Err(err).
			Str("hash", hash).
			Uint64("amount", amount.Uint64()).
			Msg("")
		return nil, err
	}

	coins := common.Coins{{
		Amount: toThor(amount),
		Asset:  common.TONAsset,
		// Decimals: 9,
	}}

	gas := common.Gas{{
		Amount: toThor(big.NewInt(TxFee)),
		Asset:  common.TONAsset,
		// Decimals: 9,
	}}

	txInItems = append(txInItems, types.TxInItem{
		BlockHeight: -1,
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
