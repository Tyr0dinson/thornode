package types

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	scalecodec "github.com/itering/scale.go"
	scaleTypes "github.com/itering/scale.go/types"
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
