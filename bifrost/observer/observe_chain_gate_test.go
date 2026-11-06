package observer

import (
	"errors"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// chainGateBridge serves one BTC vault the node belongs to plus a settable
// EnableNodeChainConsensus mimir and constant.
type chainGateBridge struct {
	thorclient.ThorchainBridge
	mu        sync.Mutex
	nodePk    common.PubKey
	vaultPk   common.PubKey
	mimir     int64
	mimirErr  error
	constant  int64
	constsErr error
}

func (b *chainGateBridge) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return []thorclient.PubKeyContractAddressPair{{
		PubKey:     b.vaultPk,
		Algo:       common.SigningAlgoSecp256k1,
		Contracts:  map[common.Chain]common.Address{},
		Membership: common.PubKeys{b.nodePk},
	}}, nil
}

func (b *chainGateBridge) GetVault(string) (stypes.Vault, error) {
	return stypes.NewVault(1, stypes.VaultStatus_ActiveVault, stypes.VaultType_AsgardVault, b.vaultPk, []string{common.BTCChain.String()}, nil), nil
}

func (b *chainGateBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if key != constants.EnableNodeChainConsensus.String() {
		return -1, nil
	}
	return b.mimir, b.mimirErr
}

func (b *chainGateBridge) GetConstants() (map[string]int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]int64{constants.EnableNodeChainConsensus.String(): b.constant}, b.constsErr
}

func (b *chainGateBridge) set(mimir int64, mimirErr error, constant int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mimir, b.mimirErr, b.constant = mimir, mimirErr, constant
}

// Vault membership only narrows observation while EnableNodeChainConsensus is
// on; with it off, on-chain quorum and slashing count every active node, so a
// non-member must keep observing.
func TestUpdateObservedChainsFollowsChainConsensusMimir(t *testing.T) {
	bridge := &chainGateBridge{
		nodePk:  stypes.GetRandomPubKey(),
		vaultPk: stypes.GetRandomPubKey(),
	}
	pkm, err := pubkeymanager.NewPubKeyManager(bridge, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkm.AddNodePubKey(bridge.nodePk, common.SigningAlgoSecp256k1)
	if err = pkm.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pkm.Stop() }()
	if got := pkm.GetSupportedChains(); !got[common.BTCChain] || len(got) != 1 {
		t.Fatalf("supported chains = %v, want BTC only", got)
	}

	o := &Observer{
		logger:          zerolog.Nop(),
		pubkeyMgr:       pkm,
		thorchainBridge: bridge,
		observedChains:  make(map[common.Chain]bool),
	}
	check := func(step string, wantETH bool) {
		t.Helper()
		o.updateObservedChains()
		if !o.IsChainObserved(common.BTCChain) {
			t.Fatalf("%s: BTC (member chain) not observed", step)
		}
		if got := o.IsChainObserved(common.ETHChain); got != wantETH {
			t.Fatalf("%s: ETH observed = %v, want %v", step, got, wantETH)
		}
	}

	// Mimir unset, constant off: observe everything.
	bridge.set(-1, nil, 0)
	check("unset mimir, constant 0", true)

	// Mimir unset, constant on: scope to membership.
	bridge.set(-1, nil, 1)
	check("unset mimir, constant 1", false)

	// Mimir explicitly turned off overrides the constant (rollback).
	bridge.set(0, nil, 1)
	check("mimir 0", true)

	// Mimir on.
	bridge.set(1, nil, 0)
	check("mimir 1", false)

	// A failed read keeps the last known setting.
	bridge.set(0, errors.New("unavailable"), 0)
	check("mimir read error after on", false)
	bridge.set(0, nil, 0)
	check("mimir 0 again", true)
	bridge.set(1, errors.New("unavailable"), 1)
	check("mimir read error after off", true)
}
