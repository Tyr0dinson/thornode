package thorchain

import (
	"fmt"
	"math/rand"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// CompMgrV1 is an implementation of CompManager
type CompMgrV1 struct{ keeper keeper.Keeper }

func NewCompMgrV1(keeper keeper.Keeper) *CompMgrV1 {
	return &CompMgrV1{keeper: keeper}
}

// FindAvailableName search for a new name for a static composite asset
func (cmgr CompMgrV1) FindAvailableName(ctx cosmos.Context, assets []common.Asset) string {
	height := common.BlockHeight(ctx)
	str := fmt.Sprintf("%d-%s", height, assets)

	var sum int64
	for _, i := range []byte(str) {
		sum += int64(i)
	}

	s1 := rand.NewSource(sum)
	r1 := rand.New(s1)

	norseWords := append(common.Gods, common.Realms...)

	attempts := 0
	for i := 1; i < 100; i++ {
		attempts += i

		bipWord := common.BIP39Words[r1.Intn(len(common.BIP39Words))]
		norseWord := norseWords[r1.Intn(len(norseWords))]
		name := fmt.Sprintf("%s-%s", bipWord, norseWord)

		if cmgr.keeper.CompositeExists(ctx, name) {
			continue
		}
		return name
	}

	return ""
}

func (cmgr CompMgrV1) Rebalance(ctx cosmos.Context, comp Composite) error {
	totalRunePoolDepth := cosmos.ZeroUint()
	totalRuneValue := cosmos.ZeroUint()

	// sum total rune in the pools
	// sum rune value of the holdings of this composite
	for _, coin := range comp.Holdings {
		pool, err := cmgr.keeper.GetPool(ctx, coin.Asset)
		if err != nil {
			return err
		}
		totalRunePoolDepth = totalRunePoolDepth.Add(pool.BalanceRune)

		totalRuneValue = totalRuneValue.Add(pool.AssetValueInRune(coin.Amount))
	}

	for _, coin := range comp.Holdings {
		pool, err := cmgr.keeper.GetPool(ctx, coin.Asset)
		if err != nil {
			return err
		}
	}

	return nil
}
