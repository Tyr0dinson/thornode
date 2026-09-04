package generic

import "testing"

func TestScannerBlockReferenceFacade(t *testing.T) {
	state := &blockReferenceMemoryState{values: make(map[string][]byte)}
	scanner, err := NewScanner(state, 3, testRetention)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []BlockReference{
		{Height: 10, Hash: "10", Timestamp: 1_000},
		{Height: 11, Hash: "11", Timestamp: 1_100},
	} {
		if err = scanner.SetBlockReference(reference); err != nil {
			t.Fatal(err)
		}
	}

	resolved, err := scanner.ResolveBlockReference(1_050)
	if err != nil || resolved.Height != 10 {
		t.Fatalf("unexpected resolved reference: %+v err=%v", resolved, err)
	}
}
