package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestRefundFundingMatchesFundsAndChainAddressCase(t *testing.T) {
	for _, chain := range []common.Chain{common.ETHChain, common.BTCChain, common.TRONChain} {
		t.Run(chain.String(), func(t *testing.T) {
			original := common.NewObservedTx(common.Tx{
				ID: "id", Chain: chain, FromAddress: "sender", ToAddress: "aBc", Memo: "swap",
				Coins: common.NewCoins(common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(100))),
			}, 10, "vault", 10)
			actual := &stypes.TxInItem{
				Tx: "id", Sender: "sender", To: "aBc", Memo: "swap", Coins: original.Tx.Coins,
				Gas: common.Gas{common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(77))},
			}
			require.True(t, RefundFundingMatches(original, actual), "gas is not the credited funding")
			actual.To = "AbC"
			require.Equal(t, chain.IsEVM(), RefundFundingMatches(original, actual), "base58 addresses must retain their case")
			actual.To = "aBc"
			actual.Coins = common.NewCoins(common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(99)))
			require.False(t, RefundFundingMatches(original, actual))
		})
	}
}

func TestRefundFundingResolvedMemoDoesNotChangeFunding(t *testing.T) {
	original := common.NewObservedTx(common.Tx{ID: common.TxID(strings.Repeat("A", 64)), Chain: common.ETHChain, FromAddress: "sender", ToAddress: "vault", Memo: "SWAP:THOR.RUNE", Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100)))}, 10, "", 10)
	for _, memo := range []string{"", "r:12345"} {
		actual := &stypes.TxInItem{Tx: original.Tx.ID.String(), Sender: "sender", To: "vault", Memo: memo, Coins: original.Tx.Coins}
		require.True(t, RefundFundingMatches(original, actual))
	}
}
