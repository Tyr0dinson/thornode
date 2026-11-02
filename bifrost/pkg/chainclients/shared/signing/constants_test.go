package signing

import (
	"errors"
	"testing"

	"gitlab.com/thorchain/thornode/v3/constants"
)

// changingReader halves the block time after each of the first `changes`
// constants reads, as when the bridge cache refreshes between two requests.
type changingReader struct {
	changes, reads int
	err            error
}

func (r *changingReader) GetConstants() (map[string]int64, error) {
	r.reads++
	return map[string]int64{"SigningTransactionPeriod": 300 << min(r.reads-1, r.changes)}, r.err
}

func (r *changingReader) GetBlockTime() (int64, constants.BlockTimeHistory, error) {
	n := min(r.reads, r.changes)
	ref := constants.ThorchainBlockTime.Milliseconds()
	var history constants.BlockTimeHistory
	for i := range n {
		history = append(history, constants.BlockTimeSegment{Height: int64(1000 + i), Ms: ref >> (i + 1)})
	}
	return ref >> n, history, nil
}

func TestReadConstants(t *testing.T) {
	ref := constants.ThorchainBlockTime.Milliseconds()

	// no change: one read
	r := &changingReader{}
	values, ms, history, err := ReadConstants(r)
	if err != nil || r.reads != 1 || ms != ref || len(history) != 0 || values["SigningTransactionPeriod"] != 300 {
		t.Fatalf("no change: reads=%d ms=%d history=%v values=%v err=%v", r.reads, ms, history, values, err)
	}

	// the block time halves between the constants and the second block time
	// read: the old period (300) is not paired with the new block time; the
	// retry returns the new period with it
	r = &changingReader{changes: 1}
	values, ms, history, err = ReadConstants(r)
	if err != nil || r.reads != 2 || ms != ref/2 || len(history) != 1 || values["SigningTransactionPeriod"] != 600 {
		t.Fatalf("one change: reads=%d ms=%d history=%v values=%v err=%v", r.reads, ms, history, values, err)
	}

	// still changing after three attempts: an error, not a mixed snapshot
	r = &changingReader{changes: 3}
	if _, _, _, err = ReadConstants(r); err == nil {
		t.Fatal("expected an error while the block time keeps changing")
	}

	// a failed read is returned
	r = &changingReader{err: errors.New("down")}
	if _, _, _, err = ReadConstants(r); err == nil {
		t.Fatal("expected the constants error")
	}
}

func TestSameRevision(t *testing.T) {
	a := constants.BlockTimeHistory{{Height: 10, Ms: 500}}
	b := constants.BlockTimeHistory{{Height: 10, Ms: 500}, {Height: 20, Ms: 1000}}
	if !SameRevision(nil, nil) || !SameRevision(a, a) || SameRevision(a, b) || SameRevision(a, constants.BlockTimeHistory{{Height: 11, Ms: 500}}) {
		t.Fatal("SameRevision")
	}
}
