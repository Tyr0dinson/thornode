package evm

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	sharedevm "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	ctypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

var (
	_ ctypes.RefundFundingValidator     = (*EVMClient)(nil)
	_ ctypes.RefundFundingHintValidator = (*EVMClient)(nil)
)

func (c *EVMClient) ValidateRefundFunding(ctx context.Context, original common.ObservedTx) error {
	return c.ValidateRefundFundingWithHints(ctx, original, nil)
}

func (c *EVMClient) ValidateRefundFundingWithHints(ctx context.Context, original common.ObservedTx, minedHeights []int64) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if c.evmScanner == nil || c.evmScanner.ethClient == nil || c.evmScanner.pubkeyMgr == nil {
		return fmt.Errorf("EVM funding scanner unavailable")
	}
	vault, err := original.ObservedPubKey.GetAddress(c.cfg.ChainID)
	if err != nil || original.ObservedPubKey.IsEmpty() || !vault.Equals(original.Tx.ToAddress) {
		return fmt.Errorf("funding vault identity mismatch")
	}
	router := c.evmScanner.pubkeyMgr.GetContract(c.cfg.ChainID, original.ObservedPubKey)
	if router.IsEmpty() {
		known, lookupErr := c.bridge.GetVault(original.ObservedPubKey.String())
		if lookupErr == nil {
			router = known.GetContract(c.cfg.ChainID).Router
		}
	}
	parser := sharedevm.RefundFundingParser{Chain: c.cfg.ChainID, Signer: c.evmScanner.eipSigner, ABI: c.evmScanner.vaultABI, MaxLogs: int64(c.evmScanner.cfg.MaxContractTxLogs), Metadata: c.evmScanner.tokenManager.GetTokenMeta, Vaults: map[string]common.Address{strings.ToLower(vault.String()): router}}
	required := int64(0)
	var tipTag *big.Int
	switch c.cfg.ChainID {
	case common.AVAXChain:
	case common.BSCChain:
		required = 3
	case common.POLChain:
		required = int64(polConfirmations)
	case common.BASEChain:
		required = 12
		if common.CurrentChainNetwork != common.MockNet {
			tipTag = big.NewInt(int64(rpc.SafeBlockNumber))
		}
	default:
		return fmt.Errorf("unsupported EVM refund funding chain: %s", c.cfg.ChainID)
	}
	// The signed final voter carries H+required, not the mined block H. This
	// chain-policy subtraction is only a bounded search hint, never proof.
	heights := append([]int64(nil), minedHeights[:min(len(minedHeights), 16)]...)
	if original.FinaliseHeight > required {
		heights = append(heights, original.FinaliseHeight-required)
	}
	return sharedevm.ValidateRefundFunding(ctx, original, sharedevm.RefundFundingSource{
		MinedHeights: heights,
		RPC:          c.evmScanner.ethClient, Chain: c.cfg.ChainID,
		Extract: func(tx *etypes.Transaction, receipt *etypes.Receipt) (*stypes.TxInItem, error) {
			return parser.Extract(tx, receipt, false)
		},
		Required: func(context.Context, *etypes.Block, *stypes.TxInItem, map[ecommon.Hash]*etypes.Receipt) (int64, error) {
			return required, nil
		},
		Tip: func(ctx context.Context) (*etypes.Header, error) {
			return c.evmScanner.ethClient.HeaderByNumber(ctx, tipTag)
		},
	})
}
