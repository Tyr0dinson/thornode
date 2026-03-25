package core

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// SolvencyCheckActor
////////////////////////////////////////////////////////////////////////////////////////

type SolvencyCheckActor struct {
	Actor
}

var xmrSolvencyTolerance = cosmos.NewUint(25)

const (
	xmrSolvencyRetryAttempts = 36
	xmrSolvencyRetryDelay    = 5 * time.Second
)

type solvencyMismatch struct {
	asset       common.Asset
	assetStr    string
	vaultAddr   common.Address
	vaultAmount cosmos.Uint
	l1Amount    cosmos.Uint
	diff        cosmos.Uint
	tolerance   cosmos.Uint
}

func NewSolvencyCheckActor(rng *rand.Rand) *Actor {
	a := &SolvencyCheckActor{
		Actor: *NewActor("SolvencyCheck", rng),
	}

	a.Ops = append(a.Ops, a.checkSolvency)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func solvencyDiff(a, b cosmos.Uint) cosmos.Uint {
	if a.GTE(b) {
		return a.Sub(b)
	}
	return b.Sub(a)
}

func solvencyTolerance(asset common.Asset) cosmos.Uint {
	if asset.Equals(common.XMRAsset) {
		return xmrSolvencyTolerance
	}
	return cosmos.ZeroUint()
}

func solvencyAmountsMatch(asset common.Asset, vaultAmount, l1Amount cosmos.Uint) (bool, cosmos.Uint, cosmos.Uint) {
	diff := solvencyDiff(vaultAmount, l1Amount)
	tolerance := solvencyTolerance(asset)
	return diff.LTE(tolerance), diff, tolerance
}

func solvencyMismatchRetryable(mismatches []solvencyMismatch) bool {
	if len(mismatches) == 0 {
		return false
	}
	for _, mismatch := range mismatches {
		if !mismatch.asset.Equals(common.XMRAsset) {
			return false
		}
	}
	return true
}

func (a *SolvencyCheckActor) checkSolvency(config *OpConfig) OpResult {
	// wait a moment for any blocks to be created
	time.Sleep(time.Second * 5)

	// find a user to lookup L1 balances
	var user *User
	userAcquired := false
	for _, user = range config.Users {
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())

		// skip users already being used
		if user.Acquire() {
			userAcquired = true
			break
		}
	}
	if !userAcquired {
		a.Log().Error().Msg("no user available")
		return OpResult{
			Continue: false,
		}
	}
	defer user.Release()

	for attempt := 1; attempt <= xmrSolvencyRetryAttempts; attempt++ {
		// get all vaults
		vaults, err := thornode.GetVaults()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get vaults")
			return OpResult{
				Continue: false,
			}
		}
		vaultCoinAmounts := make(map[string]cosmos.Uint)
		for _, vault := range vaults {
			for _, coin := range vault.Coins {
				if _, ok := vaultCoinAmounts[coin.Asset]; !ok {
					vaultCoinAmounts[coin.Asset] = cosmos.ZeroUint()
				}
				vaultCoinAmounts[coin.Asset] = vaultCoinAmounts[coin.Asset].Add(cosmos.NewUintFromString(coin.Amount))
			}
		}

		// assert only one vault
		if len(vaults) != 1 {
			a.Log().Error().Msg("expected only one vault")
			return OpResult{
				Continue: false,
			}
		}
		pubkey, err := common.NewPubKey(*vaults[0].PubKey)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to parse vault pubkey")
			return OpResult{
				Continue: false,
			}
		}
		pubkeyEddsa, err := common.NewPubKey(*vaults[0].PubKeyEddsa)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to parse vault eddsa pubkey")
			return OpResult{
				Continue: false,
			}
		}

		// get all L1 balances
		l1CoinAmounts := make(map[string]cosmos.Uint)
		vaultAssets := make(map[string]common.Asset)
		vaultAddrs := make(map[string]common.Address)
		for assetStr := range vaultCoinAmounts {
			asset, err := common.NewAsset(assetStr)
			if err != nil {
				a.Log().Fatal().Err(err).Msg("failed to parse asset")
			}
			vaultAssets[assetStr] = asset

			if asset.IsGasAsset() {
				switch asset.Chain.GetSigningAlgo() {
				case common.SigningAlgoEd25519:
					vaultAddr, err := pubkeyEddsa.GetAddress(asset.Chain)
					if err != nil {
						a.Log().Error().Err(err).Msg("failed to get vault address for eddsa pubkey")
						return OpResult{
							Continue: false,
						}
					}
					vaultAddrs[assetStr] = vaultAddr
					l1Acct, err := user.ChainClients[asset.Chain].GetAccount(&pubkeyEddsa)
					if err != nil {
						a.Log().Error().Err(err).Msg("failed to get L1 account for eddsa pubkey")
						return OpResult{
							Continue: false,
						}
					}
					l1CoinAmounts[assetStr] = l1Acct.Coins.GetCoin(asset).Amount
				case common.SigningAlgoSecp256k1:
					vaultAddr, err := pubkey.GetAddress(asset.Chain)
					if err != nil {
						a.Log().Error().Err(err).Msg("failed to get vault address for secp256k1 pubkey")
						return OpResult{
							Continue: false,
						}
					}
					vaultAddrs[assetStr] = vaultAddr
					l1Acct, err := user.ChainClients[asset.Chain].GetAccount(&pubkey)
					if err != nil {
						a.Log().Error().Err(err).Msg("failed to get L1 account")
						return OpResult{
							Continue: false,
						}
					}
					l1CoinAmounts[assetStr] = l1Acct.Coins.GetCoin(asset).Amount
				default:
					a.Log().Fatal().Msgf("unsupported signing algo %s for chain %s", asset.Chain.GetSigningAlgo(), asset.Chain)
				}
			} else if asset.Chain.IsEVM() {
				// for EVM chains, we need to get the balance from the contract
				_, routerAddr, err := thornode.GetInboundAddress(asset.Chain)
				if err != nil {
					a.Log().Error().Err(err).Msg("failed to get router address")
					return OpResult{
						Continue: false,
					}
				}
				vaultAddr, err := pubkey.GetAddress(asset.Chain)
				if err != nil {
					a.Log().Error().Err(err).Msg("failed to get vault address")
					return OpResult{
						Continue: false,
					}
				}
				vaultAddrs[assetStr] = vaultAddr
				balance, err := user.ChainClients[asset.Chain].(*evm.Client).GetVaultAllowance(*routerAddr, vaultAddr, asset)
				if err != nil {
					a.Log().Error().Err(err).Msg("failed to get vault allowance")
					return OpResult{
						Continue: false,
					}
				}
				l1CoinAmounts[assetStr] = balance
			} else {
				a.Log().Error().Msgf("unsupported asset %s", asset)
				return OpResult{
					Continue: false,
					Error:    fmt.Errorf("unsupported asset %s", asset),
				}
			}
		}

		// verify vault and on chain amount are equal for all assets
		mismatches := make([]solvencyMismatch, 0)
		for assetStr, vaultAmount := range vaultCoinAmounts {
			l1Amount, ok := l1CoinAmounts[assetStr]
			if !ok {
				a.Log().Error().Str("asset", assetStr).Msg("L1 amount not found for asset")
			}
			asset := vaultAssets[assetStr]
			match, diff, tolerance := solvencyAmountsMatch(asset, vaultAmount, l1Amount)
			if !match {
				mismatches = append(mismatches, solvencyMismatch{
					asset:       asset,
					assetStr:    assetStr,
					vaultAddr:   vaultAddrs[assetStr],
					vaultAmount: vaultAmount,
					l1Amount:    l1Amount,
					diff:        diff,
					tolerance:   tolerance,
				})
				continue
			}
			if !diff.IsZero() {
				a.Log().Warn().
					Str("asset", assetStr).
					Str("vault_address", vaultAddrs[assetStr].String()).
					Str("vault_amount", vaultAmount.String()).
					Str("l1_amount", l1Amount.String()).
					Str("difference", diff.String()).
					Str("tolerance", tolerance.String()).
					Msg("vault and L1 amounts differ within tolerance")
			}
		}
		if len(mismatches) == 0 {
			return OpResult{
				Continue: true,
			}
		}

		retryable := attempt < xmrSolvencyRetryAttempts && solvencyMismatchRetryable(mismatches)
		for _, mismatch := range mismatches {
			logMismatch := a.Log().Error
			if retryable {
				logMismatch = a.Log().Warn
			}
			logMismatch().
				Str("asset", mismatch.assetStr).
				Str("vault_address", mismatch.vaultAddr.String()).
				Str("vault_pubkey", pubkey.String()).
				Str("vault_pubkey_eddsa", pubkeyEddsa.String()).
				Str("vault_amount", mismatch.vaultAmount.String()).
				Str("l1_amount", mismatch.l1Amount.String()).
				Str("difference", mismatch.diff.String()).
				Str("tolerance", mismatch.tolerance.String()).
				Msg("vault and L1 amounts do not match")
		}
		if retryable {
			a.Log().Warn().
				Int("attempt", attempt).
				Int("max_attempts", xmrSolvencyRetryAttempts).
				Dur("delay", xmrSolvencyRetryDelay).
				Msg("retrying XMR solvency check after transient scanner lag")
			time.Sleep(xmrSolvencyRetryDelay)
			continue
		}
		return OpResult{
			Continue: false,
			Error:    fmt.Errorf("vault and L1 amounts do not match"),
			Finish:   true,
		}
	}

	return OpResult{
		Continue: false,
		Error:    fmt.Errorf("xmr solvency retry loop exhausted"),
		Finish:   true,
	}
}
