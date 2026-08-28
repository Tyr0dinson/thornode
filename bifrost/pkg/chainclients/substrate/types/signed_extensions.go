package types

import "fmt"

// ExtrinsicLayout is an immutable, validated description of the runtime's
// ordered signed extensions.
type ExtrinsicLayout struct {
	extensions []SignedExtension
	kinds      []signedExtensionKind
}

type signedExtensionKind uint8

const (
	signedExtensionNoop signedExtensionKind = iota
	signedExtensionSpecVersion
	signedExtensionTxVersion
	signedExtensionGenesis
	signedExtensionMortality
	signedExtensionNonce
	signedExtensionPayment
	signedExtensionAssetPayment
	signedExtensionMetadataHash
)

// NewExtrinsicLayout builds an encoding layout from a configured extension
// list. Unknown extensions are rejected because this client would not know how
// to encode their explicit or additional-signed values.
func NewExtrinsicLayout(extensions []SignedExtension) (ExtrinsicLayout, error) {
	if len(extensions) == 0 {
		return ExtrinsicLayout{}, fmt.Errorf("empty signed extension layout")
	}

	seen := make(map[string]struct{}, len(extensions))
	kinds := make([]signedExtensionKind, 0, len(extensions))
	for i, extension := range extensions {
		if extension.Identifier == "" || extension.ExtraType == "" || extension.AdditionalType == "" {
			return ExtrinsicLayout{}, fmt.Errorf("incomplete signed extension at index %d", i)
		}
		kind, ok := signedExtensionKindFor(extension.Identifier)
		if !ok {
			return ExtrinsicLayout{}, fmt.Errorf("unsupported signed extension %q", extension.Identifier)
		}
		if _, ok := seen[extension.Identifier]; ok {
			return ExtrinsicLayout{}, fmt.Errorf("duplicate signed extension %q", extension.Identifier)
		}
		seen[extension.Identifier] = struct{}{}
		kinds = append(kinds, kind)
	}

	for _, required := range []string{
		"CheckSpecVersion",
		"CheckTxVersion",
		"CheckGenesis",
		"CheckMortality",
		"CheckNonce",
	} {
		if _, ok := seen[required]; !ok {
			return ExtrinsicLayout{}, fmt.Errorf("required signed extension %q not found", required)
		}
	}

	_, isStandardPayment := seen["ChargeTransactionPayment"]
	_, isAssetPayment := seen[chargeAssetTxPaymentExtensionID]
	if isStandardPayment == isAssetPayment {
		return ExtrinsicLayout{}, fmt.Errorf("signed extension layout must contain exactly one supported payment extension")
	}

	return ExtrinsicLayout{
		extensions: append([]SignedExtension(nil), extensions...),
		kinds:      kinds,
	}, nil
}

// Validate checks that runtime metadata has exactly the configured extension
// layout. Order matters for both the extrinsic body and signing payload.
func (l ExtrinsicLayout) Validate(actual []SignedExtension) error {
	if len(l.extensions) == 0 {
		return fmt.Errorf("empty signed extension layout")
	}
	if len(actual) != len(l.extensions) {
		return fmt.Errorf("invalid signed extension count: got %d, expected %d", len(actual), len(l.extensions))
	}

	for i := range l.extensions {
		expected := l.extensions[i]
		got := actual[i]
		if got.Identifier != expected.Identifier {
			return fmt.Errorf(
				"invalid signed extension at index %d: got %q, expected %q",
				i, got.Identifier, expected.Identifier,
			)
		}
		if got.ExtraType != expected.ExtraType {
			return fmt.Errorf(
				"invalid explicit type for signed extension %s: got %q, expected %q",
				got.Identifier, got.ExtraType, expected.ExtraType,
			)
		}
		if got.AdditionalType != expected.AdditionalType {
			return fmt.Errorf(
				"invalid additional type for signed extension %s: got %q, expected %q",
				got.Identifier, got.AdditionalType, expected.AdditionalType,
			)
		}
	}

	return nil
}

func signedExtensionKindFor(identifier string) (signedExtensionKind, bool) {
	switch identifier {
	case "AuthorizeCall", "CheckNonZeroSender",
		"CheckWeight",
		"PrevalidateAttests",
		"EthSetOrigin",
		"StorageWeightReclaim",
		"SudoTransactionExtension",
		"CheckShieldedTxValidity",
		"SubtensorTransactionExtension",
		"DrandPriority":
		return signedExtensionNoop, true
	case "CheckSpecVersion":
		return signedExtensionSpecVersion, true
	case "CheckTxVersion":
		return signedExtensionTxVersion, true
	case "CheckGenesis":
		return signedExtensionGenesis, true
	case "CheckMortality":
		return signedExtensionMortality, true
	case "CheckNonce":
		return signedExtensionNonce, true
	case "ChargeTransactionPayment":
		return signedExtensionPayment, true
	case chargeAssetTxPaymentExtensionID:
		return signedExtensionAssetPayment, true
	case "CheckMetadataHash":
		return signedExtensionMetadataHash, true
	default:
		return 0, false
	}
}
