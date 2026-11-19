package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/fbsobreira/gotron-sdk/pkg/proto/core"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

var errTransaction = errors.New("invalid or unsupported signed transaction")

type transactionJSON struct {
	ID        string          `json:"txID"`
	RawHex    string          `json:"raw_data_hex"`
	Signature []string        `json:"signature"`
	Visible   bool            `json:"visible,omitempty"`
	Ret       json.RawMessage `json:"ret,omitempty"`
	Raw       struct {
		Data       string `json:"data,omitempty"`
		Timestamp  int64  `json:"timestamp"`
		Expiration int64  `json:"expiration"`
		RefBytes   string `json:"ref_block_bytes"`
		RefHash    string `json:"ref_block_hash"`
		FeeLimit   int64  `json:"fee_limit,omitempty"`
		Contracts  []struct {
			Type      string `json:"type"`
			Parameter struct {
				TypeURL string `json:"type_url"`
				Value   struct {
					Owner     string `json:"owner_address"`
					To        string `json:"to_address,omitempty"`
					Contract  string `json:"contract_address,omitempty"`
					Amount    int64  `json:"amount,omitempty"`
					Data      string `json:"data,omitempty"`
					CallValue int64  `json:"call_value,omitempty"`
				} `json:"value"`
			} `json:"parameter"`
		} `json:"contract"`
	} `json:"raw_data"`
}

// Reject duplicate JSON keys before a different upstream decoder can interpret them.
func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errTransaction
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errTransaction
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errTransaction
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errTransaction
	}
	return nil
}

func address(value string, visible bool) ([]byte, error) {
	if visible {
		body, version, err := base58.CheckDecode(value)
		if err != nil || version != 0x41 || len(body) != 20 {
			return nil, errTransaction
		}
		return append([]byte{version}, body...), nil
	}
	data, err := hex.DecodeString(value)
	if err != nil || len(data) != 21 || data[0] != 0x41 {
		return nil, errTransaction
	}
	return data, nil
}

func parseSigned(body []byte) (*Entry, error) {
	if len(body) > 65536 || uniqueJSON(body) != nil {
		return nil, errTransaction
	}
	var tx transactionJSON
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&tx) != nil || len(tx.Signature) != 1 || len(tx.Raw.Contracts) != 1 {
		return nil, errTransaction
	}
	if !exactFields(body, reflect.TypeOf(tx)) {
		return nil, errTransaction
	}
	rawBytes, err := hex.DecodeString(tx.RawHex)
	if err != nil || len(rawBytes) == 0 {
		return nil, errTransaction
	}
	id := sha256.Sum256(rawBytes)
	if tx.ID != hex.EncodeToString(id[:]) {
		return nil, errTransaction
	}
	signature, err := hex.DecodeString(tx.Signature[0])
	if err != nil || len(signature) != 65 {
		return nil, errTransaction
	}
	var raw core.TransactionRaw
	if proto.Unmarshal(rawBytes, &raw) != nil {
		return nil, errTransaction
	}
	canonical, err := proto.Marshal(&raw)
	if err != nil || !bytes.Equal(canonical, rawBytes) {
		return nil, errTransaction
	}
	refBytes, err := hex.DecodeString(tx.Raw.RefBytes)
	if err != nil || len(refBytes) != 2 {
		return nil, errTransaction
	}
	refHash, err := hex.DecodeString(tx.Raw.RefHash)
	if err != nil || len(refHash) != 8 {
		return nil, errTransaction
	}
	memo, err := hex.DecodeString(tx.Raw.Data)
	if err != nil {
		return nil, errTransaction
	}
	contract := tx.Raw.Contracts[0]
	param := contract.Parameter.Value
	owner, err := address(param.Owner, tx.Visible)
	if err != nil {
		return nil, err
	}
	var payload proto.Message
	var contractType core.Transaction_Contract_ContractType
	switch contract.Type {
	case "TransferContract":
		to, err := address(param.To, tx.Visible)
		if err != nil || param.Amount <= 0 || param.Contract != "" || param.Data != "" || param.CallValue != 0 {
			return nil, errTransaction
		}
		payload = &core.TransferContract{OwnerAddress: owner, ToAddress: to, Amount: param.Amount}
		contractType = core.Transaction_Contract_TransferContract
	case "TriggerSmartContract":
		to, err := address(param.Contract, tx.Visible)
		data, dataErr := hex.DecodeString(param.Data)
		if err != nil || dataErr != nil || len(data) == 0 || param.To != "" || param.Amount != 0 || param.CallValue != 0 {
			return nil, errTransaction
		}
		payload = &core.TriggerSmartContract{OwnerAddress: owner, ContractAddress: to, Data: data}
		contractType = core.Transaction_Contract_TriggerSmartContract
	default:
		return nil, errTransaction
	}
	parameter, err := anypb.New(payload)
	if err != nil || parameter.TypeUrl != contract.Parameter.TypeURL {
		return nil, errTransaction
	}
	expected := &core.TransactionRaw{RefBlockBytes: refBytes, RefBlockHash: refHash, Expiration: tx.Raw.Expiration, Timestamp: tx.Raw.Timestamp, Data: memo, FeeLimit: tx.Raw.FeeLimit,
		Contract: []*core.Transaction_Contract{{Type: contractType, Parameter: parameter}}}
	if tx.Raw.Timestamp <= 0 || tx.Raw.Expiration <= tx.Raw.Timestamp || tx.Raw.Expiration-tx.Raw.Timestamp > 86400000 || tx.Raw.FeeLimit < 0 || !proto.Equal(&raw, expected) {
		return nil, errTransaction
	}
	fingerprint := sha256.Sum256(append(append([]byte{}, rawBytes...), signature...))
	return &Entry{ID: tx.ID, Fingerprint: hex.EncodeToString(fingerprint[:]), Body: bytes.Clone(body), Expiration: raw.Expiration}, nil
}

func validID(id string) bool {
	data, err := hex.DecodeString(id)
	return err == nil && len(data) == 32 && id == strings.ToLower(id)
}

// encoding/json accepts case-insensitive aliases; Java's decoder does not.
func exactFields(data []byte, typ reflect.Type) bool {
	if typ == reflect.TypeOf(json.RawMessage{}) {
		return true
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			fields[strings.Split(field.Tag.Get("json"), ",")[0]] = field.Type
		}
		for name, value := range object {
			child, ok := fields[name]
			if !ok || !exactFields(value, child) {
				return false
			}
		}
	case reflect.Slice:
		if typ.Elem().Kind() != reflect.Struct {
			return true
		}
		var list []json.RawMessage
		if json.Unmarshal(data, &list) != nil {
			return false
		}
		for _, value := range list {
			if !exactFields(value, typ.Elem()) {
				return false
			}
		}
	}
	return true
}
