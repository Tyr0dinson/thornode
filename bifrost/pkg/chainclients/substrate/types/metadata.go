package types

import (
	"encoding/hex"
	"fmt"
	"strings"

	scalecodec "github.com/itering/scale.go"
	"github.com/itering/scale.go/types"
)

type Metadata struct {
	Modules []types.MetadataModules
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
	decoder.Init(data)

	err = decoder.Process()
	if err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &Metadata{Modules: decoder.Metadata.Metadata.Modules}, nil
}
