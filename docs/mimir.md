# Constants and Mimir

## Overview

The network launched with a set number of constants. Constants can be overridden via Mimir and nodes have the ability to [vote on](https://docs.thorchain.org/thornodes/overview#node-voting) and change Mimir values. \
See [Halt Management](./concepts/network-halts.md) for halt and pause specific settings.

Mimir setting can be created and changed without a corresponding Constant.

Mimirs have a maximum length of 128 bytes (see MaxMimirLength in THORNode code).

### Values

- Constant Values: [https://gateway.liquify.com/chain/thorchain_api/thorchain/constants](https://gateway.liquify.com/chain/thorchain_api/thorchain/constants)
- Mimir Values: [https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir](https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir)

### Key

- No Symbol - Constant only, no Mimir override.
- Star (\*) indicates a Mimir override of a Constant.
- Hash (#) indicates Mimir with no Constant.

### Block Time

Block counts in constants and mimirs are stored in reference blocks of the network's default block time (6s on mainnet, 1s on mocknet) and converted when read, so they keep their wall-clock meaning when `BlockTimeMs` sets a different target. They are converted to the effective block time, which can differ from the `BlockTimeMs` target:

- The effective block time is latched at the start of each block, so a vote in block H takes effect from H+1.
- It is rate limited. A step back toward the default applies at once, without going past it. Any other step waits until `ChurnInterval` of wall-clock time has passed since the previous change, and goes at most half or double the current block time. On mainnet, a `BlockTimeMs` of `2000` first gives `3000`, and `2000` one `ChurnInterval` later.

Each constant has a unit (`constants/units.go`):

- Duration (e.g. `ChurnInterval`, `JailTimeKeysign`, `BlocksPerYear`): multiplied by default / effective block time, rounded up. `0` and `-1` are unchanged.
- Per-block rate (e.g. `TxOutDelayRate`, `TNSFeePerBlock`, `POLMaxPoolMovement`): divided by the same ratio, rounded half up.
- Count, height and wall-clock values are never converted.

Stored mimir values are not rewritten, except `NodePauseChainGlobal`: like the other deadlines the protocol stores as a future height (jail releases, the outbound queue, THORName expiries), it is moved at a change so it keeps the time it had left. Unsetting `BlockTimeMs` returns every scaled constant and mimir to its stored value. Governance heights (`Halt*`, upgrades, `ScheduledMigration`) are absolute and are not moved; re-plan them after a change. `/thorchain/constants` shows constants at the current block time; `/thorchain/mimir` shows the stored values. Block counts in memos and quote parameters are THORChain blocks at the current block time and are not converted.

## Outbound Transactions

- `OutboundTransactionFee`: Amount of rune to withhold on all outbound transactions (1e8 notation)
- `RescheduleCoalesceBlocks`\*: The number of blocks to coalesce rescheduled outbounds
- `MaxOutboundAttempts`: The maximum retries to reschedule a transaction
- `MaxRetiredVaultRecoveryAttempts`\*: Maximum reschedule attempts for an outbound held by a retired vault before it is dropped
- `MaxGas-<Chain>`#: Maximum gas for outbound transactions on an EVM chain, overriding the built-in per-chain cap

### Scheduled Outbound

- `MaxTxOutOffset`: Maximum number of blocks a scheduled outbound transaction can be delayed
- `MinTxOutVolumeThreshold`: Quantity of outbound value (in 1e8 rune) in a block before it's considered "full" and additional value is pushed into the next block
- `TxOutDelayMax`: Maximum number of blocks a scheduled transaction can be delayed
- `TxOutDelayRate`\*: Rate of which scheduled transactions are delayed

## Swapping

- `Halt<chain>Trading`#: Pause trading on a specific chain
- `MaxSwapsPerBlock`: Artificial limit on the number of swaps that a single block with process
- `MinSwapsPerBlock`: Process all swaps if the queue is equal to or smaller than this number
- `EnableDerivedAssets`: Enable/disable derived asset swapping (excludes lending)
- `DerivedDepthBasisPts`\*: Basis points to increase/decrease derived pool depth (10,000 = 1x)
- `DerivedMinDepth`\*: Minimum derived pool depth, in basis points
- `MaxAnchorSlip`\*: Anchor pool slip (basis points of RUNE depth) at which a derived pool is paused
- `MaxAnchorBlocks`\*: Maximum number of blocks of anchor pool swap slip to accumulate
- `DynamicMaxAnchorSlipBlocks`\*: Number of blocks sampled when calculating the dynamic max anchor slip
- `DynamicMaxAnchorTarget`\*: Target depth of derived pools, in basis points, for the dynamic max anchor slip (0 = disabled)
- `DynamicMaxAnchorCalcInterval`\*: Number of blocks between dynamic max anchor slip recalculations
- `MintSynths`#: Block height after which swaps that mint synths are disabled
- `BurnSynths`#: Block height after which swaps from synths are disabled
- `ManualSwapsToSynthDisabled`#: Disable swaps to synths
- `MaxSynthPerPoolDepth`\*: Maximum synth supply relative to pool depth, in basis points
- `VirtualMultSynthsBasisPoints`\*: Pool depth multiplier for synth swaps, in basis points
- `VirtualMultSynths`: Pool depth multiplier for synth swaps (not used, replaced by `VirtualMultSynthsBasisPoints`)
- `SynthYieldBasisPoints`\*: Share of the pool yield paid to synth holders when synth per pool is 0%, in basis points
- `MaxSynthsForSaversYield`\*: Synth per pool depth, in basis points, at which synth yield reaches 0%
- `SynthYieldCycle`: Number of blocks between rewards paid to yield bearing synths (not used)
- `SwapOutDexAggregationDisabled`#: Disable swap out through DEX aggregators
- `StreamingSwapMinBPFee`\*: Minimum swap fee (in basis points) for a streaming swap trade (not used)
- `StreamingSwapMaxLength`: Maximum number of blocks a streaming swap can trade for
- `StreamingSwapMaxLengthNative`\*: Maximum number of blocks native streaming swaps can trade over
- `StreamingSwapPause`\*: Pause legacy streaming swaps
- `TradeAccountsEnabled`: Enable/disable trade account
- `TradeAccountsDepositEnabled`\*: Enable/disable trade account deposits (also requires `TradeAccountsEnabled`)
- `HaltTradeDeposit-<Chain>`#: Disables deposit of trade assets on the specified chain
- `HaltTradeWithdraw-<Chain>`#: Disables withdrawal of trade assets on the specified chain
- `CloutReset`: The number of blocks before clout spent gets reset
- `CloutLimit`\*: Max clout allowed to spend
- `MultipleAffiliatesMaxCount`: Maximum number of nested affiliates
- `EnableAdvSwapQueue`: Enable advanced swap queue (0 = disabled, 1 = enabled, 2 = market swaps only, limit swaps skipped)
- `AdvSwapQueueRapidSwapMax`: Maximum number of rapid swap iterations per block
- `StreamingLimitSwapMaxAge`: Maximum number of blocks a limit swap can exist before expiring
- `ModifyLimitSwapMaxIterations`: Maximum iterations when searching for a user's swap to modify or cancel
- `EnableOrderBooks`: Enable order books instead of the swap queue (not used)

### [Swap Fee Minimums](./concepts/fees.md#minimum-swap-fee)

- `L1SlipMinBps`: Minimum L1 asset swap fee in basis points
- `TradeAccountsSlipMinBps`: Minimum trade asset swap fee in basis points
- `SecuredAssetSlipMinBps`: Minimum secured asset swap fee in basis points
- `SynthSlipMinBps`: Minimum synth asset swap fee in basis points
- `DerivedSlipMinBps`: Minimum derived asset swap fee in basis points
- `StableSlipMinBps`: Minimum swap fee in basis points for stable-to-stable swaps
- `WasmArbSlipMinBps`: Minimum swap fee in basis points for swaps from the WASM arbitrage contract
- `SlipMinBpsMax`: Upper limit accepted for any of the minimum swap fee mimirs
- `SwapSlipBasisPointsMin-<ref>`#: Per-reference minimum swap fee (not used)
- `L1DynamicFeeEnabled`: Enable/disable the per-thorname [dynamic L1 minimum fee](./concepts/fees.md#dynamic-l1-minimum-fee)
- `L1DynamicFeeEpochBlocks`: Number of blocks per dynamic fee epoch
- `L1DynamicFeeFloorBPS`: Lower bound of the dynamic minimum, and the value a new record starts at
- `L1DynamicFeeCeilingBPS`: Upper bound of the dynamic minimum
- `L1DynamicFeeStepBPS`: Basis points the dynamic minimum moves per epoch
- `L1DynamicFeeDeadbandBPS`: Percentage change in fees (in basis points) below which the floor is left unchanged
- `L1DynamicFeeWindowEpochs`: Epochs averaged either side of the last change when measuring the fee response

## Memoless Transactions

- `MemolessTxnTTL`\*: Number of blocks before a registered reference memo expires (default: 3600 blocks ≈ 6 hours). **CAUTION:** Expiry is evaluated against the current value, so _raising_ it brings already-expired references back to life. The occupancy estimate behind `MemolessTxnCostSlopeBPS` cannot see those, and so undercharges until every pre-change reference has expired — at most one (new) `MemolessTxnTTL` after the change. Lowering it is safe; the estimate errs high instead.
- `MemolessTxnRefCount`\*: Maximum number of reference IDs available per chain (default: 99,999). Reference numbers range from 1 to this value. **CAUTION:** Changing this value alters the zero-padding length used to normalize reference keys in the KV store. All existing (non-expired) references will become inaccessible under the new normalization. Only change this value after all current references have expired (see `MemolessTxnTTL`), or coordinate a store migration.
- `MemolessTxnCost`\*: Optional RUNE cost for registering reference memos (default: 0, in 1e8 notation). Only charged on successful registration.
- `MemolessTxnMaxUse`\*: Maximum number of times a reference can be used before being invalidated (default: 1). Set to 0 for unlimited usage.
- `MemolessTxnCostSlopeBPS`\*: Steepness of the memoless registration cost curve as an asset's reference slots fill up (default: 0). The charged cost is `MemolessTxnCost * (1 + slope * used / free)` where `slope = MemolessTxnCostSlopeBPS / 10,000`, `used` is an estimate of the asset's occupied reference slots and `free` is the remainder of `MemolessTxnRefCount` (floored at 1). `used` is deliberately conservative: it is counted over two `MemolessTxnTTL`-wide buckets, so it never undercounts, but it keeps counting a registration for up to one extra `MemolessTxnTTL` after that registration has actually expired — the cost can therefore stay elevated for that long after a burst drains. (The `MemolessTxnTTL` caveat above is the one case where it undercounts instead.) `0` disables the curve and charges a flat `MemolessTxnCost`. `10,000` gives `cost = MemolessTxnCost / (1 - occupancy)`: 2x at half full, 10x at 90% full. Higher values make the cost climb sooner, lower values keep it cheap until the ring is nearly exhausted.
- `HaltMemoless`#: Emergency halt for all memoless transaction functionality (registration and usage)
- `EnableMemolessOutbound`#: Enable memoless outbound transactions

## TCY Management

- `MinRuneForTCYStakeDistribution`: Minimum RUNE required in the TCY fund to be eligible for distribution (default: 2,100 RUNE (210000000000 in 1e8 notation))
- `MinTCYForTCYStakeDistribution`: Minimum TCY a staker must have staked to be included in a RUNE distribution (default: 0.001 TCY (100000 in 1e8 notation))
- `TCYStakeSystemIncomeBps`: Percentage (in basis points) of system income allocated to the TCY fund (default: 1000 bps = 10%)
- `TCYClaimingSwapHalt`: Enables/disables RUNE-to-TCY swaps in the claiming module (default: 1, halted)
- `TCYStakeDistributionHalt`: Enables/disables distribution of RUNE revenue to TCY stakers (default: 1, halted)
- `TCYStakingHalt`: Enables/disables staking of TCY tokens (default: 1, halted)
- `TCYUnstakingHalt`: Enables/disables unstaking of TCY tokens (default: 1, halted)
- `TCYClaimingHalt`: Enables/disables claiming of TCY tokens for THORFi deposits (default: 1, halted)
- `HaltTCYTrading`#: Halts TCY trading

## Secured Asset Management

- `HaltSecuredGlobal`#: Halts operations for all secured assets across both the Base and App Layers
- `HaltSecuredDeposit-<Chain>`#: Disables deposit of secured assets on the specified chain
- `HaltSecuredWithdraw-<Chain>`#: Disables withdrawal of secured asset on the specified chain

## Stable Reserve

- `StableReserveEnabled`: Enable/disable Stable Reserve. Defaults to `0`.
- `StableReserveDepegToleranceBps`: Maximum stable oracle deviation from $1 in basis points.
- `StableReservePOLShareBps`: Share of `POLReserveSystemIncomeBps` routed to the Stable Reserve module while `StableReserveEnabled > 0`.
- `StableReserveWithdrawalRequestMax`: Maximum request-coin amount accepted for `STABLE-` withdrawals.
- `StableReserveOraclePricing`\*: `0` fills Stable Reserve swaps 1:1, `1` scales the fill by the oracle price ratio.
- `POLReserveSystemIncomeBps`\*: Share of system income routed to the POL reserve, in basis points. `0` disables.
- `POLReserveMaxDeployment`\*: Maximum RUNE the POL reserve deploys per pool per block (1e8 notation).
- `POLReserveBlacklist-<Asset>`#: Excludes a pool from POL reserve deployment. Takes precedence over the whitelist.
- `POLReserveWhitelist-<Asset>`#: Includes a pool in POL reserve deployment. Without either key, gas asset pools and TOR anchor pools are included.

Operational notes:

- RUNE is diverted to the Stable Reserve only when `StableReserveEnabled > 0`, `POLReserveSystemIncomeBps > 0`, and `StableReservePOLShareBps > 0`. If there are no healthy TOR-anchor stable targets, diverted RUNE stays idle in the Stable Reserve module balance until deployment becomes possible.
- Contributor credits are same-asset obligations. Stable Reserve automation treats outstanding contributor credits as reserved inventory; free stable-to-stable fills and depeg rescue can use only surplus inventory above total credits for that asset.
- Unfilled `STABLE-` withdrawals preserve contributor credit and return an error so normal refund handling can return the request coin.
- Every swap event includes the `stable_reserve` attribute. It is `true` only for Stable Reserve fills and `false` for normal swaps.

## App Layer Management

- `WasmPermissionless`#: Toggles permissionless deployment of CosmWasm smart contracts (default: 0, restricted).
- `WasmMinGasPrice`#: Sets the minimum gas price for CosmWasm transactions in the App Layer in RUNE.
- `HaltWasmGlobal`#: Pauses all CosmWasm smart contract executions in the App Layer.
- `HaltWasmCs-<checksum>`#: Halts a specific CosmWasm contract by its base32-encoded checksum.
- `HaltWasmDeployer-<address>`#: Halts all contracts deployed by a specific address using the full bech32 address.
- `HaltWasmContract-<address suffix>`#: Halts a specific CosmWasm contract using the last 6 characters of its address.
- `WasmArbContract`: Address of the WASM arbitrage contract that `WasmArbSlipMinBps` applies to.

## LP Management

- `PauseLP`#: Pauses the ability for LPs to add/remove liquidity
- `PauseLP<chain>`#: Pauses the ability for LPs to add/remove liquidity, per chain
- `MaximumLiquidityRune`#: Maximum RUNE capped on the pools known as the ‘soft cap’
- `LiquidityLockUpBlocks`: The number of blocks an LP must wait before they can withdraw their liquidity
- `PendingLiquidityAgeLimit`: The number of blocks the network waits before initiating pending liquidity cleanup. Cleanup of all pools lasts for the same duration.
- `PauseAsymWithdrawal-<Chain>`#: Forces dual-address liquidity providers to withdraw symmetrically rather than asymmetrically.
- `PauseLPDeposit-<Asset>`#: pauses the ability to add liquidity into that pool. E.g. `PAUSELPDEPOSIT-BTC-BTC=1` suspends deposits for the BTC pool
- `StrictBondLiquidityRatio`: Caps liquidity so pooled assets cannot exceed the bond (TVL cap)
- `DefaultPoolStatus`: Status of a new pool when it is created (`Staged`)
- `POL-<Asset>`#: Protocol-owned liquidity for a pool: `0` or unset = off, `1` = on, `2` = forced withdraw
- `POLMaxNetworkDeposit`\*: Maximum RUNE the protocol deposits into pools (1e8 notation)
- `POLMaxPoolMovement`\*: Maximum POL movement into or out of a pool per iteration (1 = one hundredth of a basis point of pool RUNE depth)
- `POLTargetSynthPerPoolDepth`\*: Target synth per pool depth for POL, in basis points
- `POLBuffer`\*: Buffer around `POLTargetSynthPerPoolDepth`, in basis points
- `RagnarokProcessNumOfLPPerIteration`\*: Number of LPs processed per iteration when a pool is ragnaroked
- `SaversStreamingSwapsInterval`\*: Streaming swap interval for the native-synth swap in savers deposits and withdrawals
- `SaversEjectInterval`\*: Number of blocks between savers utilization checks (0 = disabled)
- `LUVIOverhangGuard`\*: When above `0` (the default is `1`), rejects liquidity adds, from users and from POL, into a pool in synth overhang (synth supply at least twice the pool's asset depth). POL skips such pools, and aged pending liquidity in them stays pending (and withdrawable) instead of being committed. Withdrawals are unaffected. `0` turns the guard off
- `LUVIGuard`\*: When above `0` (the default is `1`), checks every handler run dispatched through the internal handler (including the swap queue and the messages that observed txs and deposits dispatch) and the wrapped EndBlock steps for drops in the hedged LP equity G of the pools it wrote (see `x/thorchain/types/type_luvi.go`). A user value flow (swap, add or withdraw liquidity, donate, trade/secured asset, RUNEPool and TCY messages) that lowers a pool's G beyond the computed allowance for gas, slashes, LP-flow rounding and synth mints is rejected and refunded. Other handlers, the same messages when the protocol sends them itself (POL, ragnarok, saver ejects, pending liquidity commits), and EndBlock steps only report. Messages handled directly by the external msg server (for example `MsgErrataTx` and errata quorum), BeginBlock, and the EndBlock steps that are not wrapped (observation tallies, volume, dynamic fees, scheduled migrations) are outside the guard: their pool changes reach only the accumulator, with no `luvi_loss` event. Within the guard, every drop beyond the allowance, and every LP-unit change without matching add/withdraw events, emits a `luvi_loss` event naming the pool, the scope and the inbound tx; covered drops are only recorded by the accumulator. With a virtual synth depth above 1x (`VirtualMultSynthsBasisPoints` above 10000, as on mocknet), pools whose synth supply moved are skipped. `0` turns the guard and the events off; the per-pool G accumulator on the pool endpoints always runs

## RunePool

- `RUNEPoolEnabled`: Enable/disable RUNE Pool
- `RUNEPoolDepositMaturityBlocks`: Minimum number of blocks from last RUNEPool deposit when a withdraw is allowed.
- `RUNEPoolMaxReserveBackstop`: Max amount of RUNE above the `POLMaxNetworkDeposit` that the reserve can enter RunePool before withdrawals are disabled.
- `RUNEPoolHaltDeposit`\*: Halts RUNEPool deposits (block height)
- `RUNEPoolHaltWithdraw`\*: Halts RUNEPool withdrawals (block height)
- `MaxAffiliateFeeBasisPoints`\*: Maximum affiliate fee in basis points for RUNEPool withdrawals

## Chain Management

- `HaltChainGlobal`#: Pause observations on all chains (chain clients)
- `HaltTrading`#: Stops swaps and additions, if done, will result in refunds. Observations still occur.
- `Halt<chain>Chain`#: Pause a specific blockchain
- `HaltTHORChain`#: Halts native THORChain transactions (sends, deposits and App Layer)
- `SolvencyHalt<Chain>Chain`#: Halts a chain if the solvency checker for that chain fails.
- `HaltScannerXMR`#: Bifrost-only XMR scanner activation switch. `0` explicitly enables block scanning and spent-reference catch-up, positive values pause them. Missing or negative values and lookup errors fail closed. This does not set THORNode's XMR chain-halt state or directly pause churn. A paused scanner also blocks normal XMR signing through the scanner-health check, so funded vault migrations can still stall and subsequently block churn. See [XMR Scanner Activation](./concepts/network-halts.md#xmr-scanner-activation) for rollout and voting requirements.
- `NodePauseChainGlobal`#: Individual node controlled means to pause all chains. The value is the block height until which chains are paused
- `NodePauseChainBlocks`: Number of block a node operator can pause/resume the chains for
- `BlocksPerYear`: Blocks in a year
- `MaxUTXOsToSpend`#: Max UTXOs to be spent in one block
- `XMRConsolidation<Setting>`#: Tunes Bifrost's Monero output consolidation (operational Mimir, as each run locks vault liquidity for Monero's 10-block spend lock): `XMRConsolidationEnabled` (`0` disables), `XMRConsolidationCheckIntervalBlocks`, `XMRConsolidationCooldownBlocks`, `XMRConsolidationMinUnlockedOutputs`, `XMRConsolidationTargetOutputs`, `XMRConsolidationMinUnlockedAtomic`, `XMRConsolidationReserveBps`, `XMRConsolidationChurnLookaheadBlocks`, `XMRConsolidationMaxRetries`, `XMRConsolidationMaxInputs` and `XMRConsolidationMaxLockBps`. Unset or non-positive values fall back to Bifrost defaults.
- `EnableMonovaultKeygen`#: Enables Bifrost XMR monovault keygen (economic Mimir)
- `HaltMonovaultKeygen`#: Halts Bifrost XMR monovault keygen (block height)
- `SOLDurableNonce`#: Lets Bifrost provision a Solana durable-nonce account for each active SOL vault. Once a vault's nonce is committed it keeps signing with it, even if this is reset
- `SOLAnchorMarginSeconds`#: Seconds Bifrost steps back from the keysign block time to choose the Solana blockhash anchor (unset = 15, clamped to 12-30)
- `SOLNonceClear-<pubkey>`#: Per-vault break-glass: a committed Solana durable nonce updated at or below this block height is ignored, so the vault signs with a recent blockhash. **CAUTION:** Set only after confirming on-chain that the committed nonce is consumed, or with `HaltSigningSOL` in place, otherwise an outbound can be paid twice.
- `SignatureRefreshBlocks`#: Number of THORChain blocks before Bifrost refreshes a Solana vault's keep-alive signature (0 = disabled)
- `SkipSignerCache<Chain>`#: Allows Bifrost to bypass the signer cache on expiry, e.g. `SkipSignerCacheBTC`
- `EVMAllowanceCheck-<Chain>`#: Bifrost checks the token allowance before an ERC20 outbound and sends an approval when needed
- `EVMDirectERC20Inbound-<Chain>`#: Bifrost observes ERC20 tokens sent directly to vault addresses (bypassing the router) as memoless inbounds
- `EVMDisableContractWhitelist`\*: Set to 1 for Bifrost to parse EVM transactions without the contract whitelist
- `CompromisedVault-<pubkey>`#: Marks a vault as compromised. It is removed from inbound addresses and inbounds sent to it are not processed
- `DisableTSSSignerFiltering`#: Block height from which nodes flagged to leave are no longer filtered out of keygen and keysign parties
- `PreflightChainSetDisabled`#: Disables the preflight check that keeps a node out of the Ready set after it reported a keygen chain set missing a chain the active vaults serve
- `KeygenRetryInterval`\*: Number of blocks Bifrost waits before retrying a keygen (0 = disabled)
- `P2PGateDisabled`#: Disables Bifrost P2P gating, which restricts connections to THORChain validator nodes
- `ScheduledMigration`#: Block height at which the scheduled migration is processed
- `MinimumNodesForBFT`: Minimum node count to keep the network running. Below this, Ragnarök is performed
- `MaxConfirmations-<Chain>`# : The maximum number of confirmations for a chain
- `ConfMultiplierBasisPoints-<Chain>`#: Increases or decrease the inbound confirmation count block requirement for a chain

### Fee Management

- `EnableUSDFees`\*: Enable USD-denominated fees. When set, the `*USD` fee settings below replace their RUNE equivalents and are converted to RUNE at the current price
- `NativeTransactionFeeUSD`\*: USD fee on all on chain txs (1e8 notation)
- `NativeOutboundFeeUSD`\*: USD fee withheld on outbound transactions (1e8 notation)
- `TNSRegisterFeeUSD`\*: Registration fee for new THORName, in USD (1e8 notation)
- `TNSFeePerBlockUSD`\*: Per block cost for TNS, in USD (1e8 notation)
- `FeeUSDRoundSignificantDigits`\*: Significant digits the RUNE value of a USD fee is rounded to
- `TNSRegisterFee`: Registration fee for new THORName, in RUNE
- `TNSFeeOnSale`: fee for TNS sale in basis points (not used)
- `MinimumL1OutboundFeeUSD`: Minimum L1 outbound fee in USD (1e8 notation)
- `TargetOutboundFeeSurplusRune`\*: Target RUNE surplus per asset (withheld - spent) for outbound fee calculations (1e8 notation)
- `MaxOutboundFeeMultiplierBasisPoints`\*: Maximum outbound fee multiplier in basis points
- `MinOutboundFeeMultiplierBasisPoints`\*: Minimum outbound fee multiplier in basis points
- `PreferredAssetOutboundFeeMultiplier`\*: Multiplier of preferred asset outbound fee for triggering preferred asset swaps
- `DYNAMICFEE-WHITELIST-<thorname>`#: Per-thorname [enrollment](./concepts/fees.md#dynamic-l1-minimum-fee) in the dynamic L1 minimum fee
- `REVSHARE-<thorname>`#: Per-thorname [share of swap liquidity fees](./affiliate-guide/affiliate-fee-guide.md#revenue-share-revshare) paid to the affiliate from the reserve, in basis points (max 5000)

### Solvency Checker

- `StopSolvencyCheck`#: Block height after which the Solvency Checker is stopped
- `StopSolvencyCheck<chain>`#: Block height after which the Solvency Checker is stopped, per chain
- `AllowNoVault-<Chain>`#: Authorizes `NOOP:NOVAULT` vault debits on the chain, used for a brief, coordinated insolvency correction (operational Mimir). Prefer setting it to a future block height so the window closes itself (agree the exact height off-chain first: operational votes only count when the values match, and a tie sets nothing); `1` holds it open until it is reset to `0`, and because churn purges operational node votes while keeping the effective value, a `1` gate outlives churn still listed in `/thorchain/mimir` but with no vote trail left in `/thorchain/mimir/nodes_all`.
- `OverSolvencyToTreasuryBps-<asset>`#: Per-asset basis points (0-10000) of over-solvent liquidity to swap to RUNE and transfer to the over-solvency sweep destination address, for example `OverSolvencyToTreasuryBps-BTC-BTC`. Falls back to `OverSolvencyToTreasuryBps` when unset.
- `PermittedSolvencyGap`: (deprecated) The amount of funds permitted to be "insolvent" as a percentage (basis points). Replaced by `PermittedSolvencyGapUSD`.
- `PermittedSolvencyGapUSD`: The permitted solvency gap in USD (1e8 notation). Vault insolvency below this USD value will not trigger a chain halt. Default is $500 (500_00000000).
- `SolvencyHaltVault-<pubkey>-<Chain>`#: Set automatically by the solvency checker when a vault is insolvent on a chain, and cleared when it is solvent again. A flagged vault is removed from inbound addresses
- `OverSolvencyToTreasuryBps`\*: Basis points (0-10000) of over-solvent liquidity to sweep, used when the per-asset key is unset
- `OverSolvencyCheckInterval`\*: Number of blocks between over-solvency checks
- `OverSolvencyAddress`: Over-solvency sweep destination address
- `VaultDeficitGasHaltMaxGasMultiplier`\*: Multiple of the chain's current max gas at which a gas-related vault deficit halts signing and trading on the chain (0 = disabled)

## Node Management

- `MinimumBondInRune`\*: Sets a lower bound on bond for a node to be considered to be churned in
- `ValidatorMaxRewardRatio`\*: the ratio to MinimumBondInRune at which validators stop receiving rewards proportional to their bond (not used)
- `MaxBondProviders`\*: Maximum number of bond providers per mode
- `NodeOperatorFee`\*: Minimum node operator fee
- `BondYieldEnabled`\*: Enables automatic bond-yield payouts during reward distribution
- `SignerConcurrency`\*: Number of concurrent signers for active and retiring vaults
- `MaximumBondInRune`#: Maximum bond per node (1e8 notation)
- `PauseBond`\*: Pause bonding
- `PauseUnbond`\*: Pause unbonding
- `HaltRebond`#: Halt rebonding
- `HaltOperatorRotate`#: Halt node operator rotation
- `OperationalVotesMin`\*: Minimum matching node votes for an operational Mimir to take effect

### Slashing Management

- `LackOfObservationPenalty`: Add two slash points for each block where a node does not observe
- `SigningTransactionPeriod`: How many blocks before a request to sign a tx by a vault, is counted as delinquent.
- `DoubleSignMaxAge`: Number of blocks to limit double signing a block
- `FailKeygenSlashPoints`: Slash for 720 blocks, which equals 1 hour
- `FailKeysignSlashPoints`: Slash for 2 blocks
- `ObserveSlashPoints`: The number of slashpoints for making an observation (redeems later if observation reaches consensus)
- `JailTimeKeygen`: Blocks a node account is jailed for failing to keygen. DO NOT drop below TSS timeout
- `JailTimeKeygenBase`: Base blocks a non-active node account is jailed for failing to keygen, multiplied by its keygen blames since the last churn. DO NOT drop below `JailTimeKeygen`
- `MaxKeygenBlameMultiplier`\*: Cap on the keygen blame multiplier applied to `JailTimeKeygenBase` and `FailKeygenSlashPoints` for non-active nodes (0 = no cap)
- `JailTimeKeysign`: Blocks a node account is jailed for failing to keysign. DO NOT drop below TSS timeout
- `BondSlashBan`: RUNE amount to slash bond of banned node
- `SlashPenalty`\*: Penalty for theft of vault assets, in basis points of the stolen value, slashed from the vault members' bonds
- `PauseOnSlashThreshold`\*: Slash amount (in 1e8 rune) at which signing and trading on the chain are halted automatically (0 = disabled)
- `DoubleBlockSignSlashPoints`\*: Slash points for double signing a block
- `MissBlockSignSlashPoints`\*: Slash points for not signing a block
- `MaxTrackMissingBlock`\*: Maximum number of missed blocks tracked per block signer
- `MaxAbsentKeygenReporters`\*: Number of keygen members allowed to not report before a successful keygen finalizes
- `AllowWideBlame`\*: Allow wide blame for keysign failures (mocknet regression testing only)

### Churning

- `AsgardSize`\*: Defines the number of members to an Asgard vault
- `MinSlashPointsForBadValidator`: Minimum quantity of slash points needed to be considered "bad" and be marked for churn out
- `BondLockupPeriod`: Lockout period that a node must wait before being allowed to unbond
- `ChurnInterval`\*: Number of blocks between each churn
- `HaltChurning`: Pause churning
- `DesiredValidatorSet`\*: Maximum number of validators
- `FundMigrationInterval`\*: Number of blocks between attempts to migrate funds between asgard vaults during a migration
- `NumberOfNewNodesPerChurn`#: Number of targeted additional nodes added to the validator set each churn
- `BadValidatorRedline`\*: Redline multiplier to find a multitude of bad actors
- `LowBondValidatorRate`: Rate to mark a validator to be rotated out for low bond (not used)
- `MaxNodeToChurnOutForLowVersion`\*: Maximum number of validators to churn out for low version each churn
- `MaxMissingObservationChurnOut`\*: Maximum number of validators to churn out each churn for missing most of the observations on a chain; `0` (the default) turns the mark off, while the tallies are still kept
- `MissingObservationChurnOutBps`\*: A validator is marked when, on any chain, the share of voters it failed to sign within `ObservationDelayFlexibility` is above this, in basis points
- `MissingObservationGuardBps`\*: A voter is not counted against anyone when more than this share of the validators missed it (a shared outage), in basis points, not counting validators already failing its chain that missed it; `10000` turns the guard off
- `MissingObservationGuardMaxBlocks`\*: The guard's grace on each chain, in blocks (default one churn interval). Skipping the chain's voters uses it up, time in which they pass the guard refills it at the same rate, and once it is used up the voters count again, so a group failing above the guard share is not immune for good; `0` keeps the guard open indefinitely
- `MissingObservationHalfLife`\*: Number of blocks between halvings of the missed-observation tallies, which restart at churn-in
- `MissingObservationMinSamples`\*: Minimum number of voters on a chain, after decay, before a validator's missed share on it counts
- `MigrationVaultSecurityBps`: Vault bond must be greater than bps of funds value in rune to receive migrations
- `ChurnRetryInterval`\*: Number of blocks before a failed churn is retried
- `ChurnMigrateRounds`\*: Number of rounds to migrate vault funds during a churn
- `ChurnOutForLowVersionBlocks`\*: Number of blocks after `MinJoinVersion` changes before nodes can be churned out for low version
- `MissingBlockChurnOut`\*: Number of missed blocks between churns that marks a validator for churn out (0 = disabled)
- `MaxMissingBlockChurnOut`\*: Maximum number of validators churned out for missing blocks (0 = disabled)

## Economics

- `EmissionCurve`\*: How quickly rune is emitted from the reserve in block rewards
- `ReserveMaxCap`\*: Maximum reserve balance (in 1e8 notation) before EmissionCurve is automatically overridden to default (6). Set to 0 to disable. This prevents excessive emissions when the reserve grows too large.
- `MaxAvailablePools`: Maximum number of pools allowed on the network. Gas pools (native pools) are excluded from this.
- `MinRunePoolDepth`\*: Minimum number of RUNE to be considered to become active
- `PoolCycle`\*: Number of blocks the network will churn the pools (add/remove new available pools)
- `StagedPoolCost`: Number of RUNE (1e8 notation) that a staged pool is deducted on each pool cycle.
- `KillSwitchStart`\*: Block height to start to kill BEP2 and ERC20 RUNE (not used)
- `KillSwitchDuration`: Duration (in blocks) until switching is deprecated (not used)
- `MinimumPoolLiquidityFee`: Minimum liquidity fee an active pool should accumulate to avoid being demoted, set to 0 to disable demote pool based on liquidity fee
- `MaxRuneSupply`\*: Maximum supply of RUNE
- `LendingLever`: Lending allowed relative to RUNE supply, in basis points (not used)
- `MinCR`: Minimum lending collateralization ratio, in basis points (not used)
- `MaxCR`: Maximum lending collateralization ratio, in basis points (not used)
- `SystemIncomeBurnRateBps`\*: Share of system income burned, in basis points
- `DevFundSystemIncomeBps`\*: Share of system income sent to the dev fund, in basis points
- `DevFundAddress`: Dev fund address
- `MarketingFundSystemIncomeBps`\*: Share of system income sent to the marketing fund, in basis points
- `MarketingFundAddress`: Marketing fund address
- `PendulumUseEffectiveSecurity`: Determines the [Incentive Pendulum](./concepts/incentive-pendulum.md) perception of the `securing`. If set to 1, `Effective Security Bond` is used; otherwise `Total Effective Bond` is applied.
- `PendulumUseVaultAssets`: Determines the [Incentive Pendulum](./concepts/incentive-pendulum.md) perception of the `secured`. If set to 1, `Vaulted Assets` is used; otherwise `Total Pooled` is applied.
- `PendulumAssetsBasisPoints`: Scales the Incentive Pendulum perception of the `secured` L1 asset size, where values above 100% overestimate and values below 100% underestimate the amount of `secured` assets.
- `TVLCapBasisPoints`\*: If 0, TVL Cap is set to the effective active bond. If non-zero, the value is interrupted as basis points relative to total active bond.

## Attestation Gossip

- `AttestationMaxBatchSize`: Maximum attestations to send in a batch
- `AttestationPeerConcurrentSends`: Maximum batches to concurrently send to peer
- `AttestationPeerConcurrentReceives`: Maximum batches to concurrently receive from a peer

## Miscellaneous

- `DollarsPerRune`: Manual override of number of dollars per one RUNE. Used for metrics data collection and RUNE calculation from MinimumL1OutboundFeeUSD
- `TorAnchor-<asset>`#: Set above zero to include a stablecoin pool in the [TOR](https://docs.thorchain.org/technical-documentation/thorchain-finance/tor) anchor set, e.g. `TorAnchor-ETH-USDC-0XA0B8...`. The median of these pools prices RUNE in USD, and a swap between two anchors uses `StableSlipMinBps`
- `THORNames`: Enable/Disable THORNames
- `TNSFeePerBlock`: TNS cost per block to retain ownership of a name
- `ArtificialRagnarokBlockHeight`: Triggers a chain shutdown and ragnarok
- `NativeTransactionFee`: The RUNE fee for a native transaction (gas cost in 1e8 notation)
- `HALTSIGNING<chain>`#: Halt signing in a specific chain
- `HALTSIGNING`#: Halt signing globally
- `Ragnarok-<Asset>`#: Ragnaroks a specific pool
- `BankSendEnabled`: Enable/Disable cosmos bank send messages
- `EnableSwitch-<Chain>-<Symbol>`#: Enables switching of the asset from this block height
- `MaxDepositTxIDRetries`\*: Maximum retries to find a unique tx ID for a native deposit
- `HaltOracle`#: Stops Bifrost publishing oracle prices
- `OracleUpdateInterval`#: Milliseconds between Bifrost oracle price updates
- `RequiredPriceFeeds`: Price feeds the oracle must report
- `ObservationDelayFlexibility`\*: Number of blocks of flexibility for a validator to get their slash points taken off for making an observation
- `BlockTimeMs`\*: Target block interval in milliseconds (`1500`-`10000`). While set above zero, each node steers its CometBFT commit wait (`timeout_commit`) every block so the mean interval between block header times tracks the target. The wait never drops below 200ms (or the whole target, if that is shorter), so the next proposer still collects late precommits; a target the network cannot reach with that wait runs slow. Unset or `0` leaves `timeout_commit` at the node's configured value; setting `0` after a target restores it. The constant (the default block time) is not applied on its own. Economic mimir (2/3 supermajority); block-count durations scale with it (see [Block Time](#block-time))

### Router Upgrading (DO NOT TOUCH!)

#### Old keys (pre 1.94.0)

- `MimirRecallFund`: Recalls Chain funds, typically used for router upgrades only (not used)
- `MimirUpgradeContract`: Upgrades contract, typically used for router upgrades only

#### New keys (1.94.0 and on)

- `MimirRecallFund<CHAIN>`: Recalls Chain funds, typically used for router upgrades only (not used)
- `MimirUpgradeContract<CHAIN>`: Upgrades contract, typically used for router upgrades only
