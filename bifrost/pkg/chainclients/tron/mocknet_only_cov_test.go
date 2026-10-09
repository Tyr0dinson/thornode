package tron

import "gitlab.com/thorchain/thornode/v3/common"

// notMocknetCov reports whether this test binary was built for a network other
// than mocknet. Tests whose fixtures or expectations are mocknet constants skip
// themselves there, since CI only runs the unit tests under -tags=mocknet.
func notMocknetCov() bool {
	return common.CurrentChainNetwork != common.MockNet
}
