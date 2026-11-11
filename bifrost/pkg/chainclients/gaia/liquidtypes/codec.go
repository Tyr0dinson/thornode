// Package liquidtypes vendors the Cosmos Hub x/liquid message types
// (gaia.liquid.v1beta1) so bifrost can DECODE Hub transactions that carry
// them. Bifrost never acts on these messages.
//
// liquid.pb.go and tx.pb.go are generated code copied unmodified from
// github.com/cosmos/gaia v29.1.0, x/liquid/types (Apache-2.0), except for the
// package clause. The gaia module itself cannot be imported: it requires
// ibc-go v10 and a newer wasmd than this repository builds with. tx.pb.go is
// byte-identical from v25.1.0, where the module was introduced, to v29.1.0.
// To refresh after a Gaia release that changes proto/gaia/liquid/v1beta1,
// copy the two files again and re-run the gaia chain client tests.
package liquidtypes

import (
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

// RegisterInterfaces registers the x/liquid messages with the interface
// registry. It mirrors gaia's x/liquid/types/codec.go without the legacy amino
// half, which a decode-only registry does not need.
func RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	registry.RegisterImplementations(
		(*sdk.Msg)(nil),
		&MsgUpdateParams{},
		&MsgTokenizeShares{},
		&MsgRedeemTokensForShares{},
		&MsgTransferTokenizeShareRecord{},
		&MsgDisableTokenizeShares{},
		&MsgEnableTokenizeShares{},
		&MsgWithdrawTokenizeShareRecordReward{},
		&MsgWithdrawAllTokenizeShareRecordReward{},
	)

	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
