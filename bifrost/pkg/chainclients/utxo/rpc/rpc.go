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
	// default to http if no scheme is specified
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	// credentials embedded in the URL are lifted into the header and stripped from
	// the URL, matching the router and the probes (and the documented behavior)
	if u, perr := url.Parse(host); perr == nil && u.User != nil {
		if bearer == "" && user == "" && password == "" {
			user = u.User.Username()
			password, _ = u.User.Password()
		}
		u.User = nil
		host = u.String()
	}

	authFn := func(h http.Header) error {
		if bearer != "" {
			h.Set("Authorization", fmt.Sprintf("Bearer %s", bearer))
			return nil
		}
		auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
		h.Set("Authorization", fmt.Sprintf("Basic %s", auth))
		return nil
	}

	client := Client{
		log:        log,
		chain:      chain,
		maxRetries: maxRetries,
		timeout:    timeout,
	}
	// record this client's own secrets so its errors are redacted with or without
	// an endpoint manager
	if u, perr := url.Parse(host); perr == nil {
		client.secrets = [][]string{endpoints.SecretsOf(u, bearer, password)}
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
// Without an endpoint manager, fallbacks are tried in slice order after the primary
// exhausts its retries, and a rate-limited primary is waited for (rateLimitBackoff,
// no failover). With one (WithEndpointManager, as in production), reads follow the
// manager's candidate order, so a fallback can be asked first, and a rate-limited
// endpoint is put in cooldown while the next candidate serves the read.
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
	secrets := c.secrets
	c.secrets = make([][]string, len(eps))
	for i, e := range eps {
		c.names[i] = e.Name
		c.index[e.Name] = i
		if u, err := url.Parse(endpoints.NormalizeHost(e.RPCHost)); err == nil {
			c.secrets[i] = endpoints.SecretsOf(u, e.AuthorizationBearer, e.Password)
		} else if i < len(secrets) {
			c.secrets[i] = secrets[i]
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

// rpcVerdict classifies an RPC error for health accounting: a provider rate limit
// (HTTP 429 or JSON-RPC -32005 / -32029) is a cooldown; JSON-RPC -32601 / -32603
// (method not found / internal) mean the endpoint cannot serve bifrost; any other
// JSON-RPC application answer (such as "No such mempool transaction") means the
// endpoint served the request; everything else is a transport / HTTP failure.
type rpcVerdict int

const (
	verdictFailure rpcVerdict = iota
	verdictRateLimit
	verdictEndpointError
	verdictApplication
)

func classifyRPCError(err error) rpcVerdict {
	if isRateLimitError(err) {
		return verdictRateLimit
	}
	if isAuthRefusal(err) {
		return verdictFailure
	}
	var code int
	var rpcErr *btcjson.RPCError
	var zecErr *zec.Error
	switch {
	case errors.As(extractBTCError(err), &rpcErr):
		// a body without a JSON-RPC error object (a gateway's own error) decodes to
		// code 0 and no message: not a daemon's answer
		if rpcErr.Code == 0 && rpcErr.Message == "" {
			return verdictFailure
		}
		code = int(rpcErr.Code)
	case errors.As(err, &zecErr): // Zebra answers through the zec client with its own error type
		code = zecErr.Code
	default:
		return verdictFailure
	}
	switch code {
	case -32005, -32029:
		return verdictRateLimit
	case -32601, -32603, int(btcjson.ErrRPCInWarmup):
		// a method it cannot serve, an internal error, or still warming up (-28,
		// refused before the request runs): the endpoint has not served the read
		return verdictEndpointError
	}
	return verdictApplication
}

func (c *Client) recordFailure(i int, err error) {
	if c.manager == nil || err == nil {
		return
	}
	switch classifyRPCError(err) {
	case verdictRateLimit:
		c.manager.RecordRateLimited(c.names[i])
	case verdictApplication:
		c.manager.RecordSuccess(c.names[i])
	default:
		c.manager.RecordError(c.names[i], endpoints.ErrorKindHTTP)
	}
}

// primaryDisabled reports whether the primary may not be used: the manager disabled
// it (identity mismatch), or its network must be verified and is not yet (its
// identity probe has not succeeded). Primary-only operations (writes, wallet RPC,
// verification reads) must then fail instead of reaching a daemon on an unknown or
// wrong network. A verified primary that is merely unhealthy is still asked.
func (c *Client) primaryDisabled() error {
	if c.manager == nil {
		return nil
	}
	e := c.manager.Entry(c.names[0])
	switch {
	case e == nil || e.Usable():
		return nil
	case e.State() == endpoints.StateDisabled:
		return fmt.Errorf("primary %s endpoint %s is disabled: %s", c.chain, e.Name, e.Snapshot().DisabledReason)
	default:
		return fmt.Errorf("primary %s endpoint %s is not usable: its network is not verified yet", c.chain, e.Name)
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
// cooldown and the read is served by the next candidate (an application answer, such
// as "no such transaction", counts as served); if nothing served the read and the
// primary was rate limited, the caller still sleeps.
func (c *Client) readWithFallback(primary func() error, fn func(UTXOReader) error) error {
	var (
		firstErr    error
		firstIdx    int
		appErr      error // an application answer ("no such transaction") from an endpoint that served the read
		appIdx      int
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
		if appErr == nil && classifyRPCError(err) == verdictApplication {
			appErr, appIdx = err, i
		}
		if i == 0 && classifyRPCError(err) == verdictRateLimit {
			rateLimited = true
			if c.manager == nil {
				break
			}
		}
	}
	if rateLimited && appErr == nil {
		c.sleepRateLimit()
	}
	if appErr != nil {
		// an endpoint that served the read is authoritative over one that failed
		return c.redact(appIdx, appErr)
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

// redact scrubs endpoint i's URL and credentials from err. Index 0 is the primary,
// whose secrets are recorded at construction, so a client with no endpoint manager
// (every fallback built by buildUTXOFallbacks) still redacts its own URL.
func (c *Client) redact(i int, err error) error {
	if err == nil || i >= len(c.secrets) {
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

// GetBlockCountPrimary returns the primary daemon's own block count, so a decision
// that trusts the primary's "not found" can first check the primary has reached the
// heights in question (a resyncing primary answers "not found" for everything above
// its tip).
func (c *Client) GetBlockCountPrimary() (int64, error) {
	var count int64
	err := c.callRaw(&count, "getblockcount")
	return count, extractBTCError(c.redact(0, err))
}

// HasFallbacks reports whether reads can be served by an endpoint other than the
// primary, so a height the scan reached may be ahead of the primary's own.
func (c *Client) HasFallbacks() bool { return len(c.fallbacks) > 0 }

// PrimaryInRotation reports whether the health checks currently keep the primary
// daemon in rotation (always true without an endpoint manager). A decision that can
// wait, such as pruning, skips a primary they have taken out instead of running its
// full retry loop on every block.
func (c *Client) PrimaryInRotation() bool {
	if c.manager == nil || len(c.names) == 0 {
		return true
	}
	e := c.manager.Entry(c.names[0])
	return e != nil && e.Eligible()
}

// PrimaryOutWithFallback reports whether the health checks hold the primary daemon
// out of rotation while a fallback serves reads. A per-block read only the primary
// can answer is then skipped (or fails at once) instead of running the primary's
// retry loop on every block, which would keep the fallback from holding the scanner
// at the tip. Without a fallback it is false: there is nothing else to scan from, so
// the primary is asked as before failover existed.
func (c *Client) PrimaryOutWithFallback() bool {
	return len(c.fallbacks) > 0 && !c.PrimaryInRotation()
}

// GetBlockHashPrimary returns the primary daemon's own block hash at a height, so a
// decision that trusts the primary's "not found" can first check the primary is on
// the chain in question.
func (c *Client) GetBlockHashPrimary(height int64) (string, error) {
	var hash string
	err := c.callRaw(&hash, "getblockhash", height)
	return hash, extractBTCError(c.redact(0, err))
}

// GetRawMempoolPrimary lists the primary daemon's mempool (for daemons such as Zebra
// that do not implement getmempoolentry).
func (c *Client) GetRawMempoolPrimary() ([]string, error) {
	var txids []string
	err := c.callRaw(&txids, "getrawmempool")
	return txids, extractBTCError(c.redact(0, err))
}

// GetMempoolEntryPrimary is the primary-only counterpart of GetMempoolEntry, for
// signer-side mempool checks that must observe the broadcasting daemon.
func (c *Client) GetMempoolEntryPrimary(txid string) (*btcjson.GetMempoolEntryResult, error) {
	var entry btcjson.GetMempoolEntryResult
	err := c.callRaw(&entry, "getmempoolentry", txid)
	return &entry, extractBTCError(c.redact(0, err))
}

// GetBlockCount returns the number of blocks in the longest block chain.
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

// ZecAddressUTXO preserves the block height and integer value returned by Zebra.
// Signing must not recover this height from confirmations at a moving RPC tip.
type ZecAddressUTXO struct {
	Txid     string `json:"txid"`
	Address  string `json:"address"`
	Index    uint32 `json:"outputIndex"`
	Script   string `json:"script"`
	Satoshis uint64 `json:"satoshis"`
	Height   int64  `json:"height"`
}

func (c *Client) GetZecAddressUTXOs(address string) ([]ZecAddressUTXO, error) {
	if c.chain != common.ZECChain {
		return nil, fmt.Errorf("ZEC address UTXOs requested for %s", c.chain)
	}
	var utxos []ZecAddressUTXO
	err := c.Call(&utxos, "getaddressutxos", []any{address}...)
	return utxos, extractBTCError(err)
}

// ListUnspent returns all unspent outputs with between min and max confirmations.
func (c *Client) ListUnspent(address string) ([]btcjson.ListUnspentResult, error) {
	var unspent []btcjson.ListUnspentResult
	var err error

	switch c.chain {
	case common.ZECChain:
		// the primary's own height: the UTXOs and their heights below come from the
		// primary only, so confirmations must be counted against the same daemon
		var height int64
		height, err = c.GetBlockCountPrimary()
		if err != nil {
			return nil, err
		}

		var utxos []ZecAddressUTXO
		utxos, err = c.GetZecAddressUTXOs(address)

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

// GetNetworkInfoPrimary is GetNetworkInfo asked of the primary daemon only. The relay
// fee it reports feeds the outbound fee floor, which shapes the signed transaction
// every signer must build alike: it must be this node's own daemon's policy (the one
// its broadcasts go to), never a fallback provider's, which may run another version
// with another default.
func (c *Client) GetNetworkInfoPrimary() (*btcjson.GetNetworkInfoResult, error) {
	var info struct {
		btcjson.GetNetworkInfoResult
		Warnings flexibleWarnings `json:"warnings"`
	}
	if err := c.callRaw(&info, "getnetworkinfo"); err != nil {
		return nil, extractBTCError(c.redact(0, err))
	}
	result := info.GetNetworkInfoResult
	result.Warnings = strings.Join(info.Warnings, "; ")
	return &result, nil
}

func (c *Client) Call(result any, method string, args ...interface{}) error {
	err := c.callRaw(result, method, args...)
	return extractBTCError(err)
}

// CallWithFallback issues a generic JSON-RPC read through the read candidates (see
// NewClientWithFallbacks for the order and the rate-limit rule). Use it
// for reads that have no typed helper, e.g. BCH getblockstats, whose float avgfeerate
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
	if err := c.primaryDisabled(); err != nil {
		return err
	}
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
	var err error
	if c.timeout <= 0 {
		err = c.client.BatchCall(batch)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		err = c.client.BatchCallContext(ctx, batch)
	}
	if err != nil {
		return err
	}
	// A batch reports per-element failures through BatchElem.Error with a nil
	// aggregate error, so an endpoint that rate limits, cannot serve the method, or
	// did not answer an element at all (missing response, null result from a node
	// that does not have the data yet) would otherwise look like a success. Surface
	// the first such element so the read is retried on another endpoint and the
	// endpoint is accounted for. A result that does not decode stays per-element, as
	// it always did: the caller skips that element, and one transaction that will
	// never decode cannot abort the whole batch on every round.
	for _, elem := range batch {
		if elem.Error == nil {
			continue
		}
		if errors.Is(elem.Error, rpc.ErrMissingBatchResponse) || errors.Is(elem.Error, rpc.ErrNoResult) {
			return elem.Error
		}
		switch classifyRPCError(elem.Error) {
		case verdictRateLimit, verdictEndpointError:
			return elem.Error
		}
	}
	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// isAuthRefusal reports whether err is an HTTP 401 or 403: the server refused the
// request (a gateway's API key or auth check), so it has not served it, whatever JSON
// its body carries. A daemon answers wrong credentials with an empty body.
func isAuthRefusal(err error) bool {
	var httpErr rpc.HTTPError
	return errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden)
}

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
	// an HTTP 429 must keep its status: the fallback clients return through this
	// function, and classifyRPCError needs to see the rate limit, not the generic
	// JSON-RPC error object some providers put in the body
	if isRateLimitError(err) {
		return err
	}
	// a refusal keeps its status for the same reason: classifyRPCError must not read a
	// gateway's JSON body as the daemon's answer
	if isAuthRefusal(err) {
		return err
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

// retrySleep waits out a retry backoff. Tests replace it to record the
// backoffs instead of sleeping through them.
var retrySleep = time.Sleep

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
		c.log.Err(c.redact(0, err)).
			Str("backoff", backoff.String()).
			Msgf("retrying %d/%d after backoff", i+1, c.maxRetries)
		retrySleep(backoff)

		// exponential backoff, max 10 seconds
		backoff *= 2
		if backoff > time.Second*10 {
			backoff = time.Second * 10
		}
	}
	return err
}
