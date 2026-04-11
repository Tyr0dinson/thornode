package types

import (
	"encoding/binary"
	"fmt"
)

// Known event data sizes for Substrate runtimes.
// Balance event field sizes depend on the pallet's configured Balance type,
// (u64 = 8 bytes on TAO, u128 = 16 bytes on DOT), passed via EventPalletConfig.BalanceSize.
// AccountId = 32 bytes, Hash = 32 bytes.
const (
	sizeAccountID = 32
	sizeHash      = 32
)

// dispatchErrorSize returns the byte size of a SCALE-encoded DispatchError
// given the first (variant) byte.
func dispatchErrorSize(variant byte) (int, error) {
	switch variant {
	case 0, 1, 2, 4, 5, 6, 10, 11, 12, 13:
		return 1, nil // just the variant byte, no extra data
	case 3:
		return 1 + 1 + 4, nil // Module { index: u8, error: [u8; 4] }
	case 7, 8, 9:
		return 1 + 1, nil // Token/Arithmetic/Transactional(sub-enum: 1 byte)
	default:
		return 0, fmt.Errorf("unknown DispatchError variant %d", variant)
	}
}

// dispatchErrorSizeEstimates returns a pessimistic size estimate for unknown
// DispatchError variants. Used by the scan fallback to match ExtrinsicFailed
// events even when new variants are added in runtime upgrades. Tries all known
// sizes (1, 2, 6 bytes) and returns them for the caller to attempt each.
func dispatchErrorSizeEstimates(variant byte) []int {
	known, err := dispatchErrorSize(variant)
	if err == nil {
		return []int{known}
	}
	// Unknown variant — return all plausible sizes to try.
	// Known sizes: 1 (unit), 2 (sub-enum), 6 (Module). Try each.
	return []int{1, 2, 6}
}

// EventPalletConfig holds pallet indices for event parsing.
type EventPalletConfig struct {
	SystemPalletIndex             byte
	BalancesPalletIndex           byte
	UtilityPalletIndex            byte
	TransactionPaymentPalletIndex byte
	BalanceSize                   int
}

// ParseExtrinsicResults parses SCALE-encoded System::Events and returns a map of
// extrinsic index → success (true) or failure (false). Extrinsics not present in
// the map could not be determined (parsing stopped at an unknown event).
func ParseExtrinsicResults(data []byte, pallets EventPalletConfig) (map[uint32]bool, map[uint32]uint64, error) {
	if len(data) == 0 {
		return nil, nil, nil
	}

	numEvents, consumed, err := DecodeCompactToInt(data)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode events count: %w", err)
	}
	pos := consumed

	results := make(map[uint32]bool)
	fees := make(map[uint32]uint64)

	for i := 0; i < numEvents; i++ {
		if pos >= len(data) {
			break
		}

		// Phase
		phaseVariant := data[pos]
		pos++
		var extrinsicIdx uint32
		isApplyExtrinsic := phaseVariant == 0x00

		if isApplyExtrinsic {
			if pos+4 > len(data) {
				return results, fees, fmt.Errorf("events data too short for extrinsic index at event %d", i)
			}
			extrinsicIdx = binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
		}
		// Phase::Finalization (0x01) and Phase::Initialization (0x02) have no extra data

		// Event ID: pallet_index + event_index
		if pos+2 > len(data) {
			return results, fees, fmt.Errorf("events data too short for event ID at event %d", i)
		}
		palletIdx := data[pos]
		eventIdx := data[pos+1]
		pos += 2

		// Determine event data size based on pallet and event
		var dataSize int
		dataSize, err = eventDataSize(palletIdx, eventIdx, data[pos:], pallets)
		if err != nil {
			// Unknown event — stop parsing. Return what we have.
			return results, fees, nil
		}

		if pos+dataSize > len(data) {
			return results, fees, fmt.Errorf("events data too short for event data at event %d (need %d, have %d)", i, dataSize, len(data)-pos)
		}

		// Record ExtrinsicSuccess / ExtrinsicFailed
		if isApplyExtrinsic && palletIdx == pallets.SystemPalletIndex {
			if eventIdx == 0x00 { // ExtrinsicSuccess
				results[extrinsicIdx] = true
			} else if eventIdx == 0x01 { // ExtrinsicFailed
				results[extrinsicIdx] = false
			}
		}

		sizeBalance := pallets.BalanceSize

		// Extract actual fee from TransactionPayment::TransactionFeePaid
		if isApplyExtrinsic && palletIdx == pallets.TransactionPaymentPalletIndex && eventIdx == 0x00 {
			if dataSize >= sizeAccountID+sizeBalance {
				fees[extrinsicIdx] = binary.LittleEndian.Uint64(data[pos+sizeAccountID : pos+sizeAccountID+sizeBalance])
			}
		}

		pos += dataSize

		// Topics: Vec<H256>
		if pos >= len(data) {
			return results, fees, fmt.Errorf("events data too short for topics at event %d", i)
		}

		var topicCount int
		topicCount, consumed, err = DecodeCompactToInt(data[pos:])
		if err != nil {
			return results, fees, nil
		}
		pos += consumed
		if topicCount > (len(data)-pos)/sizeHash {
			return results, fees, fmt.Errorf("events data too short for topic data at event %d", i)
		}
		pos += int(topicCount) * sizeHash
	}

	return results, fees, nil
}

// eventDataSize returns the byte size of event data for a given pallet/event.
// The remaining slice is provided for variable-size events (e.g., DispatchError).
func eventDataSize(palletIdx, eventIdx byte, remaining []byte, pallets EventPalletConfig) (int, error) {
	switch palletIdx {
	case pallets.SystemPalletIndex:
		return systemEventDataSize(eventIdx, remaining)
	case pallets.BalancesPalletIndex:
		return balancesEventDataSize(eventIdx, pallets.BalanceSize)
	case pallets.UtilityPalletIndex:
		return utilityEventDataSize(eventIdx, remaining)
	case pallets.TransactionPaymentPalletIndex:
		return txPaymentEventDataSize(eventIdx, pallets.BalanceSize)
	default:
		return 0, fmt.Errorf("unknown pallet %d", palletIdx)
	}
}

// decodeDispatchInfoSize reads the DispatchInfo from remaining bytes and returns
// the total byte size consumed. Substrate Weight v2 uses compact-encoded fields:
//
//	Weight { ref_time: Compact<u64>, proof_size: Compact<u64> }
func decodeDispatchInfoSize(remaining []byte) (int, error) {
	if len(remaining) < 4 {
		return 0, fmt.Errorf("insufficient data for DispatchInfo")
	}
	pos := 0

	// Compact ref_time
	_, n, err := DecodeCompact(remaining[pos:])
	if err != nil {
		return 0, fmt.Errorf("failed to decode ref_time: %w", err)
	}
	pos += n

	// Compact proof_size
	if pos >= len(remaining) {
		return 0, fmt.Errorf("insufficient data for proof_size")
	}
	_, n, err = DecodeCompact(remaining[pos:])
	if err != nil {
		return 0, fmt.Errorf("failed to decode proof_size: %w", err)
	}
	pos += n

	// DispatchClass (1 byte) + Pays (1 byte)
	if pos+2 > len(remaining) {
		return 0, fmt.Errorf("insufficient data for DispatchClass/Pays")
	}
	if remaining[pos] > 2 {
		return 0, fmt.Errorf("invalid DispatchClass: %d", remaining[pos])
	}
	if remaining[pos+1] > 1 {
		return 0, fmt.Errorf("invalid Pays: %d", remaining[pos+1])
	}
	pos += 2

	return pos, nil
}

// systemEventDataSize returns event data size for System pallet events.
func systemEventDataSize(eventIdx byte, remaining []byte) (int, error) {
	switch eventIdx {
	case 0x00: // ExtrinsicSuccess(DispatchInfo)
		return decodeDispatchInfoSize(remaining)
	case 0x01: // ExtrinsicFailed(DispatchError, DispatchInfo)
		if len(remaining) == 0 {
			return 0, fmt.Errorf("no data for ExtrinsicFailed")
		}
		deSize, err := dispatchErrorSize(remaining[0])
		if err != nil {
			return 0, err
		}
		if deSize > len(remaining) {
			return 0, fmt.Errorf("insufficient data for DispatchError")
		}
		diSize, err := decodeDispatchInfoSize(remaining[deSize:])
		if err != nil {
			return 0, err
		}
		return deSize + diSize, nil
	case 0x02: // CodeUpdated
		return 0, nil
	case 0x03: // NewAccount(AccountId)
		return sizeAccountID, nil
	case 0x04: // KilledAccount(AccountId)
		return sizeAccountID, nil
	case 0x05: // Remarked(AccountId, Hash)
		return sizeAccountID + sizeHash, nil
	case 0x06: // UpgradeAuthorized { code_hash: Hash, check_version: bool }
		return sizeHash + 1, nil
	default:
		return 0, fmt.Errorf("unknown System event %d", eventIdx)
	}
}

// balancesEventDataSize returns event data size for Balances pallet events.
// sizeBalance reflects the chain's Balance type (8 for u64, 16 for u128).
func balancesEventDataSize(eventIdx byte, sizeBalance int) (int, error) {
	switch eventIdx {
	case 0: // Endowed { account, free_balance }
		return sizeAccountID + sizeBalance, nil
	case 1: // DustLost { account, amount }
		return sizeAccountID + sizeBalance, nil
	case 2: // Transfer { from, to, amount }
		return sizeAccountID + sizeAccountID + sizeBalance, nil
	case 3: // BalanceSet { who, free }
		return sizeAccountID + sizeBalance, nil
	case 4: // Reserved { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 5: // Unreserved { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 6: // ReserveRepatriated { from, to, amount, destination_status }
		return sizeAccountID + sizeAccountID + sizeBalance + 1, nil
	case 7: // Deposit { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 8: // Withdraw { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 9: // Slashed { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 10: // Minted { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 11: // Burned { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 12: // Suspended { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 13: // Restored { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 14: // Upgraded { who }
		return sizeAccountID, nil
	case 15: // Issued { amount }
		return sizeBalance, nil
	case 16: // Rescinded { amount }
		return sizeBalance, nil
	case 17: // Locked { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 18: // Unlocked { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 19: // Frozen { who, amount }
		return sizeAccountID + sizeBalance, nil
	case 20: // Thawed { who, amount }
		return sizeAccountID + sizeBalance, nil
	default:
		return 0, fmt.Errorf("unknown Balances event %d", eventIdx)
	}
}

// utilityEventDataSize returns event data size for Utility pallet events.
func utilityEventDataSize(eventIdx byte, remaining []byte) (int, error) {
	switch eventIdx {
	case 0: // BatchInterrupted { index: u32, error: DispatchError }
		if len(remaining) < 5 { // 4 bytes u32 + at least 1 byte DispatchError
			return 0, fmt.Errorf("insufficient data for BatchInterrupted")
		}
		deSize, err := dispatchErrorSize(remaining[4])
		if err != nil {
			return 0, err
		}
		return 4 + deSize, nil
	case 1: // BatchCompleted
		return 0, nil
	case 2: // BatchCompletedWithErrors
		return 0, nil
	case 3: // ItemCompleted
		return 0, nil
	case 4: // ItemFailed { error: DispatchError }
		if len(remaining) == 0 {
			return 0, fmt.Errorf("insufficient data for ItemFailed")
		}
		return dispatchErrorSize(remaining[0])
	case 5: // DispatchedAs { result: DispatchResult }
		// DispatchResult = Result<(), DispatchError>
		// Ok = 0x00 (1 byte), Err = 0x01 + DispatchError
		if len(remaining) == 0 {
			return 0, fmt.Errorf("insufficient data for DispatchedAs")
		}
		if remaining[0] == 0x00 {
			return 1, nil // Ok
		}
		if len(remaining) < 2 {
			return 0, fmt.Errorf("insufficient data for DispatchedAs error")
		}
		deSize, err := dispatchErrorSize(remaining[1])
		if err != nil {
			return 0, err
		}
		return 1 + deSize, nil
	default:
		return 0, fmt.Errorf("unknown Utility event %d", eventIdx)
	}
}

// txPaymentEventDataSize returns event data size for TransactionPayment pallet events.
func txPaymentEventDataSize(eventIdx byte, sizeBalance int) (int, error) {
	switch eventIdx {
	case 0: // TransactionFeePaid { who: AccountId, actual_fee: Balance, tip: Balance }
		return sizeAccountID + sizeBalance + sizeBalance, nil
	default:
		return 0, fmt.Errorf("unknown TransactionPayment event %d", eventIdx)
	}
}

// ScanExtrinsicResults performs a pattern-based scan of raw event data to find
// System::ExtrinsicSuccess and System::ExtrinsicFailed events. This is a fallback
// for when the linear parser stops at unknown events from unrecognized pallets
// (e.g., chain-specific pallets like SubtensorModule on TAO, or Staking on DOT).
// It searches for byte patterns matching these System events and validates
// them by decoding compact DispatchInfo fields.
func ScanExtrinsicResults(data []byte, pallets EventPalletConfig, numExtrinsics int) map[uint32]bool {
	results := make(map[uint32]bool)
	if len(data) == 0 || numExtrinsics <= 0 {
		return results
	}

	// Track success and failure matches separately. If both patterns match for
	// the same extrinsic, the match is ambiguous (likely a false positive from
	// unknown pallet event data) — we exclude it rather than risk a false credit.
	successes := make(map[uint32]bool)
	failures := make(map[uint32]bool)

	// Minimum match: phase(1) + extIdx(4) + palletIdx(1) + eventIdx(1) + DispatchInfo(min 4) + topics(1)
	minMatch := 12

	for pos := 0; pos+minMatch <= len(data); pos++ {
		// Phase must be ApplyExtrinsic (0x00)
		if data[pos] != 0x00 {
			continue
		}

		extIdx := binary.LittleEndian.Uint32(data[pos+1 : pos+5])
		if int(extIdx) >= numExtrinsics {
			continue
		}

		// Must be System pallet
		if data[pos+5] != pallets.SystemPalletIndex {
			continue
		}

		eventIdx := data[pos+6]
		diStart := pos + 7

		switch eventIdx {
		case 0x00: // ExtrinsicSuccess(DispatchInfo)
			diSize, err := decodeDispatchInfoSize(data[diStart:])
			if err != nil {
				continue
			}
			endPos := diStart + diSize
			if endPos >= len(data) || data[endPos] != 0x00 {
				continue
			}
			successes[extIdx] = true

		case 0x01: // ExtrinsicFailed(DispatchError, DispatchInfo)
			if diStart >= len(data) {
				continue
			}
			// Try all plausible DispatchError sizes. For known variants this
			// returns the exact size; for unknown variants it tries all known
			// sizes (1, 2, 6) to match ExtrinsicFailed even after runtime upgrades.
			for _, deSize := range dispatchErrorSizeEstimates(data[diStart]) {
				diOffset := diStart + deSize
				if diOffset >= len(data) {
					continue
				}
				diSize, err := decodeDispatchInfoSize(data[diOffset:])
				if err != nil {
					continue
				}
				endPos := diOffset + diSize
				if endPos >= len(data) || data[endPos] != 0x00 {
					continue
				}
				failures[extIdx] = true
				break // matched, no need to try other sizes
			}
		}
	}

	// Build results only for non-conflicting matches. If both success and failure
	// patterns matched for the same extIdx, exclude it (treat as undetermined).
	for extIdx := range successes {
		if !failures[extIdx] {
			results[extIdx] = true
		}
	}
	for extIdx := range failures {
		if !successes[extIdx] {
			results[extIdx] = false
		}
	}

	return results
}
