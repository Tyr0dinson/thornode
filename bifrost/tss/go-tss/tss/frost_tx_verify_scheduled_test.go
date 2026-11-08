package tss

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// golden7969 is the stagenet migration 796951f7 (MIGRATE:1724844, vault mem4u8
// to 6amewy), mined at XMR height 3778331. Three of a four-member party
// combined; the vault's fifth member sat outside the party and could not adopt
// the result, which left the outbound at two of four active votes.
const golden7969VaultAddress = "46QvdTtn6AEWL5TRNM7TcudFuxecz4zt7bDoo17dEj7FRhQksNYtj5J6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGdDsaf5"

// golden7969 returns the scheduled request and the result its signers
// reported, spent refs included.
func golden7969(t *testing.T) (FrostKeySignRequest, FrostKeySignResponse) {
	t.Helper()
	raw, err := os.ReadFile("testdata/xmr_migrate_796951f7.hex")
	if err != nil {
		t.Fatal(err)
	}
	txHex := strings.TrimSpace(string(raw))
	tx, err := parseXMRTx(mustHex(t, txHex))
	if err != nil {
		t.Fatal(err)
	}
	const txHash = "796951f78acfdef8a5bb14b93955adb144e24aaac23c95d4581edecf138147f8"
	req := FrostKeySignRequest{
		ToAddress:    "46kX3jJKkbqZRvFKuAS7soGYFLMWDRr2vFdoG96KGhfmh5ve9dtGfgt6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGgBeMZx",
		AmountAtomic: 373808 * xmrAtomicPerThorUnit,
		Memo:         "MIGRATE:1724844",
		MaxGasAtomic: 4665 * xmrAtomicPerThorUnit,
	}
	resp := FrostKeySignResponse{
		TxHash:    txHash,
		TxHex:     txHex,
		TxKey:     "78a5a3b7b3c753a0eb012c75195dd224774284e2da50763e9588f5a4183dde00",
		FeeAtomic: tx.fee,
		SpentOutputRefs: []FrostSpentOutputRef{{
			TxHash:      "a05af63311c923383bd42ae6ecb28d14e0c9459e2fe6b3bb87cdf45a2355e7a6",
			KeyImage:    "22f8429967fd555376c5029aef9ee753bc8403eb137cc10d7aa94440d38d3b8c",
			SpendTxHash: txHash,
		}},
	}
	return req, resp
}

// A source-vault member outside the signing party has no prepare, only the
// scheduled txout, so it binds the inputs through the reported spent refs.
func TestVerifyScheduledFrostTxGolden7969(t *testing.T) {
	req, resp := golden7969(t)
	if err := verifyFrostTxForVault(req, nil, resp, golden7969VaultAddress); err != nil {
		t.Fatalf("verify 7969 without prepared inputs: %v", err)
	}

	// The fixture's amount and MaxGas come from the observation, where the fee
	// used all of MaxGas. A smaller scheduled amount with unused gas swept into
	// the destination pays the same output.
	req.AmountAtomic -= 1000 * xmrAtomicPerThorUnit
	req.MaxGasAtomic += 1000 * xmrAtomicPerThorUnit
	if err := verifyFrostTxForVault(req, nil, resp, golden7969VaultAddress); err != nil {
		t.Fatalf("verify 7969 with a migration gas sweep: %v", err)
	}
	req.MaxGasAtomic -= xmrAtomicPerThorUnit
	if err := verifyFrostTxForVault(req, nil, resp, golden7969VaultAddress); err == nil {
		t.Fatal("verified a sweep one THOR unit short")
	}
}

// A multi-input transaction verifies the same way.
func TestVerifyScheduledFrostTxGolden832c(t *testing.T) {
	req, prepared, resp := golden832c(t)
	resp.SpentOutputRefs = make([]FrostSpentOutputRef, len(prepared))
	for i, ref := range prepared {
		ref.SpendTxHash = resp.TxHash
		resp.SpentOutputRefs[i] = ref
	}
	if err := verifyFrostTxForVault(req, nil, resp, golden832cVaultAddress); err != nil {
		t.Fatalf("verify 832c without prepared inputs: %v", err)
	}
}

func TestVerifyScheduledFrostTxRejectsMismatches(t *testing.T) {
	baseReq, baseResp := golden7969(t)
	tx, err := parseXMRTx(mustHex(t, baseResp.TxHex))
	if err != nil {
		t.Fatal(err)
	}
	keyImage := tx.keyImages[0]

	// retx swaps bytes in the hex and keeps the hash and refs consistent with
	// it, so the swap is the only inconsistency.
	retx := func(resp *FrostKeySignResponse, old, repl string) {
		resp.TxHex = replaceOnce(t, resp.TxHex, old, repl)
		resp.TxHash = retxHash(t, resp.TxHex)
		refs := append([]FrostSpentOutputRef(nil), resp.SpentOutputRefs...)
		for i := range refs {
			refs[i].SpendTxHash = resp.TxHash
		}
		resp.SpentOutputRefs = refs
	}
	setRefs := func(resp *FrostKeySignResponse, refs ...FrostSpentOutputRef) {
		resp.SpentOutputRefs = refs
	}
	ref := baseResp.SpentOutputRefs[0]
	withKeyImage := func(ki string) FrostSpentOutputRef {
		r := ref
		r.KeyImage = ki
		return r
	}
	otherRef := ref
	otherRef.OutputIndex = 1
	otherRef.KeyImage = strings.Repeat("ab", 32)

	for _, tc := range []struct {
		name   string
		mutate func(*FrostKeySignRequest, *FrostKeySignResponse)
	}{
		{"output to a foreign key", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			retx(resp, hex.EncodeToString(tx.outputs[0].key[:]), strings.Repeat("11", 32))
		}},
		{"change to a foreign key", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			retx(resp, hex.EncodeToString(tx.outputs[1].key[:]), strings.Repeat("11", 32))
		}},
		{"spends a key image the refs do not report", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			retx(resp, keyImage, strings.Repeat("22", 32))
		}},
		{"ref reports a different key image", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			setRefs(resp, withKeyImage(strings.Repeat("ab", 32)))
		}},
		{"ref without key image", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			setRefs(resp, withKeyImage(""))
		}},
		{"ref with a malformed key image", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			setRefs(resp, withKeyImage(keyImage[:62]))
		}},
		{"duplicate ref key image", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			dup := ref
			dup.OutputIndex = 1
			setRefs(resp, ref, dup)
		}},
		{"duplicate ref", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			setRefs(resp, ref, ref)
		}},
		{"more refs than inputs", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			setRefs(resp, ref, otherRef)
		}},
		{"no refs", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			setRefs(resp)
		}},
		{"ref names another spend transaction", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			r := ref
			r.SpendTxHash = strings.Repeat("cd", 32)
			setRefs(resp, r)
		}},
		{"amount off by one THOR unit", func(r *FrostKeySignRequest, _ *FrostKeySignResponse) {
			r.AmountAtomic += xmrAtomicPerThorUnit
		}},
		{"other destination", func(r *FrostKeySignRequest, _ *FrostKeySignResponse) {
			r.ToAddress = golden832cVaultAddress
		}},
		{"other memo", func(r *FrostKeySignRequest, _ *FrostKeySignResponse) {
			r.Memo = "MIGRATE:1724845"
		}},
		{"fee above max gas", func(r *FrostKeySignRequest, resp *FrostKeySignResponse) {
			r.MaxGasAtomic = resp.FeeAtomic - 1
		}},
		{"wrong tx key", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			resp.TxKey = strings.Repeat("01", 32)
		}},
		{"tx hash does not match hex", func(_ *FrostKeySignRequest, resp *FrostKeySignResponse) {
			resp.TxHash = strings.Repeat("cd", 32)
			setRefs(resp, FrostSpentOutputRef{TxHash: ref.TxHash, KeyImage: ref.KeyImage, SpendTxHash: resp.TxHash})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, resp := baseReq, baseResp
			resp.SpentOutputRefs = append([]FrostSpentOutputRef(nil), baseResp.SpentOutputRefs...)
			tc.mutate(&req, &resp)
			err := verifyFrostTxForVault(req, nil, resp, golden7969VaultAddress)
			if err == nil {
				t.Fatal("tampered transaction verified")
			}
			t.Log(err)
		})
	}

	// The change must return to the source vault, not any THORChain vault.
	if err = verifyFrostTxForVault(baseReq, nil, baseResp, golden832cVaultAddress); err == nil {
		t.Fatal("verified change against another vault")
	}
}

// With the counts equal, repeated refs or key images are still rejected.
func TestVerifyXMRReportedKeyImagesRejectsRepeats(t *testing.T) {
	spend := testHashOf('f')
	a, b := testHashOf('a'), testHashOf('b')
	ref := func(funding rune, index uint32, ki string) FrostSpentOutputRef {
		return FrostSpentOutputRef{TxHash: testHashOf(funding), OutputIndex: index, KeyImage: ki, SpendTxHash: spend}
	}
	if err := verifyXMRReportedKeyImages([]string{a, b}, []FrostSpentOutputRef{ref('c', 0, a), ref('c', 1, strings.ToUpper(b))}, spend); err != nil {
		t.Fatalf("distinct refs: %v", err)
	}
	for name, refs := range map[string][]FrostSpentOutputRef{
		"repeated key image": {ref('c', 0, a), ref('c', 1, a)},
		"repeated ref":       {ref('c', 0, a), ref('c', 0, b)},
		"missing key image":  {ref('c', 0, a), ref('c', 1, "")},
	} {
		if err := verifyXMRReportedKeyImages([]string{a, b}, refs, spend); err == nil {
			t.Fatalf("%s verified", name)
		}
	}
	if err := verifyXMRReportedKeyImages([]string{a, a}, []FrostSpentOutputRef{ref('c', 0, a), ref('c', 1, b)}, spend); err == nil {
		t.Fatal("transaction spending one key image twice verified")
	}
}

// golden7969Outputs is what the vault's sidecar tracks: the genuine funding
// output, holding exactly what the transaction pays out plus its fee, and
// another unspent vault output of a different amount.
func golden7969Outputs(t *testing.T) []FrostVaultOutput {
	t.Helper()
	req, resp := golden7969(t)
	total, err := verifyFrostTxSpend(req, nil, resp, golden7969VaultAddress)
	if err != nil {
		t.Fatal(err)
	}
	ref := resp.SpentOutputRefs[0]
	return []FrostVaultOutput{
		{TxHash: ref.TxHash, OutputIndex: ref.OutputIndex, Amount: total, State: "unspent", KeyImage: ref.KeyImage},
		{TxHash: strings.Repeat("ab", 32), OutputIndex: 3, Amount: total + xmrAtomicPerThorUnit, State: "unspent", KeyImage: strings.Repeat("cd", 32)},
	}
}

// With no prepared inputs the transaction binds only key images, so the
// reported funding refs are bound to the vault outputs the local sidecar
// tracks: threshold colluders pairing the genuine key image with another
// output must not get it attested.
func TestVerifyScheduledFrostTxBindsRefsToTrackedOutputs(t *testing.T) {
	req, resp := golden7969(t)
	tracked := golden7969Outputs(t)
	genuine, other := tracked[0], tracked[1]
	keyImage := resp.SpentOutputRefs[0].KeyImage

	withRef := func(out FrostVaultOutput) FrostKeySignResponse {
		r := resp
		ref := resp.SpentOutputRefs[0]
		ref.TxHash, ref.OutputIndex = out.TxHash, out.OutputIndex
		r.SpentOutputRefs = []FrostSpentOutputRef{ref}
		return r
	}
	with := func(out FrostVaultOutput, mutate func(*FrostVaultOutput)) FrostVaultOutput {
		mutate(&out)
		return out
	}

	for _, tc := range []struct {
		name    string
		resp    FrostKeySignResponse
		outputs []FrostVaultOutput
		ok      bool
	}{
		{"genuine ref", resp, tracked, true},
		{"funding output reserved", resp, []FrostVaultOutput{with(genuine, func(o *FrostVaultOutput) { o.State = "reserved" })}, true},
		{"funding output already spent under the same key image", resp, []FrostVaultOutput{with(genuine, func(o *FrostVaultOutput) {
			o.State, o.KeyImage = "spent", strings.ToUpper(keyImage)
		})}, true},
		{"ref names another tracked output", withRef(other), tracked, false},
		{"ref names an untracked output", withRef(with(other, func(o *FrostVaultOutput) { o.TxHash = strings.Repeat("cd", 32) })), tracked, false},
		{"funding output not scanned yet", resp, tracked[1:], false},
		{"no tracked outputs", resp, nil, false},
		{"funding output with no bound key image", resp, []FrostVaultOutput{with(genuine, func(o *FrostVaultOutput) { o.KeyImage = "" })}, false},
		{"funding output spent with no bound key image", resp, []FrostVaultOutput{with(genuine, func(o *FrostVaultOutput) {
			o.State, o.KeyImage = "spent", ""
		})}, false},
		{"funding output bound to another key image", resp, []FrostVaultOutput{with(genuine, func(o *FrostVaultOutput) { o.KeyImage = strings.Repeat("ef", 32) })}, false},
		{"funding output in an unknown state", resp, []FrostVaultOutput{with(genuine, func(o *FrostVaultOutput) { o.State = "frozen" })}, false},
		{"funding output one atomic unit short", resp, []FrostVaultOutput{with(genuine, func(o *FrostVaultOutput) { o.Amount-- })}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyScheduledFrostTxForVault(req, tc.resp, tc.outputs, golden7969VaultAddress)
			if tc.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("verified")
			}
		})
	}
}

// A ref naming another tracked, unspent vault output of exactly the funding
// amount must not bind: the amount cannot tie a key image to its output, so an
// output with no key image bound locally is rejected.
func TestVerifyScheduledFrostTxRejectsEqualAmountSwap(t *testing.T) {
	req, resp := golden7969(t)
	tracked := golden7969Outputs(t)
	twin := tracked[1]
	twin.Amount, twin.KeyImage = tracked[0].Amount, ""
	ref := resp.SpentOutputRefs[0]
	ref.TxHash, ref.OutputIndex = twin.TxHash, twin.OutputIndex
	resp.SpentOutputRefs = []FrostSpentOutputRef{ref}
	if err := verifyScheduledFrostTxForVault(req, resp, []FrostVaultOutput{tracked[0], twin}, golden7969VaultAddress); err == nil {
		t.Fatal("bound the key image to an equal-amount output with no key image of its own")
	}
}

// A multi-input transaction binds each ref, and the refs together must fund it.
func TestVerifyScheduledFrostTxBindsMultiInputRefs(t *testing.T) {
	req, prepared, resp := golden832c(t)
	resp.SpentOutputRefs = make([]FrostSpentOutputRef, len(prepared))
	for i, ref := range prepared {
		ref.SpendTxHash = resp.TxHash
		resp.SpentOutputRefs[i] = ref
	}
	total, err := verifyFrostTxSpend(req, nil, resp, golden832cVaultAddress)
	if err != nil {
		t.Fatal(err)
	}
	outputs := make([]FrostVaultOutput, len(prepared))
	remaining := total
	for i, ref := range prepared {
		amount := total / uint64(len(prepared))
		if i == len(prepared)-1 {
			amount = remaining
		}
		remaining -= amount
		outputs[i] = FrostVaultOutput{TxHash: ref.TxHash, OutputIndex: ref.OutputIndex, Amount: amount, State: "unspent", KeyImage: ref.KeyImage}
	}
	if err = verifyScheduledFrostTxForVault(req, resp, outputs, golden832cVaultAddress); err != nil {
		t.Fatalf("verify 832c refs: %v", err)
	}
	outputs[0].Amount++
	if err = verifyScheduledFrostTxForVault(req, resp, outputs, golden832cVaultAddress); err == nil {
		t.Fatal("verified refs funding more than the transaction spends")
	}
}

func TestFrostScheduledResultQuorum(t *testing.T) {
	for _, tc := range []struct{ quorum, want int }{
		{2, frostPreparedResultQuorum},
		{4, 2},
		{6, 2},
		{7, 3},
		{9, 3},
		{10, 4},
		{67, 23},
	} {
		if got := frostScheduledResultQuorum(tc.quorum); got != tc.want {
			t.Fatalf("scheduled quorum for a party of %d is %d, want %d", tc.quorum, got, tc.want)
		}
	}
}
