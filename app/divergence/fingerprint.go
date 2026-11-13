package divergence

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/cosmos/cosmos-sdk/version"

	servertypes "github.com/cosmos/cosmos-sdk/server/types"

	thornodeconfig "gitlab.com/thorchain/thornode/v3/config"
)

// fingerprintKeys are the app.toml / config.toml keys that change how state is
// read, cached, pruned or served, i.e. that a node-local divergence could
// plausibly depend on.
var fingerprintKeys = []string{
	"inter-block-cache",
	"iavl-cache-size",
	"iavl-disable-fastnode",
	"iavl-lazy-loading",
	"app-db-backend",
	"app-db-block-cache-mib",
	"db_backend",
	"pruning",
	"pruning-keep-recent",
	"pruning-interval",
	"min-retain-blocks",
	"halt-height",
	"query-gas-limit",
	"api.enable",
	"api.query-via-grpc",
	"grpc.enable",
	"grpc-web.enable",
	"state-sync.snapshot-interval",
	"state-sync.snapshot-keep-recent",
	"statesync.enable",
	"telemetry.enabled",
	"wasm.query_gas_limit",
	"wasm.memory_cache_size",
	"ebifrost.enable",
	"streaming.abci.keys",
	FlagEnabled,
	FlagRetainBlocks,
}

// fingerprintEnv are process env vars that change runtime behaviour.
var fingerprintEnv = []string{"GOGC", "GOMEMLIMIT", "GOMAXPROCS", "GODEBUG"}

// Fingerprint returns the consensus-adjacent configuration of this node:
// app options, THORNode telemetry flags and runtime env. Unset keys are
// reported as "<unset>" so two fingerprints can be compared key by key.
func Fingerprint(opts servertypes.AppOptions) map[string]string {
	fp := make(map[string]string, len(fingerprintKeys)+16)
	for _, k := range fingerprintKeys {
		v := any(nil)
		if opts != nil {
			v = opts.Get(k)
		}
		if v == nil {
			fp[k] = "<unset>"
			continue
		}
		fp[k] = fmt.Sprint(v)
	}

	tn := thornodeconfig.GetThornode()
	fp["thornode.telemetry.slash_points"] = fmt.Sprint(tn.Telemetry.SlashPoints)
	fp["thornode.telemetry.price_per_node"] = fmt.Sprint(tn.Telemetry.PricePerNode)
	fp["thornode.telemetry.trace_managers"] = fmt.Sprint(tn.Telemetry.TraceManagers)
	fp["thornode.telemetry.trace_handlers"] = fmt.Sprint(tn.Telemetry.TraceHandlers)
	fp["thornode.telemetry.trace_contracts"] = fmt.Sprint(tn.Telemetry.TraceContracts)
	fp["thornode.telemetry.trace_payloads"] = fmt.Sprint(tn.Telemetry.TracePayloads)
	fp["thornode.api.logs.retain_blocks"] = fmt.Sprint(tn.API.Logs.RetainBlocks)
	fp["thornode.cosmos.telemetry.enabled"] = fmt.Sprint(tn.Cosmos.Telemetry.Enabled)

	for _, k := range fingerprintEnv {
		if v, ok := os.LookupEnv(k); ok {
			fp["env."+k] = v
		} else {
			fp["env."+k] = "<unset>"
		}
	}
	fp["runtime.gomaxprocs"] = fmt.Sprint(runtime.GOMAXPROCS(0))
	fp["runtime.numcpu"] = fmt.Sprint(runtime.NumCPU())
	return fp
}

// BuildInfo identifies the running binary.
type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTags string `json:"build_tags"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	// VCSRevision is the vcs.revision build setting, when the binary has it.
	VCSRevision string `json:"vcs_revision,omitempty"`
}

// CurrentBuild returns the running binary's build info.
func CurrentBuild() BuildInfo {
	b := BuildInfo{
		Version:   version.Version,
		Commit:    version.Commit,
		BuildTags: version.BuildTags,
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				b.VCSRevision = s.Value
			}
		}
	}
	return b
}
