package features

import (
	"fmt"
	rand "math/rand/v2"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// InactiveVaultRefunds
////////////////////////////////////////////////////////////////////////////////////////

func InactiveVaultRefunds(rng *rand.Rand) *Actor {
	a := NewActor("Feature-InactiveVaultRefunds", rng)

	for _, chain := range acommon.SimChains {
		if chain.Equals(common.SOLChain) { // skip SOL as inactive vaults default disabled
			continue
		}
		a.Children[NewInactiveVaultRefundActor(chain.GetGasAsset(), rng)] = true
	}

	return a
}

////////////////////////////////////////////////////////////////////////////////////////
// InactiveVaultRefundActor
////////////////////////////////////////////////////////////////////////////////////////

type InactiveVaultRefundActor struct {
	Actor

	account          *User
	asset            common.Asset
	amount           cosmos.Uint
	inboundTxid      string
	refundAddress    common.Address
	inboundAddress   common.Address
	inboundVault     string
	memo             string
	registeredMemo   string
	registrationTxID common.TxID

	getInactiveVaults func() ([]openapi.Vault, error)
}

func NewInactiveVaultRefundActor(asset common.Asset, rng *rand.Rand) *Actor {
	a := &InactiveVaultRefundActor{
		Actor:             *NewActor(fmt.Sprintf("InactiveVaultRefund-%s", asset), rng),
		asset:             asset,
		getInactiveVaults: thornode.GetInactiveVaults,
	}

	a.SetLogger(a.Log().With().Str("asset", asset.String()).Logger())

	// Require an eligible retired vault before acquiring funds or sending a deposit.
	a.Ops = append(a.Ops, a.selectInactiveVault)

	// lock a user that has sufficient L1 balance
	a.Ops = append(a.Ops, a.acquireUser)

	// XMR needs an explicit refund destination in a registered reference memo.
	a.Ops = append(a.Ops, a.registerRefundMemo, a.resolveRefundMemo)

	// send inbound to the selected inactive vault
	a.Ops = append(a.Ops, a.sendInactiveVaultInbound)

	// verify refund outbound
	a.Ops = append(a.Ops, a.verifyRefund)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *InactiveVaultRefundActor) acquireUser(config *OpConfig) OpResult {
	// determine the asset amount
	pool, err := thornode.GetPool(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pool")
		return OpResult{
			Continue: false,
		}
	}

	fee, err := thornode.GetOutboundFee(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get refund outbound fee")
		return OpResult{Continue: false}
	}
	a.amount = refundAmount(cosmos.NewUintFromString(pool.BalanceAsset), fee)

	for _, user := range config.Users {
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())

		// skip users already being used
		if !user.Acquire() {
			continue
		}

		// skip users that with insufficient L1 balance
		l1Acct, err := user.ChainClients[a.asset.Chain].GetAccount(nil)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get L1 account")
			user.Release()
			continue
		}
		if l1Acct.Coins.GetCoin(a.asset).Amount.LTE(a.amount) {
			a.Log().Error().Msg("user has insufficient L1 balance")
			user.Release()
			continue
		}

		// get l1 address to store in state context
		l1Address, err := user.PubKey(a.asset.Chain).GetAddress(a.asset.Chain)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get L1 address")
			user.Release()
			continue
		}

		// set acquired account and amounts in state context
		a.Log().Info().Stringer("l1Address", l1Address).Msg("acquired user")
		a.account = user
		a.refundAddress = l1Address

		break
	}

	// remain pending if no user is available
	return OpResult{
		Continue: a.account != nil,
	}
}

func (a *InactiveVaultRefundActor) selectInactiveVault(_ *OpConfig) OpResult {
	address, pubkey, err := a.findInactiveVaultAddress()
	if err != nil {
		return OpResult{Error: err}
	}
	if address.IsEmpty() || pubkey == "" {
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("inactive-vault refund requires an eligible inactive %s vault; run churn first", a.asset.Chain),
		}
	}
	a.inboundAddress, a.inboundVault = address, pubkey
	return OpResult{Continue: true}
}

func (a *InactiveVaultRefundActor) registerRefundMemo(_ *OpConfig) OpResult {
	a.memo = "TEST-INACTIVE-VAULT-REFUND"
	if !a.asset.Chain.Equals(common.XMRChain) {
		return OpResult{Continue: true}
	}
	thorAddress, err := a.account.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		return OpResult{Error: err}
	}
	// The inactive-vault handler refunds this ADD instead of adding liquidity.
	// Its explicit asset address is needed because XMR has no observed sender.
	a.registeredMemo = fmt.Sprintf("+:%s:%s/%s", a.asset, thorAddress, a.refundAddress)
	msg := types.NewMsgDeposit(
		common.NewCoins(common.NewCoin(common.RuneNative, cosmos.ZeroUint())),
		fmt.Sprintf("REFERENCE:%s:%s", a.asset, a.registeredMemo), thorAddress,
	)
	a.registrationTxID, err = a.account.Thorchain.BroadcastWithBlocking(msg)
	if err != nil {
		return OpResult{Error: err}
	}
	return OpResult{Continue: true}
}

func (a *InactiveVaultRefundActor) resolveRefundMemo(_ *OpConfig) OpResult {
	if !a.asset.Chain.Equals(common.XMRChain) {
		return OpResult{Continue: true}
	}
	registered, err := thornode.GetMemoHash(a.registrationTxID.String())
	if err != nil {
		return OpResult{Error: err}
	}
	memo, err := inactiveRefundReference(registered, a.asset, a.registeredMemo)
	if err != nil {
		return OpResult{Error: err}
	}
	a.memo = memo
	return OpResult{Continue: true}
}

func inactiveRefundReference(registered openapi.ReferenceMemoResponse, asset common.Asset, expectedMemo string) (string, error) {
	if registered.Asset != asset.String() || registered.Memo != expectedMemo {
		return "", fmt.Errorf("inactive-vault refund reference does not match the registered asset and memo")
	}
	ref, err := strconv.ParseUint(registered.Reference, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid inactive-vault refund reference: %w", err)
	}
	return fmt.Sprintf("r:%d", ref), nil
}

func (a *InactiveVaultRefundActor) sendInactiveVaultInbound(config *OpConfig) OpResult {
	client := a.account.ChainClients[a.asset.Chain]

	// create tx out
	tx := SimTx{
		Chain:     a.asset.Chain,
		ToAddress: a.inboundAddress,
		Coin:      common.NewCoin(a.asset, a.amount),
		Memo:      a.memo,
	}

	txid, err := SignAndBroadcastTx(client, tx)
	if err != nil {
		log.Error().Err(err).Msg("failed to broadcast tx")
		return SimTxErrorResult(err)
	}
	a.Log().Info().Str("txid", txid).Msg("sent inbound to inactive vault")

	a.inboundTxid = txid
	return OpResult{
		Continue: true,
	}
}

func (a *InactiveVaultRefundActor) findInactiveVaultAddress() (common.Address, string, error) {
	vaults, err := a.getInactiveVaults()
	if err != nil {
		return "", "", err
	}

	for _, vault := range vaults {
		if vault.Status != types.VaultStatus_InactiveVault.String() {
			continue
		}
		for _, address := range vault.Addresses {
			if address.Chain == a.asset.Chain.String() {
				pubkey := vault.GetPubKey()
				if a.asset.Chain.Equals(common.XMRChain) {
					pubkey = vault.GetPubKeyFrost()
				}
				if address.Address == "" || pubkey == "" {
					return "", "", fmt.Errorf("inactive %s vault has incomplete address or signing key", a.asset.Chain)
				}
				return common.Address(address.Address), pubkey, nil
			}
		}
	}

	return "", "", nil
}

func (a *InactiveVaultRefundActor) verifyRefund(config *OpConfig) OpResult {
	// get swap stages
	stages, err := thornode.GetTxStages(a.inboundTxid)
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
	details, err := thornode.GetTxDetails(a.inboundTxid)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx details")
		return OpResult{
			Continue: false,
		}
	}

	// Verify the deposit reached the selected retired vault and that the
	// completed outbound really is a refund to this user's source-chain address.
	err = validateInactiveVaultRefund(details, a.asset, a.inboundTxid, a.inboundVault, a.refundAddress, a.amount)
	a.account.Release()
	return OpResult{Finish: true, Error: err}
}

func validateInactiveVaultRefund(details openapi.TxDetailsResponse, asset common.Asset, inboundTxID, inboundVault string, refundAddress common.Address, deposited cosmos.Uint) error {
	if details.Tx.GetObservedPubKey() != inboundVault {
		return fmt.Errorf("refund inbound was not observed on the selected inactive vault")
	}
	if len(details.OutTxs) != 1 || len(details.Actions) != 1 {
		return fmt.Errorf("expected exactly one refund action and outbound")
	}
	action, outbound := details.Actions[0], details.OutTxs[0]
	memo := action.GetOriginalMemo()
	if memo == "" {
		memo = action.GetMemo()
	}
	if !strings.EqualFold(action.GetInHash(), inboundTxID) || !strings.EqualFold(memo, "REFUND:"+inboundTxID) {
		return fmt.Errorf("outbound action is not the expected inactive-vault refund")
	}
	if action.Chain != asset.Chain.String() || outbound.GetChain() != asset.Chain.String() ||
		!common.Address(action.ToAddress).Equals(refundAddress) || !common.Address(outbound.GetToAddress()).Equals(refundAddress) {
		return fmt.Errorf("inactive-vault refund has the wrong chain or recipient")
	}
	if len(outbound.Coins) != 1 || outbound.Coins[0].Asset != asset.String() || action.Coin.Asset != asset.String() {
		return fmt.Errorf("inactive-vault refund has the wrong asset")
	}
	amount, err := cosmos.ParseUint(outbound.Coins[0].Amount)
	if err != nil || amount.IsZero() || amount.GT(deposited) {
		return fmt.Errorf("inactive-vault refund has an invalid amount")
	}
	return nil
}
