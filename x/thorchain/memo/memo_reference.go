package thorchain

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/v3/common"
)

type ReferenceWriteMemo struct {
	MemoBase
	Chain         common.Chain
	Memo          string
	ReferenceHash common.TxID
}

func (m ReferenceWriteMemo) GetChain() common.Chain        { return m.Chain }
func (m ReferenceWriteMemo) GetMemo() string               { return m.Memo }
func (m ReferenceWriteMemo) GetReferenceHash() common.TxID { return m.ReferenceHash }

func NewReferenceWriteMemo(hash common.TxID, chain common.Chain, memo string) ReferenceWriteMemo {
	return ReferenceWriteMemo{
		MemoBase:      MemoBase{TxType: TxReferenceWriteMemo},
		Chain:         chain,
		Memo:          memo,
		ReferenceHash: hash,
	}
}

func (p *parser) ParseReferenceWriteMemo() (ReferenceWriteMemo, error) {
	switch {
	case p.version.GTE(semver.MustParse("1.129.0")):
		return p.ParseReferenceWriteMemoV129()
	default:
		return ReferenceWriteMemo{}, fmt.Errorf("invalid version(%s)", p.version.String())
	}
}

func (p *parser) ParseReferenceWriteMemoV129() (ReferenceWriteMemo, error) {
	chain := p.getChain(1, true, common.EmptyChain)
	var refHash common.TxID
	if len(p.parts) > 2 {
		parts := strings.SplitN(p.memo, ":", 4)
		if len(parts) > 2 {
			var err error
			refHash, err = common.NewTxID(parts[2])
			if err != nil {
				return ReferenceWriteMemo{}, fmt.Errorf("fail to parse reference hash (%s): %w", parts[2], err)
			}
		}
	}
	var memo string
	if len(p.parts) > 3 {
		parts := strings.SplitN(p.memo, ":", 4)
		if len(parts) > 3 {
			memo = parts[3]
		}
	}
	if len(memo) == 0 {
		return ReferenceWriteMemo{}, fmt.Errorf("memo cannot be blank")
	}
	return NewReferenceWriteMemo(refHash, chain, memo), p.Error()
}

type ReferenceReadMemo struct {
	MemoBase
	Reference string
}

func (m ReferenceReadMemo) GetReference() string { return m.Reference }

func NewReferenceReadMemo(ref string) ReferenceReadMemo {
	return ReferenceReadMemo{
		MemoBase:  MemoBase{TxType: TxReferenceReadMemo},
		Reference: ref,
	}
}

func (p *parser) ParseReferenceReadMemo() (ReferenceReadMemo, error) {
	switch {
	case p.version.GTE(semver.MustParse("1.129.0")):
		return p.ParseReferenceReadMemoV129()
	default:
		return ReferenceReadMemo{}, fmt.Errorf("invalid version(%s)", p.version.String())
	}
}

func (p *parser) ParseReferenceReadMemoV129() (ReferenceReadMemo, error) {
	ref := p.getString(1, true, "")
	return NewReferenceReadMemo(ref), p.Error()
}
