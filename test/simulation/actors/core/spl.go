package core

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	rand "math/rand/v2"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/solana"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// splTokenAccountRentTHOR is the rent-exempt minimum of an SPL token account
// (2,039,280 lamports) in THORChain's 1e8 SOL units.
const splTokenAccountRentTHOR = 203_928

// freshSolanaAddress returns a SOL wallet that has never held anything, derived from
// the actor's rng so a run is reproducible.
func freshSolanaAddress(rng *rand.Rand) common.Address {
	seed := make([]byte, ed25519.SeedSize)
	for i := 0; i < len(seed); i += 8 {
		binary.LittleEndian.PutUint64(seed[i:], rng.Uint64())
	}
	pub, _ := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return common.Address(stypes.PublicKeyFromBytes(pub).String())
}

// solTokenOutboundSurcharge returns the token outbound surcharge in effect on SOL: the
// TokenOutboundSurcharge-SOL mimir, or SOL's default.
func solTokenOutboundSurcharge() (sdkmath.Uint, error) {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		return sdkmath.ZeroUint(), err
	}
	key := fmt.Sprintf(constants.MimirTemplateTokenOutboundSurcharge, common.SOLChain.String())
	if v, ok := mimirs[strings.ToUpper(key)]; ok && v >= 0 {
		return sdkmath.NewUint(uint64(v)), nil
	}
	return common.SOLChain.TokenOutboundSurcharge(), nil
}

func poolBalances(asset common.Asset) (runeAmt, assetAmt sdkmath.Uint, err error) {
	pool, err := thornode.GetPool(asset)
	if err != nil {
		return sdkmath.ZeroUint(), sdkmath.ZeroUint(), err
	}
	runeAmt = sdkmath.NewUintFromString(pool.BalanceRune)
	assetAmt = sdkmath.NewUintFromString(pool.BalanceAsset)
	if runeAmt.IsZero() || assetAmt.IsZero() {
		return sdkmath.ZeroUint(), sdkmath.ZeroUint(), fmt.Errorf("pool %s is empty", asset)
	}
	return runeAmt, assetAmt, nil
}

// tokenFeeInSOL converts an outbound fee in token units to SOL through the pools.
func tokenFeeInSOL(token common.Asset, fee sdkmath.Uint) (sdkmath.Uint, error) {
	tokenRune, tokenAsset, err := poolBalances(token)
	if err != nil {
		return sdkmath.ZeroUint(), err
	}
	solRune, solAsset, err := poolBalances(common.SOLAsset)
	if err != nil {
		return sdkmath.ZeroUint(), err
	}
	feeRune := fee.Mul(tokenRune).Quo(tokenAsset)
	return feeRune.Mul(solAsset).Quo(solRune), nil
}

////////////////////////////////////////////////////////////////////////////////////////
// SPLFreshRecipientSwapActor
////////////////////////////////////////////////////////////////////////////////////////

// SPLFreshRecipientSwapActor swaps into an SPL token for a wallet that has no token
// account, then checks the outbound end to end:
//   - the wallet's associated token account holds exactly the observed outbound,
//     so the vault created it (CreateIdempotent) and paid into it;
//   - the observed outbound gas includes that account's rent, so THORChain booked
//     the vault's real SOL spend;
//   - the token's outbound fee covers SOL's token outbound surcharge, so users, not
//     the Reserve, pay that rent.
type SPLFreshRecipientSwapActor struct {
	SwapActor
	outboundVerified bool
}

func NewSPLFreshRecipientSwapActor(from, to common.Asset, rng *rand.Rand) *Actor {
	a := &SPLFreshRecipientSwapActor{
		SwapActor: SwapActor{
			Actor:           *NewActor(fmt.Sprintf("Swap %s => %s (fresh SPL recipient)", from, to), rng),
			from:            from,
			to:              to,
			requireOutbound: true,
		},
	}
	a.Timeout = swapActorTimeout(from, to)
	a.toAddressOverride = freshSolanaAddress(rng)

	a.Ops = append(a.Ops, a.acquireUser)
	a.Ops = append(a.Ops, a.getQuote)
	if from.Chain.IsEVM() && !from.IsGasAsset() {
		a.Ops = append(a.Ops, a.sendTokenSwap)
	} else {
		a.Ops = append(a.Ops, a.sendSwap)
	}
	a.Ops = append(a.Ops, a.verifyFreshRecipient)
	return &a.Actor
}

func (a *SPLFreshRecipientSwapActor) verifyFreshRecipient(config *OpConfig) OpResult {
	if !a.outboundVerified {
		result := a.verifyOutbound(config) // releases the user once it finishes
		if !result.Finish || result.Error != nil {
			return result
		}
		a.outboundVerified = true
	}

	details, err := thornode.GetTxDetails(a.swapTxID)
	if err != nil || len(details.OutTxs) != 1 {
		a.Log().Warn().Err(err).Msg("waiting for outbound details")
		return OpResult{Continue: false}
	}
	out := details.OutTxs[0]
	if len(out.Coins) != 1 || out.Coins[0].Asset != a.to.String() {
		return OpResult{Finish: true, Error: fmt.Errorf("unexpected outbound coins %v", out.Coins)}
	}
	if out.GetToAddress() != a.toAddressOverride.String() {
		return OpResult{Finish: true, Error: fmt.Errorf("outbound went to %s, want %s", out.GetToAddress(), a.toAddressOverride)}
	}
	outAmount := sdkmath.NewUintFromString(out.Coins[0].Amount)

	// The observed gas must include the recipient token account's rent.
	gas := sdkmath.ZeroUint()
	for _, coin := range out.Gas {
		if coin.Asset == common.SOLAsset.String() {
			gas = gas.Add(sdkmath.NewUintFromString(coin.Amount))
		}
	}
	if gas.LT(sdkmath.NewUint(splTokenAccountRentTHOR)) {
		return OpResult{Finish: true, Error: fmt.Errorf("outbound gas %s does not include the %d token account rent", gas, splTokenAccountRentTHOR)}
	}

	// The fresh wallet's token account holds exactly what THORChain observed.
	client, err := a.solanaReader(config)
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}
	acct, err := client.GetAccountByAddress(a.toAddressOverride)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to read fresh recipient balance")
		return OpResult{Continue: false}
	}
	received := acct.Coins.GetCoin(a.to).Amount
	if !received.Equal(outAmount) {
		a.Log().Info().Stringer("received", received).Stringer("outbound", outAmount).Msg("waiting for fresh recipient balance")
		return OpResult{Continue: false}
	}

	// The token's outbound fee covers the rent surcharge.
	surcharge, err := solTokenOutboundSurcharge()
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to read the SOL token outbound surcharge")
		return OpResult{Continue: false}
	}
	fee, err := thornode.GetOutboundFee(a.to)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to read outbound fee")
		return OpResult{Continue: false}
	}
	feeSOL, err := tokenFeeInSOL(a.to, fee)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to price outbound fee")
		return OpResult{Continue: false}
	}
	// Pool rounding and price moves between reads: allow 5%.
	if feeSOL.LT(surcharge.MulUint64(95).QuoUint64(100)) {
		return OpResult{Finish: true, Error: fmt.Errorf("%s outbound fee %s (%s SOL) does not cover the %s SOL surcharge", a.to, fee, feeSOL, surcharge)}
	}

	a.Log().Info().
		Stringer("recipient", a.toAddressOverride).
		Stringer("received", received).
		Stringer("gas", gas).
		Stringer("fee_in_sol", feeSOL).
		Stringer("surcharge", surcharge).
		Msg("fresh SPL recipient funded; rent booked as gas and covered by the fee")
	return OpResult{Finish: true}
}

// solanaReader returns a simulation Solana client for read-only balance queries,
// which need no user lock.
func (a *SPLFreshRecipientSwapActor) solanaReader(config *OpConfig) (*solana.Client, error) {
	return anySolanaClient(config)
}

////////////////////////////////////////////////////////////////////////////////////////
// SPLRefundActor
////////////////////////////////////////////////////////////////////////////////////////

// SPLRefundActor sends an SPL token deposit with an unparsable memo and checks
// THORChain refunds the token to the sender's wallet: a REFUND outbound of the same
// token to the sender, landing in its associated token account.
type SPLRefundActor struct {
	Actor

	asset        common.Asset
	user         *User
	address      common.Address
	amount       sdkmath.Uint
	balanceAfter sdkmath.Uint // sender balance right after the deposit
	txid         string
}

func NewSPLRefundActor(asset common.Asset, rng *rand.Rand) *Actor {
	a := &SPLRefundActor{
		Actor: *NewActor(fmt.Sprintf("SPL refund %s", asset), rng),
		asset: asset,
	}
	a.Timeout = 4 * time.Minute
	a.Ops = append(a.Ops, a.acquireUser, a.sendInvalidMemo, a.verifyRefund)
	return &a.Actor
}

func (a *SPLRefundActor) acquireUser(config *OpConfig) OpResult {
	fee, err := thornode.GetOutboundFee(a.asset)
	if err != nil {
		a.Log().Warn().Err(err).Msg("waiting for outbound fee")
		return OpResult{Continue: false}
	}
	_, depth, err := poolBalances(a.asset)
	if err != nil {
		a.Log().Warn().Err(err).Msg("waiting for pool")
		return OpResult{Continue: false}
	}
	// Large enough that the refund clears the outbound fee, as other refund actors size it.
	amount := depth.QuoUint64(100)
	if minimum := fee.MulUint64(3); amount.LT(minimum) {
		amount = minimum
	}

	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}
		acct, err := user.ChainClients[common.SOLChain].GetAccount(nil)
		if err != nil || acct.Coins.GetCoin(a.asset).Amount.LT(amount.MulUint64(2)) {
			user.Release()
			continue
		}
		addr, err := user.PubKey(common.SOLChain).GetAddress(common.SOLChain)
		if err != nil {
			user.Release()
			return OpResult{Finish: true, Error: err}
		}
		a.user, a.address, a.amount = user, addr, amount
		a.balanceAfter = acct.Coins.GetCoin(a.asset).Amount.Sub(amount)
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())
		return OpResult{Continue: true}
	}
	a.Log().Info().Msg("waiting for a user with token balance")
	return OpResult{Continue: false}
}

func (a *SPLRefundActor) sendInvalidMemo(config *OpConfig) OpResult {
	inbound, _, err := thornode.GetInboundAddress(common.SOLChain)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get inbound address")
		return OpResult{Continue: false}
	}
	tx := SimTx{
		Chain:     common.SOLChain,
		ToAddress: inbound,
		Coin:      common.NewCoin(a.asset, a.amount),
		// a swap whose destination is not a BTC address does not parse
		Memo: "=:BTC.BTC:not-a-btc-address",
	}
	txid, err := SignAndBroadcastTx(a.user.ChainClients[common.SOLChain], tx)
	if err != nil {
		result := SimTxErrorResult(err)
		if result.Finish {
			a.user.Release()
		}
		return result
	}
	a.txid = txid
	a.Log().Info().Str("txid", txid).Stringer("amount", a.amount).Msg("sent SPL deposit with an invalid memo")
	return OpResult{Continue: true}
}

func (a *SPLRefundActor) verifyRefund(config *OpConfig) OpResult {
	stages, err := thornode.GetTxStages(a.txid)
	if err != nil || stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
		return OpResult{Continue: false}
	}
	details, err := thornode.GetTxDetails(a.txid)
	if err != nil {
		return OpResult{Continue: false}
	}
	if len(details.Actions) != 1 || len(details.OutTxs) != 1 {
		return a.fail(fmt.Errorf("expected one refund action and outbound, got %d and %d", len(details.Actions), len(details.OutTxs)))
	}
	action, out := details.Actions[0], details.OutTxs[0]
	if memo := scheduledOutboundMemo(action); !strings.HasPrefix(memo, "REFUND:") {
		return a.fail(fmt.Errorf("expected a refund, got action memo %q", memo))
	}
	if len(out.Coins) != 1 || out.Coins[0].Asset != a.asset.String() || out.GetToAddress() != a.address.String() {
		return a.fail(fmt.Errorf("unexpected refund outbound %v to %s", out.Coins, out.GetToAddress()))
	}
	refund := sdkmath.NewUintFromString(out.Coins[0].Amount)
	if refund.IsZero() || refund.GTE(a.amount) {
		return a.fail(fmt.Errorf("refund %s should be positive and below the %s deposit (fee deducted)", refund, a.amount))
	}

	acct, err := a.user.ChainClients[common.SOLChain].GetAccount(nil)
	if err != nil {
		return OpResult{Continue: false}
	}
	balance := acct.Coins.GetCoin(a.asset).Amount
	if !balance.Equal(a.balanceAfter.Add(refund)) {
		a.Log().Info().Stringer("balance", balance).Stringer("expected", a.balanceAfter.Add(refund)).Msg("waiting for refund to land")
		return OpResult{Continue: false}
	}
	a.Log().Info().Stringer("deposit", a.amount).Stringer("refund", refund).Msg("SPL refund received")
	a.user.Release()
	return OpResult{Finish: true}
}

func (a *SPLRefundActor) fail(err error) OpResult {
	a.user.Release()
	return OpResult{Finish: true, Error: err}
}

// scheduledOutboundMemo returns a scheduled outbound's memo, or its original memo
// when the outbound is memoless.
func scheduledOutboundMemo(action openapi.TxOutItem) string {
	if action.Memo != nil {
		return *action.Memo
	}
	if action.OriginalMemo != nil {
		return *action.OriginalMemo
	}
	return ""
}

////////////////////////////////////////////////////////////////////////////////////////
// SPL churn migration checks
////////////////////////////////////////////////////////////////////////////////////////

// splChurnCheck carries the pre-churn snapshot from the capture actor to the verify
// actor of NewSPLChurnChecks.
type splChurnCheck struct {
	skipReason func() string
	skip       bool
	priorVault common.Address
	prior      map[string]sdkmath.Uint // asset -> on-chain balance of the prior vault
}

// NewSPLChurnChecks returns a capture actor, to run before a churn that rotates the
// Solana vault, and a verify actor, to run after it. The capture snapshots the
// active Solana vault's on-chain SPL balances; the verify waits for the vault to
// rotate and migration to finish, then requires the new vault to hold each token
// (within 1%) on chain. Both skip when Solana is not simulated or when skipReason
// (if set) reports the churn will not run.
func NewSPLChurnChecks(rng *rand.Rand, skipReason func() string) (before, after *Actor) {
	state := &splChurnCheck{skipReason: skipReason}
	before = NewActor("SPLPreChurnOut", rng)
	before.Timeout = 5 * time.Minute
	before.Ops = []Op{func(config *OpConfig) OpResult { return state.capture(before, config) }}

	after = NewActor("SPLPostChurnOut", rng)
	after.Timeout = 20 * time.Minute
	after.Ops = []Op{
		func(config *OpConfig) OpResult { return state.waitForRotation(after) },
		func(config *OpConfig) OpResult { return state.verify(after, config) },
	}
	return before, after
}

func splTokensSimulated() bool {
	for _, chain := range acommon.SimChains {
		if chain.Equals(common.SOLChain) {
			return len(solana.Tokens()) > 0
		}
	}
	return false
}

func (s *splChurnCheck) capture(a *Actor, config *OpConfig) OpResult {
	if !splTokensSimulated() {
		s.skip = true
		a.Log().Info().Msg("solana SPL tokens not simulated, skipping")
		return OpResult{Finish: true}
	}
	if s.skipReason != nil {
		if reason := s.skipReason(); reason != "" {
			s.skip = true
			a.Log().Info().Str("reason", reason).Msg("churn will not run, skipping SPL migration check")
			return OpResult{Finish: true}
		}
	}
	network, err := thornode.GetNetwork()
	if err != nil || network.VaultsMigrating {
		a.Log().Info().Err(err).Msg("waiting for vault migration to finish before snapshot")
		return OpResult{Continue: false}
	}
	vault, _, err := thornode.GetInboundAddress(common.SOLChain)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get SOL inbound address")
		return OpResult{Continue: false}
	}
	client, err := anySolanaClient(config)
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}
	acct, err := client.GetAccountByAddress(vault)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to read SOL vault balances")
		return OpResult{Continue: false}
	}
	s.prior = map[string]sdkmath.Uint{}
	for asset := range solana.Tokens() {
		if balance := acct.Coins.GetCoin(asset).Amount; !balance.IsZero() {
			s.prior[asset.String()] = balance
			a.Log().Info().Stringer("asset", asset).Stringer("balance", balance).Stringer("vault", vault).Msg("snapshotted pre-churn SPL balance")
		}
	}
	if len(s.prior) == 0 {
		return OpResult{Finish: true, Error: fmt.Errorf("no SPL tokens in the active SOL vault %s; migration check would pass vacuously", vault)}
	}
	s.priorVault = vault
	return OpResult{Finish: true}
}

func (s *splChurnCheck) waitForRotation(a *Actor) OpResult {
	if s.skip {
		return OpResult{Finish: true}
	}
	vault, _, err := thornode.GetInboundAddress(common.SOLChain)
	if err != nil || vault.Equals(s.priorVault) {
		a.Log().Info().Err(err).Msg("waiting for SOL vault to rotate")
		return OpResult{Continue: false}
	}
	network, err := thornode.GetNetwork()
	if err != nil || network.VaultsMigrating {
		a.Log().Info().Err(err).Msg("waiting for vault migration to complete")
		return OpResult{Continue: false}
	}
	return OpResult{Continue: true}
}

func (s *splChurnCheck) verify(a *Actor, config *OpConfig) OpResult {
	vault, _, err := thornode.GetInboundAddress(common.SOLChain)
	if err != nil {
		return OpResult{Continue: false}
	}
	client, err := anySolanaClient(config)
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}
	newAcct, err := client.GetAccountByAddress(vault)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to read new SOL vault balances")
		return OpResult{Continue: false}
	}
	for assetStr, prior := range s.prior {
		asset, err := common.NewAsset(assetStr)
		if err != nil {
			return OpResult{Finish: true, Error: err}
		}
		balance := newAcct.Coins.GetCoin(asset).Amount
		if threshold := prior.MulUint64(99).QuoUint64(100); balance.LT(threshold) {
			// Migration rounds may still be landing on chain.
			a.Log().Info().Stringer("asset", asset).Stringer("prior", prior).Stringer("new", balance).Stringer("vault", vault).
				Msg("waiting for SPL balance to reach the new vault")
			return OpResult{Continue: false}
		}
		a.Log().Info().Stringer("asset", asset).Stringer("prior", prior).Stringer("new", balance).Msg("SPL balance migrated to the new vault")
	}
	return OpResult{Finish: true}
}

// anySolanaClient returns a simulation Solana client for read-only queries.
func anySolanaClient(config *OpConfig) (*solana.Client, error) {
	for _, user := range config.Users {
		if client, ok := user.ChainClients[common.SOLChain].(*solana.Client); ok {
			return client, nil
		}
	}
	return nil, fmt.Errorf("no simulation solana client")
}
