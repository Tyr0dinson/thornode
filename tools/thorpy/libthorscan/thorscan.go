package main

import (
	// #include <stdlib.h>
	"C"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/tools/thorscan"

	coretypes "github.com/tendermint/tendermint/rpc/core/types"
	tendertypes "github.com/tendermint/tendermint/types"
)

// -------------------------------------------------------------------------------------
// Global
// -------------------------------------------------------------------------------------

// NOTE: We maintain global state to handle subscriptions, as FFI calls will require the
// consumer to poll for updates and this allows for the cleanest API contract.

var (
	scanBlocks       <-chan *coretypes.ResultBlock
	scanBlockResults <-chan *coretypes.ResultBlockResults
	subscribeBlocks  <-chan *tendertypes.EventDataNewBlock
	subscribeTxs     <-chan *tendertypes.EventDataTx

	// shutdown cleanly
	shutdown = make(chan os.Signal, 1)
)

// -------------------------------------------------------------------------------------
// Init
// -------------------------------------------------------------------------------------

func init() {
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
}

// -------------------------------------------------------------------------------------
// Exported
// -------------------------------------------------------------------------------------

//export Free
func Free(p unsafe.Pointer) {
	C.free(p)
}

//export ScanBlocks
func ScanBlocks(start C.int) {
	scanBlocks = thorscan.ScanBlocks(int(start))
}

//export GetScanBlock
func GetScanBlock() *C.char {
	var err error
	var jb []byte
	select {
	case b := <-scanBlocks:
		jb, err = json.Marshal(b)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to marshal block")
		}
	case <-shutdown:
		close(shutdown)
	}
	return C.CString(string(jb))
}

//export ScanBlockResults
func ScanBlockResults(start C.int) {
	scanBlockResults = thorscan.ScanBlockResults(int(start))
}

//export GetScanBlockResult
func GetScanBlockResult() *C.char {
	var err error
	var jb []byte
	select {
	case br := <-scanBlockResults:
		jb, err = json.Marshal(br)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to marshal block result")
		}
	case <-shutdown:
		close(shutdown)
	}
	return C.CString(string(jb))
}

//export SubscribeBlocks
func SubscribeBlocks() {
	subscribeBlocks = thorscan.SubscribeBlocks()
}

//export GetSubscribeBlock
func GetSubscribeBlock() *C.char {
	var err error
	var jb []byte
	select {
	case sb := <-subscribeBlocks:
		jb, err = json.Marshal(sb)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to marshal block")
		}
	case <-shutdown:
		close(shutdown)
	}
	return C.CString(string(jb))
}

//export SubscribeTxs
func SubscribeTxs() {
	subscribeTxs = thorscan.SubscribeTxs()
}

//export GetSubscribeTx
func GetSubscribeTx() *C.char {
	var err error
	var jt []byte
	select {
	case tx := <-subscribeTxs:
		jt, err = json.Marshal(tx)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to marshal transaction")
		}
	case <-shutdown:
		close(shutdown)
	}
	return C.CString(string(jt))
}

//export TxToJSON
func TxToJSON(txb *C.char) *C.char {
	// decode base64
	txs, err := base64.StdEncoding.DecodeString(C.GoString(txb))
	if err != nil {
		log.Error().Err(err).Msg("failed to decode base64")
		return nil
	}

	// unmarshal protobuf
	tx, err := thorscan.DecodeTx(txs)
	if err != nil {
		log.Error().Err(err).Msg("failed to decode protobuf")
		return nil
	}

	// marshal json
	txj, err := thorscan.EncodeTxJSON(tx)
	if err != nil {
		log.Error().Err(err).Msg("failed to marshal transaction")
		return nil
	}

	return C.CString(string(txj))
}

//export TxFromJSON
func TxFromJSON(txj *C.char) *C.char {
	// unmarshal json
	tx, err := thorscan.DecodeTxJSON([]byte(C.GoString(txj)))
	if err != nil {
		log.Error().Err(err).Msg("failed to decode json")
		return nil
	}

	// marshal protobuf
	txb, err := thorscan.EncodeTx(tx)
	if err != nil {
		log.Error().Err(err).Msg("failed to marshal transaction")
		return nil
	}

	// encode base64
	txs := base64.StdEncoding.EncodeToString(txb)
	if err != nil {
		log.Error().Err(err).Msg("failed to base64 encode transaction")
		return nil
	}

	return C.CString(string(txs))
}

// -------------------------------------------------------------------------------------
// Main
// -------------------------------------------------------------------------------------

func main() {}
