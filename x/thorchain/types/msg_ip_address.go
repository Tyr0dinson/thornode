package types

import (
	"fmt"
	"net"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgSetIPAddress{}
	_ sdk.HasValidateBasic = &MsgSetIPAddress{}
	_ sdk.LegacyMsg        = &MsgSetIPAddress{}
)

// NewMsgSetIPAddress is a constructor function for NewMsgSetIPAddress
func NewMsgSetIPAddress(ip string, signer cosmos.AccAddress) *MsgSetIPAddress {
	return &MsgSetIPAddress{
		IPAddress: ip,
		Signer:    signer,
	}
}

// IsValidOnionV3 returns true for a syntactically valid Tor v3 .onion hostname.
func IsValidOnionV3(addr string) bool {
	const suffix = ".onion"
	if !strings.HasSuffix(addr, suffix) {
		return false
	}

	label := strings.TrimSuffix(addr, suffix)
	if len(label) != 56 {
		return false
	}

	for _, c := range label {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}

	return true
}

// ValidateBasic implements HasValidateBasic
// ValidateBasic is now ran in the message service router handler for messages that
// used to be routed using the external handler and only when HasValidateBasic is implemented.
// No versioning is used there.
func (m *MsgSetIPAddress) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if strings.HasSuffix(m.IPAddress, ".onion") {
		if IsValidOnionV3(m.IPAddress) {
			return nil
		}
		if len(m.IPAddress) == 22 {
			return cosmos.ErrUnknownRequest("Tor v2 .onion not supported; use v3 (62 chars)")
		}
		return cosmos.ErrUnknownRequest("invalid .onion: must be 56-char base32 label + .onion")
	}
	if net.ParseIP(m.IPAddress) == nil {
		return cosmos.ErrUnknownRequest("invalid address: must be IPv4, IPv6, or Tor v3 .onion")
	}
	return nil
}

// GetSigners defines whose signature is required
func (m *MsgSetIPAddress) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgSetIPAddressCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgSetIPAddress)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgSetIPAddress: %T", m)
	}
	return [][]byte{msg.Signer}, nil
}
