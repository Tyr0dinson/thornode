package divergence

import (
	"strings"
	"unicode/utf8"
)

// thorchainPrefixes maps the thorchain store key prefixes to their constant
// names in x/thorchain/keeper/v1/keeper.go (unexported there, so copied here;
// TestThorchainPrefixesMatchKeeper keeps the copy in sync).
var thorchainPrefixes = map[string]string{
	"observed_tx_in/":                "prefixObservedTxIn",
	"observed_tx_out/":               "prefixObservedTxOut",
	"ob_link/":                       "prefixObservedLink",
	"refund_cleanup/":                "prefixRefundCleanup",
	"refund_cleanup_expiry/":         "prefixRefundCleanupExpiry",
	"pool/":                          "prefixPool",
	"luvi/":                          "prefixPoolLUVI",
	"pool_luvi/":                     "prefixPoolLuviAccumulator",
	"txout/":                         "prefixTxOut",
	"total_liquidity_fee/":           "prefixTotalLiquidityFee",
	"pool_liquidity_fee/":            "prefixPoolLiquidityFee",
	"pool_swap_slip/":                "prefixPoolSwapSlip",
	"pool_swap_slip_long/":           "prefixPoolSwapSlipLong",
	"pool_swap_slip_ss/":             "prefixPoolSwapSnapShot",
	"lp/":                            "prefixLiquidityProvider",
	"last_chain_height/":             "prefixLastChainHeight",
	"last_signed_height/":            "prefixLastSignedHeight",
	"last_observe_height/":           "prefixLastObserveHeight",
	"last_keygen_chain/":             "prefixLastKeygenChain",
	"last_keygen_report/":            "prefixLastKeygenReport",
	"chain_set_stall/":               "prefixChainSetStall",
	"observation_tally_due/":         "prefixObservationTallyDue",
	"observation_tally_total/":       "prefixObservationTotal",
	"observation_tally_missed/":      "prefixObservationMissed",
	"observation_tally_base/":        "prefixObservationBase",
	"observation_tally_guard/":       "prefixObservationGuard",
	"observation_tally_guard_full/":  "prefixObservationGuardFull",
	"node_account/":                  "prefixNodeAccount",
	"bond_providers/":                "prefixBondProviders",
	"vault/":                         "prefixVault",
	"vault_asgard_index/":            "prefixVaultAsgardIndex",
	"vault_asgard_eddsa_index/":      "prefixVaultAsgardEDDSAIndex",
	"vault_asgard_frost_index/":      "prefixVaultAsgardFrostIndex",
	"xmr_spent_refs/":                "prefixXMRSpentOutputRef",
	"sol_vault_nonce/":               "prefixSolVaultNonce",
	"sol_nonce_attest/":              "prefixSolNonceAttestVoter",
	"network/":                       "prefixNetwork",
	"sclout/":                        "prefixSwapperClout",
	"pol/":                           "prefixPOL",
	"pol_reserve/":                   "prefixPOLReserve",
	"stable_reserve_credit/":         "prefixStableReserveCredit",
	"tr_acct/":                       "prefixTradeAccount",
	"sa/":                            "prefixSecuredAsset",
	"rune_provider/":                 "prefixRUNEProvider",
	"rune_pool/":                     "prefixRUNEPool",
	"tr_unit/":                       "prefixTradeUnit",
	"stream/":                        "prefixStreamingSwap",
	"observing_addresses/":           "prefixObservingAddresses",
	"tss/":                           "prefixTss",
	"tssKeysignFailure/":             "prefixTssKeysignFailure",
	"keygen/":                        "prefixKeygen",
	"ragnarokHeight/":                "prefixRagnarokHeight",
	"ragnarokNth/":                   "prefixRagnarokNth",
	"ragnarokPending/":               "prefixRagnarokPending",
	"ragnarokPosition/":              "prefixRagnarokPosition",
	"ragnarokPool/":                  "prefixRagnarokPoolHeight",
	"errata/":                        "prefixErrataTx",
	"ban/":                           "prefixBanVoter",
	"slash/":                         "prefixNodeSlashPoints",
	"jail/":                          "prefixNodeJail",
	"node_blame/":                    "prefixNodeBlameRecord",
	"swapitem/":                      "prefixSwapQueueItem",
	"aq/":                            "prefixAdvSwapQueueItem",
	"aqlim/":                         "prefixAdvSwapQueueLimitIndex",
	"aqmark/":                        "prefixAdvSwapQueueMarketIndex",
	"aqttl/":                         "prefixAdvSwapQueueTTL",
	"outbound_fee_withheld_rune/":    "prefixOutboundFeeWithheldRune",
	"outbound_fee_spent_rune/":       "prefixOutboundFeeSpentRune",
	"mimir/":                         "prefixMimir",
	"minjoinlast/":                   "prefixMinJoinLast",
	"nodemimir/":                     "prefixNodeMimir",
	"node_pause_chain/":              "prefixNodePauseChain",
	"network_fee/":                   "prefixNetworkFee",
	"network_fee_voter/":             "prefixNetworkFeeVoter",
	"tss_keygen_metric/":             "prefixTssKeygenMetric",
	"tss_keysign_metric/":            "prefixTssKeysignMetric",
	"latest_tss_keysign_metric/":     "prefixTssKeysignMetricLatest",
	"chain_contract/":                "prefixChainContract",
	"solvency_voter/":                "prefixSolvencyVoter",
	"solvency_voter_quota/":          "prefixSolvencyVoterQuota",
	"thorname/":                      "prefixTHORName",
	"affcol_name/":                   "prefixAffiliateCollector",
	"affiliate_liquidity_fee/":       "prefixAffiliateLiquidityFee",
	"refm/":                          "prefixReferenceMemo",
	"refmi/":                         "prefixReferenceMemoIndex",
	"refmh/":                         "prefixReferenceMemoHash",
	"refmw/":                         "prefixReferenceMemoWindow",
	"rolling_pool_liquidity_fee/":    "prefixRollingPoolLiquidityFee",
	"prev_cycle_pool_liquidity_fee/": "prefixPrevCyclePoolLiquidityFee",
	"version/":                       "prefixVersion",
	"upgr_props/":                    "prefixUpgradeProposals",
	"upgr_votes/":                    "prefixUpgradeVotes",
	"tcy_claimer/":                   "prefixTCYClaimer",
	"tcy_staker/":                    "prefixTCYStaker",
	"addr_pk/":                       "prefixAddrPubKey",
	"oracle_price/":                  "prefixOraclePrice",
	"volume_bucket/":                 "prefixVolumeBucket",
	"volume/":                        "prefixVolume",
	"dynfee/":                        "prefixDynamicFee",
	"dynfeeacc/":                     "prefixDynamicFeeAcc",
	"dynfeeebo/":                     "prefixDynamicFeeEpochBlocks",
}

// KeyInfo is a best-effort decoding of a store key.
type KeyInfo struct {
	// Prefix is the key's leading "name/" segment when it is printable.
	Prefix string `json:"prefix,omitempty"`
	// PrefixName is the keeper constant for Prefix in the thorchain store.
	PrefixName string `json:"prefix_name,omitempty"`
	// Text is the key as text when it is printable UTF-8.
	Text string `json:"text,omitempty"`
}

// DecodeKey decodes a thorchain store key ("pool//BTC.BTC" -> prefix "pool/",
// prefixPool). Keys of other stores only get Text when they are printable.
func DecodeKey(store string, key []byte) KeyInfo {
	var ki KeyInfo
	if printable(key) {
		ki.Text = string(key)
		if i := strings.IndexByte(ki.Text, '/'); i > 0 {
			ki.Prefix = ki.Text[:i+1]
		}
	}
	if store == "thorchain" && ki.Prefix != "" {
		ki.PrefixName = thorchainPrefixes[ki.Prefix]
	}
	return ki
}

func printable(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
