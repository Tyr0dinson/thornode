package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cosmoscryptoed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	bech32 "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32" // nolint SA1019 deprecated
	"github.com/spf13/cobra"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
)

func GetEd25519Keys() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ed25519",
		Short: "Derive ed25519 public key from mnemonic on stdin",
		Long:  `Reads a mnemonic from stdin and derives the ed25519 public key. Uses SIGNER_PASSWD env var as BIP39 passphrase if set.`,
		Args:  cobra.ExactArgs(0),
		RunE:  ed25519Keys,
	}
	return cmd
}

func ed25519Keys(cmd *cobra.Command, args []string) error {
	// Read mnemonic from stdin
	buf := bufio.NewReader(cmd.InOrStdin())
	mnemonic, err := buf.ReadString('\n')
	if err != nil && mnemonic == "" {
		return fmt.Errorf("fail to read mnemonic from stdin: %w", err)
	}
	mnemonic = strings.TrimSpace(mnemonic)
	if mnemonic == "" {
		return fmt.Errorf("mnemonic is required on stdin")
	}

	// Use SIGNER_PASSWD env var as BIP39 passphrase for key derivation. The
	// shared helper trims it (as the keyring path did) so the derived pubkey
	// matches the one bifrost loads at runtime.
	passphrase := os.Getenv(cosmos.EnvSignerPassword)

	// Derive ed25519 private key directly from mnemonic (no keyring needed)
	privKeyBytes, err := ed25519.DeriveSignerEDDSAKeyBytes(mnemonic, passphrase)
	if err != nil {
		return fmt.Errorf("fail to derive ed25519 key: %w", err)
	}

	// Create pubkey from derived private key
	privKey := &cosmoscryptoed25519.PrivKey{Key: privKeyBytes}
	cosmosPubKey, ok := privKey.PubKey().(*cosmoscryptoed25519.PubKey)
	if !ok {
		return fmt.Errorf("fail to get ed25519 public key")
	}

	pkey := cmted25519.PubKey(cosmosPubKey.Bytes())
	tmp, err := cryptocodec.FromCmtPubKeyInterface(pkey)
	if err != nil {
		return fmt.Errorf("fail to get ED25519 key: %w", err)
	}
	// nolint
	pubBech32, err := bech32.MarshalPubKey(bech32.AccPK, tmp)
	if err != nil {
		return fmt.Errorf("fail generate bech32 account pub key")
	}
	fmt.Fprintln(cmd.OutOrStdout(), pubBech32)
	return nil
}
