package core

import (
	crypto_rand "crypto/rand"
	"fmt"
	rand "math/rand/v2"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	bip39 "github.com/tyler-smith/go-bip39"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	simcommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// AltNativeFeeActor
////////////////////////////////////////////////////////////////////////////////////////

type AltNativeFeeMode string

const (
	AltNativeFeeModeNative  AltNativeFeeMode = "native"
	AltNativeFeeModeTrade   AltNativeFeeMode = "trade"
	AltNativeFeeModeSecured AltNativeFeeMode = "secured"
)

// AltNativeFeeActor exercises the happy-path alt-native-fee flow for THOR-native
// transactions that are funded by non-RUNE assets.
//
// The actor borrows a funded bootstrap account, creates one or more fresh local
// THOR wallets with zero RUNE, then bootstraps the wallet under test with one
// of three source asset types:
//   - native mode: a THOR-native asset is sent directly from a seeded THOR account
//   - trade mode: an L1 deposit credits trade+:<thor address>
//   - secured mode: an L1 deposit credits secure+:<thor address>
//
// Once the wallet under test holds the source asset but still has no RUNE, the
// actor proves that the tx fee is being paid in that source asset by executing:
//   - native or secured mode: a native MsgSend of the source asset to another
//     ad-hoc wallet
//   - native, trade, or secured mode: a native MsgDeposit swap from the source
//     asset to RUNE, with the RUNE output sent to a separate ad-hoc recipient wallet
//
// The assertions are balance-based:
//   - the source wallet never receives RUNE
//   - the recipient receives the business output
//   - the source asset decreases by more than the sent/swapped amount, showing the
//     asset itself also covered the transaction fee
type AltNativeFeeActor struct {
	Actor

	mode  AltNativeFeeMode
	asset common.Asset // Native asset under test, or L1 asset backing trade/secured mode

	bootstrapUser     *User
	bootstrapAmount   cosmos.Uint
	bootstrapUserHeld bool

	testUser             *User
	testAccount          cosmos.AccAddress
	testAddress          common.Address
	sendRecipient        *User
	sendRecipientAccount cosmos.AccAddress
	sendRecipientAddress common.Address
	swapRecipient        *User
	swapRecipientAccount cosmos.AccAddress
	swapRecipientAddress common.Address

	sendAmount                 cosmos.Uint
	swapAmount                 cosmos.Uint
	sourceBalanceBeforeSend    cosmos.Uint
	recipientBalanceBeforeSend cosmos.Uint
	sourceBalanceBeforeSwap    cosmos.Uint
	recipientRuneBeforeSwap    cosmos.Uint

	swapTxID common.TxID
}

// NewAltNativeFeeActor builds the op sequence for one asset/mode combination.
// Native and secured modes include a send flow; all modes include a swap flow.
func NewAltNativeFeeActor(asset common.Asset, mode AltNativeFeeMode, rng *rand.Rand) *Actor {
	a := &AltNativeFeeActor{
		Actor: *NewActor(fmt.Sprintf("AltNativeFee-%s-%s", mode, asset), rng),
		mode:  mode,
		asset: asset,
	}
	a.Timeout = 15 * time.Minute

	a.Ops = append(a.Ops,
		a.acquireBootstrapUser,
		a.createLocalUsers,
		a.bootstrapAsset,
		a.waitForBootstrap,
	)

	if mode == AltNativeFeeModeNative || mode == AltNativeFeeModeSecured {
		a.Ops = append(a.Ops,
			a.prepareSend,
			a.sendSourceAsset,
			a.verifySend,
		)
	}

	a.Ops = append(a.Ops,
		a.prepareSwap,
		a.submitSwap,
		a.verifySwap,
		a.finish,
	)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// sourceAsset resolves the THOR-side asset that the test wallet actually spends.
func (a *AltNativeFeeActor) sourceAsset() common.Asset {
	switch a.mode {
	case AltNativeFeeModeNative:
		return a.asset
	case AltNativeFeeModeTrade:
		return a.asset.GetTradeAsset()
	case AltNativeFeeModeSecured:
		return a.asset.GetSecuredAsset()
	default:
		panic(fmt.Sprintf("unsupported alt-native fee mode %s", a.mode))
	}
}

// newLocalUser creates a throwaway local THOR wallet for this actor only.
//
// The returned values intentionally include both THOR address forms used by the
// simulation:
//   - cosmos.AccAddress for signing native THORChain messages
//   - common.Address for memos, queries, and generic THORChain address handling
//
// These wallets are not part of the seeded simulation user pool and start with
// no balances, which is required to exercise the alt-native-fee path instead of
// the normal RUNE-fee path.
func (a *AltNativeFeeActor) newLocalUser() (*User, cosmos.AccAddress, common.Address, error) {
	entropy := make([]byte, 32)
	if _, err := crypto_rand.Read(entropy); err != nil {
		return nil, nil, common.NoAddress, err
	}

	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return nil, nil, common.NoAddress, err
	}

	user := NewUser(mnemonic, map[common.Chain]LiteChainClientConstructor{})
	account, err := user.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		return nil, nil, common.NoAddress, err
	}

	address, err := user.PubKey(common.THORChain).GetAddress(common.THORChain)
	if err != nil {
		return nil, nil, common.NoAddress, err
	}

	return user, account, address, nil
}

// balance returns the wallet balance relevant to the current mode.
// Native mode reads bank balances directly, while trade and secured modes read
// their effective spendable value from thornode queries.
func (a *AltNativeFeeActor) balance(addr common.Address) (cosmos.Uint, error) {
	switch a.mode {
	case AltNativeFeeModeNative:
		return a.coinBalance(addr)
	case AltNativeFeeModeTrade:
		return thornode.GetEffectiveTradeBalance(addr, a.sourceAsset())
	case AltNativeFeeModeSecured:
		return thornode.GetEffectiveSecuredBalance(addr, a.sourceAsset())
	default:
		panic(fmt.Sprintf("unsupported alt-native fee mode %s", a.mode))
	}
}

// balances loads THOR bank balances and treats a missing auth account as empty.
func (a *AltNativeFeeActor) balances(addr common.Address) (common.Coins, error) {
	coins, err := thornode.GetBalances(addr)
	if err != nil && strings.Contains(err.Error(), "HTTP: 404") {
		return common.Coins{}, nil
	}
	return coins, err
}

// runeBalance returns the wallet's direct native RUNE bank balance.
func (a *AltNativeFeeActor) runeBalance(addr common.Address) (cosmos.Uint, error) {
	coins, err := a.balances(addr)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	return coins.GetCoin(common.RuneAsset()).Amount, nil
}

// coinBalance returns the wallet's direct bank balance for the source asset.
func (a *AltNativeFeeActor) coinBalance(addr common.Address) (cosmos.Uint, error) {
	coins, err := a.balances(addr)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	return coins.GetCoin(a.sourceAsset()).Amount, nil
}

// releaseBootstrapUser unlocks the borrowed funded user once the actor is done.
func (a *AltNativeFeeActor) releaseBootstrapUser() {
	if a.bootstrapUserHeld {
		a.bootstrapUser.Release()
		a.bootstrapUserHeld = false
		a.bootstrapUser = nil
	}
}

// fail centralizes cleanup for error exits from any op.
func (a *AltNativeFeeActor) fail(err error) OpResult {
	a.releaseBootstrapUser()
	return OpResult{
		Finish: true,
		Error:  err,
	}
}

// chooseBootstrapAmount keeps the borrowed funding amount small and bounded.
func (a *AltNativeFeeActor) chooseBootstrapAmount(balance cosmos.Uint) cosmos.Uint {
	if balance.IsZero() {
		return cosmos.ZeroUint()
	}

	amount := balance.QuoUint64(10)
	maxWanted := cosmos.NewUint(5 * common.One)
	if amount.GT(maxWanted) {
		amount = maxWanted
	}
	return amount
}

// chooseAccountBootstrapAmount picks the tiny secured deposit used only to
// materialize a THOR auth account ahead of trade-mode tests.
func (a *AltNativeFeeActor) chooseAccountBootstrapAmount() cosmos.Uint {
	amount := a.bootstrapAmount.QuoUint64(100)
	if !a.bootstrapAmount.IsZero() && amount.IsZero() {
		amount = cosmos.NewUint(1)
	}
	return amount
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

// acquireBootstrapUser borrows a funded user that can source the asset under
// test, either from native THOR balances or from an external L1 wallet.
func (a *AltNativeFeeActor) acquireBootstrapUser(config *OpConfig) OpResult {
	if a.mode == AltNativeFeeModeNative {
		for _, user := range config.NodeUsers {
			if !user.Acquire() {
				continue
			}

			coins, err := a.balances(user.Address(common.THORChain))
			if err != nil {
				user.Release()
				continue
			}

			amount := a.chooseBootstrapAmount(coins.GetCoin(a.sourceAsset()).Amount)
			if amount.IsZero() {
				user.Release()
				continue
			}

			a.bootstrapUser = user
			a.bootstrapAmount = amount
			a.bootstrapUserHeld = true
			a.SetLogger(a.Log().With().
				Str("mode", string(a.mode)).
				Stringer("asset", a.asset).
				Str("bootstrap_user", user.Name()).
				Logger())
			a.Log().Info().Stringer("bootstrap_amount", amount).Msg("acquired bootstrap user")

			return OpResult{Continue: true}
		}

		return OpResult{Continue: false}
	}

	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}

		l1Acct, err := user.ChainClients[a.asset.Chain].GetAccount(nil)
		if err != nil {
			user.Release()
			continue
		}

		amount := a.chooseBootstrapAmount(l1Acct.Coins.GetCoin(a.asset).Amount)
		if amount.IsZero() {
			user.Release()
			continue
		}

		a.bootstrapUser = user
		a.bootstrapAmount = amount
		a.bootstrapUserHeld = true
		a.SetLogger(a.Log().With().
			Str("mode", string(a.mode)).
			Stringer("asset", a.asset).
			Str("bootstrap_user", user.Name()).
			Logger())
		a.Log().Info().Stringer("bootstrap_amount", amount).Msg("acquired bootstrap user")

		return OpResult{Continue: true}
	}

	return OpResult{Continue: false}
}

// createLocalUsers creates the ad-hoc wallets used by this actor only.
func (a *AltNativeFeeActor) createLocalUsers(config *OpConfig) OpResult {
	var err error
	a.testUser, a.testAccount, a.testAddress, err = a.newLocalUser()
	if err != nil {
		return a.fail(fmt.Errorf("failed to create test wallet: %w", err))
	}

	if a.mode == AltNativeFeeModeNative || a.mode == AltNativeFeeModeSecured {
		// Secured send needs a separate recipient so the source and destination
		// balances can be checked independently. Native mode uses the same pattern.
		a.sendRecipient, a.sendRecipientAccount, a.sendRecipientAddress, err = a.newLocalUser()
		if err != nil {
			return a.fail(fmt.Errorf("failed to create send recipient wallet: %w", err))
		}
	}

	// Swap output is intentionally sent to a different wallet so the test can
	// prove where the final RUNE landed.
	a.swapRecipient, a.swapRecipientAccount, a.swapRecipientAddress, err = a.newLocalUser()
	if err != nil {
		return a.fail(fmt.Errorf("failed to create swap recipient wallet: %w", err))
	}

	a.Log().Info().
		Stringer("test_wallet", a.testAddress).
		Stringer("swap_recipient", a.swapRecipientAddress).
		Msg("created local wallets")

	return OpResult{Continue: true}
}

// bootstrapAsset funds the zero-RUNE test wallet with the source asset for the
// current mode, either natively on THORChain or via L1 deposit memos.
func (a *AltNativeFeeActor) bootstrapAsset(config *OpConfig) OpResult {
	if a.mode == AltNativeFeeModeNative {
		from, err := a.bootstrapUser.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			return a.fail(fmt.Errorf("failed to get native bootstrap sender address: %w", err))
		}

		nativeCoins, err := common.NewCoins(common.NewCoin(a.sourceAsset(), a.bootstrapAmount)).Native()
		if err != nil {
			return a.fail(fmt.Errorf("failed to convert native bootstrap coin: %w", err))
		}

		// Native mode is bootstrapped entirely on THORChain: a seeded THOR account
		// transfers NAMI directly to the ad-hoc zero-RUNE wallet under test.
		msg := &ttypes.MsgSend{
			FromAddress: from,
			ToAddress:   a.testAccount,
			Amount:      sdk.Coins(nativeCoins),
		}

		txid, err := a.bootstrapUser.Thorchain.BroadcastWithBlocking(msg)
		if err != nil {
			return a.fail(fmt.Errorf("failed to bootstrap native asset: %w", err))
		}

		a.Log().Info().
			Str("txid", txid.String()).
			Stringer("amount", a.bootstrapAmount).
			Msg("submitted native bootstrap send")

		return OpResult{Continue: true}
	}

	if a.mode == AltNativeFeeModeTrade {
		// Trade balances alone do not materialize an auth account, so trade mode
		// first performs a tiny secured bootstrap to create the THOR account while
		// still keeping the wallet effectively RUNE-free.
		authAmount := a.chooseAccountBootstrapAmount()
		memo := fmt.Sprintf("secure+:%s", a.testAccount)
		client := a.bootstrapUser.ChainClients[a.asset.Chain]

		var (
			txid string
			err  error
		)
		if a.asset.Chain.IsEVM() && !a.asset.IsGasAsset() {
			txid, err = simcommon.DepositL1Token(a.Log(), client, a.asset, memo, authAmount)
		} else {
			txid, err = simcommon.DepositL1(a.Log(), client, a.asset, memo, authAmount)
		}
		if err != nil {
			return a.fail(fmt.Errorf("failed to bootstrap trade auth account: %w", err))
		}

		a.Log().Info().
			Str("memo", memo).
			Str("txid", txid).
			Stringer("amount", authAmount).
			Msg("submitted auth-account bootstrap deposit")
	}

	memo := fmt.Sprintf("trade+:%s", a.testAccount)
	if a.mode == AltNativeFeeModeSecured {
		memo = fmt.Sprintf("secure+:%s", a.testAccount)
	}

	// This is the real bootstrap deposit. It spends the borrowed bootstrap
	// user's L1 asset on the external chain and has THORChain credit the
	// ad-hoc test wallet with the trade or secured representation of that asset.
	client := a.bootstrapUser.ChainClients[a.asset.Chain]
	var (
		txid string
		err  error
	)
	if a.asset.Chain.IsEVM() && !a.asset.IsGasAsset() {
		txid, err = simcommon.DepositL1Token(a.Log(), client, a.asset, memo, a.bootstrapAmount)
	} else {
		txid, err = simcommon.DepositL1(a.Log(), client, a.asset, memo, a.bootstrapAmount)
	}
	if err != nil {
		return a.fail(fmt.Errorf("failed to bootstrap %s asset: %w", a.mode, err))
	}

	a.Log().Info().
		Str("memo", memo).
		Str("txid", txid).
		Stringer("amount", a.bootstrapAmount).
		Msg("submitted bootstrap deposit")

	return OpResult{Continue: true}
}

// waitForBootstrap polls until the test wallet shows a usable source balance
// while still remaining RUNE-free.
func (a *AltNativeFeeActor) waitForBootstrap(config *OpConfig) OpResult {
	balance, err := a.balance(a.testAddress)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to query bootstrapped balance")
		return OpResult{Continue: false}
	}
	if balance.IsZero() {
		return OpResult{Continue: false}
	}

	runeBal, err := a.runeBalance(a.testAddress)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to query test wallet rune balance")
		return OpResult{Continue: false}
	}
	if !runeBal.IsZero() {
		return a.fail(fmt.Errorf("expected no rune in test wallet after bootstrap, got %s", runeBal))
	}

	// The wallet under test is now ready: it has the source asset to spend, but
	// no RUNE to pay fees with.
	a.Log().Info().
		Stringer("source_asset", a.sourceAsset()).
		Stringer("balance", balance).
		Msg("bootstrap completed")

	return OpResult{Continue: true}
}

// prepareSend snapshots balances and chooses the amount for the send leg.
func (a *AltNativeFeeActor) prepareSend(config *OpConfig) OpResult {
	runeBal, err := a.runeBalance(a.testAddress)
	if err != nil {
		return OpResult{Continue: false}
	}
	if !runeBal.IsZero() {
		return a.fail(fmt.Errorf("%s send source wallet unexpectedly has rune balance %s", a.mode, runeBal))
	}

	sourceBalance, err := a.coinBalance(a.testAddress)
	if err != nil {
		return OpResult{Continue: false}
	}
	if sourceBalance.IsZero() {
		return OpResult{Continue: false}
	}

	recipientBalance, err := a.coinBalance(a.sendRecipientAddress)
	if err != nil {
		return OpResult{Continue: false}
	}

	a.sendAmount = sourceBalance.QuoUint64(3)
	if a.sendAmount.IsZero() {
		return a.fail(fmt.Errorf("%s send amount resolved to zero from balance %s", a.mode, sourceBalance))
	}

	a.sourceBalanceBeforeSend = sourceBalance
	a.recipientBalanceBeforeSend = recipientBalance

	return OpResult{Continue: true}
}

// sendSourceAsset broadcasts a native MsgSend from the zero-RUNE wallet under test.
func (a *AltNativeFeeActor) sendSourceAsset(config *OpConfig) OpResult {
	nativeCoins, err := common.NewCoins(common.NewCoin(a.sourceAsset(), a.sendAmount)).Native()
	if err != nil {
		return a.fail(fmt.Errorf("failed to convert %s send coin: %w", a.mode, err))
	}

	// Native MsgSend moves the source asset to another THOR wallet. The sender has
	// no RUNE, so success here depends on that same asset also covering the native tx fee.
	msg := &ttypes.MsgSend{
		FromAddress: a.testAccount,
		ToAddress:   a.sendRecipientAccount,
		Amount:      sdk.Coins(nativeCoins),
	}

	txid, err := a.testUser.Thorchain.BroadcastWithBlocking(msg)
	if err != nil {
		return a.fail(fmt.Errorf("failed to broadcast %s send: %w", a.mode, err))
	}

	a.Log().Info().Stringer("txid", txid).Stringer("amount", a.sendAmount).Msg("broadcasted source-asset send")

	return OpResult{Continue: true}
}

// verifySend proves the business transfer succeeded and the fee was also paid
// from the same source asset instead of RUNE.
func (a *AltNativeFeeActor) verifySend(config *OpConfig) OpResult {
	sourceAfter, err := a.coinBalance(a.testAddress)
	if err != nil {
		return OpResult{Continue: false}
	}
	recipientAfter, err := a.coinBalance(a.sendRecipientAddress)
	if err != nil {
		return OpResult{Continue: false}
	}

	expectedRecipient := a.recipientBalanceBeforeSend.Add(a.sendAmount)
	if recipientAfter.LT(expectedRecipient) {
		return OpResult{Continue: false}
	}
	if recipientAfter.GT(expectedRecipient) {
		return a.fail(fmt.Errorf("%s send recipient balance mismatch: expected %s got %s", a.mode, expectedRecipient, recipientAfter))
	}

	expectedMaxSource := a.sourceBalanceBeforeSend.Sub(a.sendAmount)
	if !sourceAfter.LT(expectedMaxSource) {
		return OpResult{Continue: false}
	}

	// The source must have spent more than the transferred amount. The difference
	// is the evidence that the tx fee was also charged in the same source asset.
	runeBal, err := a.runeBalance(a.testAddress)
	if err != nil {
		return OpResult{Continue: false}
	}
	if !runeBal.IsZero() {
		return a.fail(fmt.Errorf("%s send source wallet unexpectedly has rune balance %s", a.mode, runeBal))
	}

	a.Log().Info().
		Stringer("source_before", a.sourceBalanceBeforeSend).
		Stringer("source_after", sourceAfter).
		Stringer("recipient_after", recipientAfter).
		Msg("verified source-asset send alt-fee path")

	return OpResult{Continue: true}
}

// prepareSwap snapshots balances and chooses the amount for the swap leg.
func (a *AltNativeFeeActor) prepareSwap(config *OpConfig) OpResult {
	runeBal, err := a.runeBalance(a.testAddress)
	if err != nil {
		return OpResult{Continue: false}
	}
	if !runeBal.IsZero() {
		return a.fail(fmt.Errorf("swap source wallet unexpectedly has rune balance %s", runeBal))
	}

	sourceBalance, err := a.balance(a.testAddress)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to query source balance before swap")
		return OpResult{Continue: false}
	}
	if sourceBalance.IsZero() {
		return OpResult{Continue: false}
	}

	swapRecipientRune, err := a.runeBalance(a.swapRecipientAddress)
	if err != nil {
		return OpResult{Continue: false}
	}

	a.sourceBalanceBeforeSwap = sourceBalance
	a.recipientRuneBeforeSwap = swapRecipientRune
	a.swapAmount = sourceBalance.QuoUint64(3)
	if a.swapAmount.IsZero() {
		return a.fail(fmt.Errorf("swap amount resolved to zero from balance %s", sourceBalance))
	}

	return OpResult{Continue: true}
}

// submitSwap broadcasts the native deposit that swaps the source asset into
// RUNE for the dedicated swap recipient wallet.
func (a *AltNativeFeeActor) submitSwap(config *OpConfig) OpResult {
	// The swap is submitted from the source wallet, but the output RUNE is sent
	// to a separate ad-hoc wallet. This makes it unambiguous where the swapped
	// funds finally land.
	memo := fmt.Sprintf("=:r:%s", a.swapRecipientAddress)
	msg := ttypes.NewMsgDeposit(
		common.NewCoins(common.NewCoin(a.sourceAsset(), a.swapAmount)),
		memo,
		a.testAccount,
	)

	txid, err := a.testUser.Thorchain.BroadcastWithBlocking(msg)
	if err != nil {
		return a.fail(fmt.Errorf("failed to broadcast %s swap: %w", a.mode, err))
	}

	a.swapTxID = txid
	a.Log().Info().
		Stringer("txid", txid).
		Stringer("amount", a.swapAmount).
		Str("memo", memo).
		Msg("broadcasted alt-fee swap")

	return OpResult{Continue: true}
}

// verifySwap rejects refunded swaps and then checks that RUNE reached the
// recipient while the source wallet spent more than the swap input alone.
func (a *AltNativeFeeActor) verifySwap(config *OpConfig) OpResult {
	details, err := thornode.GetTxDetails(a.swapTxID.String())
	if err == nil {
		for _, action := range details.Actions {
			memo := ""
			if action.Memo != nil {
				memo = *action.Memo
			} else if action.OriginalMemo != nil {
				memo = *action.OriginalMemo
			}
			if strings.HasPrefix(memo, "REFUND:") {
				return a.fail(fmt.Errorf("%s swap refunded: %s", a.mode, memo))
			}
		}
	}

	sourceAfter, err := a.balance(a.testAddress)
	if err != nil {
		return OpResult{Continue: false}
	}

	recipientRuneAfter, err := a.runeBalance(a.swapRecipientAddress)
	if err != nil {
		return OpResult{Continue: false}
	}

	if !recipientRuneAfter.GT(a.recipientRuneBeforeSwap) {
		return OpResult{Continue: false}
	}

	expectedMaxSource := a.sourceBalanceBeforeSwap.Sub(a.swapAmount)
	if !sourceAfter.LT(expectedMaxSource) {
		return OpResult{Continue: false}
	}

	// As with secured send, the source must lose more than the swap input alone,
	// proving the fee was paid in the same source asset while the RUNE output
	// landed in the dedicated recipient wallet.
	runeBal, err := a.runeBalance(a.testAddress)
	if err != nil {
		return OpResult{Continue: false}
	}
	if !runeBal.IsZero() {
		return a.fail(fmt.Errorf("swap source wallet unexpectedly has rune balance %s", runeBal))
	}

	a.Log().Info().
		Stringer("source_before", a.sourceBalanceBeforeSwap).
		Stringer("source_after", sourceAfter).
		Stringer("recipient_rune_before", a.recipientRuneBeforeSwap).
		Stringer("recipient_rune_after", recipientRuneAfter).
		Msg("verified alt-fee swap path")

	return OpResult{Continue: true}
}

// finish releases the borrowed bootstrap user and ends the actor successfully.
func (a *AltNativeFeeActor) finish(config *OpConfig) OpResult {
	a.releaseBootstrapUser()
	return OpResult{
		Finish: true,
	}
}
