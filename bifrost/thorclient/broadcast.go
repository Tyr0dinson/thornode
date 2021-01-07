package thorclient

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/legacy/legacytx"
	flag "github.com/spf13/pflag"

	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/common"
)

// Broadcast Broadcasts tx to thorchain
func (b *ThorchainBridge) Broadcast(stdTx authtypes.StdTx, mode types.TxMode) (common.TxID, error) {
	b.broadcastLock.Lock()
	defer b.broadcastLock.Unlock()

	noTxID := common.TxID("")
	if !mode.IsValid() {
		fmt.Println("FOO 1")
		return noTxID, errors.New(fmt.Sprintf("transaction Mode (%s) is invalid", mode))
	}
	start := time.Now()
	defer func() {
		b.m.GetHistograms(metrics.SendToThorchainDuration).Observe(time.Since(start).Seconds())
	}()

	blockHeight, err := b.GetBlockHeight()
	if err != nil {
		fmt.Println("FOO 2")
		return noTxID, err
	}
	if blockHeight > b.blockHeight {
		var seqNum uint64
		b.accountNumber, seqNum, err = b.getAccountNumberAndSequenceNumber()
		if err != nil {
			fmt.Println("FOO 3")
			return noTxID, fmt.Errorf("fail to get account number and sequence number from thorchain : %w", err)
		}
		b.blockHeight = blockHeight
		if seqNum > b.seqNumber {
			b.seqNumber = seqNum
		}
	}

	b.logger.Info().Uint64("account_number", b.accountNumber).Uint64("sequence_number", b.seqNumber).Msg("account info")

	flags := flag.NewFlagSet("thorchain", 0)

	ctx := b.GetContext()
	factory := clienttx.NewFactoryCLI(ctx, flags)
	factory = factory.WithAccountNumber(b.accountNumber)
	factory = factory.WithSequence(b.seqNumber)
	factory = factory.WithSignMode(signing.SignMode_SIGN_MODE_DIRECT)
	factory = factory.WithMemo(stdTx.GetMemo())

	builder, err := clienttx.BuildUnsignedTx(factory, stdTx.GetMsgs()...)
	if err != nil {
		fmt.Println("FOO 4")
		return noTxID, err
	}

	err = clienttx.Sign(factory, ctx.GetFromName(), builder, true)
	if err != nil {
		fmt.Println("FOO 5")
		return noTxID, err
	}

	txBytes, err := ctx.TxConfig.TxJSONEncoder()(builder.GetTx())
	if err != nil {
		fmt.Println("FOO 6")
		return noTxID, err
	}

	// broadcast to a Tendermint node
	commit, err := ctx.BroadcastTx(txBytes)
	if err != nil {
		fmt.Println("FOO 7")
		return noTxID, err
	}

	b.m.GetCounter(metrics.TxToThorchainSigned).Inc()
	// b.logger.Debug().Str("body", string(body)).Msg("broadcast response from THORChain")
	txHash, err := common.NewTxID(commit.TxHash)
	if err != nil {
		fmt.Println("FOO 8")
		return common.BlankTxID, fmt.Errorf("fail to convert txhash: %w", err)
	}
	// Code will be the tendermint ABICode , it start at 1 , so if it is an error , code will not be zero
	if commit.Code > 0 {
		fmt.Println("FOO 9")
		return txHash, fmt.Errorf("fail to broadcast to THORChain,code:%d", commit.Code)
	}
	b.m.GetCounter(metrics.TxToThorchain).Inc()
	b.logger.Info().Msgf("Received a TxHash of %v from the thorchain", commit.TxHash)

	// increment seqNum
	atomic.AddUint64(&b.seqNumber, 1)

	fmt.Println("FOO 10")
	return txHash, nil
}
