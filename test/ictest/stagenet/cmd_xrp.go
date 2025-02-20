package stagenet

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Peersyst/xrpl-go/xrpl/queries/account"
	qcommon "github.com/Peersyst/xrpl-go/xrpl/queries/common"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	transactions "github.com/Peersyst/xrpl-go/xrpl/transaction"
	txtypes "github.com/Peersyst/xrpl-go/xrpl/transaction/types"
	"github.com/Peersyst/xrpl-go/xrpl/wallet"
	//"github.com/Peersyst/xrpl-go/pkg/crypto"
)

func XrpSendFunds(r *rpc.Client, srcWallet wallet.Wallet, dstAddr string, amount int64) error {
	_, err := XrpSendFundsWithNote(r, srcWallet, dstAddr, amount, "")
	return err
}

func XrpSendFundsWithNote(r *rpc.Client, srcWallet wallet.Wallet, dstAddr string, amount int64, note string) (string, error) {
	return XrpSendFundsWithRetry(r, srcWallet, dstAddr, amount, note, true)
}

func XrpSendFundsWithRetry(r *rpc.Client, srcWallet wallet.Wallet, dstAddr string, amount int64, note string, retry bool) (string, error) {
	ai, err := r.GetAccountInfo(&account.InfoRequest{
		Account: txtypes.Address(srcWallet.ClassicAddress),
		LedgerIndex: qcommon.Current,
	})
	if err != nil {
		return "", err
	}

	// Create payment transaction.
	tx := transactions.Payment{
		BaseTx: transactions.BaseTx{
			Account: srcWallet.ClassicAddress,
			Sequence: ai.AccountData.Sequence,
			Fee: txtypes.XRPCurrencyAmount(uint64(15)),
		},
		Amount: txtypes.XRPCurrencyAmount(amount),
		Destination: txtypes.Address(dstAddr),
	}

	if note != "" {
		tx.BaseTx.Memos = []txtypes.MemoWrapper{
			{
				Memo: txtypes.Memo{
					MemoData: hex.EncodeToString([]byte(note)),
				},
			},
		}
	}

	flatTx := tx.Flatten()
	if err := r.Autofill(&flatTx); err != nil {
		return "", err
	}

	txBlob, _, err := srcWallet.Sign(flatTx)
	if err != nil {
		return "", err
	}

	fmt.Println("sending xrp funds", "tx", flatTx)

	response, err := r.SubmitAndWait(txBlob, true)
	if err != nil {
		if strings.Contains(err.Error(), "tefPAST_SEQ") && retry {
			time.Sleep(time.Second * 5)
			return XrpSendFundsWithRetry(r, srcWallet, dstAddr, amount, note, false)
		}
		return "", err
	}

	return response.Hash.String(), err
}