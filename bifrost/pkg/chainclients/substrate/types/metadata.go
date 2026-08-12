package types

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	scalecodec "github.com/itering/scale.go"
	scaleTypes "github.com/itering/scale.go/types"
	"github.com/itering/scale.go/types/scaleBytes"
)

type Metadata struct {
	Modules   []scaleTypes.MetadataModules
	codec     *scaleTypes.MetadataStruct
	codecSpec int
}

// scale.go registers portable runtime types in package-global maps. All access
// goes through this lock so metadata refreshes cannot alter the registry while
// another chain is decoding events.
var scaleCodecRegistryMu sync.RWMutex

var scaleCodecSpecSequence atomic.Int64

func nextScaleCodecSpec() int {
	// Built-in scale.go type overrides use spec ranges below 100,000,000.
	// A unique higher value gives every parsed runtime its own type namespace.
	return int(1_000_000_000 + scaleCodecSpecSequence.Add(1))
}

func ParseMetadata(raw string) (_ *Metadata, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("metadata decode panicked: %v", r)
		}
	}()

	if len(raw) == 0 {
		return nil, fmt.Errorf("empty metadata")
	}

	raw = strings.TrimPrefix(raw, "0x")

	var data []byte
	data, err = hex.DecodeString(raw)
	if err != nil {
		return nil, err
	}

	decoder := scalecodec.MetadataDecoder{}
	decoder.Spec = nextScaleCodecSpec()
	decoder.Init(data)

	scaleCodecRegistryMu.Lock()
	defer scaleCodecRegistryMu.Unlock()
	err = decoder.Process()
	if err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &Metadata{
		Modules:   decoder.Metadata.Metadata.Modules,
		codec:     &decoder.Metadata,
		codecSpec: decoder.Spec,
	}, nil
}

// DecodeSignedExtension decodes one signed extension using its runtime metadata
// and returns the decoded value and number of bytes consumed.
func (m *Metadata) DecodeSignedExtension(name string, data []byte) (_ interface{}, consumed int, err error) {
	defer func() {
		recovered := recover()
		if recovered != nil {
			err = fmt.Errorf("signed extension %s decode panicked: %v", name, recovered)
		}
	}()

	if m == nil || m.codec == nil || m.codec.Extrinsic == nil {
		return nil, 0, fmt.Errorf("runtime metadata required to decode signed extension %s", name)
	}
	if len(data) == 0 {
		return nil, 0, fmt.Errorf("empty signed extension %s data", name)
	}

	var typeString string
	for _, extension := range m.codec.Extrinsic.SignedExtensions {
		if extension.Identifier != name {
			continue
		}
		typeString = extension.TypeString
		break
	}
	if typeString == "" {
		return nil, 0, fmt.Errorf("signed extension %s not found in runtime metadata", name)
	}

	// scale.go's ScaleBytes truncates reads at EOF instead of reporting them.
	// A guard byte makes a one-byte overread visible in the final offset.
	guarded := make([]byte, len(data)+1)
	copy(guarded, data)

	decoder := scaleTypes.ScaleDecoder{}
	decoder.Init(
		scaleBytes.ScaleBytes{Data: guarded},
		&scaleTypes.ScaleDecoderOption{Metadata: m.codec, Spec: m.codecSpec},
	)

	scaleCodecRegistryMu.RLock()
	defer scaleCodecRegistryMu.RUnlock()
	value := decoder.ProcessAndUpdateData(typeString)
	consumed = decoder.GetData().Offset
	if consumed <= 0 {
		return nil, 0, fmt.Errorf("signed extension %s decoder made no progress", name)
	}
	if consumed > len(data) {
		return nil, 0, fmt.Errorf("truncated signed extension %s data", name)
	}

	return value, consumed, nil
}
