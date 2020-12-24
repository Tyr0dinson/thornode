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

// NewMsgTssPool is a constructor function for MsgTssPool
func NewMsgTssPool(pks []string, poolpk common.PubKey, KeygenType KeygenType, height int64, bl Blame, chains []string, signer cosmos.AccAddress, keygenTime int64) MsgTssPool {
	return MsgTssPool{
		ID:         getTssID(pks, poolpk, height, bl),
		PubKeys:    pks,
		PoolPubKey: poolpk,
		Height:     height,
		KeygenType: KeygenType,
		Blame:      bl,
		Chains:     chains,
		Signer:     signer,
		KeygenTime: keygenTime,
	}
}

// getTssID
func getTssID(members []string, poolPk common.PubKey, height int64, bl Blame) string {
	// ensure input pubkeys list is deterministically sorted
	sort.SliceStable(members, func(i, j int) bool {
		return members[i] < members[j]
	})

	pubkeys := make([]string, len(bl.BlameNodes))
	for i, node := range bl.BlameNodes {
		pubkeys[i] = node.Pubkey
	}
	sort.SliceStable(pubkeys, func(i, j int) bool {
		return pubkeys[i] < pubkeys[j]
	})

	sb := strings.Builder{}
	for _, item := range members {
		sb.WriteString("m:" + item)
	}
	for _, item := range pubkeys {
		sb.WriteString("p:" + item)
	}
	sb.WriteString(poolPk.String())
	sb.WriteString(fmt.Sprintf("%d", height))
	hash := sha256.New()
	return hex.EncodeToString(hash.Sum([]byte(sb.String())))
}

// Route should return the route key of the module
func (msg MsgTssPool) Route() string { return RouterKey }

// Type should return the action
func (msg MsgTssPool) Type() string { return "set_tss_pool" }

// ValidateBasic runs stateless checks on the message
func (msg MsgTssPool) ValidateBasic() error {
	if msg.Signer.Empty() {
		return cosmos.ErrInvalidAddress(msg.Signer.String())
	}
	if len(msg.ID) == 0 {
		return cosmos.ErrUnknownRequest("ID cannot be blank")
	}
	if len(msg.PubKeys) < 2 {
		return cosmos.ErrUnknownRequest("Must have at least 2 pub keys")
	}
	if len(msg.PubKeys) > 100 {
		return cosmos.ErrUnknownRequest("Must have no more then 100 pub keys")
	}
	pks, err := msg.GetPubKeys()
	if err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	for _, pk := range pks {
		if pk.IsEmpty() {
			return cosmos.ErrUnknownRequest("Pubkey cannot be empty")
		}
	}
	// PoolPubKey can't be empty only when keygen success
	if msg.IsSuccess() {
		if msg.PoolPubKey.IsEmpty() {
			return cosmos.ErrUnknownRequest("Pool pubkey cannot be empty")
		}
	}
	// ensure pool pubkey is a valid bech32 pubkey
	if _, err := common.NewPubKey(msg.PoolPubKey.String()); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	chains, err := msg.GetChains()
	if err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if !chains.Has(common.RuneAsset().Chain) {
		return cosmos.ErrUnknownRequest("must support rune asset chain")
	}
	if len(chains) != len(chains.Distinct()) {
		return cosmos.ErrUnknownRequest("cannot have duplicate chains")
	}
	return nil
}

// IsSuccess when blame is empty , then treat it as success
func (msg MsgTssPool) IsSuccess() bool {
	return msg.Blame.IsEmpty()
}

func (msg MsgTssPool) GetChains() (common.Chains, error) {
	chains := make(common.Chains, len(msg.Chains))
	for i, c := range msg.Chains {
		var err error
		chains[i], err = common.NewChain(c)
		if err != nil {
			return nil, err
		}
	}
	return chains, nil
}

func (msg MsgTssPool) GetPubKeys() (common.PubKeys, error) {
	pubkeys := make(common.PubKeys, len(msg.PubKeys))
	for i, pk := range msg.PubKeys {
		var err error
		pubkeys[i], err = common.NewPubKey(pk)
		if err != nil {
			return nil, err
		}
	}
	return pubkeys, nil
}

// GetSignBytes encodes the message for signing
func (msg MsgTssPool) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(&msg))
}

// GetSigners defines whose signature is required
func (msg MsgTssPool) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{msg.Signer}
}
