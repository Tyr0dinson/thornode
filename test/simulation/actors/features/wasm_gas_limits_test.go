package features

import (
	rand "math/rand/v2"
	"testing"
)

func TestWasmGasLimitsActorShape(t *testing.T) {
	actor := WasmGasLimits(rand.New(rand.NewPCG(1, 2)))
	if actor.Name != "Feature-WasmGasLimits" {
		t.Fatalf("unexpected actor name: %s", actor.Name)
	}
	if len(actor.Ops) != 17 {
		t.Fatalf("expected complete configure-test-restore lifecycle, got %d ops", len(actor.Ops))
	}
}

func TestTxEventAttribute(t *testing.T) {
	var tx cosmosTxResponse
	tx.TxResponse.Events = append(tx.TxResponse.Events, cosmosEvent{
		Type:       "instantiate",
		Attributes: []cosmosAttribute{{Key: "_contract_address", Value: "tthor1contract"}},
	})

	if got := txEventAttribute(tx, "instantiate", "_contract_address"); got != "tthor1contract" {
		t.Fatalf("unexpected contract address: %q", got)
	}
	if got := txEventAttribute(tx, "store_code", "code_id"); got != "" {
		t.Fatalf("expected missing attribute, got %q", got)
	}
}
