package cardano

import (
	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"

	"github.com/rs/zerolog/log"
)

type Scanner struct {
	config                config.BifrostBlockScannerConfiguration
	logger                zerolog.Logger
	bridge                thorclient.ThorchainBridge
	n2c                   *ouroboros.Connection
	n2n                   *ouroboros.Connection
	networkFee            uint64
	globalNetworkFeeQueue chan common.NetworkFee
}

func NewScanner(
	cfg config.BifrostBlockScannerConfiguration,
	bridge thorclient.ThorchainBridge,
	n2c, n2n *ouroboros.Connection,
) (*Scanner, error) {
	return &Scanner{
		config: cfg,
		logger: log.Logger.With().
			Str("module", "blockscanner").
			Str("chain", cfg.ChainID.String()).
			Logger(),
		bridge: bridge,
		n2c:    n2c,
		n2n:    n2n,
	}, nil
}

// GetHeight returns the block height from tip  minus 1
func (s *Scanner) GetHeight() (int64, error) {
	tip, err := s.n2n.ChainSync().Client.GetCurrentTip()
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to get current tip")
		return -1, err
	}
	return int64(tip.BlockNumber), nil
}

func (s *Scanner) FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error) {
	// TODO implement me
	panic("implement me")
}

func (s *Scanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	// TODO implement me
	panic("implement me")
}

func (s *Scanner) FetchMemPool(height int64) (types.TxIn, error) {
	// TODO implement me
	panic("implement me")
}
