package tss

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/btcsuite/btcutil/bech32"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// golden832c is the stagenet migration 832c94a5 (MIGRATE:1716429, vault ujturt
// to mem4u8), mined at XMR height 3777940. Its v3/v4 non-combiners never
// adopted it, which left the outbound at three of five votes.
const golden832cVaultAddress = "4446ArEc4TBF5Fa3GDnw6iC5EDZUs3dLPDpvNww86eKHgFRH5bnodMJ6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGcUSzxu"

func golden832c(t *testing.T) (FrostKeySignRequest, []FrostSpentOutputRef, FrostKeySignResponse) {
	t.Helper()
	raw, err := os.ReadFile("testdata/xmr_migrate_832c94a5.hex")
	if err != nil {
		t.Fatal(err)
	}
	prepared := []FrostSpentOutputRef{
		{TxHash: "a05af63311c923383bd42ae6ecb28d14e0c9459e2fe6b3bb87cdf45a2355e7a6", OutputIndex: 1, KeyImage: "3bbda04ec650f6e1898ab5288ab216753098978cfbb2c15a8bcba0b4a12ec124"},
		{TxHash: "b4497f9add21875cca50a1cbff5a3a25de1445574b268a4784154394e7d9357e", OutputIndex: 1, KeyImage: "452f941203336fd56bb68dde85ea96051dfc4a1d7f694237e307ef8b81aada94"},
		{TxHash: "e4fd4610b4111b0ea63a675526f8c017dda27019d13777fc0d9b9eabc8b811c3", KeyImage: "7898f66e28de35336249357f4c83db7a2c41d1e20ba78f115fe169b143f38e8a"},
	}
	req := FrostKeySignRequest{
		VaultPubKey:  "sthorpub1zcjduepqgqj84j7etqp9gguy5s7teastgge4f88g4ykugn9w6d4zhaj32r4qujturt",
		ToAddress:    "46QvdTtn6AEWL5TRNM7TcudFuxecz4zt7bDoo17dEj7FRhQksNYtj5J6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGdDsaf5",
		AmountAtomic: 830206 * xmrAtomicPerThorUnit,
		Memo:         "MIGRATE:1716429",
	}
	tx, err := parseXMRTx(mustHex(t, strings.TrimSpace(string(raw))))
	if err != nil {
		t.Fatal(err)
	}
	resp := FrostKeySignResponse{
		TxHash:    "832c94a5a8000a8ff068183827be753bdfc1f50bfad62dd0ffc7e1cd9458d882",
		TxHex:     strings.TrimSpace(string(raw)),
		TxKey:     "be86d2ee91310abdadec4b58e3a370d1558444ddbdec63783d73048fa6e5ab02",
		FeeAtomic: tx.fee,
	}
	return req, prepared, resp
}

func TestVerifyAdoptedFrostTxGolden832c(t *testing.T) {
	req, prepared, resp := golden832c(t)
	if err := verifyFrostTxForVault(req, prepared, resp, golden832cVaultAddress); err != nil {
		t.Fatalf("verify 832c: %v", err)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// retxHash recomputes the transaction id of a tampered hex so the tamper is the
// only inconsistency.
func retxHash(t *testing.T, txHex string) string {
	t.Helper()
	tx, err := parseXMRTx(mustHex(t, txHex))
	if err != nil {
		t.Fatal(err)
	}
	return tx.hash
}

// replaceOnce swaps the first occurrence of old in s, failing if absent.
func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("%s not found", old)
	}
	return strings.Replace(s, old, repl, 1)
}

func TestVerifyAdoptedFrostTxRejectsMismatches(t *testing.T) {
	baseReq, basePrepared, baseResp := golden832c(t)
	tx, err := parseXMRTx(mustHex(t, baseResp.TxHex))
	if err != nil {
		t.Fatal(err)
	}
	destKey := hex.EncodeToString(tx.outputs[0].key[:])
	keyImage := tx.keyImages[0]

	for _, tc := range []struct {
		name   string
		mutate func(*FrostKeySignRequest, *[]FrostSpentOutputRef, *FrostKeySignResponse)
	}{
		{"amount off by one THOR unit", func(r *FrostKeySignRequest, _ *[]FrostSpentOutputRef, _ *FrostKeySignResponse) {
			r.AmountAtomic += xmrAtomicPerThorUnit
		}},
		{"other destination", func(r *FrostKeySignRequest, _ *[]FrostSpentOutputRef, _ *FrostKeySignResponse) {
			r.ToAddress = golden832cVaultAddress
		}},
		{"other memo", func(r *FrostKeySignRequest, _ *[]FrostSpentOutputRef, _ *FrostKeySignResponse) {
			r.Memo = "MIGRATE:1716430"
		}},
		{"fee above max gas", func(r *FrostKeySignRequest, _ *[]FrostSpentOutputRef, resp *FrostKeySignResponse) {
			r.MaxGasAtomic = resp.FeeAtomic - 1
		}},
		{"unprepared key image", func(_ *FrostKeySignRequest, p *[]FrostSpentOutputRef, _ *FrostKeySignResponse) {
			refs := append([]FrostSpentOutputRef(nil), (*p)...)
			refs[0].KeyImage = strings.Repeat("ab", 32)
			*p = refs
		}},
		{"prepared input without key image", func(_ *FrostKeySignRequest, p *[]FrostSpentOutputRef, _ *FrostKeySignResponse) {
			refs := append([]FrostSpentOutputRef(nil), (*p)...)
			refs[0].KeyImage = ""
			*p = refs
		}},
		{"wrong tx key", func(_ *FrostKeySignRequest, _ *[]FrostSpentOutputRef, resp *FrostKeySignResponse) {
			resp.TxKey = strings.Repeat("01", 32)
		}},
		{"tx hash does not match hex", func(_ *FrostKeySignRequest, _ *[]FrostSpentOutputRef, resp *FrostKeySignResponse) {
			resp.TxHash = strings.Repeat("cd", 32)
		}},
		{"reported fee differs", func(_ *FrostKeySignRequest, _ *[]FrostSpentOutputRef, resp *FrostKeySignResponse) {
			resp.FeeAtomic++
		}},
		{"output to a foreign key", func(_ *FrostKeySignRequest, _ *[]FrostSpentOutputRef, resp *FrostKeySignResponse) {
			resp.TxHex = replaceOnce(t, resp.TxHex, destKey, strings.Repeat("11", 32))
			resp.TxHash = retxHash(t, resp.TxHex)
		}},
		{"spends a different key image", func(_ *FrostKeySignRequest, _ *[]FrostSpentOutputRef, resp *FrostKeySignResponse) {
			resp.TxHex = replaceOnce(t, resp.TxHex, keyImage, strings.Repeat("22", 32))
			resp.TxHash = retxHash(t, resp.TxHex)
		}},
		{"truncated", func(_ *FrostKeySignRequest, _ *[]FrostSpentOutputRef, resp *FrostKeySignResponse) {
			resp.TxHex = resp.TxHex[:200]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, prepared, resp := baseReq, append([]FrostSpentOutputRef(nil), basePrepared...), baseResp
			tc.mutate(&req, &prepared, &resp)
			err := verifyFrostTxForVault(req, prepared, resp, golden832cVaultAddress)
			if err == nil {
				t.Fatal("tampered transaction verified")
			}
			t.Log(err)
		})
	}
}

// Prepare reports no key image for an output the vault has not spent before.
// The key image is then bound through the peer-reported spent refs, which must
// still match the transaction.
func TestVerifyAdoptedFrostTxBindsUnknownKeyImagesToReportedRefs(t *testing.T) {
	req, prepared, resp := golden832c(t)
	reported := make([]FrostSpentOutputRef, len(prepared))
	unknown := make([]FrostSpentOutputRef, len(prepared))
	for i, ref := range prepared {
		reported[i] = ref
		reported[i].SpendTxHash = resp.TxHash
		unknown[i] = ref
		unknown[i].KeyImage = ""
	}
	resp.SpentOutputRefs = reported
	if err := verifyFrostTxForVault(req, unknown, resp, golden832cVaultAddress); err != nil {
		t.Fatalf("prepare-shaped inputs: %v", err)
	}

	lying := append([]FrostSpentOutputRef(nil), reported...)
	lying[0].KeyImage = strings.Repeat("ab", 32)
	resp.SpentOutputRefs = lying
	if err := verifyFrostTxForVault(req, unknown, resp, golden832cVaultAddress); err == nil {
		t.Fatal("verified a reported key image the transaction does not spend")
	}

	resp.SpentOutputRefs = nil
	if err := verifyFrostTxForVault(req, unknown, resp, golden832cVaultAddress); err == nil {
		t.Fatal("verified inputs with no key image at all")
	}
}

// The production entry point resolves the source vault from its pubkey.
func TestVerifyAdoptedFrostTxResolvesVaultFromPubKey(t *testing.T) {
	ensureBech32Prefix()
	req, prepared, resp := golden832c(t)
	_, data, err := bech32.Decode(req.VaultPubKey)
	if err != nil {
		t.Fatal(err)
	}
	req.VaultPubKey, err = bech32.Encode(sdk.GetConfig().GetBech32AccountPubPrefix(), data)
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyAdoptedFrostTx(req, prepared, resp); err != nil {
		t.Fatalf("verify via vault pubkey: %v", err)
	}
}

// A migration also sweeps the unused gas into the destination vault.
func TestExpectedXMRDestinationAmountSweepsMigrationGas(t *testing.T) {
	req := FrostKeySignRequest{AmountAtomic: 1_000, MaxGasAtomic: 300, Memo: "MIGRATE:5"}
	if got := expectedXMRDestinationAmount(req, 100); got != 1_200 {
		t.Fatalf("migration amount %d", got)
	}
	if got := expectedXMRDestinationAmount(req, 300); got != 1_000 {
		t.Fatalf("fee at cap %d", got)
	}
	req.Memo = "OUT:ABC"
	if got := expectedXMRDestinationAmount(req, 100); got != 1_000 {
		t.Fatalf("outbound amount %d", got)
	}
}
