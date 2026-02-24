package cardano

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/fxamacker/cbor/v2"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type UtilTestSuite struct{}

var _ = Suite(&UtilTestSuite{})

// ---------------------------------------------------------------------------
// AddressFromBytes
// ---------------------------------------------------------------------------

func (s *UtilTestSuite) TestAddressFromBytes(c *C) {
	hexStr := "881982ddba21b3093be9a0cfeb32386c517e13e8799ed03fbfd482e4505eb2dc"
	mockAddr := "addr_test1vze3395j26tfwymgtmk4e26gmam7vaq9vyejtj0lkpj70rc20g9xv"
	mainAddr := "addr1vxe3395j26tfwymgtmk4e26gmam7vaq9vyejtj0lkpj70rc38ueff"
	bz, err := hex.DecodeString(hexStr)
	c.Assert(err, IsNil)

	addr, err := AddressFromBytes(bz)
	c.Assert(err, IsNil)
	switch common.CurrentChainNetwork {
	case common.MockNet:
		c.Assert(addr.String(), Equals, mockAddr)
	default:
		c.Assert(addr.String(), Equals, mainAddr)
	}
}

// ---------------------------------------------------------------------------
// ExtractMemo
// ---------------------------------------------------------------------------

func (s *UtilTestSuite) TestExtractMemo(c *C) {
	testCases := []struct {
		Name   string
		Result string
		Meta   lcommon.TransactionMetadatum
	}{
		{
			Name:   "Simple memo with one item",
			Result: "cardano",
			Meta: lcommon.MetaMap{
				Pairs: []lcommon.MetaPair{
					{
						Key: lcommon.MetaInt{Value: big.NewInt(674)},
						Value: lcommon.MetaMap{
							Pairs: []lcommon.MetaPair{{
								Key: lcommon.MetaText{Value: "msg"},
								Value: lcommon.MetaList{
									Items: []lcommon.TransactionMetadatum{
										lcommon.MetaText{Value: "cardano"},
									},
								},
							}},
						},
					},
				},
			},
		},
		{
			Name:   "Simple memo with multiple items",
			Result: "abcxyz123",
			Meta: lcommon.MetaMap{
				Pairs: []lcommon.MetaPair{
					{
						Key: lcommon.MetaInt{Value: big.NewInt(674)},
						Value: lcommon.MetaMap{
							Pairs: []lcommon.MetaPair{{
								Key: lcommon.MetaText{Value: "msg"},
								Value: lcommon.MetaList{
									Items: []lcommon.TransactionMetadatum{
										lcommon.MetaText{Value: "abc"},
										lcommon.MetaText{Value: "xyz"},
										lcommon.MetaText{Value: "123"},
									},
								},
							}},
						},
					},
				},
			},
		},
		{
			Name: "Wrong meta id",
			Meta: lcommon.MetaMap{
				Pairs: []lcommon.MetaPair{
					{
						Key: lcommon.MetaInt{Value: big.NewInt(123)},
						Value: lcommon.MetaMap{
							Pairs: []lcommon.MetaPair{{
								Key: lcommon.MetaText{Value: "msg"},
								Value: lcommon.MetaList{
									Items: []lcommon.TransactionMetadatum{
										lcommon.MetaText{Value: "won't appear"},
									},
								},
							}},
						},
					},
				},
			},
		},
		{
			Name: "Wrong msg key",
			Meta: lcommon.MetaMap{
				Pairs: []lcommon.MetaPair{
					{
						Key: lcommon.MetaInt{Value: big.NewInt(674)},
						Value: lcommon.MetaMap{
							Pairs: []lcommon.MetaPair{{
								Key: lcommon.MetaText{Value: "message"},
								Value: lcommon.MetaList{
									Items: []lcommon.TransactionMetadatum{
										lcommon.MetaText{Value: "won't appear"},
									},
								},
							}},
						},
					},
				},
			},
		},
		{
			Name: "Empty meta map",
			Meta: lcommon.MetaMap{},
		},
		{
			Name: "Non meta map input",
			Meta: lcommon.MetaText{Value: "text"},
		},
		{
			Name: "Msg value is not a list",
			Meta: lcommon.MetaMap{
				Pairs: []lcommon.MetaPair{
					{
						Key: lcommon.MetaInt{Value: big.NewInt(674)},
						Value: lcommon.MetaMap{
							Pairs: []lcommon.MetaPair{{
								Key:   lcommon.MetaText{Value: "msg"},
								Value: lcommon.MetaText{Value: "not a list"},
							}},
						},
					},
				},
			},
		},
		{
			Name:   "Skip non text item",
			Result: "thorchain",
			Meta: lcommon.MetaMap{
				Pairs: []lcommon.MetaPair{
					{
						Key: lcommon.MetaInt{Value: big.NewInt(674)},
						Value: lcommon.MetaMap{
							Pairs: []lcommon.MetaPair{{
								Key: lcommon.MetaText{Value: "msg"},
								Value: lcommon.MetaList{
									Items: []lcommon.TransactionMetadatum{
										lcommon.MetaText{Value: "thor"},
										lcommon.MetaInt{Value: big.NewInt(123)}, // non-text, skipped
										lcommon.MetaText{Value: "chain"},
									},
								},
							}},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		hint := Commentf("Test: %s", tc.Name)
		c.Assert(ExtractMemo(tc.Meta, 0), Equals, tc.Result, hint)
	}
}

// ---------------------------------------------------------------------------
// CreateMemo
// ---------------------------------------------------------------------------

func (s *UtilTestSuite) TestCreateMemo(c *C) {
	testCases := []struct {
		Name string
		Memo string
	}{
		{
			Name: "Memo less than 64 chars",
			Memo: "abcxyz123",
		},
		{
			Name: "Memo more than 64 chars",
			Memo: strings.Repeat("a", 200),
		},
		{
			Name: "Memo exactly 64 chars",
			Memo: strings.Repeat("x", 64),
		},
	}

	for _, tc := range testCases {
		meta, hash, err := CreateMemo(tc.Memo)
		c.Assert(err, IsNil)
		c.Assert(hash, NotNil)
		c.Assert(meta, NotNil)
		c.Assert(ExtractMemo(meta, 0), Equals, tc.Memo)
	}

	meta, hash, err := CreateMemo("")
	c.Assert(err, NotNil)
	c.Assert(hash, IsNil)
	c.Assert(meta, IsNil)
}

// ---------------------------------------------------------------------------
// CreateConwayTx
// ---------------------------------------------------------------------------

func makeTestAddress(c *C, seed string) ledger.Address {
	addr, err := AddressFromBytes([]byte(seed))
	c.Assert(err, IsNil)
	return addr
}

func makeUtxos(amounts ...uint64) []Utxo {
	utxos := make([]Utxo, len(amounts))
	for i, amount := range amounts {
		utxos[i] = Utxo{
			Hash:   lcommon.Blake2b256Hash([]byte{uint8(i)}).String(),
			Index:  0,
			Amount: amount,
		}
	}
	return utxos
}

func (s *UtilTestSuite) TestCreateConwayTx(c *C) {
	testCases := []struct {
		Name    string
		Amount  uint64
		Inputs  []uint64
		Memo    string
		MinFeeA uint64
		MinFeeB uint64
		Fee     uint64
		Change  uint64
		Size    int
		Error   string
	}{
		{
			Name:    "Valid tx with memo with fees",
			Amount:  3_000_000,
			Inputs:  []uint64{10_000_000},
			MinFeeB: 199_995,
			Change:  6_800_000,
			Fee:     200_000,
			Memo:    "SWAP:ETH.ETH:0xABC",
			Size:    301,
		},
		{
			Name:    "Valid tx with memo with different fees",
			Amount:  1_000_000,
			Inputs:  []uint64{5_000_000},
			MinFeeA: 44,
			MinFeeB: 155_381,
			Change:  3_831_370,
			Fee:     168_630,
			Memo:    "SWAP:BTC.BTC:bc1tc",
			Size:    301,
		},
		{
			Name:   "Verify estimated size constant",
			Amount: 3_000_000,
			Inputs: []uint64{1e6, 1e6, 1e6, 1e6, 1e6},
			Memo:   strings.Repeat("a", 150),
			Size:   estimatedTxSize,
		},
		{
			Name:   "Valid tx without memo",
			Amount: 1_000_000,
			Inputs: []uint64{5_000_000},
			Memo:   "",
		},
		{
			Name:   "Amount greater than inputs",
			Amount: 5_000_000,
			Inputs: []uint64{1_000_000},
			Error:  "insufficient funds",
		},
		{
			Name:   "Not enough inputs for 2 ADA security margin",
			Amount: 5_000_000,
			Inputs: []uint64{6_000_000},
			Error:  "insufficient funds",
		},
		{
			Name:    "Exactly matching inputs",
			Amount:  5_000_000,
			Inputs:  []uint64{7_200_000},
			MinFeeB: 200_000,
		},
	}

	for _, tc := range testCases {
		from := makeTestAddress(c, "from-address")
		to := makeTestAddress(c, "to-address")
		utxos := makeUtxos(tc.Inputs...)

		tx, err := CreateConwayTx(
			from, to, tc.Amount, tc.MinFeeA, tc.MinFeeB, tc.Memo, utxos,
		)
		if tc.Error == "" {
			c.Assert(err, IsNil)
			c.Assert(tx, NotNil)

			c.Assert(tx.Inputs(), HasLen, len(tc.Inputs))
			c.Assert(tx.Outputs(), HasLen, 2)
			c.Assert(tx.Outputs()[1].Amount().Int64() > 1e6, Equals, true)

			c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, tc.Amount)

			c.Assert(ExtractMemo(tx.Metadata(), 0), Equals, tc.Memo)

			if tc.Size > 0 {
				var bz []byte
				bz, err = cbor.Marshal(tx)
				c.Assert(err, IsNil)
				c.Assert(bz, HasLen, tc.Size)
			}

			if tc.Fee > 0 {
				c.Assert(tx.Fee().Uint64(), Equals, tc.Fee)
			}

			if tc.Change > 0 {
				c.Assert(tx.Body.TxOutputs[1].Amount().Uint64(), Equals, tc.Change)
			}
		} else {
			c.Assert(strings.Contains(err.Error(), tc.Error), Equals, true)
		}
	}
}
