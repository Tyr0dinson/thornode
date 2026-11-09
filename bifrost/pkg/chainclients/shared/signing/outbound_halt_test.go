package signing_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// fakeOutboundBridge adds the Asgard lookup the outbound halt needs.
type fakeOutboundBridge struct {
	fakeBridge
	vaults      ttypes.Vaults
	asgardErr   error
	asgardCalls int
}

func (b *fakeOutboundBridge) GetAsgards() (ttypes.Vaults, error) {
	b.asgardCalls++
	if b.asgardErr != nil {
		return nil, b.asgardErr
	}
	return b.vaults, nil
}

// fakeOutboundHeightBridge also serves the Asgard set as of a height, like the
// production bridge.
type fakeOutboundHeightBridge struct {
	fakeOutboundBridge
	atHeight    map[int64]ttypes.Vaults
	atHeightErr error
}

func (b *fakeOutboundHeightBridge) GetAsgardsAtHeight(height int64) (ttypes.Vaults, error) {
	if b.atHeightErr != nil {
		return nil, b.atHeightErr
	}
	return b.atHeight[height], nil
}

func newVault(t *testing.T, status ttypes.VaultStatus, vaultType ttypes.VaultType) ttypes.Vault {
	t.Helper()
	v := ttypes.NewVaultV2(1, status, vaultType, ttypes.GetRandomPubKey(),
		common.Chains{common.BTCChain, common.ETHChain, common.SOLChain, common.XMRChain}.Strings(),
		nil, ttypes.GetRandomEd25519PubKey())
	frost := ttypes.GetRandomEd25519PubKey()
	v.PubKeyFrost = frost
	return v
}

func vaultAddr(t *testing.T, v ttypes.Vault, chain common.Chain) common.Address {
	t.Helper()
	addr, err := v.GetAddress(chain)
	if err != nil || addr.IsEmpty() {
		t.Fatalf("vault address for %s: %q %v", chain, addr, err)
	}
	return addr
}

func migrateMemo() string     { return mem.NewMigrateMemo(10).String() }
func consolidateMemo() string { return mem.NewConsolidateMemo().String() }

// ----------------------------------------------------------------------------
// CheckOutboundHalt
// ----------------------------------------------------------------------------

func TestCheckOutboundHalt(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mimirs         map[string]int64
		chain          common.Chain
		halted, global bool
	}{
		{"unset", map[string]int64{}, common.BTCChain, false, false},
		{"global active", map[string]int64{constants.MimirKeyHaltOutbound: 50}, common.BTCChain, true, true},
		{"global at height", map[string]int64{constants.MimirKeyHaltOutbound: 100}, common.BTCChain, true, true},
		{"global future", map[string]int64{constants.MimirKeyHaltOutbound: 101}, common.BTCChain, false, false},
		{"chain active", map[string]int64{"HaltOutboundBTC": 1}, common.BTCChain, true, false},
		{"chain future", map[string]int64{"HaltOutboundBTC": 101}, common.BTCChain, false, false},
		{"other chain", map[string]int64{"HaltOutboundBTC": 1}, common.ETHChain, false, false},
		{"zero is unset", map[string]int64{constants.MimirKeyHaltOutbound: 0, "HaltOutboundBTC": 0}, common.BTCChain, false, false},
		{"negative is unset", map[string]int64{constants.MimirKeyHaltOutbound: -1, "HaltOutboundBTC": -1}, common.BTCChain, false, false},
		// HaltSigning is a different switch; it must not read as an outbound halt.
		{"signing halt is separate", map[string]int64{"HALTSIGNING": 1, "HaltSigningBTC": 1}, common.BTCChain, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeBridge{mimirs: tc.mimirs}
			halted, global, err := signing.CheckOutboundHalt(b, tc.chain, 100)
			if err != nil || halted != tc.halted || global != tc.global {
				t.Fatalf("halted=%v global=%v err=%v, want halted=%v global=%v", halted, global, err, tc.halted, tc.global)
			}
		})
	}
}

func TestCheckOutboundHaltMimirError(t *testing.T) {
	b := &fakeBridge{mimirErr: errors.New("boom")}
	halted, _, err := signing.CheckOutboundHalt(b, common.BTCChain, 100)
	if err == nil || halted {
		t.Fatalf("want error and not halted, got halted=%v err=%v", halted, err)
	}
}

// The global key is read first and short-circuits, like CheckSigningHalt.
func TestCheckOutboundHaltChainMimirError(t *testing.T) {
	b := &keyErrBridge{fakeBridge: fakeBridge{mimirs: map[string]int64{}}, errKey: "HaltOutboundBTC"}
	if _, _, err := signing.CheckOutboundHalt(b, common.BTCChain, 100); err == nil {
		t.Fatal("want error from the per-chain read")
	}
	b.mimirs[constants.MimirKeyHaltOutbound] = 1
	halted, global, err := signing.CheckOutboundHalt(b, common.BTCChain, 100)
	if err != nil || !halted || !global {
		t.Fatalf("global halt must short-circuit the failing per-chain read: halted=%v global=%v err=%v", halted, global, err)
	}
}

type keyErrBridge struct {
	fakeBridge
	errKey string
}

func (b *keyErrBridge) GetMimir(key string) (int64, error) {
	if key == b.errKey {
		return 0, errors.New("boom")
	}
	return b.fakeBridge.GetMimir(key)
}

// ----------------------------------------------------------------------------
// IsInternalTxOut: the internal/external classification
// ----------------------------------------------------------------------------

func TestIsInternalTxOut(t *testing.T) {
	active := newVault(t, ttypes.VaultStatus_ActiveVault, ttypes.VaultType_AsgardVault)
	retiring := newVault(t, ttypes.VaultStatus_RetiringVault, ttypes.VaultType_AsgardVault)
	inactive := newVault(t, ttypes.VaultStatus_InactiveVault, ttypes.VaultType_AsgardVault)
	initVault := newVault(t, ttypes.VaultStatus_InitVault, ttypes.VaultType_AsgardVault)
	unknownType := newVault(t, ttypes.VaultStatus_ActiveVault, ttypes.VaultType_UnknownVault)
	vaults := ttypes.Vaults{active, retiring, inactive, initVault, unknownType}

	activeBTC := vaultAddr(t, active, common.BTCChain)
	retiringBTC := vaultAddr(t, retiring, common.BTCChain)
	user := ttypes.GetRandomBTCAddress()
	outMemo := mem.NewOutboundMemo(common.TxID("A6F5B26F2F1B1A8BB39EF6DE0E1A6E1C5A3E1AEB8A84F9E8D24C0D9C3B6C3A1E")).String()
	refundMemo := mem.NewRefundMemo(common.TxID("A6F5B26F2F1B1A8BB39EF6DE0E1A6E1C5A3E1AEB8A84F9E8D24C0D9C3B6C3A1E")).String()
	ragnarokMemo := mem.NewRagnarokMemo(10).String()

	for _, tc := range []struct {
		name     string
		tx       stypes.TxOutItem
		internal bool
	}{
		{"migrate to active vault", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: migrateMemo()}, true},
		{"migrate to retiring vault", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: retiringBTC, Memo: migrateMemo()}, true},
		{"consolidate to self", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: consolidateMemo()}, true},
		{"memo case and space insensitive", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: "  migrate:10 "}, true},
		{"memoless migrate keeps original memo", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, OriginalMemo: migrateMemo()}, true},
		{"memo and original both internal", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: migrateMemo(), OriginalMemo: migrateMemo()}, true},
		{"address case insensitive on BTC", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: common.Address(upper(activeBTC.String())), Memo: migrateMemo()}, true},

		{"migrate to user address", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: user, Memo: migrateMemo()}, false},
		{"consolidate to user address", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: user, Memo: consolidateMemo()}, false},
		{"migrate to inactive vault", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: vaultAddr(t, inactive, common.BTCChain), Memo: migrateMemo()}, false},
		{"migrate to init vault", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: vaultAddr(t, initVault, common.BTCChain), Memo: migrateMemo()}, false},
		{"migrate to non-asgard vault", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: vaultAddr(t, unknownType, common.BTCChain), Memo: migrateMemo()}, false},
		{"outbound to vault address", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: outMemo}, false},
		{"refund to vault address", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: refundMemo}, false},
		{"ragnarok to vault address", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: ragnarokMemo}, false},
		{"outbound to user", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: user, Memo: outMemo}, false},
		{"empty memo to vault (EVM cancel shape)", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC}, false},
		{"unparsable memo to vault", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: "MIGRATE"}, false},
		{"garbage memo to vault", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: "hello"}, false},
		{"internal wire memo, external original", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: migrateMemo(), OriginalMemo: outMemo}, false},
		{"external wire memo, internal original", stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: outMemo, OriginalMemo: migrateMemo()}, false},
		{"empty to address", stypes.TxOutItem{Chain: common.BTCChain, Memo: migrateMemo()}, false},
		{"empty chain", stypes.TxOutItem{ToAddress: activeBTC, Memo: migrateMemo()}, false},
		{"vault address on another chain", stypes.TxOutItem{Chain: common.ETHChain, ToAddress: activeBTC, Memo: migrateMemo()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := signing.IsInternalTxOut(tc.tx, vaults); got != tc.internal {
				t.Fatalf("IsInternalTxOut = %v, want %v (tx %+v)", got, tc.internal, tc.tx)
			}
		})
	}

	if signing.IsInternalTxOut(stypes.TxOutItem{Chain: common.BTCChain, ToAddress: activeBTC, Memo: migrateMemo()}, nil) {
		t.Fatal("no vaults: nothing is internal")
	}
}

// Each chain derives its vault address from the key its signing algorithm uses
// (secp256k1, EdDSA, FROST), the same derivation THORChain's migrate uses.
func TestIsInternalTxOutPerAlgorithm(t *testing.T) {
	v := newVault(t, ttypes.VaultStatus_ActiveVault, ttypes.VaultType_AsgardVault)
	vaults := ttypes.Vaults{v}
	for _, chain := range []common.Chain{common.ETHChain, common.SOLChain, common.XMRChain} {
		addr := vaultAddr(t, v, chain)
		if !signing.IsInternalTxOut(stypes.TxOutItem{Chain: chain, ToAddress: addr, Memo: migrateMemo()}, vaults) {
			t.Errorf("%s: migrate to the vault address must be internal", chain)
		}
	}
	// A secp256k1 pubkey's SOL "address" is not the vault's SOL address.
	wrong, err := v.PubKey.GetAddress(common.SOLChain)
	if err == nil && !wrong.IsEmpty() &&
		signing.IsInternalTxOut(stypes.TxOutItem{Chain: common.SOLChain, ToAddress: wrong, Memo: migrateMemo()}, vaults) {
		t.Error("SOL address derived from the secp256k1 key must not count")
	}
	// Monero addresses are case-sensitive.
	xmr := vaultAddr(t, v, common.XMRChain)
	if signing.IsInternalTxOut(stypes.TxOutItem{Chain: common.XMRChain, ToAddress: common.Address(upper(xmr.String())), Memo: migrateMemo()}, vaults) {
		t.Error("XMR address match must be case-sensitive")
	}
}

func upper(s string) string { return string(bytes.ToUpper([]byte(s))) }

// ----------------------------------------------------------------------------
// OutboundHaltBlocks: the gate used by signer/sign.go
// ----------------------------------------------------------------------------

func TestOutboundHaltBlocks(t *testing.T) {
	active := newVault(t, ttypes.VaultStatus_ActiveVault, ttypes.VaultType_AsgardVault)
	internal := stypes.TxOutItem{Chain: common.BTCChain, ToAddress: vaultAddr(t, active, common.BTCChain), Memo: migrateMemo()}
	external := stypes.TxOutItem{Chain: common.BTCChain, ToAddress: ttypes.GetRandomBTCAddress(), Memo: "OUT:A6F5B26F2F1B1A8BB39EF6DE0E1A6E1C5A3E1AEB8A84F9E8D24C0D9C3B6C3A1E"}

	t.Run("no halt skips the vault read", func(t *testing.T) {
		b := &fakeOutboundBridge{fakeBridge: fakeBridge{mimirs: map[string]int64{}}, asgardErr: errors.New("must not be called")}
		blocked, _, err := signing.OutboundHaltBlocks(b, external, 100)
		if err != nil || blocked || b.asgardCalls != 0 {
			t.Fatalf("blocked=%v err=%v asgardCalls=%d", blocked, err, b.asgardCalls)
		}
	})
	for _, key := range []string{constants.MimirKeyHaltOutbound, "HaltOutboundBTC"} {
		t.Run(key, func(t *testing.T) {
			b := &fakeOutboundBridge{fakeBridge: fakeBridge{mimirs: map[string]int64{key: 1}}, vaults: ttypes.Vaults{active}}
			blocked, global, err := signing.OutboundHaltBlocks(b, internal, 100)
			if err != nil || blocked {
				t.Fatalf("internal must pass: blocked=%v err=%v", blocked, err)
			}
			blocked, global2, err := signing.OutboundHaltBlocks(b, external, 100)
			if err != nil || !blocked {
				t.Fatalf("external must be blocked: blocked=%v err=%v", blocked, err)
			}
			wantGlobal := key == constants.MimirKeyHaltOutbound
			if global != wantGlobal || global2 != wantGlobal {
				t.Fatalf("global=%v/%v, want %v", global, global2, wantGlobal)
			}
		})
	}
	t.Run("other chain halted", func(t *testing.T) {
		b := &fakeOutboundBridge{fakeBridge: fakeBridge{mimirs: map[string]int64{"HaltOutboundETH": 1}}, vaults: ttypes.Vaults{active}}
		blocked, _, err := signing.OutboundHaltBlocks(b, external, 100)
		if err != nil || blocked {
			t.Fatalf("HaltOutboundETH must not block BTC: blocked=%v err=%v", blocked, err)
		}
	})
	t.Run("vaults read at height, not from the live cache", func(t *testing.T) {
		// The live (cached) set predates the rotation and lacks the new vault.
		b := &fakeOutboundHeightBridge{
			fakeOutboundBridge: fakeOutboundBridge{fakeBridge: fakeBridge{mimirs: map[string]int64{"HaltOutboundBTC": 1}}},
			atHeight:           map[int64]ttypes.Vaults{100: {active}},
		}
		blocked, _, err := signing.OutboundHaltBlocks(b, internal, 100)
		if err != nil || blocked || b.asgardCalls != 0 {
			t.Fatalf("round-1 MIGRATE must pass: blocked=%v err=%v asgardCalls=%d", blocked, err, b.asgardCalls)
		}
		b.atHeightErr = errors.New("boom")
		if _, _, err := signing.OutboundHaltBlocks(b, internal, 100); err == nil {
			t.Fatal("want error so the caller does not sign")
		}
	})
	t.Run("vault read error fails closed", func(t *testing.T) {
		b := &fakeOutboundBridge{fakeBridge: fakeBridge{mimirs: map[string]int64{"HaltOutboundBTC": 1}}, asgardErr: errors.New("boom")}
		if _, _, err := signing.OutboundHaltBlocks(b, internal, 100); err == nil {
			t.Fatal("want error so the caller does not sign")
		}
	})
	t.Run("mimir error", func(t *testing.T) {
		b := &fakeOutboundBridge{fakeBridge: fakeBridge{mimirErr: errors.New("boom")}}
		if _, _, err := signing.OutboundHaltBlocks(b, internal, 100); err == nil {
			t.Fatal("want error so the caller does not sign")
		}
	})
}

// ----------------------------------------------------------------------------
// Retry-loop wrappers used by the bifrost-autonomous signing paths
// ----------------------------------------------------------------------------

func TestIsOutboundHalted(t *testing.T) {
	logger := zerolog.Nop()
	for _, tc := range []struct {
		name string
		b    *fakeBridge
		want bool
	}{
		{"not halted", &fakeBridge{height: 100, mimirs: map[string]int64{}}, false},
		{"global", &fakeBridge{height: 100, mimirs: map[string]int64{constants.MimirKeyHaltOutbound: 1}}, true},
		{"chain", &fakeBridge{height: 100, mimirs: map[string]int64{"HaltOutboundSOL": 1}}, true},
		{"future", &fakeBridge{height: 100, mimirs: map[string]int64{"HaltOutboundSOL": 101}}, false},
		{"height error fails closed", &fakeBridge{heightErr: errors.New("boom")}, true},
		{"mimir error fails closed", &fakeBridge{height: 100, mimirErr: errors.New("boom")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := signing.IsOutboundHalted(tc.b, common.SOLChain, logger); got != tc.want {
				t.Fatalf("IsOutboundHalted = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsTxOutBlockedByOutboundHalt(t *testing.T) {
	logger := zerolog.Nop()
	active := newVault(t, ttypes.VaultStatus_ActiveVault, ttypes.VaultType_AsgardVault)
	self := stypes.TxOutItem{Chain: common.BTCChain, ToAddress: vaultAddr(t, active, common.BTCChain), Memo: consolidateMemo()}
	away := stypes.TxOutItem{Chain: common.BTCChain, ToAddress: ttypes.GetRandomBTCAddress(), Memo: consolidateMemo()}
	halted := map[string]int64{"HaltOutboundBTC": 1}

	for _, tc := range []struct {
		name string
		b    *fakeOutboundBridge
		tx   stypes.TxOutItem
		want bool
	}{
		{"not halted", &fakeOutboundBridge{fakeBridge: fakeBridge{height: 100, mimirs: map[string]int64{}}}, away, false},
		{"halted, self consolidate", &fakeOutboundBridge{fakeBridge: fakeBridge{height: 100, mimirs: halted}, vaults: ttypes.Vaults{active}}, self, false},
		{"halted, consolidate elsewhere", &fakeOutboundBridge{fakeBridge: fakeBridge{height: 100, mimirs: halted}, vaults: ttypes.Vaults{active}}, away, true},
		{"height error fails closed", &fakeOutboundBridge{fakeBridge: fakeBridge{heightErr: errors.New("boom")}}, self, true},
		{"vault error fails closed", &fakeOutboundBridge{fakeBridge: fakeBridge{height: 100, mimirs: halted}, asgardErr: errors.New("boom")}, self, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := signing.IsTxOutBlockedByOutboundHalt(tc.b, tc.tx, logger); got != tc.want {
				t.Fatalf("IsTxOutBlockedByOutboundHalt = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsCancelBlockedByOutboundHalt(t *testing.T) {
	logger := zerolog.Nop()
	active := newVault(t, ttypes.VaultStatus_ActiveVault, ttypes.VaultType_AsgardVault)
	retiring := newVault(t, ttypes.VaultStatus_RetiringVault, ttypes.VaultType_AsgardVault)
	stuckMigrate := &stypes.TxOutItem{Chain: common.ETHChain, ToAddress: vaultAddr(t, active, common.ETHChain), Memo: migrateMemo()}
	stuckOut := &stypes.TxOutItem{Chain: common.ETHChain, ToAddress: ttypes.GetRandomETHAddress(), Memo: "OUT:A6F5B26F2F1B1A8BB39EF6DE0E1A6E1C5A3E1AEB8A84F9E8D24C0D9C3B6C3A1E"}
	stuckSelfSend := &stypes.TxOutItem{Chain: common.ETHChain, ToAddress: vaultAddr(t, retiring, common.ETHChain)}
	notHalted := func() *fakeBridge { return &fakeBridge{height: 100, mimirs: map[string]int64{}} }
	halted := func() *fakeBridge { return &fakeBridge{height: 100, mimirs: map[string]int64{"HaltOutboundETH": 1}} }
	haltedGlobal := func() *fakeBridge {
		return &fakeBridge{height: 100, mimirs: map[string]int64{constants.MimirKeyHaltOutbound: 1}}
	}
	mimirErr := func() *fakeBridge { return &fakeBridge{height: 100, mimirErr: errors.New("boom")} }
	heightErr := func() *fakeBridge { return &fakeBridge{heightErr: errors.New("boom")} }

	for _, tc := range []struct {
		name  string
		b     *fakeBridge
		chain common.Chain
		stuck *stypes.TxOutItem
		want  bool
	}{
		{"not halted, external", notHalted(), common.ETHChain, stuckOut, false},
		{"not halted, nil stuck item", notHalted(), common.ETHChain, nil, false},
		// A cancel is a zero-value self-send: with a real stuck item it is
		// allowed under the halt whatever that item pays.
		{"halted, stuck migration", halted(), common.ETHChain, stuckMigrate, false},
		{"halted, stuck external outbound", halted(), common.ETHChain, stuckOut, false},
		{"global halt, stuck external outbound", haltedGlobal(), common.ETHChain, stuckOut, false},
		{"halted, stuck self-send without memo", halted(), common.ETHChain, stuckSelfSend, false},
		{"read error, stuck external outbound", mimirErr(), common.ETHChain, stuckOut, false},
		// Without the item there is nothing to sign with: fail closed.
		{"halted, nil stuck item fails closed", halted(), common.ETHChain, nil, true},
		{"global halt, nil stuck item fails closed", haltedGlobal(), common.ETHChain, nil, true},
		{"mimir error, nil stuck item fails closed", mimirErr(), common.ETHChain, nil, true},
		{"height error, nil stuck item fails closed", heightErr(), common.ETHChain, nil, true},
		// The client's chain selects the halt key.
		{"other chain halted, nil stuck item", halted(), common.AVAXChain, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := signing.IsCancelBlockedByOutboundHalt(tc.b, tc.chain, tc.stuck, logger); got != tc.want {
				t.Fatalf("IsCancelBlockedByOutboundHalt = %v, want %v", got, tc.want)
			}
		})
	}
}

// fakeHeightBridge serves mimirs by consensus height and records the heights read.
type fakeHeightBridge struct {
	byHeight map[int64]map[string]int64
	err      error
	readAt   []int64
}

func (b *fakeHeightBridge) GetMimirAtHeight(key string, height int64) (int64, error) {
	b.readAt = append(b.readAt, height)
	if b.err != nil {
		return 0, b.err
	}
	if v, ok := b.byHeight[height][key]; ok {
		return v, nil
	}
	return -1, nil
}

func TestCheckOutboundHaltAtHeight(t *testing.T) {
	b := &fakeHeightBridge{byHeight: map[int64]map[string]int64{
		100: {"HaltOutboundSOL": 90},
		200: {constants.MimirKeyHaltOutbound: 150},
		300: {},
	}}
	for _, tc := range []struct {
		name           string
		chain          common.Chain
		height         int64
		halted, global bool
	}{
		{"chain halt at height", common.SOLChain, 100, true, false},
		{"other chain at same height", common.ETHChain, 100, false, false},
		{"global halt at height", common.SOLChain, 200, true, true},
		{"lifted at a later height", common.SOLChain, 300, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b.readAt = nil
			halted, global, err := signing.CheckOutboundHaltAtHeight(b, tc.chain, tc.height)
			if err != nil {
				t.Fatal(err)
			}
			if halted != tc.halted || global != tc.global {
				t.Fatalf("got halted=%v global=%v, want %v %v", halted, global, tc.halted, tc.global)
			}
			for _, h := range b.readAt {
				if h != tc.height {
					t.Fatalf("read at height %d, want %d", h, tc.height)
				}
			}
		})
	}

	// A value set for a later height than the one asked about is not active.
	future := &fakeHeightBridge{byHeight: map[int64]map[string]int64{100: {"HaltOutboundSOL": 101}}}
	if halted, _, err := signing.CheckOutboundHaltAtHeight(future, common.SOLChain, 100); err != nil || halted {
		t.Fatalf("future halt: halted=%v err=%v", halted, err)
	}

	if _, _, err := signing.CheckOutboundHaltAtHeight(&fakeHeightBridge{err: errors.New("boom")}, common.SOLChain, 100); err == nil {
		t.Fatal("expected read error")
	}
}
