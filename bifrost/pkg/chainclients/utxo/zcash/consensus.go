package zcash

import (
	"fmt"
	"strconv"
)

// ConsensusState contains the daemon's native upgrade schedule.
type ConsensusState struct {
	Blocks   int64 `json:"blocks"`
	Upgrades map[string]struct {
		ActivationHeight int64 `json:"activationheight"`
	} `json:"upgrades"`
	Consensus struct {
		ChainTip  string `json:"chaintip"`
		NextBlock string `json:"nextblock"`
	} `json:"consensus"`
}

// PaymentState fixes the branch and expiry from an agreed height, never a moving local tip.
func (s ConsensusState) PaymentState(height, window uint32) (uint32, uint32, error) {
	// The resulting expiry must stay below ZIP 203's 500,000,000 boundary.
	if window == 0 || uint64(height)+uint64(window) >= 5e8 {
		return 0, 0, fmt.Errorf("invalid Zcash payment height or expiry window")
	}
	var branch uint32
	activation := int64(-1)
	expiry := height + window
	nextHeight := int64(height) + 1
	// Find the last activation first. Regtest can activate several upgrades at once.
	for _, upgrade := range s.Upgrades {
		upgradeHeight := upgrade.ActivationHeight
		if upgradeHeight >= 0 && upgradeHeight <= nextHeight && upgradeHeight > activation {
			activation = upgradeHeight
		}
		if upgradeHeight > nextHeight && upgradeHeight <= int64(expiry) {
			expiry = uint32(upgradeHeight) - 1
		}
	}
	candidates := make(map[uint32]bool)
	for text, upgrade := range s.Upgrades {
		if activation < 0 || upgrade.ActivationHeight != activation {
			continue
		}
		id, err := strconv.ParseUint(text, 16, 32)
		if err != nil || len(text) != 8 || id == 0 {
			return 0, 0, fmt.Errorf("invalid Zcash upgrade branch %q", text)
		}
		branch = uint32(id)
		candidates[branch] = true
	}
	if len(candidates) > 1 {
		// Regtest can activate several upgrades together. Use the daemon's branch
		// only within the activation selected by the agreed height.
		branch = 0
		for _, text := range []string{s.Consensus.ChainTip, s.Consensus.NextBlock} {
			id, err := strconv.ParseUint(text, 16, 32)
			if err != nil || len(text) != 8 || !candidates[uint32(id)] {
				continue
			}
			if branch != 0 && branch != uint32(id) {
				return 0, 0, fmt.Errorf("ambiguous Zcash upgrade schedule")
			}
			branch = uint32(id)
		}
		if branch == 0 {
			return 0, 0, fmt.Errorf("ambiguous Zcash upgrade schedule")
		}
	}
	if branch == 0 {
		return 0, 0, fmt.Errorf("missing Zcash upgrade branch")
	}
	return branch, expiry, nil
}
