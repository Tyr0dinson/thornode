# Session 1 - THORChain Darknodes Tor Design

> Date: 2026-05-29
> Focus: Implement the darknodes whitepaper design across Thornode and the companion node-launcher deployment repo.

## Summary

Implemented the whitepaper's repo-level Tor darknode design across Thornode and the separate node-launcher repo. Thornode and Bifrost now support gated `.onion` validator registration and `/onion3` TSS transport, while node-launcher now provisions the Tor sidecar and hidden-service routing needed to run it. Helm validation also exposed and closed two real chart bugs before wrap-up.

## Work Done

- Created fresh GitLab worktrees for Thornode and node-launcher to keep the implementation isolated from the main workspace.
- Added Thornode `.onion` validation, a `TorOnionValidatorEnabled` Mimir gate, validator preflight support, and script/config support for onion registration.
- Added Bifrost Tor SOCKS transport support, onion advertisement, `/onion3` persistence and bootstrap handling, and peerstore-based `GetKnownPeers()` fallback for `.onion` peers.
- Added node-launcher Tor flags, `ONION_ADDRESS` handling, an internal gateway service, torrc generation, Tor sidecar wiring, and a Tor data PVC.
- Installed Helm locally, rendered the chart in default and Tor-enabled modes, found two real template issues, fixed them, and re-ran render/lint validation successfully.
- Compared the implementation against the PDF design and closed the remaining repo-local gaps, leaving only live rollout and testnet validation.

## Discoveries

- The whitepaper spans two repos: `thornode` and `node-launcher`. Treating the work as Thornode-only leaves the deployment track incomplete.
- Helm render validation caught a real YAML layout bug in `thornode/templates/bifrost/pvc.yaml` and a real boolean logic bug where `default true` prevented Tor exposure flags from honoring explicit `false`.
- In `thornode-stack`, `gateway.enabled` and `global.gateway.enabled` control different layers and both matter when testing gateway-disabled Tor flows.
- The remaining whitepaper work is operational rather than repo-local: sentry deployment, live hidden-service bring-up, bootstrap distribution, and Section 8.5 mixed-network validation.

## Files Changed

| File | Change |
|------|--------|
| bifrost/p2p/communication.go | Added onion advertisement, mutual exclusion checks, and Tor transport registration plumbing. |
| bifrost/p2p/onion_transport.go | Added the dial-only `/onion3` Tor transport implementation. |
| bifrost/tss/go-tss/tss/tss.go | Updated `GetKnownPeers()` to prefer peerstore IPv4 and fall back to `.onion`. |
| x/thorchain/handler_ip_address.go | Added the `TorOnionValidatorEnabled` keeper-level gate. |
| x/thorchain/types/msg_ip_address.go | Added Tor v3 `.onion` format validation for `set-ip-address`. |
| config/config.go | Added Tor-related Bifrost config handling and bootstrap parsing support. |
| build/scripts/core.sh | Added `ONION_ADDRESS` support in the local helper path. |
| ../node-launcher-darknodes/thornode/templates/cosmosfullnode.yaml | Added Tor init flow, torrc generation, Tor sidecar, and Bifrost onion env wiring. |
| ../node-launcher-darknodes/gateway/templates/service-internal.yaml | Added an internal ClusterIP gateway service for Tor-routed monitoring ports. |
| ../node-launcher-darknodes/thornode/templates/bifrost/pvc.yaml | Added Tor data PVC support and fixed render-safe YAML layout. |

## In Progress

Operational rollout remains: deploy the sentry topology and execute the Section 8.5 testnet checklist against a live Tor-enabled validator set.

## Next Steps

- [ ] Deploy the CometBFT sentry topology for validator privacy.
- [ ] Roll out the Tor-enabled node-launcher stack on testnet and retrieve the live hidden-service hostname.
- [ ] Add the `/onion3/.../p2p/...` bootstrap peer to active validators and verify DHT propagation.
- [ ] Execute the Section 8.5 mixed IPv4 and `.onion` keygen/keysign checklist.
- [ ] Publish the Thornode and node-launcher branches together for review.
