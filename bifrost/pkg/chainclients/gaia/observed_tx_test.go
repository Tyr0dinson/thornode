package gaia

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/cometbft/cometbft/crypto/tmhash"
	tmtypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	channel "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
	"github.com/stretchr/testify/require"
)

func TestObserveLegacyCompanionMessages(t *testing.T) {
	fixture, err := os.ReadFile("test-data/legacy/legacy-send.hex")
	require.NoError(t, err)
	raw, err := hex.DecodeString(strings.TrimSpace(string(fixture)))
	require.NoError(t, err)
	registry := codectypes.NewInterfaceRegistry()
	bank.RegisterInterfaces(registry)
	channel.RegisterInterfaces(registry)
	_, err = authtx.DefaultTxDecoder(codec.NewProtoCodec(registry))(raw)
	require.ErrorContains(t, err, "MsgPruneAcknowledgements")
	decoded, err := decodeObservedTx(raw)
	require.NoError(t, err)
	require.Len(t, decoded.msgs, 3)
	require.Nil(t, decoded.msgs[0])
	require.IsType(t, &bank.MsgSend{}, decoded.msgs[1])
	require.Nil(t, decoded.msgs[2])
	scanner := newFailedTxScanner(t, "")
	scanner.rpc = &mockTendermintRPC{}
	items, err := scanner.processTxs(1, []tmtypes.Tx{raw})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, hex.EncodeToString(tmhash.Sum(raw)), items[0].Tx)
	require.Equal(t, "legacy-deposit", items[0].Memo)
	require.Equal(t, uint64(100000000), items[0].Coins[0].Amount.Uint64())
	require.Equal(t, uint64(100000), items[0].Gas[0].Amount.Uint64())

	// Malformed supported messages must still reject the transaction.
	var envelope txtypes.TxRaw
	require.NoError(t, envelope.Unmarshal(raw))
	var body txtypes.TxBody
	require.NoError(t, body.Unmarshal(envelope.BodyBytes))
	body.Messages[1].Value = []byte{0xff}
	envelope.BodyBytes, err = body.Marshal()
	require.NoError(t, err)
	malformed, err := envelope.Marshal()
	require.NoError(t, err)
	_, err = decodeObservedTx(malformed)
	require.Error(t, err)
	_, err = decodeObservedTx([]byte{0xff})
	require.Error(t, err)
	_, err = decodeObservedTx(nil)
	require.Error(t, err)
}
