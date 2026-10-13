package types

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestObservedTxMessagesAcceptTaprootVaultAddress(t *testing.T) {
	if !common.LTCChain.SupportsTaprootVaults() {
		t.Skipf("taproot vaults are not enabled on %v builds", common.CurrentChainNetwork)
	}
	pk := GetTaprootTestPubKey()
	taprootAddr, err := pk.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
	if err != nil {
		t.Fatal(err)
	}
	legacyAddr, err := pk.GetAddress(common.LTCChain)
	if err != nil {
		t.Fatal(err)
	}
	other := GetRandomLTCAddress()
	coins := common.Coins{common.NewCoin(common.LTCAsset, cosmos.NewUint(100_000_000))}
	gas := common.Gas{common.NewCoin(common.LTCAsset, cosmos.NewUint(1_000))}

	inbound := func(to common.Address) error {
		tx := common.NewTx(GetRandomTxHash(), other, to, coins, gas, "SWAP:THOR.RUNE")
		obs := common.NewObservedTx(tx, 10, pk, 10)
		return NewMsgObservedTxIn(common.ObservedTxs{obs}, GetRandomBech32Addr()).ValidateBasic()
	}
	if err := inbound(taprootAddr); err != nil {
		t.Fatalf("inbound to the taproot vault address rejected: %v", err)
	}
	if err := inbound(legacyAddr); err != nil {
		t.Fatalf("inbound to the ECDSA-form address rejected: %v", err)
	}
	if err := inbound(other); err == nil {
		t.Fatal("inbound to an unrelated address accepted")
	}

	outbound := func(from common.Address) error {
		tx := common.NewTx(GetRandomTxHash(), from, other, coins, gas, "OUT:"+GetRandomTxHash().String())
		obs := common.NewObservedTx(tx, 10, pk, 10)
		return NewMsgObservedTxOut(common.ObservedTxs{obs}, GetRandomBech32Addr()).ValidateBasic()
	}
	if err := outbound(taprootAddr); err != nil {
		t.Fatalf("outbound from the taproot vault address rejected: %v", err)
	}
	if err := outbound(other); err == nil {
		t.Fatal("outbound from an unrelated address accepted")
	}
}
