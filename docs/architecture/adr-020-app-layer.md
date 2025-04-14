# ADR 020: App-layer

## Changelog

- May 2025: Proposed

## Status

Proposed

## Context

THORChain's SDK V50 update in V3.0.0 (https://gitlab.com/thorchain/thornode/-/merge_requests/3756) enabled it to have an "app-layer" which is a separate execution environment to its base layer. Accounts are allowed to contain RUST-based WASM code, that execute deterministically in the transaction life-cycle. 

This ADR describes the design, operating principles and boundary conditions of the App Layer, its developers and possible apps. It also describes generally how apps can be deployed, managed and handled in the normal case as well as exploits or un-aligned behaviour. 

## Alternative Approaches

1) No App Layer - this design has been how TC has historically operated; attempting to build features as complete handlers/managers in the base layer code. However the key problem with this is the developer bottleneck - since base layer code can easily touch sensitive vault/security logic, base-layer development is slow and cautious. 

2) Hosted App Layer - this design - where x/wasm is deployed and hosted by the THORNode binary. Contracts execute as part of THORChain's state transitions for each block.  

3) Remote App Layer - using IBC and [Cosmos Interchain Accounts](https://tutorials.cosmos.network/academy/3-ibc/8-ica.html) feature - a remote app layer hosted by external validators is possible. Users deposit IBC assets into a remote chain, and remote validators settle period state updates back at THORChain. 

## Decision - Hosted App Layer

A Hosted App Layer is most appropriate for THORChain:

1) WASM contracts with strict boundary conditions allow for a variety of apps to be developed quickly and easily by developers with low risk to the Base Layer. 
2) A Remote App Layer for a specific high-use app may be entertained in future, eg, Perps where blocktimes, state bloat and more limit the UX of a Hosted App. 

## Detailed Design

### Secured Assets


### WASM
The `x/wasm` module is implemented with the following features:

1) `MsgExecuteContract` can be called by any account to any other account, which can run logic and call:`MsgSend` or `MsgDeposit`
2) Permissioning, where only whitelisted deployer addresses can deploy whitelisted bytecode - this ensures a basic due diligence and quality control process. 
3) Control - where Node Mimir can pause any contract, any set of contracts or the entire app layer
```
Pause
```
4) Layer1 Callable - where a deposit can be made by an L1 account with an `X:contract:function` memo to directly call an app. 

Implemented as per https://gitlab.com/thorchain/thornode/-/merge_requests/3829



## Consequences

> This section describes the consequences, after applying the decision. All
> consequences should be summarized here, not just the "positive" ones.

### Positive

### Negative

### Neutral

## References

> Are there any relevant PR comments, issues that led up to this, or articles
> referenced for why we made the given design choice? If so link them here!

- {reference link}
