package cardano

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkmath "cosmossdk.io/math"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type Scanner struct {
	config                config.BifrostBlockScannerConfiguration
	logger                zerolog.Logger
	bridge                thorclient.ThorchainBridge
	db                    *blockscanner.BlockScannerStorageCardano
	n2c                   ClientWrapper
	n2n                   NodeWrapper
	minFeeA               uint64
	minFeeB               uint64
	networkFee            uint64
	networkMtx            sync.Mutex
	lastAsgardUpdate      time.Time
	asgardAddresses       []common.Address
	asgardMtx             sync.Mutex
	globalNetworkFeeQueue chan common.NetworkFee
	globalErrataQueue     chan types.ErrataBlock
	rollbackHeight        int64
	indexedBlocks         atomic.Uint64
	errataBlocks          []types.ErrataBlock
	errataMtx             sync.Mutex
}

func NewScanner(
	logger zerolog.Logger,
	cfg config.BifrostBlockScannerConfiguration,
	bridge thorclient.ThorchainBridge,
	storage *blockscanner.BlockScannerStorageCardano,
	n2cAddress string,
	n2nAddress string,
) (*Scanner, error) {
	var err error

	s := &Scanner{
		logger:     logger.With().Str("module", "scanner").Logger(),
		config:     cfg,
		bridge:     bridge,
		db:         storage,
		asgardMtx:  sync.Mutex{},
		errataMtx:  sync.Mutex{},
		networkMtx: sync.Mutex{},
	}

	// create n2n connection
	s.n2n, err = NewNodeConnection(
		logger, n2nAddress, s.rollBackwardHandler, s.rollForwardHandler,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ouroboros n2n connection: %w", err)
	}

	// create n2c connection
	s.n2c, err = NewClientConnection(s.logger, n2cAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to create ouroboros n2c connection: %w", err)
	}

	var point pcommon.Point

	if s.config.StartBlockHeight > 0 {
		slot := s.config.Cardano.StartBlockSlot
		if slot == 0 {
			return nil, fmt.Errorf("start block slot is zero")
		}

		hash := s.config.Cardano.StartBlockHash
		if hash == "" {
			return nil, fmt.Errorf("start block hash is undefined")
		}

		var bz []byte
		bz, err = hex.DecodeString(hash)
		if err != nil {
			return nil, fmt.Errorf("failed to decode start block hash: %w", err)
		}

		point = pcommon.NewPoint(slot, bz)
	} else {
		var tip *pcommon.Point
		tip, err = s.n2c.GetChainPoint()
		if err != nil {
			return nil, fmt.Errorf("failed to get current tip: %w", err)
		}

		point = *tip
	}

	// start chain sync in NewScanner to be able to fetch two consecutive
	// blocks before blockscanner tries to determine last scan position
	err = s.n2n.Sync(point)
	if err != nil {
		return nil, err
	}

	// fetchTxs needs the last _two_ blocks, to retrieve the user address
	// from the spent utxos, so wait for them
	start := time.Now()
	for {
		if time.Since(start) > time.Second*30 {
			return nil, fmt.Errorf("timeout waiting for block scanner to start")
		}

		time.Sleep(time.Second)

		if s.indexedBlocks.Load() > 1 {
			break
		}
	}

	return s, nil
}

func (s *Scanner) Stop() {
	s.n2n.Close()
	s.n2c.Close()
}

// GetHeight returns the current height of the chain.
// Implements both ChainClient and BlockScannerFetcher interfaces.
func (s *Scanner) GetHeight() (int64, error) {
	return s.n2c.GetChainBlockNo()
}

// FetchMemPool implements blockscanner.BlockScannerFetcher.
func (s *Scanner) FetchMemPool(_ int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (s *Scanner) GetNetworkFee() (uint64, uint64) {
	s.networkMtx.Lock()
	defer s.networkMtx.Unlock()
	return 1, s.networkFee
}

func (s *Scanner) GetNetworkMinFees() (uint64, uint64) {
	s.networkMtx.Lock()
	defer s.networkMtx.Unlock()
	return s.minFeeA, s.minFeeB
}

// FetchTxs implements blockscanner.BlockScannerFetcher.
func (s *Scanner) FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error) {
	// handle erratas first
	s.errataMtx.Lock()
	errataBlocks := s.errataBlocks
	s.errataBlocks = nil
	s.errataMtx.Unlock()

	for _, errata := range errataBlocks {
		s.globalErrataQueue <- errata
	}

	var allTxIns []*types.TxInItem

	rollbackHeight := atomic.LoadInt64(&s.rollbackHeight)
	if rollbackHeight > 0 {
		latestHeight, _, _, err := s.db.GetLastBlockId()
		if err != nil {
			return types.TxIn{}, err
		}

		s.logger.Info().
			Int64("rollback_height", rollbackHeight).
			Uint64("latest_height", latestHeight).
			Msg("processing rollback blocks")

		// process all new blocks
		for height := rollbackHeight + 1; height < fetchHeight; height++ {
			var txIns []*types.TxInItem
			txIns, err = s.fetchTxs(height)
			if err != nil {
				return types.TxIn{}, fmt.Errorf("failed to fetch txs: %w", err)
			}

			allTxIns = append(allTxIns, txIns...)
		}
	}

	txIns, err := s.fetchTxs(fetchHeight)
	if err != nil {
		return types.TxIn{}, fmt.Errorf("failed to fetch txs: %w", err)
	}

	allTxIns = append(allTxIns, txIns...)

	err = s.updateNetworkFee(fetchHeight)
	if err != nil {
		s.logger.Err(err).Msg("failed to update network fee")
	}

	if chainHeight > s.config.MaxReorgRescanBlocks {
		pruneHeight := uint64(chainHeight - s.config.MaxReorgRescanBlocks)

		err = s.db.PruneObservedTxs(pruneHeight)
		if err != nil {
			s.logger.Err(err).Msg("failed to prune observed txs")
		}

		err = s.db.PruneBlockIds(pruneHeight)
		if err != nil {
			s.logger.Err(err).Msg("failed to prune block ids")
		}
	}

	if chainHeight > transactionTimeout {
		pruneHeight := uint64(chainHeight - transactionTimeout)
		err = s.db.PruneSpentUtxos(pruneHeight)
		if err != nil {
			s.logger.Err(err).Msg("failed to prune spent utxos")
		}
	}

	// reset rollbackHeight only if no new rollback occurred
	if rollbackHeight > 0 {
		atomic.CompareAndSwapInt64(&s.rollbackHeight, rollbackHeight, 0)
	}

	return types.TxIn{
		Chain:    s.config.ChainID,
		TxArray:  allTxIns,
		Filtered: false,
		MemPool:  false,
	}, nil
}

func (s *Scanner) fetchTxs(height int64) ([]*types.TxInItem, error) {
	currentPoint, err := s.getPoint(height)
	if err != nil {
		return nil, err
	}

	block, err := s.n2n.GetBlock(currentPoint)
	if err != nil {
		s.logger.Err(err).Msg("failed to fetch block")
		return nil, err
	}

	// Scanner.Start() waits for two consecutive blocks to arrive
	var previousPoint pcommon.Point
	previousPoint, err = s.getPoint(height - 1)
	if err != nil {
		// shouldn't happen and maybe isn't needed for this block
		s.logger.Err(err).
			Int64("height", height-1).
			Msg("failed to fetch previous point")
	}

	var txIds [][]byte
	var txIns []*types.TxInItem
	for _, tx := range block.Transactions() {
		if !tx.IsValid() {
			continue
		}

		// only tx with exactly one VKey are supported
		if len(tx.Witnesses().Vkey()) != 1 {
			s.logger.Warn().Msg("tx with multiple witnesses are not supported")
			continue
		}

		// lookup the sender from the first input utxo
		if len(tx.Consumed()) < 1 {
			s.logger.Warn().Msg("tx without inputs")
			continue
		}

		if len(tx.Outputs()) < 1 {
			s.logger.Warn().Msg("tx without outputs")
			continue
		}

		var toAddress, fromAddress ledger.Address
		var amount *big.Int

		// deposits _must_ have asgard address as first, like BTC for example
		isToAsgard := s.isAsgardAddress(tx.Outputs()[0].Address())
		if isToAsgard {
			isToAsgard = true
			toAddress = tx.Outputs()[0].Address()
			amount = tx.Outputs()[0].Amount()
		}

		fromAddress, err = AddressFromBytes(tx.Witnesses().Vkey()[0].Vkey)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate address: %w", err)
		}

		// ignore if neither from nor to an asgard vault
		if !isToAsgard && !s.isAsgardAddress(fromAddress) {
			continue
		}

		if isToAsgard {
			// toAddress is asgard vault, get sender from first input
			// txs with multiple VKeys where already discarded

			// if we don't have height - 1 indexed yet (directly after start)
			// we can't determine the utxos
			if previousPoint.Slot == 0 {
				s.logger.Error().
					Str("tx_id", tx.Hash().String()).
					Msg("previous point not available, skipping")
				continue
			}

			var result *localstatequery.UTxOByTxInResult
			result, err = s.n2c.GetUTxOByTxIn(tx.Consumed()[:1], &previousPoint)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch utxo result for tx: %w", err)
			}

			if len(result.Results) != 1 {
				s.logger.Warn().
					Str("tx", tx.Hash().String()).
					Int("results", len(result.Results)).
					Int("expected", 1).
					Msg("unexpected amount of utxo results")
				continue
			}

			// get the first item from map
			for _, output := range result.Results {
				fromAddress = output.Address()
				break
			}
		} else {
			// fromAddress is asgard vault, so recipient is first output
			toAddress = tx.Outputs()[0].Address()
			amount = tx.Outputs()[0].Amount()
		}

		if amount == nil || amount.Uint64() == 0 {
			continue
		}

		memo := ExtractMemo(tx.Metadata(), 0)

		coins := common.Coins{{
			Asset:    common.ADAAsset,
			Amount:   sdkmath.NewUintFromBigInt(amount).MulUint64(100), // 1e6 => 1e8
			Decimals: 6,
		}}

		gas := common.Gas{{
			Asset:    common.ADAAsset,
			Amount:   sdkmath.NewUintFromBigInt(tx.Fee()).MulUint64(100), // 1e6 => 1e8
			Decimals: 6,
		}}

		txIns = append(txIns, &types.TxInItem{
			Tx:          tx.Hash().String(),
			BlockHeight: height,
			Memo:        memo,
			Sender:      fromAddress.String(),
			To:          toAddress.String(),
			Coins:       coins,
			Gas:         gas,
		})

		txIds = append(txIds, tx.Hash().Bytes())
	}

	// store observed tx hashes for rollback errata
	if len(txIds) > 0 {
		err = s.db.SetObservedTxs(uint64(height), txIds)
		if err != nil {
			s.logger.Err(err).Msg("failed to store observed txs")
			return nil, err
		}
	}

	return txIns, nil
}

func (s *Scanner) updateNetworkFee(height int64) error {
	if height <= 0 {
		return nil
	}

	if height%networkFeeUpdatePeriod != 0 && s.minFeeA > 0 && s.minFeeB > 0 {
		return nil
	}

	// fetch network protocol parameters, needed for tx signing
	protocolParams, err := s.n2c.GetCurrentProtocolParams()
	if err != nil {
		return fmt.Errorf("failed to get protocol parameters: %w", err)
	}

	params, ok := protocolParams.(*conway.ConwayProtocolParameters)
	if !ok {
		return fmt.Errorf("failed to get protocol parameters")
	}

	minFeeA := uint64(params.MinFeeA)
	minFeeB := uint64(params.MinFeeB)

	s.networkMtx.Lock()

	if minFeeA == s.minFeeA && minFeeB == s.minFeeB {
		s.networkMtx.Unlock()
		return nil
	}

	s.minFeeA = minFeeA
	s.minFeeB = minFeeB

	feeRate := minFeeA*estimatedTxSize + minFeeB + extraFeeBuffer

	var resolution uint64
	if s.config.GasPriceResolution > 0 {
		resolution = uint64(s.config.GasPriceResolution)
	} else {
		resolution = 1000 // default to 0.001 ADA
	}

	roundedFeeRate := ((feeRate + (resolution / 2)) / resolution) * resolution

	if roundedFeeRate == 0 {
		s.networkMtx.Unlock()
		return fmt.Errorf("rounded fee rate is zero")
	}

	// no need to update
	if s.networkFee == roundedFeeRate {
		s.networkMtx.Unlock()
		return nil
	}

	s.networkFee = roundedFeeRate
	s.networkMtx.Unlock()

	s.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           common.ADAChain,
		Height:          height,
		TransactionSize: 1,
		TransactionRate: roundedFeeRate,
	}

	s.logger.Info().
		Uint64("fee", roundedFeeRate).
		Int64("height", height).
		Msg("sent network fee to THORChain")

	return nil
}

func (s *Scanner) rollForwardHandler(
	_ chainsync.CallbackContext,
	_ uint,
	blockData any,
	_ chainsync.Tip,
) error {
	switch h := blockData.(type) {
	case ledger.Block:
		s.logger.Warn().Msg("block type is ledger.Block, chain-sync n2n protocol expects ledger.BlockHeader")
	case ledger.BlockHeader:
		s.logger.Debug().
			Uint64("block_number", h.BlockNumber()).
			Uint64("slot_number", h.SlotNumber()).
			Str("hash", h.Hash().String()).
			Msg("roll forward")

		err := s.db.SetBlockId(h.BlockNumber(), h.SlotNumber(), h.Hash().Bytes())
		if err != nil {
			return fmt.Errorf("failed to set block id: %w", err)
		}

		s.indexedBlocks.Add(1)
	}
	return nil
}

func (s *Scanner) rollBackwardHandler(
	_ chainsync.CallbackContext,
	point pcommon.Point,
	tip chainsync.Tip,
) error {
	s.logger.Warn().
		Uint64("height", tip.BlockNumber).
		Msg("roll backward")

	latestHeight, _, _, err := s.db.GetLastBlockId()
	if err != nil {
		s.logger.Err(err).Msg("failed to get last block height")
		return nil
	}

	for height := tip.BlockNumber + 1; height <= latestHeight; height++ {
		var txIds [][]byte
		txIds, err = s.db.GetObservedTxs(height)
		if err != nil {
			s.logger.Err(err).
				Uint64("height", height).
				Msg("failed to get observed txs")
			continue
		}

		var errataTxs []types.ErrataTx
		for _, txId := range txIds {
			errataTxs = append(errataTxs, types.ErrataTx{
				TxID:  common.TxID(hex.EncodeToString(txId)),
				Chain: s.config.ChainID,
			})
		}

		if len(errataTxs) > 0 {
			s.errataMtx.Lock()
			s.errataBlocks = append(s.errataBlocks, types.ErrataBlock{
				Height: int64(height),
				Txs:    errataTxs,
			})
			s.errataMtx.Unlock()

			s.logger.Info().
				Uint64("height", height).
				Int("txs", len(errataTxs)).
				Msg("sent errata for rolled-back block")
		}

		// remove block and associated txs
		if err = s.db.DeleteBlockId(height); err != nil {
			s.logger.Err(err).
				Uint64("height", height).
				Msg("failed to delete block id")
		}
	}

	err = s.db.SetScanPos(int64(tip.BlockNumber))
	if err != nil {
		s.logger.Err(err).Msg("failed to set scan position")
	}

	// signal FetchTxs to reprocess blocks from this height, blockscanner
	// doesn't know of this rollback
	atomic.StoreInt64(&s.rollbackHeight, int64(tip.BlockNumber))

	return nil
}

func (s *Scanner) getAsgardAddresses() ([]common.Address, error) {
	s.asgardMtx.Lock()
	defer s.asgardMtx.Unlock()

	if time.Since(s.lastAsgardUpdate) < constants.ThorchainBlockTime && s.asgardAddresses != nil {
		return s.asgardAddresses, nil
	}

	addresses, err := s.bridge.GetAsgardAddresses(s.config.ChainID)
	if err != nil {
		return nil, err
	}

	// ensure we don't overwrite with empty list
	if len(addresses) > 0 {
		s.asgardAddresses = addresses
	}

	s.lastAsgardUpdate = time.Now()

	return s.asgardAddresses, nil
}

func (s *Scanner) isAsgardAddress(address ledger.Address) bool {
	asgards, err := s.getAsgardAddresses()
	if err != nil {
		s.logger.Err(err).Msg("failed to get asgard addresses")
		return false
	}
	for _, asgard := range asgards {
		if strings.EqualFold(asgard.String(), address.String()) {
			return true
		}
	}
	return false
}

func (s *Scanner) getPoint(height int64) (pcommon.Point, error) {
	_, slot, hash, err := s.db.GetBlockId(uint64(height))
	if err != nil {
		s.logger.Err(err).Int64("height", height).Msg("failed to get block id")
		return pcommon.Point{}, err
	}

	return pcommon.NewPoint(slot, hash), nil
}
