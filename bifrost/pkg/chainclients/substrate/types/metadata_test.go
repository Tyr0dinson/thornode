package types

import (
	"os"
	"strings"
	"testing"

	scaleTypes "github.com/itering/scale.go/types"
	"github.com/itering/scale.go/types/scaleBytes"
	. "gopkg.in/check.v1"
)

func TestMetadataSuite(t *testing.T) { TestingT(t) }

type MetadataSuite struct{}

var _ = Suite(&MetadataSuite{})

// ---------------------------------------------------------------------------
// ParseMetadata — golden vectors from real mainnet RPCs
// ---------------------------------------------------------------------------

// To refresh the fixtures (after a runtime upgrade or a scale.go library bump),
// re-capture from the public RPCs and commit the new files:
//
//	curl -sS -H 'Content-Type: application/json' \
//	  -d '{"jsonrpc":"2.0","id":1,"method":"state_getMetadata","params":[]}' \
//	  https://polkadot-asset-hub-rpc.polkadot.io | jq -r .result \
//	  > testdata/metadata-dot-assethub-<spec_version>.hex
//
//	curl -sS -H 'Content-Type: application/json' \
//	  -d '{"jsonrpc":"2.0","id":1,"method":"state_getMetadata","params":[]}' \
//	  https://rpc.polkadot.io | jq -r .result \
//	  > testdata/metadata-dot-mainnet-<spec_version>.hex
//
//	curl -sS -H 'Content-Type: application/json' \
//	  -d '{"jsonrpc":"2.0","id":1,"method":"state_getMetadata","params":[]}' \
//	  https://entrypoint-finney.opentensor.ai:443 | jq -r .result \
//	  > testdata/metadata-tao-mainnet-<spec_version>.hex
//
// Find the current spec_version by swapping state_getMetadata for
// state_getRuntimeVersion and reading .result.specVersion. Update the
// fixturePath entries below (and the equivalents in params_test.go's
// TestValidateMetadataAgainstFixture) to match the new filenames.
func (s *MetadataSuite) TestParseMetadataGoldenVectors(c *C) {
	tests := []struct {
		name        string
		fixturePath string
	}{
		{
			name:        "DOT Asset Hub 2003001",
			fixturePath: "testdata/metadata-dot-assethub-2003001.hex",
		},
		{
			// Relay-chain metadata: kept as a ParseMetadata golden vector and
			// as the wrong-runtime input for params_mainnet_test.go
			name:        "DOT relay mainnet v2.2.1",
			fixturePath: "testdata/metadata-dot-mainnet-2002001.hex",
		},
		{
			name:        "TAO mainnet v3.3.14-401",
			fixturePath: "testdata/metadata-tao-mainnet-401.hex",
		},
	}

	for _, tt := range tests {
		raw, err := os.ReadFile(tt.fixturePath)
		c.Assert(err, IsNil, Commentf("%s: read fixture", tt.name))

		md, err := ParseMetadata(strings.TrimSpace(string(raw)))
		c.Assert(err, IsNil, Commentf("%s: ParseMetadata", tt.name))
		c.Assert(md, NotNil, Commentf("%s: nil metadata", tt.name))
		c.Check(len(md.Modules) > 0, Equals, true,
			Commentf("%s: expected non-empty modules, got 0", tt.name))
		c.Check(len(md.SignedExtensions) > 0, Equals, true,
			Commentf("%s: expected signed extensions", tt.name))
		for i, extension := range md.SignedExtensions {
			c.Check(extension.Identifier, Not(Equals), "", Commentf("%s extension %d identifier", tt.name, i))
			c.Check(extension.ExtraType, Not(Equals), "", Commentf("%s extension %d explicit type", tt.name, i))
			c.Check(extension.AdditionalType, Not(Equals), "", Commentf("%s extension %d additional type", tt.name, i))
		}
	}
}

func (s *MetadataSuite) TestDecodeChargeAssetTxPayment(c *C) {
	metadata := loadAssetHubTestMetadata(c)

	none := []byte{0x00, 0x00} // compact tip 0, Option<Location>::None
	_, consumed, err := metadata.DecodeSignedExtension(chargeAssetTxPaymentExtensionID, none)
	c.Assert(err, IsNil)
	c.Check(consumed, Equals, len(none))

	// compact tip 0, Some(Location::new(0, [PalletInstance(50), GeneralIndex(1984)]))
	some := []byte{0x00, 0x01, 0x00, 0x02, 0x04, 50, 0x05}
	some = append(some, EncodeCompact(1984)...)
	_, consumed, err = metadata.DecodeSignedExtension(chargeAssetTxPaymentExtensionID, some)
	c.Assert(err, IsNil)
	c.Check(consumed, Equals, len(some))

	_, _, err = metadata.DecodeSignedExtension(chargeAssetTxPaymentExtensionID, some[:len(some)-1])
	c.Assert(err, NotNil)
	c.Check(strings.Contains(err.Error(), "truncated"), Equals, true, Commentf("got: %v", err))
}

func (s *MetadataSuite) TestSupportedNoopExtensionsEncodeNoBytes(c *C) {
	fixtures := []string{
		"testdata/metadata-dot-assethub-2003001.hex",
		"testdata/metadata-tao-mainnet-401.hex",
	}
	noop := map[string]struct{}{
		"AuthorizeCall":                 {},
		"CheckNonZeroSender":            {},
		"CheckWeight":                   {},
		"PrevalidateAttests":            {},
		"EthSetOrigin":                  {},
		"StorageWeightReclaim":          {},
		"SudoTransactionExtension":      {},
		"CheckShieldedTxValidity":       {},
		"SubtensorTransactionExtension": {},
		"DrandPriority":                 {},
	}

	for _, fixture := range fixtures {
		raw, err := os.ReadFile(fixture)
		c.Assert(err, IsNil)
		metadata, err := ParseMetadata(strings.TrimSpace(string(raw)))
		c.Assert(err, IsNil)

		for _, extension := range metadata.SignedExtensions {
			if _, ok := noop[extension.Identifier]; !ok {
				continue
			}
			decoder := scaleTypes.ScaleDecoder{}
			decoder.Init(
				scaleBytes.ScaleBytes{Data: []byte{0xFF}},
				&scaleTypes.ScaleDecoderOption{Metadata: metadata.codec, Spec: metadata.codecSpec},
			)
			scaleCodecRegistryMu.RLock()
			decoder.ProcessAndUpdateData(extension.ExtraType)
			consumed := decoder.GetData().Offset
			scaleCodecRegistryMu.RUnlock()
			c.Check(consumed, Equals, 0, Commentf("%s %s encoded bytes", fixture, extension.Identifier))
		}
	}
}

// ---------------------------------------------------------------------------
// ParseMetadata — defensive input handling (incl. panic-recovery path)
// ---------------------------------------------------------------------------

func (s *MetadataSuite) TestParseMetadataInputErrors(c *C) {
	tests := []struct {
		name      string
		raw       string
		wantErrIn string // "" means any error
	}{
		{
			name:      "empty string",
			raw:       "",
			wantErrIn: "empty",
		},
		{
			name:      "non-hex characters",
			raw:       "zzz",
			wantErrIn: "",
		},
		{
			name:      "wrong magic prefix",
			raw:       "0x00010203deadbeef",
			wantErrIn: "",
		},
		{
			// `meta` magic but no version/body bytes — exercises the
			// defer-recover path; without it this would panic.
			name:      "truncated after magic",
			raw:       "0x6d657461",
			wantErrIn: "",
		},
	}

	for _, tt := range tests {
		_, err := ParseMetadata(tt.raw)
		c.Check(err, NotNil, Commentf("%s: expected error", tt.name))
		if err == nil {
			continue
		}
		if tt.wantErrIn != "" {
			c.Check(strings.Contains(err.Error(), tt.wantErrIn), Equals, true,
				Commentf("%s: error %q missing %q", tt.name, err.Error(), tt.wantErrIn))
		}
	}
}
