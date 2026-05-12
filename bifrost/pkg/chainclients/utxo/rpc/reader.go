package rpc

import (
	"github.com/btcsuite/btcd/btcjson"
	ethrpc "github.com/ethereum/go-ethereum/rpc"
)

// UTXOReader is the read-only subset of *Client that fallback providers must implement.
// Write operations (SendRawTransaction, wallet imports, CreateWallet, ListUnspent) are
// deliberately excluded — hosted providers (Blockchair, BlockCypher) do not support them.
type UTXOReader interface {
	GetBlockCount() (int64, error)
	GetBlockHash(height int64) (string, error)
	GetBlockVerboseTxs(hash string) (*btcjson.GetBlockVerboseTxResult, error)
	GetBlockVerbose(hash string) (*btcjson.GetBlockVerboseResult, error)
	GetBlockStats(hash string) (*btcjson.GetBlockStatsResult, error)
	GetMempoolEntry(txid string) (*btcjson.GetMempoolEntryResult, error)
	BatchGetMempoolEntry(txids []string) ([]*btcjson.GetMempoolEntryResult, []error, error)
	GetRawMempool() ([]string, error)
	GetRawTransactionVerbose(txid string) (*btcjson.TxRawResult, error)
	GetRawTransaction(txid string) (string, error)
	BatchGetRawTransactionVerbose(txids []string) ([]*btcjson.TxRawResult, []error, error)
	GetNetworkInfo() (*btcjson.GetNetworkInfoResult, error)
	Call(result any, method string, args ...interface{}) error
	BatchCall(batch []ethrpc.BatchElem) error
}

// Compile-time assertion: *Client must satisfy UTXOReader.
var _ UTXOReader = (*Client)(nil)
