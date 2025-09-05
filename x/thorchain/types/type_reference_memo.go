package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
)

func NewReferenceMemo(chain common.Chain, memo, reference string, height int64) ReferenceMemo {
	return ReferenceMemo{
		Chain:     chain,
		Memo:      memo,
		Reference: reference,
		Height:    height,
	}
}

func (ref ReferenceMemo) Key() string {
	return fmt.Sprintf("%s/%s", ref.Chain, ref.Reference)
}

func (ref ReferenceMemo) IsExpired(height, ttl int64) bool {
	return ref.Height+ttl < height
}
