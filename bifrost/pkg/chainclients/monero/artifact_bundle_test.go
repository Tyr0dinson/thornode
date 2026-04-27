package monero

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

type xmrArtifactBundle struct {
	CapturedAt string                          `json:"captured_at"`
	Host       string                          `json:"host"`
	Nodes      []string                        `json:"nodes"`
	Asgards    []xmrArtifactVault              `json:"asgards"`
	NodeStates map[string]xmrArtifactNodeState `json:"node_states"`
	DaemonTxs  map[string]json.RawMessage      `json:"daemon_txs"`
}

type xmrArtifactVault struct {
	PubKey                 string                   `json:"pub_key"`
	PubKeyEddsa            string                   `json:"pub_key_eddsa"`
	PubKeyFrost            string                   `json:"pub_key_frost"`
	Chains                 []string                 `json:"chains"`
	VaultCreationHeightXmr int64                    `json:"vault_creation_height_xmr"`
	KeyImagesXmr           []*common.XmrKeyImageRef `json:"key_images_xmr"`
	TxKeysXmr              []*common.XmrTxKeyRef    `json:"tx_keys_xmr"`
}

type xmrArtifactNodeState struct {
	ChainHeight    int64                                           `json:"chain_height"`
	VaultStates    []xmrFrostVaultStateEntry                       `json:"vault_states"`
	Balances       map[string]xmrFrostBalanceResponse              `json:"balances"`
	StoreOutputs   map[string][]xmrFrostOutputEntry                `json:"store_outputs"`
	ScannerOutputs map[string][]xmrFrostOutputEntry                `json:"scanner_outputs"`
	Outbounds      map[string][]xmrFrostOutgoingTransferStateEntry `json:"outbounds"`
}

type normalizedArtifactOutput struct {
	TxHash          string
	OutputIndex     uint32
	Amount          uint64
	Height          uint64
	KeyImage        string
	State           string
	SubaddressMajor uint32
	SubaddressMinor uint32
}

type normalizedArtifactOutbound struct {
	TxHash              string
	TxKeys              []string
	RegisteredInventory bool
	FromAddress         string
	ToAddress           string
	Amount              uint64
	Fee                 uint64
	Memo                string
}

func TestXMRArtifactBundleSyntheticInvariants(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	outboundHash := strings.Repeat("b", 64)
	txKey := strings.Repeat("1", 64)
	keyImage := strings.Repeat("2", 64)
	spendable := uint64(700)

	vault := xmrArtifactVault{
		PubKey:                 vaultPubKey.String(),
		PubKeyEddsa:            vaultPubKey.String(),
		PubKeyFrost:            vaultPubKey.String(),
		Chains:                 []string{common.XMRChain.String()},
		VaultCreationHeightXmr: 100,
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: txHash, OutputIndex: 0, KeyImage: keyImage},
		},
		TxKeysXmr: []*common.XmrTxKeyRef{
			{TxHash: outboundHash, TxKeys: []string{txKey}},
		},
	}

	bundle := &xmrArtifactBundle{
		CapturedAt: "synthetic",
		Host:       "test",
		Nodes:      []string{"dog", "cat"},
		Asgards:    []xmrArtifactVault{vault},
		NodeStates: map[string]xmrArtifactNodeState{
			"dog": {
				ChainHeight: 120,
				Balances: map[string]xmrFrostBalanceResponse{
					vaultPubKey.String(): {Balance: 1_000, UnlockedBalance: 700, SpendableBalance: &spendable},
				},
				StoreOutputs: map[string][]xmrFrostOutputEntry{
					vaultPubKey.String(): {
						{TxHash: txHash, OutputIndex: 0, Amount: 700, Height: 100, KeyImage: keyImage, State: "unspent"},
						{TxHash: strings.Repeat("c", 64), OutputIndex: 1, Amount: 300, Height: 115, State: "unspent"},
					},
				},
				ScannerOutputs: map[string][]xmrFrostOutputEntry{
					vaultPubKey.String(): {
						{TxHash: txHash, OutputIndex: 0, Amount: 700, Height: 100, KeyImage: keyImage, State: "unspent"},
						{TxHash: strings.Repeat("c", 64), OutputIndex: 1, Amount: 300, Height: 115, State: "unspent"},
					},
				},
				Outbounds: map[string][]xmrFrostOutgoingTransferStateEntry{
					vaultPubKey.String(): {
						{
							TxHash:                  outboundHash,
							TxKeys:                  []string{txKey},
							RegisteredInventory:     true,
							FromAddress:             vaultAddr.String(),
							ToAddress:               testOutboundXMRDestination().String(),
							Amount:                  1_000,
							Fee:                     24,
							Memo:                    "OUT:test",
							PlannedHeight:           110,
							ObservedHeight:          artifactUint64Ptr(111),
							Status:                  "observed_mined",
							ReportedEffectiveHeight: artifactUint64Ptr(111),
						},
					},
				},
			},
			"cat": {
				ChainHeight: 120,
				Balances: map[string]xmrFrostBalanceResponse{
					vaultPubKey.String(): {Balance: 1_000, UnlockedBalance: 700, SpendableBalance: &spendable},
				},
				StoreOutputs: map[string][]xmrFrostOutputEntry{
					vaultPubKey.String(): {
						{TxHash: txHash, OutputIndex: 0, Amount: 700, Height: 100, KeyImage: keyImage, State: "unspent"},
						{TxHash: strings.Repeat("c", 64), OutputIndex: 1, Amount: 300, Height: 115, State: "unspent"},
					},
				},
				ScannerOutputs: map[string][]xmrFrostOutputEntry{
					vaultPubKey.String(): {
						{TxHash: txHash, OutputIndex: 0, Amount: 700, Height: 100, KeyImage: keyImage, State: "unspent"},
						{TxHash: strings.Repeat("c", 64), OutputIndex: 1, Amount: 300, Height: 115, State: "unspent"},
					},
				},
				Outbounds: map[string][]xmrFrostOutgoingTransferStateEntry{
					vaultPubKey.String(): {
						{
							TxHash:                  outboundHash,
							TxKeys:                  []string{txKey},
							RegisteredInventory:     true,
							FromAddress:             vaultAddr.String(),
							ToAddress:               testOutboundXMRDestination().String(),
							Amount:                  1_000,
							Fee:                     24,
							Memo:                    "OUT:test",
							PlannedHeight:           110,
							ObservedHeight:          artifactUint64Ptr(111),
							Status:                  "observed_mined",
							ReportedEffectiveHeight: artifactUint64Ptr(111),
						},
					},
				},
			},
		},
	}

	requireValidXMRArtifactBundle(t, bundle)
}

func TestXMRArtifactBundleExternalInvariants(t *testing.T) {
	t.Parallel()

	path := strings.TrimSpace(os.Getenv("XMR_REPLAY_BUNDLE"))
	if path == "" {
		t.Skip("XMR_REPLAY_BUNDLE not set")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}

	var bundle xmrArtifactBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}

	requireValidXMRArtifactBundle(t, &bundle)
}

func requireValidXMRArtifactBundle(t *testing.T, bundle *xmrArtifactBundle) {
	t.Helper()

	if bundle == nil {
		t.Fatal("artifact bundle is nil")
	}
	if len(bundle.Nodes) == 0 {
		t.Fatal("artifact bundle has no nodes")
	}
	if len(bundle.NodeStates) == 0 {
		t.Fatal("artifact bundle has no node states")
	}

	vaultPubKeys := bundleXMRVaultPubKeys(bundle)
	if len(vaultPubKeys) == 0 {
		t.Fatal("artifact bundle has no XMR vaults")
	}

	for _, vaultPubKey := range vaultPubKeys {
		t.Run("parity_"+xmrBundleName(vaultPubKey), func(t *testing.T) {
			requireBundleCrossNodeParity(t, bundle, vaultPubKey)
		})
		t.Run("inventory_"+xmrBundleName(vaultPubKey), func(t *testing.T) {
			requireBundleThorchainInventoryPresent(t, bundle, vaultPubKey)
		})
	}

	for _, node := range bundle.Nodes {
		for _, vaultPubKey := range vaultPubKeys {
			t.Run("balance_"+node+"_"+xmrBundleName(vaultPubKey), func(t *testing.T) {
				requireBundleBalanceMatchesOutputs(t, bundle, node, vaultPubKey)
			})
		}
	}
}

func bundleXMRVaultPubKeys(bundle *xmrArtifactBundle) []string {
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, vault := range bundle.Asgards {
		if !vaultHasXMR(vault) {
			continue
		}
		pubKey := artifactVaultPubKey(vault)
		if pubKey == "" {
			continue
		}
		if _, ok := seen[pubKey]; ok {
			continue
		}
		seen[pubKey] = struct{}{}
		out = append(out, pubKey)
	}
	if len(out) == 0 {
		for _, node := range bundle.Nodes {
			state, ok := bundle.NodeStates[node]
			if !ok {
				continue
			}
			for pubKey := range state.StoreOutputs {
				if _, exists := seen[pubKey]; exists {
					continue
				}
				seen[pubKey] = struct{}{}
				out = append(out, pubKey)
			}
		}
	}
	sort.Strings(out)
	return out
}

func vaultHasXMR(vault xmrArtifactVault) bool {
	for _, chain := range vault.Chains {
		if strings.EqualFold(strings.TrimSpace(chain), common.XMRChain.String()) {
			return true
		}
	}
	return false
}

func artifactVaultPubKey(vault xmrArtifactVault) string {
	if strings.TrimSpace(vault.PubKeyFrost) != "" {
		return strings.TrimSpace(vault.PubKeyFrost)
	}
	if strings.TrimSpace(vault.PubKeyEddsa) != "" {
		return strings.TrimSpace(vault.PubKeyEddsa)
	}
	return strings.TrimSpace(vault.PubKey)
}

func requireBundleCrossNodeParity(t *testing.T, bundle *xmrArtifactBundle, vaultPubKey string) {
	t.Helper()

	var canonicalOutputs []normalizedArtifactOutput
	var canonicalOutbounds []normalizedArtifactOutbound
	var canonicalFound bool

	for _, node := range bundle.Nodes {
		state, ok := bundle.NodeStates[node]
		if !ok {
			t.Fatalf("missing node state for %s", node)
		}

		outputs := normalizeArtifactOutputs(state.StoreOutputs[vaultPubKey])
		outbounds := normalizeArtifactOutbounds(state.Outbounds[vaultPubKey])
		if !canonicalFound {
			canonicalOutputs = outputs
			canonicalOutbounds = outbounds
			canonicalFound = true
			continue
		}
		if diff := compareNormalizedOutputs(canonicalOutputs, outputs); diff != "" {
			t.Fatalf("store output mismatch for node %s: %s", node, diff)
		}
		if diff := compareNormalizedOutbounds(canonicalOutbounds, outbounds); diff != "" {
			t.Fatalf("outbound inventory mismatch for node %s: %s", node, diff)
		}
	}
}

func requireBundleThorchainInventoryPresent(t *testing.T, bundle *xmrArtifactBundle, vaultPubKey string) {
	t.Helper()

	var chainVault *xmrArtifactVault
	for i := range bundle.Asgards {
		if artifactVaultPubKey(bundle.Asgards[i]) == vaultPubKey {
			chainVault = &bundle.Asgards[i]
			break
		}
	}
	if chainVault == nil {
		return
	}

	for _, node := range bundle.Nodes {
		state, ok := bundle.NodeStates[node]
		if !ok {
			t.Fatalf("missing node state for %s", node)
		}
		outputIndex := make(map[string]xmrFrostOutputEntry, len(state.StoreOutputs[vaultPubKey]))
		for _, output := range state.StoreOutputs[vaultPubKey] {
			outputIndex[fmt.Sprintf("%s:%d", strings.ToLower(output.TxHash), output.OutputIndex)] = output
		}
		outboundIndex := make(map[string]xmrFrostOutgoingTransferStateEntry, len(state.Outbounds[vaultPubKey]))
		for _, outbound := range state.Outbounds[vaultPubKey] {
			outboundIndex[strings.ToLower(outbound.TxHash)] = outbound
		}

		for _, ref := range chainVault.KeyImagesXmr {
			key := fmt.Sprintf("%s:%d", strings.ToLower(ref.TxHash), ref.OutputIndex)
			output, ok := outputIndex[key]
			if !ok {
				t.Fatalf("node %s missing tracked output for key-image ref %s", node, key)
			}
			if !strings.EqualFold(strings.TrimSpace(output.KeyImage), strings.TrimSpace(ref.KeyImage)) {
				t.Fatalf("node %s key-image mismatch for %s: have %s want %s", node, key, output.KeyImage, ref.KeyImage)
			}
		}

		for _, ref := range chainVault.TxKeysXmr {
			outbound, ok := outboundIndex[strings.ToLower(ref.TxHash)]
			if !ok {
				t.Fatalf("node %s missing outbound inventory for tx-key ref %s", node, ref.TxHash)
			}
			if diff := compareStringSets(outbound.TxKeys, ref.TxKeys); diff != "" {
				t.Fatalf("node %s tx-keys mismatch for %s: %s", node, ref.TxHash, diff)
			}
		}
	}
}

func requireBundleBalanceMatchesOutputs(t *testing.T, bundle *xmrArtifactBundle, node, vaultPubKey string) {
	t.Helper()

	state, ok := bundle.NodeStates[node]
	if !ok {
		t.Fatalf("missing node state for %s", node)
	}
	balance, ok := state.Balances[vaultPubKey]
	if !ok {
		return
	}

	var total uint64
	var unlocked uint64
	for _, output := range state.StoreOutputs[vaultPubKey] {
		if output.IsSpent() {
			continue
		}
		total += output.Amount
		if strings.EqualFold(strings.TrimSpace(output.State), "reserved") {
			continue
		}
		if state.ChainHeight <= 0 {
			continue
		}
		if uint64(state.ChainHeight) >= output.Height && uint64(state.ChainHeight)-output.Height >= 10 {
			unlocked += output.Amount
		}
	}

	if balance.Balance != total {
		t.Fatalf("node %s balance mismatch for vault %s: have %d want %d", node, vaultPubKey, balance.Balance, total)
	}
	if balance.UnlockedBalance != unlocked {
		t.Fatalf("node %s unlocked balance mismatch for vault %s: have %d want %d", node, vaultPubKey, balance.UnlockedBalance, unlocked)
	}
	if balance.SpendableBalance != nil && *balance.SpendableBalance != unlocked {
		t.Fatalf("node %s spendable balance mismatch for vault %s: have %d want %d", node, vaultPubKey, *balance.SpendableBalance, unlocked)
	}
}

func normalizeArtifactOutputs(entries []xmrFrostOutputEntry) []normalizedArtifactOutput {
	out := make([]normalizedArtifactOutput, 0, len(entries))
	for _, entry := range entries {
		out = append(out, normalizedArtifactOutput{
			TxHash:          strings.ToLower(entry.TxHash),
			OutputIndex:     entry.OutputIndex,
			Amount:          entry.Amount,
			Height:          entry.Height,
			KeyImage:        strings.ToLower(strings.TrimSpace(entry.KeyImage)),
			State:           strings.ToLower(strings.TrimSpace(entry.State)),
			SubaddressMajor: entry.SubaddressMajor,
			SubaddressMinor: entry.SubaddressMinor,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TxHash != out[j].TxHash {
			return out[i].TxHash < out[j].TxHash
		}
		return out[i].OutputIndex < out[j].OutputIndex
	})
	return out
}

func normalizeArtifactOutbounds(entries []xmrFrostOutgoingTransferStateEntry) []normalizedArtifactOutbound {
	out := make([]normalizedArtifactOutbound, 0, len(entries))
	for _, entry := range entries {
		txKeys := append([]string(nil), entry.TxKeys...)
		sort.Strings(txKeys)
		out = append(out, normalizedArtifactOutbound{
			TxHash:              strings.ToLower(entry.TxHash),
			TxKeys:              txKeys,
			RegisteredInventory: entry.RegisteredInventory,
			FromAddress:         strings.TrimSpace(entry.FromAddress),
			ToAddress:           strings.TrimSpace(entry.ToAddress),
			Amount:              entry.Amount,
			Fee:                 entry.Fee,
			Memo:                strings.TrimSpace(entry.Memo),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].TxHash < out[j].TxHash
	})
	return out
}

func compareNormalizedOutputs(left, right []normalizedArtifactOutput) string {
	if len(left) != len(right) {
		return fmt.Sprintf("length %d != %d", len(left), len(right))
	}
	for i := range left {
		if left[i] != right[i] {
			return fmt.Sprintf("entry %d mismatch: %+v != %+v", i, left[i], right[i])
		}
	}
	return ""
}

func compareNormalizedOutbounds(left, right []normalizedArtifactOutbound) string {
	if len(left) != len(right) {
		return fmt.Sprintf("length %d != %d", len(left), len(right))
	}
	for i := range left {
		if left[i].TxHash != right[i].TxHash ||
			left[i].RegisteredInventory != right[i].RegisteredInventory ||
			left[i].FromAddress != right[i].FromAddress ||
			left[i].ToAddress != right[i].ToAddress ||
			left[i].Amount != right[i].Amount ||
			left[i].Fee != right[i].Fee ||
			left[i].Memo != right[i].Memo {
			return fmt.Sprintf("entry %d mismatch: %+v != %+v", i, left[i], right[i])
		}
		if diff := compareStringSets(left[i].TxKeys, right[i].TxKeys); diff != "" {
			return fmt.Sprintf("entry %d tx-keys mismatch: %s", i, diff)
		}
	}
	return ""
}

func compareStringSets(left, right []string) string {
	l := append([]string(nil), left...)
	r := append([]string(nil), right...)
	for i := range l {
		l[i] = strings.ToLower(strings.TrimSpace(l[i]))
	}
	for i := range r {
		r[i] = strings.ToLower(strings.TrimSpace(r[i]))
	}
	sort.Strings(l)
	sort.Strings(r)
	if len(l) != len(r) {
		return fmt.Sprintf("len %d != %d (%v vs %v)", len(l), len(r), l, r)
	}
	for i := range l {
		if l[i] != r[i] {
			return fmt.Sprintf("%v != %v", l, r)
		}
	}
	return ""
}

func xmrBundleName(vaultPubKey string) string {
	if len(vaultPubKey) <= 16 {
		return vaultPubKey
	}
	return vaultPubKey[:16]
}

func artifactUint64Ptr(v uint64) *uint64 {
	return &v
}
