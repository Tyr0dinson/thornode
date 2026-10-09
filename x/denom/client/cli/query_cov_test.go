package cli_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/x/denom/client/cli"
	"gitlab.com/thorchain/thornode/v3/x/denom/types"
)

// mockCometCov answers ABCI queries from memory.
type mockCometCov struct {
	client.CometRPC
	value   []byte
	err     error
	code    uint32
	gotPath string
	gotData []byte
}

func (m *mockCometCov) ABCIQueryWithOptions(_ context.Context, path string, data cmtbytes.HexBytes, _ rpcclient.ABCIQueryOptions) (*coretypes.ResultABCIQuery, error) {
	m.gotPath = path
	m.gotData = data
	if m.err != nil {
		return nil, m.err
	}
	return &coretypes.ResultABCIQuery{Response: abci.ResponseQuery{Code: m.code, Value: m.value, Height: 1, Log: "query failed"}}, nil
}

func newClientCtxCov(node client.CometRPC, out *bytes.Buffer) client.Context {
	reg := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(reg)
	return client.Context{}.
		WithCodec(codec.NewProtoCodec(reg)).
		WithInterfaceRegistry(reg).
		WithClient(node).
		WithOutput(out).
		WithOutputFormat("json")
}

func TestGetQueryCmdCov(t *testing.T) {
	cmd := cli.GetQueryCmd()
	require.Equal(t, types.ModuleName, cmd.Use)
	require.True(t, cmd.DisableFlagParsing)
	require.Len(t, cmd.Commands(), 1)
	require.Equal(t, "admin", cmd.Commands()[0].Name())
}

func TestDenomAdminCmdCov(t *testing.T) {
	resp := &types.QueryDenomAdminResponse{Admin: "tthor1admin"}
	bz, err := resp.Marshal()
	require.NoError(t, err)
	node := &mockCometCov{value: bz}
	out := &bytes.Buffer{}

	cmd := cli.GetCmdDenomAdmin()
	require.NotNil(t, cmd.Flags().Lookup("height"))
	require.NotNil(t, cmd.Flags().Lookup("output"))
	setCtxCov(t, cmd, newClientCtxCov(node, out))
	cmd.SetArgs([]string{"x/btc"})
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, "/thorchain.denom.v1.Query/DenomAdmin", node.gotPath)
	var req types.QueryDenomAdminRequest
	require.NoError(t, req.Unmarshal(node.gotData))
	require.Equal(t, "x/btc", req.Denom)
	require.Contains(t, out.String(), `"admin":"tthor1admin"`)
}

func TestDenomAdminCmdErrorsCov(t *testing.T) {
	out := &bytes.Buffer{}

	// wrong arg count
	cmd := cli.GetCmdDenomAdmin()
	setCtxCov(t, cmd, newClientCtxCov(&mockCometCov{}, out))
	cmd.SetArgs([]string{})
	cmd.SetOut(out)
	cmd.SetErr(out)
	err := cmd.Execute()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "accepts 1 arg"))

	// node error propagates
	cmd = cli.GetCmdDenomAdmin()
	boom := errors.New("node down")
	setCtxCov(t, cmd, newClientCtxCov(&mockCometCov{err: boom}, out))
	cmd.SetArgs([]string{"x/btc"})
	cmd.SetOut(out)
	cmd.SetErr(out)
	require.ErrorContains(t, cmd.Execute(), "node down")

	// an unparsable --node makes the client query context fail
	cmd = cli.GetCmdDenomAdmin()
	setCtxCov(t, cmd, newClientCtxCov(&mockCometCov{}, out))
	cmd.SetArgs([]string{"x/btc", "--node", "tcp://bad\x7fhost:1"})
	cmd.SetOut(out)
	cmd.SetErr(out)
	require.Error(t, cmd.Execute())
}

func setCtxCov(t *testing.T, cmd *cobra.Command, clientCtx client.Context) {
	t.Helper()
	cmd.SetContext(context.Background())
	require.NoError(t, client.SetCmdClientContext(cmd, clientCtx))
}
