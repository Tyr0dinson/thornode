package cardano

import (
	"encoding/hex"
	"fmt"
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
	n2n                   *NodeWrapper
	networkMtx            sync.Mutex
	networkFee            uint64
	minFeeA               uint64
	minFeeB               uint64
	asgardAddresses       []common.Address
	lastAsgardUpdate      time.Time
	globalNetworkFeeQueue chan common.NetworkFee
	globalErrataQueue     chan types.ErrataBlock
	rollbackHeight        int64
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
		logger: logger.With().Str("module", "scanner").Logger(),
		config: cfg,
		bridge: bridge,
		db:     storage,
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

		bz, err := hex.DecodeString(hash)
		if err != nil {
			return nil, fmt.Errorf("failed to decode start block hash: %w", err)
		}

		point = pcommon.NewPoint(slot, bz)
	} else {
		tip, err := s.n2c.GetChainPoint()
		if err != nil {
			return nil, fmt.Errorf("failed to get current tip: %w", err)
		}

		point = *tip
	}

	// start chain sync in NewScanner to be able to fetch two consecutive
	// blocks before blockscanner tries to determine last scan position
	err = s.n2n.conn.ChainSync().Client.Sync([]pcommon.Point{point})
	if err != nil {
		return nil, err
	}

	// FetchTxs expects the last _two_ blocks, because it needs
	// the previous block state to extract the sender address from
	// the input utxos
	start := time.Now()
	for {
		if time.Since(start) > time.Second*30 {
			return nil, fmt.Errorf("timeout waiting for block scanner to start")
		}

		time.Sleep(time.Second)

		var height uint64
		height, _, _, err = s.db.GetLastBlockId()
		if err != nil || height == 0 {
			continue
		}

		height, _, _, err = s.db.GetBlockId(height - 1)
		if err != nil {
			continue
		}

		if height > 0 {
			break
		}
	}

	return s, nil
}

func (s *Scanner) Stop() error {
	err := s.n2n.conn.ChainSync().Client.Stop()
	s.n2c.Close()
	s.n2n.Close()
	return err
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
	var allTxIns []*types.TxInItem

	rollbackHeight := atomic.LoadInt64(&s.rollbackHeight)
	if rollbackHeight > 0 && rollbackHeight < fetchHeight {
		s.logger.Info().
			Int64("rollback_height", rollbackHeight).
			Int64("fetch_height", fetchHeight).
			Msg("processing rollback blocks")

		for height := rollbackHeight + 1; height < fetchHeight; height++ {
			txIns, err := s.fetchTxs(height)
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

	if err = s.updateNetworkFee(fetchHeight); err != nil {
		return types.TxIn{}, err
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

	// reset rollbackHeight
	if rollbackHeight > 0 {
		atomic.StoreInt64(&s.rollbackHeight, 0)
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

	block, err := s.n2n.conn.BlockFetch().Client.GetBlock(currentPoint)
	if err != nil {
		s.logger.Err(err).Msg("failed to fetch block")
		return nil, err
	}

	// Scanner.Start() waits for two consecutive blocks to arrive
	var previousPoint pcommon.Point
	previousPoint, err = s.getPoint(height - 1)
	if err != nil {
		return nil, err
	}

	var txIds [][]byte
	var txIns []*types.TxInItem
	for _, tx := range block.Transactions() {
		if !tx.IsValid() {
			continue
		}

		// only tx witch exactly one VKey are supported
		if len(tx.Witnesses().Vkey()) != 1 {
			s.logger.Warn().Msg("tx with multiple witnesses are not supported")
			continue
		}

		// lookup the sender from the first input utxo
		var result *localstatequery.UTxOByTxInResult
		result, err = s.n2c.GetUTxOByTxIn(tx.Consumed(), &previousPoint)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch utxo result for tx: %w", err)
		}

		if len(result.Results) == 0 {
			s.logger.Warn().
				Str("tx", tx.Hash().String()).
				Msg("no utxo result for tx, skipping")
			continue
		}

		var fromAddress ledger.Address

		// We just use the first input for the sender address,
		// txs with multiple VKeys where already discarded
		for _, output := range result.Results {
			fromAddress = output.Address()
			break
		}

		isFromAsgard := s.isAsgardAddress(fromAddress)

		var toAddress ledger.Address
		var amount uint64

		for _, output := range tx.Outputs() {
			toAddress = output.Address()

			// skip txs that are sent to the sender, i.e. for utxo change
			if fromAddress.String() == toAddress.String() {
				continue
			}

			if isFromAsgard || s.isAsgardAddress(toAddress) {
				amount = output.Amount().Uint64()
				break
			}
		}

		if amount == 0 {
			continue
		}

		memo := ExtractMemo(tx.Metadata(), 0)

		coins := common.Coins{{
			Asset:    common.ADAAsset,
			Amount:   sdkmath.NewUint(amount * 100), // 1e6 => 1e8
			Decimals: 6,
		}}

		fee := tx.Fee().Uint64()
		gas := common.Gas{{
			Asset:    common.ADAAsset,
			Amount:   sdkmath.NewUint(fee * 100), // 1e6 => 1e8
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
	defer s.networkMtx.Unlock()

	if minFeeA == s.minFeeA && minFeeB == s.minFeeB {
		return nil
	}

	s.minFeeA = minFeeA
	s.minFeeB = minFeeB

	feeRate := minFeeA*estimatedTxSize + minFeeB + extraFeeBuffer

	resolution := uint64(s.config.GasPriceResolution)
	roundedFeeRate := ((feeRate + (resolution / 2)) / resolution) * resolution

	if roundedFeeRate == 0 {
		return fmt.Errorf("rounded fee rate is zero")
	}

	// no need to update
	if s.networkFee == roundedFeeRate {
		return nil
	}

	s.networkFee = roundedFeeRate

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

	// find invalidated block IDs (slot > rollback slot)
	invalidated, err := s.db.GetBlockIdsAboveSlot(point.Slot)
	if err != nil {
		s.logger.Err(err).Msg("failed to get invalidated block ids")
		return nil
	}
	if len(invalidated) == 0 {
		return nil
	}

	// generate errata for observed txs at invalidated heights
	for _, block := range invalidated {
		var txIds [][]byte
		txIds, err = s.db.GetObservedTxs(block.Height)
		if err != nil {
			s.logger.Err(err).Uint64("height", block.Height).Msg("failed to get observed txs")
			continue
		}

		var errataTxs []types.ErrataTx
		for _, txId := range txIds {
			errataTxs = append(errataTxs, types.ErrataTx{
				TxID:  common.TxID(hex.EncodeToString(txId)),
				Chain: s.config.ChainID,
			})
		}

		if len(errataTxs) > 0 && s.globalErrataQueue != nil {
			s.globalErrataQueue <- types.ErrataBlock{
				Height: int64(block.Height),
				Txs:    errataTxs,
			}
			s.logger.Info().Uint64("height", block.Height).
				Int("txs", len(errataTxs)).Msg("sent errata for rolled-back block")
		}

		// clean up storage for this height
		if err = s.db.DeleteObservedTxs(block.Height); err != nil {
			s.logger.Err(err).Uint64("height", block.Height).Msg("failed to delete observed txs")
		}
		if err = s.db.DeleteBlockId(block.Height); err != nil {
			s.logger.Err(err).Uint64("height", block.Height).Msg("failed to delete block id")
		}
	}

	// set the scan position to the highest valid remaining block so the
	// block scanner resumes from the correct height on next restart
	height, _, _, err := s.db.GetLastBlockId()
	if err != nil {
		s.logger.Err(err).Msg("failed to get rollback height")
		return nil
	}

	if err = s.db.SetScanPos(int64(height)); err != nil {
		s.logger.Err(err).Msg("failed to set scan position")
	} else {
		s.logger.Info().
			Uint64("height", height).
			Msg("set scan position for rollback")
	}

	// signal FetchTxs to reprocess blocks from this height, blockscanner
	// doesn't know of this rollback
	atomic.StoreInt64(&s.rollbackHeight, int64(height))

	return nil
}

func (s *Scanner) getAsgardAddresses() ([]common.Address, error) {
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
