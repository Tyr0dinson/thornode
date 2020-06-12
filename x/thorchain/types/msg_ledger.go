package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// MsgLedger means TSS keysign failed
type MsgLedger struct {
	ID          string            `json:"id"`
	Chain       common.Chain      `json:"chain"`
	ChainHeight int64             `json:"chain_height"`
	Ledgers     []Ledger          `json:"ledger"`
	Signer      cosmos.AccAddress `json:"signer"`
}

// NewMsgLedger create a new instance of MsgLedger message
func NewMsgLedger(height int64, chain common.Chain, ledgers []Ledger, signer cosmos.AccAddress) MsgLedger {
	return MsgLedger{
		ID:          getMsgLedgerID(height, chain, ledgers),
		Chain:       chain,
		ChainHeight: height,
		Ledgers:     ledgers,
		Signer:      signer,
	}
}

func getMsgLedgerID(height int64, chain common.Chain, ledgers []Ledger) string {
	// ensure input coins list is deterministically sorted
	sort.Slice(ledgers[:], func(i, j int) bool {
		return ledgers[i].PubKey.String() < ledgers[j].PubKey.String()
	})
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("%s", chain.String()))
	sb.WriteString(fmt.Sprintf("%d", height))
	for _, ledger := range ledgers {
		sb.WriteString(ledger.String())
	}
	hash := sha256.New()
	return hex.EncodeToString(hash.Sum([]byte(sb.String())))
}

// Route should return the cmname of the module
func (msg MsgLedger) Route() string { return RouterKey }

// Type should return the action
func (msg MsgLedger) Type() string { return "set_ledger" }

// ValidateBasic runs stateless checks on the message
func (msg MsgLedger) ValidateBasic() error {
	if msg.Signer.Empty() {
		return cosmos.ErrInvalidAddress(msg.Signer.String())
	}
	if len(msg.ID) == 0 {
		return cosmos.ErrUnknownRequest("ID cannot be blank")
	}
	if len(msg.Ledgers) == 0 {
		return cosmos.ErrUnknownRequest("cannot have no ledgers")
	}
	for _, ledger := range msg.Ledgers {
		if err := ledger.IsValid(); err != nil {
			return err
		}
	}
	if msg.Chain.IsEmpty() {
		return cosmos.ErrUnknownRequest("chain is empty")
	}
	if msg.ChainHeight <= 0 {
		return cosmos.ErrUnknownRequest("chain height is zero")
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (msg MsgLedger) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(msg))
}

// GetSigners defines whose signature is required
func (msg MsgLedger) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{msg.Signer}
}
