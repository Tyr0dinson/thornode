package thorchain

import (
	"fmt"
	"math/rand"
	"sort"

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

func (cmgr CompMgrV1) Maintain(ctx cosmos.Context, comp *Composite) error {
	var err error
	targetAssets := comp.Holdings.Assets()
	if comp.Type == DynamicComposite {
		targetAssets, err = cmgr.findDynamicAssets(ctx, comp)
		if err != nil {
			return err
		}
	}

	if err := cmgr.Rebalance(ctx, targetAssets, comp); err != nil {
		return err
	}

	return cmgr.keeper.SetComposite(ctx, *comp)
}

func (cmgr CompMgrV1) GetRuneValue(ctx cosmos.Context, comp *Composite) (cosmos.Uint, error) {
	// sum total holdings value, in rune
	totalRuneValue := cosmos.ZeroUint()
	for _, coin := range comp.Holdings {
		if coin.Asset.IsRune() {
			totalRuneValue = totalRuneValue.Add(coin.Amount)
			continue
		}

		pool, err := cmgr.keeper.GetPool(ctx, coin.Asset)
		if err != nil {
			return cosmos.ZeroUint(), err
		}

		totalRuneValue = totalRuneValue.Add(pool.AssetValueInRune(coin.Amount))
	}
	return totalRuneValue, nil
}

func (cmgr CompMgrV1) Rebalance(ctx cosmos.Context, targetAssets []common.Asset, comp *Composite) error {
	// sum total pool'ed rune for target assets
	totalRunePoolDepth := cosmos.ZeroUint()
	for _, asset := range targetAssets {
		pool, err := cmgr.keeper.GetPool(ctx, asset)
		if err != nil {
			return err
		}
		if pool.Status == PoolAvailable {
			totalRunePoolDepth = totalRunePoolDepth.Add(pool.BalanceRune)
		}
	}

	// sum total holdings value, in rune
	totalRuneValue, err := cmgr.GetRuneValue(ctx, comp)
	if err != nil {
		return err
	}

	newHoldings := make(common.Coins, len(targetAssets))
	for i, asset := range targetAssets {
		pool, err := cmgr.keeper.GetPool(ctx, asset)
		if err != nil {
			return err
		}
		poolRune := pool.BalanceRune
		if pool.Status != PoolAvailable {
			poolRune = cosmos.ZeroUint()
		}

		newHoldings[i] = common.NewCoin(
			asset,
			common.GetShare(poolRune, totalRunePoolDepth, totalRuneValue),
		)
	}

	comp.Holdings = newHoldings
	return nil
}

func (cmgr CompMgrV1) findDynamicAssets(ctx cosmos.Context, comp *Composite) ([]common.Asset, error) {
	switch comp.Name {
	case "USD":
		return cmgr.findDynamicAssetsForUSDStableCoin(ctx, comp)
	default:
		return nil, fmt.Errorf("cannot find dynamic assets, %s not implemented", comp.Name)
	}
}

func (cmgr CompMgrV1) findDynamicAssetsForUSDStableCoin(ctx cosmos.Context, comp *Composite) ([]common.Asset, error) {
	sourceAssets := comp.Holdings.Assets()
	if len(sourceAssets) == 0 {
		// default pool(s), used to launch the composite, the asset list is
		// managed by the logic below afterwards
		asset1, err := common.NewAsset("BNB.BUSD-BD1")
		if err != nil {
			return nil, err
		}
		asset2, err := common.NewAsset("BNB.USDT-6D8")
		if err != nil {
			return nil, err
		}
		sourceAssets = []common.Asset{asset1, asset2}
	}

	return cmgr.findSimilarValuedAssets(ctx, sourceAssets, 500)

}

func (cmgr CompMgrV1) findSimilarValuedAssets(ctx cosmos.Context, sourceAssets []common.Asset, basisPts int) ([]common.Asset, error) {
	assets := make([]common.Asset, 0)

	type myRatio struct {
		Ratio  cosmos.Uint
		InRune bool
	}

	// ------------------------
	// get median ratio
	ratios := make([]myRatio, 0)
	for _, asset := range sourceAssets {
		pool, err := cmgr.keeper.GetPool(ctx, asset)
		if err != nil {
			return nil, err
		}
		if pool.Status != PoolAvailable {
			continue
		}
		rat := myRatio{}
		if pool.BalanceRune.LT(pool.BalanceAsset) {
			rat.Ratio = pool.BalanceAsset.Quo(pool.BalanceRune)
			rat.InRune = true
		} else {
			rat.Ratio = pool.BalanceRune.Quo(pool.BalanceAsset)
			rat.InRune = false
		}
		ratios = append(ratios, rat)
	}
	if len(ratios) == 0 {
		return nil, fmt.Errorf("unable to find median rune ratio to asset")
	}

	// sort ratios
	sort.Slice(ratios[:], func(i, j int) bool {
		return ratios[i].Ratio.LT(ratios[j].Ratio)
	})

	ratio := myRatio{}
	if len(ratios)%2 == 0 {
		// even number of ratios, need additional math to find the median.
		// Average out the two middle numbers
		high := ratios[len(ratios)/2].Ratio
		low := ratios[len(ratios)/2-1].Ratio
		ratio.Ratio = (high.Add(low)).QuoUint64(2)
		ratio.InRune = ratios[len(ratios)/2].InRune
	} else {
		ratio = ratios[len(ratios)/2]
	}
	if ratio.Ratio.IsZero() {
		return nil, fmt.Errorf("unable to find median rune ratio for asset. Cannot be zero.")
	}
	// ------------------------

	iterator := cmgr.keeper.GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := cmgr.keeper.Cdc().UnmarshalBinaryBare(iterator.Value(), &pool); err != nil {
			return nil, err
		}

		r := cosmos.ZeroUint()
		if ratio.InRune {
			r = pool.BalanceAsset.Quo(pool.BalanceRune)
		} else {
			r = pool.BalanceRune.Quo(pool.BalanceAsset)
		}

		diff := cosmos.ZeroUint()
		if r.LT(ratio.Ratio) {
			diff = ratio.Ratio.Sub(r)
		} else {
			diff = r.Sub(ratio.Ratio)
		}
		if diff.IsZero() {
			assets = append(assets, pool.Asset)
			continue
		}
		if ratio.Ratio.Quo(diff).GTE(cosmos.NewUint(uint64(10000 / basisPts))) {
			assets = append(assets, pool.Asset)
		}

	}

	return assets, nil
}
