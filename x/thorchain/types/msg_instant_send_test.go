package types

import (
	"strings"
	"testing"

	"github.com/cosmos/gogoproto/jsonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apitypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestInstantSendQueryRoundTrip(t *testing.T) {
	response := QueryInstantSendResponse{ObservationHeight: 96, MinAmount: 1000000, MaxAmount: 1000000000}
	marshaler := jsonpb.Marshaler{OrigName: true, EmitDefaults: true}
	jsonData, err := marshaler.MarshalToString(&response)
	require.NoError(t, err)
	require.JSONEq(t, `{"vaults":[],"observation_height":"96","min_amount":"1000000","max_amount":"1000000000","status":{"chain":"","disabled":false,"overdue":[],"min_height":"0"}}`, jsonData)
	var decoded QueryInstantSendResponse
	err = jsonpb.Unmarshal(strings.NewReader(jsonData), &decoded)
	require.NoError(t, err)
	data, err := response.Marshal()
	require.NoError(t, err)
	decodedData, err := decoded.Marshal()
	require.NoError(t, err)
	require.Equal(t, data, decodedData)
	var modern apitypes.QueryInstantSendResponse
	err = proto.Unmarshal(data, &modern)
	require.NoError(t, err)
	require.Equal(t, response.ObservationHeight, modern.ObservationHeight)
	require.Equal(t, response.MinAmount, modern.MinAmount)
	require.Equal(t, response.MaxAmount, modern.MaxAmount)
	data, err = proto.Marshal(&modern)
	require.NoError(t, err)
	err = decoded.Unmarshal(data)
	require.NoError(t, err)
	decodedData, err = decoded.Marshal()
	require.NoError(t, err)
	require.Equal(t, data, decodedData)
}

func TestInstantSendCustomerEnvelope(t *testing.T) {
	SetupConfigForTest()
	key := GetRandomPubKey()
	vault, err := key.GetAddress(common.DASHChain)
	require.NoError(t, err)
	customer, err := GetRandomPubKey().GetAddress(common.DASHChain)
	require.NoError(t, err)
	tx := common.NewObservedTx(common.Tx{
		ID: GetRandomTxHash(), Chain: common.DASHChain, FromAddress: customer, ToAddress: vault,
		Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One))),
		Gas:   common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))}, Memo: "NOOP",
	}, 100, key, 100)
	tx.InstantSend = &common.InstantSend{}
	tx.SpentOutputRefs = []common.OutputRef{{TxHash: strings.Repeat("A", 64)}}
	msg := NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: tx, Inbound: true}, GetRandomBech32Addr())
	require.NoError(t, msg.ValidateBasic())
	msg.QuoTx.Inbound = false
	msg.QuoTx.ObsTx.Tx.FromAddress, msg.QuoTx.ObsTx.Tx.ToAddress = vault, customer
	require.ErrorContains(t, msg.ValidateBasic(), "customer inbounds")
	legacy := NewMsgObservedTxOut(common.ObservedTxs{msg.QuoTx.ObsTx}, msg.Signer)
	require.ErrorContains(t, legacy.ValidateBasic(), "customer inbounds")
	msg.QuoTx.ObsTx.InstantSend = nil
	require.NoError(t, msg.ValidateBasic())
	legacy.Txs[0].InstantSend = nil
	require.NoError(t, legacy.ValidateBasic())
}
