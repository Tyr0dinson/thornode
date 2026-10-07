package keeperv1

import (
	"math/big"
	"strings"

	storetypes "cosmossdk.io/store/types"
	"google.golang.org/protobuf/encoding/protowire"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperVaultSecuritySuite struct{}

var _ = Suite(&KeeperVaultSecuritySuite{})

// fullDecodeStatusBond is what GetNodeAccount decodes from the given stored bytes.
func fullDecodeStatusBond(k KVStore, bz []byte) (NodeStatus, cosmos.Uint, error) {
	na := NewNodeAccount(GetRandomBech32Addr(), NodeUnknown, common.PubKeySet{}, "", cosmos.ZeroUint(), "", 18)
	err := k.Cdc().Unmarshal(bz, &na)
	return na.Status, na.Bond, err
}

func appendStatusField(bz []byte, v uint64) []byte {
	bz = protowire.AppendTag(append([]byte{}, bz...), nodeAccountStatusField, protowire.VarintType)
	return protowire.AppendVarint(bz, v)
}

func appendBondField(bz []byte, v string) []byte {
	bz = protowire.AppendTag(append([]byte{}, bz...), nodeAccountBondField, protowire.BytesType)
	return protowire.AppendString(bz, v)
}

// appendRaw appends field num with the given wire type; a bytes field gets
// the given bytes as its value, a varint field the first of them.
func appendRaw(bz []byte, num protowire.Number, typ protowire.Type, v ...byte) []byte {
	bz = protowire.AppendTag(append([]byte{}, bz...), num, typ)
	if typ == protowire.VarintType {
		return protowire.AppendVarint(bz, uint64(v[0]))
	}
	return protowire.AppendBytes(bz, v)
}

func (s *KeeperVaultSecuritySuite) TestPeekNodeAccountStatusBond(c *C) {
	_, k := setupKeeperForTest(c)

	var cases [][]byte
	maxBond := cosmos.NewUintFromBigInt(new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)))
	for _, status := range []NodeStatus{NodeUnknown, types.NodeStatus_Whitelisted, NodeStandby, types.NodeStatus_Ready, NodeActive, types.NodeStatus_Disabled} {
		for _, bond := range []cosmos.Uint{cosmos.ZeroUint(), cosmos.NewUint(1), cosmos.NewUint(123456789 * common.One), maxBond} {
			na := GetRandomValidatorNode(status)
			na.Bond = bond
			na.SignerMembership = []string{GetRandomPubKey().String(), GetRandomPubKey().String()}
			bz, err := k.Cdc().Marshal(&na)
			c.Assert(err, IsNil)
			cases = append(cases, bz)
		}
	}
	base := cases[len(cases)-1]
	unknown := protowire.AppendString(protowire.AppendTag(append([]byte{}, base...), 999, protowire.BytesType), "x")
	cases = append(cases,
		nil, // no fields: unknown status, zero bond
		appendStatusField(nil, uint64(NodeActive)),        // no bond
		appendBondField(nil, "77"),                        // no status
		appendStatusField(base, uint64(NodeStandby)),      // repeated status: last wins
		appendBondField(base, "42"),                       // repeated bond: last wins
		appendBondField(base, ""),                         // empty bond keeps the previous value
		appendStatusField(base, 1<<40|uint64(NodeActive)), // status beyond int32 truncates
		unknown, // unknown field is skipped
		appendBondField(appendStatusField(nil, 4), "0"),     // explicit zero bond
		appendStatusField(appendBondField(nil, "5"), 3),     // bond before status
		appendBondField(base, "abc"),                        // malformed bond
		appendBondField(base, "-1"),                         // negative bond
		appendBondField(base, "1"+strings.Repeat("0", 100)), // bond beyond 256 bits
		base[:len(base)-1],                                  // truncated
		[]byte{0x10},                                        // status tag without a value
		protowire.AppendBytes(protowire.AppendTag(nil, nodeAccountStatusField, protowire.BytesType), []byte{1}), // wrong wire type
		protowire.AppendVarint(protowire.AppendTag(nil, nodeAccountBondField, protowire.VarintType), 1),         // wrong wire type
		appendRaw(base, 1, protowire.VarintType, 7),                                                             // node_address as varint
		appendRaw(base, 9, protowire.VarintType, 7),                                                             // signer_membership as varint
		appendRaw(base, 17, protowire.BytesType, 7),                                                             // maintenance as bytes
		appendRaw(base, 3, protowire.BytesType, 0x08, 0x01),                                                     // pub_key_set.secp256k1 as varint
		appendRaw(base, 3, protowire.BytesType, 0x0a, 0x05),                                                     // pub_key_set.secp256k1 truncated
		appendRaw(base, 3, protowire.BytesType, 0x1a, 0x01, 0x78),                                               // pub_key_set unknown field
		appendRaw(base, 3, protowire.BytesType),                                                                 // empty pub_key_set
		appendRaw(base, 18, protowire.VarintType, 7),                                                            // unknown varint field
	)
	fastPath := 0
	for i, bz := range cases {
		wantStatus, wantBond, err := fullDecodeStatusBond(k, bz)
		status, bond, ok := peekNodeAccountStatusBond(bz)
		if err != nil {
			c.Check(ok, Equals, false, Commentf("case %d", i))
			continue
		}
		if !ok {
			// the caller falls back to the full decode, which is the reference
			continue
		}
		fastPath++
		c.Check(status, Equals, wantStatus, Commentf("case %d", i))
		c.Check(bond.String(), Equals, wantBond.String(), Commentf("case %d", i))
	}
	c.Check(fastPath, Equals, 24+13)
}

func (s *KeeperVaultSecuritySuite) TestGetNodeStatusBondByPubKey(c *C) {
	ctx, k := setupKeeperForTest(c)
	store := k.kvStore(ctx)

	var pks []common.PubKey
	for _, status := range []NodeStatus{NodeActive, NodeStandby, types.NodeStatus_Disabled} {
		for _, bond := range []cosmos.Uint{cosmos.ZeroUint(), cosmos.NewUint(1234 * common.One)} {
			na := GetRandomValidatorNode(status)
			na.Bond = bond
			c.Assert(k.SetNodeAccount(ctx, na), IsNil)
			pks = append(pks, na.PubKeySet.Secp256k1)
		}
	}
	// missing node account
	pks = append(pks, GetRandomPubKey())
	// stored bytes that do not decode
	corrupt := GetRandomPubKey()
	corruptAddr, err := corrupt.GetThorAddress()
	c.Assert(err, IsNil)
	store.Set(k.GetKey(prefixNodeAccount, corruptAddr.String()), appendBondField(appendStatusField(nil, 4), "abc"))
	pks = append(pks, corrupt)
	// not a pubkey
	pks = append(pks, common.PubKey("bogus"))

	for i, pk := range pks {
		ctx1 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		na, err1 := k.GetNodeAccountByPubKey(ctx1, pk)
		ctx2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		status, bond, err2 := k.getNodeStatusBondByPubKey(ctx2, pk)
		c.Check(ctx2.GasMeter().GasConsumed(), Equals, ctx1.GasMeter().GasConsumed(), Commentf("pk %d", i))
		c.Check(err2 == nil, Equals, err1 == nil, Commentf("pk %d", i))
		if err1 != nil {
			c.Check(err2.Error(), Equals, err1.Error(), Commentf("pk %d", i))
			continue
		}
		c.Check(status, Equals, na.Status, Commentf("pk %d", i))
		c.Check(bond.String(), Equals, na.Bond.String(), Commentf("pk %d", i))
	}
}

func (s *KeeperVaultSecuritySuite) TestGetPoolMemo(c *C) {
	ctx, k := setupKeeperForTest(c)
	store := k.kvStore(ctx)

	for i, asset := range []common.Asset{common.ETHAsset, common.BTCAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(uint64(i+1) * 100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(uint64(i+1) * 7 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
	}
	// stored bytes that do not decode
	store.Set(k.GetKey(prefixPool, common.DOGEAsset.String()), []byte{0x0a})

	memo := make(map[string]poolMemo)
	check := func(asset common.Asset) {
		ctx1 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		want, err1 := k.GetPool(ctx1, asset)
		ctx2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		got, err2 := k.getPoolMemo(ctx2, asset, memo)
		c.Check(ctx2.GasMeter().GasConsumed(), Equals, ctx1.GasMeter().GasConsumed(), Commentf("%s", asset))
		c.Check(err2 == nil, Equals, err1 == nil, Commentf("%s", asset))
		if err1 != nil {
			c.Check(err2.Error(), Equals, err1.Error(), Commentf("%s", asset))
		}
		c.Check(got, DeepEquals, want, Commentf("%s", asset))
	}
	for i := 0; i < 2; i++ { // decoded, then from memo
		check(common.ETHAsset)
		check(common.BTCAsset)
		check(common.LTCAsset) // missing
		check(common.DOGEAsset)
	}
	c.Check(memo, HasLen, 2)

	// a changed pool is decoded again
	pool, err := k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	pool.BalanceAsset = cosmos.NewUint(3)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	check(common.ETHAsset)
	got, err := k.getPoolMemo(ctx, common.ETHAsset, memo)
	c.Assert(err, IsNil)
	c.Check(got.BalanceAsset.Uint64(), Equals, uint64(3))
}
