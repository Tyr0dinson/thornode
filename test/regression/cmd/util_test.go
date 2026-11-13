package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestParseOpsKeepsLinesAligned(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		lines        []int
		failExport   bool
	}{
		{"plain", "type: create-blocks\ncount: 1\n---\ntype: create-blocks\ncount: 1\n", []int{0, 4}, false},
		{"expanded", "{{ range 3 }}type: create-blocks\ncount: 1\n---\n{{ end }}type: create-blocks\ncount: 1\n", []int{-1, -1, -1, -1}, false},
		{"conditional", "type: create-blocks\ncount: 1\n{{ if false }}\n---\ntype: create-blocks\ncount: 1\n{{ end }}", []int{-1}, false},
		{"env and sentinel", "type: env\nkey: TEST\nvalue: '1'\n---\ntype: state\ngenesis: {}\n---\ntype: create-blocks\ncount: 1\n---\ntype: fail-export-invariants\n", []int{5, 8}, true},
		{"env only", "type: env\nkey: TEST\nvalue: '1'\n", []int{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "suite.yaml")
			err := os.WriteFile(path, []byte(tc.source), 0o600)
			require.NoError(t, err)
			ops, lines, _, failExport := parseOps(zerolog.Nop(), path, template.New("suite"), nil)
			require.Equal(t, tc.lines, lines)
			require.Len(t, lines, len(ops))
			require.Equal(t, tc.failExport, failExport)
		})
	}
}

func TestRegressionSuitesHaveAlignedLinesAndDashInputs(t *testing.T) {
	tmpls := template.Must(template.New("").Funcs(funcMap).ParseGlob("../templates/*.yaml"))
	err := filepath.Walk("../suites", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}
		t.Run(path, func(t *testing.T) {
			ops, lines, _, _ := parseOps(zerolog.Nop(), path, template.Must(tmpls.Clone()), nil)
			require.Len(t, lines, len(ops))
			for _, op := range ops {
				inbound, ok := op.(*OpTxObservedIn)
				if !ok {
					continue
				}
				for _, tx := range inbound.Txs {
					if tx.Tx.Chain.Equals(common.DASHChain) {
						require.NoError(t, common.ValidateDashInputs(tx.SpentOutputRefs), tx.Tx.ID)
					}
				}
			}
		})
		return nil
	})
	require.NoError(t, err)
}

func TestAutoUpdateRequiresSourceLine(t *testing.T) {
	t.Setenv("AUTO_UPDATE", "1")
	previous := templates
	templates = template.New("")
	t.Cleanup(func() { templates = previous })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"value":2}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "suite.yaml")
	original := "asserts:\n  - .value == 1\n"
	err := os.WriteFile(path, []byte(original), 0o600)
	require.NoError(t, err)
	op := &OpCheck{Endpoint: strings.Replace(server.URL, "127.0.0.1", "localhost", 1), Status: http.StatusOK, Asserts: []string{".value == 1"}}
	err = op.Execute(io.Discard, path, -1, 0, nil, nil)
	require.EqualError(t, err, "cannot auto-update checks without a source line")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(data))
}
