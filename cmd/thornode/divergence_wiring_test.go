package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cosmossdk.io/log/v2"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/app/divergence"
)

// TestNewAppEnablesDivergence checks the start command's app creator turns the
// recorder on by default and honours enabled = false.
func TestNewAppEnablesDivergence(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		divergence.SetActive(nil)
		home := t.TempDir()
		opts := simtestutil.AppOptionsMap{
			flags.FlagHome:         home,
			flags.FlagChainID:      "divergence-wiring",
			app.TestApp:            true,
			divergence.FlagEnabled: enabled,
			"pruning":              "nothing",
		}
		a := newApp(log.NewNopLogger(), dbm.NewMemDB(), opts)
		_, err := os.Stat(filepath.Join(divergence.DataDir(home), "digests.log"))
		if enabled {
			if divergence.Active() == nil || err != nil {
				t.Fatalf("enabled: active=%v stat err=%v", divergence.Active(), err)
			}
		} else if divergence.Active() != nil || err == nil {
			t.Fatalf("disabled: active=%v stat err=%v", divergence.Active(), err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
	}
	divergence.SetActive(nil)
}

func TestInitAppConfigHasDivergence(t *testing.T) {
	tmpl, cfg := initAppConfig()
	if !strings.Contains(tmpl, "[divergence]") || !strings.Contains(tmpl, "retain_blocks = 20") {
		t.Fatal("app.toml template lacks the [divergence] section")
	}
	if !strings.Contains(fmt.Sprintf("%+v", cfg), "Divergence:{Enabled:true RetainBlocks:20 ReferenceAPI:}") {
		t.Fatalf("custom app config lacks the Divergence defaults: %+v", cfg)
	}
}
