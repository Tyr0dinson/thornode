package cardano

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
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
			Name: "Nil input",
			Meta: nil,
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
		{
			Name:   "Long memo is truncated",
			Result: strings.Repeat("x", 250),
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
										lcommon.MetaText{Value: strings.Repeat("x", 999)},
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
		Name           string
		Amount         uint64
		Inputs         []uint64
		Memo           string
		MinFeeA        uint64
		MinFeeB        uint64
		MaxFee         uint64
		ExpectedFee    uint64
		ExpectedChange uint64
		ExpectedSize   int
		ExpectedError  string
		ExpectedInputs []uint64
	}{
		{
			Name:           "Valid tx with memo with fees",
			Amount:         3_000_000,
			Inputs:         []uint64{10_000_000},
			MinFeeB:        199_995,
			Memo:           "SWAP:ETH.ETH:0xABC",
			ExpectedFee:    200_000,
			ExpectedSize:   301,
			ExpectedChange: 6_800_000,
		},
		{
			Name:           "Valid tx with memo with different fees",
			Amount:         1_000_000,
			Inputs:         []uint64{7_000_000},
			MinFeeA:        44,
			MinFeeB:        155_381,
			Memo:           "SWAP:BTC.BTC:bc1tc",
			ExpectedFee:    168_630,
			ExpectedSize:   301,
			ExpectedChange: 5_831_370,
		},
		{
			Name:           "Verify estimated size constant",
			Amount:         5_000_000,
			MaxFee:         500_000,
			Inputs:         []uint64{2e6, 4e6, 1e6, 2e6, 1e6, 3e6},
			Memo:           strings.Repeat("a", 150),
			ExpectedSize:   estimatedTxSize,
			ExpectedInputs: []uint64{1e6, 1e6, 2e6, 4e6, 2e6},
		},
		{
			Name:   "Valid tx without memo",
			Amount: 1_000_000,
			Inputs: []uint64{7_000_000},
		},
		{
			Name:          "Amount greater than inputs",
			Amount:        5_000_000,
			Inputs:        []uint64{1_000_000},
			ExpectedError: "insufficient funds",
		},
		{
			Name:          "Not enough inputs to cover amount plus max fee",
			Amount:        5_000_000,
			Inputs:        []uint64{5_400_000},
			ExpectedError: "insufficient funds",
		},
		{
			Name:           "Exactly matching inputs",
			Amount:         5_000_000,
			Inputs:         []uint64{10_200_000},
			MinFeeB:        200_000,
			ExpectedFee:    200_005,
			ExpectedChange: 4_999_995,
		},
		{
			Name:          "Empty utxos",
			Amount:        1_000_000,
			Inputs:        []uint64{},
			ExpectedError: "insufficient funds",
		},
		{
			Name:          "Single input equals amount (no room for fee)",
			Amount:        5_000_000,
			Inputs:        []uint64{5_000_000},
			ExpectedError: "insufficient funds",
		},
	}

	for _, tc := range testCases {
		from := makeTestAddress(c, "from-address")
		to := makeTestAddress(c, "to-address")
		utxos := makeUtxos(tc.Inputs...)

		maxFee := tc.MaxFee
		if maxFee == 0 {
			maxFee = 500_000
		}

		tx, err := CreateConwayTx(
			from, to, tc.Amount, tc.MinFeeA, tc.MinFeeB, maxFee, tc.Memo, utxos,
		)
		if tc.ExpectedError == "" {
			c.Assert(err, IsNil)
			c.Assert(tx, NotNil)

			c.Assert(tx.Inputs(), HasLen, len(tc.Inputs))
			c.Assert(tx.Outputs(), HasLen, 2)
			c.Assert(tx.Outputs()[1].Amount().Int64() > 1e6, Equals, true)

			c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, tc.Amount)

			c.Assert(ExtractMemo(tx.Metadata(), 0), Equals, tc.Memo)

			if tc.ExpectedSize > 0 {
				var bz []byte
				bz, err = cbor.Encode(tx)
				c.Assert(err, IsNil)
				c.Assert(bz, HasLen, tc.ExpectedSize)
			}

			if tc.ExpectedFee > 0 {
				c.Assert(tx.Fee().Uint64(), Equals, tc.ExpectedFee)
			}

			if tc.ExpectedChange > 0 {
				c.Assert(tx.Body.TxOutputs[1].Amount().Uint64(), Equals, tc.ExpectedChange)
			}
		} else {
			c.Assert(strings.Contains(err.Error(), tc.ExpectedError), Equals, true)
		}
	}
}

func (s *UtilTestSuite) TestCreateConwayTxSubDustChange(c *C) {
	// when change is below dustThreshold (1e6), it should be folded into the fee
	// resulting in only 1 output (no change output)
	from := makeTestAddress(c, "from-address")
	to := makeTestAddress(c, "to-address")
	utxos := makeUtxos(2_000_000)

	tx, err := CreateConwayTx(from, to, 1_500_000, 0, 0, 300_000, "", utxos)
	c.Assert(err, IsNil)
	c.Assert(tx, NotNil)

	// change (2M - 1.5M - fee) is sub-dust, so only 1 output
	c.Assert(len(tx.Body.TxOutputs), Equals, 1)
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(1_500_000))

	// fee should be total - amount (entire remainder goes to fee)
	c.Assert(tx.Fee().Uint64(), Equals, uint64(500_000))
}

func (s *UtilTestSuite) TestCreateConwayTxUtxoConsolidation(c *C) {
	// when there are more than numSmallUtxos (3) utxos, the 3 smallest
	// should be moved to the front for consolidation
	from := makeTestAddress(c, "from-address")
	to := makeTestAddress(c, "to-address")

	// 6 utxos: the 3 smallest (1e6, 1e6, 2e6) + the largest (9e6) should be consumed
	utxos := makeUtxos(4e6, 1e6, 2e6, 1e6, 3e6, 9e6)

	tx, err := CreateConwayTx(from, to, 4_500_000, 0, 0, 500_000, "", utxos)
	c.Assert(err, IsNil)
	c.Assert(tx, NotNil)
	c.Assert(tx.Body.TxOutputs, HasLen, 4)
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(1e6))
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(1e6))
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(2e6))
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(9e6))
}

func (s *UtilTestSuite) TestCreateConwayTxFeeExceedsMaxFee(c *C) {
	// when calculated fee exceeds maxFee, should return error
	from := makeTestAddress(c, "from-address")
	to := makeTestAddress(c, "to-address")
	utxos := makeUtxos(10_000_000)

	// set maxFee to 1 (impossibly low)
	_, err := CreateConwayTx(from, to, 1_000_000, 44, 155_381, 1, "", utxos)
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "fee exceeds max fee"), Equals, true)
}

func (s *UtilTestSuite) TestCreateConwayTxMultipleInputsNeeded(c *C) {
	// needs multiple small utxos to cover the amount
	from := makeTestAddress(c, "from-address")
	to := makeTestAddress(c, "to-address")
	utxos := makeUtxos(1e6, 1e6, 1e6, 1e6, 5e6)

	tx, err := CreateConwayTx(from, to, 3_000_000, 0, 0, 500_000, "", utxos)
	c.Assert(err, IsNil)
	c.Assert(tx, NotNil)
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(3_000_000))
	c.Assert(tx.Body.TxOutputs, HasLen, 3)
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(1e6))
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(1e6))
	c.Assert(tx.Body.TxOutputs[0].Amount().Uint64(), Equals, uint64(1e6))
}
