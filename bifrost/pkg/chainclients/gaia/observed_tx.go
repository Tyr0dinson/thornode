package gaia

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
)

// observedTx is only for scanning transactions whose execution result came from
// Gaia. Unrelated messages must not prevent observation of supported deposits.
type observedTx struct {
	msgs []sdk.Msg
	memo string
	fee  *txtypes.Fee
}

func (t *observedTx) GetFee() sdk.Coins { return t.fee.Amount }
func (t *observedTx) GetGas() uint64    { return t.fee.GasLimit }

func decodeObservedTx(bz []byte) (*observedTx, error) {
	var raw txtypes.TxRaw
	if err := raw.Unmarshal(bz); err != nil {
		return nil, err
	}
	var body txtypes.TxBody
	if err := body.Unmarshal(raw.BodyBytes); err != nil {
		return nil, err
	}
	var auth txtypes.AuthInfo
	if err := auth.Unmarshal(raw.AuthInfoBytes); err != nil {
		return nil, err
	}
	if auth.Fee == nil || !auth.Fee.Amount.IsValid() {
		return nil, fmt.Errorf("missing or invalid transaction fee")
	}
	tx := &observedTx{msgs: make([]sdk.Msg, len(body.Messages)), memo: body.Memo, fee: auth.Fee}
	for i, msg := range body.Messages {
		if msg == nil {
			return nil, fmt.Errorf("nil transaction message at index %d", i)
		}
		// Keep original indexes: IBC execution events identify messages by index.
		switch msg.TypeUrl {
		case sdk.MsgTypeURL(&banktypes.MsgSend{}):
			var send banktypes.MsgSend
			if err := send.Unmarshal(msg.Value); err != nil {
				return nil, err
			}
			tx.msgs[i] = &send
		case sdk.MsgTypeURL(&channeltypes.MsgRecvPacket{}):
			var recv channeltypes.MsgRecvPacket
			if err := recv.Unmarshal(msg.Value); err != nil {
				return nil, err
			}
			tx.msgs[i] = &recv
		}
	}
	return tx, nil
}
