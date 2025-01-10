package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/xrp/rpca/types"
	keymanager "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/xrp/keymanager"
)

// Get account sequence number.
func (x XrpClient) GetAccountSequence(account string) (int, error) {
	params := []any{
		map[string]string{
			"account": account,
			"strict":  "true",
		},
	}

	response, err := makeRPCCall(x.url, "account_info", params)
	if err != nil {
		return 0, err
	}

	var result struct {
		AccountData struct {
			Sequence int `json:"Sequence"`
		} `json:"account_data"`
	}

	if err := json.Unmarshal(response.Result, &result); err != nil {
		return 0, err
	}

	return result.AccountData.Sequence, nil
}

func SignPayment(km *keymanager.KeyManager, payment *types.Payment) (string, error) {
	// // In a real implementation, you'd need to serialize the transaction fields
	// // in the exact order specified by XRPL
	// txBytes, err := json.Marshal(payment)
	// if err != nil {
	//     return "", fmt.Errorf("failed to marshal payment: %v", err)
	// }

	txBytes, err := SerializePayment(payment, false)
	if err != nil {
		return "", err
	}

	signature, err := km.Keys.Sign(txBytes)
	if err != nil {
		return "", fmt.Errorf("error sign payment: %v", err)
	}

	verified, err := VerifySignature(km, payment, signature)
	if err != nil {
		return "", fmt.Errorf("error verifying signature: %v", err)
	}

	if !verified {
		return "", fmt.Errorf("signature verification failed")
	}

	return hex.EncodeToString(signature), nil
}

func (x XrpClient) SignAndSubmitPayment(km *keymanager.KeyManager, payment *types.Payment) (string, error) {
	// Set the public key in the payment.
	payment.SigningPubKey = km.PublicKeyHex

	// Sign the transaction.
	signature, err := SignPayment(km, payment)
	if err != nil {
		return "", fmt.Errorf("failed to sign payment: %v", err)
	}
	payment.TxnSignature = signature

	serializedTx, err := SerializePayment(payment, true)
	if err != nil {
		return "", err
	}
	txBlob := hex.EncodeToString(serializedTx)
	return x.SubmitTxBlob(txBlob)

}

func (x XrpClient) SubmitTxBlob(txBlob string) (string, error) {
	// Submit the signed transaction.
	params := []any{
		map[string]interface{}{
			"tx_blob": txBlob,
		},
	}

	// _, err = x.GetFee(payment)
	// if err != nil {
	// 	return "", fmt.Errorf("get fee error: %v", err)
	// }

	response, err := makeRPCCall(x.url, "submit", params)
	if err != nil {
		return "", err
	}

	if response.Error != nil {
		return "", fmt.Errorf("error submitting payment tx, code id: %d, message: %s", response.Error.Code, response.Error.Message)
	}

	var results types.TransactionResponse
	err = json.Unmarshal(response.Result, &results)
	if err != nil {
		return "", fmt.Errorf("fail unmarshal submit response: %v", err)
	}

	if results.Error != "" || results.ErrorCode != 0 {
		return "", fmt.Errorf("server side error submitting payment, error: %s, error code: %d, error message: %s, status: %s", results.Error, results.ErrorCode, results.ErrorMessage, results.Status)
	}

	x.log.Info().Str("results", string(response.Result)).Msg("payment tx submitted")
	return results.TxJSON.Hash, nil
}

// Signature verification.
func VerifySignature(km *keymanager.KeyManager, payment *types.Payment, signature []byte) (bool, error) {
	txBytes, err := SerializePayment(payment, false)
	if err != nil {
		return false, err
	}
	return km.Keys.Verify(txBytes, signature)
}
