package common

import (
	"sort"
	"strconv"
	"strings"
)

func NormalizeXMRTxHash(txHash string) string {
	return strings.ToLower(strings.TrimSpace(txHash))
}

func XMROutputRefKey(txHash string, outputIndex uint32) string {
	return NormalizeXMRTxHash(txHash) + ":" + strconv.FormatUint(uint64(outputIndex), 10)
}

func NormalizeXMRTxKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func MergeXMRStrings(existing, incoming []string) []string {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}

	merged := make([]string, 0, len(existing)+len(incoming))
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	add := func(value string) {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			return
		}
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		merged = append(merged, normalized)
	}
	for _, value := range existing {
		add(value)
	}
	for _, value := range incoming {
		add(value)
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func CanonicalXMRKeyImageProofs(proofs []*XmrKeyImageProof) []*XmrKeyImageProof {
	if len(proofs) == 0 {
		return nil
	}

	out := make([]*XmrKeyImageProof, 0, len(proofs))
	for _, proof := range proofs {
		if proof == nil {
			continue
		}
		cp := *proof
		cp.TxHash = NormalizeXMRTxHash(cp.TxHash)
		cp.KeyImage = strings.ToLower(strings.TrimSpace(cp.KeyImage))
		cp.ProofC = strings.ToLower(strings.TrimSpace(cp.ProofC))
		cp.ProofS = strings.ToLower(strings.TrimSpace(cp.ProofS))
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TxHash != out[j].TxHash {
			return out[i].TxHash < out[j].TxHash
		}
		if out[i].OutputIndex != out[j].OutputIndex {
			return out[i].OutputIndex < out[j].OutputIndex
		}
		if out[i].KeyImage != out[j].KeyImage {
			return out[i].KeyImage < out[j].KeyImage
		}
		if out[i].ProofC != out[j].ProofC {
			return out[i].ProofC < out[j].ProofC
		}
		return out[i].ProofS < out[j].ProofS
	})
	return out
}

func MergeXMRKeyImageProofs(existing, incoming []*XmrKeyImageProof) []*XmrKeyImageProof {
	incoming = CanonicalXMRKeyImageProofs(incoming)
	if len(incoming) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return incoming
	}

	merged := CanonicalXMRKeyImageProofs(existing)
	seen := make(map[string]struct{}, len(merged)+len(incoming))
	for _, proof := range merged {
		seen[xmrProofKey(proof)] = struct{}{}
	}
	for _, proof := range incoming {
		key := xmrProofKey(proof)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, proof)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].TxHash != merged[j].TxHash {
			return merged[i].TxHash < merged[j].TxHash
		}
		if merged[i].OutputIndex != merged[j].OutputIndex {
			return merged[i].OutputIndex < merged[j].OutputIndex
		}
		return merged[i].KeyImage < merged[j].KeyImage
	})
	return merged
}

func XMRKeyImageProofsRegistered(proofs []*XmrKeyImageProof, registered []*XmrKeyImageRef) bool {
	proofs = CanonicalXMRKeyImageProofs(proofs)
	if len(proofs) == 0 {
		return false
	}
	registeredSet := make(map[string]struct{}, len(registered))
	for _, ref := range registered {
		if ref == nil {
			continue
		}
		registeredSet[XMROutputRefKey(ref.TxHash, ref.OutputIndex)] = struct{}{}
	}
	for _, proof := range proofs {
		if _, ok := registeredSet[XMROutputRefKey(proof.TxHash, proof.OutputIndex)]; !ok {
			return false
		}
	}
	return true
}

func XMRKeyImageRefsContainTx(txHash string, refs []*XmrKeyImageRef) bool {
	txHash = NormalizeXMRTxHash(txHash)
	if txHash == "" {
		return false
	}
	for _, ref := range refs {
		if ref != nil && NormalizeXMRTxHash(ref.TxHash) == txHash {
			return true
		}
	}
	return false
}

func xmrProofKey(proof *XmrKeyImageProof) string {
	if proof == nil {
		return ""
	}
	return XMROutputRefKey(proof.TxHash, proof.OutputIndex) + ":" + strings.ToLower(strings.TrimSpace(proof.KeyImage))
}
