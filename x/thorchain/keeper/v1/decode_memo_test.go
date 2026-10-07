package keeperv1

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"google.golang.org/protobuf/encoding/protowire"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type DecodeMemoSuite struct{}

var _ = Suite(&DecodeMemoSuite{})

// refPaths lists the fields of t, recursively, whose values can share memory
// with a copy of them: pointers, slices, maps, interfaces, funcs and chans.
// Strings are immutable and not listed.
func refPaths(t reflect.Type, prefix string) []string {
	var paths []string
	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			paths = append(paths, refPaths(f.Type, prefix+"."+f.Name)...)
		}
	case reflect.Slice:
		paths = append(paths, prefix)
		paths = append(paths, refPaths(t.Elem(), prefix+"[]")...)
	case reflect.Pointer, reflect.Map, reflect.Interface, reflect.Func, reflect.Chan:
		paths = append(paths, prefix)
	}
	sort.Strings(paths)
	return paths
}

// pbField appends field num with the raw bytes v.
func pbField(bz []byte, num protowire.Number, v string) []byte {
	bz = protowire.AppendTag(bz, num, protowire.BytesType)
	return protowire.AppendString(bz, v)
}

// testVault is a vault with every field set.
func testVault(i int) Vault {
	v := types.GetRandomVault()
	v.BlockHeight = int64(100 + i)
	v.Type = AsgardVault
	v.Status = ActiveVault
	v.StatusSince = int64(50 + i)
	v.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(uint64(i+1)*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(i+2)*12345)),
		common.NewCoin(common.ATOMAsset, cosmos.ZeroUint()),
	)
	v.Coins[1].Decimals = 18
	v.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(uint64(i)+7)))
	v.Membership = []string{GetRandomPubKey().String(), GetRandomPubKey().String()}
	v.Chains = []string{"BTC", "ETH", "THOR"}
	v.InboundTxCount = int64(i)
	v.OutboundTxCount = int64(2 * i)
	v.PendingTxBlockHeights = []int64{int64(i), int64(i + 3)}
	v.Routers = []ChainContract{{Chain: common.ETHChain, Router: GetRandomETHAddress()}}
	v.Frozen = []string{"frozen"}
	v.PubKeyEddsa = GetRandomPubKey()
	v.VaultCreationHeightXmr = int64(9 + i)
	v.PubKeyFrost = GetRandomPubKey()
	return v
}

func vaultTestBlobs(c *C, k KVStore) [][]byte {
	var blobs [][]byte
	for i := 0; i < 3; i++ {
		v := testVault(i)
		bz, err := k.cdc.Marshal(&v)
		c.Assert(err, IsNil)
		blobs = append(blobs, bz)
	}
	empty := Vault{PubKey: GetRandomPubKey()}
	bz, err := k.cdc.Marshal(&empty)
	c.Assert(err, IsNil)
	blobs = append(blobs, bz)
	base := blobs[1]
	coin := func(amount string) string {
		return string(pbField(pbField(nil, 1, "\x0a\x03BTC\x12\x03BTC\x1a\x03BTC"), 2, amount))
	}
	// an unpacked pending height (field 11 as a varint) after the packed ones
	unpacked := protowire.AppendTag(append([]byte{}, base...), 11, protowire.VarintType)
	unpacked = protowire.AppendVarint(unpacked, 77)
	blobs = append(blobs,
		pbField(append([]byte{}, base...), 3, coin("")),       // coin with an empty amount
		pbField(append([]byte{}, base...), 7, ""),             // empty member
		pbField(append([]byte{}, base...), 99, "unknown"),     // unknown field
		pbField(append([]byte{}, base...), 11, ""),            // empty packed heights
		pbField(append([]byte{}, base...), 22, "\x0a\x03ETH"), // router without an address
		unpacked,
		pbField(append([]byte{}, base...), 3, coin("-1")),   // rejected Uint
		pbField(append([]byte{}, base...), 28, coin("x")),   // rejected Uint
		append(append([]byte{}, base...), 0x1a, 0x05, 0x0a), // truncated
		append(append([]byte{}, base...), 0x10, 0x01),       // wrong wire type
		[]byte{0xff},
		[]byte{},
		nil,
	)
	return blobs
}

func (s *DecodeMemoSuite) TestVaultRefFields(c *C) {
	// cloneVault must copy every one of these; a new reference field must be
	// added to cloneVault and to TestVaultMutationIsolation
	c.Check(refPaths(reflect.TypeOf(Vault{}), ""), DeepEquals, []string{
		".Chains", ".Coins", ".Coins[].Amount.i", ".Deficit", ".Deficit[].Amount.i",
		".Frozen", ".Membership", ".PendingTxBlockHeights", ".Routers",
	})
}

func (s *DecodeMemoSuite) TestVaultMatchesCodec(c *C) {
	ctx, k := setupKeeperForTest(c)
	store := k.kvStore(ctx)
	for i, bz := range vaultTestBlobs(c, k) {
		pk := GetRandomPubKey()
		want := Vault{BlockHeight: ctx.BlockHeight(), PubKey: pk}
		wantErr := k.cdc.Unmarshal(bz, &want)
		key := []byte(fmt.Sprintf("memo-test/vault/%d", i))
		for pass := 0; pass < 2; pass++ { // miss, then hit
			got := Vault{InboundTxCount: -1}
			ok := k.memoDecodeVault(key, bz, &got)
			c.Assert(ok, Equals, wantErr == nil, Commentf("blob %d pass %d", i, pass))
			if ok {
				c.Assert(got, DeepEquals, want, Commentf("blob %d pass %d", i, pass))
			} else {
				c.Assert(got, DeepEquals, Vault{InboundTxCount: -1})
			}
		}

		// through the keeper: same vault and error, same gas on miss and hit
		if len(bz) == 0 {
			continue // the store does not hold empty values
		}
		if want.PubKey.IsEmpty() {
			want.PubKey = pk
		}
		if want.BlockHeight == 0 {
			want.BlockHeight = ctx.BlockHeight()
		}
		store.Set(k.GetKey(prefixVault, pk.String()), bz)
		var gas []uint64
		for pass := 0; pass < 2; pass++ {
			gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
			got, err := k.GetVault(gctx, pk)
			gas = append(gas, gctx.GasMeter().GasConsumed())
			c.Assert(err == nil, Equals, wantErr == nil, Commentf("blob %d pass %d", i, pass))
			if err != nil {
				c.Check(strings.HasSuffix(err.Error(), ": "+wantErr.Error()), Equals, true, Commentf("blob %d pass %d: %s", i, pass, err))
			}
			c.Assert(got, DeepEquals, want, Commentf("blob %d pass %d", i, pass))
		}
		c.Check(gas[1], Equals, gas[0], Commentf("blob %d", i))
		store.Delete(k.GetKey(prefixVault, pk.String()))
	}
}

func (s *DecodeMemoSuite) TestAsgardVaultsMatchCodec(c *C) {
	ctx, k := setupKeeperForTest(c)
	var want Vaults
	for i := 0; i < 3; i++ {
		v := testVault(i)
		v.Deficit = nil
		c.Assert(k.SetVault(ctx, v), IsNil)
		bz, err := k.cdc.Marshal(&v)
		c.Assert(err, IsNil)
		var decoded Vault
		c.Assert(k.cdc.Unmarshal(bz, &decoded), IsNil)
		want = append(want, decoded)
	}
	var gas []uint64
	for pass := 0; pass < 2; pass++ {
		gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		got, err := k.GetAsgardVaults(gctx)
		c.Assert(err, IsNil)
		c.Assert(got, DeepEquals, want)
		ok, err := k.HasValidVaultPools(gctx)
		c.Assert(err, IsNil)
		c.Assert(ok, Equals, true)
		gas = append(gas, gctx.GasMeter().GasConsumed())
	}
	c.Check(gas[1], Equals, gas[0])
}

func (s *DecodeMemoSuite) TestVaultMutationIsolation(c *C) {
	_, k := setupKeeperForTest(c)
	bz := vaultTestBlobs(c, k)[0]
	var want Vault
	c.Assert(k.cdc.Unmarshal(bz, &want), IsNil)
	key := []byte("memo-test/vault/mutation")

	var first Vault
	c.Assert(k.memoDecodeVault(key, bz, &first), Equals, true)
	for pass := 0; pass < 2; pass++ {
		var got Vault
		c.Assert(k.memoDecodeVault(key, bz, &got), Equals, true)
		c.Assert(got, DeepEquals, want)
		for i := range got.Coins {
			got.Coins[i].Amount.BigIntMut().SetInt64(5)
			got.Coins[i].Asset = common.DOGEAsset
			got.Coins[i].Decimals = 3
		}
		got.Coins = append(got.Coins, common.NewCoin(common.LTCAsset, cosmos.NewUint(1)))
		got.Deficit[0].Amount.BigIntMut().SetInt64(5)
		got.Deficit[0].Asset = common.DOGEAsset
		got.Membership[0] = "changed"
		got.Membership = append(got.Membership, "appended")
		got.Chains[0] = "changed"
		got.PendingTxBlockHeights[0] = -1
		got.Routers[0].Router = "changed"
		got.Frozen[0] = "changed"
		got.Status = RetiringVault
	}
	first.Coins[0].Amount.BigIntMut().SetInt64(1)
	first.Membership[1] = "changed"

	var got Vault
	c.Assert(k.memoDecodeVault(key, bz, &got), Equals, true)
	c.Assert(got, DeepEquals, want)
}

func (s *DecodeMemoSuite) TestVaultChangedBytesRedecode(c *C) {
	ctx, k := setupKeeperForTest(c)
	blobs := vaultTestBlobs(c, k)
	key := []byte("memo-test/vault/changed")
	for i, bz := range [][]byte{blobs[0], blobs[1], blobs[1], blobs[0], blobs[1]} {
		var want, got Vault
		c.Assert(k.cdc.Unmarshal(bz, &want), IsNil)
		c.Assert(k.memoDecodeVault(key, bz, &got), Equals, true)
		c.Assert(got, DeepEquals, want, Commentf("step %d", i))
	}

	// a write through the keeper is seen by the next read
	v := testVault(1)
	c.Assert(k.SetVault(ctx, v), IsNil)
	got, err := k.GetVault(ctx, v.PubKey)
	c.Assert(err, IsNil)
	c.Assert(got.Coins.EqualsEx(v.Coins), Equals, true)
	v.Coins[0].Amount = cosmos.NewUint(42)
	v.Membership = v.Membership[:1]
	c.Assert(k.SetVault(ctx, v), IsNil)
	got, err = k.GetVault(ctx, v.PubKey)
	c.Assert(err, IsNil)
	c.Assert(got.Coins[0].Amount.Uint64(), Equals, uint64(42))
	c.Assert(got.Membership, DeepEquals, v.Membership)

	// another store holding other bytes under the same key
	ctx2, k2 := setupKeeperForTest(c)
	v2 := v
	v2.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(7)))
	c.Assert(k2.SetVault(ctx2, v2), IsNil)
	for i := 0; i < 3; i++ {
		got, err = k.GetVault(ctx, v.PubKey)
		c.Assert(err, IsNil)
		c.Assert(got.Coins[0].Amount.Uint64(), Equals, uint64(42))
		got, err = k2.GetVault(ctx2, v.PubKey)
		c.Assert(err, IsNil)
		c.Assert(got.Coins, HasLen, 1)
		c.Assert(got.Coins[0].Amount.Uint64(), Equals, uint64(7))
	}
}

func (s *DecodeMemoSuite) TestVaultBounded(c *C) {
	_, k := setupKeeperForTest(c)
	bz := vaultTestBlobs(c, k)[0]
	for i := 0; i < vaultMemoMaxEntries+10; i++ {
		var got Vault
		c.Assert(k.memoDecodeVault([]byte(fmt.Sprintf("memo-test/vault/bound/%d", i)), bz, &got), Equals, true)
		c.Assert(vaultDecodeMemo.len() <= vaultMemoMaxEntries, Equals, true)
	}
}

// TestVaultMemoConcurrent is meant for -race: readers decode alternating
// bytes under shared keys and mutate what they get back.
func TestVaultMemoConcurrent(t *testing.T) {
	k := KVStore{cdc: codec.NewProtoCodec(codectypes.NewInterfaceRegistry())}
	var blobs [][]byte
	var wants []Vault
	for i := 0; i < 4; i++ {
		v := testVault(i)
		bz, err := k.cdc.Marshal(&v)
		if err != nil {
			t.Fatal(err)
		}
		var want Vault
		if err := k.cdc.Unmarshal(bz, &want); err != nil {
			t.Fatal(err)
		}
		blobs = append(blobs, bz)
		wants = append(wants, want)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				j := (g + i) % len(blobs)
				key := []byte(fmt.Sprintf("memo-test/vault/concurrent/%d", i%3))
				var got Vault
				if !k.memoDecodeVault(key, bytes.Clone(blobs[j]), &got) {
					t.Error("decode failed")
					return
				}
				if !reflect.DeepEqual(got, wants[j]) {
					t.Errorf("goroutine %d step %d: mismatch", g, i)
					return
				}
				got.Coins[0].Amount.BigIntMut().SetInt64(int64(i))
				got.Membership[0] = "x"
				got.PendingTxBlockHeights[0] = int64(i)
			}
		}(g)
	}
	wg.Wait()
}
