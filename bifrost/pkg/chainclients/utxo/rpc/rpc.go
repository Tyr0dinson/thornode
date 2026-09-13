package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc/zec"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// op-geth rpc uses 10s timeouts
const ZecClientTimeout = time.Second * 10

////////////////////////////////////////////////////////////////////////////////////////
// Interfaces
////////////////////////////////////////////////////////////////////////////////////////

type SerializableTx interface {
	SerializeSize() int
	Serialize(io.Writer) error
}

type rpcClient interface {
	Call(result any, method string, args ...any) error
	BatchCall(batch []rpc.BatchElem) error
	CallContext(ctx context.Context, result any, method string, args ...any) error
	BatchCallContext(ctx context.Context, batch []rpc.BatchElem) error
}

////////////////////////////////////////////////////////////////////////////////////////
// Client
////////////////////////////////////////////////////////////////////////////////////////

// Client represents a client connection to a UTXO daemon. Internally this uses the
// Ethereum JSON-RPC 2.0 implementation, which allows batching and connection reuse.
type Client struct {
	client           rpcClient
	log              zerolog.Logger
	chain            common.Chain
	maxRetries       int
	timeout          time.Duration
	fallbacks        []UTXOReader
	rateLimitBackoff time.Duration

	// manager (optional) orders reads by endpoint health, skips endpoints the
	// health monitor marked unhealthy or rate limited, and receives inline results.
	// names[0] is the primary; names[i] is fallbacks[i-1]. secrets is parallel.
	manager *endpoints.Manager
	names   []string
	index   map[string]int
	secrets [][]string
}

// NewClient returns a client connection to a UTXO daemon.
func NewClient(host, user, password string, maxRetries int, timeout time.Duration, chain common.Chain, log zerolog.Logger) (
	*Client, error,
) {
	return NewClientWithAuth(host, user, password, "", maxRetries, timeout, chain, log)
}

// NewClientWithAuth creates a UTXO rpc client. When bearer is non-empty it authenticates
// with an Authorization: Bearer header; otherwise it falls back to HTTP Basic auth built
// from user/password. Only the bearer branch mirrors the EVM dialer; the Basic branch
// retains existing UTXO behavior unchanged (including the empty-credential case) for
// backward compatibility.
func NewClientWithAuth(host, user, password, bearer string, maxRetries int, timeout time.Duration, chain common.Chain, log zerolog.Logger) (
	*Client, error,
) {
	authFn := func(h http.Header) error {
		if bearer != "" {
			h.Set("Authorization", fmt.Sprintf("Bearer %s", bearer))
			return nil
		}
		auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
		h.Set("Authorization", fmt.Sprintf("Basic %s", auth))
		return nil
	}

	// default to http if no scheme is specified
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}

	client := Client{
		log:        log,
		chain:      chain,
		maxRetries: maxRetries,
		timeout:    timeout,
	}

	var err error

	switch chain {
	case common.ZECChain:
		header := http.Header{}
		err = authFn(header)
		if err != nil {
			return nil, err
		}
		client.client = zec.NewClient(host, ZecClientTimeout, header)
	default:
		client.client, err = rpc.DialOptions(context.Background(), host, rpc.WithHTTPAuth(authFn))
	}

	if err != nil {
		return nil, err
	}

	return &client, nil
}

// NewClientWithFallbacks wraps NewClientWithAuth and attaches read-only fallback providers.
// Fallbacks are tried in slice order after the primary exhausts its retries.
// rateLimitBackoff is the sleep duration when the primary returns HTTP 429 (no
// failover occurs on rate-limit errors).
func NewClientWithFallbacks(
	host, user, password, bearer string,
	maxRetries int,
	timeout time.Duration,
	chain common.Chain,
	log zerolog.Logger,
	fallbacks []UTXOReader,
	rateLimitBackoff time.Duration,
) (*Client, error) {
	cl, err := NewClientWithAuth(host, user, password, bearer, maxRetries, timeout, chain, log)
	if err != nil {
		return nil, err
	}
	cl.fallbacks = fallbacks
	cl.rateLimitBackoff = rateLimitBackoff
	return cl, nil
}

// GetBlockCount returns the number of blocks in the longest block chain.
// WithEndpointManager attaches the chain's endpoint manager. eps must list the
// primary first, then one endpoint per fallback in order; the manager is expected
// to have been built from the same list.
func (c *Client) WithEndpointManager(m *endpoints.Manager, eps []config.ChainEndpointConfig) *Client {
	if len(eps) != len(c.fallbacks)+1 {
		c.log.Error().Int("endpoints", len(eps)).Int("fallbacks", len(c.fallbacks)).
			Msg("endpoint manager endpoints do not match fallbacks; manager disabled")
		return c
	}
	c.manager = m
	c.names = make([]string, len(eps))
	c.index = make(map[string]int, len(eps))
	c.secrets = make([][]string, len(eps))
	for i, e := range eps {
		c.names[i] = e.Name
		c.index[e.Name] = i
		if u, err := url.Parse(endpoints.NormalizeHost(e.RPCHost)); err == nil {
			c.secrets[i] = endpoints.SecretsOf(u, e.AuthorizationBearer, e.Password)
		}
	}
	return c
}

// candidates returns the read order as indexes: 0 is the primary, i+1 is
// fallbacks[i]. Without a manager: primary then every fallback in config order.
// With one: the manager's ReadCandidates order (the active endpoint first, healthy
// ones by priority, every non-disabled one when everything is down), so a recovered
// primary is not read before the endpoint the manager selected.
func (c *Client) candidates() []int {
	order := make([]int, 0, len(c.fallbacks)+1)
	if c.manager == nil {
		for i := 0; i <= len(c.fallbacks); i++ {
			order = append(order, i)
		}
		return order
	}
	for _, e := range c.manager.ReadCandidates() {
		if i, ok := c.index[e.Name]; ok {
			order = append(order, i)
		}
	}
	return order
}

// callPrimary performs one JSON-RPC call against the primary without retry.
func (c *Client) callPrimary(result any, method string, args ...interface{}) error {
	if c.timeout <= 0 {
		return c.client.Call(result, method, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.client.CallContext(ctx, result, method, args...)
}

// isAppError reports whether err is a JSON-RPC application answer (a
// btcjson.RPCError such as "No such mempool transaction"): the endpoint served the
// request, so it is not an endpoint failure.
func isAppError(err error) bool {
	var rpcErr *btcjson.RPCError
	return errors.As(extractBTCError(err), &rpcErr)
}

func (c *Client) recordFailure(i int, err error) {
	if c.manager == nil || err == nil {
		return
	}
	switch {
	case isRateLimitError(err):
		c.manager.RecordRateLimited(c.names[i])
	case isAppError(err):
		c.manager.RecordSuccess(c.names[i])
	default:
		c.manager.RecordError(c.names[i], endpoints.ErrorKindHTTP)
	}
}

func (c *Client) recordSuccess(i int) {
	if c.manager != nil {
		c.manager.RecordSuccess(c.names[i])
	}
}

// readWithFallback runs a read against the endpoints in candidate order: primary
// (with the usual retry loop) at its own position, fallbacks through fn. The first
// nil result wins. Every attempt is reported to the manager; application-level RPC
// errors count as served, not as failures.
//
// Without a manager (single-endpoint legacy path), an HTTP 429 on the primary never
// fails over: the caller sleeps rateLimitBackoff and returns the error, so a
// rate-limited daemon is not hammered. With a manager, a 429 puts that endpoint in
// cooldown and the read is served by the next candidate; if nothing served the read
// and the primary was rate limited, the caller still sleeps.
func (c *Client) readWithFallback(primary func() error, fn func(UTXOReader) error) error {
	var (
		firstErr    error
		firstIdx    int
		rateLimited bool
	)
	for _, i := range c.candidates() {
		var err error
		if i == 0 {
			err = c.retry(primary)
		} else {
			err = fn(c.fallbacks[i-1])
		}
		if err == nil {
			c.recordSuccess(i)
			return nil
		}
		c.recordFailure(i, err)
		if firstErr == nil {
			firstErr, firstIdx = err, i
		}
		if i == 0 && isRateLimitError(err) {
			rateLimited = true
			if c.manager == nil {
				break
			}
		}
	}
	if rateLimited {
		c.sleepRateLimit()
	}
	if firstErr == nil {
		return fmt.Errorf("no eligible %s endpoint served the read", c.chain)
	}
	return c.redact(firstIdx, firstErr)
}

func (c *Client) sleepRateLimit() {
	if c.rateLimitBackoff > 0 {
		time.Sleep(c.rateLimitBackoff)
	}
}

// redact scrubs endpoint i's URL and credentials from err.
func (c *Client) redact(i int, err error) error {
	if c.manager == nil || err == nil || i >= len(c.secrets) {
		return err
	}
	return endpoints.Redact(err, c.secrets[i])
}

// GetRawTransactionVerbosePrimary is the signer-cache verification read: it asks
// only the primary daemon (the one every broadcast went to) and never falls back,
// because a fallback that has not seen the broadcast would answer "not found" and
// trigger a wrongful re-sign. Any error must be treated as ambiguous by the caller.
func (c *Client) GetRawTransactionVerbosePrimary(txid string) (*btcjson.TxRawResult, error) {
	var tx btcjson.TxRawResult
	err := c.callRaw(&tx, "getrawtransaction", txid, true)
	return &tx, extractBTCError(c.redact(0, err))
}

// GetMempoolEntryPrimary is the primary-only counterpart of GetMempoolEntry, for
// signer-side mempool checks that must observe the broadcasting daemon.
func (c *Client) GetMempoolEntryPrimary(txid string) (*btcjson.GetMempoolEntryResult, error) {
	var entry btcjson.GetMempoolEntryResult
	err := c.callRaw(&entry, "getmempoolentry", txid)
	return &entry, extractBTCError(c.redact(0, err))
}

func (c *Client) GetBlockCount() (int64, error) {
	var count int64
	err := c.readWithFallback(func() error { return c.callPrimary(&count, "getblockcount") }, func(fb UTXOReader) error {
		var ferr error
		count, ferr = fb.GetBlockCount()
		return ferr
	})
	return count, extractBTCError(err)
}

// SendRawTransaction serializes and sends the transaction. The maxFeeParam differs in
// type between chains - ensure the correct variant is used.
func (c *Client) SendRawTransaction(tx SerializableTx, maxFeeParam any) (string, error) {
	txHex := ""
	if tx != nil {
		buf := bytes.NewBuffer(make([]byte, 0, tx.SerializeSize()))
		if err := tx.Serialize(buf); err != nil {
			return "", err
		}
		txHex = hex.EncodeToString(buf.Bytes())
	}

	args := []interface{}{txHex, maxFeeParam}

	var txid string
	err := c.Call(&txid, "sendrawtransaction", args...)
	return txid, extractBTCError(err)
}

// GetBlockHash returns the hash of the block in best-block-chain at the given height.
func (c *Client) GetBlockHash(height int64) (string, error) {
	var hash string
	err := c.readWithFallback(func() error { return c.callPrimary(&hash, "getblockhash", height) }, func(fb UTXOReader) error {
		var ferr error
		hash, ferr = fb.GetBlockHash(height)
		return ferr
	})
	return hash, extractBTCError(err)
}

// GetBlockVerboseTxs returns information about the block with verbosity 2.
func (c *Client) GetBlockVerboseTxs(hash string) (*btcjson.GetBlockVerboseTxResult, error) {
	var block btcjson.GetBlockVerboseTxResult
	err := c.readWithFallback(func() error { return c.callPrimary(&block, "getblock", hash, 2) }, func(fb UTXOReader) error {
		result, ferr := fb.GetBlockVerboseTxs(hash)
		if ferr == nil {
			block = *result
		}
		return ferr
	})
	return &block, extractBTCError(err)
}

// GetBlockVerbose returns information about the block with verbosity 1.
func (c *Client) GetBlockVerbose(hash string) (*btcjson.GetBlockVerboseResult, error) {
	var block btcjson.GetBlockVerboseResult
	err := c.readWithFallback(func() error { return c.callPrimary(&block, "getblock", hash, 1) }, func(fb UTXOReader) error {
		result, ferr := fb.GetBlockVerbose(hash)
		if ferr == nil {
			block = *result
		}
		return ferr
	})
	return &block, extractBTCError(err)
}

// GetBlockStats returns statistics about the block at the given hash.
func (c *Client) GetBlockStats(hash string) (*btcjson.GetBlockStatsResult, error) {
	var stats btcjson.GetBlockStatsResult
	err := c.readWithFallback(func() error { return c.callPrimary(&stats, "getblockstats", hash) }, func(fb UTXOReader) error {
		result, ferr := fb.GetBlockStats(hash)
		if ferr == nil {
			stats = *result
		}
		return ferr
	})
	return &stats, extractBTCError(err)
}

// GetMempoolEntry returns mempool data for the given transaction.
func (c *Client) GetMempoolEntry(txid string) (*btcjson.GetMempoolEntryResult, error) {
	var entry btcjson.GetMempoolEntryResult
	err := c.readWithFallback(func() error { return c.callPrimary(&entry, "getmempoolentry", txid) }, func(fb UTXOReader) error {
		result, ferr := fb.GetMempoolEntry(txid)
		if ferr == nil {
			entry = *result
		}
		return ferr
	})
	return &entry, extractBTCError(err)
}

// BatchGetMempoolEntry returns mempool data for the given transactions.
func (c *Client) BatchGetMempoolEntry(txids []string) ([]*btcjson.GetMempoolEntryResult, []error, error) {
	batch := []rpc.BatchElem{}
	for _, txid := range txids {
		batch = append(batch, rpc.BatchElem{
			Method: "getmempoolentry",
			Args:   []interface{}{txid},
			Result: &btcjson.GetMempoolEntryResult{},
		})
	}

	var (
		fbResults []*btcjson.GetMempoolEntryResult
		fbErrs    []error
		served    bool
	)
	err := c.readWithFallback(func() error { return c.batchCall(batch) }, func(fb UTXOReader) error {
		var e error
		fbResults, fbErrs, e = fb.BatchGetMempoolEntry(txids)
		served = e == nil
		return e
	})
	if err != nil {
		return nil, nil, extractBTCError(err)
	}
	if served {
		return fbResults, fbErrs, nil
	}

	errs := make([]error, len(txids))
	results := make([]*btcjson.GetMempoolEntryResult, len(txids))
	var ok bool
	for i, b := range batch {
		results[i], ok = b.Result.(*btcjson.GetMempoolEntryResult)
		if !ok {
			return nil, nil, fmt.Errorf("unexpected type returned from batch call: %T", b.Result)
		}
		errs[i] = extractBTCError(b.Error)
	}

	return results, errs, nil
}

// GetRawMempool returns all transaction ids in the mempool.
func (c *Client) GetRawMempool() ([]string, error) {
	var txids []string
	err := c.readWithFallback(func() error { return c.callPrimary(&txids, "getrawmempool") }, func(fb UTXOReader) error {
		var ferr error
		txids, ferr = fb.GetRawMempool()
		return ferr
	})
	return txids, extractBTCError(err)
}

// GetRawTransactionVerbose returns a raw transaction for the transaction id.
func (c *Client) GetRawTransactionVerbose(txid string) (*btcjson.TxRawResult, error) {
	var tx btcjson.TxRawResult
	err := c.readWithFallback(func() error { return c.callPrimary(&tx, "getrawtransaction", txid, true) }, func(fb UTXOReader) error {
		result, ferr := fb.GetRawTransactionVerbose(txid)
		if ferr == nil {
			tx = *result
		}
		return ferr
	})
	return &tx, extractBTCError(err)
}

// GetRawTransaction returns a raw transaction string for the transaction id.
func (c *Client) GetRawTransaction(txid string) (string, error) {
	var tx string
	err := c.readWithFallback(func() error { return c.callPrimary(&tx, "getrawtransaction", txid, false) }, func(fb UTXOReader) error {
		var ferr error
		tx, ferr = fb.GetRawTransaction(txid)
		return ferr
	})
	return tx, extractBTCError(err)
}

// BatchGetRawTransactionVerbose returns a raw transaction for given transaction ids.
func (c *Client) BatchGetRawTransactionVerbose(txids []string) ([]*btcjson.TxRawResult, []error, error) {
	batch := make([]rpc.BatchElem, 0, len(txids))
	for _, txid := range txids {
		batch = append(batch, rpc.BatchElem{
			Method: "getrawtransaction",
			Args:   []interface{}{txid, true},
			Result: &btcjson.TxRawResult{},
		})
	}

	var (
		fbResults []*btcjson.TxRawResult
		fbErrs    []error
		served    bool
	)
	err := c.readWithFallback(func() error { return c.batchCall(batch) }, func(fb UTXOReader) error {
		var e error
		fbResults, fbErrs, e = fb.BatchGetRawTransactionVerbose(txids)
		served = e == nil
		return e
	})
	if err != nil {
		return nil, nil, extractBTCError(err)
	}
	if served {
		return fbResults, fbErrs, nil
	}

	errs := make([]error, len(txids))
	results := make([]*btcjson.TxRawResult, len(txids))
	var ok bool
	for i, b := range batch {
		results[i], ok = b.Result.(*btcjson.TxRawResult)
		if !ok {
			return nil, nil, fmt.Errorf("unexpected type returned from batch call: %T", b.Result)
		}
		errs[i] = extractBTCError(b.Error)
	}

	return results, errs, nil
}

// ImportAddress imports the address with no rescan.
func (c *Client) ImportAddress(address string) error {
	err := c.Call(nil, "importaddress", address, "", false)
	return extractBTCError(err)
}

// ImportAddressRescan imports the address with rescan.
func (c *Client) ImportAddressRescan(address string) error {
	err := c.Call(nil, "importaddress", address, "", true)
	return extractBTCError(err)
}

// IsAddressImported reports whether the node already tracks the address.
func (c *Client) IsAddressImported(address string) (bool, error) {
	info, err := c.getAddressTrackingInfo("getaddressinfo", address)
	if err != nil && isMethodNotFoundError(err) {
		info, err = c.getAddressTrackingInfo("validateaddress", address)
	}
	if err != nil {
		return false, extractBTCError(err)
	}

	return info.IsMine || info.IsWatchOnly, nil
}

func (c *Client) getAddressTrackingInfo(method, address string) (struct {
	IsMine      bool `json:"ismine"`
	IsWatchOnly bool `json:"iswatchonly"`
}, error,
) {
	var info struct {
		IsMine      bool `json:"ismine"`
		IsWatchOnly bool `json:"iswatchonly"`
	}

	err := c.Call(&info, method, address)
	if err != nil {
		return info, err
	}

	return info, nil
}

func isMethodNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	var rpcErr *btcjson.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == btcjson.RPCErrorCode(-32601) {
		return true
	}

	return strings.Contains(strings.ToLower(err.Error()), "method not found")
}

// CreateWallet creates a new wallet.
func (c *Client) CreateWallet(name string) error {
	err := c.Call(nil, "createwallet", name, false, false, "", false, false)
	err = extractBTCError(err)

	// ignore code -4 (wallet already exists)
	if rpcErr, ok := err.(*btcjson.RPCError); ok && rpcErr.Code == btcjson.ErrRPCWallet {
		return nil
	}

	return err
}

// ListUnspent returns all unspent outputs with between min and max confirmations.
func (c *Client) ListUnspent(address string) ([]btcjson.ListUnspentResult, error) {
	var unspent []btcjson.ListUnspentResult
	var err error

	switch c.chain {
	case common.ZECChain:
		var height int64
		height, err = c.GetBlockCount()
		if err != nil {
			return nil, err
		}

		var utxos []struct {
			Txid     string `json:"txid"`
			Address  string `json:"address"`
			Index    uint32 `json:"outputIndex"`
			Script   string `json:"script"`
			Satoshis uint64 `json:"satoshis"`
			Height   int64  `json:"height"`
		}

		err = c.Call(&utxos, "getaddressutxos", []any{address}...)

		for _, utxo := range utxos {
			unspent = append(unspent, btcjson.ListUnspentResult{
				TxID:          utxo.Txid,
				Vout:          utxo.Index,
				Address:       utxo.Address,
				ScriptPubKey:  utxo.Script,
				Amount:        float64(utxo.Satoshis) / 1e8,
				Confirmations: height - utxo.Height,
				Spendable:     true,
			})
		}
	default:
		const minConfirm = 0
		const maxConfirm = 9999999
		err = c.Call(&unspent, "listunspent", minConfirm, maxConfirm, []string{address})
	}

	return unspent, extractBTCError(err)
}

// flexibleWarnings decodes bitcoind's getnetworkinfo "warnings" field, which is
// a plain string on Bitcoin Core <=27 and an array of strings on Core 28/29.
type flexibleWarnings []string

func (w *flexibleWarnings) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	switch b[0] {
	case '[':
		return json.Unmarshal(b, (*[]string)(w))
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s != "" {
			*w = []string{s}
		}
		return nil
	default:
		return fmt.Errorf("unexpected getnetworkinfo warnings shape: %s", b)
	}
}

func (c *Client) GetNetworkInfo() (*btcjson.GetNetworkInfoResult, error) {
	// Decode into a shadow of GetNetworkInfoResult whose warnings field accepts
	// both the pre-28 string and the Core 28/29 array shapes. The shallower
	// (outer) warnings field wins over the embedded string one during decode, so
	// the array shape no longer aborts the whole unmarshal (dropping RelayFee).
	var info struct {
		btcjson.GetNetworkInfoResult
		Warnings flexibleWarnings `json:"warnings"`
	}
	var result btcjson.GetNetworkInfoResult
	err := c.readWithFallback(func() error {
		perr := c.callPrimary(&info, "getnetworkinfo")
		if perr == nil {
			result = info.GetNetworkInfoResult
			result.Warnings = strings.Join(info.Warnings, "; ")
		}
		return perr
	}, func(fb UTXOReader) error {
		fbResult, ferr := fb.GetNetworkInfo()
		if ferr == nil {
			result = *fbResult
		}
		return ferr
	})
	return &result, extractBTCError(err)
}

func (c *Client) Call(result any, method string, args ...interface{}) error {
	err := c.callRaw(result, method, args...)
	return extractBTCError(err)
}

// CallWithFallback issues a generic JSON-RPC read against the primary and fails over to
// each read fallback when the primary errors (and the error is not a rate limit). Use it
// for reads that have no typed helper — e.g. BCH getblockstats, whose float avgfeerate
// needs a chain-specific result struct that the typed GetBlockStats cannot represent.
// Writes must keep using Call, which is primary-only.
func (c *Client) CallWithFallback(result any, method string, args ...interface{}) error {
	err := c.readWithFallback(func() error { return c.callPrimary(result, method, args...) }, func(fb UTXOReader) error {
		return fb.Call(result, method, args...)
	})
	return extractBTCError(err)
}

// callRaw performs a JSON-RPC call with retry but without extractBTCError, so the raw
// HTTP status (e.g. "429 Too Many Requests: ...") is preserved for isRateLimitError to inspect.
func (c *Client) callRaw(result any, method string, args ...interface{}) error {
	fn := func() error {
		if c.timeout <= 0 {
			return c.client.Call(result, method, args...)
		}
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		return c.client.CallContext(ctx, result, method, args...)
	}
	return c.retry(fn)
}

func (c *Client) BatchCall(batch []rpc.BatchElem) error {
	err := c.retry(func() error {
		return c.batchCall(batch)
	})
	return extractBTCError(err)
}

func (c *Client) batchCall(batch []rpc.BatchElem) error {
	if c.timeout <= 0 {
		return c.client.BatchCall(batch)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.client.BatchCallContext(ctx, batch)
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// isRateLimitError reports whether err represents an HTTP 429 response.
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "too many requests") {
		return true
	}
	return strings.Contains(s, "status 429") || strings.Contains(s, "status: 429") || strings.Contains(s, " 429 ") || strings.Contains(s, "http 429")
}

// Ethereum RPC returns an error with the response appended to the HTTP status like:
// 404 Not Found: {"error":{"code":-32601,"message":"Method not found"},"id":1}
//
// This makes best effort to extract and return the error as a btcjson.RPCError.
func extractBTCError(err error) error {
	if err == nil {
		return nil
	}

	// Bitcoin Core >=28 answers JSON-RPC 2.0 requests with HTTP 200 and a
	// spec-compliant error object, which the go-ethereum rpc client surfaces as
	// an rpc.Error (structured code + message) rather than the pre-28 HTTP-500
	// "<status>: <json body>" string the split below expects. Convert it
	// directly so the RPC error code (e.g. -4 wallet already exists) survives.
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		return btcjson.NewRPCError(btcjson.RPCErrorCode(rpcErr.ErrorCode()), rpcErr.Error())
	}

	// split the error into the HTTP status and the JSON response
	parts := strings.SplitN(err.Error(), ": ", 2)
	if len(parts) != 2 {
		return err
	}

	// parse the JSON response
	var response struct {
		Error struct {
			Code    btcjson.RPCErrorCode `json:"code"`
			Message string               `json:"message"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal([]byte(parts[1]), &response); jsonErr != nil {
		return err
	}

	// return the error message
	return btcjson.NewRPCError(response.Error.Code, response.Error.Message)
}

// retry will wrap the function call with a retry loop on specific errors - namely 500
// response when the daemon is overloaded the the work queue is exceeded. We use a
// simple static backoff of 1 second for now.
func (c *Client) retry(fn func() error) error {
	var err error
	backoff := time.Second
	for i := 0; i <= c.maxRetries; i++ {
		err = fn()
		if err == nil {
			break
		}

		errStr := strings.ToLower(err.Error())

		// check in order of most to least likely based on log sampling
		retry := strings.Contains(errStr, "connect: connection refused") ||
			strings.Contains(errStr, "work queue depth exceeded") ||
			(strings.HasPrefix(errStr, "post") && strings.HasSuffix(errStr, "eof")) ||
			strings.Contains(errStr, "loading block index") || // daemon startup
			strings.Contains(errStr, "verifying wallet") || // daemon startup
			strings.Contains(errStr, "verifying wallet") || // daemon startup
			strings.Contains(errStr, "currently rescanning") || // rescanning wallet
			strings.HasPrefix(errStr, "503 service unavailable")

		// break if not a retryable error
		if !retry {
			break
		}

		// break if this was the last retry
		if i == c.maxRetries {
			break
		}

		// backoff and retry
		c.log.Err(err).
			Str("backoff", backoff.String()).
			Msgf("retrying %d/%d after backoff", i+1, c.maxRetries)
		time.Sleep(backoff)

		// exponential backoff, max 10 seconds
		backoff *= 2
		if backoff > time.Second*10 {
			backoff = time.Second * 10
		}
	}
	return err
}
