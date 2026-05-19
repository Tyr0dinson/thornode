package evm

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func GetPrivateKey(key cryptotypes.PrivKey) (*ecdsa.PrivateKey, error) {
	return ecrypto.ToECDSA(key.Bytes())
}

// KeySignWrapper is a wrap of private key and also tss instance
type KeySignWrapper struct {
	privKey       *ecdsa.PrivateKey
	pubKey        common.PubKey
	tssKeyManager tss.ThorchainKeyManager
	logger        zerolog.Logger
	signer        etypes.Signer
}

// NewKeySignWrapper create a new instance of keysign wrapper
func NewKeySignWrapper(privateKey *ecdsa.PrivateKey, pubKey common.PubKey, keyManager tss.ThorchainKeyManager, chainID *big.Int, chain string) (*KeySignWrapper, error) {
	return &KeySignWrapper{
		privKey:       privateKey,
		pubKey:        pubKey,
		tssKeyManager: keyManager,
		signer:        etypes.NewLondonSigner(chainID),
		logger:        log.With().Str("module", "signer").Str("chain", chain).Logger(),
	}, nil
}

// GetPrivKey return the private key
func (w *KeySignWrapper) GetPrivKey() *ecdsa.PrivateKey {
	return w.privKey
}

// GetPubKey return the public key
func (w *KeySignWrapper) GetPubKey() common.PubKey {
	return w.pubKey
}

// Sign signs the given transaction either with the local private key or, when
// the transaction belongs to an Asgard vault, through TSS using the supplied
// signing intent.
func (w *KeySignWrapper) Sign(tx *etypes.Transaction, poolPubKey common.PubKey, intent tss.SigningIntent) ([]byte, error) {
	if tx == nil {
		return nil, errors.New("tx is nil")
	}
	if poolPubKey.IsEmpty() {
		return nil, errors.New("pool public key is empty")
	}
	var err error
	var sig []byte
	if w.pubKey.Equals(poolPubKey) {
		sig, err = w.sign(tx)
	} else {
		sig, err = w.signTSS(tx, poolPubKey.String(), intent)
	}
	if err != nil {
		return nil, fmt.Errorf("fail to sign tx: %w", err)
	}
	newTx, err := tx.WithSignature(w.signer, sig)
	if err != nil {
		return nil, fmt.Errorf("fail to apply signature to tx: %w", err)
	}
	enc, err := newTx.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("fail to marshal tx to json: %w", err)
	}
	return enc, nil
}

func (w *KeySignWrapper) sign(tx *etypes.Transaction) ([]byte, error) {
	hash := w.signer.Hash(tx)
	return ecrypto.Sign(hash[:], w.privKey)
}

// signTSS delegates the EVM transaction hash to TSS. Authorization happens
// inside ThorchainKeyManager.RemoteSign using the caller-supplied intent.
func (w *KeySignWrapper) signTSS(tx *etypes.Transaction, poolPubKey string, intent tss.SigningIntent) ([]byte, error) {
	pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, poolPubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get pub key: %w", err)
	}

	hash := w.signer.Hash(tx)
	if intent.Type == tss.SigningIntentEVMUnstuck {
		// Unstuck signs a replacement transaction that is intentionally not the
		// original TxOut transaction. Validate the replacement shape here while
		// the full EVM transaction is still available, then bind its exact hash
		// into the intent sent to RemoteSign.
		if err = validateEVMUnstuckTx(tx, poolPubKey, intent); err != nil {
			return nil, err
		}
		intent = intent.WithMessageHash(hash[:])
	}
	// Pass the TxOut intent through with the raw transaction hash so TSS can
	// reject hashes that are not backed by the current /keysign payload.
	sig, recovery, err := w.tssKeyManager.RemoteSign(hash[:], common.SigningAlgoSecp256k1, poolPubKey, intent)
	if errors.Is(err, tss.ErrNotSelectedForKeysign) || (err == nil && sig == nil) {
		return nil, tss.ErrNotSelectedForKeysign
	}
	if err != nil {
		return nil, fmt.Errorf("fail to TSS sign: %w", err)
	}
	secpPubKey, err := codec.ToCmtPubKeyInterface(pk)
	if err != nil {
		return nil, err
	}
	if ecrypto.VerifySignature(secpPubKey.Bytes(), hash[:], sig) {
		w.logger.Info().Msg("we can successfully verify the bytes")
	} else {
		w.logger.Error().Msg("Oops! we cannot verify the bytes")
	}
	// add the recovery id at the end
	result := make([]byte, 65)
	copy(result, sig)
	result[64] = recovery[0]
	return result, nil
}

// validateEVMUnstuckTx keeps the unstuck exception narrow: only a gas-only
// replacement transaction that sends zero value back to the vault address can
// use SigningIntentEVMUnstuck.
func validateEVMUnstuckTx(tx *etypes.Transaction, poolPubKey string, intent tss.SigningIntent) error {
	if !intent.TxOut.Chain.IsEVM() {
		return fmt.Errorf("evm unstuck signing intent must use an EVM chain")
	}
	if intent.TxOut.VaultPubKey.String() != poolPubKey {
		return fmt.Errorf("evm unstuck pool pubkey does not match txout vault")
	}
	to := tx.To()
	if to == nil {
		return fmt.Errorf("evm unstuck replacement tx must have a recipient")
	}
	if tx.Value() == nil || tx.Value().Sign() != 0 {
		return fmt.Errorf("evm unstuck replacement tx must have zero value")
	}
	if len(tx.Data()) != 0 {
		return fmt.Errorf("evm unstuck replacement tx must not contain calldata")
	}
	vaultAddress, err := intent.TxOut.VaultPubKey.GetAddress(intent.TxOut.Chain)
	if err != nil {
		return fmt.Errorf("fail to derive evm unstuck vault address: %w", err)
	}
	if !strings.EqualFold(to.Hex(), vaultAddress.String()) {
		return fmt.Errorf("evm unstuck replacement tx must send to the vault address")
	}
	return nil
}
