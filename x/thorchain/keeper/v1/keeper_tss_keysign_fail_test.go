package keeperv1

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperTssKeysignFailureSuite struct{}

var _ = Suite(&KeeperTssKeysignFailureSuite{})

func setRawTssKeysignFailVoter(ctx cosmos.Context, k KVStore, voter TssKeysignFailVoter) {
	k.setTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, voter.ID), voter)
}

func getRawTssKeysignFailVoter(c *C, ctx cosmos.Context, k KVStore, id string) TssKeysignFailVoter {
	var stored TssKeysignFailVoter
	found, err := k.getTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, id), &stored)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	return stored
}

func (KeeperTssKeysignFailureSuite) TestTssKeysignFailVoter(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()

	missing, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, IsNil)
	c.Check(missing.Empty(), Equals, true)
	c.Check(missing.Completed, Equals, false)
	c.Check(missing.SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)

	live := NewTssKeysignFailVoter(id, 0)
	live.Signers = []string{GetRandomBech32Addr().String()}
	c.Assert(k.SetTssKeysignFailVoter(ctx, live), IsNil)

	stored := getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(stored.SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)
	voter, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, IsNil)
	c.Check(voter.Completed, Equals, false)
	c.Check(voter.Signers, DeepEquals, live.Signers)
}

func (KeeperTssKeysignFailureSuite) TestAllLegacyVoterShapesAreSealed(c *C) {
	ctx, k := setupKeeperForTest(c)
	testCases := []struct {
		name  string
		voter TssKeysignFailVoter
	}{
		{name: "empty terminal", voter: TssKeysignFailVoter{}},
		{name: "positive height", voter: TssKeysignFailVoter{Height: 1024}},
		{name: "in progress", voter: TssKeysignFailVoter{Signers: []string{GetRandomBech32Addr().String()}}},
		{name: "terminal round in progress", voter: TssKeysignFailVoter{
			Signers:        []string{GetRandomBech32Addr().String(), GetRandomBech32Addr().String()},
			LastRoundCount: 2,
		}},
	}

	for i := range testCases {
		tc := testCases[i]
		c.Logf("case: %s", tc.name)
		id := fmt.Sprintf("%064X", i+1)
		tc.voter.ID = id
		setRawTssKeysignFailVoter(ctx, k, tc.voter)

		raw := getRawTssKeysignFailVoter(c, ctx, k, id)
		c.Assert(raw.SchemaVersion, Equals, uint32(0))

		normalized, err := k.GetTssKeysignFailVoter(ctx, id)
		c.Assert(err, IsNil)
		c.Check(normalized.SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)
		c.Check(normalized.Completed, Equals, true)
		c.Check(normalized.Height, Equals, tc.voter.Height)
		c.Check(normalized.Signers, DeepEquals, tc.voter.Signers)
		c.Check(normalized.LastRoundCount, Equals, tc.voter.LastRoundCount)

		// Reads are pure. The handler persists the normalized representation
		// when a late message reaches its completed-voter early return.
		raw = getRawTssKeysignFailVoter(c, ctx, k, id)
		c.Check(raw.SchemaVersion, Equals, uint32(0))
		c.Assert(k.SetTssKeysignFailVoter(ctx, normalized), IsNil)
		raw = getRawTssKeysignFailVoter(c, ctx, k, id)
		c.Check(raw.SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)
		c.Check(raw.Completed, Equals, true)
	}
}

func (KeeperTssKeysignFailureSuite) TestVersionOneInProgressVoterSurvivesReads(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()
	live := TssKeysignFailVoter{
		ID:            id,
		Signers:       []string{GetRandomBech32Addr().String()},
		SchemaVersion: types.CurrentTssKeysignFailVoterSchemaVersion,
	}
	setRawTssKeysignFailVoter(ctx, k, live)

	restarted := NewKVStore(k.cdc, k.storeService, k.coinKeeper, k.accountKeeper, k.upgradeKeeper, k.version)
	for _, reader := range []KVStore{k, restarted} {
		got, err := reader.GetTssKeysignFailVoter(ctx, id)
		c.Assert(err, IsNil)
		c.Check(got.SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)
		c.Check(got.Completed, Equals, false)
		c.Check(got.Signers, DeepEquals, live.Signers)
	}
}

func (KeeperTssKeysignFailureSuite) TestSetterAlwaysWritesCurrentSchema(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()
	c.Assert(k.SetTssKeysignFailVoter(ctx, TssKeysignFailVoter{ID: id}), IsNil)
	stored := getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(stored.SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)

	unsupported := types.CurrentTssKeysignFailVoterSchemaVersion + 1
	err := k.SetTssKeysignFailVoter(ctx, TssKeysignFailVoter{ID: id, SchemaVersion: unsupported})
	c.Assert(err, ErrorMatches, fmt.Sprintf("refusing to persist unsupported TSS keysign fail voter schema version: %d", unsupported))
	stored = getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(stored.SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)
}

func (KeeperTssKeysignFailureSuite) TestUnsupportedFutureSchemaFailsWithoutRewrite(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()
	unsupported := types.CurrentTssKeysignFailVoterSchemaVersion + 1
	setRawTssKeysignFailVoter(ctx, k, TssKeysignFailVoter{
		ID:            id,
		Completed:     true,
		SchemaVersion: unsupported,
	})

	_, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, ErrorMatches, fmt.Sprintf("unsupported TSS keysign fail voter schema version: %d", unsupported))
	stored := getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(stored.SchemaVersion, Equals, unsupported)
	c.Check(stored.Completed, Equals, true)
}

func (KeeperTssKeysignFailureSuite) TestTypedWalkNormalizesAndCanStop(c *C) {
	ctx, k := setupKeeperForTest(c)
	legacyID := fmt.Sprintf("%064X", 1)
	currentID := fmt.Sprintf("%064X", 2)
	setRawTssKeysignFailVoter(ctx, k, TssKeysignFailVoter{ID: legacyID, LastRoundCount: 1})
	setRawTssKeysignFailVoter(ctx, k, TssKeysignFailVoter{
		ID:            currentID,
		SchemaVersion: types.CurrentTssKeysignFailVoterSchemaVersion,
	})

	visited := make([]TssKeysignFailVoter, 0, 2)
	err := k.WalkTssKeysignFailVoters(ctx, func(voter TssKeysignFailVoter) bool {
		visited = append(visited, voter)
		return false
	})
	c.Assert(err, IsNil)
	c.Assert(visited, HasLen, 2)
	c.Check(visited[0].ID, Equals, legacyID)
	c.Check(visited[0].Completed, Equals, true)
	c.Check(visited[0].SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)
	c.Check(visited[1].ID, Equals, currentID)
	c.Check(visited[1].Completed, Equals, false)
	c.Check(visited[1].SchemaVersion, Equals, types.CurrentTssKeysignFailVoterSchemaVersion)

	count := 0
	err = k.WalkTssKeysignFailVoters(ctx, func(TssKeysignFailVoter) bool {
		count++
		return true
	})
	c.Assert(err, IsNil)
	c.Check(count, Equals, 1)
}

func (KeeperTssKeysignFailureSuite) TestTypedWalkRejectsFutureSchema(c *C) {
	ctx, k := setupKeeperForTest(c)
	unsupported := types.CurrentTssKeysignFailVoterSchemaVersion + 1
	setRawTssKeysignFailVoter(ctx, k, TssKeysignFailVoter{
		ID:            GetRandomTxHash().String(),
		SchemaVersion: unsupported,
	})

	visited := 0
	err := k.WalkTssKeysignFailVoters(ctx, func(TssKeysignFailVoter) bool {
		visited++
		return false
	})
	c.Assert(err, ErrorMatches, fmt.Sprintf("unsupported TSS keysign fail voter schema version: %d", unsupported))
	c.Check(visited, Equals, 0)
}
