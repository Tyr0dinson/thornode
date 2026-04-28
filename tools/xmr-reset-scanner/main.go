package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero"
	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	defaultAPIURL     = "http://localhost:1317"
	defaultSignerPort = "8283"
)

type toolOptions struct {
	apiURL          string
	chainNetwork    string
	signerEndpoint  string
	signerSeed      string
	signerContainer string
	vaultPubKey     string
	height          int64
	allVaults       bool
	dryRun          bool
}

type vaultSummary struct {
	PubKeyFrost            string   `json:"pub_key_frost"`
	PubKeyEddsa            string   `json:"pub_key_eddsa"`
	Chains                 []string `json:"chains"`
	VaultCreationHeightXmr int64    `json:"vault_creation_height_xmr"`
}

type localVault struct {
	PubKey   string
	Birthday int64
}

type signerAccess struct {
	Endpoint               string
	Seed                   string
	Container              string
	AllowLocalInsecureHTTP bool
}

func main() {
	opts := parseFlags()

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() toolOptions {
	var opts toolOptions
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.apiURL, "api", defaultAPIURL, "THORChain API base URL")
	fs.StringVar(&opts.chainNetwork, "chain-network", "chainnet", "chain network used for local signer transport policy")
	fs.StringVar(&opts.signerEndpoint, "signer-endpoint", "", "xmr-frost-signer endpoint (auto-discovered if omitted)")
	fs.StringVar(&opts.signerSeed, "signer-seed", "", "signer auth token or seed phrase used as the raw Authorization header value (defaults to SIGNER_SEED_PHRASE env or auto-discovered from docker)")
	fs.StringVar(&opts.signerContainer, "signer-container", "", "local xmr-frost-signer container name")
	fs.StringVar(&opts.vaultPubKey, "vault-pubkey", "", "reset one vault pubkey")
	fs.Int64Var(&opts.height, "height", 0, "XMR birthday height override for --vault-pubkey")
	fs.BoolVar(&opts.allVaults, "all-vaults", false, "reset all local XMR vaults known to this signer")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "print actions without changing anything")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintln(fs.Output(), "Resets local XMR vault scanners known to this signer. Bifrost will replay recovered observations during normal scanner_sync.")
		fmt.Fprintln(fs.Output())
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	return opts
}

func run(opts toolOptions) error {
	access, err := resolveSignerAccess(opts)
	if err != nil {
		return err
	}

	signer, err := monero.NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{
		AuthorizationBearer: access.Seed,
		ChainNetwork:        effectiveChainNetwork(opts.chainNetwork, access.Endpoint, access.AllowLocalInsecureHTTP),
	}, access.Endpoint)
	if err != nil {
		return fmt.Errorf("build frost signer client: %w", err)
	}

	vaults, err := discoverVaults(opts, signer)
	if err != nil {
		return err
	}
	if len(vaults) == 0 {
		return errors.New("no local XMR vaults with birthdays found")
	}

	fmt.Printf("Signer endpoint: %s\n", access.Endpoint)
	if access.Container != "" {
		fmt.Printf("Signer container: %s\n", access.Container)
	}
	fmt.Printf("Vaults:\n")
	for _, vault := range vaults {
		fmt.Printf("  %s birthday=%d\n", vault.PubKey, vault.Birthday)
	}

	if opts.dryRun {
		return nil
	}

	for _, vault := range vaults {
		fmt.Printf("Resetting %s to birthday %d\n", vault.PubKey, vault.Birthday)
		if err := signer.ResetScanner(vault.PubKey, vault.Birthday); err != nil {
			return fmt.Errorf("reset %s: %w", vault.PubKey, err)
		}
	}

	fmt.Println("Reset complete. Bifrost will replay recovered observations during normal scanner sync.")
	return nil
}

func resolveSignerAccess(opts toolOptions) (signerAccess, error) {
	endpoint := strings.TrimSpace(opts.signerEndpoint)
	seed := strings.TrimSpace(opts.signerSeed)
	container := strings.TrimSpace(opts.signerContainer)

	if endpoint != "" && seed == "" {
		seed = strings.TrimSpace(os.Getenv("SIGNER_SEED_PHRASE"))
	}
	if endpoint != "" && seed == "" {
		return signerAccess{}, errors.New("signer auth missing: pass --signer-seed or set SIGNER_SEED_PHRASE")
	}
	if endpoint != "" && seed != "" {
		return signerAccess{Endpoint: endpoint, Seed: seed, Container: container}, nil
	}

	if container == "" {
		var err error
		container, err = discoverSingleContainer(`^docker-xmr-frost-signer`)
		if err != nil {
			return signerAccess{}, err
		}
	}

	if seed == "" {
		var err error
		seed, err = dockerPrintEnv(container, "SIGNER_SEED_PHRASE")
		if err != nil {
			return signerAccess{}, err
		}
	}

	allowLocalInsecureHTTP := false
	if endpoint == "" {
		var discoveredViaDockerIP bool
		var err error
		endpoint, discoveredViaDockerIP, err = dockerSignerEndpoint(container)
		if err != nil {
			return signerAccess{}, err
		}
		allowLocalInsecureHTTP = discoveredViaDockerIP && isNonLoopbackHTTPEndpoint(endpoint)
	}

	return signerAccess{
		Endpoint:               endpoint,
		Seed:                   seed,
		Container:              container,
		AllowLocalInsecureHTTP: allowLocalInsecureHTTP,
	}, nil
}

func discoverVaults(opts toolOptions, signer *monero.FrostSigner) ([]localVault, error) {
	requestedPubKey := strings.TrimSpace(opts.vaultPubKey)
	switch {
	case requestedPubKey != "":
		vault, err := resolveLocalVault(opts.apiURL, requestedPubKey, opts.height)
		if err != nil {
			return nil, err
		}
		return []localVault{vault}, nil
	case opts.allVaults:
		return discoverAllLocalVaults(opts.apiURL, signer)
	default:
		return nil, errors.New("pass --vault-pubkey or --all-vaults")
	}
}

func resolveLocalVault(apiURL, requestedPubKey string, heightOverride int64) (localVault, error) {
	birthday := heightOverride
	pubKey := requestedPubKey
	vault, err := fetchVault(apiURL, requestedPubKey)
	if err != nil {
		if birthday <= 0 || !looksLikeCanonicalXMRVaultPubKey(requestedPubKey) {
			return localVault{}, err
		}
	} else {
		if !hasChain(vault.Chains, "XMR") {
			return localVault{}, fmt.Errorf("vault %s is not an XMR vault", requestedPubKey)
		}
		pubKey = canonicalXMRVaultPubKey(vault, requestedPubKey)
		if birthday <= 0 {
			birthday = vault.VaultCreationHeightXmr
		}
	}
	if birthday <= 0 {
		return localVault{}, fmt.Errorf("vault %s has no XMR birthday recorded", requestedPubKey)
	}
	return localVault{PubKey: pubKey, Birthday: birthday}, nil
}

func discoverAllLocalVaults(apiURL string, signer *monero.FrostSigner) ([]localVault, error) {
	keys, err := signer.ListVaultKeys()
	if err != nil {
		return nil, fmt.Errorf("list local signer vault keys: %w", err)
	}

	vaults := make([]localVault, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		requestedPubKey := strings.TrimSpace(key.VaultPubKey)
		if requestedPubKey == "" {
			continue
		}
		// Rescan every XMR vault the sidecar knows about, whether or not this
		// node holds FROST keyshares for it. Non-signing nodes still need to
		// scan member-less vaults so they can validate and co-attest to
		// key-image observations produced by the signing subset — without that,
		// finalized XMR observations never reach supermajority and the vault
		// metadata never gets the derived key-image.
		vault, err := resolveLocalVault(apiURL, requestedPubKey, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", requestedPubKey, err)
			continue
		}
		if _, ok := seen[vault.PubKey]; ok {
			continue
		}
		seen[vault.PubKey] = struct{}{}
		vaults = append(vaults, vault)
	}

	sort.Slice(vaults, func(i, j int) bool {
		if vaults[i].Birthday == vaults[j].Birthday {
			return vaults[i].PubKey < vaults[j].PubKey
		}
		return vaults[i].Birthday < vaults[j].Birthday
	})
	return vaults, nil
}

func fetchVault(apiURL, pubKey string) (vaultSummary, error) {
	var out vaultSummary
	url := strings.TrimRight(strings.TrimSpace(apiURL), "/") + "/thorchain/vault/" + pubKey
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return vaultSummary{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return vaultSummary{}, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return vaultSummary{}, fmt.Errorf("%s returned status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return vaultSummary{}, fmt.Errorf("decode %s: %w", url, err)
	}
	return out, nil
}

func canonicalXMRVaultPubKey(vault vaultSummary, fallback string) string {
	if pubKey := strings.TrimSpace(vault.PubKeyFrost); pubKey != "" {
		return pubKey
	}
	if pubKey := strings.TrimSpace(vault.PubKeyEddsa); pubKey != "" {
		return pubKey
	}
	return strings.TrimSpace(fallback)
}

func looksLikeCanonicalXMRVaultPubKey(pubKey string) bool {
	return strings.HasPrefix(strings.TrimSpace(pubKey), "cthorpub1zcjd")
}

func hasChain(chains []string, target string) bool {
	for _, chain := range chains {
		if strings.EqualFold(strings.TrimSpace(chain), target) {
			return true
		}
	}
	return false
}

func discoverSingleContainer(pattern string) (string, error) {
	names, err := dockerLines("ps", "--format", "{{.Names}}")
	if err != nil {
		return "", fmt.Errorf("list docker containers: %w", err)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("compile container pattern: %w", err)
	}
	matches := make([]string, 0, 1)
	for _, name := range names {
		if re.MatchString(name) {
			matches = append(matches, name)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no running containers match pattern %q", pattern)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple matching containers found: %s", strings.Join(matches, ", "))
	}
}

func dockerPrintEnv(container, envName string) (string, error) {
	out, err := dockerOutput("exec", container, "printenv", envName)
	if err != nil {
		return "", fmt.Errorf("read %s from %s: %w", envName, container, err)
	}
	value := strings.TrimSpace(out)
	if value == "" {
		return "", fmt.Errorf("%s missing in %s", envName, container)
	}
	return value, nil
}

func dockerSignerEndpoint(container string) (string, bool, error) {
	ip, err := dockerInspect(container, "{{range .NetworkSettings.Networks}}{{if .IPAddress}}{{.IPAddress}}{{end}}{{end}}")
	if err == nil && strings.TrimSpace(ip) != "" {
		return "http://" + strings.TrimSpace(ip) + ":" + defaultSignerPort, true, nil
	}

	portLine, err := dockerOutput("port", container, defaultSignerPort+"/tcp")
	if err != nil {
		return "", false, fmt.Errorf("discover signer endpoint for %s: %w", container, err)
	}
	portLine = strings.TrimSpace(portLine)
	parts := strings.Split(portLine, ":")
	if len(parts) == 0 {
		return "", false, fmt.Errorf("unexpected docker port output %q", portLine)
	}
	return "http://127.0.0.1:" + strings.TrimSpace(parts[len(parts)-1]), false, nil
}

func effectiveChainNetwork(chainNetwork, endpoint string, allowLocalInsecureHTTP bool) string {
	if allowLocalInsecureHTTP && isNonLoopbackHTTPEndpoint(endpoint) {
		// The shared FrostSigner client only permits insecure HTTP for local/mocknet-style
		// operator flows. Restrict that override to auto-discovered same-host Docker
		// bridge endpoints, never explicit operator-supplied endpoints.
		return "mocknet"
	}
	return chainNetwork
}

func isNonLoopbackHTTPEndpoint(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "http") {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && !ip.IsLoopback()
}

func dockerInspect(container, format string) (string, error) {
	return dockerOutput("inspect", "--format", format, container)
}

func dockerLines(args ...string) ([]string, error) {
	out, err := dockerOutput(args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(out, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}

func dockerOutput(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", args, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
