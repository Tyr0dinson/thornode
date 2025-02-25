package ton

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"math/big"
	"net/http"
	"regexp"
	"strconv"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
)

type TonBlockScanner struct {
	config     config.BifrostBlockScannerConfiguration
	logger     zerolog.Logger
	bridge     thorclient.ThorchainBridge
	client     ton.APIClientWrapped
	http       *http.Client
	apiUrl     string
	lastBlocks map[int64]uint32
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
		lastBlocks: map[int64]uint32{},
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

	return int64(info.SeqNo), nil
}

func (s *TonBlockScanner) FetchMemPool(_ int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (s *TonBlockScanner) FetchTxs(
	fetchHeight, _ int64,
) (types.TxIn, error) {
	masterBlockInfo, err := s.client.LookupBlock(
		context.TODO(),
		-1,
		2305843009213693952,
		uint32(fetchHeight),
	)
	if err != nil {
		s.logger.Err(err).Msg("failed fetching master chain block")
		return types.TxIn{}, err
	}

	shardBlockInfos, err := s.client.GetBlockShardsInfo(
		context.Background(),
		masterBlockInfo,
	)
	if err != nil {
		s.logger.Err(err).Msg("failed getting shard info")
		return types.TxIn{}, err
	}

	txInItems := []types.TxInItem{}

	for _, blockInfo := range shardBlockInfos {
		s.logger.Debug().
			Str("shard", fmt.Sprintf("%x", uint64(blockInfo.Shard))).
			Uint32("height", blockInfo.SeqNo).
			Msg("get txs")

		seqno, found := s.lastBlocks[blockInfo.Shard]
		if !found {
			seqno = blockInfo.SeqNo
			s.lastBlocks[blockInfo.Shard] = seqno
		}

		for seqno <= blockInfo.SeqNo {
			info, err := s.client.LookupBlock(
				context.Background(),
				blockInfo.Workchain,
				blockInfo.Shard,
				seqno,
			)
			if err != nil {
				s.logger.Err(err).Msg("failed to get shard block info")
			}

			txs, err := s.getBlockTxs(info)
			if err != nil {
				s.logger.Err(err).
					Uint32("seqno", blockInfo.SeqNo).
					Int32("workchain", blockInfo.Workchain).
					Int64("shard", blockInfo.Shard).
					Msg("failed getting txs")
				return types.TxIn{}, err
			}

			txInItems = append(txInItems, txs...)
			seqno++
		}

		s.lastBlocks[blockInfo.Shard] = seqno

	}

	// Set master chain height for every tx
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

func (s *TonBlockScanner) getBlockTxs(blockInfo *ton.BlockIDExt) ([]types.TxInItem, error) {
	expression, err := regexp.Compile("^(MIGRATE|RAGNAROK):.*")
	if err != nil {
		s.logger.Err(err).Msg("failed to compile regex")
		return nil, err
	}

	txInfos, _, err := s.client.GetBlockTransactionsV2(
		context.TODO(),
		blockInfo,
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
			blockInfo,
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

				items, err := s.getTxInItems(tx, message)
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

			items, err := s.getTxInItems(tx, message)
			if err != nil {
				s.logger.Err(err).Msg("failed to get incoming txs")
			}

			txInItems = append(txInItems, items...)

		}
	}

	return txInItems, nil
}

func (s *TonBlockScanner) getTxInItems(
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
