package cmd

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/spf13/cobra"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/haven"
)

// GetCryptonote returns haven cryptonote as priv view key + pub spend key
func GetCryptonote() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cryptonote <name>",
		Short: "Get cryptonote (priv view key + pub spend key) for an account in the keyring",
		Long:  ``,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			name := args[0]
			kb := keyring.NewUnsafe(clientCtx.Keyring)
			privKey, err := kb.UnsafeExportPrivKeyHex(name)
			if err != nil {
				return err
			}
			cryptonote, err := haven.GetCryptonoteFromPrivKey(privKey)
			if err != nil {
				return err
			}
			fmt.Println(cryptonote)
			return nil
		},
	}
	cmd.PersistentFlags().String(flags.FlagKeyringBackend, flags.DefaultKeyringBackend, "Select keyring's backend (os|file|test)")
	return cmd
}
