# Token Lists and Whitelists

THORChain restricts support for tokens on EVM, TRON and Solana chains to explicitly whitelisted contracts and mints. This protects users from malicious or incompatible tokens.

## Purpose of Token Whitelists

- Prevent unsupported tokens from being deposited, swapped, or added to pools
- Avoid edge-case ERC-20 behavior (e.g., tokens with transfer fees, blacklists, or non-standard returns)
- Provide transparency to integrators and wallets

## Whitelist Enforcement

- Applies to EVM chains (ETH, AVAX, BSC, BASE, POL), TRON (TRC-20 tokens), and Solana (SPL tokens)
- On EVM chains, a token that is not on the list is rejected at the Router contract level; on TRON and Solana, Bifrost does not observe it
- Tokens must be listed to be eligible for THORChain pools, swaps, and LP adds

## Whitelist Sources

Each chain has its own maintained JSON whitelist:

| Chain                     | Whitelist URL                                                                                                                          |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Ethereum (ETH)            | [`eth_mainnet_latest.json`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tokenlist/ethtokens/eth_mainnet_latest.json)    |
| Avalanche (AVAX)          | [`avax_mainnet_latest.json`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tokenlist/avaxtokens/avax_mainnet_latest.json) |
| Binance Smart Chain (BSC) | [`bsc_mainnet_latest.json`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tokenlist/bsctokens/bsc_mainnet_latest.json)    |
| Base (BASE)               | [`base_mainnet_latest.json`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tokenlist/basetokens/base_mainnet_latest.json) |
| Polygon (POL)             | [`pol_mainnet_latest.json`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tokenlist/poltokens/pol_mainnet_latest.json)    |
| TRON (TRC-20)             | [`tron_mainnet_latest.json`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tokenlist/trontokens/tron_mainnet_latest.json) |
| Solana (SPL)              | [`sol_mainnet_latest.json`](https://gitlab.com/thorchain/thornode/-/raw/develop/common/tokenlist/soltokens/sol_mainnet_latest.json)    |

**Note**: Some tokens like $USDT or $USDC exist across multiple chains and may require separate listing per chain.

## How to Request New Tokens

To add a token:

1. Fork the appropriate repository (e.g., [THORNode](https://gitlab.com/thorchain/thornode))
2. Locate the relevant token list (e.g., `common/tokenlist/ethtokens/eth_mainnet_latest.json`)
3. Add the token with required metadata (name, symbol, address, decimals; for Solana also `min_deposit`, see below)
4. Submit a merge request for review

See example MR: [thornode!2085](https://gitlab.com/thorchain/thornode/-/merge_requests/2085/diffs)

## Token List Location

All token whitelists are stored in the THORNode repository under:

```text
common/tokenlist/
├── ethtokens/
├── avaxtokens/
├── bsctokens/
├── basetokens/
├── poltokens/
├── soltokens/
└── trontokens/
```

Each directory contains a `*_mainnet_latest.json` file with the whitelist and a corresponding Go file that embeds the JSON data.

On TRON and Solana a listed token is observed only once its address is also in the chain's Bifrost `whitelist_tokens` config.

## Token Support on Solana

Bifrost supports SPL tokens of the classic SPL Token program (`TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA`). Token-2022 mints are not supported: their extensions (transfer fees, transfer hooks, permanent delegates) break one-to-one vault accounting.

SPL support is off until the `SOLTokens` mimir is set to its activation slot (see [Mimir](../mimir.md)). Until then deposits are not observed or credited, and token transfers from outside the vaults before that slot never are. The mimir gates only deposits from outside the vaults: the vaults' own token transfers (outbounds, refunds and migrations) of whitelisted mints are always observed and their token balances always reported, so turning it back off, or setting it to a later slot, stops new deposits only. Set it only after every node that can sign for a vault runs SPL-aware Bifrost: an older Bifrost signs any Solana outbound as a native SOL transfer of the coin amount.

- **Assets**: a token is `SOL.<SYMBOL>-<MINT>`, for example `SOL.USDC-EPJFWDD5AUFQSSQEM2QN1XZYBAPC8G4WEGGKZWYTDT1V`. Asset symbols are uppercased, but mints are case-sensitive base58, so Bifrost resolves an asset to its mint through the token list and matches whitelisted mints exactly.
- **Deposits**: send the token to the vault's SOL address with an SPL Token `Transfer` or `TransferChecked`, with the memo in a Memo program instruction. Wallets credit the vault's associated token account for that mint, and Bifrost scans those accounts. The transfer and the memo may be top-level or invoked by another program (CPI), as a smart wallet does; a top-level memo takes precedence over one written through a CPI. The rest of the transaction is ignored unless it touches a vault: a Jito tip or a fee in SOL or tokens beside the deposit is fine, and the fee payer may be a sponsor. A transaction with a SOL transfer to or from a vault is observed as a SOL transaction, as before, and one with more than one token transfer into vault token accounts is not observed. The sender, to whom refunds go unless the memo sets a refund address, is the source account's owner before the transaction; for a source account created in the same transaction, its owner counts only if it signed the transfer itself. Neither account's owner may change within the transaction. When the source belongs to a program, such as a swap's output account, set a refund address in the memo (`DEST/REFUND`). Only the vault's associated token account is credited: tokens sent to any other token account the vault owns are not observed. A Jito bundle is a run of ordinary transactions, each observed on its own, so a deposit's transfer and memo must be in the same transaction. A deposit below the token's `min_deposit` (in the mint's base units) is not observed either, as SOL deposits below the dust threshold are not: without a floor a deposit costs only the transaction fee, so dust deposits would be the cheapest way to make every node post observations. Mainnet tokens set it to about the value of SOL's dust threshold (0.001 SOL); USDC and USDT use `100000` (0.1). The vaults' own transfers are observed whatever their amount.
- **Refunds and outbounds** go to the recipient wallet's associated token account. The vault creates it if it is missing (`CreateIdempotent`), paying about 0.00204 SOL of rent, which is booked as gas. Every SPL outbound fee therefore includes the `SOLTokenOutboundSurcharge` mimir (default 0.00204 SOL), converted to the token, whether or not the account exists. Send token outbounds to a wallet address, not to a token account address. Bifrost never signs one whose recipient address is itself a token account or a mint (owned by the SPL Token or Token-2022 program): the derived associated token account would belong to an address nobody can sign for. Such an outbound stays pending, with the tokens in the vault.
- **Decimals**: token amounts are converted to and from THORChain's 8 decimals. A mint with fewer decimals cannot express all of an 8-decimal amount, so outbounds are truncated to the mint's precision.
