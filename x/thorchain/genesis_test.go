package thorchain

import (
	"os"
	"regexp"
	"strings"

	"github.com/btcsuite/btcutil/bech32"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/cmd"
)

type GenesisTestSuite struct{}

var _ = Suite(&GenesisTestSuite{})

var genesisFixtureBech32 = regexp.MustCompile(`thor[a-z]*1[0-9a-z]+`)

func normalizeGenesisFixtureBech32(input string) string {
	return genesisFixtureBech32.ReplaceAllStringFunc(input, func(raw string) string {
		_, data, err := bech32.DecodeNoLimit(raw)
		if err != nil {
			return raw
		}

		targetPrefix := cmd.Bech32PrefixAccAddr
		switch {
		case strings.HasPrefix(raw, "thorcpub1"):
			targetPrefix = cmd.Bech32PrefixConsPub
		case strings.HasPrefix(raw, "thorpub1"):
			targetPrefix = cmd.Bech32PrefixAccPub
		case strings.HasPrefix(raw, "thorc1"):
			targetPrefix = cmd.Bech32PrefixConsAddr
		}

		normalized, err := bech32.Encode(targetPrefix, data)
		if err != nil {
			return raw
		}
		return normalized
	})
}

func (GenesisTestSuite) TestGenesis(c *C) {
	SetupConfigForTest()
	genesisState := DefaultGenesisState()
	c.Assert(ValidateGenesis(genesisState), IsNil)
	ctx, mgr := setupManagerForTest(c)
	gs := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(ValidateGenesis(gs), IsNil)
	content, err := os.ReadFile("../../test/fixtures/genesis/genesis.json")
	c.Assert(err, IsNil)
	c.Assert(content, NotNil)
	normalizedContent := normalizeGenesisFixtureBech32(string(content))
	ctx, mgr = setupManagerForTest(c)
	var state GenesisState
	c.Assert(ModuleCdc.UnmarshalJSON([]byte(normalizedContent), &state), IsNil)
	result := InitGenesis(ctx, mgr.Keeper(), state)
	c.Assert(result, NotNil)
	gs1 := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(len(gs1.Pools) > 0, Equals, true)
}
