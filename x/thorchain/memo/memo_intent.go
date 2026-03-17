package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// IntentOpenMemo represents a memo for opening an intent
// Format: IO:TARGET_ASSET:HIGH:LOW:TARGET_ADDR:AFFILIATE:FEE_BPS
type IntentOpenMemo struct {
	MemoBase
	TargetAsset          common.Asset
	TargetAddress        common.Address
	HighTarget           cosmos.Uint
	LowTarget            cosmos.Uint
	AffiliateAddress     common.Address
	AffiliateBasisPoints cosmos.Uint
}

func (m IntentOpenMemo) GetTargetAsset() common.Asset         { return m.TargetAsset }
func (m IntentOpenMemo) GetTargetAddress() common.Address     { return m.TargetAddress }
func (m IntentOpenMemo) GetHighTarget() cosmos.Uint           { return m.HighTarget }
func (m IntentOpenMemo) GetLowTarget() cosmos.Uint            { return m.LowTarget }
func (m IntentOpenMemo) GetAffiliateAddress() common.Address  { return m.AffiliateAddress }
func (m IntentOpenMemo) GetAffiliateBasisPoints() cosmos.Uint { return m.AffiliateBasisPoints }

func (m IntentOpenMemo) String() string {
	affAddr := ""
	if !m.AffiliateAddress.IsEmpty() {
		affAddr = m.AffiliateAddress.String()
	}
	affBps := ""
	if !m.AffiliateBasisPoints.IsZero() {
		affBps = m.AffiliateBasisPoints.String()
	}

	args := []string{
		m.TxType.String(),
		m.TargetAsset.String(),
		m.HighTarget.String(),
		m.LowTarget.String(),
		m.TargetAddress.String(),
		affAddr,
		affBps,
	}

	// Trim trailing empty fields
	last := 5
	if affBps != "" {
		last = 7
	} else if affAddr != "" {
		last = 6
	}

	return strings.Join(args[:last], ":")
}

func NewIntentOpenMemo(targetAsset common.Asset, targetAddr common.Address, highTarget, lowTarget cosmos.Uint, affAddr common.Address, affBps cosmos.Uint) IntentOpenMemo {
	return IntentOpenMemo{
		MemoBase:             MemoBase{TxType: TxIntentOpen},
		TargetAsset:          targetAsset,
		TargetAddress:        targetAddr,
		HighTarget:           highTarget,
		LowTarget:            lowTarget,
		AffiliateAddress:     affAddr,
		AffiliateBasisPoints: affBps,
	}
}

func (p *parser) ParseIntentOpenMemo() (IntentOpenMemo, error) {
	// Format: IO:TARGET_ASSET:HIGH:LOW:TARGET_ADDR:AFFILIATE:FEE_BPS
	targetAsset := p.getAsset(1, true, common.EmptyAsset)
	highTarget := p.getUintWithScientificNotation(2, true, 0)
	lowTarget := p.getUintWithScientificNotation(3, true, 0)

	// For secured and trade assets, the target address must be on THORChain
	targetAddrChain := targetAsset.Chain
	if targetAsset.IsSecuredAsset() || targetAsset.IsTradeAsset() {
		targetAddrChain = common.THORChain
	}
	targetAddr := p.getAddressWithKeeper(4, true, common.NoAddress, targetAddrChain)

	// Affiliate is optional
	affAddr := p.getAddressWithKeeper(5, false, common.NoAddress, common.THORChain)
	affBps := p.getUintWithMaxValue(6, false, 0, constants.MaxBasisPts)

	return NewIntentOpenMemo(targetAsset, targetAddr, highTarget, lowTarget, affAddr, affBps), p.Error()
}

// IntentCancelMemo represents a memo for cancelling an intent
// Format: IC:INTENT_TX_HASH[:DEFER]
type IntentCancelMemo struct {
	MemoBase
	IntentTxID       common.TxID
	DeferUntilExpiry bool
}

func (m IntentCancelMemo) GetIntentTxID() common.TxID { return m.IntentTxID }
func (m IntentCancelMemo) GetDeferUntilExpiry() bool  { return m.DeferUntilExpiry }

func (m IntentCancelMemo) String() string {
	if m.DeferUntilExpiry {
		return fmt.Sprintf("%s:%s:defer", m.TxType.String(), m.IntentTxID.String())
	}
	return fmt.Sprintf("%s:%s", m.TxType.String(), m.IntentTxID.String())
}

func NewIntentCancelMemo(txID common.TxID, deferUntilExpiry ...bool) IntentCancelMemo {
	deferFlag := false
	if len(deferUntilExpiry) > 0 {
		deferFlag = deferUntilExpiry[0]
	}
	return IntentCancelMemo{
		MemoBase:         MemoBase{TxType: TxIntentCancel},
		IntentTxID:       txID,
		DeferUntilExpiry: deferFlag,
	}
}

func (p *parser) ParseIntentCancelMemo() (IntentCancelMemo, error) {
	// Format: IC:INTENT_TX_HASH[:DEFER]
	txID := p.getTxID(1, true, common.TxID(""))
	deferFlag := false
	mode := p.getString(2, false, "")
	if mode != "" {
		if strings.EqualFold(mode, "defer") {
			deferFlag = true
		} else {
			p.addErr(fmt.Errorf("invalid intent cancel mode: %s", mode))
		}
	}
	return NewIntentCancelMemo(txID, deferFlag), p.Error()
}

// IntentSolveMemo represents a memo for solving an intent
// Format: IS:INTENT_TX_HASH:DESTINATION_ADDR
// DESTINATION_ADDR is where the solver wants to receive the intent's source asset
// This allows cross-chain solving where the solver sends from one chain and receives on another
type IntentSolveMemo struct {
	MemoBase
	IntentTxID         common.TxID
	DestinationAddress common.Address
}

func (m IntentSolveMemo) GetIntentTxID() common.TxID            { return m.IntentTxID }
func (m IntentSolveMemo) GetDestinationAddress() common.Address { return m.DestinationAddress }

func (m IntentSolveMemo) String() string {
	if m.DestinationAddress.IsEmpty() {
		return fmt.Sprintf("%s:%s", m.TxType.String(), m.IntentTxID.String())
	}
	return fmt.Sprintf("%s:%s:%s", m.TxType.String(), m.IntentTxID.String(), m.DestinationAddress.String())
}

func NewIntentSolveMemo(txID common.TxID, destAddr common.Address) IntentSolveMemo {
	return IntentSolveMemo{
		MemoBase:           MemoBase{TxType: TxIntentSolve},
		IntentTxID:         txID,
		DestinationAddress: destAddr,
	}
}

func (p *parser) ParseIntentSolveMemo() (IntentSolveMemo, error) {
	// Format: IS:INTENT_TX_HASH:DESTINATION_ADDR
	// DESTINATION_ADDR is optional in memo parsing - validation happens in handler
	// when the intent is looked up and we know which chain to validate against
	txID := p.getTxID(1, true, common.TxID(""))
	// Destination address parsing - we can't validate the chain here since we don't
	// know the intent's source asset yet. Handler will validate it matches the source chain.
	destAddr := p.getAddress(2, false, common.NoAddress)
	return NewIntentSolveMemo(txID, destAddr), p.Error()
}

// IntentAuctionBidMemo represents a memo for bidding on an intent auction
// Format: IAB:INTENT_TX_HASH
type IntentAuctionBidMemo struct {
	MemoBase
	IntentTxID common.TxID
}

func (m IntentAuctionBidMemo) GetIntentTxID() common.TxID { return m.IntentTxID }

func (m IntentAuctionBidMemo) String() string {
	return fmt.Sprintf("%s:%s", m.TxType.String(), m.IntentTxID.String())
}

func NewIntentAuctionBidMemo(txID common.TxID) IntentAuctionBidMemo {
	return IntentAuctionBidMemo{
		MemoBase:   MemoBase{TxType: TxIntentAuctionBid},
		IntentTxID: txID,
	}
}

func (p *parser) ParseIntentAuctionBidMemo() (IntentAuctionBidMemo, error) {
	// Format: IAB:INTENT_TX_HASH
	txID := p.getTxID(1, true, common.TxID(""))
	return NewIntentAuctionBidMemo(txID), p.Error()
}
