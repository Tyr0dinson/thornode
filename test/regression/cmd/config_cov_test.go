package main

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	"gitlab.com/thorchain/thornode/v3/constants"
)

func TestTemplateFuncMapCov(t *testing.T) {
	render := func(s string) string {
		t.Helper()
		tmpl, err := template.Must(templates.Clone()).Parse(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		buf := &bytes.Buffer{}
		if err = tmpl.Execute(buf, nil); err != nil {
			t.Fatalf("execute %q: %v", s, err)
		}
		return buf.String()
	}

	if got := render("{{ observe_txid 255 }}"); got != strings.Repeat("0", 62)+"ff" {
		t.Fatalf("observe_txid: %s", got)
	}
	if got := render("{{ native_txid -2 }}"); got != "{{ native_txid -2 }}" {
		t.Fatalf("native_txid should defer rendering: %s", got)
	}
	if got := render("{{ version }}"); got != constants.Version {
		t.Fatalf("version: %s", got)
	}

	modules := map[string]string{
		"addr_module_thorchain":           ModuleAddrThorchain,
		"addr_module_asgard":              ModuleAddrAsgard,
		"addr_module_bond":                ModuleAddrBond,
		"addr_module_transfer":            ModuleAddrTransfer,
		"addr_module_reserve":             ModuleAddrReserve,
		"addr_module_fee_collector":       ModuleAddrFeeCollector,
		"addr_module_lending":             ModuleAddrLending,
		"addr_module_affiliate_collector": ModuleAddrAffiliateCollector,
		"addr_module_treasury":            ModuleAddrTreasury,
		"addr_module_rune_pool":           ModuleAddrRUNEPool,
		"addr_module_tcy_claim":           ModuleAddrClaiming,
		"addr_module_tcy_stake":           ModuleAddrTCYStake,
		"addr_module_pol_reserve":         ModuleAddrPOLReserve,
		"addr_module_stable_reserve":      ModuleAddrStableReserve,
	}
	seen := map[string]bool{}
	for name, want := range modules {
		got := render("{{ " + name + " }}")
		if got != want || !strings.HasPrefix(got, "tthor1") {
			t.Fatalf("%s: got %s want %s", name, got, want)
		}
		if seen[got] {
			t.Fatalf("%s: duplicate module address %s", name, got)
		}
		seen[got] = true
	}
	if ModuleAddrAsgard != "tthor1g98cy3n9mmjrpn0sxmn63lztelera37nrytwp2" {
		t.Fatalf("unexpected asgard module address: %s", ModuleAddrAsgard)
	}

	// keys registered in init are exposed as template functions
	if got := render("{{ addr_thor_dog }}"); got != templateAddress["addr_thor_dog"] || !strings.HasPrefix(got, "tthor1") {
		t.Fatalf("addr_thor_dog: %s", got)
	}
	if got := render("{{ pubkey_dog }}"); got != templatePubKey["pubkey_dog"] || !strings.HasPrefix(got, "tthorpub1") {
		t.Fatalf("pubkey_dog: %s", got)
	}
	if got := render("{{ cons_pubkey_dog }}"); got != templateConsPubKey["cons_pubkey_dog"] || !strings.HasPrefix(got, "tthorcpub1") {
		t.Fatalf("cons_pubkey_dog: %s", got)
	}
	if addressToName[templateAddress["addr_btc_cat"]] != "cat" {
		t.Fatal("address to name mapping missing for addr_btc_cat")
	}
	if _, ok := templateAddress["addr_zec_dog_tex"]; !ok {
		t.Fatal("expected ZEC tex address to be registered")
	}
	if len(invariants) == 0 {
		t.Fatal("expected invariant routes to be registered")
	}
}

func TestNodeSecpPrivKeyCov(t *testing.T) {
	priv, err := nodeSecpPrivKey("dog")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := keyRing.Key("dog")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := rec.GetPubKey()
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equals(priv.PubKey()) {
		t.Fatal("derived key does not match keyring account")
	}

	if _, err = nodeSecpPrivKey("unicorn"); err == nil || !strings.Contains(err.Error(), "unknown account name") {
		t.Fatalf("expected unknown account error, got %v", err)
	}
	if mnemonicByName["wolf"] != wolfMnemonic {
		t.Fatal("mnemonicByName mismatch")
	}
}

func TestClientContextAndFactoryCov(t *testing.T) {
	ctx, txf := clientContextAndFactory(3)
	if ctx.NodeURI != "http://localhost:26660" {
		t.Fatalf("unexpected node uri: %s", ctx.NodeURI)
	}
	if ctx.ChainID != "thorchain" || !ctx.SkipConfirm || ctx.Keyring == nil || ctx.Client == nil {
		t.Fatalf("unexpected client context: %+v", ctx)
	}
	if txf.ChainID() != "thorchain" || txf.Gas() != 0 || txf.Keybase() == nil {
		t.Fatal("unexpected tx factory configuration")
	}
}
