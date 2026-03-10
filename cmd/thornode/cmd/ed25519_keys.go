package cmd

import (
	"bufio"
	stded25519 "crypto/ed25519"
	"fmt"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cosmos/cosmos-sdk/client/input"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	bech32 "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32" // nolint SA1019 deprecated
	"github.com/spf13/cobra"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
)

func GetEd25519Keys() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ed25519",
		Short: "Derive an ed25519 public key from a mnemonic",
		Long:  "Takes a BIP39 mnemonic from stdin, derives the ed25519 public key via SLIP-0010, and outputs it in bech32 format.",
		Args:  cobra.ExactArgs(0),
		RunE:  ed25519Keys,
	}
	cmd.Flags().Bool("store", false, "Also store the derived key in the keyring")
	return cmd
}

func ed25519Keys(cmd *cobra.Command, args []string) error {
	// 1. Always read mnemonic from stdin
	buf := bufio.NewReader(cmd.InOrStdin())
	mnemonic, err := input.GetString("Enter mnemonic", buf)
	if err != nil {
		return fmt.Errorf("fail to get mnemonic: %w", err)
	}

	// 2. Derive ed25519 keypair (pure, no keyring)
	result, err := ed25519.DeriveKeypairFromMnemonic(mnemonic, "", ed25519.HDPath)
	if err != nil {
		return fmt.Errorf("fail to derive ed25519 key: %w", err)
	}

	// 3. Convert to bech32 pubkey
	pk := stded25519.PrivateKey(result)
	pub, ok := pk.Public().(stded25519.PublicKey)
	if !ok {
		return fmt.Errorf("fail to get public key")
	}
	pkey := cmted25519.PubKey(pub)
	tmp, err := cryptocodec.FromCmtPubKeyInterface(pkey)
	if err != nil {
		return fmt.Errorf("fail to get ED25519 key: %w", err)
	}
	// nolint
	pubBech32, err := bech32.MarshalPubKey(bech32.AccPK, tmp)
	if err != nil {
		return fmt.Errorf("fail to generate bech32 account pub key")
	}

	// 4. Optionally store in keyring (for bifrost)
	store, _ := cmd.Flags().GetBool("store")
	if store {
		kb, err := cosmos.GetKeybase(app.DefaultNodeHome)
		if err != nil {
			return fmt.Errorf("fail to get keybase: %w", err)
		}
		edKey := ed25519.SignerNameEDDSA(kb.SignerName)
		if _, err := kb.Keybase.Key(edKey); err != nil {
			// Key doesn't exist, store it
			if _, err := kb.Keybase.NewAccount(edKey, mnemonic, kb.SignerPasswd, ed25519.HDPath, ed25519.Ed25519); err != nil {
				return fmt.Errorf("fail to store key in keyring: %w", err)
			}
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), pubBech32)
	return nil
}
