//go:build stagenet
// +build stagenet

package thorchain

import (
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcyclaimlist"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// defaultTCYClaimers loads the embedded tcy_claimers_stagenet.json and
// returns it as TCYClaimer records ready to be stored by InitGenesis.
// Previously this list was applied during Migrate4to5; seeding it at
// genesis removes the dependency on a post-boot version upgrade for
// fresh stagenet launches.
func defaultTCYClaimers() []TCYClaimer {
	out := make([]TCYClaimer, 0)
	for _, c := range tcyclaimlist.GetTCYClaimsList() {
		asset, err := common.NewAsset(c.Asset)
		if err != nil {
			log.Warn().Err(err).Str("asset", c.Asset).Msg("skip malformed TCY claimer asset")
			continue
		}
		out = append(out, types.TCYClaimer{
			Asset:     asset,
			L1Address: common.Address(c.Address),
			Amount:    cosmos.NewUint(c.TCYClaim),
		})
	}
	return out
}
