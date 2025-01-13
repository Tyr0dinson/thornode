package ictest

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
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
	for ; currentHeight < startingHeight + delta; {
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
func ProposeUpgrade(ctx context.Context, val *thorchain.ChainNode, upgradeName string, haltHeight int64) error {
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
func ApproveUpgrade(ctx context.Context, val *thorchain.ChainNode, upgradeName string) error {
	_, err := val.ExecTx(ctx, "thorchain", "thorchain", "approve-upgrade", upgradeName)
	for count := 0; err != nil && count < 4; count++ {
		fmt.Println("ApproveUpgrade error, trying again:", err)
		time.Sleep(time.Second)
		_, err = val.ExecTx(ctx, "thorchain", "thorchain", "approve-upgrade", upgradeName)
	}
	return err
}