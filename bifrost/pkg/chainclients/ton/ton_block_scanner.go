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
	"net/http"
	"regexp"
	"sort"
	"strconv"
)

type TonBlockScanner struct {
	config     config.BifrostBlockScannerConfiguration
	logger     zerolog.Logger
	bridge     thorclient.ThorchainBridge
	client     ton.APIClientWrapped
	http       *http.Client
	apiUrl     string
	lastMaster uint32
	lastShards map[string]uint32
}

func NewTonBlockScanner(
	cfg config.BifrostBlockScannerConfiguration,
	bridge thorclient.ThorchainBridge,
	client ton.APIClientWrapped,
) (*TonBlockScanner, error) {
	logger := log.Logger.With().
		Str("module", "blockscanner").
		Str("chain", cfg.ChainID.String()).
		Logger()

	scanner := TonBlockScanner{
		config:     cfg,
		logger:     logger,
		bridge:     bridge,
		client:     client,
		http:       &http.Client{},
		apiUrl:     "https://toncenter.com/api",
		lastMaster: 0,
		lastShards: map[string]uint32{},
	}

	return &scanner, nil
}

// Return the latest block height from master chain
func (s *TonBlockScanner) GetHeight() (int64, error) {
	info, err := s.client.GetMasterchainInfo(context.Background())
	if err != nil {
		s.logger.Err(err).Msg("")
		return 0, err
	}

	return int64(info.SeqNo) - ConfirmationBlocks, nil
}

func (s *TonBlockScanner) FetchMemPool(_ int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (s *TonBlockScanner) FetchTxs(
	fetchHeight, _ int64,
) (types.TxIn, error) {
	block, err := s.client.LookupBlock(
		context.TODO(),
		-1,
		2305843009213693952,
		uint32(fetchHeight),
	)
	if err != nil {
		s.logger.Err(err).Msg("failed fetching master chain block")
		return types.TxIn{}, err
	}

	txInItems := []types.TxInItem{}

	if s.lastMaster == 0 {
		s.lastMaster = block.SeqNo - 1
	}

	for seqno := s.lastMaster + 1; seqno <= block.SeqNo; seqno++ {
		items, err := s.processMaster(seqno)
		if err != nil {
			s.logger.Err(err).Msg("failed to process master block")
			return types.TxIn{}, err
		}

		txInItems = append(txInItems, items...)
	}

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
			s.logger.Info().
				Str("hash", tx.String()).
				Uint64("fee", fee).
				Msg("updated network fee")
		}

	}

	return txIn, nil
}

func (s *TonBlockScanner) processMaster(masterSeqno uint32) ([]types.TxInItem, error) {
	master, err := s.client.LookupBlock(
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

	shards, err := s.client.GetBlockShardsInfo(context.Background(), master)
	if err != nil {
		s.logger.Err(err).Msg("failed to get shard blocks")
		return nil, err
	}

	sort.Slice(shards, func(i, j int) bool {
		return shards[i].Shard < shards[j].Shard
	})

	for _, shard := range shards {
		shardSeqno, found := s.lastShards[shardId(shard)]
		if found {
			shardSeqno++
		} else {
			shardSeqno = shard.SeqNo
		}

		for ; shardSeqno <= shard.SeqNo; shardSeqno++ {
			items, err := s.processShard(shard.Workchain, shard.Shard, shardSeqno)
			if err != nil {
				s.logger.Err(err).Msg("failed to process shard")
				return nil, err
			}

			txInItems = append(txInItems, items...)
		}
	}

	s.logger.Info().
		Str("id", fmt.Sprintf("(%s,%d)", id, masterSeqno)).
		Int("txs", len(txInItems)).
		Msg("processing master")

	s.lastMaster = masterSeqno

	return txInItems, nil
}

func (s *TonBlockScanner) processShard(
	workchain int32,
	shard int64,
	seqno uint32,
) ([]types.TxInItem, error) {
	block, err := s.client.LookupBlock(context.Background(), workchain, shard, seqno)
	if err != nil {
		s.logger.Err(err).Msg("failed to lookup block")
		return nil, err
	}

	data, err := s.client.GetBlockData(context.Background(), block)
	if err != nil {
		s.logger.Err(err).Msg("failed to get block data")
		return nil, err
	}

	info := data.BlockInfo
	id := shardId(block)

	expression, err := regexp.Compile("^(MIGRATE|RAGNAROK):.*")
	if err != nil {
		s.logger.Err(err).Msg("failed to compile regex")
		return nil, err
	}

	txInfos, _, err := s.client.GetBlockTransactionsV2(
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

		tx, err := s.client.GetTransaction(
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

				// we are only interested in certain tx types
				if !expression.MatchString(message.Comment()) {
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

			// we can't do anything without a memo
			if message.Comment() == "" {
				continue
			}

			// bounced messages are no valid deposits
			// TODO: Discuss if messages if "bouncable" flag should be accepted
			// 		 too, each wallet will probably set that by default
			if message.Bounced {
				continue
			}

			// we are not interested in certain memos, that are only sent from
			// THORChain itself, to prevent double counting (outgoing tx should
			// have been observed already
			if expression.MatchString(message.Comment()) {
				continue
			}

			items, err := s.processMessage(tx, message)
			if err != nil {
				s.logger.Err(err).Msg("failed to get incoming txs")
			}

			txInItems = append(txInItems, items...)

		}
	}

	// update internal history

	s.lastShards[id] = seqno

	if info.AfterSplit || info.AfterMerge {
		s.logger.Info().Msg("merged or split shard")
		parents, err := info.GetParentBlocks()
		if err != nil {
			s.logger.Err(err).Msg("failed to get parent blocks")
			return nil, err
		}
		for _, parent := range parents {
			delete(s.lastShards, shardId(parent))
		}
	}

	s.logger.Info().
		Bool("afterSplit", info.AfterSplit).
		Bool("afterMerge", info.AfterMerge).
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
	// sender := tx.IO.In.Msg.SenderAddr()
	// dest := tx.IO.In.Msg.DestAddr()
	sender := message.SrcAddr
	dest := message.DestAddr()

	// TODO: Check if distinguishing between mainnet and testnet is needed
	// and add checks for dest address only
	sender.SetTestnetOnly(true)
	sender.SetBounce(false)
	dest.SetTestnetOnly(true)
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

func shardId(info *ton.BlockIDExt) string {
	return fmt.Sprintf("%d,%x", info.Workchain, uint64(info.Shard))
}
