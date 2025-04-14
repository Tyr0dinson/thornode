# ADR 020: App-layer

## Changelog

- May 2025: Proposed

## Status

Proposed

## Context

THORChain's SDK V50 update in V3.0.0 (https://gitlab.com/thorchain/thornode/-/merge_requests/3756) enabled it to support an "app-layer" - a separate execution environment to its base layer. Accounts are allowed to contain RUST-based WASM code that execute deterministically in the transaction life-cycle and can trigger other messages. 

This ADR describes the Design, Guidelines, Emergency Procedures and Deployment Process of the App Layer. It also describes generally how apps can be deployed, managed and handled in the normal case as well as exploits or un-aligned behaviour. 

## Alternative Approaches

1) No App Layer - THORChain's historical position; attempting to build features as complete handlers/managers in the base layer. The key problem with this is developer bottleneck - since base layer code can easily touch sensitive vault/security logic, base-layer development is slow and cautious. 

2) Hosted App Layer - this design - where COSMWASM is deployed and hosted by the THORNode binary. Contracts execute as part of THORChain's state transitions for each block.  

3) Remote App Layer - using IBC and the [Cosmos Interchain Accounts](https://tutorials.cosmos.network/academy/3-ibc/8-ica.html) feature - a remote app layer hosted by external validators is possible. Users deposit IBC assets into a remote chain, and remote validators settle periodic state updates back to THORChain. 

## Decision - Hosted App Layer

A Hosted App Layer is most appropriate for THORChain currently:

1) WASM contracts with strict boundary conditions allow for a variety of apps to be developed quickly and easily by developers with low risk to the Base Layer. 
2) A Remote App Layer for a specific high-use app may be entertained in future, eg Perps, where blocktimes, state bloat and more limit the UX of a Hosted App. 

## Detailed Design

### WASM
The `x/wasm` module is implemented with the following features:

1) `MsgExecuteContract` can be called by any account to any other account, which can run logic and call:`MsgSend` or `MsgDeposit`
2) Permissioning, where only whitelisted deployer addresses can deploy whitelisted bytecode - this ensures a basic due diligence and quality control process. 
3) Control - where Node Mimir can pause any contract, any set of contracts or the entire app layer
```
HaltWasmGlobal
HaltWasmCs-%s
// Encode the checksum to base32 to fit within mimir's 64 char limit and case insenstivity. Truncate trailing `=` for brevity
HaltWasmContract-%s
// Use contract address checksum (last 6) for brevity and to fit inside mimir's 64 char length
```
4) Layer1 Callable - where a deposit can be made by an L1 account with an `X:ADDR:PAYLOAD` memo to directly call an app. 

Implemented as per https://gitlab.com/thorchain/thornode/-/merge_requests/3829

### Secured Assets
Contracts can move Secured Assets, which are wrapped L1 assets, represented in THORChain's state as native `x/bank` assets: 

1) Secured Assets are denomed like `btc-btc` for `BTC.BTC` and minted natively.
2) Secured Assets are not directly redeemed for vaulted assets, they are managed as a "share". This allows negative interest to be charged in future, or if a vault loss ever happens, a haircut to be handled. 
3) Secured Assets have all the features that Trade Assets have (swaps etc)
4) Mint with `secure+:thorAddress` and Redeem with `secure-:l1Address`
5) Pauseable 
```
HaltSecuredGlobal
HaltSecuredDeposit-%s
// Use with Chain
HaltSecuredWithdraw-%s
// Use with Chain
```

Implemented as per https://gitlab.com/thorchain/thornode/-/merge_requests/3721

### Token Factory
Contracts can mint using the [Token Factory](https://docs.osmosis.zone/osmosis-core/modules/tokenfactory/) - with the `x/denom` module 

1) Only WASM contracts can `MsgMint` - so tokens will be limited only by whitelisted contracts. This stops token-spam.
2) Currently X tokens are blocked as Base Layer pools - liquidity can only be provided to them on the App Layer itself. This will be reviewed in future. 


Implemented as per https://gitlab.com/thorchain/thornode/-/merge_requests/3837

### Switch Handler
Tokens can one-way migrate to the App Layer using the Switch Handler. 

1) Devs provide a source token, burn address and a target denom for each whitelisted `CHAIN-ASSET`
2) Users deposit tokens with a `SWITCH:thorAddress`. Source tokens are burnt (sent to burn address) and target tokens are minted to the address. 
3) Nodes use mimir to whitelist tokens that can switch
```
EnableSwitch-%s-%s
// Chain, Asset
```

Implemented as per https://gitlab.com/thorchain/thornode/-/merge_requests/3840

## Security

The Hosted App Layer design above achieves security by limiting what apps can call, query and use. 

### Call Limits
- `MsgMint`, eg to mint a Token to represent fungible units
- `MsgSend`, to send an asset to another address
- `MsgDeposit`, to call into THORChain's base layer, ie, to swap, bond, redeem
- `MsgExecuteContract`, to call into another contract

### Query Limits
- Only specific base layer query endpoints are made available to the App Layer to prevent possible non-deterministic queries

### Asset Limits
- Only native assets `rune, tcy`, secured assets `btc-btc` and X assets (Token Factory) can be moved by contracts

In short - apps are like "addresses that can do things" - they do not have any extra privilege that a RUNE addresss can do today (with the exception of `MsgMint`, which is only designed to prevent Token Spam by users). 

## App Layer Guidelines

This section sets out general operating principles that should guide App Layer Devs, THORNodes and the community in handling and growing the App Layer. 

### Quality

Apps should be high-quality, audited, tested and have UX support before being presented for base layer whitelisting. 

### Fee-sharing

THORNodes provide security, L1 observation, decentralisation and execution of app-layer contracts and assets; which is significantly expensive. Transaction fees are not enough to pay for 100+ nodes with RPCs to 10+ chains and counting each, since users generally object to high transaction fees ($1.00 and above). THORChain has set tx fees currently to 2c. 

Instead - Apps should offer fee-sharing to THORNodes directly from their service fees; eg
* Interest fees on lending/borrowing/stablecoins
* Trade/Liquidity fees from liquidity protocols
* Listing/Sale fees on marketplaces
* etc

Thus fees are collected from a variety of sustainable revenue sources that don't subject users to high on-chain transaction costs. The suggested fee-sharing arrangement should start at 50%, but over time can be reduced once the App Layer is significantly profitable - long term 15-30% (Marketplace fees) is likely. 

To do so, ensure that the share of fees collected in an app are swapped to RUNE and sent to the RESERVE Module address:
```
thor1dheycdevq39qlkxs2a6wuuzyn4aqxhve4qxtxt
```

### No Uncapped Systemic-risk Apps

A systemic-risk app is defined as an app that could accumulate large uncapped quantities of RUNE. Such an app could be RUNE-denominated borrow/lend, or RUNE-bond LSTs. 
These apps may be entertained only if a safe cap is enforced - a suggested cap would be no more than 10% of either the Active Bond or on-chain RUNE Liquidity. 

> Eg with 100m RUNE bonded and 50m RUNE in liquidity, a sensible LST cap would be 10m RUNE (10% of Active Bond) and a sensible RUNE collateral cap would be 10% of the liquidity - 5m RUNE. 

### Duplicate Apps Discouraged 

Apps are generally more successful if there is clear differentiation. This prevents developer angst and liquidity fragmentation. However competition and diversity should be encouraged. Diversity of apps should be encouraged during the due diligence process of base-layer whitelisting. Any question of duplication should be addressed. 

Duplicate apps by different teams may be permitted if a former app has failed, dev-ghosted or has no support any more. 

### App Upgradeability

Since whitelisted deployers can only deploy whitelisted contracts after having adequate base-layer due diligence, it is encouraged that all apps have a `migrate()` path to allow for contract upgrades. 

Upgrading contracts and ensuring state and contract-address stay the same allows for bug-fixes and improvements to be seamlessly and securely rolled out.  

More info: https://github.com/CosmWasm/cosmwasm 

### Contentious Apps

If an app becomes Contentious to the base-layer (as decided by THORNodes), then THORNodes can pause the app and request the dev address issues. Other nodes can join the pause or unpause if they disagree. 
Contentious apps could be any app becoming a risk to the protocol or its users. There is no clear category of apps that could meet this definition, it is left up to THORNodes to decide at the time. 

> Apps in breach of these Guidelines are contentious if the breach is easily proven. 

> Apps becoming a risk to the protocol and its users may be deemed contentious (amassing large amounts of assets with poor security or un-aligned developer interests, eg, rugs or scams)

> Privacy apps, dark-net marketplaces, betting platforms and more may be contentious if adequate controls are not put in place by the app-dev. "Adequate controls" should be determined as per other apps in other protocols. 



## Emergency Procedures
App-devs, Base-devs and THORNodes are responsible for responding to and managing security issues.

> THORChain does not subscribe to the philosophy that "code is forever law" - rather that the chain and assets are sovereign and that long term a safe and vibrant ecosystem will bring more users and fees - thus exploits can and should be handled. 

### Vulnerability Reported
If a P0 is reported, but no exploit yet, THORNodes can:
1) Pause the affected app, all copies of the app, or the entire app-layer
2) `make relay` the reason
3) Base Devs work with App devs to roll-out an app-upgrade

### Active Exploit
If an app is exploited and user assets are being moved, THORNodes can:
1) Pause the entire chain `make halt` which will pause everything, including `MsgSend` and Secured Assets withdrawals
2) Specifically identify the exploit and target more granular pauses (if applicable)
3) Base-devs to work with App-devs to roll-out an app-upgrade, and possible store-migrations to move stolen assets back. 

> There is a financial threshold that an exploit would entertain a widespread halt and upgrade. A $50 exploit would not call for it, but a $5m exploit would. It is left up to THORNodes to make their own decisions to pause/unpause. 

### App Upgrades
Apps that should be upgraded after a reported vulnerability or exploit should follow this process:
1) Submit the new app bytecode that migrates user-state correctly and addresses any issues to the Base Layer Devs
2) Apps that can't migrate can have their state and assets store-migrated to new contracts.


## Deployment

Devs wishing to deploy an App on THORChain should read this ADR. Any and all apps can be entertained. 

1) Build an app, conduct devnet testing and auditing. 
2) Ensure the app meets the App Layer Guidelines
3) Submit a finalised contract bytecode, deployer address and origin link (containing app description and documentation) to the Base Layer Tech lead for review and merging to the Base Layer. 
4) Deploy (3) on mainnet once approved
5) Be ready to handle feedback from THORNodes on app features and issues, as well as managing upgrades


## Rujira

[Rujira](https://gitlab.com/thorchain/rujira) is a well-funded alliance of apps with a cohesive brand, developer community and [developer tools](https://gitlab.com/thorchain/rujira-ui) that is likely to deploy and build the largest collection of apps on THORChain. 

Due to developer support, devnet tooling, audit-funding and more, app-devs are encouraged to be handled by the Ruji team. [More info can be found here](https://docs.rujira.network/developers/getting-started). 

RUJI is not exclusive to THORChain, and apps can be deployed outside of the RUJI Alliance if the app-dev wishes to support themselves on tooling, funding, testing and deployment. As long as an app meets the App Layer Guidelines and passes Base Layer Dev due diligence, then it can be deployed and hosted. 


### Summary

This ADR details the App-layer as of 2025. Some or all parts of this ADR may be superceded by an update if needed. 
