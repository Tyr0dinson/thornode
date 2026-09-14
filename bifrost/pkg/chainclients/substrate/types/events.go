package types

import (
	"encoding/binary"
	"fmt"
	"strconv"

	scalecodec "github.com/itering/scale.go"
	scaleTypes "github.com/itering/scale.go/types"
	"github.com/itering/scale.go/types/scaleBytes"
)

const (
	systemExtrinsicSuccessEvent = "ExtrinsicSuccess"
	systemExtrinsicFailedEvent  = "ExtrinsicFailed"
	txPaymentFeePaidEvent       = "TransactionFeePaid"
	actualFeeParam              = "actual_fee"
	tipParam                    = "tip"
)

// Fee records the native transaction fee, including its optional tip.
type Fee struct {
	Total uint64
	Tip   uint64
}

// EventPalletConfig identifies the pallets whose events affect transaction
// observation. Metadata supplies event names, indexes, and SCALE payload types.
type EventPalletConfig struct {
	Metadata                      *Metadata
	SystemPalletIndex             byte
	TransactionPaymentPalletIndex byte
}

// DecodedEventParam is a metadata-typed event parameter returned by scalecodec.
type DecodedEventParam struct {
	Name     string
	Type     string
	TypeName string
	Value    interface{}
}

// DecodedEvent is the stable internal representation of one runtime event.
// Keeping this type independent of scalecodec limits the dependency boundary.
type DecodedEvent struct {
	Phase          byte
	ExtrinsicIndex uint32
	PalletIndex    byte
	EventIndex     byte
	Pallet         string
	Name           string
	Params         []DecodedEventParam
	Topics         []string
}

// DecodeEvents decodes a complete SCALE Vec<EventRecord> using the runtime
// metadata that produced it. It wraps scalecodec's panic-based API, validates
// framing itself, and rejects truncated or trailing input.
func DecodeEvents(data []byte, metadata *Metadata) ([]DecodedEvent, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if metadata == nil || metadata.codec == nil {
		return nil, fmt.Errorf("runtime metadata required for event decoding")
	}

	numEvents, consumed, err := DecodeCompactToInt(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode events count: %w", err)
	}
	// The smallest record is phase + event ID + empty topics. This also keeps a
	// malformed count from causing a large allocation or decode loop.
	if numEvents > (len(data)-consumed)/4 {
		return nil, fmt.Errorf("event count %d exceeds remaining data", numEvents)
	}

	// scale.go's ScaleBytes truncates reads at EOF instead of reporting them.
	// One guard byte makes any attempted read beyond the real input observable
	// as a final offset greater than dataLen.
	guarded := make([]byte, len(data)+1)
	copy(guarded, data)

	events := make([]DecodedEvent, 0, numEvents)
	pos := consumed

	// Metadata decoding registers portable types in scale.go's package-global
	// registry. Hold the read lock for the complete event stream so a runtime
	// metadata refresh cannot change that registry midway through decoding.
	scaleCodecRegistryMu.RLock()
	defer scaleCodecRegistryMu.RUnlock()

	for i := 0; i < numEvents; i++ {
		event, next, decodeErr := decodeEventRecord(guarded, len(data), pos, metadata)
		if decodeErr != nil {
			return nil, fmt.Errorf("failed to decode event %d: %w", i, decodeErr)
		}
		events = append(events, event)
		pos = next
	}

	if pos != len(data) {
		return nil, fmt.Errorf("events data has %d trailing bytes", len(data)-pos)
	}

	return events, nil
}

func decodeEventRecord(data []byte, dataLen, pos int, metadata *Metadata) (DecodedEvent, int, error) {
	if pos >= dataLen {
		return DecodedEvent{}, pos, fmt.Errorf("missing phase")
	}

	phase := data[pos]
	eventIDPos := pos + 1
	var extrinsicIndex uint32
	switch phase {
	case 0:
		if eventIDPos+4 > dataLen {
			return DecodedEvent{}, pos, fmt.Errorf("missing extrinsic index")
		}
		extrinsicIndex = binary.LittleEndian.Uint32(data[eventIDPos : eventIDPos+4])
		eventIDPos += 4
	case 1, 2:
		// Finalization and Initialization carry no extrinsic index.
	default:
		return DecodedEvent{}, pos, fmt.Errorf("invalid phase %d", phase)
	}
	if eventIDPos+2 > dataLen {
		return DecodedEvent{}, pos, fmt.Errorf("missing event ID")
	}

	record := scalecodec.EventRecord{Metadata: metadata.codec}
	record.Init(
		scaleBytes.ScaleBytes{Data: data, Offset: pos},
		&scaleTypes.ScaleDecoderOption{Metadata: metadata.codec, Spec: metadata.codecSpec},
	)
	if err := processEventRecord(&record); err != nil {
		return DecodedEvent{}, pos, err
	}

	next := record.GetData().Offset
	if next <= pos {
		return DecodedEvent{}, pos, fmt.Errorf("decoder made no progress")
	}
	if next > dataLen {
		return DecodedEvent{}, pos, fmt.Errorf("truncated event data")
	}
	if record.Phase != int(phase) || uint32(record.ExtrinsicIdx) != extrinsicIndex {
		return DecodedEvent{}, pos, fmt.Errorf("decoder returned inconsistent phase data")
	}

	params := make([]DecodedEventParam, len(record.Params))
	for i, param := range record.Params {
		params[i] = DecodedEventParam{
			Name:     param.Name,
			Type:     param.Type,
			TypeName: param.TypeName,
			Value:    param.Value,
		}
	}

	return DecodedEvent{
		Phase:          phase,
		ExtrinsicIndex: extrinsicIndex,
		PalletIndex:    data[eventIDPos],
		EventIndex:     data[eventIDPos+1],
		Pallet:         record.Module,
		Name:           record.Event.Name,
		Params:         params,
		Topics:         append([]string(nil), record.Topic...),
	}, next, nil
}

func processEventRecord(record *scalecodec.EventRecord) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("scalecodec panicked: %v", recovered)
		}
	}()
	record.Process()
	return nil
}

// ParseExtrinsicResults returns extrinsic success and fee information from a
// fully metadata-decoded event stream.
func ParseExtrinsicResults(data []byte, pallets EventPalletConfig) (map[uint32]bool, map[uint32]Fee, error) {
	events, err := DecodeEvents(data, pallets.Metadata)
	if err != nil {
		return nil, nil, err
	}

	results := make(map[uint32]bool)
	fees := make(map[uint32]Fee)
	for _, event := range events {
		if event.Phase != 0 {
			continue
		}

		if event.PalletIndex == pallets.SystemPalletIndex {
			var succeeded bool
			switch event.Name {
			case systemExtrinsicSuccessEvent:
				succeeded = true
			case systemExtrinsicFailedEvent:
				succeeded = false
			default:
				continue
			}
			_, duplicate := results[event.ExtrinsicIndex]
			if duplicate {
				return nil, nil, fmt.Errorf("duplicate result event for extrinsic %d", event.ExtrinsicIndex)
			}
			results[event.ExtrinsicIndex] = succeeded
			continue
		}

		// Only TransactionPayment fees are denominated in the native gas asset.
		// AssetTxPayment:AssetTxFeePaid uses the selected token's units and must
		// not enter native fee reporting.
		if event.PalletIndex == pallets.TransactionPaymentPalletIndex && event.Name == txPaymentFeePaidEvent {
			fee, parseErr := uint64EventParam(event, actualFeeParam)
			if parseErr != nil {
				return nil, nil, parseErr
			}
			tip, parseErr := uint64EventParam(event, tipParam)
			if parseErr != nil {
				return nil, nil, parseErr
			}
			// The total includes the tip; reject invalid values before subtracting.
			if tip > fee {
				return nil, nil, fmt.Errorf("tip %d exceeds total fee %d for extrinsic %d", tip, fee, event.ExtrinsicIndex)
			}
			_, duplicate := fees[event.ExtrinsicIndex]
			if duplicate {
				return nil, nil, fmt.Errorf("duplicate fee event for extrinsic %d", event.ExtrinsicIndex)
			}
			fees[event.ExtrinsicIndex] = Fee{Total: fee, Tip: tip}
		}
	}

	return results, fees, nil
}

func uint64EventParam(event DecodedEvent, name string) (uint64, error) {
	for _, param := range event.Params {
		if param.Name != name {
			continue
		}
		switch value := param.Value.(type) {
		case uint64:
			return value, nil
		case uint32:
			return uint64(value), nil
		case uint16:
			return uint64(value), nil
		case int:
			if value < 0 {
				break
			}
			return uint64(value), nil
		case string:
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err == nil {
				return parsed, nil
			}
		}
		return 0, fmt.Errorf("event %s:%s parameter %s is not a uint64: %v", event.Pallet, event.Name, name, param.Value)
	}
	return 0, fmt.Errorf("event %s:%s has no %s parameter", event.Pallet, event.Name, name)
}
