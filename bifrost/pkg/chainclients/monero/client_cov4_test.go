package monero

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestKeygenFailureModesClientCov(t *testing.T) {
	t.Parallel()

	frostVault, expectedAddress := testVaultPubKeyAndAddress(t)
	groupKey, err := groupKeyHexFromVaultPubKey(frostVault)
	require.NoError(t, err)
	_, otherAddress := testVaultPubKeyAndAddress(t)
	good := tssp.FrostKeygenResponse{
		Address: expectedAddress.String(), GroupKey: groupKey, KeyBytesHex: "cafebabe", Status: tsscommon.Success,
	}
	req := chainclienttypes.CustomKeygenRequest{Members: []string{"member-a"}, BlockHeight: 7}

	_, err = (&Client{}).Keygen(req)
	require.ErrorIs(t, err, ErrSidecarNotConfigured)

	frostSigner := newTestSidecarClient(t)
	_, err = (&Client{sidecar: frostSigner}).Keygen(req)
	require.ErrorContains(t, err, "tss server is not initialized")

	newClient := func(resp tssp.FrostKeygenResponse, tssErr error) *Client {
		return &Client{
			chain:           common.XMRChain,
			logger:          log.Logger,
			sidecar:         frostSigner,
			tssServer:       &mockFrostKeygenTSS{resp: resp, err: tssErr},
			vaultAddressMap: make(map[string]common.Address),
		}
	}

	_, err = newClient(good, nil).Keygen(chainclienttypes.CustomKeygenRequest{Members: []string{" ", ""}})
	require.ErrorContains(t, err, "non-empty member set")

	_, err = newClient(good, errors.New("dkg down")).Keygen(req)
	require.ErrorContains(t, err, "fail to execute FROST keygen")

	failed := tssp.FrostKeygenResponse{
		Status: tsscommon.Fail,
		Blame:  blame.Blame{FailReason: "timeout", BlameNodes: []blame.Node{{Pubkey: "member-a"}}},
	}
	result, err := newClient(failed, nil).Keygen(req)
	require.ErrorContains(t, err, "FROST keygen failed: timeout")
	require.Len(t, result.Blame, 1)
	require.Equal(t, "timeout", result.Blame[0].FailReason)

	result, err = newClient(tssp.FrostKeygenResponse{Status: tsscommon.Fail}, nil).Keygen(req)
	require.Error(t, err)
	require.Empty(t, result.Blame, "no blame when the failure carries no reason or nodes")

	cases := map[string]struct {
		mutate func(r *tssp.FrostKeygenResponse)
		want   string
	}{
		"no-address":     {func(r *tssp.FrostKeygenResponse) { r.Address = " " }, "without an address"},
		"no-group-key":   {func(r *tssp.FrostKeygenResponse) { r.GroupKey = "" }, "without a group key"},
		"bad-group-key":  {func(r *tssp.FrostKeygenResponse) { r.GroupKey = "zz" }, "fail to convert FROST group key"},
		"wrong-address":  {func(r *tssp.FrostKeygenResponse) { r.Address = otherAddress.String() }, "but expected derived vault address"},
		"no-keyshare":    {func(r *tssp.FrostKeygenResponse) { r.KeyBytesHex = "" }, "empty recovery keyshare"},
		"bad-keyshare":   {func(r *tssp.FrostKeygenResponse) { r.KeyBytesHex = "zz" }, "invalid recovery keyshare hex"},
		"invalid-format": {func(r *tssp.FrostKeygenResponse) { r.Address = "not an address" }, "invalid FROST keygen address"},
	}
	for name, tc := range cases {
		resp := good
		tc.mutate(&resp)
		_, err = newClient(resp, nil).Keygen(req)
		require.ErrorContains(t, err, tc.want, name)
	}

	// Registration failure.
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	unreachable, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := newClient(good, nil)
	c.sidecar = unreachable
	_, err = c.Keygen(req)
	require.ErrorContains(t, err, "fail to register FROST vault mapping")

	result, err = newClient(good, nil).Keygen(req)
	require.NoError(t, err)
	require.True(t, result.PoolPubKeyFrost.Equals(frostVault))
}

func TestReadRecoveryFileClientCov(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := readRecoveryFile(filepath.Join(dir, "missing"))
	require.ErrorContains(t, err, "lstat recovery file")

	// A directory is rejected. Which guard fires depends on the filesystem's
	// directory link count: ext4/tmpfs/overlayfs report several links (the
	// hard-link guard), btrfs reports one (the regular-file check).
	_, err = readRecoveryFile(dir)
	require.Error(t, err)
	require.Truef(t,
		strings.Contains(err.Error(), "unexpected hard links") || strings.Contains(err.Error(), "not a regular file"),
		"unexpected error for a directory: %v", err)

	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	_, err = readRecoveryFile(empty)
	require.ErrorContains(t, err, "outside 1..")

	good := filepath.Join(dir, "good")
	require.NoError(t, os.WriteFile(good, []byte("blob"), 0o600))
	got, err := readRecoveryFile(good)
	require.NoError(t, err)
	require.Equal(t, []byte("blob"), got)

	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(good, link))
	_, err = readRecoveryFile(link)
	require.ErrorContains(t, err, "not a regular file")

	// A crash-interrupted publish leaves a tool-owned .tmp- alias of the same
	// inode; reading repairs it.
	aliased := filepath.Join(dir, "aliased")
	require.NoError(t, os.WriteFile(aliased, []byte("aliased"), 0o600))
	require.NoError(t, os.Link(aliased, aliased+".tmp-1"))
	// An unrelated .tmp- file with a different inode is ignored.
	require.NoError(t, os.WriteFile(aliased+".tmp-2", []byte("other"), 0o600))
	got, err = readRecoveryFile(aliased)
	require.NoError(t, err)
	require.Equal(t, []byte("aliased"), got)
	_, err = os.Lstat(aliased + ".tmp-1")
	require.True(t, os.IsNotExist(err))
	_, err = os.Lstat(aliased + ".tmp-2")
	require.NoError(t, err)

	// An unrelated hard link fails closed.
	linked := filepath.Join(dir, "linked")
	require.NoError(t, os.WriteFile(linked, []byte("linked"), 0o600))
	require.NoError(t, os.Link(linked, filepath.Join(dir, "elsewhere")))
	_, err = readRecoveryFile(linked)
	require.ErrorContains(t, err, "unexpected hard links")

	require.Equal(t, time.Second, recoveryBackoff(0, 1))
	require.Equal(t, 4*time.Second, recoveryBackoff(time.Second, 3))
	require.Equal(t, 64*time.Second, recoveryBackoff(time.Second, 20))
	require.Equal(t, 5*time.Minute, recoveryBackoff(time.Minute, 10))

	require.Error(t, syncDirectory(filepath.Join(dir, "missing-dir")))
}

func TestQuarantinePermanentErrataClientCov(t *testing.T) {
	t.Parallel()

	txID := common.TxID(strings.ToUpper(testXMRHash("errata")))
	errata := common.ErrataTx{Chain: common.XMRChain, Id: txID}

	c := &Client{}
	require.ErrorContains(t, c.QuarantinePermanentErrata(common.ErrataTx{Chain: common.BTCChain, Id: txID}, 1), "invalid XMR permanent errata")
	require.ErrorContains(t, c.QuarantinePermanentErrata(errata, -1), "invalid XMR permanent errata height")
	require.ErrorContains(t, c.QuarantinePermanentErrata(errata, 1), "quarantine is unavailable")

	c.signedTxPayloadStore = newTestSignedTxPayloadStore(t)
	require.ErrorContains(t, c.QuarantinePermanentErrata(errata, 1), "scanner reverted transaction quarantine is unavailable")

	store := newScannerReceiptStore(t)
	c.scanner = &Scanner{}
	c.scanner.SetReorgStore(store)
	require.NoError(t, c.QuarantinePermanentErrata(errata, 5))
	reverted, err := store.IsRevertedTx(txID.String())
	require.NoError(t, err)
	require.True(t, reverted)

	closed := newTestSignedTxPayloadStore(t)
	require.NoError(t, closed.db.Close())
	c.signedTxPayloadStore = closed
	require.ErrorContains(t, c.QuarantinePermanentErrata(errata, 5), "quarantine permanently reverted XMR signed transaction")
}

func TestValidateXMRSpentRefsForSidecarClientCov(t *testing.T) {
	t.Parallel()

	vault, vaultAddr := testVaultPubKeyAndAddress(t)
	spend := testXMRHash("spend")
	base := func() common.ObservedTx {
		tx := common.NewObservedTx(
			common.NewTx(common.TxID(spend), vaultAddr, testOutboundXMRDestination(),
				common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
				common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}, "OUT:"+strings.Repeat("A", 64)),
			10, vault, 10,
		)
		tx.XmrTxKey = testXMRHash("txkey")
		tx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef("r1", 0, spend)}
		return tx
	}
	require.NoError(t, validateXMRSpentRefsForSidecar(base()))

	cases := map[string]struct {
		mutate func(tx *common.ObservedTx)
		want   string
	}{
		"no-refs":    {func(tx *common.ObservedTx) { tx.SpentOutputRefs = nil }, "spent refs are empty"},
		"bad-txid":   {func(tx *common.ObservedTx) { tx.Tx.ID = "abcd" }, "spend tx hash"},
		"no-key":     {func(tx *common.ObservedTx) { tx.XmrTxKey = " " }, "require outbound tx key"},
		"bad-key":    {func(tx *common.ObservedTx) { tx.XmrTxKey = "00" }, "xmr tx key"},
		"bad-to":     {func(tx *common.ObservedTx) { tx.Tx.ToAddress = "bc1qnotxmr" }, ""},
		"wrong-from": {func(tx *common.ObservedTx) { tx.Tx.FromAddress = testOutboundXMRDestination() }, "outbound from observed vault"},
		"ref-hash":   {func(tx *common.ObservedTx) { tx.SpentOutputRefs[0].TxHash = "x" }, "spent output tx hash"},
		"ref-ki":     {func(tx *common.ObservedTx) { tx.SpentOutputRefs[0].KeyImage = "x" }, "key image"},
		"ref-spend":  {func(tx *common.ObservedTx) { tx.SpentOutputRefs[0].SpendTxHash = "x" }, "spend tx hash"},
		"dup-ki": {func(tx *common.ObservedTx) {
			dup := tx.SpentOutputRefs[0]
			dup.OutputIndex = 5
			tx.SpentOutputRefs = append(tx.SpentOutputRefs, dup)
		}, "duplicate xmr spent ref key image"},
	}
	for name, tc := range cases {
		tx := base()
		tc.mutate(&tx)
		err := validateXMRSpentRefsForSidecar(tx)
		require.Error(t, err, name)
		if tc.want != "" {
			require.ErrorContains(t, err, tc.want, name)
		}
	}
}

func TestGetConfirmationCountClientCov(t *testing.T) {
	if notMocknetCov() {
		t.Skip("mocknet only: confirmation math uses mocknet XMR defaults")
	}
	t.Parallel()

	vault, vaultAddr := testVaultPubKeyAndAddress(t)
	bridge := &consolidationBridge{asgards: ttypes.Vaults{{PubKeyFrost: vault}, {PubKeyFrost: vault}, {}}}
	c := &Client{
		chain: common.XMRChain, logger: log.Logger, thorchainBridge: bridge,
		vaultAddressMap: map[string]common.Address{}, vaultAddrToKey: map[string]string{},
	}
	c.cfg.MinConfirmations = 50

	require.Zero(t, c.GetConfirmationCount(stypes.TxIn{}))
	require.Zero(t, c.GetConfirmationCount(stypes.TxIn{MemPool: true, TxArray: []*stypes.TxInItem{{}}}))

	txIn := stypes.TxIn{TxArray: []*stypes.TxInItem{{
		Sender: testOutboundXMRDestination().String(), To: vaultAddr.String(),
		Coins: common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
	}}}
	require.Equal(t, int64(50), c.GetConfirmationCount(txIn), "configured minimum floor wins")

	addrs, err := c.getAsgardAddress()
	require.NoError(t, err)
	require.Equal(t, []common.Address{vaultAddr}, addrs, "duplicates and empty vaults are dropped")
	// Cached on the second call.
	bridge.asgards = nil
	addrs, err = c.getAsgardAddress()
	require.NoError(t, err)
	require.Len(t, addrs, 1)

	c.cfg.MinConfirmations = 0
	require.GreaterOrEqual(t, c.GetConfirmationCount(txIn), int64(xmrSpendableConfirmations))

	// Bridge errors fall back to the cached addresses.
	errBridge := &errConsolidationBridgeClientCov{consolidationBridge: bridge, asgardErr: errors.New("down"), mimirErr: errors.New("down")}
	c.thorchainBridge = errBridge
	c.lastAsgard = time.Time{}
	_, err = c.getAsgardAddress()
	require.ErrorContains(t, err, "fail to get asgards")
	require.GreaterOrEqual(t, c.GetConfirmationCount(txIn), int64(xmrSpendableConfirmations))
}

func TestLoadVaultAddressMapClientCov(t *testing.T) {
	t.Parallel()

	require.NoError(t, (&Client{}).loadVaultAddressMap())

	store := newTestVaultAddressStore(t)
	good, goodAddr := testVaultPubKeyAndAddress(t)
	mismatch, _ := testVaultPubKeyAndAddress(t)
	_, otherAddr := testVaultPubKeyAndAddress(t)
	require.NoError(t, store.Set(good.String(), goodAddr.String()))
	require.NoError(t, store.Set("not-a-pubkey", goodAddr.String()))
	require.NoError(t, store.Set(mismatch.String(), otherAddr.String()))
	require.NoError(t, store.Set(testVaultPubKey(t, "bad-address").String(), "!!"))

	c := &Client{
		chain: common.XMRChain, logger: log.Logger, vaultAddressStore: store,
		vaultAddressMap: map[string]common.Address{}, vaultAddrToKey: map[string]string{},
	}
	require.NoError(t, c.loadVaultAddressMap())
	require.Equal(t, map[string]common.Address{good.String(): goodAddr}, c.vaultAddressMap)

	require.NoError(t, store.db.Close())
	require.Error(t, c.loadVaultAddressMap())
}
