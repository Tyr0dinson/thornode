package haven

import (
	"encoding/hex"
	"fmt"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	monerocrypto "github.com/haven-protocol-org/monero-go-utils/crypto"
	"gitlab.com/thorchain/thornode/common"
)

func GetCryptonoteFromPrivKey(privKey string) (string, error) {
	// get the priv spend key
	var keyHash, privSpendKey, privViewKey, pubSpendKey [32]byte

	privB, err := hex.DecodeString(privKey)
	if err != nil {
		return "", fmt.Errorf("fail to decode priv key to bytes: %w", err)
	}
	hash := monerocrypto.NewHash()
	hash.Write(privB)
	hash.Sum(keyHash[:0])
	monerocrypto.SecretFromSeed(&privSpendKey, &keyHash)
	if !monerocrypto.CheckSecret(&privSpendKey) {
		return "", fmt.Errorf("ed25519 PrivKey is invalid, key: %s", hex.EncodeToString(privSpendKey[:]))
	}

	// generate cryonote priv/pub keys
	monerocrypto.PublicFromSecret(&pubSpendKey, &privSpendKey)
	monerocrypto.ViewFromSpend(&privViewKey, &privSpendKey)

	// generate cryptonote data
	var data []byte
	data = append(data, privViewKey[:]...)
	data = append(data, pubSpendKey[:]...)
	return hex.EncodeToString(data), nil
}

// getHavenPrivateKey contructs a private key from a thorchain private key
func getHavenPrivateKey(key cryptotypes.PrivKey) ([32]byte, [32]byte) {
	// prepare seed
	h := monerocrypto.NewHash()
	var keyHash [32]byte
	h.Write(key.Bytes())
	h.Sum(keyHash[:0])

	// generate the secret keys
	var secretSpendKey [32]byte
	var secretViewKey [32]byte
	monerocrypto.SecretFromSeed(&secretSpendKey, &keyHash)
	monerocrypto.ViewFromSpend(&secretViewKey, &secretSpendKey)
	return secretViewKey, secretSpendKey
}

func generateWalletAddress(privSpendKey *[32]byte, privViewKey *[32]byte) (common.Address, error) {
	// generate pubspendKey
	var pubSpendKey [32]byte
	monerocrypto.PublicFromSecret(&pubSpendKey, privSpendKey)

	// generate the cryptonote data
	var cnData []byte
	cnData = append(cnData, privViewKey[:]...)
	cnData = append(cnData, pubSpendKey[:]...)
	walletAddr, err := common.PubKey(hex.EncodeToString(cnData)).GetAddress(common.XHVChain)
	return walletAddr, err
}
