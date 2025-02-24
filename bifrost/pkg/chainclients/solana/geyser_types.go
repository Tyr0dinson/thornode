package solana

import (
	"context"

	"github.com/btcsuite/btcutil/base58"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/geyser/proto"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
)

// auth implements the credentials.PerRPCCredentials interface to support x-token authentication for grpc requests.
type tokenAuth struct {
	token string
}

func (a tokenAuth) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{
		"x-token": a.token,
	}, nil
}

func (tokenAuth) RequireTransportSecurity() bool {
	return false
}

func byteArrayToIntArray(byteSlice []byte) []int {
	intSlice := make([]int, len(byteSlice))
	for i, b := range byteSlice {
		intSlice[i] = int(b)
	}

	return intSlice
}

func GeyserToRPCMessage(message *proto.Message) rpc.RPCMessage {
	accountKeys := make([]string, len(message.AccountKeys))
	for i, v := range message.AccountKeys {
		if v != nil {
			accountKeys[i] = base58.Encode(v)
		} else {
			accountKeys[i] = ""
		}
	}
	addressLookups := make([]rpc.RPCAddressLookupTables, len(message.AddressTableLookups))
	for i, v := range message.AddressTableLookups {
		addressLookups[i] = rpc.RPCAddressLookupTables{
			AccountKey:      base58.Encode(v.AccountKey),
			WritableIndexes: byteArrayToIntArray(v.WritableIndexes),
			ReadonlyIndexes: byteArrayToIntArray(v.ReadonlyIndexes),
		}
	}

	instructions := make([]rpc.RPCInstruction, len(message.Instructions))
	for i, v := range message.Instructions {
		data := ""
		if v.Data != nil {
			data = base58.Encode(v.Data)
		}

		instructions[i] = rpc.RPCInstruction{
			Accounts:       byteArrayToIntArray(v.Accounts),
			Data:           data,
			ProgramIdIndex: int(v.ProgramIdIndex),
		}
	}

	recentBlockhash := ""
	if message.RecentBlockhash != nil {
		recentBlockhash = base58.Encode(message.RecentBlockhash)
	}

	return rpc.RPCMessage{
		AccountKeys:         accountKeys,
		AddressLookupTables: addressLookups,
		Header: rpc.RPCHeader{
			NumReadonlySignedAccounts:   int(message.Header.NumReadonlySignedAccounts),
			NumReadonlyUnsignedAccounts: int(message.Header.NumReadonlyUnsignedAccounts),
			NumRequiredSignatures:       int(message.Header.NumRequiredSignatures),
		},
		Instructions:    instructions,
		RecentBlockhash: recentBlockhash,
	}
}

func GeyserToRPCTranscation(transaction *proto.SubscribeUpdateTransactionInfo) rpc.RPCTransaction {
	// Meta information
	innerInstructions := make([]interface{}, len(transaction.Meta.InnerInstructions))
	for i, v := range transaction.Meta.InnerInstructions {
		innerInstructions[i] = v
	}
	postBalances := make([]interface{}, len(transaction.Meta.PostTokenBalances))
	for i, v := range transaction.Meta.PostTokenBalances {
		postBalances[i] = v
	}
	preBalances := make([]interface{}, len(transaction.Meta.PreTokenBalances))
	for i, v := range transaction.Meta.PreTokenBalances {
		preBalances[i] = v
	}

	readonlyAddresses := make([]interface{}, len(transaction.Meta.LoadedReadonlyAddresses))
	for i, v := range transaction.Meta.LoadedReadonlyAddresses {
		readonlyAddresses[i] = v
	}
	writableAddresses := make([]interface{}, len(transaction.Meta.LoadedWritableAddresses))
	for i, v := range transaction.Meta.LoadedWritableAddresses {
		writableAddresses[i] = v
	}
	addressList := rpc.RPCAddrList{
		Readonly: readonlyAddresses,
		Writable: writableAddresses,
	}

	txnMeta := rpc.RPCMeta{
		ComputeUnitsConsumed: *transaction.Meta.ComputeUnitsConsumed,
		Err:                  transaction.Meta.Err,
		Fee:                  transaction.Meta.Fee,
		InnerInstructions:    innerInstructions,
		LoadedAddresses:      addressList,
		LogMessages:          transaction.Meta.LogMessages,
		PostBalances:         transaction.Meta.PostBalances,
		PostTokenBalances:    postBalances,
		PreBalances:          transaction.Meta.PreBalances,
		PreTokenBalances:     preBalances,
		Rewards:              transaction.Meta.Rewards,
		Status:               rpc.RPCTxnStatus{},
	}

	// Transaction data
	signatures := make([]string, len(transaction.Transaction.Signatures))
	for i, s := range transaction.Transaction.Signatures {
		if s != nil {
			signatures[i] = base58.Encode(s)
		} else {
			signatures[i] = ""
		}
	}

	txnData := rpc.RPCTxnData{
		Message:    GeyserToRPCMessage(transaction.Transaction.Message),
		Signatures: signatures,
	}

	return rpc.RPCTransaction{
		Meta:        txnMeta,
		Transaction: txnData,
		Version:     rpc.RPCVersion{Value: "legacy"},
	}
}

func GeyserToRPCBlock(block *proto.SubscribeUpdateBlock) rpc.BlockResult {
	// Convert transactions
	transactions := make([]rpc.RPCTransaction, len(block.Transactions))
	for i, t := range block.Transactions {
		transactions[i] = GeyserToRPCTranscation(t)
	}

	// Convert block
	blockResult := rpc.BlockResult{
		Blockhash:         block.Blockhash,
		PreviousBlockhash: block.ParentBlockhash,
		ParentSlot:        block.ParentSlot,
		Transactions:      transactions,
	}

	return blockResult
}
