package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/binance-chain/tss-lib/ecdsa/keygen"
	"github.com/tendermint/btcd/btcec"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// shareData holds a keyshare with its index
type shareData struct {
	xi    *big.Int
	index *big.Int // ShareID is the party index
}

// reconstructPrivateKey reconstructs the private key from multiple TSS shares using Lagrange interpolation
func reconstructPrivateKey(shares []keygen.LocalPartySaveData, threshold int) (*big.Int, error) {
	if len(shares) < threshold+1 {
		return nil, fmt.Errorf("insufficient shares: have %d, need at least %d", len(shares), threshold+1)
	}

	// Get the curve order
	curve := btcec.S256()
	curveOrder := curve.N

	// Extract Xi values and indices
	shareList := make([]shareData, 0, len(shares))

	for _, share := range shares {
		if share.ShareID == nil {
			return nil, fmt.Errorf("share missing ShareID")
		}
		if share.Xi == nil {
			return nil, fmt.Errorf("share missing Xi")
		}

		// ShareID is the party index (x-coordinate in the polynomial)
		shareList = append(shareList, shareData{
			xi:    share.Xi,
			index: share.ShareID,
		})
	}

	// Use only the first threshold+1 shares
	if len(shareList) > threshold+1 {
		shareList = shareList[:threshold+1]
	}

	// Reconstruct using Lagrange interpolation
	// For threshold cryptography, we compute: sum(xi * L_i(0)) mod n
	// where L_i(0) is the Lagrange coefficient for party i at point 0
	privateKey := big.NewInt(0)

	for i, share := range shareList {
		// Compute Lagrange coefficient L_i(0)
		lagrangeCoeff := computeLagrangeCoefficient(i, shareList, big.NewInt(0), curveOrder)

		// Multiply share by Lagrange coefficient
		term := new(big.Int).Mul(share.xi, lagrangeCoeff)
		term.Mod(term, curveOrder)

		// Add to private key
		privateKey.Add(privateKey, term)
		privateKey.Mod(privateKey, curveOrder)
	}

	return privateKey, nil
}

// computeLagrangeCoefficient computes the Lagrange coefficient L_i(x) for party at index i
func computeLagrangeCoefficient(i int, shares []shareData, x *big.Int, mod *big.Int) *big.Int {
	result := big.NewInt(1)
	xi := shares[i].index

	for j, share := range shares {
		if i == j {
			continue
		}
		xj := share.index

		// numerator: (x - xj)
		num := new(big.Int).Sub(x, xj)
		num.Mod(num, mod)

		// denominator: (xi - xj)
		den := new(big.Int).Sub(xi, xj)
		den.Mod(den, mod)

		// Compute modular inverse of denominator
		denInv := new(big.Int).ModInverse(den, mod)

		// Multiply: result * (x - xj) * inv(xi - xj)
		term := new(big.Int).Mul(num, denInv)
		term.Mod(term, mod)
		result.Mul(result, term)
		result.Mod(result, mod)
	}

	return result
}

func loadKeyshareFile(filepath string) (storage.KeygenLocalState, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return storage.KeygenLocalState{}, fmt.Errorf("failed to read file: %w", err)
	}

	var state storage.KeygenLocalState
	if err := json.Unmarshal(data, &state); err != nil {
		return storage.KeygenLocalState{}, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return state, nil
}

func extractLocalPartySaveData(state storage.KeygenLocalState) (keygen.LocalPartySaveData, error) {
	var localData keygen.LocalPartySaveData
	if err := json.Unmarshal(state.LocalData, &localData); err != nil {
		return keygen.LocalPartySaveData{}, fmt.Errorf("failed to unmarshal LocalData: %w", err)
	}

	// Validate the data
	if !localData.ValidateWithProof() {
		return keygen.LocalPartySaveData{}, fmt.Errorf("invalid keygen saved data")
	}

	return localData, nil
}

func main() {
	var (
		keyshareDir = flag.String("dir", ".", "Directory containing keyshare files (localstate-*.json)")
		outputFile  = flag.String("output", "", "Output file for private key (default: stdout)")
		format      = flag.String("format", "hex", "Output format: hex, wif, or raw")
	)
	flag.Parse()

	// Find all keyshare files
	pattern := filepath.Join(*keyshareDir, "localstate-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding keyshare files: %v\n", err)
		os.Exit(1)
	}

	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "No keyshare files found matching pattern: %s\n", pattern)
		fmt.Fprintf(os.Stderr, "Expected files named: localstate-<pubkey>.json\n")
		os.Exit(1)
	}

	fmt.Printf("Found %d keyshare file(s)\n", len(matches))

	// Load all keyshares
	states := make([]storage.KeygenLocalState, 0, len(matches))
	shares := make([]keygen.LocalPartySaveData, 0, len(matches))

	for _, filepath := range matches {
		state, err := loadKeyshareFile(filepath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", filepath, err)
			continue
		}

		localData, err := extractLocalPartySaveData(state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to extract data from %s: %v\n", filepath, err)
			continue
		}

		states = append(states, state)
		shares = append(shares, localData)
		fmt.Printf("  Loaded: %s (party: %s)\n", filepath, state.LocalPartyKey)
	}

	if len(shares) == 0 {
		fmt.Fprintf(os.Stderr, "No valid keyshares loaded\n")
		os.Exit(1)
	}

	// Check if all shares are from the same keygen (same participant set)
	if len(states) > 0 {
		firstParticipants := states[0].ParticipantKeys
		for i, state := range states {
			if len(state.ParticipantKeys) != len(firstParticipants) {
				fmt.Fprintf(os.Stderr, "Warning: keyshare %d has different participant count\n", i)
			}
		}
	}

	// Calculate threshold
	participantCount := len(states[0].ParticipantKeys)
	threshold, err := conversion.GetThreshold(participantCount)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error calculating threshold: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nKeygen Parameters:\n")
	fmt.Printf("  Participants: %d\n", participantCount)
	fmt.Printf("  Threshold: %d\n", threshold)
	fmt.Printf("  Shares loaded: %d\n", len(shares))
	fmt.Printf("  Required: %d\n", threshold+1)

	// Check if we have enough shares
	if len(shares) < threshold+1 {
		fmt.Fprintf(os.Stderr, "\nERROR: Insufficient shares to reconstruct private key\n")
		fmt.Fprintf(os.Stderr, "  Have: %d\n", len(shares))
		fmt.Fprintf(os.Stderr, "  Need: %d\n", threshold+1)
		os.Exit(1)
	}

	// Reconstruct private key
	fmt.Printf("\nReconstructing private key...\n")
	privateKey, err := reconstructPrivateKey(shares, threshold)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reconstructing private key: %v\n", err)
		os.Exit(1)
	}

	// Format output
	var output string
	switch *format {
	case "hex":
		output = hex.EncodeToString(privateKey.Bytes())
	case "wif":
		// WIF format for Bitcoin/UTXO chains
		privKeyBytes := privateKey.Bytes()
		// Pad to 32 bytes if needed
		if len(privKeyBytes) < 32 {
			padded := make([]byte, 32)
			copy(padded[32-len(privKeyBytes):], privKeyBytes)
			privKeyBytes = padded
		}
		// Create WIF (simplified - actual WIF includes version and checksum)
		output = hex.EncodeToString(privKeyBytes)
	case "raw":
		output = hex.EncodeToString(privateKey.Bytes())
	default:
		fmt.Fprintf(os.Stderr, "Unknown format: %s (use: hex, wif, or raw)\n", *format)
		os.Exit(1)
	}

	// Verify the private key by deriving public key
	curve := btcec.S256()
	privKey, pubKey := btcec.PrivKeyFromBytes(curve, privateKey.Bytes())
	_ = privKey // unused but validates the key

	// Get expected public key from first share
	expectedPubKey := shares[0].ECDSAPub
	if expectedPubKey == nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not verify public key (missing in share data)\n")
	} else {
		// Compare public keys
		reconstructedX, reconstructedY := pubKey.X, pubKey.Y
		expectedX, expectedY := expectedPubKey.X(), expectedPubKey.Y()

		if reconstructedX.Cmp(expectedX) == 0 && reconstructedY.Cmp(expectedY) == 0 {
			fmt.Printf("✓ Private key verified (public key matches)\n")
		} else {
			fmt.Fprintf(os.Stderr, "WARNING: Public key mismatch!\n")
			fmt.Fprintf(os.Stderr, "  Expected: (%s, %s)\n", expectedX.String(), expectedY.String())
			fmt.Fprintf(os.Stderr, "  Got:      (%s, %s)\n", reconstructedX.String(), reconstructedY.String())
			fmt.Fprintf(os.Stderr, "  This may indicate an error in reconstruction\n")
		}
	}

	// Output the private key
	if *outputFile != "" {
		err := os.WriteFile(*outputFile, []byte(output+"\n"), 0600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nPrivate key written to: %s\n", *outputFile)
		fmt.Printf("WARNING: Keep this file secure and delete it after use!\n")
	} else {
		fmt.Printf("\nPrivate Key (%s):\n", *format)
		fmt.Printf("%s\n", output)
		fmt.Printf("\nWARNING: This is a sensitive private key. Keep it secure!\n")
	}

	// Additional info
	fmt.Printf("\nAdditional Information:\n")
	fmt.Printf("  Curve: secp256k1\n")
	fmt.Printf("  Public Key (compressed): %s\n", hex.EncodeToString(pubKey.SerializeCompressed()))
	fmt.Printf("  Public Key (uncompressed): %s\n", hex.EncodeToString(pubKey.SerializeUncompressed()))

	// Generate addresses for all supported chains
	fmt.Printf("\nChain Addresses:\n")
	fmt.Printf("  (THORNode uses the same public key for all chains, with chain-specific encoding)\n\n")

	// Configure bech32 prefixes based on network
	config := cosmos.GetConfig()
	switch common.CurrentChainNetwork {
	case common.MockNet:
		config.SetBech32PrefixForAccount("tthor", "tthorpub")
		config.SetBech32PrefixForValidator("tthorv", "tthorvpub")
		config.SetBech32PrefixForConsensusNode("tthorc", "tthorcpub")
	case common.MainNet:
		config.SetBech32PrefixForAccount("thor", "thorpub")
		config.SetBech32PrefixForValidator("thorv", "thorvpub")
		config.SetBech32PrefixForConsensusNode("thorc", "thorcpub")
	}
	config.Seal()

	// Get the THORChain public key from the first share
	if len(shares) > 0 && shares[0].ECDSAPub != nil {
		// Use conversion helper to get THORChain pubkey
		thorPubKeyStr, _, err := conversion.GetTssPubKeyECDSA(shares[0].ECDSAPub)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not convert pubkey: %v\n", err)
			fmt.Fprintf(os.Stderr, "  You can manually derive addresses using the pubkey2address tool\n")
		} else {
			thorPubKey, err := common.NewPubKey(thorPubKeyStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Could not create PubKey: %v\n", err)
			} else {
				// Generate addresses for all chains
				generateChainAddresses(thorPubKey)
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "Warning: Could not extract public key from shares\n")
		fmt.Fprintf(os.Stderr, "  You can manually derive addresses using the pubkey2address tool\n")
	}
}

// generateChainAddresses generates addresses for all supported chains
func generateChainAddresses(pubKey common.PubKey) {
	// Get all chains
	allChains := common.AllChains

	for _, chain := range allChains {
		addr, err := pubKey.GetAddress(chain)
		if err != nil {
			fmt.Printf("  %-12s: ERROR - %v\n", chain.String(), err)
			continue
		}
		if addr.IsEmpty() {
			fmt.Printf("  %-12s: (not supported)\n", chain.String())
			continue
		}
		fmt.Printf("  %-12s: %s\n", chain.String(), addr.String())
	}
}
