package core

import (
	"fmt"
	"strconv"

	sdkmath "cosmossdk.io/math"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// SwapMemolessActor
////////////////////////////////////////////////////////////////////////////////////////

//go:generate stringer -type=MemolessType
type MemolessType int

const (
	MemolessTypeRef    MemolessType = iota // "r:<ref>" memo
	MemolessTypeAmount                     // reference encoded in amount
	MemolessTypeHash                       // no reference, register inbound hash
)

type SwapMemolessActor struct {
	SwapActor

	memolessType     MemolessType
	registeredMemo   string
	registrationTxID common.TxID
	memolessRef      uint64
	swapMemo         string
	signedSwap       []byte
}

func NewSwapMemolessActor(from, to common.Asset, memolessType MemolessType) *Actor {
	a := &SwapMemolessActor{
		SwapActor: SwapActor{
			Actor: *NewActor(fmt.Sprintf("SwapMemoless %s => %s", from, to)),
			from:  from,
			to:    to,
		},
		memolessType: memolessType,
	}

	a.Ops = append(a.Ops, a.updateLogContext)

	// lock an account with from balance
	a.Ops = append(a.Ops, a.acquireUser)

	// generate swap quote
	a.Ops = append(a.Ops, a.getQuote)

	// TODO: support tokens
	if !from.IsGasAsset() {
		log.Fatal().Msg("simulation only implemented for gas assets")
	}

	// TODO: support assets with less than 1e8 precision
	if from.Chain.GetGasAssetDecimal() != 8 {
		log.Fatal().Msg("simulation only implemented for assets with at least 1e8 precision")
	}

	switch memolessType {
	case MemolessTypeRef:
		a.Ops = append(a.Ops, a.registerSwap)
		a.Ops = append(a.Ops, a.getMemolessRef)
		a.Ops = append(a.Ops, a.prepareSwapMemolessByRef)
		a.Ops = append(a.Ops, a.signSwap)
		a.Ops = append(a.Ops, a.broadcastSwap)
	case MemolessTypeAmount:
		a.Ops = append(a.Ops, a.registerSwap)
		a.Ops = append(a.Ops, a.getMemolessRef)
		a.Ops = append(a.Ops, a.prepareSwapMemolessByAmount)
		a.Ops = append(a.Ops, a.signSwap)
		a.Ops = append(a.Ops, a.broadcastSwap)
	case MemolessTypeHash:
		a.Ops = append(a.Ops, a.signSwap)
		a.Ops = append(a.Ops, a.registerSwap) // uses a.swapTxID from previous step
		a.Ops = append(a.Ops, a.broadcastSwap)
	}

	// verify the swap within expected range
	a.Ops = append(a.Ops, a.verifyOutbound)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *SwapMemolessActor) updateLogContext(config *OpConfig) OpResult {
	a.SetLogger(log.With().Stringer("type", a.memolessType).Logger())
	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) registerSwap(config *OpConfig) OpResult {
	// build the swap
	coin := common.NewCoin(common.RuneNative, sdkmath.ZeroUint())
	a.registeredMemo = fmt.Sprintf("=:%s:%s", a.to, a.toAddress)

	registerHash := a.swapTxID // if preregistering hash
	if a.swapTxID == "" {
		// generate dummy hash since current required to register
		// TODO: fix in memoless PR to not require
		registerHash = common.RandHexString(64)
	}
	registerMemo := fmt.Sprintf("REFERENCE:%s:%s:%s", a.from.Chain, registerHash, a.registeredMemo)

	thorAddress, err := a.user.PubKey(common.THORChain).GetAddress(common.THORChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor address")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}
	thorAccAddr, err := thorAddress.AccAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor acc address")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}
	deposit := types.NewMsgDeposit(common.NewCoins(coin), registerMemo, thorAccAddr)
	a.Log().Info().Interface("deposit", deposit).Msg("registering memoless swap")

	// broadcast the registration
	a.registrationTxID, err = a.user.Thorchain.Broadcast(deposit)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}

	a.Log().Info().
		Stringer("txid", a.registrationTxID).
		Str("memo", registerMemo).
		Msg("broadcasted register tx")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) getMemolessRef(config *OpConfig) OpResult {
	referenceMemo, err := thornode.GetMemoHash(a.registrationTxID.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get reference memo")
		return OpResult{
			Continue: false,
		}
	}

	if referenceMemo.Memo == "" {
		a.Log().Error().Msg("reference memo not found")
		return OpResult{
			Continue: false,
		}
	}
	if referenceMemo.Memo != a.registeredMemo {
		a.Log().Error().
			Str("expected", a.registeredMemo).
			Str("actual", referenceMemo.Memo).
			Msg("reference memo does not match")
		return OpResult{
			Continue: false,
			Error:    fmt.Errorf("reference memo does not match"),
		}
	}

	a.memolessRef, err = strconv.ParseUint(referenceMemo.Reference, 10, 64)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to parse memoless ref")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}

	a.Log().Info().Uint64("ref", a.memolessRef).Msg("got memoless ref")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) prepareSwapMemolessByRef(config *OpConfig) OpResult {
	a.swapMemo = fmt.Sprintf("r:%d", a.memolessRef)
	a.Log().Info().Str("memo", a.swapMemo).Msg("prepared memoless swap memo")
	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) prepareSwapMemolessByAmount(config *OpConfig) OpResult {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get mimirs")
		return OpResult{
			Continue: false,
		}
	}
	start := mimirs["MEMOLESSTXNSTARTREF"]
	end := mimirs["MEMOLESSTXNENDREF"]
	memolessRefCount := uint64(end - start + 1)

	// encode the memoless ref in the amount
	a.Log().Info().Uint64("memolessRefCount", memolessRefCount).Msg("preparing memoless by amount")
	before := a.swapAmount
	a.swapAmount = a.swapAmount.Sub(a.swapAmount.Mod(sdkmath.NewUint(memolessRefCount)))
	a.swapAmount = a.swapAmount.Add(sdkmath.NewUint(a.memolessRef))
	a.swapAmount = a.swapAmount.Add(a.from.Chain.DustThreshold())
	a.Log().Info().
		Stringer("before", before).
		Stringer("after", a.swapAmount).
		Msg("encoded memoless ref in amount")

	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) signSwap(config *OpConfig) OpResult {
	// get inbound address
	inboundAddr, _, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return OpResult{
			Continue: false,
		}
	}

	// create tx
	tx := SimTx{
		Chain:     a.from.Chain,
		ToAddress: inboundAddr,
		Coin:      common.NewCoin(a.from, a.swapAmount),
		Memo:      a.swapMemo,
	}

	// sign transaction
	client := a.user.ChainClients[a.from.Chain]
	a.signedSwap, err = client.SignTx(tx)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign tx")
		return OpResult{
			Continue: false,
		}
	}

	// hash the signed transaction to get the txid
	txid, err := client.HashTx(a.signedSwap)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to hash tx")
		return OpResult{
			Continue: false,
			Error:    err,
		}
	}
	a.swapTxID = txid

	return OpResult{
		Continue: true,
	}
}

func (a *SwapMemolessActor) broadcastSwap(config *OpConfig) OpResult {
	// broadcast transaction
	client := a.user.ChainClients[a.from.Chain]
	txid, err := client.BroadcastTx(a.signedSwap)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}

	a.Log().Info().Str("txid", txid).Msg("broadcasted swap tx")
	return OpResult{
		Continue: true,
	}
}
