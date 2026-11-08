# Stagenet

Stagenet is a long-lived THORChain test network used for **continuous
pre-mainnet testing**. It runs the real binary and real Bifrost against real
external chains, so a release can be exercised end to end — swaps, liquidity,
churn, migrations, signing — before it is proposed on mainnet.

Because stagenet exists to test `develop` before mainnet, a stagenet release is
**`develop` (synced) plus the stagenet-specific commits** — never a cut that is
behind `develop`. See [`release.md`](release.md) for the canonical release
process; this document describes how release candidates (RCs) are rolled out on
stagenet.

## Release candidates switch the binary at a height — they do not bump the version

A stagenet RC is **a tag plus a binary switch at a chosen block height**. It is
**not** an on-chain upgrade proposal and it does **not** change the embedded
version string.

- **Every RC of a given `X.Y.Z` line keeps the embedded version `X.Y.Z`.** RC2,
  RC3, … all report the same `X.Y.Z`. There is no per-RC patch bump, no
  version-bump commit, and no `app/upgrades.go` change for an RC.
- The RC is applied by a **coordinated halt + binary swap**: every node is told
  to halt at a height `H`, and a new image is registered to take over at `H`.
  No version vote and no upgrade handler are involved.

This is safe because the switch is driven by the configured halt height and the
image registered for that height, not by the embedded version number. The
network's consensus version — the value that versioned consensus logic branches
on — is tracked in state and is **monotonic**: a node's reported version is only
ever raised, never lowered. Keeping the embedded version constant across RCs
therefore moves nothing in consensus state and avoids any upgrade-handler or
version-bump edits between RCs.

> An on-chain **upgrade proposal** (the mainnet path) is a different mechanism:
> it raises the network version through governance and runs a registered
> upgrade handler. RCs deliberately avoid that — the point of an RC is to swap
> the binary at a height with no version vote.

## RC rollout flow

The flow below is the same whether it is the first RC of a line or a re-cut.

1. **Cut.** Build the RC from `develop` (synced) plus the stagenet-specific
   commits. For a re-cut, build on the single per-version branch so the previous
   RC tag stays an ancestor (a linear history; never fork the branch). Tag the
   RC. No version bump.
2. **Build.** Produce the image and record it **by digest**. Pin the digest; do
   not deploy by a moving tag.
3. **Choose `H`.** Pick the switch height from the _measured_ block rate (sample
   two real headers; do not assume a nominal block time), leaving margin to
   build the image and stage every node. Treat the height as authoritative and
   any wall-clock time as approximate. Recompute `H` just before rollout so it
   is still in the future.
4. **Stage.** On every validator, set the halt height to `H` and register the
   new image at height `H`. Nodes halt **before** executing `H`, so the last
   committed block is `H-1`.
5. **Halt / parity check.** Confirm that **all** nodes have halted at `H-1` with
   an **identical app hash**. A single committed block has one app hash
   network-wide, so agreement at `H-1` is the proof there is no fork going into
   the switch. Do not proceed if any node diverges or is missing.
6. **Release step.** Remove the halt height and move the new image's registered
   height to `H-1`, then re-apply on each node so the halted nodes resume on the
   new binary and cross `H`. Restart any node still sitting in its halt so it
   picks up the new image.
7. **Verify.** Confirm the new binary is running on every node, that the app
   hash at `H+1` is identical across all nodes (no fork), that blocks are
   advancing, and that Bifrost is healthy on every node. Then run the RC's test
   plan.

## Notes

- **Height is authoritative; time is a courtesy.** Lead with the height; quote
  any time as an approximate range derived from the measured block rate.
- **Chart-first deploys.** Apply configuration (the halt height and the
  registered image) through the deployment's chart values and the standard
  install path — not ad-hoc patches — so the declared config and the running
  cluster stay in agreement.
- **Re-cuts stay linear.** Each re-cut builds on the per-version branch and adds
  the new changes; it does not re-cut from a different base. A re-cut that
  abandons the previous RC's lineage forks the network.
