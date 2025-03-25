package ictest

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"

	"gitlab.com/thorchain/thornode/v3/test/ictest/features"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func BootstrapPools(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, exoChains ExoChains) {
	eg, egCtx := errgroup.WithContext(ctx)
	for _, exoChain := range exoChains {
		exoChain := exoChain
		eg.Go(func() error {
			_, lper, err := features.DualLp(t, egCtx, thorchain, exoChain.chain)
			if err != nil {
				return err
			}
			exoChain.lpers = append(exoChain.lpers, lper)
			return nil
		})
	}
	require.NoError(t, eg.Wait())
}

func CreateSavers(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, exoChains ExoChains) {
	require.Fail(t, "savers functionality is disabled")
	eg, egCtx := errgroup.WithContext(ctx)
	for _, exoChain := range exoChains {
		exoChain := exoChain
		eg.Go(func() error {
			saver, err := features.Saver(t, egCtx, thorchain, exoChain.chain)
			if err != nil {
				return err
			}
			exoChain.savers = append(exoChain.savers, saver)
			return nil
		})
	}
	require.NoError(t, eg.Wait())
}

func CreateArbers(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, exoChains ExoChains) *time.Timer {
	_, arbTimer, err := features.Arb(t, ctx, thorchain, exoChains.GetChains()...)
	require.NoError(t, err)

	return arbTimer
}

// --------------------------------------------------------
// Swap - only swaps non-rune assets for now
// these swaps happen in parallel, pool depth is low
// so only the starting place for the random chain pairing is random
// ensures an asset is not swapped too much at the same time for the 
// min/max vs actual swap check to work and be of value
// --------------------------------------------------------
func PerformSwaps(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, exoChains ExoChains) {
	if len(exoChains) < 2 {
		t.Error("must have at least 2 exochains to perform swaps")
	}
	eg, egCtx := errgroup.WithContext(ctx)
	exoChainList := exoChains.GetChains()
	randomChain := rand.Intn(len(exoChainList))
	if randomChain == 0 {
		randomChain++
	}
	for i := range exoChainList {
		i := i
		fmt.Println("Chain:", i, "Name:", exoChainList[i].Config().Name)
		randomChain := (randomChain + i) % len(exoChainList)
		eg.Go(func() error {
			return features.DualSwap(t, egCtx, thorchain, exoChainList[i], exoChainList[randomChain])
		})
	}
	require.NoError(t, eg.Wait())
}

// ------------------------------------------------------------
// Saver Eject - must be done sequentially due to mimir states
// ------------------------------------------------------------
func PerformSaverEjects(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, exoChains ExoChains) {
	require.Fail(t, "savers functionality is disabled")
	mimirLock := sync.Mutex{}
	eg, egCtx := errgroup.WithContext(ctx)
	for _, exoChain := range exoChains {
		exoChain := exoChain
		eg.Go(func() error {
			_, err := features.SaverEject(t, egCtx, &mimirLock, thorchain, exoChain.chain, exoChain.savers...)
			if err != nil {
				return err
			}
			return nil
		})
	}
	require.NoError(t, eg.Wait())
}

func Ragnarok(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, exoChains ExoChains) {
	eg, egCtx := errgroup.WithContext(ctx)
	for _, exoChain := range exoChains {
		exoChain := exoChain
		eg.Go(func() error {
			refundWallets := append(exoChain.lpers, exoChain.savers...)
			return features.Ragnarok(t, egCtx, thorchain, exoChain.chain, refundWallets...)
		})
	}
	require.NoError(t, eg.Wait())
}