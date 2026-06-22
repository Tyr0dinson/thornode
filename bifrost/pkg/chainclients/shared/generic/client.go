// Package generic provides shared implementation for TSS-capable chain
// clients. Concrete chain clients embed *Client to inherit key management,
// signing, the signer cache, and the standard THORChain bridge wiring,
// and implement chain-specific behavior (block scanning, tx construction,
// broadcast) on top.
package generic

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	thorconfig "gitlab.com/thorchain/thornode/v3/config"
	thormemo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// Client bundles the shared state needed by TSS-capable chain clients:
// config, bridge, logger, TSS signer, local signer, and signer cache.
// It is safe for concurrent use — all embedded managers are thread-safe.
type Client struct {
	logger             zerolog.Logger
	config             thorconfig.BifrostChainConfiguration
	bridge             thorclient.ThorchainBridge
	tssKeyManager      tss.ThorchainKeyManager
	localKeyManager    LocalKeyManager
	signerCacheManager *signercache.CacheManager
}

// NewClient wires up the TSS and local key managers and the signer cache
// over the given leveldb. Returns an error if any sub-component fails to
// initialize; the returned *Client is ready to Start.
func NewClient(
	logger zerolog.Logger,
	keys *thorclient.Keys,
	config thorconfig.BifrostChainConfiguration,
	tssServer *tssp.TssServer,
	bridge thorclient.ThorchainBridge,
	db *leveldb.DB,
) (*Client, error) {
	c := &Client{
		bridge: bridge,
		logger: logger,
		config: config,
	}

	var err error

	c.tssKeyManager, err = tss.NewKeySign(tssServer, bridge)
	if err != nil {
		return nil, fmt.Errorf("failed to create tss signer: %w", err)
	}

	c.localKeyManager, err = NewLocalKeyManager(keys, config.ChainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create local key manager: %w", err)
	}

	c.signerCacheManager, err = signercache.NewSignerCacheManager(db)
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache: %w", err)
	}

	return c, nil
}

// Start launches the TSS key manager. Not idempotent — call exactly once
// per Client; pair with Stop.
func (c *Client) Start() {
	c.tssKeyManager.Start()
}

// Stop shuts down the TSS key manager. Safe to call once after Start.
func (c *Client) Stop() {
	c.tssKeyManager.Stop()
}

// GetChain returns the chain this client serves.
func (c *Client) GetChain() common.Chain {
	return c.config.ChainID
}

// GetGasAsset returns the chain's native gas asset.
func (c *Client) GetGasAsset() common.Asset {
	return c.GetChain().GetGasAsset()
}

// GetConfig returns the bifrost chain configuration.
func (c *Client) GetConfig() thorconfig.BifrostChainConfiguration {
	return c.config
}

// GetBridge returns the THORChain bridge used.
func (c *Client) GetBridge() thorclient.ThorchainBridge {
	return c.bridge
}

// GetPubKey returns the local signer's public key.
func (c *Client) GetPubKey() common.PubKey {
	return c.localKeyManager.PubKey()
}

// Sign signs data for the vault identified by txOutItem.VaultPubKey.
// Routes to the local signer when the vault address matches this node's
// local key, otherwise to TSS. Verifies the resulting signature against
// the vault pubkey before returning. On TSS failure with blame, posts
// the blame to THORChain and returns the original error.
func (c *Client) Sign(
	data []byte,
	txOutItem types.TxOutItem,
	thorchainHeight int64,
) ([]byte, error) {
	var signature []byte

	vaultPubKey := txOutItem.GetVaultPubKey()
	vaultAddress, err := vaultPubKey.GetAddress(txOutItem.Chain)
	if err != nil {
		return nil, fmt.Errorf("failed to get vault address: %w", err)
	}

	if c.localKeyManager.Address().Equals(vaultAddress) {
		signature, err = c.localKeyManager.Sign(data)
		if err != nil {
			return nil, fmt.Errorf("failed to sign data: %w", err)
		}
	} else {
		algo := c.GetChain().GetSigningAlgo()

		var recovery []byte
		signature, recovery, err = c.tssKeyManager.RemoteSign(
			data,
			algo,
			vaultPubKey.String(),
			thorchainHeight,
		)

		if algo == common.SigningAlgoSecp256k1 && err == nil && signature != nil {
			signature = append(signature, recovery...)
		}
	}

	if err != nil {
		var keysignError tss.KeysignError
		if errors.As(err, &keysignError) {
			if len(keysignError.Blame.BlameNodes) == 0 {
				// TSS doesn't know which node to blame
				c.logger.Err(err).Msg("failed to sign tx")
				return nil, err
			}

			// forward the keysign blame to thorchain
			txId, errPostKeysignFail := c.bridge.PostKeysignFailure(
				keysignError.Blame,
				txOutItem.Height,
				txOutItem.Memo,
				txOutItem.Coins,
				txOutItem.GetVaultPubKey(),
			)
			if errPostKeysignFail != nil {
				return nil, multierror.Append(err, errPostKeysignFail)
			}
			c.logger.Info().
				Str("tx_id", txId.String()).
				Msg("post keysign failure to THORChain")
		}
		return nil, err
	}

	pubKey := txOutItem.GetVaultPubKey()
	algo := c.GetChain().GetSigningAlgo()
	switch algo {
	case common.SigningAlgoSecp256k1:
		err = verifySecp256k1(data, signature, pubKey)
	case common.SigningAlgoEd25519:
		err = verifyEd25519(data, signature, pubKey)
	default:
		err = fmt.Errorf("unsupported signing algo: %s", algo)
	}

	if err != nil {
		return nil, err
	}

	return signature, nil
}

// verifySecp256k1 checks that sig was produced by pub's secp256k1 key.
// msg must be a 32-byte digest — callers are responsible for hashing the
// payload, since Ecrecover silently returns wrong results otherwise.
func verifySecp256k1(msg, sig []byte, pub common.PubKey) error {
	if len(msg) != 32 {
		return fmt.Errorf("msg must be 32 byte, got: %d", len(msg))
	}

	sigPub, err := crypto.Ecrecover(msg, sig)
	if err != nil {
		return fmt.Errorf("failed to get public key from signature: %w", err)
	}

	secpPub, err := pub.Secp256K1()
	if err != nil {
		return fmt.Errorf("failed to convert public key: %w", err)
	}

	ecdsaPub := crypto.FromECDSAPub(secpPub.ToECDSA())

	if !bytes.Equal(sigPub, ecdsaPub) {
		return fmt.Errorf("invalid signature")
	}

	return nil
}

// verifyEd25519 checks that sig was produced by pub's ed25519 key.
func verifyEd25519(msg, sig []byte, pub common.PubKey) error {
	pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, pub.String())
	if err != nil {
		return fmt.Errorf("failed to get public key: %w", err)
	}

	pubBytes := pk.Bytes()
	if len(pubBytes) != 32 {
		return fmt.Errorf("invalid ed25519 public key length: %d", len(pubBytes))
	}

	if !ed25519.Verify(pubBytes, msg, sig) {
		return fmt.Errorf("invalid ed25519 signature")
	}

	return nil
}

// ShouldReportSolvency returns true when height is a multiple of
// config.SolvencyBlocks. Returns false when SolvencyBlocks is non-positive.
func (c *Client) ShouldReportSolvency(height int64) bool {
	if c.config.SolvencyBlocks <= 0 {
		return false
	}
	return height%c.config.SolvencyBlocks == 0
}

// SetSigned records that txOutItem has been broadcast with the given
// txId, preventing re-signs. Logs and swallows cache errors.
func (c *Client) SetSigned(txOutItem types.TxOutItem, txId string) {
	err := c.signerCacheManager.SetSigned(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(c.GetChain()),
		txId,
	)
	if err != nil {
		c.logger.Err(err).
			Interface("tx_out_item", txOutItem).
			Msg("failed to mark tx out item as signed")
	}
}

// HasSigned reports whether txOutItem has already been broadcast by this
// node, per the signer cache.
func (c *Client) HasSigned(txOutItem types.TxOutItem) bool {
	return c.signerCacheManager.HasSigned(txOutItem.CacheHash())
}

// GetLatestTxForVault returns the last observed inbound tx id and the
// last broadcast outbound tx id for the given vault on this chain.
func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	chain := c.GetChain().String()

	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(
		types.InboundCacheKey(vault, chain),
	)
	if err != nil {
		return "", "", err
	}

	lastBroadCasted, err := c.signerCacheManager.GetLatestRecordedTx(
		types.BroadcastCacheKey(vault, chain),
	)

	return lastObserved, lastBroadCasted, err
}

// OnObservedTxIn records an outbound-memo inbound in the signer cache
// so future outbounds with the same memo are deduplicated. No-op for
// inbounds whose memo doesn't parse or doesn't carry an outbound txid.
func (c *Client) OnObservedTxIn(txIn types.TxInItem, _ int64) {
	memo, err := thormemo.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// Debug log only as ParseMemo error is expected for THORName inbounds.
		c.logger.Debug().Err(err).Str("memo", txIn.Memo).Msg("fail to parse memo")
		return
	}
	if !memo.IsOutbound() {
		return
	}
	if memo.GetTxID().IsEmpty() {
		return
	}

	chain := c.GetChain()

	cacheHash := txIn.CacheHash(chain, memo.GetTxID().String())
	cacheVault := txIn.CacheVault(chain)

	err = c.signerCacheManager.SetSigned(cacheHash, cacheVault, txIn.Tx)
	if err != nil {
		c.logger.Err(err).Msg("fail to update signer cache")
	}
}
