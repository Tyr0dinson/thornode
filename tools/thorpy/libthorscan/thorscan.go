package main

import (
	// #include <stdlib.h>
	"C"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/signal"
	"sync"
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
	mu = &sync.Mutex{}

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
	mu.Lock()
	defer mu.Unlock()
	if scanBlocks != nil {
		log.Fatal().Msg("block scanner can only be started once")
	}
	log.Info().Msg("starting block scanner")
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
		os.Exit(0)
	}
	return C.CString(string(jb))
}

//export ScanBlockResults
func ScanBlockResults(start C.int) {
	mu.Lock()
	defer mu.Unlock()
	if scanBlockResults != nil {
		log.Fatal().Msg("block result scanner can only be started once")
	}
	log.Info().Msg("starting block result scanner")
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
		os.Exit(0)
	}
	return C.CString(string(jb))
}

//export SubscribeBlocks
func SubscribeBlocks() {
	mu.Lock()
	defer mu.Unlock()
	if subscribeBlocks != nil {
		log.Fatal().Msg("block subscription can only be started once")
	}
	log.Info().Msg("starting block subscription")
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
		os.Exit(0)
	}
	return C.CString(string(jb))
}

//export SubscribeTxs
func SubscribeTxs() {
	mu.Lock()
	defer mu.Unlock()
	if subscribeTxs != nil {
		log.Fatal().Msg("transaction subscription can only be started once")
	}
	log.Info().Msg("starting transaction subscription")
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
		os.Exit(0)
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
