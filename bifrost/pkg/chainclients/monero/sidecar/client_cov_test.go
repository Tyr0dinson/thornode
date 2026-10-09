package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/config"
)

var (
	hashACov = strings.Repeat("a", 64)
	hashBCov = strings.Repeat("b", 64)
	hashCCov = strings.Repeat("c", 64)
)

func writeJSONCov(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestNewClientCov(t *testing.T) {
	t.Parallel()

	_, err := NewClient(config.BifrostChainConfiguration{})
	require.EqualError(t, err, "sidecar endpoint is empty")

	c, err := NewClient(config.BifrostChainConfiguration{SignerEndpoint: " http://127.0.0.1:1/ "})
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:1", c.endpoint)

	_, err = NewClientWithEndpoint(config.BifrostChainConfiguration{UserName: "u"}, "http://127.0.0.1:1")
	require.ErrorContains(t, err, "monero daemon requires both username and password")

	_, err = NewClientWithEndpoint(config.BifrostChainConfiguration{ChainNetwork: "bogus"}, "http://127.0.0.1:1")
	require.ErrorContains(t, err, "unsupported xmr chain_network")

	_, err = NewClientWithEndpoint(config.BifrostChainConfiguration{}, "ftp://127.0.0.1:1")
	require.Error(t, err)

	_, err = NewClientWithEndpoint(config.BifrostChainConfiguration{}, "https://")
	require.EqualError(t, err, "invalid sidecar endpoint host")

	_, err = NewClientWithEndpoint(config.BifrostChainConfiguration{}, "http://127.0.0.1:1/%zz")
	require.EqualError(t, err, "invalid sidecar endpoint URL")

	_, err = NewClientWithEndpoint(config.BifrostChainConfiguration{}, "https://signer.example")
	require.ErrorContains(t, err, "requires signer_authorization_bearer")

	c, err = NewClientWithEndpoint(config.BifrostChainConfiguration{
		BlockScanner: config.BifrostBlockScannerConfiguration{HTTPRequestTimeout: 200 * time.Second},
	}, "127.0.0.1:9")
	require.NoError(t, err)
	require.Equal(t, "https://127.0.0.1:9", c.endpoint)
	require.Equal(t, 200*time.Second, c.client.Timeout)
	require.Equal(t, 200*time.Second, c.signClient.Timeout)

	require.False(t, IsLoopbackHost(" "))
	require.True(t, IsLoopbackHost("LOCALHOST"))
	require.False(t, IsLoopbackHost("8.8.8.8"))
}

func TestClientDKGAndSignValidationCov(t *testing.T) {
	t.Parallel()

	resp := map[string]any{}
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		writeJSONCov(w, resp[r.URL.Path])
	})

	// Empty-valued responses are rejected.
	resp["/v1/monero/dkg/round1"] = DKGRound1Resp{}
	_, err := c.DKGRound1(DKGRound1Req{})
	require.EqualError(t, err, "dkg round1 endpoint returned empty commitments")

	resp["/v1/monero/dkg/round3"] = DKGRound3Resp{GroupKey: "g"}
	_, err = c.DKGRound3(DKGRound3Req{})
	require.EqualError(t, err, "dkg round3 endpoint returned empty address")

	resp["/v1/monero/dkg/finalize"] = DKGFinalizeResp{Success: false}
	_, err = c.DKGFinalize(DKGFinalizeReq{})
	require.EqualError(t, err, "dkg finalize endpoint returned unsuccessful response")

	resp["/v1/monero/sign/prepare"] = SignPrepareResp{Preprocess: "p"}
	_, err = c.SignPrepare(SignPrepareReq{})
	require.EqualError(t, err, "sign prepare endpoint returned empty session_id")

	resp["/v1/monero/sign/partial"] = SignPartialResp{}
	_, err = c.SignPartial(SignPartialReq{})
	require.EqualError(t, err, "sign partial endpoint returned empty signature_share")

	resp["/v1/monero/sign/combine"] = SignCombineResp{TxHash: "t"}
	_, err = c.SignCombine(SignCombineReq{})
	require.EqualError(t, err, "sign combine endpoint returned empty tx_key")

	resp["/v1/monero/sign/combine"] = SignCombineResp{TxKey: "k"}
	_, err = c.SignCombine(SignCombineReq{})
	require.EqualError(t, err, "sign combine endpoint returned empty tx_hash")

	// Happy paths.
	resp["/v1/monero/dkg/round1"] = DKGRound1Resp{Commitments: "cm"}
	r1, err := c.DKGRound1(DKGRound1Req{})
	require.NoError(t, err)
	require.Equal(t, "cm", r1.Commitments)

	resp["/v1/monero/dkg/finalize"] = DKGFinalizeResp{Success: true, KeyBytesHex: "ab"}
	fin, err := c.DKGFinalize(DKGFinalizeReq{})
	require.NoError(t, err)
	require.Equal(t, "ab", fin.KeyBytesHex)

	resp["/v1/monero/sign/combine"] = SignCombineResp{TxHash: "t", TxKey: "k"}
	comb, err := c.SignCombine(SignCombineReq{})
	require.NoError(t, err)
	require.Equal(t, "t", comb.TxHash)
}

func TestClientSignCombineNilContextAndCancelCov(t *testing.T) {
	t.Parallel()

	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		writeJSONCov(w, SignCombineResp{TxHash: "t", TxKey: "k"})
	})
	//nolint:staticcheck // deliberately exercising the nil-context guard
	out, err := c.SignCombineContext(nil, SignCombineReq{})
	require.NoError(t, err)
	require.Equal(t, "k", out.TxKey)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.SignCombineContext(ctx, SignCombineReq{})
	require.ErrorContains(t, err, "sign combine canceled")
	require.ErrorIs(t, err, context.Canceled)
}

func TestSignCombineRetryPolicyCov(t *testing.T) {
	t.Parallel()

	c := &Client{}
	n, b, m := c.signCombineRetryPolicy()
	require.Equal(t, defaultSignCombineMaxAttempts, n)
	require.Equal(t, defaultSignCombineInitialBackoff, b)
	require.Equal(t, defaultSignCombineMaxBackoff, m)

	c = &Client{signCombineMaxAttempts: 5, signCombineInitialBackoff: -1, signCombineMaxBackoff: time.Millisecond}
	n, b, m = c.signCombineRetryPolicy()
	require.Equal(t, 5, n)
	require.Equal(t, time.Duration(0), b)
	require.Equal(t, time.Millisecond, m)

	c = &Client{signCombineMaxAttempts: 2, signCombineInitialBackoff: time.Hour, signCombineMaxBackoff: time.Second}
	_, b, m = c.signCombineRetryPolicy()
	require.Equal(t, time.Hour, b)
	require.Equal(t, time.Hour, m, "max backoff is raised to the initial backoff")

	c.SetSignCombineIdempotent(true)
	require.Equal(t, idempotentSignCombineMaxAttempts, c.signCombineMaxAttempts)
	c.SetSignCombineIdempotent(false)
	require.Equal(t, defaultSignCombineMaxAttempts, c.signCombineMaxAttempts)

	require.Equal(t, 2*time.Second, nextSignCombineBackoff(time.Second, 3*time.Second))
	require.Equal(t, 3*time.Second, nextSignCombineBackoff(2*time.Second, 3*time.Second))
	require.Equal(t, 3*time.Second, nextSignCombineBackoff(4*time.Second, 3*time.Second))
	require.Equal(t, time.Duration(math.MaxInt64), nextSignCombineBackoff(time.Duration(math.MaxInt64/2+1), time.Duration(math.MaxInt64)))
}

type timeoutErrCov struct{}

func (timeoutErrCov) Error() string   { return "timeout" }
func (timeoutErrCov) Timeout() bool   { return true }
func (timeoutErrCov) Temporary() bool { return true }

var _ net.Error = timeoutErrCov{}

func TestIsRetryableSignCombineErrorCov(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	require.False(t, isRetryableSignCombineError(ctx, nil))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.False(t, isRetryableSignCombineError(canceled, errors.New("x")))

	for status, want := range map[int]bool{
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          true,
		http.StatusRequestTimeout:      true,
		http.StatusTooEarly:            true,
		http.StatusTooManyRequests:     true,
		http.StatusBadRequest:          false,
		http.StatusConflict:            false,
	} {
		err := fmt.Errorf("wrap: %w", &HTTPError{Status: status})
		require.Equal(t, want, isRetryableSignCombineError(ctx, err), status)
	}
	require.True(t, isRetryableSignCombineError(ctx, fmt.Errorf("w: %w", timeoutErrCov{})))
	require.True(t, isRetryableSignCombineError(ctx, fmt.Errorf("w: %w", io.EOF)))
	require.True(t, isRetryableSignCombineError(ctx, io.ErrUnexpectedEOF))
	require.False(t, isRetryableSignCombineError(ctx, errors.New("application")))
	require.True(t, isRetryableSignCombineError(nil, io.EOF)) //nolint:staticcheck // nil-context guard

	require.NoError(t, waitForSignCombineRetry(ctx, 0))
	require.NoError(t, waitForSignCombineRetry(ctx, time.Millisecond))
	require.ErrorIs(t, waitForSignCombineRetry(canceled, time.Hour), context.Canceled)
}

func TestSignCombineRetriesAndBacksOffCov(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		writeJSONCov(w, SignCombineResp{TxHash: "t", TxKey: "k"})
	})
	c.SetSignCombineIdempotent(true)
	c.signCombineInitialBackoff = time.Millisecond
	c.signCombineMaxBackoff = 2 * time.Millisecond
	out, err := c.SignCombine(SignCombineReq{MsgID: "m"})
	require.NoError(t, err)
	require.Equal(t, "t", out.TxHash)
	require.EqualValues(t, 3, calls.Load())

	// Non-retryable error stops immediately.
	calls.Store(0)
	c2 := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad", http.StatusBadRequest)
	})
	c2.SetSignCombineIdempotent(true)
	_, err = c2.SignCombine(SignCombineReq{})
	require.ErrorContains(t, err, "fail to call sign combine endpoint")
	require.EqualValues(t, 1, calls.Load())
}

func TestClientMiscEndpointsCov(t *testing.T) {
	t.Parallel()

	key := strings.Repeat("A", 64)
	var lastBody map[string]any
	var lastAuth string
	c := newCovClient(t, config.BifrostChainConfiguration{SignerAuthorizationBearer: "tok"}, func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		lastBody = nil
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&lastBody)
		}
		switch {
		case r.URL.Path == "/v1/monero/sessions/clear":
			writeJSONCov(w, map[string]any{"cleared_sessions": 4, "status": "ok"})
		case r.URL.Path == "/v1/monero/broadcast":
			writeJSONCov(w, BroadcastResp{TxHash: "bh", TxKey: " " + strings.ToUpper(key) + " "})
		case r.URL.Path == "/v1/monero/balance/vault%2F1" || r.URL.RawPath == "/v1/monero/balance/vault%2F1":
			writeJSONCov(w, map[string]any{"result": Balance{Balance: 10, UnlockedBalance: 7}})
		case r.URL.Path == "/v1/monero/transfers" && r.URL.Query().Get("height") == "12":
			writeJSONCov(w, Transfers{Incoming: []Transfer{{TxID: "in12"}}})
		case r.URL.Path == "/v1/monero/transfers" && r.URL.Query().Get("mempool") == "true":
			writeJSONCov(w, Transfers{Incoming: []Transfer{{TxID: "mem"}}})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/addresses/"):
			writeJSONCov(w, Addresses{Addresses: []string{"a1", "a2"}})
		case r.URL.Path == "/v1/monero/fee-rate":
			writeJSONCov(w, FeeRate{FeeRatePerWeight: 20})
		case r.URL.Path == "/v1/monero/keys":
			writeJSONCov(w, map[string]any{"vaults": []VaultKey{{VaultPubKey: "v", Signable: true}}})
		case r.URL.Path == "/v1/monero/keys/v1/export":
			writeJSONCov(w, map[string]any{"key_bytes_hex": "beef"})
		case r.URL.Path == "/v1/monero/outputs/mark-spent":
			writeJSONCov(w, MarkSpentResp{Marked: 2, NotFound: 1})
		case r.URL.Path == "/v1/monero/outputs/mark-observed":
			writeJSONCov(w, Transfer{TxID: hashACov, Height: 5})
		default:
			http.Error(w, "unexpected "+r.URL.String(), http.StatusNotFound)
		}
	})

	n, err := c.ClearSessions()
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, "Bearer tok", lastAuth)

	n, err = (*Client)(nil).ClearSessions()
	require.NoError(t, err)
	require.Zero(t, n)

	_, err = c.Broadcast(BroadcastReq{TxKey: "short"})
	require.EqualError(t, err, "broadcast tx_key must be canonical 32-byte hex")
	_, err = c.Broadcast(BroadcastReq{TxKey: strings.Repeat("z", 64)})
	require.EqualError(t, err, "broadcast tx_key must be canonical 32-byte hex")
	br, err := c.Broadcast(BroadcastReq{TxKey: key, TxHex: "00"})
	require.NoError(t, err)
	require.Equal(t, strings.ToLower(key), br.TxKey)
	require.Equal(t, strings.ToLower(key), lastBody["tx_key"])
	_, err = c.Broadcast(BroadcastReq{TxKey: hashBCov})
	require.EqualError(t, err, "broadcast endpoint returned mismatched tx_key")

	bal, err := c.Balance("vault/1")
	require.NoError(t, err)
	require.EqualValues(t, 10, bal.Balance)
	require.EqualValues(t, 7, bal.UnlockedBalance)

	tr, err := c.TransfersAt(12)
	require.NoError(t, err)
	require.Equal(t, "in12", tr.Incoming[0].TxID)

	mem, err := c.Mempool()
	require.NoError(t, err)
	require.Equal(t, "mem", mem.Incoming[0].TxID)

	addrs, err := c.Addresses("v")
	require.NoError(t, err)
	require.Equal(t, []string{"a1", "a2"}, addrs)

	fee, err := c.FeeRate()
	require.NoError(t, err)
	require.EqualValues(t, 20, fee)

	vaults, err := c.Vaults()
	require.NoError(t, err)
	require.Equal(t, []VaultKey{{VaultPubKey: "v", Signable: true}}, vaults)

	exported, err := c.ExportKeys("v1")
	require.NoError(t, err)
	require.Equal(t, "beef", exported)

	ms, err := c.MarkSpent("v", "tx", []SpentRef{{TxHash: "in"}}, true, nil)
	require.NoError(t, err)
	require.Equal(t, MarkSpentResp{Marked: 2, NotFound: 1}, ms)
	require.Equal(t, OutgoingOriginChainCommitted, lastBody["origin"])
	require.Equal(t, true, lastBody["skip_verification"])

	_, err = c.MarkSpent("v", "tx", nil, false, &OutboundMeta{
		FromAddress: "f", ToAddress: "to", Amount: 3, Fee: 1, Memo: "m", TxKey: "k",
		Origin: OutgoingOriginVaultCertified, CertificateHash: "ch",
	})
	require.NoError(t, err)
	require.Equal(t, OutgoingOriginVaultCertified, lastBody["origin"])
	require.Equal(t, "to", lastBody["to_address"])
	require.Equal(t, "ch", lastBody["certificate_hash"])
	require.EqualValues(t, 3, lastBody["amount"])

	obs, err := c.MarkObserved(" v ", strings.ToUpper(hashACov), 5, hashBCov)
	require.NoError(t, err)
	require.EqualValues(t, 5, obs.Height)
	require.Equal(t, hashACov, lastBody["tx_hash"])
	require.Equal(t, "v", lastBody["vault_pub_key"])
}

func TestClientEndpointErrorsCov(t *testing.T) {
	t.Parallel()

	c := newCovClient(t, config.BifrostChainConfiguration{SignerUserName: "u", SignerPassword: "p"}, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "u" || pass != "p" {
			http.Error(w, "unauth", http.StatusUnauthorized)
			return
		}
		http.Error(w, `{"code":"boom"}`, http.StatusInternalServerError)
	})

	_, err := c.ClearSessions()
	require.ErrorContains(t, err, "sessions clear")
	require.True(t, IsHTTPStatus(err, http.StatusInternalServerError), "basic auth was applied")
	_, err = c.Broadcast(BroadcastReq{TxKey: hashACov})
	require.ErrorContains(t, err, "broadcast endpoint")
	_, err = c.Balance("v")
	require.ErrorContains(t, err, "balance endpoint")
	_, err = c.TransfersAt(1)
	require.ErrorContains(t, err, "transfers endpoint")
	_, err = c.Mempool()
	require.ErrorContains(t, err, "mempool transfers endpoint")
	_, err = c.Addresses("v")
	require.ErrorContains(t, err, "addresses endpoint")
	_, err = c.FeeRate()
	require.ErrorContains(t, err, "fee-rate endpoint")
	_, err = c.Vaults()
	require.ErrorContains(t, err, "list vault keys")
	_, err = c.ExportKeys("v")
	require.ErrorContains(t, err, "export keys")
	_, err = c.MarkSpent("v", "t", nil, false, nil)
	require.ErrorContains(t, err, "mark-spent endpoint")
	_, err = c.MarkObserved("v", hashACov, 1, hashBCov)
	require.ErrorContains(t, err, "mark-observed endpoint")
	_, err = c.ScannerState()
	require.ErrorContains(t, err, "scanner state endpoint")
	_, err = c.CanonicalTxLocation(hashACov)
	require.ErrorContains(t, err, "fail to locate canonical monero transaction")
	_, err = c.ChainBlock(1)
	require.ErrorContains(t, err, "fail to call sidecar chain-block endpoint")
	_, err = c.ChainBlock(-1)
	require.EqualError(t, err, "height must be >= 0")
	_, err = c.Info()
	require.ErrorContains(t, err, "compatibility info")
	err = c.ResetScanner("v", 1)
	require.Error(t, err)
	require.True(t, IsHTTPStatus(err, http.StatusInternalServerError))
	_, err = c.RegisterVault(RegisterVaultReq{})
	require.ErrorContains(t, err, "register vault endpoint")
}

func TestMarkObservedValidationCov(t *testing.T) {
	t.Parallel()
	c := &Client{}
	cases := []struct {
		vault, tx, block string
		height           int64
		want             string
	}{
		{" ", hashACov, hashBCov, 1, "mark-observed vault pubkey is empty"},
		{"v", hashACov, hashBCov, 0, "mark-observed height must be positive"},
		{"v", "ab", hashBCov, 1, "mark-observed tx hash must be canonical 32-byte hex"},
		{"v", strings.Repeat("g", 64), hashBCov, 1, "mark-observed tx hash must be canonical 32-byte hex"},
		{"v", hashACov, "ab", 1, "mark-observed block hash must be canonical 32-byte hex"},
		{"v", hashACov, strings.Repeat("g", 64), 1, "mark-observed block hash must be canonical 32-byte hex"},
	}
	for _, tc := range cases {
		_, err := c.MarkObserved(tc.vault, tc.tx, tc.height, tc.block)
		require.EqualError(t, err, tc.want)
	}
}

func TestCanonicalTxLocationCov(t *testing.T) {
	t.Parallel()

	var resp CanonicalTxLocationResp
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/monero/chain/tx/"+hashACov+"/location", r.URL.Path)
		writeJSONCov(w, resp)
	})

	_, err := c.CanonicalTxLocation("abc")
	require.EqualError(t, err, "canonical tx location hash must be 32-byte hex")
	_, err = c.CanonicalTxLocation(strings.Repeat("x", 64))
	require.EqualError(t, err, "canonical tx location hash must be 32-byte hex")

	resp = CanonicalTxLocationResp{TxHash: hashBCov, Height: 1, BlockHash: hashBCov, PreviousHash: hashCCov}
	_, err = c.CanonicalTxLocation(hashACov)
	require.EqualError(t, err, "canonical tx location returned a different transaction")

	resp = CanonicalTxLocationResp{TxHash: hashACov, Height: 0, BlockHash: hashBCov, PreviousHash: hashCCov}
	_, err = c.CanonicalTxLocation(hashACov)
	require.EqualError(t, err, "canonical tx location returned a non-positive height")

	resp = CanonicalTxLocationResp{TxHash: hashACov, Height: 3, BlockHash: "short", PreviousHash: hashCCov}
	_, err = c.CanonicalTxLocation(hashACov)
	require.EqualError(t, err, "canonical tx location returned invalid block hash")

	resp = CanonicalTxLocationResp{TxHash: hashACov, Height: 3, BlockHash: hashBCov, PreviousHash: strings.Repeat("z", 64)}
	_, err = c.CanonicalTxLocation(hashACov)
	require.EqualError(t, err, "canonical tx location returned invalid parent hash")

	resp = CanonicalTxLocationResp{TxHash: strings.ToUpper(hashACov), Height: 3, BlockHash: strings.ToUpper(hashBCov), PreviousHash: " " + hashCCov + " "}
	got, err := c.CanonicalTxLocation(" " + strings.ToUpper(hashACov) + " ")
	require.NoError(t, err)
	require.Equal(t, CanonicalTxLocationResp{TxHash: hashACov, Height: 3, BlockHash: hashBCov, PreviousHash: hashCCov}, got)
}

func TestScannerStateAndCachedCov(t *testing.T) {
	t.Parallel()

	var body string
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, scannerStatePath, r.URL.Path)
		_, _ = io.WriteString(w, body)
	})

	_, ok := c.CachedScannerState()
	require.False(t, ok)

	body = `{"reset_epoch":0,"next_height":null,"strict_after_reset":false}`
	_, err := c.ScannerState()
	require.ErrorContains(t, err, "invalid continuity")
	_, ok = c.CachedScannerState()
	require.False(t, ok, "invalid state never seeds the cache")

	body = fmt.Sprintf(`{"reset_epoch":2,"next_height":11,"last_height":10,"last_block_hash":%q,"last_parent_hash":%q,"strict_after_reset":true}`,
		strings.ToUpper(hashACov), hashBCov)
	st, err := c.ScannerState()
	require.NoError(t, err)
	require.EqualValues(t, 11, *st.NextHeight)

	cached, ok := c.CachedScannerState()
	require.True(t, ok)
	require.EqualValues(t, 2, cached.ResetEpoch)
	require.EqualValues(t, 11, *cached.NextHeight)
	require.EqualValues(t, 10, *cached.LastHeight)
	require.Equal(t, hashACov, cached.LastBlockHash, "hashes are normalized to lowercase")
	require.Equal(t, hashBCov, cached.LastParentHash)
	require.True(t, cached.StrictAfterReset)

	// Without last height details the cached state omits LastHeight.
	c2 := &Client{}
	c2.mergeScannerContinuity(scannerContinuityCache{resetEpoch: 1, nextHeight: 4})
	cached, ok = c2.CachedScannerState()
	require.True(t, ok)
	require.Nil(t, cached.LastHeight)
	require.EqualValues(t, 4, *cached.NextHeight)
}

func u64Cov(v uint64) *uint64 { return &v }

func TestScannerContinuityFromStateErrorsCov(t *testing.T) {
	t.Parallel()

	cases := []struct {
		state ScannerState
		want  string
	}{
		{ScannerState{}, "scanner state next_height is nil"},
		{ScannerState{NextHeight: u64Cov(math.MaxInt64 + 1)}, "exceeds the int64 scanner range"},
		{ScannerState{NextHeight: u64Cov(1), ResetEpoch: 1}, "reset epoch and strict mode are inconsistent"},
		{ScannerState{NextHeight: u64Cov(1), LastBlockHash: hashACov}, "hashes without last_height"},
		{ScannerState{NextHeight: u64Cov(1), LastHeight: u64Cov(math.MaxInt64)}, "cannot advance"},
		{ScannerState{NextHeight: u64Cov(5), LastHeight: u64Cov(3)}, "not contiguous"},
		{ScannerState{NextHeight: u64Cov(4), LastHeight: u64Cov(3), LastBlockHash: "x", LastParentHash: hashACov}, "last_block_hash must be"},
		{ScannerState{NextHeight: u64Cov(4), LastHeight: u64Cov(3), LastBlockHash: hashACov, LastParentHash: "x"}, "last_parent_hash must be"},
	}
	for _, tc := range cases {
		_, err := scannerContinuityFromState(tc.state)
		require.ErrorContains(t, err, tc.want)
	}

	cont, err := scannerContinuityFromState(ScannerState{NextHeight: u64Cov(7)})
	require.NoError(t, err)
	require.True(t, cont.hasStateDetails)
	require.False(t, cont.hasLastHeight)
	require.EqualValues(t, 7, cont.nextHeight)

	_, err = scannerContinuityFromRegistration(RegisterVaultResp{})
	require.EqualError(t, err, "expected_next_height is nil")
	_, err = scannerContinuityFromRegistration(RegisterVaultResp{ExpectedNextHeight: u64Cov(math.MaxInt64 + 1)})
	require.ErrorContains(t, err, "exceeds")

	_, ok := checkedNextResetEpoch(math.MaxUint64)
	require.False(t, ok)
	next, ok := checkedNextResetEpoch(3)
	require.True(t, ok)
	require.EqualValues(t, 4, next)

	err = validateScannerRewindResponse(ScannerRewindReq{ExpectedResetEpoch: math.MaxUint64}, ScannerRewindResp{})
	require.EqualError(t, err, "expected reset epoch cannot advance")

	require.False(t, isAmbiguousScannerRewindError(nil))
	require.False(t, isAmbiguousScannerRewindError(&HTTPError{Status: 500}))
	require.True(t, isAmbiguousScannerRewindError(io.EOF))
	require.False(t, isAmbiguousScannerRewindError(errors.New("x")))
}

func TestResetScannerCov(t *testing.T) {
	t.Parallel()

	var resp ScannerResetResp
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/monero/scanner/reset", r.URL.Path)
		writeJSONCov(w, resp)
	})

	require.EqualError(t, c.ResetScanner("v", -1), "scanner reset birthday height must be non-negative")

	for _, bad := range []ScannerResetResp{
		{Reset: false, ResetEpoch: 1, ExpectedNextHeight: 9},
		{Reset: true, ResetEpoch: 0, ExpectedNextHeight: 9},
		{Reset: true, ResetEpoch: 1, ExpectedNextHeight: 8},
	} {
		resp = bad
		require.EqualError(t, c.ResetScanner("v", 9), "scanner reset returned an invalid continuity cursor")
	}
	_, ok := c.CachedScannerState()
	require.False(t, ok)

	resp = ScannerResetResp{Reset: true, ResetEpoch: 3, ExpectedNextHeight: 9}
	require.NoError(t, c.ResetScanner("v", 9))
	st, ok := c.CachedScannerState()
	require.True(t, ok)
	require.EqualValues(t, 3, st.ResetEpoch)
	require.EqualValues(t, 9, *st.NextHeight)
	require.True(t, st.StrictAfterReset)
}

func TestRegisterVaultCov(t *testing.T) {
	t.Parallel()

	var body string
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})

	_, err := c.RegisterVault(RegisterVaultReq{VaultCreationHeight: -1})
	require.EqualError(t, err, "vault creation height must be non-negative")

	body = `{"registered":true,"reset_epoch":0}`
	_, err = c.RegisterVault(RegisterVaultReq{})
	require.ErrorContains(t, err, "invalid scanner continuity")

	body = `{"registered":true,"reset_epoch":1,"expected_next_height":42}`
	out, err := c.RegisterVault(RegisterVaultReq{})
	require.NoError(t, err)
	require.True(t, out.Registered)
	st, ok := c.CachedScannerState()
	require.True(t, ok)
	require.EqualValues(t, 42, *st.NextHeight)
	require.True(t, st.StrictAfterReset)
}

func TestChainBlockSidecarCov(t *testing.T) {
	t.Parallel()

	var status int
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/monero/chain/block/8", r.URL.Path)
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		writeJSONCov(w, ChainBlockResp{Height: 8, Hash: hashACov, PreviousHash: hashBCov, Reward: 1, TxHashes: []string{"t"}})
	})

	b, err := c.ChainBlock(8)
	require.NoError(t, err)
	require.Equal(t, ChainBlock{Height: 8, Hash: hashACov, PreviousHash: hashBCov, Reward: 1, TxHashes: []string{"t"}}, b)

	status = http.StatusConflict
	_, err = c.ChainBlock(8)
	require.ErrorIs(t, err, ErrUnavailableBlock)
}

func TestCallHelpersEnvelopeCov(t *testing.T) {
	t.Parallel()

	var body string
	var status int
	c := newCovClient(t, config.BifrostChainConfiguration{}, func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = io.WriteString(w, body)
	})

	var out FeeRate
	// GET: application error via "error"
	body = `{"error":"  nope "}`
	err := c.callGet("/x", &out)
	require.ErrorContains(t, err, "/x reported an application error (message_length=4 ")
	// GET: application error via "message"
	body = `{"message":"msg-only"}`
	err = c.callGet("/x", &out)
	require.ErrorContains(t, err, "/x reported an application error (message_length=8 ")
	// GET: invalid json
	body = `not json`
	err = c.callGet("/x", &out)
	require.ErrorContains(t, err, "fail to unmarshal sidecar response")
	// GET: empty body
	body = ``
	err = c.callGet("/x", &out)
	require.ErrorContains(t, err, "returned empty response body")
	// GET: nil out ignores body
	require.NoError(t, c.callGet("/x", nil))
	// GET: result envelope
	body = `{"result":{"fee_rate_per_weight":9}}`
	require.NoError(t, c.callGet("/x", &out))
	require.EqualValues(t, 9, out.FeeRatePerWeight)

	// POST equivalents.
	body = `{"message":"post-msg"}`
	err = c.call(http.MethodPost, "/y", struct{}{}, &out)
	require.ErrorContains(t, err, "/y reported an application error (message_length=8 ")
	body = `{"error":"post-err"}`
	err = c.call(http.MethodPost, "/y", struct{}{}, &out)
	require.ErrorContains(t, err, "/y reported an application error (message_length=8 ")
	body = `garbage`
	err = c.call(http.MethodPost, "/y", struct{}{}, &out)
	require.ErrorContains(t, err, "fail to unmarshal sidecar response")
	body = ``
	err = c.call(http.MethodPost, "/y", struct{}{}, &out)
	require.ErrorContains(t, err, "returned empty response body")
	require.NoError(t, c.call(http.MethodPost, "/y", struct{}{}, nil))
	body = `{"result":{"fee_rate_per_weight":11}}`
	require.NoError(t, c.call(http.MethodPost, "/y", struct{}{}, &out))
	require.EqualValues(t, 11, out.FeeRatePerWeight)

	// Marshal failure.
	err = c.call(http.MethodPost, "/y", make(chan int), &out)
	require.ErrorContains(t, err, "fail to marshal sidecar request payload")

	// Bad method / bad request construction.
	err = c.callJSONContext(context.Background(), "BAD METHOD", "/y", nil, &out)
	require.ErrorContains(t, err, "fail to create sidecar request")
	err = c.callGetContext(context.Background(), "/%zz", &out)
	require.ErrorContains(t, err, "fail to create sidecar request")

	// Transport failure.
	dead := &Client{endpoint: "http://127.0.0.1:1", client: &http.Client{Timeout: time.Second}}
	err = dead.callGet("/x", &out)
	require.ErrorContains(t, err, "fail to call sidecar endpoint /x")
	err = dead.call(http.MethodPost, "/x", struct{}{}, &out)
	require.ErrorContains(t, err, "fail to call sidecar endpoint /x")

	// applySignerAuth tolerates nil receivers/requests.
	(*Client)(nil).applySignerAuth(nil)
	c.applySignerAuth(nil)
}

func TestErrorTypesNilCov(t *testing.T) {
	t.Parallel()

	var rewind *ScannerRewindRequiredError
	require.Equal(t, "sidecar scanner rewind required", rewind.Error())
	var recovery *ScannerRecoveryRequiredError
	require.Equal(t, "sidecar scanner recovery required", recovery.Error())
	recovery = &ScannerRecoveryRequiredError{RequestedHeight: 1, NextHeight: 5, ResetEpoch: 2}
	require.Contains(t, recovery.Error(), "requested 1, next height 5, reset epoch 2")
}

func TestTypesUnmarshalErrorsCov(t *testing.T) {
	t.Parallel()

	var reg RegisterVaultResp
	require.Error(t, json.Unmarshal([]byte(`{"registered":"x"}`), &reg))
	require.EqualError(t, json.Unmarshal([]byte(`{"registered":true}`), &reg), "register vault response omitted reset_epoch")

	var st ScannerState
	require.Error(t, json.Unmarshal([]byte(`{"reset_epoch":"x"}`), &st))
	require.EqualError(t, json.Unmarshal([]byte(`{"strict_after_reset":false}`), &st), "scanner state omitted reset_epoch")
	require.EqualError(t, json.Unmarshal([]byte(`{"reset_epoch":0}`), &st), "scanner state omitted strict_after_reset")

	var rw ScannerRewindResp
	require.Error(t, json.Unmarshal([]byte(`{"rewound":1}`), &rw))
	require.EqualError(t, json.Unmarshal([]byte(`{"rewound":true}`), &rw), "scanner rewind response omitted a required field")
	require.NoError(t, json.Unmarshal([]byte(`{"rewound":true,"previous_next_height":9,"next_height":5,"reset_epoch":2}`), &rw))
	require.Equal(t, ScannerRewindResp{Rewound: true, PreviousNextHeight: 9, NextHeight: 5, ResetEpoch: 2}, rw)
}
