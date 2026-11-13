package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/bech32"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/cmd"
)

// fixtures use mainnet prefixes; rebech32 rewrites them for the build's network
// (CI runs with -tags=mocknet, i.e. tthor/tthorpub)
var (
	testVault    = "thorpub1addwnpepqdu0wrnvx63eqf6gf5qyfz2k95gj9c96hsgecdcfwawckvsgqy3ezh0rxwt"
	otherVault   = "thorpub1addwnpepq08960sw6gr3867rqmncyjx3sng7qug4s8vu2kzgzfqyd93dq4uxvqev7w6"
	memberPubKey = "thorpub1addwnpepq04cvrhekametlquvkd6yhk9nzexu7pjg7v9luznq99puwdmtvtwx5r9247"
	testNode     = "thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex"
	otherNode    = "thor10czf2s89h79fsjmqqck85cdqeq536hw5ngz4lt"
	testMnemonic = "profit used piece repeat real curtain endorse tennis tenant sentence include glass return learn upgrade apple crane polar attend before ripple doctor decrease depend"
)

func rebech32(s, prefix string) string {
	_, bz, err := bech32.DecodeAndConvert(s)
	if err != nil {
		panic(err)
	}
	out, err := bech32.ConvertAndEncode(prefix, bz)
	if err != nil {
		panic(err)
	}
	return out
}

func TestMain(m *testing.M) {
	setupConfig()
	for _, pk := range []*string{&testVault, &otherVault, &memberPubKey} {
		*pk = rebech32(*pk, cmd.Bech32PrefixAccPub)
	}
	for _, a := range []*string{&testNode, &otherNode} {
		*a = rebech32(*a, cmd.Bech32PrefixAccAddr)
	}
	os.Exit(m.Run())
}

func TestParseFlags(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.scanWindow != 50 || o.vaultPubKey != "" || o.outDir != "." {
		t.Fatalf("defaults: %+v %v", o, err)
	}

	o, err = parseFlags([]string{
		"--vault-pubkey", testVault, "--keygen-height", "12856892",
		"--archive-endpoint", "http://archive", "--node-address", testNode,
		"--scan-window", "10", "--backup-out", "x.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.vaultPubKey != testVault || o.keygenHeight != 12856892 || o.archiveEndpoint != "http://archive" ||
		o.nodeAddress != testNode || o.scanWindow != 10 || o.backupOut != "x.json" {
		t.Fatalf("parsed: %+v", o)
	}

	bad := [][]string{
		{"--node-address", testNode, "--node-pubkey", memberPubKey},
		{"--backup-in", "a", "--backup-out", "b"},
		{"--scan-window", "-1"},
		{"--keygen-height", "-5"},
		{"positional"},
	}
	for _, args := range bad {
		if _, err = parseFlags(args); err == nil {
			t.Errorf("expected error for %v", args)
		}
	}
}

func TestResolveNode(t *testing.T) {
	n, err := resolveNode(&options{})
	if err != nil || n != "" {
		t.Fatalf("no flags: %q %v", n, err)
	}

	n, err = resolveNode(&options{nodeAddress: testNode})
	if err != nil || n != testNode {
		t.Fatalf("address: %q %v", n, err)
	}

	// pubkey -> address must match the vault membership derivation
	n, err = resolveNode(&options{nodePubKey: memberPubKey})
	if err != nil {
		t.Fatal(err)
	}
	members, err := membersFromVault(&vaultInfo{Membership: []string{memberPubKey}})
	if err != nil || len(members) != 1 || members[0] != n {
		t.Fatalf("pubkey %q vs members %v %v", n, members, err)
	}

	if _, err = resolveNode(&options{nodeAddress: "thor1bogus"}); err == nil {
		t.Error("expected bad address error")
	}
	if _, err = resolveNode(&options{nodePubKey: "thorpub1bogus"}); err == nil {
		t.Error("expected bad pubkey error")
	}
}

func TestScanOrder(t *testing.T) {
	got := scanOrder(100, 2)
	want := []int64{100, 101, 99, 102, 98}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got = scanOrder(1, 2); fmt.Sprint(got) != fmt.Sprint([]int64{1, 2, 3}) {
		t.Fatalf("low clamp: %v", got)
	}
	if len(scanOrder(100, 50)) != 101 {
		t.Fatal("window 50 must cover 101 blocks")
	}
}

func tssPoolMsg(typ, vault, signer string, backup []byte) json.RawMessage {
	m := map[string]any{"@type": typ, "pool_pub_key": vault, "signer": signer}
	if backup != nil {
		m["keyshares_backup"] = base64.StdEncoding.EncodeToString(backup)
	}
	b, _ := json.Marshal(m)
	return b
}

func block(msgs ...json.RawMessage) *rawBlock {
	b := &rawBlock{}
	for _, m := range msgs {
		b.Txs = append(b.Txs, struct {
			Tx struct {
				Body struct {
					Messages []json.RawMessage `json:"messages"`
				} `json:"body"`
			} `json:"tx"`
		}{})
		b.Txs[len(b.Txs)-1].Tx.Body.Messages = []json.RawMessage{m}
	}
	return b
}

func TestFindInBlock(t *testing.T) {
	other := json.RawMessage(`{"@type":"/types.MsgObservedTxOut","signer":"` + testNode + `"}`)
	b := block(
		other,
		tssPoolMsg("/types.MsgTssPool", otherVault, testNode, []byte("wrong-vault")), // same signer, other vault
		tssPoolMsg("/types.MsgTssPool", testVault, otherNode, []byte("wrong-node")),  // same vault, other signer
		tssPoolMsg("/types.MsgTssPool", testVault, testNode, []byte("mine")),
	)
	bk, err := findInBlock(b, 7, testVault, testNode)
	if err != nil || bk == nil || string(bk.KeysharesBackup) != "mine" || bk.TxHeight != 7 {
		t.Fatalf("got %+v %v", bk, err)
	}

	// type url matched by suffix, independent of package prefix
	b = block(tssPoolMsg("/thorchain.v1.MsgTssPool", testVault, testNode, []byte("v3")))
	if bk, err = findInBlock(b, 1, testVault, testNode); err != nil || string(bk.KeysharesBackup) != "v3" {
		t.Fatalf("v3 type: %+v %v", bk, err)
	}

	// absent
	if bk, err = findInBlock(block(other), 1, testVault, testNode); err != nil || bk != nil {
		t.Fatalf("absent: %+v %v", bk, err)
	}

	// present but backup disabled
	b = block(tssPoolMsg("/types.MsgTssPool", testVault, testNode, nil))
	if _, err = findInBlock(b, 1, testVault, testNode); err != errNoBackup {
		t.Fatalf("empty backup: %v", err)
	}
}

func TestScanForBackupWindow(t *testing.T) {
	const keygen = 1000
	var mu sync.Mutex
	fetched := map[int64]bool{}
	fetch := func(at int64) blockFetcher {
		return func(h int64) (*rawBlock, error) {
			mu.Lock()
			fetched[h] = true
			mu.Unlock()
			if h == at {
				return block(tssPoolMsg("/types.MsgTssPool", testVault, testNode, []byte(fmt.Sprint(h)))), nil
			}
			return block(), nil
		}
	}

	// backup lands after keygen (old tool scanned only keygen-20..keygen)
	for _, at := range []int64{keygen + 1, keygen - 20, keygen + 50, keygen - 50} {
		bk, err := scanForBackup(fetch(at), keygen, 50, testVault, testNode, 4)
		if err != nil || string(bk.KeysharesBackup) != fmt.Sprint(at) {
			t.Fatalf("at %d: %+v %v", at, bk, err)
		}
	}

	// outside window
	fetched = map[int64]bool{}
	if _, err := scanForBackup(fetch(keygen+51), keygen, 50, testVault, testNode, 4); err == nil {
		t.Fatal("expected not found outside window")
	}
	if len(fetched) != 101 || fetched[keygen+51] || fetched[keygen-51] {
		t.Fatalf("scanned %d blocks", len(fetched))
	}

	// fetch errors surface
	errFetch := func(int64) (*rawBlock, error) { return nil, fmt.Errorf("boom") }
	if _, err := scanForBackup(errFetch, keygen, 1, testVault, testNode, 2); err == nil {
		t.Fatal("expected fetch error")
	}
}

func TestBackupFileRoundTripDecrypt(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	// encrypt a localstate exactly as bifrost does for the on-chain backup
	plain := []byte(`{"PubKey":"` + testVault + `","ParticipantKeys":["a","b"]}`)
	if err := os.WriteFile("in.json", plain, 0o600); err != nil {
		t.Fatal(err)
	}
	enc, err := tss.EncryptKeyshares("in.json", testMnemonic)
	if err != nil {
		t.Fatal(err)
	}

	// the API JSON message decodes into a backup file and back
	b := block(tssPoolMsg("/types.MsgTssPool", testVault, testNode, enc))
	bk, err := findInBlock(b, 12856892, testVault, testNode)
	if err != nil || bk == nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "backup.json")
	if err = writeBackup(path, bk); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode %v", st.Mode().Perm())
	}
	bk2, err := readBackup(path)
	if err != nil || string(bk2.KeysharesBackup) != string(enc) || bk2.Vault != testVault {
		t.Fatalf("readBackup: %v", err)
	}

	// wrong mnemonic fails and writes nothing
	wrong := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	if err = decryptBackup(bk2, wrong, "."); err == nil {
		t.Fatal("expected wrong-mnemonic failure")
	}
	if _, err = os.Stat("localstate-" + testVault + ".json"); !os.IsNotExist(err) {
		t.Fatal("output written on failed decrypt")
	}

	// right mnemonic reproduces the localstate
	if err = decryptBackup(bk2, testMnemonic, "fsex"); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join("fsex", "localstate-"+testVault+".json"))
	if err != nil || string(out) != string(plain) {
		t.Fatalf("decrypted %q %v", out, err)
	}

	// readBackup rejects incomplete files
	_ = os.WriteFile("empty.json", []byte(`{}`), 0o600)
	if _, err = readBackup("empty.json"); err == nil {
		t.Fatal("expected incomplete backup error")
	}
}

func TestParseFlagsErrorKinds(t *testing.T) {
	// fs.Parse failures are already printed by the flag package; the tool's own
	// validation errors are not, so main must print those itself
	_, err := parseFlags([]string{"--no-such-flag"})
	var pe flagParseError
	if !errors.As(err, &pe) {
		t.Fatalf("unknown flag: %v", err)
	}
	if _, err = parseFlags([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
	_, err = parseFlags([]string{"--backup-in", "a", "--backup-out", "b"})
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("own validation error must not look pre-printed: %v", err)
	}
}

// frostOnlyMsg is the MsgTssPool Bifrost posts for an XMR monovault keygen: no
// secp256k1/EdDSA backup, pool_pub_key == pool_pub_key_frost.
func frostOnlyMsg(vault, signer string, frost []byte) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"@type": "/types.MsgTssPool", "pool_pub_key": vault, "signer": signer,
		"pool_pub_key_frost": vault, "keyshares_backup_frost": base64.StdEncoding.EncodeToString(frost),
	})
	return b
}

func TestFrostOnlyBackup(t *testing.T) {
	dir := t.TempDir()
	raw := []byte{0x00, 0x01, 0xfe, 0xff, 'f', 'r', 'o', 's', 't'}
	enc, err := tss.EncryptRawKeyshares(raw, testMnemonic)
	if err != nil {
		t.Fatal(err)
	}

	bk, err := findInBlock(block(frostOnlyMsg(testVault, testNode, enc)), 9, testVault, testNode)
	if err != nil || bk == nil || len(bk.KeysharesBackup) != 0 || string(bk.KeysharesBackupFrost) != string(enc) {
		t.Fatalf("findInBlock: %+v %v", bk, err)
	}

	// the --backup-out / --backup-in round trip keeps a FROST-only backup
	path := filepath.Join(dir, "backup.json")
	if err = writeBackup(path, bk); err != nil {
		t.Fatal(err)
	}
	if bk, err = readBackup(path); err != nil {
		t.Fatalf("readBackup: %v", err)
	}

	out := filepath.Join(dir, "out")
	if err = decryptBackup(bk, testMnemonic, out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "frost-keyshare-"+testVault+".bin"))
	if err != nil || string(got) != string(raw) {
		t.Fatalf("frost share %x %v", got, err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 1 {
		t.Fatalf("expected only the frost file, got %d files", len(entries))
	}
}

func TestBackupPubKeysCannotEscapeOutDir(t *testing.T) {
	dir := t.TempDir()
	plain := []byte(`{"PubKey":"` + testVault + `"}`)
	in := filepath.Join(dir, "in.json")
	if err := os.WriteFile(in, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	enc, err := tss.EncryptKeyshares(in, testMnemonic)
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "a", "b")

	// genuine ciphertext with tampered pubkeys: the encryption does not bind them
	for name, bk := range map[string]*Backup{
		"vault":         {Vault: "x/../../leaked", KeysharesBackup: enc},
		"vault-abs":     {Vault: "/tmp/leaked", KeysharesBackup: enc},
		"eddsa":         {Vault: testVault, KeysharesBackup: enc, PoolPubKeyEddsa: "../leaked", KeysharesBackupEddsa: enc},
		"frost":         {Vault: testVault, PoolPubKeyFrost: `..\leaked`, KeysharesBackupFrost: enc},
		"eddsa-missing": {Vault: testVault, KeysharesBackupEddsa: enc},
		"frost-missing": {Vault: testVault, KeysharesBackupFrost: enc},
		"no-backup":     {Vault: testVault},
	} {
		path := filepath.Join(dir, name+".json")
		if err = writeBackup(path, bk); err != nil {
			t.Fatal(err)
		}
		if _, err = readBackup(path); err == nil {
			t.Errorf("%s: readBackup accepted it", name)
		}
		if err = decryptBackup(bk, testMnemonic, outDir); err == nil {
			t.Errorf("%s: decryptBackup accepted it", name)
		}
	}
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && strings.Contains(filepath.Base(p), "leaked") {
			t.Errorf("wrote %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("out dir created for a rejected backup: %v", err)
	}
}

func TestDecryptBackupAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.json")
	if err := os.WriteFile(in, []byte(`{"PubKey":"`+testVault+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	enc, err := tss.EncryptKeyshares(in, testMnemonic)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")

	// a corrupt later share must not leave the good secp256k1 file behind
	bad := &Backup{Vault: testVault, KeysharesBackup: enc, PoolPubKeyEddsa: otherVault, KeysharesBackupEddsa: []byte("corrupt")}
	if err = decryptBackup(bad, testMnemonic, out); err == nil {
		t.Fatal("expected eddsa decrypt failure")
	}
	if _, err = os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("output left after failed decrypt: %v", err)
	}

	// an existing destination fails before anything is written
	good := &Backup{Vault: testVault, KeysharesBackup: enc, PoolPubKeyEddsa: otherVault, KeysharesBackupEddsa: enc}
	if err = os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(out, "localstate-"+otherVault+".json"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = decryptBackup(good, testMnemonic, out); err == nil {
		t.Fatal("expected existing-file failure")
	}
	if _, err = os.Stat(filepath.Join(out, "localstate-"+testVault+".json")); !os.IsNotExist(err) {
		t.Fatal("secp256k1 file written although the eddsa destination exists")
	}

	// two key types resolving to one file name are rejected
	dup := &Backup{Vault: testVault, KeysharesBackup: enc, PoolPubKeyEddsa: testVault, KeysharesBackupEddsa: enc}
	if err = validateBackup(dup); err == nil {
		t.Fatal("expected duplicate output error")
	}
}

func TestBlockFetcherRetries(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.URL.Query().Get("height")
		mu.Lock()
		calls[h]++
		n := calls[h]
		mu.Unlock()
		switch {
		case h == "1":
			w.WriteHeader(http.StatusBadRequest) // pruned / bad height: permanent
		case h == "2" && n == 1:
			w.WriteHeader(http.StatusBadGateway) // transient
		case h == "4":
			w.Header().Set("Content-Encoding", "gzip") // proxy mislabels an empty error body
			w.WriteHeader(http.StatusNotFound)
		case h == "3":
			w.WriteHeader(http.StatusInternalServerError) // beyond the tip: permanent
			_, _ = w.Write([]byte(`{"code":2, "message":"cannot query with height in the future; please provide a valid height: invalid height"}`))
		default:
			_, _ = w.Write([]byte(`{"txs":[]}`))
		}
	}))
	defer srv.Close()
	fetch := httpBlockFetcher(srv.URL)

	if _, err := fetch(1); err == nil {
		t.Fatal("expected 400 error")
	}
	if _, err := fetch(2); err != nil {
		t.Fatalf("transient 502 not retried: %v", err)
	}
	_, err := fetch(3)
	var se *httpStatusError
	if !errors.As(err, &se) || !se.heightUnavailable() {
		t.Fatalf("future height: %v", err)
	}
	if _, err = fetch(4); !errors.As(err, &se) || se.code != http.StatusNotFound {
		t.Fatalf("gzip-labelled empty error body must keep the status: %v", err)
	}
	if calls["1"] != 1 || calls["2"] != 2 || calls["3"] != 1 || calls["4"] != 1 {
		t.Fatalf("calls %v", calls)
	}
}

func TestFindInBlockMatchRules(t *testing.T) {
	// an empty resend before the real backup in the same block
	b := block(
		tssPoolMsg("/types.MsgTssPool", testVault, testNode, nil),
		tssPoolMsg("/types.MsgTssPool", testVault, testNode, []byte("real")),
	)
	if bk, err := findInBlock(b, 1, testVault, testNode); err != nil || string(bk.KeysharesBackup) != "real" {
		t.Fatalf("empty then real: %+v %v", bk, err)
	}

	// the vault can be named by its FROST pubkey
	m, _ := json.Marshal(map[string]any{
		"@type": "/types.MsgTssPool", "pool_pub_key": otherVault, "signer": testNode,
		"pool_pub_key_frost": testVault, "keyshares_backup_frost": base64.StdEncoding.EncodeToString([]byte("f")),
	})
	bk, err := findInBlock(block(m), 1, testVault, testNode)
	if err != nil || bk == nil || bk.Vault != otherVault || string(bk.KeysharesBackupFrost) != "f" {
		t.Fatalf("by frost pubkey: %+v %v", bk, err)
	}
}

func TestScanForBackupKeepsGoing(t *testing.T) {
	const keygen = 1000
	fetch := func(h int64) (*rawBlock, error) {
		switch {
		case h == keygen:
			return block(tssPoolMsg("/types.MsgTssPool", testVault, testNode, nil)), nil // empty first copy
		case h > keygen+2: // what thornode returns past the tip
			return nil, &httpStatusError{
				url: "u", code: http.StatusInternalServerError,
				body: `{"code":2, "message":"cannot query with height in the future; please provide a valid height: invalid height"}`,
			}
		case h == keygen-2:
			return block(tssPoolMsg("/types.MsgTssPool", testVault, testNode, []byte("resent"))), nil
		}
		return block(), nil
	}
	bk, err := scanForBackup(fetch, keygen, 5, testVault, testNode, 4)
	if err != nil || string(bk.KeysharesBackup) != "resent" {
		t.Fatalf("resent backup past empty copy and missing heights: %+v %v", bk, err)
	}

	// only empty copies: errNoBackup, with the unavailable heights reported
	onlyEmpty := func(h int64) (*rawBlock, error) {
		if h > keygen+1 { // Liquify's non-archive gateway on a pruned height
			return nil, &httpStatusError{url: "u", code: http.StatusServiceUnavailable, body: "No archive nodes configured for this chain"}
		}
		return block(tssPoolMsg("/types.MsgTssPool", testVault, testNode, nil)), nil
	}
	if _, err = scanForBackup(onlyEmpty, keygen, 3, testVault, testNode, 2); !errors.Is(err, errNoBackup) ||
		!strings.Contains(err.Error(), "2 heights unavailable") {
		t.Fatalf("only empty: %v", err)
	}

	// other errors still fail the scan: a generic 5xx, a 404 (wrong path), and an
	// unavailable keygen height itself
	for name, f := range map[string]blockFetcher{
		"502": func(int64) (*rawBlock, error) { return nil, &httpStatusError{url: "u", code: http.StatusBadGateway} },
		"404": func(h int64) (*rawBlock, error) {
			if h == keygen {
				return block(), nil
			}
			return nil, &httpStatusError{url: "u", code: http.StatusNotFound, body: "not found"}
		},
		"keygen-pruned": func(int64) (*rawBlock, error) {
			return nil, &httpStatusError{
				url: "u", code: http.StatusInternalServerError,
				body: `{"code":2, "message":"failed to load state at height 100; no commit info found (latest height: 28411147): not found"}`,
			}
		},
	} {
		if _, err = scanForBackup(f, keygen, 1, testVault, testNode, 2); err == nil || errors.Is(err, errNoBackup) {
			t.Fatalf("%s must fail the scan: %v", name, err)
		}
	}
}

func TestWriteBackupNoOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.json")
	bk := &Backup{Vault: testVault, KeysharesBackup: []byte("a")}
	if err := writeBackup(path, bk); err != nil {
		t.Fatal(err)
	}
	if err := writeBackup(path, &Backup{Vault: otherVault, KeysharesBackup: []byte("b")}); err == nil {
		t.Fatal("second --backup-out to the same path must fail")
	}
	got, err := readBackup(path)
	if err != nil || got.Vault != testVault {
		t.Fatalf("original backup clobbered: %+v %v", got, err)
	}
}
