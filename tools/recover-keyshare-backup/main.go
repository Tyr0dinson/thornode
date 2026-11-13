package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
)

const defaultEndpoint = "https://gateway.liquify.com/chain/thorchain_api"

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func check(e error, msg string) {
	if e != nil {
		_, file, line, _ := runtime.Caller(1)
		callerLine := fmt.Sprintf("%s:%d", file, line)
		log.Fatal().Msgf("%s: %s\n%s", callerLine, msg, e)
	}
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

// httpStatusError is a non-200 API response; body holds its first bytes.
type httpStatusError struct {
	url  string
	code int
	body string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: status code %d: %s", e.url, e.code, e.body)
}

// unavailableHeightMarkers are the error texts thornode and the Liquify gateway
// return (HTTP 500/503) when they hold no state at the requested height.
var unavailableHeightMarkers = []string{
	"height in the future",
	"no commit info found",
	"failed to load state at height",
	"version does not exist",
	"lowest height is",
	"no archive nodes configured",
}

// heightUnavailable reports that the endpoint has no block at the requested
// height: pruned, beyond the tip, or a non-archive gateway.
func (e *httpStatusError) heightUnavailable() bool {
	b := strings.ToLower(e.body)
	for _, m := range unavailableHeightMarkers {
		if strings.Contains(b, m) {
			return true
		}
	}
	return false
}

// permanent reports that retrying cannot succeed: an unavailable height, or a
// 4xx other than 429 (bad path, auth).
func (e *httpStatusError) permanent() bool {
	return e.heightUnavailable() || (e.code >= 400 && e.code < 500 && e.code != http.StatusTooManyRequests)
}

// get fetches url and decodes the JSON body into result.
func get(url string, result interface{}) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	gzipped := strings.Contains(res.Header.Get("Content-Encoding"), "gzip")
	if res.StatusCode != http.StatusOK {
		return &httpStatusError{url: url, code: res.StatusCode, body: errorBody(res.Body, gzipped)}
	}
	var body io.Reader = res.Body
	if gzipped {
		gz, err := gzip.NewReader(res.Body)
		if err != nil {
			return err
		}
		defer gz.Close()
		body = gz
	}
	return json.NewDecoder(body).Decode(result)
}

// errorBody returns the start of an error response, best effort: a proxy may
// label an empty or plain body as gzip.
func errorBody(r io.Reader, gzipped bool) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 4096))
	if gzipped {
		if gz, gerr := gzip.NewReader(bytes.NewReader(raw)); gerr == nil {
			if msg, rerr := io.ReadAll(io.LimitReader(gz, 512)); rerr == nil {
				raw = msg
			}
		}
	}
	if len(raw) > 512 {
		raw = raw[:512]
	}
	return strings.TrimSpace(string(raw))
}

func selectMember(members []string, in *bufio.Reader) (string, error) {
	if len(members) == 0 {
		return "", fmt.Errorf("no options available")
	}

	// display the options
	fmt.Println("Select vault member:")
	for i, option := range members {
		fmt.Printf("%d. %s\n", i+1, option)
	}

	// read user input
	fmt.Print("Enter the number of member: ")
	input, err := in.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("error reading input: %w", err)
	}

	// convert input to integer
	input = strings.TrimSpace(input)
	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(members) {
		return "", fmt.Errorf("invalid choice, please enter a number between 1 and %d", len(members))
	}

	// return the selected option
	return members[choice-1], nil
}

func prompt(in *bufio.Reader, msg string) (string, error) {
	fmt.Print(msg)
	s, err := in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Options
////////////////////////////////////////////////////////////////////////////////////////

type options struct {
	endpoint        string
	archiveEndpoint string
	vaultPubKey     string
	keygenHeight    int64
	nodeAddress     string
	nodePubKey      string
	scanWindow      int64
	backupOut       string
	backupIn        string
	outDir          string
}

func parseFlags(args []string) (*options, error) {
	fs := flag.NewFlagSet("recover-keyshare-backup", flag.ContinueOnError)
	o := &options{}
	fs.StringVar(&o.endpoint, "endpoint", "", "thornode API endpoint (prompted if unset and --vault-pubkey is unset; default "+defaultEndpoint+")")
	fs.StringVar(&o.archiveEndpoint, "archive-endpoint", "", "archive thornode API endpoint holding the keygen blocks (e.g. https://gateway.liquify.com/chain/thorchain_v1_api); used for the vault lookup at --keygen-height and the block scan")
	fs.StringVar(&o.vaultPubKey, "vault-pubkey", "", "vault secp256k1 pubkey (thorpub1...); skips the interactive prompts")
	fs.Int64Var(&o.keygenHeight, "keygen-height", 0, "vault keygen block height; skips the current-chain vault lookup (needed for retired vaults)")
	fs.StringVar(&o.nodeAddress, "node-address", "", "your node address (thor1...); skips the member list / node lookup")
	fs.StringVar(&o.nodePubKey, "node-pubkey", "", "your node secp256k1 pubkey (thorpub1...); alternative to --node-address")
	fs.Int64Var(&o.scanWindow, "scan-window", 50, "scan keygen height +/- this many blocks for the MsgTssPool backup")
	fs.StringVar(&o.backupOut, "backup-out", "", "write the encrypted on-chain backup to this file and exit (no mnemonic needed, online step)")
	fs.StringVar(&o.backupIn, "backup-in", "", "decrypt a backup file written by --backup-out (no network, offline step)")
	fs.StringVar(&o.outDir, "out-dir", ".", "directory for the decrypted localstate-<pubkey>.json / frost-keyshare-<pubkey>.bin files (use one per node: two nodes of one vault produce the same file names)")
	if err := fs.Parse(args); err != nil {
		return nil, flagParseError{err}
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if o.scanWindow < 0 {
		return nil, fmt.Errorf("--scan-window must be >= 0")
	}
	if o.keygenHeight < 0 {
		return nil, fmt.Errorf("--keygen-height must be >= 0 (0 = look it up)")
	}
	if o.nodeAddress != "" && o.nodePubKey != "" {
		return nil, fmt.Errorf("set only one of --node-address and --node-pubkey")
	}
	if o.backupIn != "" && o.backupOut != "" {
		return nil, fmt.Errorf("set only one of --backup-in and --backup-out")
	}
	return o, nil
}

// flagParseError is an fs.Parse failure; the flag package has already printed it
// together with the usage.
type flagParseError struct{ error }

func (e flagParseError) Unwrap() error { return e.error }

////////////////////////////////////////////////////////////////////////////////////////
// Vault / node resolution
////////////////////////////////////////////////////////////////////////////////////////

type vaultInfo struct {
	PubKey      string   `json:"pub_key"`
	BlockHeight int64    `json:"block_height"`
	StatusSince int64    `json:"status_since"`
	Membership  []string `json:"membership"`
}

type nodeInfo struct {
	NodeAddress      string   `json:"node_address"`
	SignerMembership []string `json:"signer_membership"`
}

// lookupVault returns the vault; on the archive endpoint it queries at a height
// after keygen so retired vaults still resolve.
func lookupVault(o *options) (*vaultInfo, error) {
	url := fmt.Sprintf("%s/thorchain/vault/%s", o.endpoint, o.vaultPubKey)
	if o.archiveEndpoint != "" && o.keygenHeight > 0 {
		url = fmt.Sprintf("%s/thorchain/vault/%s?height=%d", o.archiveEndpoint, o.vaultPubKey, o.keygenHeight+o.scanWindow)
	}
	v := &vaultInfo{}
	if err := get(url, v); err != nil {
		return nil, err
	}
	return v, nil
}

// pubKeyToAddress converts a thorpub secp256k1 pubkey into its thor node address.
func pubKeyToAddress(pk string) (string, error) {
	pubKey, err := common.NewPubKey(pk)
	if err != nil {
		return "", err
	}
	addr, err := pubKey.GetThorAddress()
	if err != nil {
		return "", err
	}
	return addr.String(), nil
}

// membersFromVault derives member node addresses from the vault membership pubkeys,
// avoiding the /thorchain/nodes call (which fails on archive endpoints).
func membersFromVault(v *vaultInfo) ([]string, error) {
	out := make([]string, 0, len(v.Membership))
	for _, pk := range v.Membership {
		addr, err := pubKeyToAddress(pk)
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", pk, err)
		}
		out = append(out, addr)
	}
	return out, nil
}

func membersFromNodes(endpoint, vault string, height int64) ([]string, error) {
	nodes := []nodeInfo{}
	if err := get(fmt.Sprintf("%s/thorchain/nodes?height=%d", endpoint, height), &nodes); err != nil {
		return nil, err
	}
	out := []string{}
	for _, node := range nodes {
		for _, member := range node.SignerMembership {
			if member == vault {
				out = append(out, node.NodeAddress)
				break
			}
		}
	}
	return out, nil
}

// resolveNode returns the node address from flags, or "" if it must be selected.
func resolveNode(o *options) (string, error) {
	switch {
	case o.nodeAddress != "":
		addr, err := types.AccAddressFromBech32(o.nodeAddress)
		if err != nil {
			return "", fmt.Errorf("bad --node-address: %w", err)
		}
		return addr.String(), nil
	case o.nodePubKey != "":
		addr, err := pubKeyToAddress(o.nodePubKey)
		if err != nil {
			return "", fmt.Errorf("bad --node-pubkey: %w", err)
		}
		return addr, nil
	}
	return "", nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Block scan
////////////////////////////////////////////////////////////////////////////////////////

// Backup is the encrypted on-chain keyshare backup of one node for one vault.
// Field names follow MsgTssPool; byte fields are base64 as in the API JSON.
type Backup struct {
	Vault                string `json:"pool_pub_key"`
	Signer               string `json:"signer"`
	TxHeight             int64  `json:"tx_height"`
	KeysharesBackup      []byte `json:"keyshares_backup"`
	PoolPubKeyEddsa      string `json:"pool_pub_key_eddsa,omitempty"`
	KeysharesBackupEddsa []byte `json:"keyshares_backup_eddsa,omitempty"`
	PoolPubKeyFrost      string `json:"pool_pub_key_frost,omitempty"`
	KeysharesBackupFrost []byte `json:"keyshares_backup_frost,omitempty"`
}

type rawBlock struct {
	Txs []struct {
		Tx struct {
			Body struct {
				Messages []json.RawMessage `json:"messages"`
			} `json:"body"`
		} `json:"tx"`
	} `json:"txs"`
}

type rawTssPool struct {
	Type                 string `json:"@type"`
	PoolPubKey           string `json:"pool_pub_key"`
	Signer               string `json:"signer"`
	KeysharesBackup      []byte `json:"keyshares_backup"`
	PoolPubKeyEddsa      string `json:"pool_pub_key_eddsa"`
	KeysharesBackupEddsa []byte `json:"keyshares_backup_eddsa"`
	PoolPubKeyFrost      string `json:"pool_pub_key_frost"`
	KeysharesBackupFrost []byte `json:"keyshares_backup_frost"`
}

// errNoBackup means the node posted a MsgTssPool for the vault but without any backup.
var errNoBackup = errors.New("keyshare was not backed up (MsgTssPool found with empty keyshares_backup, keyshares_backup_eddsa and keyshares_backup_frost)")

// hasBackup reports whether any key type carries an encrypted backup. XMR
// monovault keygens back up only the FROST share (keyshares_backup is empty).
func (bk *Backup) hasBackup() bool {
	return len(bk.KeysharesBackup) > 0 || len(bk.KeysharesBackupEddsa) > 0 || len(bk.KeysharesBackupFrost) > 0
}

// summary lists the backed-up key types and their sizes.
func (bk *Backup) summary() string {
	var parts []string
	for _, k := range []struct {
		kind string
		data []byte
	}{{"secp256k1", bk.KeysharesBackup}, {"eddsa", bk.KeysharesBackupEddsa}, {"frost", bk.KeysharesBackupFrost}} {
		if len(k.data) > 0 {
			parts = append(parts, fmt.Sprintf("%s %d bytes", k.kind, len(k.data)))
		}
	}
	return strings.Join(parts, ", ")
}

// findInBlock returns the backup for (vault, signer) in a block, nil if absent, or
// errNoBackup if the only matching MsgTssPool carries no backup. vault may be the
// pool or the FROST pubkey (Bifrost posts XMR monovault keygens with both set to
// the FROST key). Messages are parsed as raw JSON, so old (v1) and current blocks
// both work.
func findInBlock(b *rawBlock, height int64, vault, signer string) (*Backup, error) {
	emptyMatch := false
	for _, tx := range b.Txs {
		for _, raw := range tx.Tx.Body.Messages {
			var head struct {
				Type string `json:"@type"`
			}
			if json.Unmarshal(raw, &head) != nil || !strings.HasSuffix(head.Type, "MsgTssPool") {
				continue
			}
			var m rawTssPool
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("height %d: decode MsgTssPool: %w", height, err)
			}
			if m.Signer != signer || (m.PoolPubKey != vault && m.PoolPubKeyFrost != vault) {
				continue
			}
			bk := &Backup{
				Vault:                m.PoolPubKey,
				Signer:               m.Signer,
				TxHeight:             height,
				KeysharesBackup:      m.KeysharesBackup,
				PoolPubKeyEddsa:      m.PoolPubKeyEddsa,
				KeysharesBackupEddsa: m.KeysharesBackupEddsa,
				PoolPubKeyFrost:      m.PoolPubKeyFrost,
				KeysharesBackupFrost: m.KeysharesBackupFrost,
			}
			if !bk.hasBackup() {
				emptyMatch = true // a resent MsgTssPool may still carry the backup
				continue
			}
			return bk, nil
		}
	}
	if emptyMatch {
		return nil, errNoBackup
	}
	return nil, nil
}

// scanOrder returns keygen, keygen+1, keygen-1, keygen+2, ... within +/- window.
func scanOrder(keygen, window int64) []int64 {
	out := []int64{keygen}
	for d := int64(1); d <= window; d++ {
		out = append(out, keygen+d)
		if keygen-d > 0 {
			out = append(out, keygen-d)
		}
	}
	return out
}

type blockFetcher func(height int64) (*rawBlock, error)

func httpBlockFetcher(endpoint string) blockFetcher {
	return func(height int64) (*rawBlock, error) {
		const attempts = 3
		for attempt := 1; ; attempt++ {
			b := &rawBlock{}
			err := get(fmt.Sprintf("%s/thorchain/block?height=%d", endpoint, height), b)
			if err == nil {
				return b, nil
			}
			var se *httpStatusError
			if attempt == attempts || (errors.As(err, &se) && se.permanent()) {
				return nil, err
			}
			time.Sleep(time.Second * time.Duration(attempt))
		}
	}
}

// scanForBackup scans the window around keygen, nearest blocks first. A match
// without a backup does not stop the scan (the node may have resent the message).
// Window heights the endpoint has no state for (pruned, beyond the tip) are
// skipped and reported; the keygen height itself must be available.
func scanForBackup(fetch blockFetcher, keygen, window int64, vault, signer string, parallel int) (*Backup, error) {
	order := scanOrder(keygen, window)
	noBackup := false
	var unavailable []int64
	for i := 0; i < len(order); i += parallel {
		batch := order[i:min(i+parallel, len(order))]
		blocks := make([]*rawBlock, len(batch))
		errs := make([]error, len(batch))
		var wg sync.WaitGroup
		for j, h := range batch {
			wg.Add(1)
			go func(j int, h int64) {
				defer wg.Done()
				blocks[j], errs[j] = fetch(h)
			}(j, h)
		}
		wg.Wait()
		for j, h := range batch {
			if errs[j] != nil {
				var se *httpStatusError
				if h != keygen && errors.As(errs[j], &se) && se.heightUnavailable() {
					unavailable = append(unavailable, h)
					continue
				}
				return nil, fmt.Errorf("fetch block %d: %w", h, errs[j])
			}
			bk, err := findInBlock(blocks[j], h, vault, signer)
			if errors.Is(err, errNoBackup) {
				noBackup = true
				continue
			}
			if err != nil {
				return nil, err
			}
			if bk != nil {
				return bk, nil
			}
		}
	}
	where := fmt.Sprintf("signer %s, vault %s, heights %d +/- %d", signer, vault, keygen, window)
	if len(unavailable) > 0 {
		where += fmt.Sprintf("; inconclusive: %d heights unavailable on the endpoint (e.g. %d, pruned or beyond the tip)", len(unavailable), unavailable[0])
	}
	if noBackup {
		return nil, fmt.Errorf("%w (%s)", errNoBackup, where)
	}
	return nil, fmt.Errorf("no MsgTssPool found (%s)", where)
}

////////////////////////////////////////////////////////////////////////////////////////
// Decrypt
////////////////////////////////////////////////////////////////////////////////////////

// keyshareOutput is one backed-up keyshare and the file it decrypts to.
type keyshareOutput struct {
	kind      string // secp256k1 | eddsa | frost
	encrypted []byte
	path      string
}

// outputPath builds outDir/<pattern with pubKey>. The pubkey comes from an API
// response or an editable backup file, and the encryption does not bind it, so it
// must be a valid bech32 pubkey and the resulting path must stay inside outDir.
func outputPath(outDir, pattern, pubKey string) (string, error) {
	if pubKey == "" {
		return "", errors.New("missing pubkey")
	}
	if _, err := common.NewPubKey(pubKey); err != nil {
		return "", fmt.Errorf("bad pubkey: %w", err)
	}
	name := fmt.Sprintf(pattern, pubKey)
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("unsafe file name %q", name)
	}
	path := filepath.Join(outDir, name)
	if filepath.Dir(path) != filepath.Clean(outDir) {
		return "", fmt.Errorf("%s is outside %s", path, outDir)
	}
	return path, nil
}

// keyshareOutputs lists every key type present in bk with its validated output
// path. secp256k1/EdDSA decrypt to the localstate-<pubkey>.json Bifrost reads. The
// FROST share is the raw XMR sidecar blob, written as frost-keyshare-<pubkey>.bin;
// it is not Bifrost's frost-recovery-<pubkey>.hex envelope.
func keyshareOutputs(bk *Backup, outDir string) ([]keyshareOutput, error) {
	var out []keyshareOutput
	seen := map[string]string{}
	for _, k := range []struct {
		kind, pubKey, pattern string
		encrypted             []byte
	}{
		{"secp256k1", bk.Vault, "localstate-%s.json", bk.KeysharesBackup},
		{"eddsa", bk.PoolPubKeyEddsa, "localstate-%s.json", bk.KeysharesBackupEddsa},
		{"frost", bk.PoolPubKeyFrost, "frost-keyshare-%s.bin", bk.KeysharesBackupFrost},
	} {
		if len(k.encrypted) == 0 {
			continue
		}
		path, err := outputPath(outDir, k.pattern, k.pubKey)
		if err != nil {
			return nil, fmt.Errorf("%s backup: %w", k.kind, err)
		}
		if prev, dup := seen[path]; dup {
			return nil, fmt.Errorf("%s and %s backups both write %s", prev, k.kind, path)
		}
		seen[path] = k.kind
		out = append(out, keyshareOutput{kind: k.kind, encrypted: k.encrypted, path: path})
	}
	if len(out) == 0 {
		return nil, errNoBackup
	}
	return out, nil
}

func validateVault(bk *Backup) error {
	if bk.Vault == "" {
		return errors.New("missing pool_pub_key")
	}
	if _, err := common.NewPubKey(bk.Vault); err != nil {
		return fmt.Errorf("bad pool_pub_key: %w", err)
	}
	return nil
}

// validateBackup checks a fetched or imported backup up front, before a backup
// file is written or the mnemonic is asked for.
func validateBackup(bk *Backup) error {
	if err := validateVault(bk); err != nil {
		return err
	}
	_, err := keyshareOutputs(bk, ".")
	return err
}

// mustNotExist fails if path exists (or cannot be checked).
func mustNotExist(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// writeNew durably writes data to a file that must not exist yet, removing it on
// failure.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if d, derr := os.Open(filepath.Dir(path)); derr == nil { // persist the new directory entry
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// decryptBackup decrypts every backed-up key type. All shares are decrypted and
// all destinations checked before the first file is created, so a wrong mnemonic,
// a corrupt share or an existing file leaves nothing behind.
func decryptBackup(bk *Backup, mnemonic, outDir string) error {
	if err := validateVault(bk); err != nil {
		return err
	}
	outs, err := keyshareOutputs(bk, outDir)
	if err != nil {
		return err
	}
	// destinations first: the scrypt-based decrypts are slow
	for _, o := range outs {
		if err = mustNotExist(o.path); err != nil {
			return fmt.Errorf("%s: %w", o.kind, err)
		}
	}
	plain := make([][]byte, len(outs))
	// best effort: clears our copies; tss's intermediate buffers are out of reach
	defer func() {
		for _, p := range plain {
			clear(p)
		}
	}()
	for i, o := range outs {
		if plain[i], err = tss.DecryptRawKeyshares(o.encrypted, mnemonic); err != nil {
			return fmt.Errorf("%s: decrypt: %w", o.kind, err)
		}
	}
	if err = os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	for i, o := range outs {
		if err = writeNew(o.path, plain[i]); err != nil {
			for _, w := range outs[:i] {
				_ = os.Remove(w.path)
			}
			return fmt.Errorf("%s: %w", o.kind, err)
		}
	}
	for _, o := range outs {
		fmt.Printf("Decrypted %s keyshare written to %s\n", o.kind, o.path)
		if o.kind == "frost" {
			fmt.Println("  (raw XMR FROST sidecar share, not a Bifrost file: Bifrost restores FROST shares itself from the on-chain backup at startup)")
		}
	}
	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Main
////////////////////////////////////////////////////////////////////////////////////////

func setupConfig() {
	cfg := types.GetConfig()
	cfg.SetBech32PrefixForAccount(cmd.Bech32PrefixAccAddr, cmd.Bech32PrefixAccPub)
	cfg.SetBech32PrefixForValidator(cmd.Bech32PrefixValAddr, cmd.Bech32PrefixValPub)
	cfg.SetBech32PrefixForConsensusNode(cmd.Bech32PrefixConsAddr, cmd.Bech32PrefixConsPub)
	cfg.SetCoinType(cmd.THORChainCoinType)
	cfg.SetPurpose(cmd.THORChainCoinPurpose)
	cfg.Seal()
}

// fetchBackup resolves vault + node and returns the on-chain backup.
func fetchBackup(o *options, in *bufio.Reader) (*Backup, error) {
	var err error

	// interactive vault/endpoint prompts (stock behaviour) unless --vault-pubkey
	if o.vaultPubKey == "" {
		if o.endpoint == "" {
			if o.endpoint, err = prompt(in, fmt.Sprintf("Thornode (must contain vault block heights) [%s]: ", defaultEndpoint)); err != nil {
				return nil, fmt.Errorf("read endpoint: %w", err)
			}
		}
		if o.vaultPubKey, err = prompt(in, "Vault: "); err != nil {
			return nil, fmt.Errorf("read vault: %w", err)
		}
	}
	if o.endpoint == "" {
		o.endpoint = defaultEndpoint
	}
	if _, err = common.NewPubKey(o.vaultPubKey); err != nil {
		return nil, fmt.Errorf("bad vault pubkey: %w", err)
	}

	node, err := resolveNode(o)
	if err != nil {
		return nil, err
	}

	// vault lookup: skipped when --keygen-height is set without an archive endpoint
	var vault *vaultInfo
	if o.keygenHeight == 0 || o.archiveEndpoint != "" {
		vault, err = lookupVault(o)
		if err != nil {
			if o.keygenHeight == 0 {
				return nil, fmt.Errorf("get vault (retired vault? pass --keygen-height and --archive-endpoint): %w", err)
			}
			fmt.Fprintf(os.Stderr, "warning: vault lookup failed, continuing with --keygen-height: %v\n", err)
			vault = nil
		}
	}
	keygen := o.keygenHeight
	if keygen == 0 {
		if vault.BlockHeight <= 0 {
			return nil, fmt.Errorf("vault %s has no keygen block_height in the API response: pass --keygen-height", o.vaultPubKey)
		}
		keygen = vault.BlockHeight
	}

	if vault != nil {
		var members []string
		members, err = membersFromVault(vault)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: vault membership unusable, falling back to the node list: %v\n", err)
		}
		if err != nil || len(members) == 0 {
			members, err = membersFromNodes(o.endpoint, o.vaultPubKey, vault.StatusSince)
			if (err != nil || len(members) == 0) && o.archiveEndpoint != "" { // the height may be pruned on the current endpoint
				members, err = membersFromNodes(o.archiveEndpoint, o.vaultPubKey, vault.StatusSince)
			}
		}
		switch {
		case err != nil && node == "":
			return nil, fmt.Errorf("get vault members: %w", err)
		case err != nil:
			// membership only sanity-checks an explicit --node-*; the scan still matches signer + vault
			fmt.Fprintf(os.Stderr, "warning: cannot list vault members, skipping the membership check: %v\n", err)
		case node == "":
			if node, err = selectMember(members, in); err != nil {
				return nil, err
			}
		default:
			found := false
			for _, m := range members {
				found = found || m == node
			}
			if !found {
				return nil, fmt.Errorf("node %s is not a member of vault %s", node, o.vaultPubKey)
			}
		}
	} else if node == "" {
		return nil, fmt.Errorf("vault lookup skipped: pass --node-address or --node-pubkey")
	}

	scanEndpoint := o.endpoint
	if o.archiveEndpoint != "" {
		scanEndpoint = o.archiveEndpoint
	}
	fmt.Fprintf(os.Stderr, "Scanning %s blocks %d +/- %d for %s ...\n", scanEndpoint, keygen, o.scanWindow, node)
	bk, err := scanForBackup(httpBlockFetcher(scanEndpoint), keygen, o.scanWindow, o.vaultPubKey, node, 4)
	if err != nil {
		var se *httpStatusError
		if o.archiveEndpoint == "" && errors.As(err, &se) && se.heightUnavailable() {
			return nil, fmt.Errorf("%w (old vault? pass --archive-endpoint)", err)
		}
		return nil, err
	}
	if err = validateBackup(bk); err != nil {
		return nil, fmt.Errorf("invalid on-chain backup at height %d: %w", bk.TxHeight, err)
	}
	return bk, nil
}

func writeBackup(path string, bk *Backup) error {
	data, err := json.MarshalIndent(bk, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(path, data) // never overwrite another node's backup or follow a symlink
}

func readBackup(path string) (*Backup, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	bk := &Backup{}
	if err = json.Unmarshal(data, bk); err != nil {
		return nil, err
	}
	if err = validateBackup(bk); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return bk, nil
}

func main() {
	setupConfig()

	o, err := parseFlags(os.Args[1:])
	if err != nil {
		var pe flagParseError
		if !errors.As(err, &pe) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(2)
	}
	in := bufio.NewReader(os.Stdin)

	var bk *Backup
	if o.backupIn != "" {
		bk, err = readBackup(o.backupIn)
		check(err, "Failed to read backup file")
	} else {
		if o.backupOut != "" { // before the (slow) scan, not after it
			check(mustNotExist(o.backupOut), "Refusing to overwrite --backup-out")
			st, serr := os.Stat(filepath.Dir(o.backupOut))
			if serr == nil && !st.IsDir() {
				serr = fmt.Errorf("%s is not a directory", filepath.Dir(o.backupOut))
			}
			check(serr, "Bad --backup-out directory")
		}
		bk, err = fetchBackup(o, in)
		check(err, "Failed to find keyshare backup")
		fmt.Fprintf(os.Stderr, "Found backup: vault %s signer %s height %d (%s)\n",
			bk.Vault, bk.Signer, bk.TxHeight, bk.summary())
		if o.backupOut != "" {
			check(writeBackup(o.backupOut, bk), "Failed to write backup file")
			fmt.Printf("Encrypted backup written to %s (still encrypted; decrypt offline with --backup-in)\n", o.backupOut)
			return
		}
	}

	mnemonic, err := prompt(in, "Mnemonic: ")
	check(err, "Failed to read mnemonic")
	check(decryptBackup(bk, mnemonic, o.outDir), "Failed to decrypt keyshare")
}
