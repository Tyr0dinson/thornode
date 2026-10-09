package main

import (
	"bytes"
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
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
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
	require.EqualError(t, err, srv.URL+"/missing: status code 404")

	require.Error(t, get(srv.URL+"/bad", &res))

	require.Error(t, get("http://127.0.0.1:1/x", &res))
}

func TestSelectMemberCov(t *testing.T) {
	members := []string{"thor1aaa", "thor1bbb", "thor1ccc"}

	_, err := selectMember(nil)
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
		w := rkbCovStdin(t)
		_, err = io.WriteString(w, tc.input)
		require.NoError(t, err)
		require.NoError(t, w.Close())

		var got string
		var selErr error
		out := rkbCovCaptureStdout(t, func() { got, selErr = selectMember(members) })
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
	// The thorscan import's init() already sealed the sdk config, so main()
	// as shipped panics on its very first prefix assignment.
	require.PanicsWithValue(t, "Config is sealed", main)

	const (
		vault       = "tthorpub1vaultpubkey"
		vaultEddsa  = "tthorpub1vaulteddsakey"
		vaultHeight = int64(1000)
		tssHeight   = int64(990)
		mnemonic    = "test passphrase for keyshare recovery"
	)
	// each scrypt pass is expensive, so the ecdsa and eddsa backups share one
	// ciphertext (main decrypts each independently)
	plain := []byte(`{"keyshare":"local state"}`)
	ecdsaEnc, err := tss.EncryptRawKeyshares(plain, mnemonic)
	require.NoError(t, err)
	eddsaEnc := ecdsaEnc

	signer := cosmos.AccAddress(bytes.Repeat([]byte{0x01}, 20))
	other := cosmos.AccAddress(bytes.Repeat([]byte{0x02}, 20))

	// build the block tx: an unrelated msg, a TssPool from a different signer,
	// then the selected node's TssPool
	encCfg := app.MakeEncodingConfig()
	txb := encCfg.TxConfig.NewTxBuilder()
	require.NoError(t, txb.SetMsgs(
		&thorchain.MsgSend{FromAddress: other, ToAddress: signer},
		&thorchain.MsgTssPool{Signer: other, KeysharesBackup: []byte("wrong")},
		&thorchain.MsgTssPool{Signer: signer, KeysharesBackup: ecdsaEnc, KeysharesBackupEddsa: eddsaEnc},
	))
	txJSON, err := encCfg.TxConfig.TxJSONEncoder()(txb.GetTx())
	require.NoError(t, err)

	stdin := rkbCovStdin(t)
	var once sync.Map // write each follow-up input exactly once
	writeOnce := func(key, line string) {
		if _, loaded := once.LoadOrStore(key, true); !loaded {
			_, _ = io.WriteString(stdin, line)
		}
	}

	var mu sync.Mutex
	var heights []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thorchain/vault/" + vault:
			// main's first stdin reader has consumed the endpoint and vault
			// lines; selectMember uses a fresh reader, so feed it now
			writeOnce("choice", "2\n")
			_, _ = fmt.Fprintf(w, `{"block_height":%d,"status_since":%d,"pub_key":%q,"pub_key_eddsa":%q,"status":"ActiveVault"}`,
				vaultHeight, vaultHeight-5, vault, vaultEddsa)
		case "/thorchain/nodes":
			if r.URL.Query().Get("height") != strconv.FormatInt(vaultHeight-5, 10) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `[
				{"node_address":%q,"signer_membership":["other"]},
				{"node_address":%q,"signer_membership":["x",%q]},
				{"node_address":%q,"signer_membership":[%q]}
			]`, "tthor1notmember", other.String(), vault, signer.String(), vault)
		case "/thorchain/block":
			// selection is done once scanning starts; feed the mnemonic
			writeOnce("mnemonic", mnemonic+"\n")
			h, _ := strconv.ParseInt(r.URL.Query().Get("height"), 10, 64)
			mu.Lock()
			heights = append(heights, h)
			mu.Unlock()
			ts := time.Unix(0, 0).UTC().Format(time.RFC3339)
			if h == tssHeight {
				_, _ = fmt.Fprintf(w, `{"header":{"height":%d,"time":%q},"txs":[{"hash":"ABC","tx":%s,"result":{}}]}`, h, ts, txJSON)
				return
			}
			_, _ = fmt.Fprintf(w, `{"header":{"height":%d,"time":%q},"txs":[]}`, h, ts)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldEndpoint := thorscan.APIEndpoint
	defer func() { thorscan.APIEndpoint = oldEndpoint }()

	_, err = io.WriteString(stdin, srv.URL+"\n"+vault+"\n")
	require.NoError(t, err)

	dir := t.TempDir()
	t.Chdir(dir)
	rkbCovUnsealSDKConfig(t)

	out := rkbCovCaptureStdout(t, main)

	require.Equal(t, srv.URL, thorscan.APIEndpoint)
	require.Contains(t, out, "Select vault member:\n1. "+other.String()+"\n2. "+signer.String()+"\n")
	require.Contains(t, out, "Decrypted keyshare written to localstate-"+vault+".json\n")
	require.Contains(t, out, "Decrypted eddsa keyshare written to localstate-"+vaultEddsa+".json\n")

	got, err := os.ReadFile(filepath.Join(dir, "localstate-"+vault+".json"))
	require.NoError(t, err)
	require.Equal(t, plain, got)
	got, err = os.ReadFile(filepath.Join(dir, "localstate-"+vaultEddsa+".json"))
	require.NoError(t, err)
	require.Equal(t, plain, got)

	// scanning started 20 blocks before the vault height
	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, heights, vaultHeight-20)
	for _, h := range heights {
		require.GreaterOrEqual(t, h, vaultHeight-20)
		require.LessOrEqual(t, h, vaultHeight)
	}
	require.True(t, strings.HasPrefix(cosmos.GetConfig().GetBech32AccountAddrPrefix(), "tthor"))
}
