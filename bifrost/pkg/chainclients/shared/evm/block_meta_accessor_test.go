package evm

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	. "gopkg.in/check.v1"

	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

// PrefixTxStorage declares prefix to use in leveldb to avoid conflicts
const (
	PrefixBlockMeta    = `eth-blockmeta-`
	PrefixSignedTxItem = `signed-txitem-`
)

type EthereumBlockMetaAccessorTestSuite struct{}

var _ = Suite(
	&EthereumBlockMetaAccessorTestSuite{},
)

func CreateBlock(height int) (*etypes.Header, error) {
	strHeight := fmt.Sprintf("%x", height)
	blockJson := `{
		"parentHash":"0x8b535592eb3192017a527bbf8e3596da86b3abea51d6257898b2ced9d3a83826",
		"difficulty": "0x31962a3fc82b",
		"extraData": "0x4477617266506f6f6c",
		"gasLimit": "0x47c3d8",
		"gasUsed": "0x0",
		"hash": "0x78bfef68fccd4507f9f4804ba5c65eb2f928ea45b3383ade88aaa720f1209cba",
		"logsBloom": "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
		"miner": "0x2a65aca4d5fc5b5c859090a6c34d164135398226",
		"nonce": "0xa5e8fb780cc2cd5e",
		"number": "0x` + strHeight + `",
		"receiptsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		"sha3Uncles": "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		"size": "0x20e",
		"stateRoot": "0xdc6ed0a382e50edfedb6bd296892690eb97eb3fc88fd55088d5ea753c48253dc",
		"timestamp": "0x579f4981",
		"totalDifficulty": "0x25cff06a0d96f4bee",
		"transactionsRoot": "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"
	}`
	var header *etypes.Header
	if err := json.Unmarshal([]byte(blockJson), &header); err != nil {
		return nil, err
	}
	return header, nil
}

func (s *EthereumBlockMetaAccessorTestSuite) TestNewBlockMetaAccessor(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	dbBlockMetaAccessor, err := NewLevelDBBlockMetaAccessor(PrefixBlockMeta, PrefixSignedTxItem, db)
	c.Assert(err, IsNil)
	c.Assert(dbBlockMetaAccessor, NotNil)
	c.Assert(db.Close(), IsNil)
}

func (s *EthereumBlockMetaAccessorTestSuite) TestBlockMetaAccessor(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	blockMetaAccessor, err := NewLevelDBBlockMetaAccessor(PrefixBlockMeta, PrefixSignedTxItem, db)
	c.Assert(err, IsNil)
	c.Assert(blockMetaAccessor, NotNil)

	block, err := CreateBlock(1722479)
	c.Assert(err, IsNil)
	blockMeta := evmtypes.NewBlockMeta(block, stypes.TxIn{TxArray: []*stypes.TxInItem{{Tx: "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"}}})
	c.Assert(blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)

	key := blockMetaAccessor.getBlockMetaKey(blockMeta.Height)
	c.Assert(key, Equals, fmt.Sprintf(PrefixBlockMeta+"%d", blockMeta.Height))

	bm, err := blockMetaAccessor.GetBlockMeta(blockMeta.Height)
	c.Assert(err, IsNil)
	c.Assert(bm, NotNil)

	nbm, err := blockMetaAccessor.GetBlockMeta(1024)
	c.Assert(err, IsNil)
	c.Assert(nbm, IsNil)

	for i := 0; i < 1024; i++ {
		block, err = CreateBlock(i)
		c.Assert(err, IsNil)
		bm = evmtypes.NewBlockMeta(block, stypes.TxIn{TxArray: []*stypes.TxInItem{{Tx: "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"}}})
		c.Assert(blockMetaAccessor.SaveBlockMeta(bm.Height, bm), IsNil)
	}
	blockMetas, err := blockMetaAccessor.GetBlockMetas()
	c.Assert(err, IsNil)
	c.Assert(blockMetas, HasLen, 1025)
	c.Assert(blockMetaAccessor.PruneBlockMeta(1000), IsNil)
	allBlockMetas, err := blockMetaAccessor.GetBlockMetas()
	c.Assert(err, IsNil)
	c.Assert(allBlockMetas, HasLen, 25)
	c.Assert(db.Close(), IsNil)
}

// TestBlockMetaUpdates covers the accessor-owned helpers for block meta updates.
func (s *EthereumBlockMetaAccessorTestSuite) TestBlockMetaUpdates(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	defer db.Close()

	blockMetaAccessor, err := NewLevelDBBlockMetaAccessor(PrefixBlockMeta, PrefixSignedTxItem, db)
	c.Assert(err, IsNil)

	block, err := CreateBlock(100)
	c.Assert(err, IsNil)
	blockMeta := evmtypes.NewBlockMeta(block, stypes.TxIn{
		TxArray: []*stypes.TxInItem{{Tx: "0xfirst"}},
	})
	c.Assert(blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)

	found, err := blockMetaAccessor.AppendBlockMetaTransaction(blockMeta.Height, evmtypes.TransactionMeta{
		Hash:        "0xsecond",
		BlockHeight: blockMeta.Height,
	})
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)

	found, err = blockMetaAccessor.AppendBlockMetaTransaction(404, evmtypes.TransactionMeta{
		Hash:        "0xmissing",
		BlockHeight: 404,
	})
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	found, err = blockMetaAccessor.AppendBlockMetaTransaction(blockMeta.Height, evmtypes.TransactionMeta{
		Hash:        "0xsecond",
		BlockHeight: blockMeta.Height,
	})
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)

	blockMeta, err = blockMetaAccessor.GetBlockMeta(blockMeta.Height)
	c.Assert(err, IsNil)
	c.Assert(blockMeta.Transactions, HasLen, 2)

	appendStarted := make(chan struct{})
	appendDone := make(chan error, 1)
	go func() {
		<-appendStarted
		_, appendErr := blockMetaAccessor.AppendBlockMetaTransaction(blockMeta.Height, evmtypes.TransactionMeta{
			Hash:        "0xthird",
			BlockHeight: blockMeta.Height,
		})
		appendDone <- appendErr
	}()

	err = blockMetaAccessor.UpdateBlockMetas(func(blockMeta *evmtypes.BlockMeta) (bool, error) {
		close(appendStarted)
		select {
		case appendErr := <-appendDone:
			if appendErr != nil {
				return false, appendErr
			}
		case <-time.After(time.Second):
			return false, errors.New("concurrent append blocked")
		}
		blockMeta.BlockHash = "updated"
		return true, nil
	})
	c.Assert(err, IsNil)

	blockMeta, err = blockMetaAccessor.GetBlockMeta(blockMeta.Height)
	c.Assert(err, IsNil)
	c.Assert(blockMeta.BlockHash, Equals, "updated")
	c.Assert(blockMeta.Transactions, HasLen, 3)

	err = blockMetaAccessor.UpdateBlockMetas(func(blockMeta *evmtypes.BlockMeta) (bool, error) {
		blockMeta.BlockHash = "skipped"
		return false, nil
	})
	c.Assert(err, IsNil)

	blockMeta, err = blockMetaAccessor.GetBlockMeta(blockMeta.Height)
	c.Assert(err, IsNil)
	c.Assert(blockMeta.BlockHash, Equals, "updated")

	boom := errors.New("boom")
	err = blockMetaAccessor.UpdateBlockMetas(func(blockMeta *evmtypes.BlockMeta) (bool, error) {
		return false, boom
	})
	c.Assert(err, Equals, boom)
}

func (s *EthereumBlockMetaAccessorTestSuite) TestBlockMetaUpdateConflictsWithConcurrentHashUpdate(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	defer db.Close()

	blockMetaAccessor, err := NewLevelDBBlockMetaAccessor(PrefixBlockMeta, PrefixSignedTxItem, db)
	c.Assert(err, IsNil)

	block, err := CreateBlock(200)
	c.Assert(err, IsNil)
	blockMeta := evmtypes.NewBlockMeta(block, stypes.TxIn{
		TxArray: []*stypes.TxInItem{{Tx: "0xfirst"}},
	})
	c.Assert(blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)

	updateStarted := make(chan struct{})
	saveDone := make(chan error, 1)
	go func() {
		<-updateStarted
		current, getErr := blockMetaAccessor.GetBlockMeta(blockMeta.Height)
		if getErr != nil {
			saveDone <- getErr
			return
		}
		current.BlockHash = "concurrent"
		saveDone <- blockMetaAccessor.SaveBlockMeta(current.Height, current)
	}()

	err = blockMetaAccessor.UpdateBlockMetas(func(blockMeta *evmtypes.BlockMeta) (bool, error) {
		close(updateStarted)
		select {
		case saveErr := <-saveDone:
			if saveErr != nil {
				return false, saveErr
			}
		case <-time.After(time.Second):
			return false, errors.New("concurrent save blocked")
		}
		blockMeta.BlockHash = "callback"
		return true, nil
	})
	c.Assert(err, ErrorMatches, "block meta block hash changed during update:.*")

	blockMeta, err = blockMetaAccessor.GetBlockMeta(blockMeta.Height)
	c.Assert(err, IsNil)
	c.Assert(blockMeta.BlockHash, Equals, "concurrent")
}

func (s *EthereumBlockMetaAccessorTestSuite) TestBlockMetaUpdatePreservesConcurrentHashWhenCallbackDoesNotChangeHash(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	defer db.Close()

	blockMetaAccessor, err := NewLevelDBBlockMetaAccessor(PrefixBlockMeta, PrefixSignedTxItem, db)
	c.Assert(err, IsNil)

	block, err := CreateBlock(250)
	c.Assert(err, IsNil)
	blockMeta := evmtypes.NewBlockMeta(block, stypes.TxIn{
		TxArray: []*stypes.TxInItem{{Tx: "0xfirst"}},
	})
	c.Assert(blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)

	updateStarted := make(chan struct{})
	saveDone := make(chan error, 1)
	go func() {
		<-updateStarted
		current, getErr := blockMetaAccessor.GetBlockMeta(blockMeta.Height)
		if getErr != nil {
			saveDone <- getErr
			return
		}
		current.BlockHash = "concurrent"
		saveDone <- blockMetaAccessor.SaveBlockMeta(current.Height, current)
	}()

	err = blockMetaAccessor.UpdateBlockMetas(func(blockMeta *evmtypes.BlockMeta) (bool, error) {
		close(updateStarted)
		select {
		case saveErr := <-saveDone:
			if saveErr != nil {
				return false, saveErr
			}
		case <-time.After(time.Second):
			return false, errors.New("concurrent save blocked")
		}
		blockMeta.Transactions = append(blockMeta.Transactions, evmtypes.TransactionMeta{
			Hash:        "0xsecond",
			BlockHeight: blockMeta.Height,
		})
		return true, nil
	})
	c.Assert(err, IsNil)

	blockMeta, err = blockMetaAccessor.GetBlockMeta(blockMeta.Height)
	c.Assert(err, IsNil)
	c.Assert(blockMeta.BlockHash, Equals, "concurrent")
	c.Assert(blockMeta.Transactions, HasLen, 2)
}

func (s *EthereumBlockMetaAccessorTestSuite) TestCloneBlockMetaCopiesEmptyTransactions(c *C) {
	blockMeta := &evmtypes.BlockMeta{
		Height:       300,
		Transactions: make([]evmtypes.TransactionMeta, 0, 1),
	}

	clone := cloneBlockMeta(blockMeta)
	c.Assert(clone.Transactions, NotNil)

	clone.Transactions = append(clone.Transactions, evmtypes.TransactionMeta{
		Hash:        "0xadded",
		BlockHeight: blockMeta.Height,
	})
	c.Assert(blockMeta.Transactions, HasLen, 0)
}
