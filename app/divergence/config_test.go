package divergence

import (
	"go/ast"
	"go/parser"
	"go/token"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

type mapOpts map[string]any

func (m mapOpts) Get(k string) any { return m[k] }

func TestReadConfig(t *testing.T) {
	cfg, err := ReadConfig(nil)
	require.NoError(t, err)
	require.Equal(t, DefaultConfig(), cfg)

	cfg, err = ReadConfig(mapOpts{})
	require.NoError(t, err)
	require.Equal(t, Config{Enabled: true, RetainBlocks: 20}, cfg)

	cfg, err = ReadConfig(mapOpts{FlagEnabled: "false", FlagRetainBlocks: "7", FlagReferenceAPI: " http://ref:1317 "})
	require.NoError(t, err)
	require.Equal(t, Config{Enabled: false, RetainBlocks: 7, ReferenceAPI: "http://ref:1317"}, cfg)

	cfg, err = ReadConfig(mapOpts{FlagRetainBlocks: 0})
	require.NoError(t, err)
	require.Equal(t, 1, cfg.RetainBlocks)
	cfg, err = ReadConfig(mapOpts{FlagRetainBlocks: 1 << 30})
	require.NoError(t, err)
	require.Equal(t, maxRetainBlocks, cfg.RetainBlocks)

	_, err = ReadConfig(mapOpts{FlagEnabled: "maybe"})
	require.ErrorContains(t, err, FlagEnabled)
	_, err = ReadConfig(mapOpts{FlagRetainBlocks: "many"})
	require.ErrorContains(t, err, FlagRetainBlocks)
	_, err = ReadConfig(mapOpts{FlagReferenceAPI: []int{1}})
	require.ErrorContains(t, err, FlagReferenceAPI)
}

// TestConfigTemplateParses renders the template, reads it as app.toml would be
// read, and gets the same config back.
func TestConfigTemplateParses(t *testing.T) {
	for _, want := range []Config{DefaultConfig(), {Enabled: false, RetainBlocks: 50, ReferenceAPI: "http://r:1317"}} {
		v := viper.New()
		v.SetConfigType("toml")
		require.NoError(t, v.ReadConfig(strings.NewReader(ConfigTemplate(want))))
		got, err := ReadConfig(v)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	require.Contains(t, DefaultConfigTemplate(), "[divergence]")
}

// TestAppTomlTemplateHasSection checks that config/app.toml.tmpl, which
// render-config writes, carries the same keys as ConfigTemplate.
func TestAppTomlTemplateHasSection(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	src := readRepoFile(t, file, "config/app.toml.tmpl")
	require.Contains(t, src, "[divergence]")
	require.Contains(t, src, "enabled = {{ .Divergence.Enabled }}")
	require.Contains(t, src, "retain_blocks = {{ .Divergence.RetainBlocks }}")
	require.Contains(t, src, `reference_api = "{{ .Divergence.ReferenceAPI }}"`)
}

func TestDecodeKey(t *testing.T) {
	ki := DecodeKey("thorchain", []byte("pool//BTC.BTC"))
	require.Equal(t, KeyInfo{Prefix: "pool/", PrefixName: "prefixPool", Text: "pool//BTC.BTC"}, ki)

	ki = DecodeKey("thorchain", []byte("unknown/x"))
	require.Equal(t, "unknown/", ki.Prefix)
	require.Empty(t, ki.PrefixName)

	ki = DecodeKey("bank", []byte("pool//BTC.BTC"))
	require.Empty(t, ki.PrefixName)
	require.Equal(t, "pool/", ki.Prefix)

	require.Equal(t, KeyInfo{}, DecodeKey("bank", []byte{0x02, 0x14, 0xff}))
	require.Equal(t, KeyInfo{}, DecodeKey("thorchain", nil))
	require.Equal(t, KeyInfo{Text: "noslash"}, DecodeKey("thorchain", []byte("noslash")))
	require.Equal(t, KeyInfo{}, DecodeKey("thorchain", []byte("a\x7f/")))
}

// TestThorchainPrefixesMatchKeeper parses x/thorchain/keeper/v1/keeper.go and
// checks thorchainPrefixes is an exact copy of its DbPrefix constants.
func TestThorchainPrefixesMatchKeeper(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	src := readRepoFile(t, file, "x/thorchain/keeper/v1/keeper.go")
	f, err := parser.ParseFile(token.NewFileSet(), "keeper.go", src, 0)
	require.NoError(t, err)

	want := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			require.True(t, ok)
			sel, ok := vs.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "DbPrefix" {
				continue
			}
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				require.True(t, ok)
				v, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				want[v] = name.Name
			}
		}
	}
	require.NotEmpty(t, want)
	require.Equal(t, want, thorchainPrefixes)
}
