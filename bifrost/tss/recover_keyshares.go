package tss

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	ctypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/itchio/lzma"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func RecoverKeyShares(conf config.Bifrost, thorchain thorclient.ThorchainBridge) error {
	tctx := thorchain.GetContext()

	// fetch the node account
	na, err := thorchain.GetNodeAccount(tctx.FromAddress.String())
	if err != nil {
		return fmt.Errorf("fail to get node account: %w", err)
	}

	// skip recovery if the current node is not active
	if na.Status != types.NodeStatus_Active {
		log.Info().Msgf("%s is not active, skipping key shares recovery", na.NodeAddress)
		return nil
	}

	// the current vault is the last pub key in the signer membership list
	membership := na.GetSignerMembership()
	if len(membership) == 0 {
		log.Info().Msgf("no signer membership for %s, skipping key shares recovery", na.NodeAddress)
		return fmt.Errorf("fail to get signer membership")
	}
	vault := membership[len(membership)-1]
	keysharesPath := filepath.Join(app.DefaultNodeHome, fmt.Sprintf("localstate-%s.json", vault))

	// skip recovery if keyshares for the nodes current vault already exist
	if _, err = os.Stat(keysharesPath); !os.IsNotExist(err) {
		log.Info().Msgf("ecdsa key shares for %s already exist, skipping recovery", vault)
		return nil
	}

	// get all vaults
	vaults, err := thorchain.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards: %w", err)
	}

	// get the creation height of the member vault
	var lastVaultHeight int64
	for _, v := range vaults {
		if v.PubKey.Equals(vault) {
			lastVaultHeight = v.BlockHeight
			break
		}
	}
	if lastVaultHeight == 0 {
		return fmt.Errorf("fail to get creation height of %s", vault)
	}

	// walk backward from the churn height until we find the TssPool message we sent
	var keysharesEncBytes []byte
	var keysharesEncBytesEddsa []byte
	var keysharesEncBytesFrost []byte
	var vaultEddsa common.PubKey
	cdc := thorclient.MakeCodec()
	dec := ebifrost.TxDecoder(cdc, tx.DefaultTxDecoder(cdc))
	scanLowerBound := recoverScanLowerBound(lastVaultHeight, conf.TSS.MaxKeyshareRecoverScanBlocks)
	for i := lastVaultHeight; i > scanLowerBound; i-- {
		if i%1000 == 0 {
			log.Info().Msgf("scanning block %d for TssPool message to recover key shares", i)
		}

		var b *coretypes.ResultBlock
		b, err = thorchain.GetContext().Client.Block(context.Background(), &i)
		if err != nil {
			return fmt.Errorf("fail to get block: %w", err)
		}

		for _, txb := range b.Block.Txs {
			var tx ctypes.Tx
			tx, err = dec(txb)
			if err != nil {
				return fmt.Errorf("fail to decode tx: %w", err)
			}
			for _, msg := range tx.GetMsgs() {
				switch m := msg.(type) {
				case *types.MsgTssPool:
					if m.Signer.Equals(na.NodeAddress) {
						if m.KeysharesBackup == nil {
							log.Warn().Msgf("key shares backup not saved for %s", na.NodeAddress)
						}
						keysharesEncBytes = m.KeysharesBackup
						keysharesEncBytesEddsa = m.KeysharesBackupEddsa
						keysharesEncBytesFrost = m.KeysharesBackupFrost
						vaultEddsa = m.PoolPubKeyEddsa
						goto finish
					}
				default:
				}
			}
		}
	}

	if keysharesEncBytes == nil {
		return fmt.Errorf(
			"TssPool message from %s not found within %d blocks of vault height %d",
			na.NodeAddress,
			conf.TSS.MaxKeyshareRecoverScanBlocks,
			lastVaultHeight,
		)
	}

finish:
	seedPhrase, err := recoverySeedPhrase()
	if err != nil {
		return err
	}

	if err = writeRecoveredDecompressedKeyshares(keysharesPath, keysharesEncBytes, seedPhrase); err != nil {
		return fmt.Errorf("failed to recover ecdsa key shares: %w", err)
	}

	// success
	log.Info().Str("path", keysharesPath).Msgf("recovered ecdsa key shares for %s", na.NodeAddress)

	// Now recover eddsa

	keysharesPath = filepath.Join(app.DefaultNodeHome, fmt.Sprintf("localstate-%s.json", vaultEddsa))

	lastVaultHeight = 0
	for _, v := range vaults {
		if v.PubKeyEddsa.Equals(vaultEddsa) {
			lastVaultHeight = v.BlockHeight
			break
		}
	}
	if lastVaultHeight == 0 {
		return fmt.Errorf("fail to get creation height of %s", vaultEddsa)
	}

	// skip recovery if keyshares for the nodes current vault already exist
	if _, err = os.Stat(keysharesPath); !os.IsNotExist(err) {
		log.Info().Msgf("eddsa key shares for %s already exist, skipping recovery", vaultEddsa)
		return nil
	}

	if err = writeRecoveredDecompressedKeyshares(keysharesPath, keysharesEncBytesEddsa, seedPhrase); err != nil {
		return fmt.Errorf("failed to recover eddsa key shares: %w", err)
	}

	// success
	log.Info().Str("path", keysharesPath).Msgf("recovered key shares for %s", na.NodeAddress)

	// FROST key recovery — decrypt the backup and write the raw hex to a
	// recovery file. The XMR client's startup picks this up and sends it
	// to the sidecar's restore endpoint.
	if len(keysharesEncBytesFrost) > 0 && !vaultEddsa.IsEmpty() {
		frostRecoveryPath := filepath.Join(app.DefaultNodeHome, fmt.Sprintf("frost-recovery-%s.hex", vaultEddsa))
		if _, statErr := os.Stat(frostRecoveryPath); os.IsNotExist(statErr) {
			frostErr := writeRecoveredRawKeyshares(frostRecoveryPath, keysharesEncBytesFrost, seedPhrase)
			if frostErr != nil {
				log.Error().Err(frostErr).Str("path", frostRecoveryPath).Msg("failed to recover frost keyshare backup")
			} else {
				log.Info().Str("path", frostRecoveryPath).Msgf("recovered frost key shares for %s", vaultEddsa)
			}
		}
	}

	return nil
}

func recoverScanLowerBound(lastVaultHeight, maxScanBlocks int64) int64 {
	if lastVaultHeight <= 0 {
		return 0
	}
	lowerBound := lastVaultHeight - maxScanBlocks
	if lowerBound < 0 {
		return 0
	}
	return lowerBound
}

func recoverySeedPhrase() (string, error) {
	seedPhrase := strings.TrimSpace(os.Getenv("SIGNER_SEED_PHRASE"))
	if seedPhrase == "" {
		return "", fmt.Errorf("SIGNER_SEED_PHRASE environment variable is not set")
	}
	return seedPhrase, nil
}

func writeRecoveredDecompressedKeyshares(path string, encrypted []byte, seedPhrase string) error {
	if len(encrypted) == 0 {
		return fmt.Errorf("encrypted key shares backup is empty")
	}

	decrypted, err := DecryptKeyshares(encrypted, seedPhrase)
	if err != nil {
		return fmt.Errorf("failed to decrypt key shares: %w", err)
	}

	rawKeyshares, err := io.ReadAll(lzma.NewReader(bytes.NewReader(decrypted)))
	if err != nil {
		return fmt.Errorf("failed to decompress key shares: %w", err)
	}

	return writeRecoveredBytes(path, rawKeyshares)
}

func writeRecoveredRawKeyshares(path string, encrypted []byte, seedPhrase string) error {
	if len(encrypted) == 0 {
		return fmt.Errorf("encrypted raw key shares backup is empty")
	}

	rawKeyshares, err := DecryptRawKeyshares(encrypted, seedPhrase)
	if err != nil {
		return fmt.Errorf("failed to decrypt raw key shares: %w", err)
	}

	return writeRecoveredBytes(path, rawKeyshares)
}

func writeRecoveredBytes(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to open recovery temp file: %w", err)
	}

	tmpPath := tmp.Name()
	success := false
	defer func() {
		_ = tmp.Close()
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("failed to set recovery temp file permissions: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("failed to write recovery temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close recovery temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to move recovery file into place: %w", err)
	}

	success = true
	return nil
}
