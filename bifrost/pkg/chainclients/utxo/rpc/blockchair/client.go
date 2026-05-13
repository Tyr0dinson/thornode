package blockchair

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	ethrpc "github.com/ethereum/go-ethereum/rpc"
)

// ErrNotSupported is returned for write/wallet operations that hosted providers cannot perform.
var ErrNotSupported = errors.New("operation not supported by blockchair read-only adapter")

// Client implements UTXOReader by translating calls to the Blockchair REST API.
// It does NOT implement write operations (SendRawTransaction, wallet imports).
//
// Scope limitation: GetBlockVerboseTxs returns transaction stubs with only Txid populated.
// Full tx data (Vsize, Vin, Vout) requires per-tx API calls and is not fetched here.
// Safe for block scanning (txid discovery) and individual tx lookups.
type Client struct {
	baseURL    string
	chain      string
	apiKey     string
	maxBytes   int64
	httpClient *http.Client
}

// NewClient returns a Blockchair REST adapter.
//   - baseURL: e.g. "https://api.blockchair.com". No trailing slash.
//   - chain: e.g. "bitcoin", "bitcoin-cash", "litecoin", "dogecoin", "zcash".
//   - apiKey: optional; empty string = no key.
//   - maxResponseBytes: max bytes to read per response (guards against memory exhaustion).
func NewClient(baseURL, chain, apiKey string, maxResponseBytes int64) *Client {
	return &Client{
		baseURL:  baseURL,
		chain:    chain,
		apiKey:   apiKey,
		maxBytes: maxResponseBytes,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) get(path string, queryParams url.Values, out any) error {
	u := c.baseURL + "/" + c.chain + path
	if c.apiKey != "" {
		if queryParams == nil {
			queryParams = url.Values{}
		}
		queryParams.Set("key", c.apiKey)
	}
	if len(queryParams) > 0 {
		u += "?" + queryParams.Encode()
	}

	resp, err := c.httpClient.Get(u) //nolint:noctx
	if err != nil {
		return fmt.Errorf("blockchair GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes))
	if err != nil {
		return fmt.Errorf("blockchair read body %s: %w", path, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("blockchair %s returned status %d: %s", path, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

func (c *Client) GetBlockCount() (int64, error) {
	var resp StatsResponse
	if err := c.get("/stats", nil, &resp); err != nil {
		return 0, err
	}
	return resp.Data.Blocks, nil
}

func (c *Client) GetBlockHash(height int64) (string, error) {
	q := url.Values{"q": {fmt.Sprintf("id(%d)", height)}}
	var resp BlocksResponse
	if err := c.get("/blocks", q, &resp); err != nil {
		return "", err
	}
	if len(resp.Data) == 0 {
		return "", fmt.Errorf("blockchair: no block found at height %d", height)
	}
	return resp.Data[0].Hash, nil
}

func (c *Client) GetBlockVerboseTxs(_ string) (*btcjson.GetBlockVerboseTxResult, error) {
	// Blockchair does not provide full Vin/Vout data in block responses; returning stubs
	// would cause the UTXO client to silently drop all observations for the block.
	// Return ErrNotSupported so withFallback falls through to the next provider.
	return nil, ErrNotSupported
}

func (c *Client) GetBlockVerbose(hash string) (*btcjson.GetBlockVerboseResult, error) {
	var resp BlockDetailResponse
	if err := c.get("/dashboards/block/"+hash, nil, &resp); err != nil {
		return nil, err
	}
	detail, ok := resp.Data[hash]
	if !ok {
		return nil, fmt.Errorf("blockchair: block %s not found in response", hash)
	}
	return &btcjson.GetBlockVerboseResult{
		Hash:   detail.Block.Hash,
		Height: detail.Block.ID,
	}, nil
}

func (c *Client) GetBlockStats(_ string) (*btcjson.GetBlockStatsResult, error) {
	return nil, ErrNotSupported
}

func (c *Client) GetMempoolEntry(txid string) (*btcjson.GetMempoolEntryResult, error) {
	var resp TransactionDashboardResponse
	if err := c.get("/dashboards/transaction/"+txid, nil, &resp); err != nil {
		return nil, err
	}
	tx, ok := resp.Data[txid]
	if !ok {
		return nil, fmt.Errorf("blockchair: transaction %s not found", txid)
	}
	if tx.Transaction.BlockID > 0 {
		return nil, fmt.Errorf("blockchair: transaction %s is confirmed (block %d)", txid, tx.Transaction.BlockID)
	}
	return &btcjson.GetMempoolEntryResult{
		Fees: struct {
			Base       float64 `json:"base"`
			Modified   float64 `json:"modified"`
			Ancestor   float64 `json:"ancestor"`
			Descendant float64 `json:"descendant"`
		}{Base: float64(tx.Transaction.Fee) / 1e8},
		Size: tx.Transaction.Size,
	}, nil
}

func (c *Client) BatchGetMempoolEntry(txids []string) ([]*btcjson.GetMempoolEntryResult, []error, error) {
	results := make([]*btcjson.GetMempoolEntryResult, len(txids))
	errs := make([]error, len(txids))
	for i, txid := range txids {
		results[i], errs[i] = c.GetMempoolEntry(txid)
	}
	return results, errs, nil
}

func (c *Client) GetRawMempool() ([]string, error) {
	var resp MempoolResponse
	if err := c.get("/mempool/transactions", nil, &resp); err != nil {
		return nil, err
	}
	txids := make([]string, len(resp.Data))
	for i, tx := range resp.Data {
		txids[i] = tx.Hash
	}
	return txids, nil
}

func (c *Client) GetRawTransactionVerbose(txid string) (*btcjson.TxRawResult, error) {
	var resp TransactionDashboardResponse
	if err := c.get("/dashboards/transaction/"+txid, nil, &resp); err != nil {
		return nil, err
	}
	tx, ok := resp.Data[txid]
	if !ok {
		return nil, fmt.Errorf("blockchair: transaction %s not found", txid)
	}
	result := &btcjson.TxRawResult{
		Txid: tx.Transaction.Hash,
		Vin:  make([]btcjson.Vin, len(tx.Inputs)),
		Vout: make([]btcjson.Vout, len(tx.Outputs)),
	}
	for i, in := range tx.Inputs {
		result.Vin[i] = btcjson.Vin{Txid: in.TransactionHash, Vout: in.Index}
	}
	for i, out := range tx.Outputs {
		result.Vout[i] = btcjson.Vout{
			N:     uint32(i),
			Value: float64(out.Value) / 1e8,
			ScriptPubKey: btcjson.ScriptPubKeyResult{
				Type:      out.Type,
				Addresses: []string{out.Recipient},
			},
		}
	}
	return result, nil
}

func (c *Client) GetRawTransaction(txid string) (string, error) {
	// Blockchair does not expose raw hex without a separate endpoint.
	return "", ErrNotSupported
}

func (c *Client) BatchGetRawTransactionVerbose(txids []string) ([]*btcjson.TxRawResult, []error, error) {
	results := make([]*btcjson.TxRawResult, len(txids))
	errs := make([]error, len(txids))
	for i, txid := range txids {
		results[i], errs[i] = c.GetRawTransactionVerbose(txid)
	}
	return results, errs, nil
}

func (c *Client) GetNetworkInfo() (*btcjson.GetNetworkInfoResult, error) {
	return nil, ErrNotSupported
}

func (c *Client) Call(_ any, _ string, _ ...interface{}) error {
	return ErrNotSupported
}

func (c *Client) BatchCall(_ []ethrpc.BatchElem) error {
	return ErrNotSupported
}
