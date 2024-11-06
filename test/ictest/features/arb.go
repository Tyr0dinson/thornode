package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/chain/thorchain/common"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"golang.org/x/sync/errgroup"
	
	tcommon "gitlab.com/thorchain/thornode/common"
)

func Arb(
	t *testing.T,
	ctx context.Context,
	thorchain *tc.Thorchain,
	exoChains ...ibc.Chain,
) (users []ibc.Wallet, arbTimer *time.Timer, err error) {
	fmt.Println("#### Arb")
	chains := append(exoChains, thorchain)
	users, err = GetAndFundTestUsers(t, ctx, "arb", false, chains...)
	if err != nil {
		return users, nil, err
	}

	err = AddAdminIfNecessary(ctx, thorchain)
	if err != nil {
		return users, nil, err
	}

	mimirs, err := thorchain.APIGetMimirs(ctx)
	if err != nil {
		return users, nil, err
	}

	if mimir, ok := mimirs[strings.ToUpper("TradeAccountsEnabled")]; ok && mimir != int64(1) || !ok {
		err := thorchain.SetMimir(ctx, "admin", "TradeAccountsEnabled", "1")
		if err != nil {
			return users, nil, err
		}
	}

	thorUser := users[len(users)-1]

	var eg errgroup.Group
	for i, exoChain := range exoChains {
		i := i
		exoChain := exoChain
		eg.Go(func() error {
			exoChainType, err := common.NewChain(exoChain.Config().Name)
			if err != nil {
				return err
			}

			exoUser := users[i]

			exoUserBalance, err := exoChain.GetBalance(ctx, exoUser.FormattedAddress(), exoChain.Config().Denom)
			if err != nil {
				return err
			}

			memo := fmt.Sprintf("trade+:%s", thorUser.FormattedAddress())
			exoInboundAddr, _, err := thorchain.APIGetInboundAddress(ctx, exoChainType.String())
			if err != nil {
				return err
			}
			_, err = exoChain.SendFundsWithNote(ctx, exoUser.KeyName(), ibc.WalletAmount{
				Address: exoInboundAddr,
				Denom:   exoChain.Config().Denom,
				Amount:  exoUserBalance.QuoRaw(10).MulRaw(9),
			}, memo)

			return err
		})
	}
	if err := eg.Wait(); err != nil {
		return users, nil, err
	}

	arbTimer = time.NewTimer(time.Second)
	go func() {
		type Pool struct {
			BalanceRune  sdkmath.Uint
			BalanceAsset sdkmath.Uint
		}
		originalPools := make(map[string]Pool)
		maxBasisPts := uint64(10_000)

		arbCtx := context.Background()
		arbCtx, arbCtxCancel := context.WithCancel(arbCtx)
		t.Cleanup(func() {
			arbCtxCancel()
		})

		for {
			arbTimer.Reset(time.Second)
			select {
			case <-arbCtx.Done():
				return
			case <-arbTimer.C:
				pools, err := thorchain.APIGetPools(ctx)
				if err != nil {
					fmt.Println("Error getting arb api pools", err)
					continue
				}

				allPoolsSuspended := true
				arbPools := []tc.Pool{}
				for _, pool := range pools {
					if pool.Status != "Suspended" {
						allPoolsSuspended = false
					}

					// skip unavailable pools and those with no liquidity
					if pool.BalanceRune == "0" || pool.BalanceAsset == "0" || pool.Status != "Available" {
						continue
					}

					// if this is the first time we see the pool, store it to use as the target price
					if _, ok := originalPools[pool.Asset]; !ok {
						originalPools[pool.Asset] = Pool{
							BalanceRune:  sdkmath.NewUintFromString(pool.BalanceRune),
							BalanceAsset: sdkmath.NewUintFromString(pool.BalanceAsset),
						}
						continue
					}

					arbPools = append(arbPools, pool)
				}

				if allPoolsSuspended {
					continue
				}

				if len(arbPools) < 2 {
					continue
				}

				// sort pools by price change
				priceChangeBps := func(pool tc.Pool) int64 {
					originalPool := originalPools[pool.Asset]
					originalPrice := originalPool.BalanceRune.MulUint64(1e8).Quo(originalPool.BalanceAsset)
					currentPrice := sdkmath.NewUintFromString(pool.BalanceRune).MulUint64(1e8).Quo(sdkmath.NewUintFromString(pool.BalanceAsset))
					return int64(maxBasisPts) - int64(originalPrice.MulUint64(maxBasisPts).Quo(currentPrice).Uint64())
				}
				sort.Slice(arbPools, func(i, j int) bool {
					return priceChangeBps(arbPools[i]) > priceChangeBps(arbPools[j])
				})

				send := arbPools[0]
				receive := arbPools[len(arbPools)-1]

				// skip if none have diverged more than 10 basis points
				adjustmentBps := tcommon.Min(tcommon.Abs(priceChangeBps(send)),tcommon.Abs(priceChangeBps(receive)))
				if adjustmentBps < 10 {
					// pools have not diverged enough
					continue
				}

				// build the swap
				memo := fmt.Sprintf("=:%s", strings.Replace(receive.Asset, ".", "~", 1))
				asset, err := common.NewAsset(strings.Replace(send.Asset, ".", "~", 1))
				if err != nil {
					fmt.Println("Error building arb swap asset", err)
					continue
				}
				amount := sdkmath.NewUint(uint64(adjustmentBps / 10)).Mul(sdkmath.NewUintFromString(send.BalanceAsset)).QuoUint64(maxBasisPts)

				fmt.Println("Arbing:", amount, asset.String(), memo)
				err = thorchain.Deposit(ctx, thorUser.KeyName(), sdkmath.Int(amount), asset.String(), memo)
				if err != nil {
					fmt.Println("Error:", err)
				} else {
					fmt.Println("No arb error")
				}
			}
		}
	}()

	return users, arbTimer, nil
}
