package ton

import (
	"context"
	"encoding/base64"
	"fmt"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"math/big"
	"net/http"
	"strconv"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
)

type TonBlockScanner struct {
	config config.BifrostBlockScannerConfiguration
	logger zerolog.Logger
	bridge thorclient.ThorchainBridge
	client ton.APIClientWrapped
	http   *http.Client
	apiUrl string
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
		config: cfg,
		logger: logger,
		bridge: bridge,
		client: client,
		http:   &http.Client{},
		apiUrl: "https://toncenter.com/api",
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

func (s *TonBlockScanner) FetchMemPool(height int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (s *TonBlockScanner) FetchTxs(
	fetchHeight, chainHeight int64,
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

		txs, err := s.getBlockTxs(blockInfo)
		if err != nil {
			s.logger.Err(err).
				Uint32("seqno", blockInfo.SeqNo).
				Int32("workchain", blockInfo.Workchain).
				Int64("shard", blockInfo.Shard).
				Msg("failed getting txs")
			return types.TxIn{}, err
		}

		txInItems = append(txInItems, txs...)
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
		fee := uint64(1_000_000)
		tx, err := s.bridge.PostNetworkFee(
			fetchHeight, s.config.ChainID, 1, fee, // 1e6 -> 1e8
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

		if tx.IO.In == nil {
			continue
		}

		if tx.IO.In.MsgType != tlb.MsgTypeInternal {
			continue
		}

		hash := base64.StdEncoding.EncodeToString(tx.Hash)
		msg := tx.IO.In.AsInternal()

		if msg.Bounced || msg.Bounce {
			// bounced or bouncing msgs can't be regular deposits...
			continue
		}

		discard := true

		switch desc := tx.Description.Description.(type) {
		case tlb.TransactionDescriptionOrdinary:
			// if not aborted, then no simple fund transfer
			if !desc.Aborted {
				continue
			}

			switch phase := desc.ComputePhase.Phase.(type) {
			case tlb.ComputePhaseSkipped:
				switch phase.Reason.Type {
				case tlb.ComputeSkipReasonNoState, tlb.ComputeSkipReasonNoGas:
					discard = false
				}
			}
		}

		if discard {
			continue
		}

		sender := tx.IO.In.Msg.SenderAddr()
		dest := tx.IO.In.Msg.DestAddr()

		// TODO: Check if distinguishing between mainnet and testnet is needed
		// and add checks for dest address only
		sender.SetTestnetOnly(true)
		sender.SetBounce(false)
		dest.SetTestnetOnly(true)
		dest.SetBounce(false)

		amount := msg.Amount.Nano()
		fees := tx.TotalFees.Coins.Nano()

		if amount.Cmp(tlb.ZeroCoins.Nano()) <= 0 {
			s.logger.Debug().
				Str("hash", hash).
				Uint64("amount", amount.Uint64()).
				Msg("invalid amount")
			continue
		}

		// should be discarded for reason tlb.ComputeSkipReasonNoGas
		if amount.Cmp(fees) <= 0 {
			s.logger.Debug().
				Str("hash", hash).
				Uint64("amount", amount.Uint64()).
				Uint64("fees", fees.Uint64()).
				Msg("sent amount less than fees")
			continue
		}

		// when creating multiple txs at once, fee is 0 for additional txs
		if len(fees.Bits()) == 0 {
			fees = big.NewInt(1)
		}

		txInItems = append(txInItems, types.TxInItem{
			BlockHeight: -1,
			Tx:          hash,
			Memo:        msg.Comment(),
			Sender:      sender.String(),
			To:          dest.String(),
			Coins:       toCoins(amount.Sub(amount, fees), 9),
			Gas:         common.Gas{toCoin(fees, 9)},
		})
	}

	return txInItems, nil
}

func toCoin(amount *big.Int, decimals int) common.Coin {
	return common.Coin{
		Asset:    common.TONAsset,
		Amount:   cosmos.NewUintFromBigInt(amount),
		Decimals: int64(decimals),
	}
}

func toCoins(amount *big.Int, decimals int) common.Coins {
	return common.Coins{toCoin(amount, decimals)}
}
