package core

import (
	"fmt"
	"math/big"
	rand "math/rand/v2"
	"regexp"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"

	ecommon "github.com/ethereum/go-ethereum/common"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// SwapActor
////////////////////////////////////////////////////////////////////////////////////////

type SwapActor struct {
	Actor

	user *User

	// starting balances
	from        common.Asset
	fromAddress common.Address
	fromBalance sdkmath.Uint
	to          common.Asset
	toAddress   common.Address
	toBalance   sdkmath.Uint

	swapAmount sdkmath.Uint
	swapTxID   string

	// expected range for received amount, including outbound fee
	minExpected sdkmath.Uint
	maxExpected sdkmath.Uint
}

var recommendedMinAmountInPattern = regexp.MustCompile(`recommended_min_amount_in:\s*(\d+)`)

func NewSwapActor(from, to common.Asset, rng *rand.Rand) *Actor {
	a := &SwapActor{
		Actor: *NewActor(fmt.Sprintf("Swap %s => %s", from, to), rng),
		from:  from,
		to:    to,
	}
	a.Timeout = swapActorTimeout(from, to)

	// lock an account with from balance
	a.Ops = append(a.Ops, a.acquireUser)

	// generate swap quote
	a.Ops = append(a.Ops, a.getQuote)

	// send swap inbound
	if from.Chain.IsEVM() && !from.IsGasAsset() {
		a.Ops = append(a.Ops, a.sendTokenSwap)
	} else {
		a.Ops = append(a.Ops, a.sendSwap)
	}

	// verify the swap within expected range
	a.Ops = append(a.Ops, a.verifyOutbound)

	return &a.Actor
}

func swapActorTimeout(from, to common.Asset) time.Duration {
	if from.Chain.Equals(common.XMRChain) || to.Chain.Equals(common.XMRChain) {
		return 4 * time.Minute
	}
	return 2 * time.Minute
}

func getRecommendedMinAmountIn(quote openapi.QuoteSwapResponse) (sdkmath.Uint, bool) {
	if quote.RecommendedMinAmountIn == nil || *quote.RecommendedMinAmountIn == "" {
		return sdkmath.ZeroUint(), false
	}
	return sdkmath.NewUintFromString(*quote.RecommendedMinAmountIn), true
}

func getRecommendedMinAmountInFromError(err error) (sdkmath.Uint, bool) {
	matches := recommendedMinAmountInPattern.FindStringSubmatch(err.Error())
	if len(matches) != 2 {
		return sdkmath.ZeroUint(), false
	}
	return sdkmath.NewUintFromString(matches[1]), true
}

func getSwapQuoteWithRecommendedMin(from, to common.Asset, amount sdkmath.Uint, destination common.Address, extended bool) (openapi.QuoteSwapResponse, sdkmath.Uint, error) {
	currentAmount := amount
	for attempt := 0; attempt < 4; attempt++ {
		quote, err := thornode.GetSwapQuote(from, to, currentAmount, destination, extended)
		if err != nil {
			minAmount, ok := getRecommendedMinAmountInFromError(err)
			if ok && currentAmount.LT(minAmount) {
				currentAmount = minAmount
				continue
			}
			return openapi.QuoteSwapResponse{}, currentAmount, err
		}

		minAmount, ok := getRecommendedMinAmountIn(quote)
		if ok && currentAmount.LT(minAmount) {
			currentAmount = minAmount
			continue
		}

		return quote, currentAmount, nil
	}

	return openapi.QuoteSwapResponse{}, currentAmount, fmt.Errorf("failed to settle swap quote amount within recommended minimum")
}

func swapSpendableBalance(asset common.Asset, balance sdkmath.Uint) sdkmath.Uint {
	if balance.IsZero() {
		return sdkmath.ZeroUint()
	}

	if !asset.IsGasAsset() {
		return balance
	}

	// Preserve enough of the gas asset balance to avoid draining the account below
	// chain dust/reserve requirements while still allowing late-stage swaps to use
	// whatever balance remains.
	reserve := asset.Chain.DustThreshold()
	tenPercent := balance.QuoUint64(10)
	if tenPercent.GT(reserve) {
		reserve = tenPercent
	}
	if !balance.GT(reserve) {
		return sdkmath.ZeroUint()
	}

	return balance.Sub(reserve)
}

func swapAmountForBalance(asset common.Asset, target, balance sdkmath.Uint) sdkmath.Uint {
	spendable := swapSpendableBalance(asset, balance)
	if spendable.IsZero() {
		return sdkmath.ZeroUint()
	}
	if spendable.LT(target) {
		return spendable
	}
	return target
}

func setQuoteExpectations(a *SwapActor, quote openapi.QuoteSwapResponse) {
	// store expected range to fail if received amount is outside 10% tolerance
	quoteOut := sdkmath.NewUintFromString(quote.ExpectedAmountOut)
	tolerance := quoteOut.QuoUint64(10)
	if quote.Fees.Outbound != nil {
		outboundFee := sdkmath.NewUintFromString(*quote.Fees.Outbound)
		quoteOut = quoteOut.Add(outboundFee)

		// handle 3x gas rate fluctuation (add 2x outbound fee to tolerance)
		tolerance = tolerance.Add(outboundFee.MulUint64(2))
	}
	a.minExpected = common.SafeSub(quoteOut, tolerance)
	a.maxExpected = quoteOut.Add(tolerance)
}

func (a *SwapActor) updateSwapAmount(next sdkmath.Uint, reason string) error {
	if next.GT(a.fromBalance) {
		if a.user != nil {
			a.user.Release()
		}
		return fmt.Errorf("%s %s exceeds user balance %s", reason, next, a.fromBalance)
	}

	if !next.Equal(a.swapAmount) {
		a.Log().Info().
			Stringer("before", a.swapAmount).
			Stringer("after", next).
			Str("reason", reason).
			Msg("adjusted swap amount")
	}

	a.swapAmount = next
	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *SwapActor) acquireUser(config *OpConfig) OpResult {
	// swap 0.25% of from pool depth
	pool, err := thornode.GetPool(a.from)
	if err != nil {
		return OpResult{
			Continue: false,
		}
	}
	targetAmount := sdkmath.NewUintFromString(pool.BalanceAsset).QuoUint64(400)
	a.swapAmount = targetAmount

	var selectedUser *User
	selectedFromBalance := sdkmath.ZeroUint()
	selectedSwapAmount := sdkmath.ZeroUint()

	for _, user := range config.Users {
		// skip users already being used
		if !user.Acquire() {
			continue
		}

		cl := a.Log().With().
			Str("user", user.Name()).
			Stringer("from", a.from).
			Stringer("to", a.to).
			Logger()
		a.SetLogger(cl)

		// skip users that don't have from asset balance
		var fromAcct *common.Account
		fromAcct, err = user.ChainClients[a.from.Chain].GetAccount(nil)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get from account")
			user.Release()
			continue
		}
		fromBalance := fromAcct.Coins.GetCoin(a.from).Amount
		candidateAmount := swapAmountForBalance(a.from, targetAmount, fromBalance)
		if candidateAmount.IsZero() {
			a.Log().Error().Msg("user has insufficient from balance")
			user.Release()
			continue
		}

		// Prefer the user with the most spendable balance so late-stage swaps can
		// keep progressing even after earlier actors have partially drained assets.
		if selectedUser != nil && !candidateAmount.GT(selectedSwapAmount) {
			user.Release()
			continue
		}

		if selectedUser != nil {
			selectedUser.Release()
		}
		selectedUser = user
		selectedFromBalance = fromBalance
		selectedSwapAmount = candidateAmount
	}

	if selectedUser != nil {
		a.user = selectedUser
		a.fromBalance = selectedFromBalance

		cl := a.Log().With().
			Str("user", selectedUser.Name()).
			Stringer("from", a.from).
			Stringer("to", a.to).
			Logger()
		a.SetLogger(cl)

		// get l1 address to store in state context
		a.fromAddress, err = selectedUser.PubKey(a.from.Chain).GetAddress(a.from.Chain)
		if err != nil {
			a.Log().Fatal().Err(err).Msg("failed to get L1 address")
		}
		a.toAddress, err = selectedUser.PubKey(a.to.Chain).GetAddress(a.to.Chain)
		if err != nil {
			a.Log().Fatal().Err(err).Msg("failed to get L1 address")
		}

		// get to asset balance for tracking
		var toAcct *common.Account
		toAcct, err = selectedUser.ChainClients[a.to.Chain].GetAccount(nil)
		if err != nil {
			a.Log().Warn().Err(err).Msg("failed to get to account")
		} else {
			a.toBalance = toAcct.Coins.GetCoin(a.to).Amount
		}

		if err := a.updateSwapAmount(selectedSwapAmount, "available user balance"); err != nil {
			return OpResult{
				Error:  err,
				Finish: true,
			}
		}
	}

	// continue if we acquired a user
	if a.user != nil {
		a.Log().Info().Msg("acquired user")
		return OpResult{
			Continue: true,
		}
	}

	// remain pending if no user is available
	lockedUsers := 0
	availableUsers := 0
	for _, user := range config.Users {
		if !user.IsLocked() {
			availableUsers++
		} else {
			lockedUsers++
		}
	}
	a.Log().Info().
		Int("available_users", availableUsers).
		Int("locked_users", lockedUsers).
		Msg("waiting for user with sufficient balance")
	return OpResult{
		Continue: false,
	}
}

func (a *SwapActor) getQuote(config *OpConfig) OpResult {
	quote, quoteAmount, err := getSwapQuoteWithRecommendedMin(a.from, a.to, a.swapAmount, a.toAddress, a.from.Chain.IsUTXO())
	if err != nil {
		a.Log().Error().Err(err).Str("amount", a.swapAmount.String()).Msg("failed to get swap quote")
		return OpResult{
			Continue: false,
		}
	}

	if err := a.updateSwapAmount(quoteAmount, "recommended minimum inbound amount"); err != nil {
		return OpResult{
			Error:  err,
			Finish: true,
		}
	}

	setQuoteExpectations(a, quote)

	return OpResult{
		Continue: true,
	}
}

func (a *SwapActor) sendSwap(config *OpConfig) OpResult {
	// get inbound address
	inboundAddr, _, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{
			Continue: false,
		}
	}

	// if on a utxo chain, shorten the to asset to fuzzy match
	to := a.to.String()
	if a.from.Chain.IsUTXO() && !a.to.IsGasAsset() {
		to = strings.Split(to, "-")[0]
	}

	// create tx out
	memo := fmt.Sprintf("=:%s:%s", to, a.toAddress)
	tx := SimTx{
		Chain:     a.from.Chain,
		ToAddress: inboundAddr,
		Coin:      common.NewCoin(a.from, a.swapAmount),
		Memo:      memo,
	}

	client := a.user.ChainClients[a.from.Chain]

	// sign transaction
	signed, err := client.SignTx(tx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign tx")
		return OpResult{
			Continue: false,
		}
	}

	// broadcast transaction
	txid, err := client.BroadcastTx(signed)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}
	a.swapTxID = txid

	a.Log().Info().Str("txid", txid).Msg("broadcasted swap tx")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapActor) sendTokenSwap(config *OpConfig) OpResult {
	// get router address
	inboundAddr, routerAddr, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{
			Continue: false,
		}
	}
	if routerAddr == nil {
		a.Log().Error().Msg("failed to get router address")
		return OpResult{
			Continue: false,
		}
	}

	token := evm.Tokens(a.from.Chain)[a.from]

	// convert amount to token decimals
	factor := big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(token.Decimals)), nil)
	tokenAmount := a.swapAmount.Mul(cosmos.NewUintFromBigInt(factor))
	tokenAmount = tokenAmount.QuoUint64(common.One)

	// approve the router
	eRouterAddr := ecommon.HexToAddress(routerAddr.String())
	tx := SimContractTx{
		Chain:    a.from.Chain,
		Contract: common.Address(token.Address),
		ABI:      evm.ERC20ABI(),
		Method:   "approve",
		Args:     []interface{}{eRouterAddr, tokenAmount.BigInt()},
	}

	iClient := a.user.ChainClients[a.from.Chain]
	client, ok := iClient.(*evm.Client)
	if !ok {
		a.Log().Fatal().Msg("failed to get evm client")
	}

	// sign approve transaction
	signed, err := client.SignContractTx(tx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign tx")
		return OpResult{
			Continue: false,
		}
	}

	// broadcast approve transaction
	txid, err := client.BroadcastTx(signed)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}
	a.Log().Info().Str("txid", txid).Msg("broadcasted router approve tx")

	// call depositWithExpiry
	memo := fmt.Sprintf("=:%s:%s", a.to, a.toAddress)
	expiry := time.Now().Add(time.Hour).Unix()
	eInboundAddr := ecommon.HexToAddress(inboundAddr.String())
	eTokenAddr := ecommon.HexToAddress(token.Address)
	tx = SimContractTx{
		Chain:    a.from.Chain,
		Contract: *routerAddr,
		ABI:      evm.RouterABI(),
		Method:   "depositWithExpiry",
		Args: []interface{}{
			eInboundAddr,
			eTokenAddr,
			tokenAmount.BigInt(),
			memo,
			big.NewInt(expiry),
		},
	}

	// sign deposit transaction
	signed, err = client.SignContractTx(tx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign tx")
		return OpResult{
			Continue: false,
		}
	}

	// broadcast deposit transaction
	txid, err = client.BroadcastTx(signed)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}

	a.swapTxID = txid

	a.Log().Info().Str("txid", txid).Msg("broadcasted swap tx")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapActor) verifyOutbound(config *OpConfig) OpResult {
	// get swap stages
	stages, err := thornode.GetTxStages(a.swapTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx stages")
		return OpResult{
			Continue: false,
		}
	}

	// wait for outbound to be marked complete
	if stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
		return OpResult{
			Continue: false,
		}
	}

	// get tx details
	details, err := thornode.GetTxDetails(a.swapTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx details")
		return OpResult{
			Continue: false,
		}
	}

	// verify exactly one out transaction
	if len(details.OutTxs) != 1 {
		return OpResult{
			Error:  fmt.Errorf("expected exactly one out transaction"),
			Finish: true,
		}
	}

	// verify exactly one action
	if len(details.Actions) != 1 {
		return OpResult{
			Error:  fmt.Errorf("expected exactly one action"),
			Finish: true,
		}
	}

	// verify action is not refund
	// For memoless outbounds, Memo may be nil but OriginalMemo contains the actual memo
	actionMemo := ""
	if details.Actions[0].Memo != nil {
		actionMemo = *details.Actions[0].Memo
	} else if details.Actions[0].OriginalMemo != nil {
		actionMemo = *details.Actions[0].OriginalMemo
	}
	if strings.HasPrefix(actionMemo, "REFUND:") {
		return OpResult{
			Error:  fmt.Errorf("expected action to not be a refund"),
			Finish: true,
		}
	}

	// verify outbound amount + max gas within expected range
	action := details.Actions[0]
	out := details.OutTxs[0]
	outAmountPlusMaxGas := cosmos.NewUintFromString(out.Coins[0].Amount)
	maxGas := action.MaxGas[0]
	if maxGas.Asset == a.to.String() {
		outAmountPlusMaxGas = outAmountPlusMaxGas.Add(cosmos.NewUintFromString(maxGas.Amount))
	} else {
		var maxGasAssetValue sdkmath.Uint
		maxGasAssetValue, err = thornode.ConvertAssetAmount(maxGas, a.to.String())
		if err != nil {
			a.Log().Warn().Err(err).Msg("failed to convert asset")
			return OpResult{
				Continue: false,
			}
		}
		outAmountPlusMaxGas = outAmountPlusMaxGas.Add(maxGasAssetValue)
	}

	// retrieve L1 balance
	toAcct, err := a.user.ChainClients[a.to.Chain].GetAccount(nil)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get to account")
		return OpResult{
			Continue: false,
		}
	}

	// check received amount
	received := toAcct.Coins.GetCoin(a.to).Amount.Sub(a.toBalance)
	a.Log().Info().
		Stringer("received", received).
		Stringer("outAmountPlusMaxGas", outAmountPlusMaxGas).
		Stringer("minExpected", a.minExpected).
		Stringer("maxExpected", a.maxExpected).
		Msg("swap complete")

	// fail if received amount is outside expected range
	if outAmountPlusMaxGas.LT(a.minExpected) || outAmountPlusMaxGas.GT(a.maxExpected) {
		err = fmt.Errorf("out amount plus gas outside tolerance")
	}

	// release user
	a.user.Release()

	return OpResult{
		Finish: true,
		Error:  err,
	}
}
