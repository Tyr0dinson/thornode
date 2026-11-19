package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/core"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const testGenesis = "0000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func signed(t *testing.T, visible, token bool, expires time.Time) []byte {
	t.Helper()
	owner := append([]byte{0x41}, bytes.Repeat([]byte{1}, 20)...)
	to := append([]byte{0x41}, bytes.Repeat([]byte{2}, 20)...)
	addr := func(b []byte) string {
		if visible {
			return base58.CheckEncode(b[1:], 0x41)
		}
		return hex.EncodeToString(b)
	}
	tx := transactionJSON{Visible: visible, Signature: []string{strings.Repeat("ab", 65)}}
	tx.Raw.Timestamp = expires.Add(-time.Hour).UnixMilli()
	tx.Raw.Expiration = expires.UnixMilli()
	tx.Raw.RefBytes = "1234"
	tx.Raw.RefHash = "1234567812345678"
	tx.Raw.Data = "74657374"
	var payload proto.Message = &core.TransferContract{OwnerAddress: owner, ToAddress: to, Amount: 100}
	kind := "TransferContract"
	ctype := core.Transaction_Contract_TransferContract
	if token {
		payload = &core.TriggerSmartContract{OwnerAddress: owner, ContractAddress: to, Data: []byte{1, 2, 3, 4}}
		kind = "TriggerSmartContract"
		ctype = core.Transaction_Contract_TriggerSmartContract
		tx.Raw.FeeLimit = 100000000
	}
	parameter, err := anypb.New(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw := &core.TransactionRaw{RefBlockBytes: []byte{0x12, 0x34}, RefBlockHash: []byte{0x12, 0x34, 0x56, 0x78, 0x12, 0x34, 0x56, 0x78}, Timestamp: tx.Raw.Timestamp, Expiration: tx.Raw.Expiration, Data: []byte("test"), FeeLimit: tx.Raw.FeeLimit, Contract: []*core.Transaction_Contract{{Type: ctype, Parameter: parameter}}}
	rawBytes, err := proto.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	id := sha256.Sum256(rawBytes)
	tx.ID = hex.EncodeToString(id[:])
	tx.RawHex = hex.EncodeToString(rawBytes)
	// Decode the contract using the same JSON representation emitted by Bifrost.
	value := map[string]any{"owner_address": addr(owner)}
	if token {
		value["contract_address"] = addr(to)
		value["data"] = "01020304"
	} else {
		value["to_address"] = addr(to)
		value["amount"] = 100
	}
	b, _ := json.Marshal(tx)
	var object map[string]any
	_ = json.Unmarshal(b, &object)
	object["raw_data"].(map[string]any)["contract"] = []any{map[string]any{"type": kind, "parameter": map[string]any{"type_url": parameter.TypeUrl, "value": value}}}
	b, _ = json.Marshal(object)
	return b
}

type fakeNode struct {
	mu       sync.Mutex
	head     int64
	age      time.Duration
	genesis  string
	fork     bool
	missing  bool
	response string
	lookup   bool
	delay    time.Duration
	writes   [][]byte
	reads    map[string]int
	tx       []byte
	server   *httptest.Server
}

func node(t *testing.T) *fakeNode {
	t.Helper()
	n := &fakeNode{head: 100000, genesis: testGenesis, response: "success", reads: map[string]int{}}
	n.server = httptest.NewServer(http.HandlerFunc(n.handle))
	t.Cleanup(n.server.Close)
	return n
}

func (n *fakeNode) upstream() *Upstream {
	u, _ := url.Parse(n.server.URL)
	rpc, _ := url.Parse(n.server.URL + "/jsonrpc")
	return newUpstream(u, rpc, 100*time.Millisecond)
}

func (n *fakeNode) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reads[r.URL.Path]++
	switch r.URL.Path {
	case "/wallet/getnowblock", "/wallet/getblockbynum":
		h := n.head
		if r.URL.Path == "/wallet/getblockbynum" {
			var q struct {
				Num int64 `json:"num"`
			}
			_ = json.Unmarshal(body, &q)
			h = q.Num
		}
		if n.missing && h != 0 {
			reply(w, []byte(`{}`))
			return
		}
		id := fmt.Sprintf("%016x%s", h, strings.Repeat("a", 48))
		if h == 0 {
			id = n.genesis
		} else if n.fork {
			id = fmt.Sprintf("%016x%s", h, strings.Repeat("b", 48))
		}
		b, _ := json.Marshal(map[string]any{"blockID": id, "block_header": map[string]any{"raw_data": map[string]any{"number": h, "timestamp": time.Now().Add(-n.age).UnixMilli()}}})
		reply(w, b)
	case "/jsonrpc":
		var q struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &q)
		if q.Method == "eth_getBlockByNumber" {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": map[string]string{"hash": "0x" + n.genesis}})
			reply(w, b)
			return
		}
		result := fmt.Sprintf("0x%x", n.head-20)
		if q.Method == "eth_call" {
			result = "0x0000000000000000000000000000000000000000000000000000000000000006"
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result})
		reply(w, b)
	case "/wallet/gettransactionbyid":
		if n.lookup && len(n.tx) > 0 {
			reply(w, n.tx)
		} else {
			reply(w, []byte(`{}`))
		}
	case "/wallet/gettransactioninfobyid":
		if n.missing {
			reply(w, []byte(`{}`))
			return
		}
		var q struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(body, &q)
		b, _ := json.Marshal(map[string]any{"id": q.Value, "blockNumber": 99990, "blockTimeStamp": time.Now().UnixMilli()})
		reply(w, b)
	case "/wallet/broadcasttransaction":
		n.writes = append(n.writes, bytes.Clone(body))
		n.tx = bytes.Clone(body)
		if n.delay > 0 {
			time.Sleep(n.delay)
		}
		if n.response == "disconnect" {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		if n.response == "duplicate" {
			reply(w, []byte(`{"result":false,"code":"DUP_TRANSACTION_ERROR"}`))
			return
		}
		if n.response == "wrongid" {
			reply(w, []byte(`{"result":true,"txid":"incorrect"}`))
			return
		}
		var q struct {
			ID string `json:"txID"`
		}
		_ = json.Unmarshal(body, &q)
		b, _ := json.Marshal(map[string]any{"result": true, "txid": q.ID})
		reply(w, b)
	default:
		reply(w, []byte(`{}`))
	}
}

func fixture(t *testing.T) (*Proxy, *Store, *fakeNode, *fakeNode) {
	t.Helper()
	a, b := node(t), node(t)
	s, err := openStore(filepath.Join(t.TempDir(), "journal.db"), testGenesis)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.db.Close() })
	p := newProxy(Config{Genesis: testGenesis, BroadcastEnabled: true, PollSeconds: 10}, s, a.upstream(), b.upstream())
	p.logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	p.probe(context.Background())
	if !p.healthy() || !p.snapshot().Local {
		t.Fatal("fixture not healthy")
	}
	return p, s, a, b
}

func request(p http.Handler, route string, body []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", route, bytes.NewReader(body)))
	return w
}

func TestSignedValidation(t *testing.T) {
	for _, visible := range []bool{false, true} {
		for _, token := range []bool{false, true} {
			t.Run(fmt.Sprintf("visible=%t/token=%t", visible, token), func(t *testing.T) {
				b := signed(t, visible, token, time.Now().Add(time.Hour))
				if _, err := parseSigned(b); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	good := signed(t, false, false, time.Now().Add(time.Hour))
	for name, change := range map[string]func([]byte) []byte{
		"id":                func(b []byte) []byte { return bytes.Replace(b, []byte(`"txID":"`), []byte(`"txID":"0`), 1) },
		"json-raw-mismatch": func(b []byte) []byte { return bytes.Replace(b, []byte(`"amount":100`), []byte(`"amount":101`), 1) },
		"signature":         func(b []byte) []byte { return bytes.Replace(b, []byte(strings.Repeat("ab", 65)), []byte("ab"), 1) },
		"case-alias":        func(b []byte) []byte { return bytes.Replace(b, []byte(`"raw_data"`), []byte(`"RAW_DATA"`), 1) },
		"unknown":           func(b []byte) []byte { return append([]byte(`{"unknown":1,`), b[1:]...) },
		"duplicate":         func(b []byte) []byte { return append([]byte(`{"txID":"x",`), b[1:]...) },
		"trailing":          func(b []byte) []byte { return append(b, []byte(` {}`)...) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSigned(change(bytes.Clone(good))); err == nil {
				t.Fatal("accepted invalid transaction")
			}
		})
	}
}

func TestHistoricalFallbackAndStickyMode(t *testing.T) {
	p, s, local, ext := fixture(t)
	p.setMode("local")
	local.missing = true
	id := strings.Repeat("1", 64)
	q := []byte(fmt.Sprintf(`{"value":%q}`, id))
	w := request(p, "/wallet/gettransactioninfobyid", q)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if mode, _ := s.mode(); mode != "external" {
		t.Fatal(mode)
	}
	local.missing = false
	p.probe(context.Background())
	if mode, _ := s.mode(); mode != "external" {
		t.Fatal("automatic failback")
	}
	w = request(p, "/jsonrpc", []byte(`{"jsonrpc":"2.0","id":7,"method":"eth_call","params":[{},"latest"]}`))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	ext.mu.Lock()
	ext.missing = true
	ext.mu.Unlock()
	w = request(p, "/wallet/gettransactioninfobyid", q)
	if w.Code != 503 {
		t.Fatal("absence incorrectly claimed", w.Code)
	}
	w = request(p, "/wallet/gettransactionbyid", q)
	if w.Code != 503 {
		t.Fatal("lookup incorrectly claimed absence", w.Code)
	}
}

func TestKnownBlockFloorUsesExternal(t *testing.T) {
	p, _, local, _ := fixture(t)
	p.cfg.BlockFloor = 90000
	p.setMode("local")
	local.mu.Lock()
	before := local.reads["/wallet/getblockbynum"]
	local.mu.Unlock()
	if w := request(p, "/wallet/getblockbynum", []byte(`{"num":89999}`)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	local.mu.Lock()
	defer local.mu.Unlock()
	if local.reads["/wallet/getblockbynum"] != before {
		t.Fatal("queried known pruned range")
	}
}

func TestHealthAdmission(t *testing.T) {
	for _, problem := range []string{"stale", "lagging", "fork", "wrong-genesis"} {
		t.Run(problem, func(t *testing.T) {
			p, _, local, _ := fixture(t)
			p.setMode("local")
			switch problem {
			case "stale":
				local.age = time.Minute
			case "lagging":
				local.head -= 100
			case "fork":
				local.fork = true
			case "wrong-genesis":
				local.genesis = strings.Repeat("0", 64)
			}
			p.probe(context.Background())
			if p.currentMode() != "external" || p.snapshot().Local {
				t.Fatal("did not fail closed")
			}
			w := httptest.NewRecorder()
			p.admin(w, httptest.NewRequest("POST", "/arm-local", nil))
			if w.Code != 503 {
				t.Fatal("unsafe admission")
			}
		})
	}
	p, _, _, _ := fixture(t)
	p.healthMu.Lock()
	p.health.Checked = time.Now().Add(-time.Minute)
	p.healthMu.Unlock()
	if w := request(p, "/wallet/getnowblock", []byte(`null`)); w.Code != 503 {
		t.Fatal("stale health accepted")
	}
}

func TestReadOnlyAndAllowlist(t *testing.T) {
	p, _, local, ext := fixture(t)
	p.cfg.BroadcastEnabled = false
	for _, route := range []string{"/wallet/broadcasttransaction", "/wallet/createtransaction", "/wallet/triggersmartcontract"} {
		if w := request(p, route, signed(t, false, false, time.Now().Add(time.Hour))); w.Code != 403 {
			t.Fatal(route, w.Code)
		}
	}
	for _, b := range []string{`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":[]}`, `[{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{},"latest"]}]`, `{"jsonrpc":"2.0","method":"eth_call","params":[{},"latest"]}`} {
		if w := request(p, "/jsonrpc", []byte(b)); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if len(local.writes)+len(ext.writes) != 0 {
		t.Fatal("broadcast escaped read-only mode")
	}
}

func TestBroadcastExactRetry(t *testing.T) {
	for _, problem := range []string{"disconnect", "duplicate", "wrongid", "timeout"} {
		t.Run(problem, func(t *testing.T) {
			p, s, local, ext := fixture(t)
			p.setMode("local")
			local.response = problem
			if problem == "timeout" {
				local.delay = 150 * time.Millisecond
			}
			body := signed(t, true, true, time.Now().Add(time.Hour))
			w := request(p, "/wallet/broadcasttransaction", body)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			local.mu.Lock()
			defer local.mu.Unlock()
			ext.mu.Lock()
			defer ext.mu.Unlock()
			if len(local.writes) != 1 || len(ext.writes) != 1 || !bytes.Equal(local.writes[0], ext.writes[0]) || !bytes.Equal(ext.writes[0], body) {
				t.Fatal("retry changed payload or count")
			}
			e, _ := parseSigned(body)
			stored, _ := s.get(e.ID)
			if !stored.Accepted || stored.Attempts != 2 {
				t.Fatal(stored)
			}
		})
	}
}

func TestLostAckReconcilesWithoutSecondSend(t *testing.T) {
	p, _, local, ext := fixture(t)
	p.setMode("local")
	local.response = "disconnect"
	local.lookup = true
	if w := request(p, "/wallet/broadcasttransaction", signed(t, false, false, time.Now().Add(time.Hour))); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if len(local.writes) != 1 || len(ext.writes) != 0 {
		t.Fatal("unnecessary resend")
	}
}

func TestUnverifiedDuplicateIsNotSuccess(t *testing.T) {
	p, s, _, ext := fixture(t)
	ext.response = "duplicate"
	body := signed(t, false, false, time.Now().Add(time.Hour))
	for i := 0; i < 6; i++ {
		w := request(p, "/wallet/broadcasttransaction", body)
		if w.Code != 503 || bytes.Contains(w.Body.Bytes(), []byte("DUP_TRANSACTION_ERROR")) {
			t.Fatal("unsafe duplicate response", w.Body.String())
		}
	}
	if len(ext.writes) != 4 {
		t.Fatal("attempt limit", len(ext.writes))
	}
	e, _ := parseSigned(body)
	e, _ = s.get(e.ID)
	if e.Accepted {
		t.Fatal("unverified accepted")
	}
}

func TestConcurrentDedupAndConflict(t *testing.T) {
	p, _, _, ext := fixture(t)
	body := signed(t, false, false, time.Now().Add(time.Hour))
	var wg sync.WaitGroup
	var failures atomic.Int32
	server := httptest.NewServer(p)
	defer server.Close()
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(server.URL+"/wallet/broadcasttransaction", "application/json", bytes.NewReader(body))
			if err != nil {
				failures.Add(1)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != 200 {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 || len(ext.writes) != 1 {
		t.Fatal("dedup failed", failures.Load(), len(ext.writes))
	}
	changed := bytes.Replace(body, []byte(strings.Repeat("ab", 65)), []byte(strings.Repeat("cd", 65)), 1)
	if w := request(p, "/wallet/broadcasttransaction", changed); w.Code != 409 {
		t.Fatal("conflicting signature accepted", w.Code)
	}
}

func TestExpiryAndStoredBytes(t *testing.T) {
	p, s, _, ext := fixture(t)
	expired := signed(t, false, false, time.Now().Add(-time.Minute))
	if w := request(p, "/wallet/broadcasttransaction", expired); w.Code != 503 || len(ext.writes) != 0 {
		t.Fatal("expired transaction sent")
	}
	body := signed(t, false, false, time.Now().Add(time.Hour))
	e, _ := parseSigned(body)
	e.Attempts = 1
	if err := s.save(e); err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, body, "", "  ")
	if w := request(p, "/wallet/broadcasttransaction", pretty.Bytes()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !bytes.Equal(ext.writes[0], body) {
		t.Fatal("did not retry immutable original")
	}
}

type faultJournal struct {
	journal
	calls int
	fail  int
}

func (s *faultJournal) save(e *Entry) error {
	s.calls++
	if s.calls == s.fail {
		return errStore
	}
	return s.journal.save(e)
}

func TestJournalFailures(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			p, s, _, ext := fixture(t)
			p.store = &faultJournal{journal: s, fail: failAt}
			body := signed(t, false, false, time.Now().Add(time.Hour))
			w := request(p, "/wallet/broadcasttransaction", body)
			if w.Code != 503 || !p.unsafe.Load() {
				t.Fatal("store failure did not close admission")
			}
			want := 0
			if failAt == 3 {
				want = 1
			}
			if len(ext.writes) != want {
				t.Fatal("unexpected dispatch", len(ext.writes))
			}
			if failAt == 3 {
				e, _ := parseSigned(body)
				stored, _ := s.get(e.ID)
				if stored.Accepted || stored.Attempts != 1 {
					t.Fatal("lost pending state")
				}
				ext.lookup = true
				p2 := newProxy(p.cfg, s, p.local, p.external)
				p2.probe(context.Background())
				if w = request(p2, "/wallet/broadcasttransaction", body); w.Code != 200 || len(ext.writes) != 1 {
					t.Fatal("ack recovery failed")
				}
			}
		})
	}
}

func TestJournalLockAndBinding(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "journal.db")
	s, err := openStore(filename, testGenesis)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := openStore(filename, testGenesis); err == nil {
		second.db.Close()
		t.Fatal("second writer admitted")
	}
	s.setMode("external")
	s.db.Close()
	if second, err := openStore(filename, strings.Repeat("b", 64)); err == nil {
		second.db.Close()
		t.Fatal("network mismatch admitted")
	}
	s, err = openStore(filename, testGenesis)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if mode, _ := s.mode(); mode != "external" {
		t.Fatal("mode not persisted")
	}
}

func TestJournalPermissionsAfterRemount(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "journal.db")
	s, err := openStore(filename, testGenesis)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	if err = os.Chmod(filename, 0660); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(filename, testGenesis)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	info, err := os.Stat(filename)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("journal mode not restored", err)
	}
}

func TestProcessCrashRecovery(t *testing.T) {
	if os.Getenv("TRON_PROXY_TEST_CRASH") == "1" {
		s, err := openStore(os.Getenv("TRON_PROXY_TEST_DB"), testGenesis)
		if err != nil {
			os.Exit(10)
		}
		body, _ := os.ReadFile(os.Getenv("TRON_PROXY_TEST_BODY"))
		u, _ := url.Parse(os.Getenv("TRON_PROXY_TEST_UPSTREAM"))
		rpc, _ := url.Parse(u.String() + "/jsonrpc")
		up := newUpstream(u, rpc, time.Second)
		p := newProxy(Config{Genesis: testGenesis, BroadcastEnabled: true, PollSeconds: 10}, &crashJournal{journal: s}, up, up)
		p.probe(context.Background())
		request(p, "/wallet/broadcasttransaction", body)
		os.Exit(12)
	}
	dir := t.TempDir()
	filename := filepath.Join(dir, "journal.db")
	bodyFile := filepath.Join(dir, "body.json")
	body := signed(t, false, false, time.Now().Add(time.Hour))
	if err := os.WriteFile(bodyFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	local, ext := node(t), node(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessCrashRecovery$")
	cmd.Env = append(os.Environ(), "TRON_PROXY_TEST_CRASH=1", "TRON_PROXY_TEST_DB="+filename, "TRON_PROXY_TEST_BODY="+bodyFile, "TRON_PROXY_TEST_UPSTREAM="+ext.server.URL)
	err := cmd.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 {
		t.Fatal("child did not crash at expected point", err)
	}
	s, err := openStore(filename, testGenesis)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if len(ext.writes) != 1 {
		t.Fatal("child did not dispatch exactly once")
	}
	ext.lookup = true
	p := newProxy(Config{Genesis: testGenesis, BroadcastEnabled: true, PollSeconds: 10}, s, local.upstream(), ext.upstream())
	p.probe(context.Background())
	if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 200 || len(ext.writes) != 1 {
		t.Fatal("crash recovery resent verified transaction", w.Code)
	}
}

type crashJournal struct {
	journal
	calls int
}

func (s *crashJournal) save(e *Entry) error {
	s.calls++
	if s.calls == 3 {
		os.Exit(23)
	}
	return s.journal.save(e)
}

type brokenMode struct{ journal }

func (s brokenMode) setMode(string) error { return errStore }

func TestModePersistenceFailure(t *testing.T) {
	p, s, local, _ := fixture(t)
	p.setMode("local")
	p.store = brokenMode{s}
	local.missing = true
	w := request(p, "/wallet/getnowblock", []byte(`null`))
	if w.Code != 503 || !p.unsafe.Load() {
		t.Fatal("stateless fallback was allowed")
	}
}

func TestCorruptEntryFailsClosed(t *testing.T) {
	p, s, _, ext := fixture(t)
	body := signed(t, false, false, time.Now().Add(time.Hour))
	e, _ := parseSigned(body)
	e.Fingerprint = "bad"
	_ = s.save(e)
	if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 503 || len(ext.writes) != 0 || !p.unsafe.Load() {
		t.Fatal("corrupt entry dispatched")
	}
}

func TestHTTPFailuresAndNoRedirects(t *testing.T) {
	for _, code := range []int{301, 302, 307, 308, 401, 404, 429, 500, 502, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			p, _, _, _ := fixture(t)
			p.setMode("local")
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(code)
			}))
			defer source.Close()
			u, _ := url.Parse(source.URL)
			p.local = newUpstream(u, u, 100*time.Millisecond)
			if w := request(p, "/wallet/getnowblock", []byte(`null`)); w.Code != 200 || p.currentMode() != "external" || redirected.Load() != 0 {
				t.Fatal("unsafe redirect or failed fallback", w.Code)
			}
		})
	}
}

func TestAdminAndEndpoints(t *testing.T) {
	p, _, _, _ := fixture(t)
	for _, route := range []string{"/arm-local", "/external"} {
		w := httptest.NewRecorder()
		p.admin(w, httptest.NewRequest("POST", route, nil))
		if w.Code != 200 {
			t.Fatal(route, w.Code)
		}
	}
	w := httptest.NewRecorder()
	p.admin(w, httptest.NewRequest("GET", "/status", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"external"`) {
		t.Fatal(w.Body.String())
	}
	for _, route := range []string{"/healthz", "/readyz", "/metrics"} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != 200 {
			t.Fatal(route, w.Code)
		}
	}
	if w := request(p, "/wallet/getnowblock?target=elsewhere", []byte(`null`)); w.Code != 400 {
		t.Fatal("query accepted")
	}
	if w := request(p, "/wallet/getnowblock", bytes.Repeat([]byte("x"), 65537)); w.Code != 413 {
		t.Fatal("oversized request accepted")
	}
	if w := request(p, "/arm-local", []byte(`{}`)); w.Code != 400 {
		t.Fatal("public admin enabled")
	}
}

func TestInvalidResponses(t *testing.T) {
	id := strings.Repeat("1", 64)
	q := []byte(fmt.Sprintf(`{"value":%q}`, id))
	for _, r := range []string{`{}`, `null`, `{"id":"wrong","blockNumber":1,"blockTimeStamp":1}`, `{"id":"` + id + `","blockNumber":0}`} {
		if validResponse("/wallet/gettransactioninfobyid", q, []byte(r)) {
			t.Fatal("invalid receipt accepted", r)
		}
	}
	q = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{},"latest"]}`)
	for _, r := range []string{`{"jsonrpc":"2.0","id":1,"result":null}`, `{"jsonrpc":"2.0","id":2,"result":"0x12"}`, `{"jsonrpc":"2.0","id":1,"result":"0xg"}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-1}}`} {
		if validResponse("/jsonrpc", q, []byte(r)) {
			t.Fatal("invalid RPC response accepted")
		}
	}
}

func FuzzSigned(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"txID":null}`))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = parseSigned(b) })
}
