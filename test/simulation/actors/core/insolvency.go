package core

import (
	"fmt"
	"time"

	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// InsolvencyActor is a simulation test actor designed to verify that Bifrost and
// THORChain's solvency checker correctly detects and handles on-chain insolvency.
type InsolvencyActor struct {
	Actor

	chain         common.Chain
	asset         common.Asset
	nodeClient    LiteChainClient
	userClient    LiteChainClient
	nodeUser      *User
	userAccount   *User
	nodeL1Address common.Address
	stolenAmount  cosmos.Uint
	haltDuration  int64
}

// bumpBaseBlock submits a zero-value self-transfer on BASE to force block production in mocknet.
func (a *InsolvencyActor) bumpBaseBlock() {
	if !a.chain.Equals(common.BASEChain) || a.userAccount == nil {
		return
	}
	to := a.userAccount.Address(a.chain)
	if _, err := Send(a.Log(), a.userAccount, to, a.asset, "tick", cosmos.ZeroUint()); err != nil {
		a.Log().Debug().Err(err).Msg("bumpBaseBlock: failed to send tick tx")
	}
}

func NewInsolvencyActor(chain common.Chain, rng *rand.Rand) *Actor {
	a := &InsolvencyActor{
		Actor:        *NewActor(fmt.Sprintf("Insolvency-%s", chain), rng),
		chain:        chain,
		asset:        chain.GetGasAsset(),
		haltDuration: 3,
	}
	a.Timeout = 5 * time.Minute
	a.Interval = time.Second

	a.SetLogger(a.Log().With().Stringer("asset", a.asset).Logger())

	// 1. Setup
	a.Ops = append(a.Ops, a.setup)

	// 2. Steal
	a.Ops = append(a.Ops, a.stealFunds)

	// 3. Verify halt
	a.Ops = append(a.Ops, a.verifyInsolvencyHalt)

	// 4. Verify it stays halted for a few blocks
	a.Ops = append(a.Ops, a.verifyInsolvencyHaltDuration)

	// 5. Restore
	a.Ops = append(a.Ops, a.restoreFunds)

	// 6. Verify un-halt
	a.Ops = append(a.Ops, a.verifySolvencyUnhalt)

	// 7. Cleanup
	a.Ops = append(a.Ops, a.cleanup)

	return &a.Actor
}

// setup acquires accounts and identifies the active Asgard vault.
func (a *InsolvencyActor) setup(config *OpConfig) OpResult {
	for _, user := range config.Users {
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())
		if !user.Acquire() {
			continue
		}
		a.userAccount = user
		break
	}

	if a.userAccount == nil {
		a.Log().Info().Msg("no available user accounts to acquire, waiting")
		return OpResult{Continue: false}
	}

	var ok bool
	a.userClient, ok = a.userAccount.ChainClients[a.asset.Chain]
	if !ok {
		return OpResult{
			Error:  fmt.Errorf("failed to get user chain client for chain %s", a.chain),
			Finish: true,
		}
	}

	asgards, err := thornode.GetVaults()
	if err != nil || len(asgards) == 0 {
		return OpResult{
			Error:  fmt.Errorf("failed to get asgard vaults: %w", err),
			Finish: true,
		}
	}
	asgardVault := asgards[0]

	if len(config.NodeUsers) == 0 {
		return OpResult{
			Error:  fmt.Errorf("no node users configured"),
			Finish: true,
		}
	}
	a.nodeUser = config.NodeUsers[0]
	if !a.nodeUser.Acquire() {
		a.Log().Info().Msg("node user locked, waiting")
		return OpResult{Continue: false}
	}

	var asgardVaultPubKey string
	if asgardVault.PubKey == nil {
		asgardVaultPubKey = "nil"
	} else {
		asgardVaultPubKey = *asgardVault.PubKey
	}
	if asgardVaultPubKey != a.nodeUser.PubKey(common.BASEChain).String() {
		err = fmt.Errorf("asgard pubkey (%s) is not the same as the node pubkey (%s), this test won't work", asgardVaultPubKey, a.nodeUser.PubKey(common.BASEChain))
		return OpResult{
			Error:  err,
			Finish: true,
		}
	}
	a.Log().Info().Msg("node pubkey matches the Asgard vault pubkey, insolvency test can proceed")

	a.nodeL1Address, err = a.nodeUser.PubKey(a.chain).GetAddress(a.asset.Chain)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get asgard L1 address for chain %s: %w", a.chain, err),
			Finish: true,
		}
	}

	a.Log().Info().
		Stringer("vault_pubkey", a.nodeUser.PubKey(a.chain)).
		Stringer("asgard_l1_address", a.nodeL1Address).
		Msg("setup complete")

	a.nodeClient, ok = a.nodeUser.ChainClients[a.chain]
	if !ok {
		return OpResult{
			Error:  fmt.Errorf("failed to get node chain client for chain %s", a.chain),
			Finish: true,
		}
	}

	return OpResult{Continue: true}
}

func (a *InsolvencyActor) stealFunds(config *OpConfig) OpResult {
	a.Log().Info().Msg("attempting to steal funds from Asgard vault to create insolvency")

	nodeBalance, err := a.getNodeBalance()
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get asgard node balance for chain %s: %w", a.chain, err),
			Finish: true,
		}
	}

	if nodeBalance.IsZero() {
		return OpResult{
			Error:  fmt.Errorf("asgard node %s has no %s balance to steal", a.nodeL1Address, a.asset),
			Finish: true,
		}
	}

	a.stolenAmount = nodeBalance.QuoUint64(10)
	if a.chain.Equals(common.BASEChain) {
		a.stolenAmount = nodeBalance.MulUint64(6).QuoUint64(10)
	}
	if a.stolenAmount.IsZero() {
		return OpResult{
			Error:  fmt.Errorf("calculated stolen amount is zero, node balance %s too small", nodeBalance),
			Finish: true,
		}
	}

	var txid string
	var stolen cosmos.Uint

	switch {
	case a.chain.IsEVM():
		txid, stolen, err = a.sendStealToUser(a.stolenAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute EVM send-steal transaction: %w", err),
				Finish: true,
			}
		}
		a.stolenAmount = stolen
		a.Log().Info().
			Str("txid", txid).
			Stringer("stolen_amount", a.stolenAmount).
			Str("balances", a.getBalancesStr()).
			Int64("chain_height", a.getHeight()).
			Msg("broadcasted EVM send-steal transaction")
		return OpResult{Continue: true}
	case a.chain.IsUTXO():
		toAddress := a.userAccount.Address(a.chain)
		txid, err = Send(a.Log(), a.nodeUser, toAddress, a.asset, "CONSOLIDATE", a.stolenAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute steal transaction: %w", err),
				Finish: true,
			}
		}
		a.Log().Info().
			Str("txid", txid).
			Stringer("stolen_amount", a.stolenAmount).
			Str("balances", a.getBalancesStr()).
			Int64("chain_height", a.getHeight()).
			Msg("broadcasted UTXO steal transaction")
		return OpResult{Continue: true}
	}

	a.Log().Info().Stringer("chain", a.chain).Msg("no steal method for this chain")
	return OpResult{
		Error:  fmt.Errorf("no steal method implemented for chain %s", a.chain),
		Finish: true,
	}
}

// sendStealToUser sends funds from the vault to a user without a memo to force an insolvency gap.
func (a *InsolvencyActor) sendStealToUser(target cosmos.Uint) (string, cosmos.Uint, error) {
	toAddress := a.userAccount.Address(a.chain)

	startBalance, err := a.getNodeBalance()
	if err != nil {
		return "", cosmos.ZeroUint(), fmt.Errorf("failed to get starting balance: %w", err)
	}

	txid, err := Send(a.Log(), a.nodeUser, toAddress, a.asset, "", target)
	if err != nil {
		return "", cosmos.ZeroUint(), fmt.Errorf("failed to execute send-steal transaction: %w", err)
	}

	endBalance, err := a.getNodeBalance()
	if err != nil {
		return txid, cosmos.ZeroUint(), fmt.Errorf("failed to refresh balance after send-steal: %w", err)
	}

	stolen := cosmos.ZeroUint()
	if endBalance.LT(startBalance) {
		stolen = startBalance.Sub(endBalance)
	}
	if stolen.IsZero() {
		return txid, cosmos.ZeroUint(), fmt.Errorf("no balance delta detected after send-steal tx %s", txid)
	}

	return txid, stolen, nil
}

func (a *InsolvencyActor) restoreFunds(config *OpConfig) OpResult {
	a.Log().Info().Msg("attempting to restore funds to Asgard vault to resolve insolvency")

	restoreAmount := a.stolenAmount
	if a.chain.IsEVM() {
		account, err := a.userClient.GetAccount(nil)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to get user account balance for chain %s: %w", a.chain, err),
				Finish: true,
			}
		}
		userBal := account.Coins.GetCoin(a.asset).Amount
		if userBal.IsZero() {
			return OpResult{
				Error:  fmt.Errorf("user account has no %s balance to restore", a.asset),
				Finish: true,
			}
		}
		if userBal.LT(restoreAmount) {
			restoreAmount = userBal
		}
	}

	txid, err := DepositL1(a.Log(), a.userClient, a.asset, "NOOP:novault", restoreAmount)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to execute restore transaction: %w", err),
			Finish: true,
		}
	}

	a.Log().Info().
		Str("txid", txid).
		Stringer("stolen_amount", a.stolenAmount).
		Stringer("restored_amount", restoreAmount).
		Int64("chain_height", a.getHeight()).
		Str("balances", a.getBalancesStr()).
		Msg("broadcasted restore transaction")

	return OpResult{Continue: true}
}

func (a *InsolvencyActor) verifyInsolvencyHalt(config *OpConfig) OpResult {
	a.bumpBaseBlock()

	key := fmt.Sprintf("SolvencyHalt%sChain", a.asset.Chain)
	mimir, err := thornode.GetMimir(key)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get mimir: %w", err),
			Finish: true,
		}
	}
	insolvent := mimir > 0
	if insolvent {
		a.Log().Info().
			Bool("insolvent", insolvent).
			Str("balances", a.getBalancesStr()).
			Int64(key, mimir).
			Int64("chain_height", a.getHeight()).
			Msg("insolvency detected as expected")
	}

	return OpResult{Continue: insolvent}
}

func (a *InsolvencyActor) verifyInsolvencyHaltDuration(config *OpConfig) OpResult {
	if a.chain.IsEVM() {
		a.bumpBaseBlock()
		return OpResult{Continue: true}
	}

	key := fmt.Sprintf("SolvencyHalt%sChain", a.asset.Chain)
	mimir, err := thornode.GetMimir(key)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get mimir: %w", err),
			Finish: true,
		}
	}
	if mimir <= 1 {
		return OpResult{
			Error:  fmt.Errorf("mimir %q should be > 1 but is %d", key, mimir),
			Finish: true,
		}
	}
	height := a.getHeight()
	if height < 0 {
		return OpResult{
			Error:  fmt.Errorf("failed to fetch chain height while checking insolvency duration"),
			Finish: true,
		}
	}
	finished := mimir+a.haltDuration <= height

	a.Log().Info().
		Bool("finished", finished).
		Int64("mimir", mimir).
		Int64("waitBlocks", a.haltDuration).
		Str("balances", a.getBalancesStr()).
		Int64("height", height).
		Int64("chain_height", a.getHeight()).
		Msg("insolvency duration check finished")

	return OpResult{Continue: finished}
}

func (a *InsolvencyActor) verifySolvencyUnhalt(config *OpConfig) OpResult {
	a.bumpBaseBlock()

	key := fmt.Sprintf("SolvencyHalt%sChain", a.asset.Chain)
	mimir, err := thornode.GetMimir(key)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get mimir: %w", err),
			Finish: true,
		}
	}
	solvent := mimir <= 0
	if solvent {
		a.Log().Info().
			Bool("solvent", solvent).
			Str("balances", a.getBalancesStr()).
			Int64(key, mimir).
			Int64("chain_height", a.getHeight()).
			Msg("solvency restored")
	}

	return OpResult{Continue: solvent}
}

func (a *InsolvencyActor) cleanup(config *OpConfig) OpResult {
	if a.userAccount != nil {
		a.userAccount.Release()
		a.userAccount = nil
		a.Log().Info().Msg("cleanup complete, user account released")
	}
	if a.nodeUser != nil {
		a.nodeUser.Release()
		a.nodeUser = nil
		a.Log().Info().Msg("cleanup complete, node account released")
	}
	return OpResult{Finish: true}
}

// helper functions
func (a *InsolvencyActor) getNodeBalance() (cosmos.Uint, error) {
	nodeAcct, err := a.nodeClient.GetAccount(nil)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to get node account for chain %s: %w", a.chain, err)
	}
	nodeBalance := nodeAcct.Coins.GetCoin(a.asset).Amount
	if nodeBalance.IsZero() {
		return cosmos.ZeroUint(), fmt.Errorf("asgard vault %s has no %s balance to steal", a.nodeL1Address, a.asset)
	}
	return nodeBalance, nil
}

func (a *InsolvencyActor) getHeight() int64 {
	client, ok := a.nodeClient.(interface{ GetHeight() (int64, error) })
	if !ok {
		return -1
	}
	height, err := client.GetHeight()
	if err != nil {
		a.Log().Error().Err(err).Msg("client.GetHeight error")
		return -1
	}
	return height
}

func (a *InsolvencyActor) getBalancesStr() string {
	var asgardStr, accountStr string
	asgards, err := thornode.GetVaults()
	if err != nil || len(asgards) == 0 {
		asgardStr = fmt.Sprintf("failed to get vaults: %v", err)
	} else {
		for _, coin := range asgards[0].Coins {
			asset, err := common.NewAsset(coin.Asset)
			if err != nil {
				continue
			}
			if a.asset.Equals(asset) {
				asgardStr = fmt.Sprintf("%s %s", coin.Amount, a.asset)
			}
		}
	}
	acctBal, err := a.getNodeBalance()
	if err != nil {
		accountStr = err.Error()
	} else {
		accountStr = acctBal.String()
	}
	return fmt.Sprintf("asgard_bal = %s, account_bal = %s", asgardStr, accountStr)
}
