package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	sdked25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/app/params"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func init() {
	types.SetupConfigForTest()
}

// txClientCtxCov returns a generate-only client context. If from is nil the
// context has no sender, which makes every msg fail ValidateBasic.
func txClientCtxCov(from cosmos.AccAddress, out io.Writer) client.Context {
	enc := params.MakeEncodingConfig()
	types.RegisterInterfaces(enc.InterfaceRegistry)
	ctx := client.Context{}.
		WithCodec(enc.Codec).
		WithInterfaceRegistry(enc.InterfaceRegistry).
		WithTxConfig(enc.TxConfig).
		WithLegacyAmino(enc.Amino).
		WithOutput(out).
		WithGenerateOnly(true).
		WithChainID("thorchain")
	if from != nil {
		ctx = ctx.WithFrom(from.String()).WithFromAddress(from).WithFromName("cov")
	}
	return ctx
}

// runCmdCov executes cmd with args using clientCtx and returns the generated
// tx output and the cobra stdout.
func runCmdCov(t *testing.T, cmd *cobra.Command, clientCtx client.Context, args ...string) (string, string, error) {
	t.Helper()
	txOut := &bytes.Buffer{}
	cmdOut := &bytes.Buffer{}
	clientCtx = clientCtx.WithOutput(txOut)
	if cmd.Flags().Lookup(flags.FlagNode) == nil {
		flags.AddTxFlagsToCmd(cmd)
	}
	cmd.SetContext(context.Background())
	require.NoError(t, client.SetCmdClientContext(cmd, clientCtx))
	cmd.SetArgs(args)
	cmd.SetOut(cmdOut)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return txOut.String(), cmdOut.String(), err
}

// generatedMsgCov parses the single message out of a generate-only tx.
func generatedMsgCov(t *testing.T, out string) map[string]any {
	t.Helper()
	var tx struct {
		Body struct {
			Messages []map[string]any `json:"messages"`
		} `json:"body"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &tx), out)
	require.Len(t, tx.Body.Messages, 1)
	return tx.Body.Messages[0]
}

func TestGetTxCmdCov(t *testing.T) {
	cmd := GetTxCmd()
	require.Equal(t, types.ModuleName, cmd.Use)
	names := []string{}
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
		// every sub command gets the standard tx flags
		require.NotNil(t, sub.Flags().Lookup(flags.FlagFrom), sub.Name())
		require.NotNil(t, sub.Flags().Lookup(flags.FlagGenerateOnly), sub.Name())
	}
	require.ElementsMatch(t, []string{
		"set-node-keys", "set-version", "propose-upgrade", "approve-upgrade",
		"reject-upgrade", "set-ip-address", "ban", "mimir", "pause-chain",
		"resume-chain", "deposit", "send", "observe-tx-ins", "observe-tx-outs",
	}, names)
}

func TestTxCommandsGenerateMsgsCov(t *testing.T) {
	from := types.GetRandomBech32Addr()
	to := types.GetRandomBech32Addr()
	ctx := txClientCtxCov(from, nil)

	consPk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeConsPub, sdked25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	secp := types.GetRandomPubKey()
	ed := types.GetRandomEd25519PubKey()

	testCases := []struct {
		name   string
		cmd    func() *cobra.Command
		args   []string
		typ    string
		expect map[string]any
	}{
		{
			name: "mimir upper-cases key", cmd: GetCmdMimir, args: []string{"halt", "12"},
			typ: "/types.MsgMimir", expect: map[string]any{"key": "HALT", "value": "12", "signer": from.String()},
		},
		{
			name: "deposit", cmd: GetCmdDeposit, args: []string{"100", "THOR.RUNE", "=:BTC.BTC:addr"},
			typ: "/types.MsgDeposit", expect: map[string]any{"memo": "=:BTC.BTC:addr", "signer": from.String()},
		},
		{
			name: "send", cmd: GetCmdSend, args: []string{to.String(), "25rune"},
			typ: "/types.MsgSend", expect: map[string]any{"from_address": from.String(), "to_address": to.String()},
		},
		{
			name: "pause-chain", cmd: GetCmdNodePauseChain, args: []string{},
			typ: "/types.MsgNodePauseChain", expect: map[string]any{"value": "1"},
		},
		{
			name: "resume-chain", cmd: GetCmdNodeResumeChain, args: []string{},
			typ: "/types.MsgNodePauseChain", expect: map[string]any{"value": "-1"},
		},
		{
			name: "ban", cmd: GetCmdBan, args: []string{to.String()},
			typ: "/types.MsgBan", expect: map[string]any{"node_address": to.String(), "signer": from.String()},
		},
		{
			name: "set-ip-address", cmd: GetCmdSetIPAddress, args: []string{"1.2.3.4"},
			typ: "/types.MsgSetIPAddress", expect: map[string]any{"ip_address": "1.2.3.4"},
		},
		{
			name: "set-version", cmd: GetCmdSetVersion, args: []string{},
			typ: "/types.MsgSetVersion", expect: map[string]any{"version": constants.SWVersion.String()},
		},
		{
			name: "propose-upgrade", cmd: GetCmdProposeUpgrade, args: []string{"1.2.3", "100", "--info", "hello"},
			typ: "/types.MsgProposeUpgrade", expect: map[string]any{"name": "1.2.3"},
		},
		{
			name: "approve-upgrade", cmd: GetCmdApproveUpgrade, args: []string{"1.2.3"},
			typ: "/types.MsgApproveUpgrade", expect: map[string]any{"name": "1.2.3"},
		},
		{
			name: "reject-upgrade", cmd: GetCmdRejectUpgrade, args: []string{"1.2.3"},
			typ: "/types.MsgRejectUpgrade", expect: map[string]any{"name": "1.2.3"},
		},
		{
			name: "set-node-keys", cmd: GetCmdSetNodeKeys, args: []string{secp.String(), ed.String(), consPk},
			typ: "/types.MsgSetNodeKeys", expect: map[string]any{"validator_cons_pub_key": consPk},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := runCmdCov(t, tc.cmd(), ctx, tc.args...)
			require.NoError(t, err)
			msg := generatedMsgCov(t, out)
			require.Equal(t, tc.typ, msg["@type"])
			for k, v := range tc.expect {
				require.Equal(t, v, msg[k], k)
			}
		})
	}
}

func TestTxCommandsGeneratedDetailsCov(t *testing.T) {
	from := types.GetRandomBech32Addr()
	ctx := txClientCtxCov(from, nil)

	out, _, err := runCmdCov(t, GetCmdDeposit(), ctx, "100", "THOR.RUNE", "memo")
	require.NoError(t, err)
	coins := asSliceCov(t, generatedMsgCov(t, out)["coins"])
	require.Len(t, coins, 1)
	coin := asMapCov(t, coins[0])
	require.Equal(t, "100", coin["amount"])
	require.Equal(t, "THOR.RUNE", coin["asset"])

	out, _, err = runCmdCov(t, GetCmdProposeUpgrade(), ctx, "1.2.3", "100", "--info", "hello")
	require.NoError(t, err)
	upgrade := asMapCov(t, generatedMsgCov(t, out)["upgrade"])
	require.Equal(t, "100", upgrade["height"])
	require.Equal(t, "hello", upgrade["info"])

	secp := types.GetRandomPubKey()
	ed := types.GetRandomEd25519PubKey()
	consPk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeConsPub, sdked25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	out, _, err = runCmdCov(t, GetCmdSetNodeKeys(), ctx, secp.String(), ed.String(), consPk)
	require.NoError(t, err)
	pks := asMapCov(t, generatedMsgCov(t, out)["pub_key_set_set"])
	require.Equal(t, secp.String(), pks["secp256k1"])
	require.Equal(t, ed.String(), pks["ed25519"])
}

func TestTxCommandsArgErrorsCov(t *testing.T) {
	from := types.GetRandomBech32Addr()
	ctx := txClientCtxCov(from, nil)
	secp := types.GetRandomPubKey().String()
	ed := types.GetRandomEd25519PubKey().String()

	testCases := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		err  string
	}{
		{"deposit bad amount", GetCmdDeposit, []string{"abc", "THOR.RUNE", "m"}, "invalid amount"},
		{"deposit bad asset", GetCmdDeposit, []string{"1", "B1C.BTC", "m"}, "invalid asset"},
		{"deposit non native", GetCmdDeposit, []string{"1", "BTC.BTC", "m"}, "all coins must be native"},
		{"deposit wrong args", GetCmdDeposit, []string{"1"}, "accepts 3 arg(s)"},
		{"send bad addr", GetCmdSend, []string{"bogus", "1rune"}, "invalid address"},
		{"send bad coins", GetCmdSend, []string{from.String(), "!!"}, "invalid coins"},
		{"mimir bad value", GetCmdMimir, []string{"k", "x"}, "invalid value"},
		{"mimir empty key", GetCmdMimir, []string{"", "1"}, "key cannot be empty"},
		{"ban bad addr", GetCmdBan, []string{"bogus"}, "invalid node address"},
		{"propose bad height", GetCmdProposeUpgrade, []string{"1.2.3", "x"}, "failed to parse height"},
		{"propose bad name", GetCmdProposeUpgrade, []string{"notsemver", "10"}, "name is not a valid semver"},
		{"approve empty", GetCmdApproveUpgrade, []string{""}, "name cannot be empty"},
		{"reject empty", GetCmdRejectUpgrade, []string{""}, "name cannot be empty"},
		{"node keys bad secp", GetCmdSetNodeKeys, []string{"bad", ed, "bad"}, "fail to parse secp256k1 pub key"},
		{"node keys bad ed", GetCmdSetNodeKeys, []string{secp, "bad", "bad"}, "fail to parse ed25519 pub key"},
		{"node keys bad cons", GetCmdSetNodeKeys, []string{secp, ed, "bad"}, "fail to parse validator consensus public key"},
		{"pause extra args", GetCmdNodePauseChain, []string{"x"}, "accepts 0 arg(s)"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := runCmdCov(t, tc.cmd(), ctx, tc.args...)
			require.ErrorContains(t, err, tc.err)
			require.Empty(t, out)
		})
	}
}

// Without a --from address every command must fail validation before
// generating a tx.
func TestTxCommandsRequireSignerCov(t *testing.T) {
	ctx := txClientCtxCov(nil, nil)
	to := types.GetRandomBech32Addr()
	secp := types.GetRandomPubKey().String()
	ed := types.GetRandomEd25519PubKey().String()
	consPk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeConsPub, sdked25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	testCases := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{"mimir", GetCmdMimir, []string{"k", "1"}},
		{"deposit", GetCmdDeposit, []string{"1", "THOR.RUNE", "m"}},
		{"send", GetCmdSend, []string{to.String(), "1rune"}},
		{"pause", GetCmdNodePauseChain, nil},
		{"resume", GetCmdNodeResumeChain, nil},
		{"ban", GetCmdBan, []string{to.String()}},
		{"ip", GetCmdSetIPAddress, []string{"1.2.3.4"}},
		{"version", GetCmdSetVersion, nil},
		{"propose", GetCmdProposeUpgrade, []string{"1.2.3", "10"}},
		{"approve", GetCmdApproveUpgrade, []string{"1.2.3"}},
		{"reject", GetCmdRejectUpgrade, []string{"1.2.3"}},
		{"node keys", GetCmdSetNodeKeys, []string{secp, ed, consPk}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := runCmdCov(t, tc.cmd(), ctx, tc.args...)
			require.ErrorContains(t, err, "invalid address")
			require.Empty(t, out)
		})
	}
}

// An unparsable --node flag fails client context creation for every command.
func TestTxCommandsClientContextErrorCov(t *testing.T) {
	ctx := txClientCtxCov(types.GetRandomBech32Addr(), nil)
	cmds := []func() *cobra.Command{
		GetCmdMimir, GetCmdDeposit, GetCmdSend, GetCmdNodePauseChain, GetCmdNodeResumeChain,
		GetCmdBan, GetCmdSetIPAddress, GetCmdSetVersion, GetCmdProposeUpgrade,
		GetCmdApproveUpgrade, GetCmdRejectUpgrade, GetCmdSetNodeKeys, GetCmdObserveTxIns,
	}
	for _, mk := range cmds {
		cmd := mk()
		// supply the right number of positional args for each command
		nargs := map[string]int{
			"mimir": 2, "deposit": 3, "send": 2, "ban": 1, "set-ip-address": 1,
			"propose-upgrade": 2, "approve-upgrade": 1, "reject-upgrade": 1, "set-node-keys": 3,
		}[cmd.Name()]
		args := make([]string, 0, nargs+2)
		for i := 0; i < nargs; i++ {
			args = append(args, "1")
		}
		args = append(args, "--node", "tcp://bad\x7fhost:1")
		_, _, err := runCmdCov(t, cmd, ctx, args...)
		require.Error(t, err, cmd.Name())
	}
}

// ------------------------------ observations ------------------------------

func sampleObservedTxCov(t *testing.T) common.ObservedTx {
	t.Helper()
	// the vault address is both sender and receiver so the observation is
	// valid as both an inbound and an outbound
	pk, vault := vaultCov()
	tx := common.NewTx(
		types.GetRandomTxHash(),
		vault,
		vault,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(10))},
		"memo",
	)
	return common.NewObservedTx(tx, 10, pk, 12)
}

func vaultCov() (common.PubKey, common.Address) {
	pk := types.GetRandomPubKey()
	addr, err := pk.GetAddress(common.BTCChain)
	if err != nil {
		panic(err)
	}
	return pk, addr
}

func TestObserveTxsRawObservationsCov(t *testing.T) {
	from := types.GetRandomBech32Addr()
	ctx := txClientCtxCov(from, nil)
	obs := []common.ObservedTx{sampleObservedTxCov(t)}
	raw, err := json.Marshal(obs)
	require.NoError(t, err)

	for _, tc := range []struct {
		cmd func() *cobra.Command
		typ string
	}{
		{GetCmdObserveTxIns, "/types.MsgObservedTxIn"},
		{GetCmdObserveTxOuts, "/types.MsgObservedTxOut"},
	} {
		out, stdout, err := runCmdCov(t, tc.cmd(), ctx, "--raw-observations", string(raw))
		require.NoError(t, err)
		msg := generatedMsgCov(t, out)
		require.Equal(t, tc.typ, msg["@type"])
		txs := asSliceCov(t, msg["txs"])
		require.Len(t, txs, 1)
		inner := asMapCov(t, asMapCov(t, txs[0])["tx"])
		require.Equal(t, obs[0].Tx.ID.String(), inner["id"])
		// the message is also echoed to the command output
		require.Contains(t, stdout, obs[0].Tx.ID.String())
	}
}

func TestObserveTxsFlagValidationCov(t *testing.T) {
	from := types.GetRandomBech32Addr()
	ctx := txClientCtxCov(from, nil)

	_, _, err := runCmdCov(t, GetCmdObserveTxIns(), ctx)
	require.ErrorContains(t, err, "either --txids or --raw-observations flag must be set")

	_, _, err = runCmdCov(t, GetCmdObserveTxIns(), ctx, "--txids", "a", "--raw-observations", "[]")
	require.ErrorContains(t, err, "only one of --txids or --raw-observations flag can be set")

	_, _, err = runCmdCov(t, GetCmdObserveTxIns(), ctx, "--txids", "a")
	require.ErrorContains(t, err, "--txids requires --thornode-api")

	_, _, err = runCmdCov(t, GetCmdObserveTxIns(), txClientCtxCov(nil, nil), "--raw-observations", "[]")
	require.ErrorContains(t, err, "--from must be set")

	_, _, err = runCmdCov(t, GetCmdObserveTxOuts(), ctx, "--raw-observations", "{bad")
	require.ErrorContains(t, err, "failed to parse raw observations")

	_, _, err = runCmdCov(t, GetCmdObserveTxOuts(), ctx, "x")
	require.ErrorContains(t, err, "accepts 0 arg(s)")

	// too many observations
	many := make([]common.ObservedTx, 51)
	for i := range many {
		many[i] = sampleObservedTxCov(t)
	}
	raw, err := json.Marshal(many)
	require.NoError(t, err)
	out, _, err := runCmdCov(t, GetCmdObserveTxIns(), ctx, "--raw-observations", string(raw))
	require.ErrorContains(t, err, "cannot observe more than 50 transactions at once")
	require.Empty(t, out)

	// empty list is a no-op
	stdout := captureStdoutCov(t, func() {
		out, _, err = runCmdCov(t, GetCmdObserveTxIns(), ctx, "--raw-observations", "[]")
	})
	require.NoError(t, err)
	require.Empty(t, out)
	require.Contains(t, stdout, "node has broadcast all observation versions")
}

func strPtrCov(s string) *string { return &s }
func i64PtrCov(i int64) *int64   { return &i }

func openapiObservedTxCov(signers ...string) openapi.ObservedTx {
	pk, vault := vaultCov()
	return openapi.ObservedTx{
		Tx: openapi.Tx{
			Id:          strPtrCov(types.GetRandomTxHash().String()),
			Chain:       strPtrCov("BTC"),
			FromAddress: strPtrCov(vault.String()),
			ToAddress:   strPtrCov(vault.String()),
			Coins:       []openapi.Coin{{Asset: "BTC.BTC", Amount: "1000", Decimals: i64PtrCov(8)}},
			Gas:         []openapi.Coin{{Asset: "BTC.BTC", Amount: "10", Decimals: i64PtrCov(8)}},
			Memo:        strPtrCov("=:THOR.RUNE:thor1xyz"),
		},
		ObservedPubKey:                  strPtrCov(pk.String()),
		ExternalObservedHeight:          i64PtrCov(100),
		ExternalConfirmationDelayHeight: i64PtrCov(105),
		Aggregator:                      strPtrCov("0xagg"),
		AggregatorTarget:                strPtrCov("0xtarget"),
		AggregatorTargetLimit:           strPtrCov("42"),
		Signers:                         signers,
	}
}

func TestExtractOpenAPIObservedTxCov(t *testing.T) {
	otx := openapiObservedTxCov()
	tx, err := extractOpenAPIObservedTx(otx)
	require.NoError(t, err)
	require.Equal(t, *otx.Tx.Id, tx.Tx.ID.String())
	require.Equal(t, common.BTCChain, tx.Tx.Chain)
	require.Equal(t, *otx.Tx.FromAddress, tx.Tx.FromAddress.String())
	require.Equal(t, *otx.Tx.ToAddress, tx.Tx.ToAddress.String())
	require.Len(t, tx.Tx.Coins, 1)
	require.True(t, tx.Tx.Coins[0].Asset.Equals(common.BTCAsset))
	require.Equal(t, uint64(1000), tx.Tx.Coins[0].Amount.Uint64())
	require.Equal(t, int64(8), tx.Tx.Coins[0].Decimals)
	require.Equal(t, uint64(10), tx.Tx.Gas[0].Amount.Uint64())
	require.Equal(t, int64(8), tx.Tx.Gas[0].Decimals)
	require.Equal(t, "=:THOR.RUNE:thor1xyz", tx.Tx.Memo)
	require.Equal(t, "0xagg", tx.Aggregator)
	require.Equal(t, "0xtarget", tx.AggregatorTarget)
	require.Equal(t, uint64(42), tx.AggregatorTargetLimit.Uint64())
	require.Equal(t, int64(100), tx.BlockHeight)
	require.Equal(t, int64(105), tx.FinaliseHeight)
	require.Equal(t, *otx.ObservedPubKey, tx.ObservedPubKey.String())

	// optional aggregator fields may be absent
	otx.Aggregator, otx.AggregatorTarget, otx.AggregatorTargetLimit = nil, nil, nil
	otx.Tx.Coins[0].Decimals = nil
	tx, err = extractOpenAPIObservedTx(otx)
	require.NoError(t, err)
	require.Empty(t, tx.Aggregator)
	require.Nil(t, tx.AggregatorTargetLimit)
	require.Equal(t, int64(0), tx.Tx.Coins[0].Decimals)

	errCases := []struct {
		name   string
		mutate func(o *openapi.ObservedTx)
		err    string
	}{
		{"tx id", func(o *openapi.ObservedTx) { o.Tx.Id = strPtrCov("") }, "failed to parse tx id"},
		{"chain", func(o *openapi.ObservedTx) { o.Tx.Chain = strPtrCov("NOTACHAIN!") }, "failed to parse chain"},
		{"from", func(o *openapi.ObservedTx) { o.Tx.FromAddress = strPtrCov("bad address") }, "failed to parse from address"},
		{"to", func(o *openapi.ObservedTx) { o.Tx.ToAddress = strPtrCov("bad address") }, "failed to parse to address"},
		{"coin", func(o *openapi.ObservedTx) { o.Tx.Coins[0].Asset = "A.B.C.D" }, "failed to parse coin asset"},
		{"gas", func(o *openapi.ObservedTx) { o.Tx.Gas[0].Asset = "A.B.C.D" }, "failed to parse gas asset"},
		{"pubkey", func(o *openapi.ObservedTx) { o.ObservedPubKey = strPtrCov("bad") }, "failed to parse observed public key"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			o := openapiObservedTxCov()
			tc.mutate(&o)
			_, err := extractOpenAPIObservedTx(o)
			require.ErrorContains(t, err, tc.err)
		})
	}
}

// txDetailsServerCov serves canned /thorchain/tx/details responses.
func txDetailsServerCov(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		txid := strings.TrimPrefix(r.URL.Path, "/thorchain/tx/details/")
		body, ok := responses[txid]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func detailsJSONCov(t *testing.T, txs ...openapi.ObservedTx) string {
	t.Helper()
	bz, err := json.Marshal(openapi.TxDetailsResponse{Txs: txs})
	require.NoError(t, err)
	return string(bz)
}

func TestFindLackingObservationCov(t *testing.T) {
	node := types.GetRandomBech32Addr().String()
	other1 := types.GetRandomBech32Addr().String()
	other2 := types.GetRandomBech32Addr().String()

	signedByUs := openapiObservedTxCov(node, other1)
	lowCount := openapiObservedTxCov(other1)
	highCount := openapiObservedTxCov(other1, other2)
	bad := openapiObservedTxCov(other1, other2, "x")
	bad.ObservedPubKey = strPtrCov("bad")

	srv := txDetailsServerCov(t, map[string]string{
		"pick":    detailsJSONCov(t, signedByUs, lowCount, highCount),
		"allmine": detailsJSONCov(t, signedByUs),
		"invalid": "{not json",
		"badtx":   detailsJSONCov(t, bad),
	})

	var obs *common.ObservedTx
	var err error
	stdout := captureStdoutCov(t, func() {
		obs, err = findLackingObservation("pick", node, srv.URL)
	})
	require.NoError(t, err)
	require.NotNil(t, obs)
	// the highest signer count observation we have not signed is chosen
	require.Equal(t, *highCount.Tx.Id, obs.Tx.ID.String())
	short := node[len(node)-4:]
	require.Contains(t, stdout, fmt.Sprintf("[pick] %s observed tx with 2 others", short))
	require.Contains(t, stdout, fmt.Sprintf("[pick] %s will observe tx with 2 signers", short))

	captureStdoutCov(t, func() {
		obs, err = findLackingObservation("allmine", node, srv.URL)
	})
	require.NoError(t, err)
	require.Nil(t, obs)

	_, err = findLackingObservation("invalid", node, srv.URL)
	require.ErrorContains(t, err, "failed to parse tx details")

	_, err = findLackingObservation("badtx", node, srv.URL)
	require.ErrorContains(t, err, "failed to extract observed tx")

	// unreachable api
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	_, err = findLackingObservation("pick", node, closed.URL)
	require.ErrorContains(t, err, "failed to get tx details")
}

func TestObserveTxsWithTxIDsCov(t *testing.T) {
	from := types.GetRandomBech32Addr()
	ctx := txClientCtxCov(from, nil)
	other := types.GetRandomBech32Addr().String()

	want := openapiObservedTxCov(other)
	srv := txDetailsServerCov(t, map[string]string{
		"lacking": detailsJSONCov(t, want),
		"done":    detailsJSONCov(t, openapiObservedTxCov(from.String())),
		"invalid": "{",
	})

	var out string
	var err error
	captureStdoutCov(t, func() {
		out, _, err = runCmdCov(t, GetCmdObserveTxOuts(), ctx, "--txids", "done,lacking", "--thornode-api", srv.URL)
	})
	require.NoError(t, err)
	msg := generatedMsgCov(t, out)
	require.Equal(t, "/types.MsgObservedTxOut", msg["@type"])
	txs := asSliceCov(t, msg["txs"])
	require.Len(t, txs, 1)
	require.Equal(t, *want.Tx.Id, asMapCov(t, asMapCov(t, txs[0])["tx"])["id"])

	// all already observed: nothing to do
	stdout := captureStdoutCov(t, func() {
		out, _, err = runCmdCov(t, GetCmdObserveTxIns(), ctx, "--txids", "done", "--thornode-api", srv.URL)
	})
	require.NoError(t, err)
	require.Empty(t, out)
	require.Contains(t, stdout, "node has broadcast all observation versions")

	_, _, err = runCmdCov(t, GetCmdObserveTxIns(), ctx, "--txids", "invalid", "--thornode-api", srv.URL)
	require.ErrorContains(t, err, "failed to find lacking observation")
}

// captureStdoutCov redirects os.Stdout while fn runs and returns what was
// written.
func captureStdoutCov(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		bz, _ := io.ReadAll(r)
		done <- string(bz)
	}()
	defer func() {
		os.Stdout = orig
	}()
	fn()
	require.NoError(t, w.Close())
	os.Stdout = orig
	return <-done
}

func asMapCov(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "expected object, got %T", v)
	return m
}

func asSliceCov(t *testing.T, v any) []any {
	t.Helper()
	s, ok := v.([]any)
	require.True(t, ok, "expected array, got %T", v)
	return s
}
