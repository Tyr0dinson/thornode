package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// BondYieldMemo represents BONDYIELD claim and auto-claim configuration memos.
type BondYieldMemo struct {
	MemoBase
	NodeAddress   cosmos.AccAddress
	Amount        cosmos.Uint
	AutoClaim     bool
	AutoClaimBps  uint64
	PayoutAsset   common.Asset
	PayoutAddress common.Address
}

// NewBondYieldMemo builds a bond yield memo for either a direct claim or auto-claim update.
func NewBondYieldMemo(
	nodeAddress cosmos.AccAddress,
	amount cosmos.Uint,
	autoClaim bool,
	autoClaimBps uint64,
	payoutAsset common.Asset,
	payoutAddress common.Address,
) BondYieldMemo {
	return BondYieldMemo{
		MemoBase:      MemoBase{TxType: TxBondYield},
		NodeAddress:   nodeAddress,
		Amount:        amount,
		AutoClaim:     autoClaim,
		AutoClaimBps:  autoClaimBps,
		PayoutAsset:   payoutAsset,
		PayoutAddress: payoutAddress,
	}
}

// GetAccAddress returns the node account whose provider is claiming bond yield.
func (m BondYieldMemo) GetAccAddress() cosmos.AccAddress { return m.NodeAddress }

// GetAmount returns the requested claim amount for direct bond yield claims.
func (m BondYieldMemo) GetAmount() cosmos.Uint { return m.Amount }

// ParseBondYieldMemo parses BONDYIELD memos for direct claims and auto-claim configuration.
func (p *parser) ParseBondYieldMemo() (BondYieldMemo, error) {
	nodeAddress := p.getAccAddress(1, true, nil)
	if strings.EqualFold(p.get(2), "AUTO") {
		autoClaimBps := p.getUintWithMaxValue(3, true, 0, 10000).Uint64()
		payoutAsset := p.getAsset(4, false, common.EmptyAsset)
		payoutAddress := p.getAddress(5, false, common.NoAddress)
		return NewBondYieldMemo(nodeAddress, cosmos.ZeroUint(), true, autoClaimBps, payoutAsset, payoutAddress), p.Error()
	}

	amount := p.getUint(2, true, 0)
	return NewBondYieldMemo(nodeAddress, amount, false, 0, common.EmptyAsset, common.NoAddress), p.Error()
}
