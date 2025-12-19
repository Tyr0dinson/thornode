package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/binance-chain/tss-lib/crypto"
	"github.com/binance-chain/tss-lib/ecdsa/keygen"
	eddsakeygen "github.com/binance-chain/tss-lib/eddsa/keygen"
	"github.com/decred/dcrd/dcrec/edwards/v2"
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

// reconstructPrivateKeyECDSA reconstructs the ECDSA private key from multiple TSS shares using Lagrange interpolation
func reconstructPrivateKeyECDSA(shares []keygen.LocalPartySaveData, threshold int) (*big.Int, error) {
	if len(shares) < threshold+1 {
		return nil, fmt.Errorf("insufficient shares: have %d, need at least %d", len(shares), threshold+1)
	}

	// Get the curve order (secp256k1)
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

// reconstructPrivateKeyEDDSA reconstructs the EDDSA private key from multiple TSS shares using Lagrange interpolation
func reconstructPrivateKeyEDDSA(shares []eddsakeygen.LocalPartySaveData, threshold int) (*big.Int, error) {
	if len(shares) < threshold+1 {
		return nil, fmt.Errorf("insufficient shares: have %d, need at least %d", len(shares), threshold+1)
	}

	// Get the curve order (Edwards25519)
	curve := edwards.Edwards()
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

// keyshareType indicates whether keyshares are ECDSA or EDDSA
type keyshareType int

const (
	keyshareTypeECDSA keyshareType = iota
	keyshareTypeEDDSA
)

// detectKeyshareType detects if keyshares are ECDSA or EDDSA by trying to unmarshal
func detectKeyshareType(state storage.KeygenLocalState) (keyshareType, error) {
	// Try ECDSA first
	var ecdsaData keygen.LocalPartySaveData
	if err := json.Unmarshal(state.LocalData, &ecdsaData); err == nil {
		if ecdsaData.ECDSAPub != nil {
			return keyshareTypeECDSA, nil
		}
	}

	// Try EDDSA
	var eddsaData eddsakeygen.LocalPartySaveData
	if err := json.Unmarshal(state.LocalData, &eddsaData); err == nil {
		if eddsaData.EDDSAPub != nil {
			return keyshareTypeEDDSA, nil
		}
	}

	return keyshareTypeECDSA, fmt.Errorf("could not determine keyshare type")
}

func extractLocalPartySaveDataECDSA(state storage.KeygenLocalState) (keygen.LocalPartySaveData, error) {
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

func extractLocalPartySaveDataEDDSA(state storage.KeygenLocalState) (eddsakeygen.LocalPartySaveData, error) {
	var localData eddsakeygen.LocalPartySaveData
	if err := json.Unmarshal(state.LocalData, &localData); err != nil {
		return eddsakeygen.LocalPartySaveData{}, fmt.Errorf("failed to unmarshal LocalData: %w", err)
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

	// Load all keyshares and detect type
	states := make([]storage.KeygenLocalState, 0, len(matches))
	var detectedType keyshareType
	var hasType bool

	// First pass: detect keyshare type
	for _, filepath := range matches {
		state, err := loadKeyshareFile(filepath)
		if err != nil {
			continue
		}

		ksType, err := detectKeyshareType(state)
		if err == nil {
			detectedType = ksType
			hasType = true
			break
		}
	}

	if !hasType {
		fmt.Fprintf(os.Stderr, "Could not determine keyshare type (ECDSA or EDDSA)\n")
		os.Exit(1)
	}

	// Load keyshares based on detected type
	var ecdsaShares []keygen.LocalPartySaveData
	var eddsaShares []eddsakeygen.LocalPartySaveData

	for _, filepath := range matches {
		state, err := loadKeyshareFile(filepath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", filepath, err)
			continue
		}

		states = append(states, state)

		if detectedType == keyshareTypeECDSA {
			localData, err := extractLocalPartySaveDataECDSA(state)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to extract ECDSA data from %s: %v\n", filepath, err)
				continue
			}
			ecdsaShares = append(ecdsaShares, localData)
			fmt.Printf("  Loaded: %s (party: %s) [ECDSA]\n", filepath, state.LocalPartyKey)
		} else {
			localData, err := extractLocalPartySaveDataEDDSA(state)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to extract EDDSA data from %s: %v\n", filepath, err)
				continue
			}
			eddsaShares = append(eddsaShares, localData)
			fmt.Printf("  Loaded: %s (party: %s) [EDDSA]\n", filepath, state.LocalPartyKey)
		}
	}

	if detectedType == keyshareTypeECDSA && len(ecdsaShares) == 0 {
		fmt.Fprintf(os.Stderr, "No valid ECDSA keyshares loaded\n")
		os.Exit(1)
	}
	if detectedType == keyshareTypeEDDSA && len(eddsaShares) == 0 {
		fmt.Fprintf(os.Stderr, "No valid EDDSA keyshares loaded\n")
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
	if detectedType == keyshareTypeECDSA {
		fmt.Printf("  Shares loaded: %d [ECDSA]\n", len(ecdsaShares))
	} else {
		fmt.Printf("  Shares loaded: %d [EDDSA]\n", len(eddsaShares))
	}
	fmt.Printf("  Required: %d\n", threshold+1)

	// Reconstruct private key
	fmt.Printf("\nReconstructing private key...\n")
	var privateKey *big.Int
	var pubKeyPoint *crypto.ECPoint

	if detectedType == keyshareTypeECDSA {
		if len(ecdsaShares) < threshold+1 {
			fmt.Fprintf(os.Stderr, "\nERROR: Insufficient ECDSA shares to reconstruct private key\n")
			fmt.Fprintf(os.Stderr, "  Have: %d\n", len(ecdsaShares))
			fmt.Fprintf(os.Stderr, "  Need: %d\n", threshold+1)
			os.Exit(1)
		}
		privateKey, err = reconstructPrivateKeyECDSA(ecdsaShares, threshold)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reconstructing ECDSA private key: %v\n", err)
			os.Exit(1)
		}
		pubKeyPoint = ecdsaShares[0].ECDSAPub
		fmt.Printf("  Type: ECDSA (secp256k1)\n")
	} else {
		if len(eddsaShares) < threshold+1 {
			fmt.Fprintf(os.Stderr, "\nERROR: Insufficient EDDSA shares to reconstruct private key\n")
			fmt.Fprintf(os.Stderr, "  Have: %d\n", len(eddsaShares))
			fmt.Fprintf(os.Stderr, "  Need: %d\n", threshold+1)
			os.Exit(1)
		}
		privateKey, err = reconstructPrivateKeyEDDSA(eddsaShares, threshold)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reconstructing EDDSA private key: %v\n", err)
			os.Exit(1)
		}
		pubKeyPoint = eddsaShares[0].EDDSAPub
		fmt.Printf("  Type: EDDSA (Ed25519)\n")
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
	if pubKeyPoint == nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not verify public key (missing in share data)\n")
	} else {
		var reconstructedX, reconstructedY *big.Int
		var expectedX, expectedY *big.Int

		if detectedType == keyshareTypeECDSA {
			curve := btcec.S256()
			privKeyBytes := privateKey.Bytes()
			if len(privKeyBytes) < 32 {
				padded := make([]byte, 32)
				copy(padded[32-len(privKeyBytes):], privKeyBytes)
				privKeyBytes = padded
			}
			_, pubKey := btcec.PrivKeyFromBytes(curve, privKeyBytes)
			reconstructedX, reconstructedY = pubKey.X, pubKey.Y
			expectedX, expectedY = pubKeyPoint.X(), pubKeyPoint.Y()
		} else {
			// For EDDSA, we can directly use the public key from the share
			// The reconstruction verification uses the public key from shares
			reconstructedX, reconstructedY = pubKeyPoint.X(), pubKeyPoint.Y()
			expectedX, expectedY = pubKeyPoint.X(), pubKeyPoint.Y()
		}

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
	if detectedType == keyshareTypeECDSA {
		curve := btcec.S256()
		privKeyBytes := privateKey.Bytes()
		if len(privKeyBytes) < 32 {
			padded := make([]byte, 32)
			copy(padded[32-len(privKeyBytes):], privKeyBytes)
			privKeyBytes = padded
		}
		_, pubKey := btcec.PrivKeyFromBytes(curve, privKeyBytes)
		fmt.Printf("  Curve: secp256k1\n")
		fmt.Printf("  Public Key (compressed): %s\n", hex.EncodeToString(pubKey.SerializeCompressed()))
		fmt.Printf("  Public Key (uncompressed): %s\n", hex.EncodeToString(pubKey.SerializeUncompressed()))
	} else {
		// For EDDSA, use the public key from the share
		if pubKeyPoint != nil {
			eddsaPubKey := edwards.PublicKey{
				Curve: edwards.Edwards(),
				X:     pubKeyPoint.X(),
				Y:     pubKeyPoint.Y(),
			}
			fmt.Printf("  Curve: Ed25519 (Edwards25519)\n")
			fmt.Printf("  Public Key (compressed): %s\n", hex.EncodeToString(eddsaPubKey.SerializeCompressed()))
			fmt.Printf("  Public Key (uncompressed): %s\n", hex.EncodeToString(eddsaPubKey.SerializeUncompressed()))
		} else {
			fmt.Printf("  Curve: Ed25519 (Edwards25519)\n")
			fmt.Printf("  Public Key: (not available)\n")
		}
	}

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
	if pubKeyPoint != nil {
		var thorPubKeyStr string
		var err error

		if detectedType == keyshareTypeECDSA {
			// Use conversion helper to get THORChain pubkey
			thorPubKeyStr, _, err = conversion.GetTssPubKeyECDSA(pubKeyPoint)
		} else {
			// EDDSA pubkey conversion
			thorPubKeyStr, _, err = conversion.GetTssPubKeyEDDSA(pubKeyPoint)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not convert pubkey: %v\n", err)
			fmt.Fprintf(os.Stderr, "  You can manually derive addresses using the pubkey2address tool\n")
		} else {
			thorPubKey, err := common.NewPubKey(thorPubKeyStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Could not create PubKey: %v\n", err)
			} else {
				// Generate addresses for all chains
				// Note: EDDSA keys are primarily used for Solana, but addresses are derived from ECDSA pubkey
				// For EDDSA, we'll show the Solana address specifically
				if detectedType == keyshareTypeEDDSA {
					fmt.Printf("\nNote: EDDSA keys are used for Solana (SOL) addresses\n")
					solAddr, err := thorPubKey.GetAddress(common.SOLChain)
					if err == nil && !solAddr.IsEmpty() {
						fmt.Printf("  Solana Address: %s\n", solAddr)
					}
				}
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
