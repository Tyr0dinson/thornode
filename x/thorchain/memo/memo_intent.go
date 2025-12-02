package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
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
	targetAddr := p.getAddressWithKeeper(4, true, common.NoAddress, targetAsset.Chain)

	// Affiliate is optional
	affAddr := p.getAddressWithKeeper(5, false, common.NoAddress, common.THORChain)
	affBps := p.getUint(6, false, 0)

	return NewIntentOpenMemo(targetAsset, targetAddr, highTarget, lowTarget, affAddr, affBps), p.Error()
}

// IntentCancelMemo represents a memo for cancelling an intent
// Format: IC:INTENT_TX_HASH
type IntentCancelMemo struct {
	MemoBase
	IntentTxID common.TxID
}

func (m IntentCancelMemo) GetIntentTxID() common.TxID { return m.IntentTxID }

func (m IntentCancelMemo) String() string {
	return fmt.Sprintf("%s:%s", m.TxType.String(), m.IntentTxID.String())
}

func NewIntentCancelMemo(txID common.TxID) IntentCancelMemo {
	return IntentCancelMemo{
		MemoBase:   MemoBase{TxType: TxIntentCancel},
		IntentTxID: txID,
	}
}

func (p *parser) ParseIntentCancelMemo() (IntentCancelMemo, error) {
	// Format: IC:INTENT_TX_HASH
	txID := p.getTxID(1, true, common.TxID(""))
	return NewIntentCancelMemo(txID), p.Error()
}

// IntentSolveMemo represents a memo for solving an intent
// Format: IS:INTENT_TX_HASH
type IntentSolveMemo struct {
	MemoBase
	IntentTxID common.TxID
}

func (m IntentSolveMemo) GetIntentTxID() common.TxID { return m.IntentTxID }

func (m IntentSolveMemo) String() string {
	return fmt.Sprintf("%s:%s", m.TxType.String(), m.IntentTxID.String())
}

func NewIntentSolveMemo(txID common.TxID) IntentSolveMemo {
	return IntentSolveMemo{
		MemoBase:   MemoBase{TxType: TxIntentSolve},
		IntentTxID: txID,
	}
}

func (p *parser) ParseIntentSolveMemo() (IntentSolveMemo, error) {
	// Format: IS:INTENT_TX_HASH
	txID := p.getTxID(1, true, common.TxID(""))
	return NewIntentSolveMemo(txID), p.Error()
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
