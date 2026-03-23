package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve caller path")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()

	path := filepath.Join(repoRoot(t), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", rel, err)
	}
	return string(data)
}

func TestLiteClientConstructorsCoverSimChains(t *testing.T) {
	for _, chain := range acommon.SimChains {
		if _, ok := liteClientConstructors[chain]; !ok {
			t.Fatalf("missing lite client constructor for sim chain %s", chain)
		}
	}
}

func TestChainRPCsCoverSimChains(t *testing.T) {
	for _, chain := range acommon.SimChains {
		if _, ok := chainRPCs[chain]; !ok {
			t.Fatalf("missing chain RPC entry for sim chain %s", chain)
		}
	}
}

func TestMocknetFundingSupportsXMR(t *testing.T) {
	mocknetGo := readRepoFile(t, "test/simulation/cmd/mocknet.go")
	if !strings.Contains(mocknetGo, "case common.XMRChain:") {
		t.Fatal("mocknet funding switch does not seed XMR simulation users")
	}
}

func TestThorUserFundingPlanKeepsExistingRuneUsersInLaterChainSeeding(t *testing.T) {
	seedThor, seedChains := thorUserFundingPlan(common.NewCoins(common.NewCoin(common.RuneAsset(), sdkmath.NewUint(common.One))))
	if seedThor {
		t.Fatal("users with existing RUNE should not receive another THOR seed transfer")
	}
	if !seedChains {
		t.Fatal("users with existing RUNE must still be included in later L1 chain seeding on reruns")
	}
}

func TestMocknetWaitsForXMRMasterAccountReadiness(t *testing.T) {
	mocknetGo := readRepoFile(t, "test/simulation/cmd/mocknet.go")

	if strings.Contains(mocknetGo, "account, err := master.ChainClients[chain].GetAccount(nil)") {
		t.Fatal("mocknet should not call GetAccount(nil) directly when logging master balances; XMR startup can transiently report block height 0")
	}
	if !strings.Contains(mocknetGo, "isRetryableXMRStartupError") {
		t.Fatal("mocknet should recognize and retry the transient XMR block-0 startup error before failing master account discovery")
	}
}

func TestMocknetMoneroDaemonRunsOffline(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/monero/daemon-entrypoint.sh")
	if !strings.Contains(entrypoint, "--offline") {
		t.Fatal("mocknet monero daemon should run offline so clean resets do not attempt to sync public peers instead of mining local regtest blocks")
	}
}

func TestMocknetMoneroWalletMaintainsBlockGenerationAcrossDaemonRestarts(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/monero/wallet-entrypoint.sh")

	for _, snippet := range []string{
		`current_height()`,
		`generate_blocks()`,
		`"generateblocks"`,
		`BOOTSTRAP_HEIGHT="${XMR_BOOTSTRAP_HEIGHT:-512}"`,
		`ensure_daemon_blocks &`,
		`kill "${block_pid}"`,
	} {
		if !strings.Contains(entrypoint, snippet) {
			t.Fatalf("mocknet monero wallet entrypoint must contain %q so block generation resumes when monerod restarts without restarting the wallet container", snippet)
		}
	}
}

func TestMocknetMoneroWalletAllowsFakechainDaemonVersionSkew(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/monero/wallet-entrypoint.sh")
	if !strings.Contains(entrypoint, "--allow-mismatched-daemon-version") {
		t.Fatal("mocknet monero wallet-rpc should allow mismatched daemon version checks so regtest fakechain startup does not fail refresh with wallet_is_outdated || daemon_is_outdated")
	}
}

func TestMocknetMoneroWalletRescansExistingWalletAfterDaemonReset(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/monero/wallet-entrypoint.sh")

	if !strings.Contains(entrypoint, `wallet_request "rescan_blockchain" '{}'`) {
		t.Fatal("mocknet monero wallet entrypoint must rescan an existing wallet on startup so reruns recover from deep fakechain reorgs after daemon resets")
	}
}

func TestMocknetAvalancheDoesNotShutdownOnDockerDiskThreshold(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	if !strings.Contains(compose, "--system-tracker-disk-required-available-space-percentage=0") {
		t.Fatal("mocknet avalanche service should disable the default disk-space shutdown threshold; Docker Desktop often reports low internal free percentage even when the host has enough space for simulation")
	}
	if !strings.Contains(compose, "--system-tracker-disk-warning-available-space-percentage=0") {
		t.Fatal("mocknet avalanche service should also zero the disk-space warning threshold so the local node stays healthy under Docker Desktop")
	}
}

func TestMocknetBifrostDeclaresXMRChainNetwork(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	if !strings.Contains(compose, `BIFROST_CHAINS_XMR_CHAIN_NETWORK: "mocknet"`) {
		t.Fatal("mocknet bifrost must set BIFROST_CHAINS_XMR_CHAIN_NETWORK=mocknet so the XMR signer client can distinguish trusted mocknet sidecar traffic from public deployments")
	}
}

func TestMocknetDogecoinRaisesRPCWorkQueueDepth(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	if !strings.Contains(compose, `- "./mocknet:/mocknet"`) {
		t.Fatal("mocknet dogecoin service should mount the local mocknet scripts directory so the repo can override the default node-launcher entrypoint")
	}
	if !strings.Contains(compose, `entrypoint: "/mocknet/dogecoin-entrypoint.sh"`) {
		t.Fatal("mocknet dogecoin service should use the repo dogecoin entrypoint so local queue-depth fixes apply during simulation")
	}

	entrypoint := readRepoFile(t, "build/docker/mocknet/dogecoin-entrypoint.sh")
	if !strings.Contains(entrypoint, "-rpcworkqueue=256") {
		t.Fatal("mocknet dogecoin entrypoint should raise rpcworkqueue so startup importaddress bursts do not fail with work queue depth exceeded")
	}
}

func TestSimulationWaitsForEVMRPCReadinessBeforeCreatingUsers(t *testing.T) {
	mainGo := readRepoFile(t, "test/simulation/cmd/main.go")
	if !strings.Contains(mainGo, "waitForEVMRPCReadiness") {
		t.Fatal("simulation startup should wait for local EVM RPCs before creating chain clients")
	}
	for _, chain := range []string{"common.ETHChain", "common.AVAXChain", "common.BASEChain", "common.POLChain"} {
		if !strings.Contains(mainGo, chain) {
			t.Fatalf("expected simulation startup to include %s in EVM readiness checks", chain)
		}
	}
}

func TestWaitForMasterAccountRetriesTransientXMRStartupError(t *testing.T) {
	wantAccount := &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, sdkmath.NewUint(42))),
	}

	client := &stubLiteChainClient{}
	client.getAccount = func(_ *common.PubKey) (*common.Account, error) {
		client.calls++
		if client.calls < 3 {
			return nil, errors.New(`http 503: {"error":"failed to fetch scannable block 1: invalid node (response wasn't the expected json: {\r\n  \"error\": {\r\n    \"code\": -2,\r\n    \"message\": \"Requested block height: 1 greater than current top block height: 0\"\r\n  },\r\n  \"id\": 0,\r\n  \"jsonrpc\": \"2.0\"\r\n})"}`)
		}
		return wantAccount, nil
	}

	sleepCalls := 0
	got, err := waitForMasterAccountWithSleep(common.XMRChain, client, func(time.Duration) {
		sleepCalls++
	})
	if err != nil {
		t.Fatalf("expected transient XMR startup error to recover, got %v", err)
	}
	if got != wantAccount {
		t.Fatal("expected recovered account to be returned")
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 account attempts, got %d", client.calls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestWaitForMasterAccountRetriesTransientXMRDaemonHeightError(t *testing.T) {
	wantAccount := &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, sdkmath.NewUint(84))),
	}

	client := &stubLiteChainClient{}
	client.getAccount = func(_ *common.PubKey) (*common.Account, error) {
		client.calls++
		if client.calls < 3 {
			return nil, errors.New(`http 503: {"error":"failed to query daemon height: daemon RPC get_info failed"}`)
		}
		return wantAccount, nil
	}

	sleepCalls := 0
	got, err := waitForMasterAccountWithSleep(common.XMRChain, client, func(time.Duration) {
		sleepCalls++
	})
	if err != nil {
		t.Fatalf("expected transient XMR daemon-height error to recover, got %v", err)
	}
	if got != wantAccount {
		t.Fatal("expected recovered account to be returned")
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 account attempts, got %d", client.calls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestSimulationTargetsUseExplicitStageDefaults(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")

	wantSingle := "TEST_SIMULATION_STAGES ?= seed,bootstrap,xmr-coverage,arb,swaps,memoless-swaps,consolidate,inactive-vault-refunds,solvency,ragnarok"
	if !strings.Contains(makefile, wantSingle) {
		t.Fatalf("missing expected single-node simulation stages: %s", wantSingle)
	}

	wantCluster := "TEST_SIMULATION_CLUSTER_STAGES ?= seed,bootstrap,swaps,memoless-swaps,inactive-vault-refunds,solvency,ragnarok,xmr-post-churn"
	if !strings.Contains(makefile, wantCluster) {
		t.Fatalf("missing expected cluster simulation stages: %s", wantCluster)
	}

	if !strings.Contains(makefile, "@STAGES=$(TEST_SIMULATION_STAGES) docker run") {
		t.Fatal("make test-simulation-no-reset should set STAGES from TEST_SIMULATION_STAGES")
	}
	if !strings.Contains(makefile, "@STAGES=$(TEST_SIMULATION_CLUSTER_STAGES) docker run") {
		t.Fatal("make test-simulation-cluster should set STAGES from TEST_SIMULATION_CLUSTER_STAGES")
	}
}

func TestSimulationStageOrder(t *testing.T) {
	stageNames := make([]string, 0, len(simulationStages()))
	for _, stage := range simulationStages() {
		stageNames = append(stageNames, stage.name)
	}

	assertStageOrder(t, stageNames, "bootstrap", "xmr-coverage")
	assertStageOrder(t, stageNames, "xmr-coverage", "arb")
	assertStageOrder(t, stageNames, "xmr-post-churn", "ragnarok")
}

func TestXMRFrostSignerDockerBuilderSupportsCargoLockfile(t *testing.T) {
	cargoLock := readRepoFile(t, "xmr-frost-signer/Cargo.lock")
	if !strings.Contains(cargoLock, "\nversion = 4\n") {
		t.Fatal("expected xmr-frost-signer/Cargo.lock to use lockfile version 4")
	}

	dockerfile := readRepoFile(t, "build/docker/xmr-frost-signer/Dockerfile")
	match := regexp.MustCompile(`(?m)^FROM rust:(\d+)\.(\d+)-`).FindStringSubmatch(dockerfile)
	if len(match) != 3 {
		t.Fatal("failed to parse Rust builder version from xmr-frost-signer Dockerfile")
	}

	major, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("failed to parse Rust major version: %v", err)
	}
	minor, err := strconv.Atoi(match[2])
	if err != nil {
		t.Fatalf("failed to parse Rust minor version: %v", err)
	}

	// Rust 2024 stabilized in Rust 1.85, and the current resolved dependency graph
	// includes manifests that require edition2024 support during docker builds.
	if major < 1 || (major == 1 && minor < 85) {
		t.Fatalf("xmr-frost-signer Docker builder rust:%d.%d is too old for the current Cargo dependency graph", major, minor)
	}
}

func TestXMRFrostSignerDockerBuildIncludesSeraiPathDependencies(t *testing.T) {
	cargoToml := readRepoFile(t, "xmr-frost-signer/Cargo.toml")
	if !strings.Contains(cargoToml, "../../serai/") {
		t.Fatal("expected xmr-frost-signer/Cargo.toml to use sibling serai path dependencies")
	}

	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	if !strings.Contains(compose, "additional_contexts:") || !strings.Contains(compose, "serai: ../../../serai") {
		t.Fatal("xmr-frost-signer docker compose build must expose sibling serai checkout as a named build context")
	}

	dockerfile := readRepoFile(t, "build/docker/xmr-frost-signer/Dockerfile")
	if !strings.Contains(dockerfile, "COPY --from=serai . /serai") {
		t.Fatal("xmr-frost-signer Dockerfile must copy the sibling serai checkout into /serai for Cargo path dependencies")
	}
}

func TestXMRFrostSignerDockerRuntimePreparesWritableDataVolume(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	for _, volumeRef := range []string{
		"- xmr-frost-data:/var/lib/xmr-frost",
		"- xmr-sim-data:/var/lib/xmr-sim",
	} {
		if !strings.Contains(compose, volumeRef) {
			t.Fatalf("expected compose to mount %s for xmr services", volumeRef)
		}
	}

	dockerfile := readRepoFile(t, "build/docker/xmr-frost-signer/Dockerfile")
	if !strings.Contains(dockerfile, "COPY build/docker/xmr-frost-signer/entrypoint.sh /usr/local/bin/xmr-frost-entrypoint") {
		t.Fatal("xmr-frost-signer Dockerfile must install a startup wrapper that prepares named-volume data dirs before dropping privileges")
	}
	if !strings.Contains(dockerfile, `ENTRYPOINT ["xmr-frost-entrypoint"]`) {
		t.Fatal("xmr-frost-signer Dockerfile must start through xmr-frost-entrypoint so mounted data dirs are prepared before the signer starts")
	}

	entrypoint := readRepoFile(t, "build/docker/xmr-frost-signer/entrypoint.sh")
	for _, snippet := range []string{
		`data_dir="${XMR_FROST_DATA_DIR:-/var/lib/xmr-frost}"`,
		`mkdir -p "$data_dir"`,
		`chown -R frost:frost "$data_dir"`,
		`chmod 700 "$data_dir"`,
		`exec runuser -u frost -- /usr/local/bin/xmr-frost-signer "$@"`,
	} {
		if !strings.Contains(entrypoint, snippet) {
			t.Fatalf("xmr-frost-signer entrypoint must contain %q", snippet)
		}
	}
}

func TestDockerignoreKeepsXMRFrostEntrypointInBuildContext(t *testing.T) {
	dockerignore := readRepoFile(t, ".dockerignore")
	if !strings.Contains(dockerignore, "build/docker") {
		t.Fatal("expected .dockerignore to exclude build/docker by default")
	}
	if !strings.Contains(dockerignore, "!build/docker/xmr-frost-signer/entrypoint.sh") {
		t.Fatal("expected .dockerignore to re-include the xmr-frost-signer entrypoint script so docker builds can copy it")
	}
}

func TestDockerignoreKeepsSimtestBuildContextLean(t *testing.T) {
	dockerignore := readRepoFile(t, "ci/Dockerfile.simtest.dockerignore")

	for _, snippet := range []string{
		".git",
		"thornode",
		"xmr-frost-signer/*",
		"!xmr-frost-signer/Cargo.lock",
		"!xmr-frost-signer/Cargo.toml",
		"!xmr-frost-signer/LICENSE",
		"!xmr-frost-signer/README.md",
		"!xmr-frost-signer/src",
		"!xmr-frost-signer/src/**",
		"**/target",
	} {
		if !strings.Contains(dockerignore, snippet) {
			t.Fatalf("expected .dockerignore to contain %q so ci/Dockerfile.simtest does not send local build artifacts in the docker context", snippet)
		}
	}
}

func assertStageOrder(t *testing.T, stages []string, before string, after string) {
	t.Helper()

	beforeIdx := -1
	afterIdx := -1
	for i, stage := range stages {
		switch stage {
		case before:
			beforeIdx = i
		case after:
			afterIdx = i
		}
	}

	if beforeIdx == -1 || afterIdx == -1 {
		t.Fatalf("missing expected stages %s or %s in %v", before, after, stages)
	}
	if beforeIdx >= afterIdx {
		t.Fatalf("expected %s before %s, got %v", before, after, stages)
	}
}

type stubLiteChainClient struct {
	calls      int
	getAccount func(*common.PubKey) (*common.Account, error)
}

func (s *stubLiteChainClient) GetAccount(pk *common.PubKey) (*common.Account, error) {
	return s.getAccount(pk)
}

func (s *stubLiteChainClient) SignTx(SimTx) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (s *stubLiteChainClient) BroadcastTx([]byte) (string, error) {
	return "", errors.New("not implemented")
}
