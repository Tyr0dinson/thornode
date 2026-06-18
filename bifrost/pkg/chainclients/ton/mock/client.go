package mock

import (
	"bytes"
	"context"
	"fmt"
	"time"

	tonaddress "github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// MockClient returns valid info for block 100000, error otherwise

const (
	shard0 = int64(-9223372036854775808) // 0x8000000000000000, 0b10...
	shard1 = int64(-4611686018427387904) // 0xC000000000000000, 0b11...
)

type MockClient struct {
	txs []tlb.Transaction
}

func NewMockApiClient() ton.APIClientWrapped {
	txs, _ := GetTxs()
	return MockClient{
		txs: txs,
	}
}

func (c MockClient) Client() ton.LiteClient {
	return nil
}

func (c MockClient) GetTime(ctx context.Context) (uint32, error) {
	return 0, nil
}

func (c MockClient) GetLibraries(ctx context.Context, list ...[]byte) ([]*cell.Cell, error) {
	return nil, nil
}

func (c MockClient) LookupBlock(ctx context.Context, workchain int32, shard int64, seqno uint32) (*ton.BlockIDExt, error) {
	if workchain == -1 {
		if seqno != 100000 {
			return nil, fmt.Errorf("seqno != 100000")
		}
	} else {
		if seqno != 100 && seqno != 101 {
			return nil, fmt.Errorf("seqno != 100000")
		}
	}

	return &ton.BlockIDExt{
		Workchain: workchain,
		Shard:     shard,
		SeqNo:     seqno,
		RootHash:  nil,
		FileHash:  nil,
	}, nil
}

func (c MockClient) GetBlockData(ctx context.Context, block *ton.BlockIDExt) (*tlb.Block, error) {
	switch block.Shard {
	case shard0, shard1:
		return &tlb.Block{}, nil
	default:
		return nil, fmt.Errorf("block shard not exist")
	}
}

func (c MockClient) GetBlockTransactionsV2(ctx context.Context, block *ton.BlockIDExt, count uint32, after ...*ton.TransactionID3) ([]ton.TransactionShortInfo, bool, error) {
	var txs []tlb.Transaction

	if len(c.txs) > 0 {
		index := len(c.txs) - 1
		if index > 1 {
			index /= 2
		}

		switch block.Shard {
		case shard0:
			// first half of txs
			txs = c.txs[:index]
		case shard1:
			// second half of txs
			txs = c.txs[index:]
		}
	}

	var infos []ton.TransactionShortInfo

	for _, tx := range txs {
		infos = append(infos, ton.TransactionShortInfo{
			Account: tx.AccountAddr,
			LT:      tx.LT,
			Hash:    tx.Hash,
		})
	}

	return infos, false, nil
}

func (c MockClient) GetBlockShardsInfo(ctx context.Context, master *ton.BlockIDExt) ([]*ton.BlockIDExt, error) {
	if master.SeqNo != 100000 {
		return nil, fmt.Errorf("seqno != 100000")
	}

	return []*ton.BlockIDExt{
		{
			Workchain: 0,
			Shard:     int64(-9223372036854775808),
			SeqNo:     100,
		},
		{
			Workchain: 0,
			Shard:     int64(-4611686018427387904),
			SeqNo:     101,
		},
	}, nil
}

func (c MockClient) GetBlockchainConfig(ctx context.Context, block *ton.BlockIDExt, onlyParams ...int32) (*ton.BlockchainConfig, error) {
	return nil, nil
}

func (c MockClient) GetMasterchainInfo(ctx context.Context) (*ton.BlockIDExt, error) {
	return &ton.BlockIDExt{
		Workchain: 0,
		Shard:     0,
		SeqNo:     256674,
		RootHash:  nil,
		FileHash:  nil,
	}, nil
}

func (c MockClient) GetAccount(ctx context.Context, block *ton.BlockIDExt, addr *tonaddress.Address) (*tlb.Account, error) {
	return &tlb.Account{
		IsActive: true,
		State: &tlb.AccountState{
			AccountStorage: tlb.AccountStorage{
				Balance: tlb.FromNanoTONU(12319789),
			},
		},
	}, nil
}

func (c MockClient) SendExternalMessage(ctx context.Context, msg *tlb.ExternalMessage) error {
	return nil
}

func (c MockClient) SendExternalMessageWaitTransaction(ctx context.Context, msg *tlb.ExternalMessage) (*tlb.Transaction, *ton.BlockIDExt, []byte, error) {
	return &tlb.Transaction{Hash: make([]byte, 32)}, nil, nil, nil
}

func (c MockClient) RunGetMethod(ctx context.Context, blockInfo *ton.BlockIDExt, addr *tonaddress.Address, method string, params ...interface{}) (*ton.ExecutionResult, error) {
	err := ton.ContractExecError{Code: ton.ErrCodeContractNotInitialized}
	return nil, err
}

func (c MockClient) ListTransactions(ctx context.Context, addr *tonaddress.Address, num uint32, lt uint64, txHash []byte) ([]*tlb.Transaction, error) {
	var result []*tlb.Transaction
	for i := range c.txs {
		result = append(result, &c.txs[i])
	}
	return result, nil
}

func (c MockClient) GetTransaction(ctx context.Context, block *ton.BlockIDExt, addr *tonaddress.Address, lt uint64) (*tlb.Transaction, error) {
	for _, tx := range c.txs {
		if bytes.Equal(tx.AccountAddr, addr.Data()) {
			return &tx, nil
		}
	}
	return nil, nil
}

func (c MockClient) GetBlockProof(ctx context.Context, known, target *ton.BlockIDExt) (*ton.PartialBlockProof, error) {
	return nil, nil
}

func (c MockClient) CurrentMasterchainInfo(ctx context.Context) (_ *ton.BlockIDExt, err error) {
	return c.GetMasterchainInfo(ctx)
}

func (c MockClient) SubscribeOnTransactions(workerCtx context.Context, addr *tonaddress.Address, lastProcessedLT uint64, channel chan<- *tlb.Transaction) {
}

func (c MockClient) VerifyProofChain(ctx context.Context, from, to *ton.BlockIDExt) error {
	return nil
}

func (c MockClient) WaitForBlock(seqno uint32) ton.APIClientWrapped {
	return c
}

func (c MockClient) WithRetry(maxRetries ...int) ton.APIClientWrapped {
	return c
}

func (c MockClient) WithTimeout(timeout time.Duration) ton.APIClientWrapped {
	// TODO implement me
	panic("implement me")
}

func (c MockClient) SetTrustedBlock(block *ton.BlockIDExt) {}

func (c MockClient) SetTrustedBlockFromConfig(cfg *liteclient.GlobalConfig) {}

func (c MockClient) FindLastTransactionByInMsgHash(ctx context.Context, addr *tonaddress.Address, msgHash []byte, maxTxNumToScan ...int) (*tlb.Transaction, error) {
	return nil, nil
}

func (c MockClient) FindLastTransactionByOutMsgHash(ctx context.Context, addr *tonaddress.Address, msgHash []byte, maxTxNumToScan ...int) (*tlb.Transaction, error) {
	return nil, nil
}

func (c MockClient) GetBlockHeader(ctx context.Context, block *ton.BlockIDExt) (*tlb.BlockHeader,
	error) {
	switch block.Shard {
	case shard0, shard1:
		return &tlb.BlockHeader{}, nil
	default:
		return nil, fmt.Errorf("block shard not exist")
	}
}

func (c MockClient) WithLSInfoInErrors() ton.APIClientWrapped {
	return c
}

func (c MockClient) FindLastTransactionByInMsgHashAfterTime(ctx context.Context, addr *tonaddress.Address, msgHash []byte, after time.Time) (*tlb.Transaction, error) {
	return nil, nil
}

func (c MockClient) FindLastTransactionByOutMsgHashAfterTime(ctx context.Context, addr *tonaddress.Address, msgHash []byte, after time.Time) (*tlb.Transaction, error) {
	return nil, nil
}

func (c MockClient) GetOutMsgQueueSizes(ctx context.Context, wc *int32, shard *int64) (*ton.OutMsgQueueSizes, error) {
	return nil, nil
}

func (c MockClient) GetBlockOutMsgQueueSize(ctx context.Context, block *ton.BlockIDExt) (*ton.BlockOutMsgQueueSize, error) {
	return nil, nil
}

func (c MockClient) GetDispatchQueueInfo(ctx context.Context, block *ton.BlockIDExt, afterAddr *tonaddress.Address, maxAccounts int) (*ton.DispatchQueueInfo, error) {
	return nil, nil
}

func (c MockClient) GetDispatchQueueMessages(ctx context.Context, block *ton.BlockIDExt, addr *tonaddress.Address, afterLT uint64, maxMessages int, options ...func(*ton.GetDispatchQueueMessages)) (*ton.DispatchQueueMessages, error) {
	return nil, nil
}

func (c MockClient) GetBlockDataAsCell(ctx context.Context, block *ton.BlockIDExt) (*cell.Cell, error) {
	return nil, nil
}

func (c MockClient) RunGetMethodByID(ctx context.Context, blockInfo *ton.BlockIDExt, addr *tonaddress.Address, methodID uint64, params ...interface{}) (*ton.ExecutionResult, error) {
	return nil, nil
}

func (c MockClient) WithRetryTimeout(maxRetries int, timeout time.Duration) ton.APIClientWrapped {
	return c
}

func (c MockClient) GetAllNonfinalValidatorGroups(ctx context.Context) (*ton.NonfinalValidatorGroups, error) {
	return nil, nil
}

func (c MockClient) GetNonfinalValidatorGroups(ctx context.Context, wc int32, shard int64) (*ton.NonfinalValidatorGroups, error) {
	return nil, nil
}

func (c MockClient) GetNonfinalCandidate(ctx context.Context, id *ton.NonfinalCandidateID) (*ton.NonfinalCandidate, error) {
	return nil, nil
}

func (c MockClient) GetAllNonfinalPendingShardBlocks(ctx context.Context) (*ton.NonfinalPendingShardBlocks, error) {
	return nil, nil
}

func (c MockClient) GetNonfinalPendingShardBlocks(ctx context.Context, wc int32, shard int64) (*ton.NonfinalPendingShardBlocks, error) {
	return nil, nil
}
