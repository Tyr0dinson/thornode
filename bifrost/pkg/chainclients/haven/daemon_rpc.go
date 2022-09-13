package haven

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type ChainInfo struct {
	AltBlocksCount            uint64 `json:"alt_blocks_count"`
	BlockSizeLimit            uint64 `json:"block_size_limit"`
	BlockSizeMedian           uint64 `json:"block_size_median"`
	BlockWeightLimit          uint64 `json:"block_weight_limit"`
	BlockWeightMedian         uint64 `json:"block_weight_median"`
	BootstrapDaemonAddress    string `json:"bootstrap_faemon_address"`
	CumulativeDifficulty      uint64 `json:"cumulative_difficulty"`
	CumulativeDifficultyTop64 uint64 `json:"cumulative_difficulty_Top64"`
	DatabaseSize              uint64 `json:"database_size"`
	Difficulty                uint64 `json:"difficulty"`
	DifficultyTop64           uint64 `json:"difficulty_top64"`
	FreeSpace                 uint64 `json:"free_space"`
	GreyPeerlistSize          uint64 `json:"grey_peerlist_size"`
	Height                    uint64 `json:"height"`
	HeightWithoutBootstrap    uint64 `json:"height_without_bootstrap"`
	IncomingConnectionsCount  uint64 `json:"incoming_connections_count"`
	Mainnet                   bool   `json:"mainnet"`
	Nettype                   string `json:"nettype"`
	Offline                   bool   `json:"offline"`
	OutgoingConnectionsCount  uint64 `json:"outgoing_connections_count"`
	RpcConnectionsCount       uint64 `json:"rpc_connections_count"`
	Stagenet                  bool   `json:"stagenet"`
	Start_Time                uint64 `json:"start_time"`
	Status                    string `json:"status"`
	Target                    uint64 `json:"target"`
	Target_Height             uint64 `json:"target_height"`
	Testnet                   bool   `json:"testnet"`
	TopBlockHash              string `json:"top_block_hash"`
	TxCount                   uint64 `json:"tx_count"`
	TxPoolSize                uint64 `json:"tx_pool_size"`
	Untrusted                 bool   `json:"untrusted"`
	UpdateAvailable           bool   `json:"update_available"`
	Version                   string `json:"version"`
	WasBootstrapEverUsed      bool   `json:"was_bootstrap_ever_used"`
	WhitePeerlistSize         uint64 `json:"white_peerlist_size"`
	WideCumulativeDifficulty  string `json:"wide_cumulative_difficulty"`
	WideDifficulty            string `json:"wide_difficulty"`
}

type BlockHeader struct {
	BlockSize    uint64 `json:"block_size"`
	Depth        uint64 `json:"depth"`
	Difficulty   uint64 `json:"difficulty"`
	Hash         string `json:"hash"`
	Height       uint64 `json:"height"`
	Majorversion uint64 `json:"major_version"`
	Minorversion uint64 `json:"minor_version"`
	Nonce        uint64 `json:"nonce"`
	TxCount      uint64 `json:"num_txes"`
	Orphanstatus bool   `json:"orphan_status"`
	PrevHash     string `json:"prev_Hash"`
	Rewards      []struct {
		Amount    uint64 `json:"amount"`
		AssetType string `json:"asset_type"`
	} `json:"rewards"`
	Timestamp uint64 `json:"timestamp"`
}

type Block struct {
	Blob        string      `json:"blob"`
	BlockHeader BlockHeader `json:"block_header"`
	Json        string      `json:"json"`
	MinerTxHash string      `json:"miner_tx_hash"`
	Status      string      `json:"status"`
	Untrusted   bool        `json:"untrusted"`
	TxHashes    []string    `json:"tx_hashes"`
}

type VinKey struct {
	Amount     uint64   `json:"amount"`
	KeyOffsets []uint64 `json:"key_offsets"`
	KeyImage   string   `json:"k_image"`
}

type RawTx struct {
	Hash              string   `json:"hash"`
	Version           uint64   `json:"version"`
	OutputUnlockTimes []uint64 `json:"output_unlock_times"`
	Vin               []struct {
		Key      VinKey `json:"key"`
		Onshore  VinKey `json:"onshore"`
		Offshore VinKey `json:"offshore"`
	}
	Vout []struct {
		Amount uint64
		Target struct {
			Key       string `json:"key"`
			Offshore  string `json:"offshore"`
			Xasset    string `json:"xasset"`
			AssetType string `json:"asset_type"`
		}
	}
	Extra         []byte `json:"extra"`
	RctSignatures struct {
		Type           uint64              `json:"type"`
		TxnFee         uint64              `json:"txnFee"`
		TxnOffshoreFee uint64              `json:"txnOffshoreFee"`
		EcdhInfo       []map[string]string `json:"ecdhInfo"`
		OutPk          []string            `json:"outPk"`
	} `json:"rct_signatures"`
	BlockHeight uint64 `json:"block_height"`
}

type BroadcastTxResponse struct {
	Credits           uint64 `json:"credits"`
	DoubleSpend       bool   `json:"double_spend"`
	FeeTooLow         bool   `json:"fee_too_low"`
	InvalidInput      bool   `json:"invalid_input"`
	InvalidOutput     bool   `json:"invalid_output"`
	LowMixin          bool   `json:"low_mixin"`
	NotRelayed        bool   `json:"not_telayed"`
	Overspend         bool   `json:"overspend"`
	Reason            string `json:"reason"`
	SanityCheckFailed bool   `json:"sanity_check_failed"`
	Status            string `json:"status"`
	TooBig            bool   `json:"too_big"`
	TooFewOutputs     bool   `json:"too_few_outputs"`
	TopHash           string `json:"top_hash"`
	Untrusted         bool   `json:"untrusted"`
}

var DaemonHost = ""

const ContentType = "application/json"

func getChainInfo() (ChainInfo, error) {
	reply := struct {
		Result ChainInfo `json:"result"`
	}{}

	// prepare body
	requestBody, err := json.Marshal(map[string]interface{}{"json_rpc": 2.0, "id": 0, "method": "get_info"})
	if err != nil {
		return reply.Result, fmt.Errorf("getChainInfo() Marshaling request Error: %+v", err)
	}

	// execute request
	resp, err := http.Post(DaemonHost+"/json_rpc", ContentType, bytes.NewBuffer(requestBody))
	if err != nil {
		return reply.Result, fmt.Errorf("getChainInfo() Http Error: %+v", err)
	}
	defer resp.Body.Close()

	// read and parse the returned response
	err = json.NewDecoder(resp.Body).Decode(&reply)
	if err != nil {
		return reply.Result, fmt.Errorf("getChainInfo() Unmarshaling Response Error: %+v", err)
	}

	return reply.Result, nil
}

// GetHeight gets the height of the haven blockchain
func GetChainHeight() (uint64, error) {
	chainInfo, err := getChainInfo()
	if err != nil {
		return 0, fmt.Errorf("failed to get chain height: %+v", err)
	}

	return chainInfo.Height - 1, nil
}

func GetBlock(height int64) (Block, error) {
	reply := struct {
		Result Block `json:"result"`
	}{}

	// prepare body
	params := map[string]int64{"height": height}
	requestBody, err := json.Marshal(map[string]interface{}{"json_rpc": 2.0, "id": 0, "method": "get_block", "params": params})
	if err != nil {
		return reply.Result, fmt.Errorf("getBlock() Marshaling request Error: %+v", err)
	}

	// execute request
	resp, err := http.Post(DaemonHost+"/json_rpc", ContentType, bytes.NewBuffer(requestBody))
	if err != nil {
		return reply.Result, fmt.Errorf("getBlock() Http Error: %+v", err)
	}
	defer resp.Body.Close()

	// read and parse the returned response
	err = json.NewDecoder(resp.Body).Decode(&reply)
	if err != nil {
		return reply.Result, fmt.Errorf("getBlock() Unmarshaling Response Error: %+v", err)
	}

	return reply.Result, err
}

// GetTxs returns the txs for given hashes. If any of the given hashes could not be found on the chain
// it returns then in the missedTxs variable.
func GetTxs(txs []string) (rawTxs []RawTx, missedTxs []string, err error) {
	requestBody, err := json.Marshal(map[string]interface{}{"txs_hashes": txs, "decode_as_json": true})
	if err != nil {
		return
	}

	resp, err := http.Post(DaemonHost+"/get_transactions", ContentType, bytes.NewBuffer(requestBody))
	if err != nil {
		return
	}
	defer resp.Body.Close()

	txResult := struct {
		MissedTxs []string `json:"missed_tx"`
		Status    string   `json:"status"`
		TxsAsJson []string `json:"txs_as_json"`
		Txs       []struct {
			BlockHeight uint64 `json:"block_height"`
			TxHash      string `json:"tx_hash"`
		} `json:"txs"`
	}{}

	err = json.NewDecoder(resp.Body).Decode(&txResult)
	if err != nil {
		return
	}

	// parse each tx in the result and save
	for ind, jsonTx := range txResult.TxsAsJson {
		var rawTx RawTx
		err = json.Unmarshal([]byte(jsonTx), &rawTx)
		if err != nil {
			return
		}
		rawTx.BlockHeight = txResult.Txs[ind].BlockHeight
		rawTx.Hash = txResult.Txs[ind].TxHash
		rawTxs = append(rawTxs, rawTx)
	}
	missedTxs = txResult.MissedTxs

	return
}

func GetPoolTxs() ([]RawTx, error) {
	resp, err := http.Get(DaemonHost + "/get_transaction_pool")
	if err != nil {
		return nil, fmt.Errorf("getPoolTxs() Marshaling request Error: %+v", err)
	}
	defer resp.Body.Close()

	// parse response
	result := struct {
		Transactions []struct {
			Hash   string `json:"id_hash"`
			AsJson string `json:"tx_json"`
		} `json:"transactions"`
	}{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, fmt.Errorf("getPoolTxs() Unmarshaling Response Error: %+v", err)
	}

	// parse the txs
	var rawTxs []RawTx
	for _, tx := range result.Transactions {
		var rawTx RawTx
		err = json.Unmarshal([]byte(tx.AsJson), &rawTx)
		if err != nil {
			return nil, fmt.Errorf("getPoolTxs() Unmarshaling Response Error: %+v", err)
		}
		rawTx.Hash = tx.Hash
		rawTx.BlockHeight = 0 // they are in pool, so don't have block height
		rawTxs = append(rawTxs, rawTx)
	}

	return rawTxs, nil
}

func SendRawTransaction(txHash string) BroadcastTxResponse {
	var reply BroadcastTxResponse

	requestBody, err := json.Marshal(map[string]interface{}{"tx_as_hex": txHash})
	if err != nil {
		reply.Status = "Marshaling Request Error"
		reply.Reason = fmt.Sprintf("%+v", err)
		return reply
	}

	resp, err := http.Post(DaemonHost+"/sendrawtransaction", ContentType, bytes.NewBuffer(requestBody))
	if err != nil {
		reply.Status = "Http Error"
		reply.Reason = fmt.Sprintf("%+v", err)
		return reply
	}
	defer resp.Body.Close()

	// parse response
	err = json.NewDecoder(resp.Body).Decode(&reply)
	if err != nil {
		reply.Status = "Unmarshaling Response Error"
		reply.Reason = fmt.Sprintf("%+v", err)
		return reply
	}

	return reply
}
