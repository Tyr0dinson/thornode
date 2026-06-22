package mock

import (
	"context"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	tonaddress "github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// MockClient serves a TON shard block captured from a mocknet simulation run
// so the scanner exercises the real GetBlockData -> ListTransactions path.
// Fixtures live in testdata/.

const (
	shard0 = int64(-9223372036854775808) // 0x8000000000000000, 0b10...
	shard1 = int64(-4611686018427387904) // 0xC000000000000000, 0b11...

	masterDeposit uint32 = 120
	masterRegular uint32 = 30
	shardDeposit  uint32 = 121
	shardRegular  uint32 = 27
)

//go:embed testdata/deposit_block.hex
var depositBlockHex string

//go:embed testdata/regular_block.hex
var regularBlockHex string

//go:embed testdata/sender_txs.txt
var txsRaw string

// blocks
type MockClient struct {
	txs    map[string][]*tlb.Transaction
	blocks map[uint32]*tlb.Block
	shards map[uint32]uint32
}

func NewMockApiClient() ton.APIClientWrapped {
	return MockClient{
		txs: mustTxs(txsRaw),
		blocks: map[uint32]*tlb.Block{
			shardDeposit: mustBlock(depositBlockHex),
			shardRegular: mustBlock(regularBlockHex),
		},
		shards: map[uint32]uint32{
			masterDeposit: shardDeposit,
			masterRegular: shardRegular,
		},
	}
}

func mustCell(h string) *cell.Cell {
	raw, err := hex.DecodeString(strings.TrimSpace(h))
	if err != nil {
		panic(err)
	}
	c, err := cell.FromBOC(raw)
	if err != nil {
		panic(err)
	}
	return c
}

func mustBlock(h string) *tlb.Block {
	var b tlb.Block
	if err := tlb.LoadFromCell(&b, mustCell(h).MustBeginParse()); err != nil {
		panic(err)
	}
	return &b
}

func mustTx(h string) *tlb.Transaction {
	var tx tlb.Transaction
	if err := tlb.LoadFromCell(&tx, mustCell(h).MustBeginParse()); err != nil {
		panic(err)
	}
	c, err := tlb.ToCell(tx)
	if err != nil {
		panic(err)
	}
	tx.Hash = c.Hash()
	return &tx
}

func mustTxs(raw string) map[string][]*tlb.Transaction {
	m := map[string][]*tlb.Transaction{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		m[fields[0]] = append(m[fields[0]], mustTx(fields[1]))
	}
	return m
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
		// return master block
		_, found := c.shards[seqno]
		if !found {
			return nil, fmt.Errorf("unknown master seqno")
		}
	} else {
		// return shard block
		_, found := c.blocks[seqno]
		if !found {
			return nil, fmt.Errorf("unknown shard seqno")
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
	data, found := c.blocks[block.SeqNo]
	if !found {
		return nil, fmt.Errorf("no block data found")
	}
	return data, nil
}

func (c MockClient) GetBlockTransactionsV2(ctx context.Context, block *ton.BlockIDExt, count uint32, after ...*ton.TransactionID3) ([]ton.TransactionShortInfo, bool, error) {
	return nil, false, nil
}

func (c MockClient) GetBlockShardsInfo(ctx context.Context, master *ton.BlockIDExt) ([]*ton.BlockIDExt, error) {
	seqno, found := c.shards[master.SeqNo]
	if !found {
		return nil, fmt.Errorf("no shard for master")
	}

	// single shard carrying the captured block
	return []*ton.BlockIDExt{
		{Workchain: 0, Shard: shard0, SeqNo: seqno},
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

// ListTransactions returns the captured tx history for the given sender,
// keyed by its address string (matching the TONCAPTURE_TX src field).
func (c MockClient) ListTransactions(ctx context.Context, addr *tonaddress.Address, num uint32, lt uint64, txHash []byte) ([]*tlb.Transaction, error) {
	return c.txs[addr.String()], nil
}

// GetTransaction is unused now that the scanner reads transactions from
// GetBlockData -> ListTransactions; kept only to satisfy the interface.
func (c MockClient) GetTransaction(ctx context.Context, block *ton.BlockIDExt, addr *tonaddress.Address, lt uint64) (*tlb.Transaction, error) {
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
