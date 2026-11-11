// Package divergence records node-local evidence of every committed block so
// that, when this node's app hash diverges from the network's, the crash
// itself leaves enough on disk to find the exact diverging store and keys.
//
// Nothing here touches consensus state: the recorder is a streaming
// ABCIListener (below the gas meter, after Commit), every write goes to files
// under <home>/data/divergence (crash bundles under <home>/divergence), and
// every error is logged and swallowed.
package divergence

import (
	"fmt"
	"strings"

	"github.com/spf13/cast"

	servertypes "github.com/cosmos/cosmos-sdk/server/types"
)

const (
	FlagEnabled      = "divergence.enabled"
	FlagRetainBlocks = "divergence.retain_blocks"
	FlagReferenceAPI = "divergence.reference_api"

	// DefaultRetainBlocks is how many blocks of full write sets are kept.
	DefaultRetainBlocks = 20
	// maxRetainBlocks caps retain_blocks so a typo cannot fill the disk.
	maxRetainBlocks = 10_000
)

// Config is the app.toml [divergence] section.
type Config struct {
	// Enabled turns the recorder, crash bundle and write-set endpoint on.
	Enabled bool `mapstructure:"enabled"`
	// RetainBlocks is how many recent blocks keep a full write set on disk.
	RetainBlocks int `mapstructure:"retain_blocks"`
	// ReferenceAPI is an optional THORNode API base URL (e.g.
	// http://host:1317) that the crash hook diffs against.
	ReferenceAPI string `mapstructure:"reference_api"`
}

// DefaultConfig returns the defaults: enabled, 20 blocks, no reference.
func DefaultConfig() Config {
	return Config{Enabled: true, RetainBlocks: DefaultRetainBlocks}
}

// ConfigTemplate renders the [divergence] app.toml section for c.
func ConfigTemplate(c Config) string {
	return fmt.Sprintf(`
###############################################################################
###                    Divergence Diagnostics Configuration                 ###
###############################################################################

[divergence]
# Record per-block store digests and the last retain_blocks write sets under
# <home>/data/divergence. On any consensus failure, and on a blocksync app hash
# mismatch, write a crash bundle to <home>/divergence (outside data/, so a node
# reset keeps it).
# Node-local only: it never changes state or gas.
enabled = %t

# Number of recent blocks whose full write set is kept on disk.
retain_blocks = %d

# Optional THORNode API base URL (e.g. "http://reference-node:1317") that the
# crash hook diffs the diverging height against. Empty disables the diff.
reference_api = "%s"
`, c.Enabled, c.RetainBlocks, c.ReferenceAPI)
}

// DefaultConfigTemplate renders the [divergence] section with defaults.
func DefaultConfigTemplate() string {
	return ConfigTemplate(DefaultConfig())
}

// ReadConfig reads the [divergence] section, falling back to the defaults for
// unset keys and clamping retain_blocks into [1, 10000].
func ReadConfig(opts servertypes.AppOptions) (Config, error) {
	cfg := DefaultConfig()
	if opts == nil {
		return cfg, nil
	}
	var err error
	if v := opts.Get(FlagEnabled); v != nil {
		if cfg.Enabled, err = cast.ToBoolE(v); err != nil {
			return cfg, fmt.Errorf("%s: %w", FlagEnabled, err)
		}
	}
	if v := opts.Get(FlagRetainBlocks); v != nil {
		if cfg.RetainBlocks, err = cast.ToIntE(v); err != nil {
			return cfg, fmt.Errorf("%s: %w", FlagRetainBlocks, err)
		}
	}
	if v := opts.Get(FlagReferenceAPI); v != nil {
		if cfg.ReferenceAPI, err = cast.ToStringE(v); err != nil {
			return cfg, fmt.Errorf("%s: %w", FlagReferenceAPI, err)
		}
	}
	cfg.ReferenceAPI = strings.TrimSpace(cfg.ReferenceAPI)
	if cfg.RetainBlocks < 1 {
		cfg.RetainBlocks = 1
	}
	if cfg.RetainBlocks > maxRetainBlocks {
		cfg.RetainBlocks = maxRetainBlocks
	}
	return cfg, nil
}
