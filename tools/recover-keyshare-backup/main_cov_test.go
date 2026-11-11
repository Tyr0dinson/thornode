package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// rkbCovStdin replaces os.Stdin with a pipe for the duration of the test and
// returns the write end.
func rkbCovStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
		_ = w.Close()
	})
	return w
}

// rkbCovCaptureStdout redirects os.Stdout while fn runs.
func rkbCovCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	require.NoError(t, w.Close())
	return <-done
}

func TestCheckNilCov(t *testing.T) {
	// a nil error is a no-op (non-nil errors log.Fatal the process)
	require.NotPanics(t, func() { check(nil, "unused") })
}

func TestGetCov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, `{"a":1,"b":"x"}`)
		case "/bad":
			_, _ = io.WriteString(w, `{"a":`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, " no such path \n")
		}
	}))
	defer srv.Close()

	var res struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	require.NoError(t, get(srv.URL+"/ok", &res))
	require.Equal(t, 1, res.A)
	require.Equal(t, "x", res.B)

	err := get(srv.URL+"/missing", &res)
	// the error carries the trimmed start of the response body
	require.EqualError(t, err, srv.URL+"/missing: status code 404: no such path")

	require.Error(t, get(srv.URL+"/bad", &res))

	require.Error(t, get("http://127.0.0.1:1/x", &res))
}

func TestSelectMemberCov(t *testing.T) {
	members := []string{"thor1aaa", "thor1bbb", "thor1ccc"}

	_, err := selectMember(nil, bufio.NewReader(strings.NewReader("")))
	require.EqualError(t, err, "no options available")

	cases := []struct {
		input string
		want  string
		err   string
	}{
		{input: "2\n", want: "thor1bbb"},
		{input: "  3 \n", want: "thor1ccc"},
		{input: "0\n", err: "invalid choice, please enter a number between 1 and 3"},
		{input: "4\n", err: "invalid choice, please enter a number between 1 and 3"},
		{input: "abc\n", err: "invalid choice, please enter a number between 1 and 3"},
		{input: "", err: "error reading input: EOF"},
	}
	for _, tc := range cases {
		in := bufio.NewReader(strings.NewReader(tc.input))
		var got string
		var selErr error
		out := rkbCovCaptureStdout(t, func() { got, selErr = selectMember(members, in) })
		require.Contains(t, out, "Select vault member:\n1. thor1aaa\n2. thor1bbb\n3. thor1ccc\n")
		require.Contains(t, out, "Enter the number of member: ")
		if tc.err != "" {
			require.EqualError(t, selErr, tc.err, "input %q", tc.input)
			continue
		}
		require.NoError(t, selErr, "input %q", tc.input)
		require.Equal(t, tc.want, got)
	}
}

// rkbCovUnsealSDKConfig resets the cosmos-sdk global config seal.
func rkbCovUnsealSDKConfig(t *testing.T) {
	t.Helper()
	v := reflect.ValueOf(cosmos.GetConfig()).Elem()
	sealed := v.FieldByName("sealed")
	reflect.NewAt(sealed.Type(), unsafe.Pointer(sealed.UnsafeAddr())).Elem().SetBool(false) //nolint:gosec
	ch := v.FieldByName("sealedch")
	reflect.NewAt(ch.Type(), unsafe.Pointer(ch.UnsafeAddr())).Elem(). //nolint:gosec
										Set(reflect.ValueOf(make(chan struct{})))
}

func TestMainCov(t *testing.T) {
	if notMocknetCov() {
		t.Skip("mocknet only: fixtures and expected bech32 prefixes are mocknet")
	}
	const (
		keygenHeight = int64(1000)
		statusSince  = int64(995)
		tssHeight    = int64(990)
		scanWindow   = int64(20)
	)
	// encrypt a localstate exactly as bifrost does for the on-chain backup
	plain := []byte(`{"PubKey":"` + testVault + `","ParticipantKeys":["a","b"]}`)
	enc, err := tss.EncryptRawKeyshares(plain, testMnemonic)
	require.NoError(t, err)

	var mu sync.Mutex
	var heights []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thorchain/vault/" + testVault:
			// no membership: main falls back to the node list at status_since
			_, _ = fmt.Fprintf(w, `{"pub_key":%q,"block_height":%d,"status_since":%d}`, testVault, keygenHeight, statusSince)
		case "/thorchain/nodes":
			if r.URL.Query().Get("height") != strconv.FormatInt(statusSince, 10) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `[
				{"node_address":"tthor1notmember","signer_membership":["other"]},
				{"node_address":%q,"signer_membership":["x",%q]},
				{"node_address":%q,"signer_membership":[%q]}
			]`, otherNode, testVault, testNode, testVault)
		case "/thorchain/block":
			h, _ := strconv.ParseInt(r.URL.Query().Get("height"), 10, 64)
			mu.Lock()
			heights = append(heights, h)
			mu.Unlock()
			b := &rawBlock{}
			if h == tssHeight {
				// a TssPool from another member, then the selected node's
				b = block(
					tssPoolMsg("/types.MsgTssPool", testVault, otherNode, []byte("wrong")),
					tssPoolMsg("/types.MsgTssPool", testVault, testNode, enc),
				)
			}
			_ = json.NewEncoder(w).Encode(b)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	backupPath := filepath.Join(dir, "backup.json")
	outDir := filepath.Join(dir, "out")
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	// runMain runs main with args and stdin; TestMain already sealed the sdk
	// config, and main sets it up again.
	runMain := func(input string, args ...string) string {
		stdin := rkbCovStdin(t)
		_, wErr := io.WriteString(stdin, input)
		require.NoError(t, wErr)
		require.NoError(t, stdin.Close())
		os.Args = append([]string{"recover-keyshare-backup"}, args...)
		rkbCovUnsealSDKConfig(t)
		return rkbCovCaptureStdout(t, main)
	}

	// online step: prompts for endpoint and vault, lists the vault's members,
	// scans around keygen and writes the still-encrypted backup
	out := runMain(srv.URL+"\n"+testVault+"\n2\n", "--backup-out", backupPath, "--scan-window", strconv.FormatInt(scanWindow, 10))
	require.Contains(t, out, "Select vault member:\n1. "+otherNode+"\n2. "+testNode+"\n")
	require.Contains(t, out, "Encrypted backup written to "+backupPath)
	bk, err := readBackup(backupPath)
	require.NoError(t, err)
	require.Equal(t, testVault, bk.Vault)
	require.Equal(t, testNode, bk.Signer)
	require.Equal(t, tssHeight, bk.TxHeight)
	require.Equal(t, enc, bk.KeysharesBackup)
	mu.Lock()
	require.Contains(t, heights, keygenHeight)
	require.Contains(t, heights, tssHeight)
	for _, h := range heights {
		require.GreaterOrEqual(t, h, keygenHeight-scanWindow)
		require.LessOrEqual(t, h, keygenHeight+scanWindow)
	}
	mu.Unlock()

	// offline step: decrypts the backup file with the mnemonic
	out = runMain(testMnemonic+"\n", "--backup-in", backupPath, "--out-dir", outDir)
	localstate := filepath.Join(outDir, "localstate-"+testVault+".json")
	require.Contains(t, out, "Decrypted secp256k1 keyshare written to "+localstate+"\n")
	got, err := os.ReadFile(localstate)
	require.NoError(t, err)
	require.Equal(t, plain, got)
	require.True(t, strings.HasPrefix(cosmos.GetConfig().GetBech32AccountAddrPrefix(), "tthor"))
}
