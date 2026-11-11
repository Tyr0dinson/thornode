package tss

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"filippo.io/edwards25519"
	"golang.org/x/crypto/sha3"

	"gitlab.com/thorchain/thornode/v3/common"
)

// Monero transaction layout limits mirrored from monero-oxide.
const (
	xmrTxVersion             = 2
	xmrRctClsagBulletproof   = 5
	xmrRctClsagBulletproofPP = 6
	xmrMaxTxInputs           = 128
	xmrMaxTxOutputs          = 16
	xmrMaxRingSize           = 64
	xmrMaxExtraSize          = 1060
	xmrMaxExtraNonceSize     = 255
	xmrArbitraryDataMarker   = 0x7f
	xmrAtomicPerThorUnit     = 10_000
	xmrChangeSubaddressMajor = 2
	xmrChangeSubaddressMinor = 0
	xmrExtraTagPublicKey     = 0x01
	xmrExtraTagNonce         = 0x02
	xmrExtraTagPublicKeys    = 0x04
	xmrOutputTagKey          = 0x02
	xmrOutputTagTaggedKey    = 0x03
	xmrInputTagToKey         = 0x02
)

// xmrPedersenH is Monero's second Pedersen generator H.
var xmrPedersenH = mustXMRPoint("8b655970153799af2aeadc9ff1add0ea6c7251d54154cfa92c173a0dd39c1f94")

func mustXMRPoint(h string) *edwards25519.Point {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil {
		panic(err)
	}
	return p
}

type xmrTxOutput struct {
	key     [32]byte
	viewTag *byte
}

type xmrParsedTx struct {
	hash       string
	keyImages  []string
	outputs    []xmrTxOutput
	extra      []byte
	fee        uint64
	ecdhInfo   [][8]byte
	commitment [][32]byte
}

// verifyAdoptedFrostTx checks, from the transaction bytes alone, that a FROST
// result reported by peers is the transaction this node's own prepare planned:
// it spends exactly the prepared key images, pays the requested amount to the
// requested address with the requested memo, keeps the fee within the cap, and
// sends everything else to the source vault's change subaddress. It mirrors the
// sidecar's verify_outbound_metadata_parts plus monero-oxide's change output.
//
// With no prepared inputs (a source-vault member outside the signing party,
// which holds only the scheduled txout), the transaction must spend exactly
// the key images of the reported spent refs; verifyScheduledFrostTx then binds
// the refs' funding outputs. Every output is still pinned to the scheduled
// destination or the source vault's change, so a false binding cannot redirect
// funds.
func verifyAdoptedFrostTx(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse) error {
	pk, err := common.NewPubKey(strings.TrimSpace(req.VaultPubKey))
	if err != nil {
		return fmt.Errorf("vault pubkey: %w", err)
	}
	vaultAddress, err := pk.GetAddress(common.XMRChain)
	if err != nil {
		return fmt.Errorf("vault address: %w", err)
	}
	return verifyFrostTxForVault(req, prepared, resp, vaultAddress.String())
}

// verifyFrostTxForVault is verifyAdoptedFrostTx with the source vault's Monero
// address already resolved.
func verifyFrostTxForVault(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse, vaultAddress string) error {
	_, err := verifyFrostTxSpend(req, prepared, resp, vaultAddress)
	return err
}

// verifyAdoptedFrostTxAndBind is verifyAdoptedFrostTx that additionally binds
// each reported spent ref to a vault output the local sidecar tracks. It is the
// prepared-input adoption check for the frostPreparedResultQuorum fast path:
// verifyAdoptedFrostTx alone decodes the transaction against the prepared plan
// but, for a prepared input whose key image this node did not resolve at
// prepare time, trusts the peer-reported output->key-image binding
// (validateFrostPreparedInputs skips the per-ref check and verifyXMRKeyImages
// compares only the unordered set). bindXMRSpentRefsToVaultOutputs rejects a ref
// bound to a different key image locally, and returns errFrostAdoptionPending
// for a ref the sidecar has not bound yet so the result defers to the full
// threshold+1 quorum instead of being adopted on trust.
func verifyAdoptedFrostTxAndBind(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
	pk, err := common.NewPubKey(strings.TrimSpace(req.VaultPubKey))
	if err != nil {
		return fmt.Errorf("vault pubkey: %w", err)
	}
	vaultAddress, err := pk.GetAddress(common.XMRChain)
	if err != nil {
		return fmt.Errorf("vault address: %w", err)
	}
	total, err := verifyFrostTxSpend(req, prepared, resp, vaultAddress.String())
	if err != nil {
		return err
	}
	return bindXMRSpentRefsToVaultOutputs(resp.SpentOutputRefs, outputs, total)
}

// verifyFrostTxSpend is verifyFrostTxForVault that also returns what the
// transaction's inputs must total: every decoded output plus the fee.
func verifyFrostTxSpend(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse, vaultAddress string) (uint64, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(resp.TxHex))
	if err != nil {
		return 0, fmt.Errorf("tx_hex is not valid hex: %w", err)
	}
	tx, err := parseXMRTx(raw)
	if err != nil {
		return 0, err
	}
	if !strings.EqualFold(tx.hash, strings.TrimSpace(resp.TxHash)) {
		return 0, fmt.Errorf("tx_hash does not match tx_hex")
	}
	if tx.fee != resp.FeeAtomic {
		return 0, fmt.Errorf("fee %d does not match reported fee %d", tx.fee, resp.FeeAtomic)
	}
	if req.MaxGasAtomic > 0 && tx.fee > req.MaxGasAtomic {
		return 0, fmt.Errorf("fee %d exceeds max gas %d", tx.fee, req.MaxGasAtomic)
	}
	if err = verifyXMRKeyImages(tx.keyImages, prepared, resp.SpentOutputRefs, tx.hash); err != nil {
		return 0, err
	}

	txPubKey, memo, err := parseXMRExtra(tx.extra)
	if err != nil {
		return 0, err
	}
	if memo != normalizedXMRMemo(req.Memo) {
		return 0, fmt.Errorf("memo does not match the keysign request")
	}

	toSpend, toView, toSub, err := common.XMRAddressPublicKeys(req.ToAddress)
	if err != nil {
		return 0, fmt.Errorf("destination address: %w", err)
	}
	spendPoint, err := new(edwards25519.Point).SetBytes(toSpend[:])
	if err != nil {
		return 0, fmt.Errorf("destination spend key: %w", err)
	}
	viewPoint, err := new(edwards25519.Point).SetBytes(toView[:])
	if err != nil {
		return 0, fmt.Errorf("destination view key: %w", err)
	}
	txKeyBytes, err := hex.DecodeString(strings.TrimSpace(resp.TxKey))
	if err != nil {
		return 0, fmt.Errorf("tx_key is not valid hex: %w", err)
	}
	r, err := new(edwards25519.Scalar).SetCanonicalBytes(txKeyBytes)
	if err != nil {
		return 0, fmt.Errorf("tx_key is not a canonical scalar: %w", err)
	}
	expectedR := new(edwards25519.Point).ScalarBaseMult(r)
	if toSub {
		expectedR = new(edwards25519.Point).ScalarMult(r, spendPoint)
	}
	if !bytes.Equal(expectedR.Bytes(), txPubKey[:]) {
		return 0, fmt.Errorf("tx_key does not match the transaction public key")
	}
	R, err := new(edwards25519.Point).SetBytes(txPubKey[:])
	if err != nil {
		return 0, fmt.Errorf("transaction public key: %w", err)
	}

	changeSpend, viewScalar, err := xmrVaultChangeSpendKey(vaultAddress)
	if err != nil {
		return 0, err
	}
	destECDH := new(edwards25519.Point).ScalarMult(r, viewPoint)
	changeECDH := new(edwards25519.Point).ScalarMult(viewScalar, R)

	expectedAmount := expectedXMRDestinationAmount(req, tx.fee)
	destMatches := 0
	total := tx.fee
	for i, out := range tx.outputs {
		amount, ok := xmrOutputAmount(tx, i, destECDH, spendPoint)
		if ok {
			if amount/xmrAtomicPerThorUnit != expectedAmount/xmrAtomicPerThorUnit {
				return 0, fmt.Errorf("destination output %d pays %d, expected %d", i, amount, expectedAmount)
			}
			destMatches++
		} else if amount, ok = xmrOutputAmount(tx, i, changeECDH, changeSpend); !ok {
			return 0, fmt.Errorf("output %d (%x) pays neither the destination nor the vault change address", i, out.key)
		}
		if total > math.MaxUint64-amount {
			return 0, errors.New("transaction outputs overflow")
		}
		total += amount
	}
	if destMatches != 1 {
		return 0, fmt.Errorf("transaction has %d destination outputs, expected 1", destMatches)
	}
	return total, nil
}

// expectedXMRDestinationAmount mirrors the sidecar's migration_sweep_output_amount:
// a migration also sweeps the unused gas into the destination vault.
func expectedXMRDestinationAmount(req FrostKeySignRequest, fee uint64) uint64 {
	if req.MaxGasAtomic == 0 || !isXMRMigrationMemo(req.Memo) || fee >= req.MaxGasAtomic {
		return req.AmountAtomic
	}
	return req.AmountAtomic + (req.MaxGasAtomic - fee)
}

func isXMRMigrationMemo(memo string) bool {
	memo = strings.TrimSpace(memo)
	return strings.EqualFold(memo, "MIGRATE") ||
		(len(memo) >= 8 && strings.EqualFold(memo[:8], "MIGRATE:"))
}

// normalizedXMRMemo mirrors the sidecar's normalized_outbound_memo.
func normalizedXMRMemo(memo string) string {
	if strings.TrimSpace(memo) == "" {
		return ""
	}
	return memo
}

// verifyXMRKeyImages checks that the transaction spends exactly the prepared
// inputs. Prepare reports no key image for an output this node has not spent
// before, and the sidecar does not return the key images it derives while
// signing, so for such inputs the key image is taken from the peer-reported
// spent refs, which validateFrostPreparedInputs has already matched one to one
// with the prepared inputs. Every output is still pinned by the caller, so a
// wrong binding cannot redirect funds. With no prepared inputs at all,
// verifyXMRReportedKeyImages binds them to the reported refs alone.
func verifyXMRKeyImages(actual []string, prepared, reported []FrostSpentOutputRef, txHash string) error {
	if len(prepared) == 0 {
		return verifyXMRReportedKeyImages(actual, reported, txHash)
	}
	if len(actual) != len(prepared) {
		return fmt.Errorf("transaction spends %d inputs, prepared %d", len(actual), len(prepared))
	}
	reportedByRef := make(map[string]string, len(reported))
	for _, ref := range reported {
		id := fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(ref.TxHash)), ref.OutputIndex)
		reportedByRef[id] = strings.ToLower(strings.TrimSpace(ref.KeyImage))
	}
	want := make(map[string]bool, len(prepared))
	for i, ref := range prepared {
		ki := strings.ToLower(strings.TrimSpace(ref.KeyImage))
		if ki == "" {
			ki = reportedByRef[fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(ref.TxHash)), ref.OutputIndex)]
		}
		if ki == "" {
			return fmt.Errorf("prepared input %d has no key image", i)
		}
		if want[ki] {
			return fmt.Errorf("key image %s repeats", ki)
		}
		want[ki] = true
	}
	for _, ki := range actual {
		if !want[ki] {
			return fmt.Errorf("transaction spends key image %s that was not prepared", ki)
		}
		delete(want, ki)
	}
	if len(want) != 0 {
		return errors.New("transaction does not spend every prepared input")
	}
	return nil
}

// verifyXMRReportedKeyImages checks that the transaction spends exactly the key
// images of the reported spent refs, one ref per input. Every ref must carry
// its key image and name this transaction as its spend; refs and key images may
// not repeat. Nothing in the transaction ties a ref's funding output to its key
// image; bindXMRSpentRefsToVaultOutputs does that against the local sidecar.
func verifyXMRReportedKeyImages(actual []string, reported []FrostSpentOutputRef, txHash string) error {
	if len(reported) == 0 {
		return errors.New("no prepared inputs and no reported spent refs")
	}
	if len(actual) != len(reported) {
		return fmt.Errorf("transaction spends %d inputs, reported %d spent refs", len(actual), len(reported))
	}
	want := make(map[string]bool, len(reported))
	refs := make(map[string]bool, len(reported))
	for i, ref := range reported {
		ki := strings.ToLower(strings.TrimSpace(ref.KeyImage))
		if err := validateHashHex(fmt.Sprintf("spent_output_refs[%d].key_image", i), ki); err != nil {
			return err
		}
		if want[ki] {
			return fmt.Errorf("key image %s repeats", ki)
		}
		want[ki] = true
		id := fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(ref.TxHash)), ref.OutputIndex)
		if refs[id] {
			return fmt.Errorf("spent ref %s repeats", id)
		}
		refs[id] = true
		if !strings.EqualFold(strings.TrimSpace(ref.SpendTxHash), txHash) {
			return fmt.Errorf("spent ref %s names a different spend transaction", id)
		}
	}
	for _, ki := range actual {
		if !want[ki] {
			return fmt.Errorf("transaction spends key image %s that no spent ref reports", ki)
		}
		delete(want, ki)
	}
	if len(want) != 0 {
		return errors.New("transaction does not spend every reported input")
	}
	return nil
}

// verifyScheduledFrostTx checks a peer-reported result for a source-vault
// member without prepared inputs: the transaction must match the scheduled
// txout (verifyAdoptedFrostTx with no prepared inputs), and its spent refs must
// name outputs this node's own sidecar tracks for the vault, funding exactly
// what the transaction pays out plus its fee.
func verifyScheduledFrostTx(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
	pk, err := common.NewPubKey(strings.TrimSpace(req.VaultPubKey))
	if err != nil {
		return fmt.Errorf("vault pubkey: %w", err)
	}
	vaultAddress, err := pk.GetAddress(common.XMRChain)
	if err != nil {
		return fmt.Errorf("vault address: %w", err)
	}
	return verifyScheduledFrostTxForVault(req, resp, outputs, vaultAddress.String())
}

// verifyScheduledFrostTxForVault is verifyScheduledFrostTx with the source
// vault's Monero address already resolved.
func verifyScheduledFrostTxForVault(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput, vaultAddress string) error {
	total, err := verifyFrostTxSpend(req, nil, resp, vaultAddress)
	if err != nil {
		return err
	}
	return bindXMRSpentRefsToVaultOutputs(resp.SpentOutputRefs, outputs, total)
}

// bindXMRSpentRefsToVaultOutputs checks that every reported spent ref names a
// vault output the local sidecar tracks, that the output is unspent, reserved,
// or already spent, that the sidecar has bound it to the reported key image,
// and that the outputs' amounts sum to total. The amounts alone cannot tie a
// key image to its output: equal-value outputs are interchangeable. So a ref
// whose output has no key image locally is not accepted (it is pending until
// the sidecar binds one), and without that the result is adopted only from a
// full quorum.
func bindXMRSpentRefsToVaultOutputs(refs []FrostSpentOutputRef, outputs []FrostVaultOutput, total uint64) error {
	tracked := make(map[string]FrostVaultOutput, len(outputs))
	for _, out := range outputs {
		tracked[fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(out.TxHash)), out.OutputIndex)] = out
	}
	var funded uint64
	for _, ref := range refs {
		id := fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(ref.TxHash)), ref.OutputIndex)
		out, ok := tracked[id]
		if !ok {
			return fmt.Errorf("spent ref %s is not a vault output this node tracks: %w", id, errFrostAdoptionPending)
		}
		ki := strings.ToLower(strings.TrimSpace(ref.KeyImage))
		bound := strings.ToLower(strings.TrimSpace(out.KeyImage))
		if bound == "" {
			// The sidecar may still bind it, e.g. from an authenticated import
			// during the wait; check again rather than reject the result.
			return fmt.Errorf("spent ref %s has no locally bound key image: %w", id, errFrostAdoptionPending)
		}
		if bound != ki {
			return fmt.Errorf("spent ref %s is bound to key image %s, reported %s", id, bound, ki)
		}
		switch strings.ToLower(strings.TrimSpace(out.State)) {
		case "unspent", "reserved":
		case "spent":
		default:
			return fmt.Errorf("spent ref %s has unknown state %q", id, out.State)
		}
		if funded > math.MaxUint64-out.Amount {
			return errors.New("spent refs overflow")
		}
		funded += out.Amount
	}
	if funded != total {
		return fmt.Errorf("spent refs fund %d, transaction outputs and fee total %d", funded, total)
	}
	return nil
}

// xmrVaultChangeSpendKey returns the spend key of the vault's change subaddress
// (2,0) and the shared THORChain view scalar.
func xmrVaultChangeSpendKey(vaultAddress string) (*edwards25519.Point, *edwards25519.Scalar, error) {
	spend, _, sub, err := common.XMRAddressPublicKeys(vaultAddress)
	if err != nil || sub {
		return nil, nil, fmt.Errorf("vault address is not a primary address: %w", err)
	}
	B, err := new(edwards25519.Point).SetBytes(spend[:])
	if err != nil {
		return nil, nil, fmt.Errorf("vault spend key: %w", err)
	}
	viewHex, err := common.XMRViewKeyHex()
	if err != nil {
		return nil, nil, err
	}
	viewBytes, err := hex.DecodeString(viewHex)
	if err != nil {
		return nil, nil, err
	}
	a, err := new(edwards25519.Scalar).SetCanonicalBytes(viewBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("view key: %w", err)
	}
	// D = B + Hs("SubAddr\0" || a || major || minor) G
	data := append([]byte("SubAddr\x00"), a.Bytes()...)
	data = binary.LittleEndian.AppendUint32(data, xmrChangeSubaddressMajor)
	data = binary.LittleEndian.AppendUint32(data, xmrChangeSubaddressMinor)
	m := xmrKeccakToScalar(data)
	D := new(edwards25519.Point).Add(B, new(edwards25519.Point).ScalarBaseMult(m))
	return D, a, nil
}

// xmrOutputAmount reports whether output i pays spendKey under the given ECDH,
// and its amount when its commitment opens correctly.
func xmrOutputAmount(tx xmrParsedTx, i int, ecdh, spendKey *edwards25519.Point) (uint64, bool) {
	derivation := new(edwards25519.Point).MultByCofactor(ecdh).Bytes()
	derivation = xmrAppendVarint(derivation, uint64(i))
	out := tx.outputs[i]
	if out.viewTag != nil && xmrKeccak(append([]byte("view_tag"), derivation...))[0] != *out.viewTag {
		return 0, false
	}
	shared := xmrKeccakToScalar(derivation)
	P, err := new(edwards25519.Point).SetBytes(out.key[:])
	if err != nil {
		return 0, false
	}
	recovered := new(edwards25519.Point).Subtract(P, new(edwards25519.Point).ScalarBaseMult(shared))
	if recovered.Equal(spendKey) != 1 {
		return 0, false
	}
	amountMask := xmrKeccak(append([]byte("amount"), shared.Bytes()...))
	amount := binary.LittleEndian.Uint64(tx.ecdhInfo[i][:]) ^ binary.LittleEndian.Uint64(amountMask[:8])
	mask := xmrKeccakToScalar(append([]byte("commitment_mask"), shared.Bytes()...))
	var amountBytes [32]byte
	binary.LittleEndian.PutUint64(amountBytes[:8], amount)
	amountScalar, err := new(edwards25519.Scalar).SetCanonicalBytes(amountBytes[:])
	if err != nil {
		return 0, false
	}
	commitment := new(edwards25519.Point).VarTimeDoubleScalarBaseMult(amountScalar, xmrPedersenH, mask)
	if !bytes.Equal(commitment.Bytes(), tx.commitment[i][:]) {
		return 0, false
	}
	return amount, true
}

// parseXMRTx strictly parses a v2 CLSAG RingCT transaction: its prefix and RCT
// base. The prunable part is only hashed.
func parseXMRTx(raw []byte) (xmrParsedTx, error) {
	var tx xmrParsedTx
	r := &xmrReader{b: raw}
	if v := r.varint(); v != xmrTxVersion {
		return tx, fmt.Errorf("unsupported transaction version %d", v)
	}
	if unlock := r.varint(); unlock != 0 {
		return tx, fmt.Errorf("transaction has an unlock time")
	}
	nIn := r.varint()
	if nIn == 0 || nIn > xmrMaxTxInputs {
		return tx, fmt.Errorf("invalid input count %d", nIn)
	}
	for i := uint64(0); i < nIn && r.err == nil; i++ {
		if tag := r.byte(); tag != xmrInputTagToKey {
			return tx, fmt.Errorf("input %d is not to-key", i)
		}
		if amount := r.varint(); amount != 0 {
			return tx, fmt.Errorf("input %d has a cleartext amount", i)
		}
		ring := r.varint()
		if ring == 0 || ring > xmrMaxRingSize {
			return tx, fmt.Errorf("input %d has invalid ring size %d", i, ring)
		}
		for j := uint64(0); j < ring; j++ {
			r.varint()
		}
		ki := r.bytes(32)
		tx.keyImages = append(tx.keyImages, hex.EncodeToString(ki))
	}
	if r.err != nil {
		return tx, r.err
	}
	nOut := r.varint()
	if nOut < 2 || nOut > xmrMaxTxOutputs {
		return tx, fmt.Errorf("invalid output count %d", nOut)
	}
	for i := uint64(0); i < nOut && r.err == nil; i++ {
		if amount := r.varint(); amount != 0 {
			return tx, fmt.Errorf("output %d has a cleartext amount", i)
		}
		var out xmrTxOutput
		switch tag := r.byte(); tag {
		case xmrOutputTagKey:
			copy(out.key[:], r.bytes(32))
		case xmrOutputTagTaggedKey:
			copy(out.key[:], r.bytes(32))
			viewTag := r.byte()
			out.viewTag = &viewTag
		default:
			return tx, fmt.Errorf("output %d has unsupported type %d", i, tag)
		}
		tx.outputs = append(tx.outputs, out)
	}
	if r.err != nil {
		return tx, r.err
	}
	extraLen := r.varint()
	if extraLen > xmrMaxExtraSize {
		return tx, fmt.Errorf("extra is %d bytes", extraLen)
	}
	tx.extra = append([]byte(nil), r.bytes(int(extraLen))...)
	prefixEnd := r.pos

	switch rctType := r.byte(); rctType {
	case xmrRctClsagBulletproof, xmrRctClsagBulletproofPP:
	default:
		return tx, fmt.Errorf("unsupported rct type %d", rctType)
	}
	tx.fee = r.varint()
	for i := uint64(0); i < nOut; i++ {
		var e [8]byte
		copy(e[:], r.bytes(8))
		tx.ecdhInfo = append(tx.ecdhInfo, e)
	}
	for i := uint64(0); i < nOut; i++ {
		var c [32]byte
		copy(c[:], r.bytes(32))
		tx.commitment = append(tx.commitment, c)
	}
	if r.err != nil {
		return tx, r.err
	}
	baseEnd := r.pos
	if baseEnd >= len(raw) {
		return tx, errors.New("transaction has no prunable data")
	}
	prefixHash := xmrKeccak(raw[:prefixEnd])
	baseHash := xmrKeccak(raw[prefixEnd:baseEnd])
	prunableHash := xmrKeccak(raw[baseEnd:])
	id := xmrKeccak(append(append(prefixHash[:], baseHash[:]...), prunableHash[:]...))
	tx.hash = hex.EncodeToString(id[:])
	return tx, nil
}

// parseXMRExtra returns the single transaction public key and the memo carried
// in an arbitrary-data nonce. It fails closed on any field the sidecar does
// not produce.
func parseXMRExtra(extra []byte) ([32]byte, string, error) {
	var txPubKey [32]byte
	r := &xmrReader{b: extra}
	var pubKeys int
	var memos []string
	for r.pos < len(extra) && r.err == nil {
		switch tag := r.byte(); tag {
		case xmrExtraTagPublicKey:
			copy(txPubKey[:], r.bytes(32))
			pubKeys++
		case xmrExtraTagNonce:
			n := r.varint()
			if n > xmrMaxExtraNonceSize {
				return txPubKey, "", fmt.Errorf("extra nonce is %d bytes", n)
			}
			nonce := r.bytes(int(n))
			if len(nonce) > 0 && nonce[0] == xmrArbitraryDataMarker {
				data := nonce[1:]
				if len(data) == 0 {
					return txPubKey, "", errors.New("extra carries an empty memo field")
				}
				if !utf8.Valid(data) {
					return txPubKey, "", errors.New("memo is not valid UTF-8")
				}
				memos = append(memos, string(data))
			}
		case xmrExtraTagPublicKeys:
			if n := r.varint(); n != 0 {
				return txPubKey, "", errors.New("extra carries additional tx public keys")
			}
		default:
			return txPubKey, "", fmt.Errorf("extra has unsupported field %d", tag)
		}
	}
	if r.err != nil {
		return txPubKey, "", fmt.Errorf("malformed extra: %w", r.err)
	}
	if pubKeys != 1 {
		return txPubKey, "", fmt.Errorf("extra has %d tx public keys", pubKeys)
	}
	switch len(memos) {
	case 0:
		return txPubKey, "", nil
	case 1:
		return txPubKey, memos[0], nil
	default:
		return txPubKey, "", errors.New("extra carries multiple memo fields")
	}
}

type xmrReader struct {
	b   []byte
	pos int
	err error
}

func (r *xmrReader) bytes(n int) []byte {
	if r.err != nil {
		return make([]byte, n)
	}
	if n < 0 || r.pos+n > len(r.b) {
		r.err = errors.New("unexpected end of data")
		return make([]byte, n)
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out
}

func (r *xmrReader) byte() byte {
	return r.bytes(1)[0]
}

// varint reads a canonical (minimally encoded) Monero varint.
func (r *xmrReader) varint() uint64 {
	var v uint64
	for i := 0; i < 10; i++ {
		b := r.byte()
		if r.err != nil {
			return 0
		}
		if i == 9 && b > 1 {
			r.err = errors.New("varint overflow")
			return 0
		}
		v |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			if i > 0 && b == 0 {
				r.err = errors.New("non-canonical varint")
				return 0
			}
			return v
		}
	}
	r.err = errors.New("varint too long")
	return 0
}

func xmrAppendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func xmrKeccak(data []byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// xmrKeccakToScalar is monero-oxide's keccak256_to_scalar: keccak256 reduced
// mod l.
func xmrKeccakToScalar(data []byte) *edwards25519.Scalar {
	k := xmrKeccak(data)
	wide := make([]byte, 64)
	copy(wide, k[:])
	s, err := new(edwards25519.Scalar).SetUniformBytes(wide)
	if err != nil {
		panic(err) // 64-byte input never fails
	}
	return s
}
