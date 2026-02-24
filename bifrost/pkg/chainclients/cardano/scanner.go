package cardano

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
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

	return s, nil
}

func (s *Scanner) Start() error {
	var point pcommon.Point

	if s.config.StartBlockHeight > 0 {
		slot := s.config.Cardano.StartBlockSlot
		if slot == 0 {
			return fmt.Errorf("start block slot is zero")
		}

		hash := s.config.Cardano.StartBlockHash
		if hash == "" {
			return fmt.Errorf("start block hash is undefined")
		}

		bz, err := hex.DecodeString(hash)
		if err != nil {
			return fmt.Errorf("failed to decode start block hash: %w", err)
		}

		point = pcommon.NewPoint(slot, bz)
	} else {
		tip, err := s.n2c.GetChainPoint()
		if err != nil {
			return fmt.Errorf("fail to get current tip: %w", err)
		}
		point = *tip
	}

	err := s.n2n.conn.ChainSync().Client.Sync([]pcommon.Point{point})
	if err != nil {
		return err
	}

	// wait for the first block
	start := time.Now()
	for {
		time.Sleep(1 * time.Second)

		height, _, _, _ := s.db.GetLastBlockId()
		if height > 0 {
			break
		}

		if time.Since(start).Seconds() > 10 {
			return fmt.Errorf("timeout waiting for block scanner to start")
		}
	}

	return nil
}

func (s *Scanner) Stop() error {
	return s.n2n.conn.ChainSync().Client.Stop()
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
	s.logger.Info().Msgf("FetchTxs: fetch_height=%d chain_height=%d", fetchHeight, chainHeight)
	_, slot, hash, err := s.db.GetBlockId(uint64(fetchHeight))
	if err != nil {
		s.logger.Err(err).Msgf("fail to find block id for height=%d", fetchHeight)
		return types.TxIn{}, err
	}
	block, err := s.n2n.conn.BlockFetch().Client.GetBlock(pcommon.NewPoint(slot, hash))
	if err != nil {
		s.logger.Err(err).Msg("fail to fetch block")
		return types.TxIn{}, err
	}

	var txIns []*types.TxInItem
	for _, tx := range block.Transactions() {
		s.logger.Info().Str("hash", tx.Hash().String()).Msg("processing transaction")
		if !tx.IsValid() {
			continue
		}

		// try to get sender from witness
		// if we make certain assumptions about tx origins we can skip n2c protocols and utxo queries
		if len(tx.Witnesses().Vkey()) != 1 {
			continue
		}

		fromAddress, err := AddressFromBytes(tx.Witnesses().Vkey()[0].Vkey)
		if err != nil {
			s.logger.Err(err).Msg("fail to get sender address")
			continue
		}

		// expectation is one output address per filtered tx
		// see: https://gitlab.com/thorchain/thornode/-/blob/develop/bifrost/pkg/chainclients/utxo/client_internal.go#L724
		// which collapses multiple VOUTs into a single address based on filter criteria
		if len(tx.Outputs()) == 0 {
			continue
		}
		output := tx.Outputs()[0]

		toAddress := output.Address()

		// skip txs not related to us
		if !s.isAsgardAddress(fromAddress) && !s.isAsgardAddress(toAddress) {
			continue
		}

		// skip txs that are sent to the sender, i.e. for utxo change
		if fromAddress.String() == toAddress.String() {
			continue
		}

		memo := ExtractMemo(tx.Metadata(), 0)

		amount := output.Amount().Uint64() * 100
		coins := common.Coins{{
			Asset:    common.ADAAsset,
			Amount:   sdkmath.NewUint(amount),
			Decimals: 6,
		}}

		fee := tx.Fee().Uint64() * 100
		gas := common.Gas{{
			Asset:    common.ADAAsset,
			Amount:   sdkmath.NewUint(fee),
			Decimals: 6,
		}}

		txIns = append(txIns, &types.TxInItem{
			Tx:          tx.Hash().String(),
			BlockHeight: fetchHeight,
			Memo:        memo,
			Sender:      fromAddress.String(),
			To:          toAddress.String(),
			Coins:       coins,
			Gas:         gas,
		})
	}

	s.logger.Info().Msgf("done fetch")

	if err = s.updateNetworkFee(fetchHeight); err != nil {
		return types.TxIn{}, err
	}

	s.logger.Info().Msgf("done network fee")

	err = s.db.PruneBlockIds(uint64(fetchHeight))
	if err != nil {
		s.logger.Err(err).Msg("fail to prune block ids")
	}

	if fetchHeight > transactionTimeout {
		err = s.db.PruneSpentUtxos(uint64(fetchHeight - transactionTimeout))
		if err != nil {
			s.logger.Err(err).Msg("fail to prune spent utxos")
		}
	}

	s.logger.Info().Msgf("done pruning")

	return types.TxIn{
		Chain:    s.config.ChainID,
		TxArray:  txIns,
		Filtered: false,
		MemPool:  false,
	}, nil
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

	feeRate := minFeeA*estimatedTxSize + minFeeB + 1

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
		err := s.db.SetBlockId(h.BlockNumber(), h.SlotNumber(), h.Hash().Bytes())
		s.logger.Info().
			Uint64("block_number", h.BlockNumber()).
			Uint64("slot_number", h.SlotNumber()).
			Str("hash", h.Hash().String()).
			Msg("roll forward")
		if err != nil {
			s.logger.Err(err).Msg("fail to set block id")
		}
	}
	return nil
}

func (s *Scanner) rollBackwardHandler(
	_ chainsync.CallbackContext,
	point pcommon.Point,
	tip chainsync.Tip,
) error {
	s.logger.Info().Msgf("roll backward: point = (%d, %x), tip = (%d, %x)",
		point.Slot, point.Hash, tip.Point.Slot, tip.Point.Hash,
	)
	return nil
}

func (s *Scanner) getAsgardAddresses() ([]common.Address, error) {
	if time.Since(s.lastAsgardUpdate) < constants.ThorchainBlockTime && s.asgardAddresses != nil {
		return s.asgardAddresses, nil
	}
	newAddresses, err := utxo.GetAsgardAddress(s.config.ChainID, s.bridge)
	if err != nil {
		return nil, fmt.Errorf("failed to get asgards: %w", err)
	}
	if len(newAddresses) > 0 { // ensure we don't overwrite with empty list
		s.asgardAddresses = newAddresses
	}
	s.lastAsgardUpdate = time.Now()
	return s.asgardAddresses, nil
}

func (s *Scanner) isAsgardAddress(address ledger.Address) bool {
	asgards, err := s.getAsgardAddresses()
	if err != nil {
		s.logger.Err(err).Msg("fail to get asgard addresses")
		return false
	}
	for _, asgard := range asgards {
		if strings.EqualFold(asgard.String(), address.String()) {
			return true
		}
	}
	return false
}
