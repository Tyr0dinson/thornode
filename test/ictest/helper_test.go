package ictest

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"
	
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"

	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/strangelove-ventures/interchaintest/v8/testutil"

	"gitlab.com/thorchain/thornode/v3/test/ictest/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
 	"gitlab.com/thorchain/thornode/v3/config"

	"github.com/stretchr/testify/require"
)

func GetEthAddressFromStdout(stdout string) (string, error) {
	// Define the regular expression pattern
	re := regexp.MustCompile(`"value":"(0x[0-9a-fA-F]+)"`)

	// Find the first match
	matches := re.FindStringSubmatch(stdout)
	if len(matches) <= 1 {
		return "", fmt.Errorf("failed to parse out contract address")
	}
	// Extract the value
	return matches[1], nil
}

// We are running many nodes, using many resources. This function is similar to
// testutils.WaitForBlocks(), but does not hammer calls as fast as possible.
func NiceWaitForBlocks(ctx context.Context, delta int64, chain ibc.Chain) error {
	startingHeight, err := chain.Height(ctx)
	if err != nil {
		return err
	}

	currentHeight := startingHeight
	for currentHeight < startingHeight+delta {
		time.Sleep(time.Millisecond * 200)
		currentHeight, err = chain.Height(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

// ProposeUpgrade submits an upgrade proposal by a validator
// Since bifrost is running, there is potential for a sequence number mismatch with no seq num increment logic so we try a few times
// We can shut down bifrost earlier, but current upgrade plan does not require this
func ProposeUpgrade(ctx context.Context, val *tc.ChainNode, upgradeName string, haltHeight int64) error {
	_, err := val.ExecTx(ctx, "thorchain", "thorchain", "propose-upgrade", upgradeName, fmt.Sprint(haltHeight))
	for count := 0; err != nil && count < 4; count++ {
		time.Sleep(time.Second)
		_, err = val.ExecTx(ctx, "thorchain", "thorchain", "propose-upgrade", upgradeName, fmt.Sprint(haltHeight))
	}
	return err
}

// ApproveUpgrade submits an upgrade proposal by a validator
// Since bifrost is running, there is potential for a sequence number mismatch with no seq num increment logic so we try a few times
// We can shut down bifrost earlier, but current upgrade plan does not require this
func ApproveUpgrade(ctx context.Context, val *tc.ChainNode, upgradeName string) error {
	_, err := val.ExecTx(ctx, "thorchain", "thorchain", "approve-upgrade", upgradeName)
	for count := 0; err != nil && count < 4; count++ {
		fmt.Println("ApproveUpgrade error, trying again:", err)
		time.Sleep(time.Second)
		_, err = val.ExecTx(ctx, "thorchain", "thorchain", "approve-upgrade", upgradeName)
	}
	return err
}

func addValidators(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, numValsToAdd int) error {
	// Add new validators
	err := thorchain.AddValidators(ctx, nil, numValsToAdd)
	require.NoError(t, err)

	// Wait 20 blocks to get close to the first churn
	err = testutil.WaitForBlocks(ctx, 20, thorchain)
	require.NoError(t, err, "thorchain failed to make blocks")

	// Wait for all vals to be active, then disable 1 val
	numActiveVals := 0
	for i := 0; i < 20; i++ {
		numActiveVals = getNumActiveVals(t, ctx, thorchain)
		err = testutil.WaitForBlocks(ctx, 5, thorchain)
		require.NoError(t, err, "thorchain failed to make blocks")

		if numActiveVals == len(thorchain.Validators) {
			fmt.Println("Validators added, total:", numActiveVals)
			return nil
		}
	}

	return fmt.Errorf("Failed to add validators, actual: %d, expected: %d", numActiveVals, len(thorchain.Validators))
}

func setNetworkFee(vals []*tc.ChainNode, exoChains ExoChains) error {
	for _, val := range vals {
		// add key to keyring
		registry := codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(registry)
		cdc := codec.NewProtoCodec(registry)
		kr := keyring.NewInMemory(cdc)
		name := val.HostName()
		_, err := kr.NewAccount(name, val.ValidatorMnemonic, "", `m/44'/931'/0'/0/0`, hd.Secp256k1)
		if err != nil {
			return err
		}

		// create thorclient.Keys for chain client construction
		keys := thorclient.NewKeysWithKeybase(kr, name, "")

		// bifrost config for chain client construction
		cfg := config.BifrostClientConfiguration{
			ChainID: common.Chain(val.Chain.Config().ChainID),
			ChainHost: "http://127.0.0.1:1317",
			ChainRPC: val.Chain.GetHostRPCAddress(),
			SignerName: name,
		}

		thorchainBridge, err := thorclient.NewThorchainBridge(cfg, keys)
		if err != nil {
			return err
		}

		for name := range exoChains {
			if name == "GAIA" {
				_, err = thorchainBridge.PostNetworkFee(1, common.GAIAChain, 1, 1_000_000)
				if err != nil {
					return err
				}
			}
			if name == "XRP" {
				_, err = thorchainBridge.PostNetworkFee(1, common.XRPChain, 1, 1_000)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}
