package types

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	gateway "github.com/cosmos/gogogateway"

	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

func TestMimirVotesRequiredJSONPBClient(t *testing.T) {
	marshaler := gateway.JSONPb{EmitDefaults: true, OrigName: true}
	body, err := marshaler.Marshal(&QueryMimirVotesRequiredResponse{
		ActiveNodeCount: 5,
		Mimirs:          []MimirVoteRequired{{Key: "ADR001", Votes: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/thorchain/mimir/votes_required" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	config := openapi.NewConfiguration()
	config.Servers = openapi.ServerConfigurations{{URL: server.URL}}
	client := openapi.NewAPIClient(config)
	result, response, err := client.MimirApi.MimirVotesRequired(context.Background()).Execute()
	if response != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatalf("generated client cannot decode gateway JSON %s: %v", body, err)
	}
	if result.ActiveNodeCount != 5 || len(result.Mimirs) != 1 || result.Mimirs[0].Key != "ADR001" || result.Mimirs[0].Votes != 2 {
		t.Fatalf("unexpected response: %+v", result)
	}
}
