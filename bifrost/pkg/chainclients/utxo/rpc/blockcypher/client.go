package blockcypher

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

// ErrNotSupported is returned for write/wallet operations or endpoints BlockCypher does not expose.
var ErrNotSupported = errors.New("operation not supported by blockcypher read-only adapter")

// Client implements UTXOReader by translating calls to the BlockCypher REST API.
// It does NOT implement write operations (SendRawTransaction, wallet imports).
// BCH and ZEC are not supported by BlockCypher.
//
// Scope limitation: GetBlockVerboseTxs fetches up to 500 txids per block (one page).
// Blocks with more than 500 transactions will have incomplete tx lists.
// GetRawMempool returns ErrNotSupported — BlockCypher has no full mempool dump endpoint.
type Client struct {
	baseURL    string
	coin       string
	chain      string
	token      string
	maxBytes   int64
	httpClient *http.Client
}

// NewClient returns a BlockCypher REST adapter.
//   - baseURL: e.g. "https://api.blockcypher.com/v1". No trailing slash.
//   - coin: e.g. "btc", "ltc", "doge", "dash".
//   - chain: e.g. "main", "test3".
//   - token: optional; empty string = no token (rate-limited to 3 req/s).
//   - maxResponseBytes: max bytes to read per response.
func NewClient(baseURL, coin, chain, token string, maxResponseBytes int64) *Client {
	return &Client{
		baseURL:  baseURL,
		coin:     coin,
		chain:    chain,
		token:    token,
		maxBytes: maxResponseBytes,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) get(path string, queryParams url.Values, out any) error {
	u := c.baseURL + "/" + c.coin + "/" + c.chain + path
	if c.token != "" {
		if queryParams == nil {
			queryParams = url.Values{}
		}
		queryParams.Set("token", c.token)
	}
	if len(queryParams) > 0 {
		u += "?" + queryParams.Encode()
	}

	resp, err := c.httpClient.Get(u) //nolint:noctx
	if err != nil {
		return fmt.Errorf("blockcypher GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes))
	if err != nil {
		return fmt.Errorf("blockcypher read body %s: %w", path, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("blockcypher %s returned status %d: %s", path, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

func (c *Client) GetBlockCount() (int64, error) {
	var resp ChainResponse
	if err := c.get("", nil, &resp); err != nil {
		return 0, err
	}
	return resp.Height, nil
}

func (c *Client) GetBlockHash(height int64) (string, error) {
	var resp BlockResponse
	if err := c.get(fmt.Sprintf("/blocks/%d", height), nil, &resp); err != nil {
		return "", err
	}
	if resp.Hash == "" {
		return "", fmt.Errorf("blockcypher: no block found at height %d", height)
	}
	return resp.Hash, nil
}

func (c *Client) GetBlockVerboseTxs(hash string) (*btcjson.GetBlockVerboseTxResult, error) {
	q := url.Values{
		"txids": {"true"},
		"limit": {"500"},
	}
	var resp BlockResponse
	if err := c.get("/blocks/"+hash, q, &resp); err != nil {
		return nil, err
	}
	result := &btcjson.GetBlockVerboseTxResult{
		Hash:   resp.Hash,
		Height: resp.Height,
	}
	// Only Txid is populated — full tx data requires per-tx API calls (scope limitation).
	result.Tx = make([]btcjson.TxRawResult, len(resp.TxIDs))
	for i, txid := range resp.TxIDs {
		result.Tx[i] = btcjson.TxRawResult{Txid: txid}
	}
	return result, nil
}

func (c *Client) GetBlockVerbose(hash string) (*btcjson.GetBlockVerboseResult, error) {
	var resp BlockResponse
	if err := c.get("/blocks/"+hash, nil, &resp); err != nil {
		return nil, err
	}
	return &btcjson.GetBlockVerboseResult{
		Hash:   resp.Hash,
		Height: resp.Height,
	}, nil
}

func (c *Client) GetBlockStats(_ string) (*btcjson.GetBlockStatsResult, error) {
	return nil, ErrNotSupported
}

func (c *Client) GetMempoolEntry(txid string) (*btcjson.GetMempoolEntryResult, error) {
	var resp TxResponse
	if err := c.get("/txs/"+txid, nil, &resp); err != nil {
		return nil, err
	}
	if resp.BlockHeight >= 0 {
		return nil, fmt.Errorf("blockcypher: transaction %s is confirmed (block %d)", txid, resp.BlockHeight)
	}
	return &btcjson.GetMempoolEntryResult{
		Fees: struct {
			Base       float64 `json:"base"`
			Modified   float64 `json:"modified"`
			Ancestor   float64 `json:"ancestor"`
			Descendant float64 `json:"descendant"`
		}{Base: float64(resp.Fees) / 1e8},
		Size: int32(resp.Size),
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

// GetRawMempool returns ErrNotSupported because BlockCypher has no mempool dump endpoint.
func (c *Client) GetRawMempool() ([]string, error) {
	return nil, ErrNotSupported
}

func (c *Client) GetRawTransactionVerbose(txid string) (*btcjson.TxRawResult, error) {
	var resp TxResponse
	if err := c.get("/txs/"+txid, nil, &resp); err != nil {
		return nil, err
	}
	result := &btcjson.TxRawResult{
		Txid: resp.Hash,
		Vin:  make([]btcjson.Vin, len(resp.Inputs)),
		Vout: make([]btcjson.Vout, len(resp.Outputs)),
	}
	for i, in := range resp.Inputs {
		result.Vin[i] = btcjson.Vin{Txid: in.PrevHash, Vout: in.OutputIndex}
	}
	for i, out := range resp.Outputs {
		addr := ""
		if len(out.Addresses) > 0 {
			addr = out.Addresses[0]
		}
		result.Vout[i] = btcjson.Vout{
			N:     uint32(i),
			Value: float64(out.Value) / 1e8,
			ScriptPubKey: btcjson.ScriptPubKeyResult{
				Type:      out.ScriptType,
				Addresses: []string{addr},
			},
		}
	}
	return result, nil
}

func (c *Client) GetRawTransaction(_ string) (string, error) {
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
