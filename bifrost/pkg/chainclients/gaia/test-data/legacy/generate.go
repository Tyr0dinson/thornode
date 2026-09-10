//go:build ignore

// Run from a checkout of THORNode 757199a809e069932aeaff1628a6483d5fdde416:
// go run /absolute/path/to/this/generate.go > legacy-send.hex
// Synthetic decoding fixture, not a transaction submitted to Gaia.
package main

import (
	"fmt"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	channel "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
)

func main() {
	registry := codectypes.NewInterfaceRegistry()
	bank.RegisterInterfaces(registry)
	channel.RegisterInterfaces(registry)
	sender := "cosmos1y7nghzkv8vjt0hxy9q07p8ysycclvlrh5llmq0"
	msgs := []sdk.Msg{
		&channel.MsgPruneAcknowledgements{PortId: "transfer", ChannelId: "channel-0", Limit: 10, Signer: sender},
		&bank.MsgSend{FromAddress: sender, ToAddress: sender, Amount: sdk.NewCoins(sdk.NewCoin("uatom", sdkmath.NewInt(1000000)))},
		&channel.MsgChannelUpgradeTry{PortId: "transfer", ChannelId: "channel-0", Signer: sender},
	}
	body := txtypes.TxBody{Memo: "legacy-deposit"}
	for _, msg := range msgs {
		a, err := codectypes.NewAnyWithValue(msg)
		if err != nil {
			panic(err)
		}
		body.Messages = append(body.Messages, a)
	}
	bodyBytes, err := body.Marshal()
	if err != nil {
		panic(err)
	}
	auth := txtypes.AuthInfo{Fee: &txtypes.Fee{Amount: sdk.NewCoins(sdk.NewCoin("uatom", sdkmath.NewInt(1000))), GasLimit: 200000}}
	authBytes, err := auth.Marshal()
	if err != nil {
		panic(err)
	}
	raw := txtypes.TxRaw{BodyBytes: bodyBytes, AuthInfoBytes: authBytes}
	bz, err := raw.Marshal()
	if err != nil {
		panic(err)
	}
	decoded, err := authtx.DefaultTxDecoder(codec.NewProtoCodec(registry))(bz)
	if err != nil {
		panic(err)
	}
	if len(decoded.GetMsgs()) != 3 {
		panic("lost legacy messages")
	}
	fmt.Printf("%x\n", bz)
}
