package generic

import (
	"bytes"
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
	thorconfig "gitlab.com/thorchain/thornode/v3/config"
	thormemo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

type Client struct {
	logger             zerolog.Logger
	config             thorconfig.BifrostChainConfiguration
	bridge             thorclient.ThorchainBridge
	tssKeyManager      tss.ThorchainKeyManager
	localKeyManager    LocalKeyManager
	signerCacheManager *signercache.CacheManager
}

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

func (c *Client) Start() {
	c.tssKeyManager.Start()
}

func (c *Client) Stop() {
	c.tssKeyManager.Stop()
}

func (c *Client) GetChain() common.Chain {
	return c.config.ChainID
}

func (c *Client) GetGasAsset() common.Asset {
	return c.config.ChainID.GetGasAsset()
}

func (c *Client) GetConfig() thorconfig.BifrostChainConfiguration {
	return c.config
}

func (c *Client) GetPubKey() common.PubKey {
	return c.localKeyManager.PubKey()
}

func (c *Client) Sign(
	data []byte,
	txOutItem types.TxOutItem,
) ([]byte, error) {
	// var err error
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
		signature, _, err = c.tssKeyManager.RemoteSign(
			data,
			c.config.ChainID.GetSigningAlgo(),
			vaultPubKey.String(),
		)
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

	switch c.GetChain().GetSigningAlgo() {
	case common.SigningAlgoSecp256k1:
		err = verifySecp256k1(data, signature, pubKey)
	}

	return signature, err
}

func verifySecp256k1(msg, sig []byte, pub common.PubKey) error {
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

// solvency

func (c *Client) ShouldReportSolvency(height int64) bool {
	if c.config.SolvencyBlocks <= 0 {
		return false
	}
	return height%c.config.SolvencyBlocks == 0
}

// cache manager

func (c *Client) SetSigned(txOutItem types.TxOutItem, txId string) {
	err := c.signerCacheManager.SetSigned(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(c.config.ChainID),
		txId,
	)
	if err != nil {
		c.logger.Err(err).
			Interface("tx_out_item", txOutItem).
			Msg("failed to mark tx out item as signed")
	}
}

func (c *Client) HasSigned(txOutItem types.TxOutItem) bool {
	return c.signerCacheManager.HasSigned(txOutItem.CacheHash())
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	chain := c.config.ChainID.String()

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

//

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

	chain := c.config.ChainID

	cacheHash := txIn.CacheHash(chain, memo.GetTxID().String())
	cacheVault := txIn.CacheVault(chain)

	err = c.signerCacheManager.SetSigned(cacheHash, cacheVault, txIn.Tx)
	if err != nil {
		c.logger.Err(err).Msg("fail to update signer cache")
	}
}

// // decimal conversion
//
// func (c *Client) ToThor(amount *big.Int) (*big.Int, error) {
// 	return common.ConvertDecimals(
// 		amount,
// 		int(c.config.ChainID.GetGasAssetDecimal()),
// 		common.THORChainDecimals,
// 	)
// }
//
// func (c *Client) FromThor(amount *big.Int) (*big.Int, error) {
// 	return common.ConvertDecimals(
// 		amount,
// 		common.THORChainDecimals,
// 		int(c.config.ChainID.GetGasAssetDecimal()),
// 	)
// }
