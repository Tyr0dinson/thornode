---
description: Understanding Transaction Memo In Detail
---

# Transaction Memos

### Overview

Transactions to THORChain pass user intent with the `MEMO` field on their respective chains. THORChain inspects the transaction object and the `MEMO` in order to process the transaction, so care must be taken to ensure the `MEMO` and the transaction are both valid. If not, THORChain will automatically refund the assets. All memos are listed [here](https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/memo/memo.go#L41).

THORChain uses specific [Asset Notation](asset-notation.md) for all assets. Assets and functions can be abbreviated and Affiliate Addresses and asset amounts can be shortened to [reduce memo length](memo-length-reduction.md).&#x20;

Guides have been created for [Swap](../swap-guide/quickstart-guide.md), [Savers](../saving-guide/quickstart-guide.md) and [Lending](../lending/quick-start-guide.md) to enable quoting and the automatic construction of memos for simplicity.&#x20;

### Memo Size Limits

THORChain has a [memo size limit of 250 bytes](https://gitlab.com/thorchain/thornode/-/blob/develop/constants/constants.go?ref\_type=heads#L32), any inbound tx sent with a larger memo will be ignored. Additionally, memos on UTXO chains are further constrained by the `OP_RETURN` size limit, which is [80 bytes](https://developer.bitcoin.org/devguide/transactions.html#null-data).&#x20;

### Format

All memos follow the format: `FUNCTION:PARAM1:PARAM2:PARAM3:PARAM4`

The function is invoked by a string, which in turn calls a particular handler in the state machine. The state machine parses the memo looking for the parameters which it simply decodes from human-readable strings.&#x20;

In addition, some parameters are optional. Simply leave them blank, but retain the `:` separator:

`FUNCTION:PARAM1:::PARAM4`

### Permitted Functions

The following functions can be put into a memo:

1. [**SWAP**](memos.md#swap)
2. [**DEPOSIT** **Savers**](memos.md#deposit-savers)
3. [**WITHDRAW Savers**](memos.md#withdraw-savers)
4. [**OPEN** **Loan**](memos.md#open-loan)
5. [**REPAY Loan**](memos.md#repay-loan)
6. [**ADD** **Liquidity**](memos.md#add-liquidity)
7. [**WITHDRAW** **Liquidity**](memos.md#withdraw-liquidity)
8. [**BOND**, **UNBOND** & **LEAVE**](memos.md#bond-unbond-and-leave)
9. [**DONATE** & **RESERVE**](memos.md#donate-and-reserve)
10. MIGRATE
11. [**NOOP**](memos.md#noop)

### Swap

Perform a swap.

**`SWAP:ASSET:DESTADDR:LIM/INTERVAL/QUANTITY:AFFILIATE:FEE`**

<table><thead><tr><th width="161.4771610766654">Parameter</th><th width="338.89853142855793">Notes</th><th>Conditions</th></tr></thead><tbody><tr><td>Payload</td><td>Send the asset to swap. </td><td>Must be an active pool on THORChain.</td></tr><tr><td><code>SWAP</code></td><td>The swap handler.</td><td><a href="memo-length-reduction.md#mechanism-for-transaction-intent-2">Also </a><code>s</code>, <code>=</code></td></tr><tr><td><code>:ASSET</code></td><td>The <a href="asset-notation.md">asset identifier.</a></td><td>Can be <a href="memo-length-reduction.md#shortened-asset-names">shortened</a>.</td></tr><tr><td><code>:DESTADDR</code></td><td>The destination address to send to. </td><td>Can use THORName.</td></tr><tr><td><code>:LIM</code> </td><td>The trade limit ie, set 100000000 to get a minimum of 1 full asset, else a refund.</td><td>Optional, 1e8 or <a href="memo-length-reduction.md#scientific-notation">Scientific Notation</a>.</td></tr><tr><td><code>/INTERVAL</code></td><td>Swap interval in blocks.</td><td>Optional, 1 means do not stream.</td></tr><tr><td><code>/QUANTITY</code></td><td>Swap Quantity. Swap interval times every Interval blocks.</td><td>Optional, if 0, network will determine the number of swaps.</td></tr><tr><td><code>:AFFILIATE</code></td><td>The <a href="fees.md#affiliate-fee">affiliate </a>address. RUNE is sent to Affiliate. </td><td>Optional. Must be THORName or THOR Address.</td></tr><tr><td><code>:FEE</code></td><td>The affiliate fee. Limited from 0 to 1000 Basis Points. </td><td>Optional. Limited from 0 to 1000 Basis Points.</td></tr></tbody></table>

**Syntactic Examples:**

* **`SWAP:ASSET:DESTADDR`** simple swap
* **`SWAP:ASSET:DESTADDR:LIM`** swap with trade limit
* **`SWAP:ASSET:DESTADDR:LIM/1/1`** swap with limit, do not stream swap
* **`SWAP:ASSET:DESTADDR:LIM/3/0`** swap with limit, optimise swap amount, every 3 blocks
* **`SWAP:ASSET:DESTADDR:LIM/1/0:AFFILIATE:FEE`** swap with limit, optimised and affiliate fee

**Actual Examples:**

* `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0` - swap to Ether, send output to the specified address.
* `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:10000000,` same as above except the Ether output should be more than 0.1 Ether else refund.&#x20;
* `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:10000000/1/1,` same as above except explicitly stated, do not stream the swap.&#x20;
* `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:10000000/3/0,` same as above except told to allow streaming swap, mini swap every 3 blocks and THORChain to work out the number of swaps required to achieve optimal price efficiency.&#x20;
* `SWAP:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:10000000/3/0:t:10`- same as above except will send 10 basis points from the input and send it to `t` (THORSwap's [THORName](../affiliate-guide/thorname-guide.md)).

The above memo can be further [reduced](memo-length-reduction.md) to:

`=:ETH.ETH:0xe6a30f4f3bad978910e2cbb4d97581f5b5a0ade0:1e6/3/0:t:10`

**Other examples:**

* `=:r:thor1el4ufmhll3yw7zxzszvfakrk66j7fx0tvcslym:19779138111` - swap to at least 197.79 RUNE
* `=:BNB/BUSD-BD1:thor15s4apx9ap7lazpsct42nmvf0t6am4r3w0r64f2:628197586176 -` Swap to at least 6281.9 Synthetic BUSD.&#x20;
* `=:BNB.BNB:bnb108n64knfm38f0mm23nkreqqmpc7rpcw89sqqw5:544e6/2/6` - swap to at least 5.4 BNB, using streaming swaps, six swaps, every two blocks.

### **Deposit Savers**

**`ADD:POOL::AFFILIATE:FEE`**

Depositing savers can work without a memo; however, memos are recommended to be explicit about the transaction intent.

| Parameter    | Notes                                                                       | Conditions                                     |
| ------------ | --------------------------------------------------------------------------- | ---------------------------------------------- |
| Payload      | The asset to add liquidity with.                                            | Must be supported by THORChain.                |
| `ADD`        | The Deposit handler.                                                        | Also `a` `+`                                   |
| `:POOL`      | The pool to add liquidity to.                                               | Gas and stablecoin pools only.                 |
| `:`          | Must be empty                                                               | Optional, Required if adding affiliate and fee |
| `:AFFILIATE` | The [affiliate](fees.md#affiliate-fee) address. RUNE is sent to Affiliate.  | Optional. Must be THORName or THOR Address.    |
| `:FEE`       | The affiliate fee. Limited from 0 to 1000 Basis Points.                     | Optional. Limited from 0 to 1000 Basis Points. |

**Examples**

* `+:BTC/BTC` add to the BTC Savings Vault
* `a:ETH/ETH` add to the ETH Savings Vault
* `+:BTC/BTC::t:10` Deposit with a 10 basis points affiliate

### Withdraw Savers

**`WITHDRAW:POOL`**

| Parameter      | Notes                                                                                                                                                         | Extra                                                                                                                                                      |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Payload        | Send the [dust threshold](https://midgard.ninerealms.com/v2/thorchain/inbound\_addresses) of the asset to cause the transaction to be picked up by THORChain. | Caution [Dust Limits](https://midgard.ninerealms.com/v2/thorchain/inbound\_addresses): BTC,BCH,LTC chains 10k sats; DOGE 1m Sats; ETH 0 wei; THOR 0 RUNE.  |
| `WITHDRAW`     | The withdraw handler.                                                                                                                                         | Also `-` `wd`                                                                                                                                              |
| `:POOL`        | The pool to withdraw liquidity from.                                                                                                                          | Gas and stablecoin pools only.                                                                                                                             |
| `:BASISPOINTS` | Basis points (0-10000, where 10000=100%).                                                                                                                     | Optional. Limited from 0 to 1000 Basis Points.                                                                                                             |

**Examples**

* `-:BTC/BTC:10000` Withdraw 100% from BTC Savers
* `w:ETH/ETH:5000` Withdraw 50% from ETH Savers

### **Open Loan**

**`LOAN+:ASSET:DESTADDR:MINOUT:AFFILIATE:FEE`**

<table><thead><tr><th width="178.1692373378876">Parameter</th><th width="319.5924083090204">Notes</th><th>Conditions</th></tr></thead><tbody><tr><td>Payload</td><td>The collateral to open the loan with.</td><td>Must be L1 supported by THORChain.</td></tr><tr><td><code>LOAN+</code></td><td>The Loan Open handler.</td><td>also <code>$+</code></td></tr><tr><td><code>:ASSET</code></td><td>Target debt <a href="asset-notation.md">asset identifier.</a></td><td>Can be shortened.</td></tr><tr><td><code>:DESTADDR</code></td><td>The destination address to send the debt to. </td><td>Can use THORName.</td></tr><tr><td><code>:MINOUT</code></td><td>Similar to LIM, Min debt amount, else a refund.</td><td>Optional, 1e8 format.</td></tr><tr><td><code>:AFFILIATE</code></td><td>The <a href="fees.md#affiliate-fee">affiliate</a> address. The affiliate is added in to the pool as an LP. </td><td>Optional. Must be THORName or THOR Address.</td></tr><tr><td><code>:FEE</code></td><td>The affiliate fee. Fee is allocated to the affiliate. </td><td>Optional. Limited from 0 to 1000 Basis Points.</td></tr></tbody></table>

{% hint style="warning" %}
Affiliate and Affiliate Fee yet to be implemented
{% endhint %}

**Examples:**

* `$+:BNB.BUSD:bnb177kuwn6n9fv83txq04y2tkcsp97s4yclz9k7dh` - Open a loan with BUSD as the debt asset
* `$+:ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48:0x1c7b17362c84287bd1184447e6dfeaf920c31bbe:10400000000` Open a loan where the debt is at least 104 USDT.&#x20;

### **Repay Loan**

**`LOAN-:ASSET:DESTADDR:MINOUT`**

<table><thead><tr><th width="178.1692373378876">Parameter</th><th width="319.5924083090204">Notes</th><th>Conditions</th></tr></thead><tbody><tr><td>Payload</td><td>The repayment for the loan.</td><td>Must be L1 supported on THORChain.</td></tr><tr><td><code>LOAN-</code></td><td>The Loan Repayment handler.</td><td>also <code>$-</code></td></tr><tr><td><code>:ASSET</code></td><td>Target calloteral <a href="asset-notation.md">asset identifier.</a></td><td>Can be shortened.</td></tr><tr><td><code>:DESTADDR</code></td><td>The destination address to send the collateral to. </td><td>Can use THORName.</td></tr><tr><td><code>:MINOUT</code></td><td>Similar to LIM, Min collateral to receive else a refund.</td><td>Optional, 1e8 format, loan needs to be fully repaid to close. </td></tr></tbody></table>

#### Examples <a href="#swap" id="swap"></a>

* `LOAN-:BTC.BTC:bc1qp2t4hl4jr6wjfzv28tsdyjysw7p5armf7px55w` Repay BTC loan owned by owner bc1qp2t4hl4jr6wjfzv28tsdyjysw7p5armf7px55w.
* `LOAN-:ETH.ETH:0xe9973cb51ee04446a54ffca73446d33f133d2f49:404204059`. Repay ETH loan owned by `0xe9973cb51ee04446a54ffca73446d33f133d2f49` and receive at least 4.04 ETH collateral back, else send back a refund.

### Add Liquidity

There are rules for adding liquidity, see [the rules here](https://docs.thorchain.org/learn/getting-started#entering-and-leaving-a-pool).\
**`ADD:POOL:PAIREDADDR:AFFILIATE:FEE`**

<table><thead><tr><th width="178.1692373378876">Parameter</th><th width="319.5924083090204">Notes</th><th>Conditions</th></tr></thead><tbody><tr><td>Payload</td><td>The asset to add liquidity with. </td><td>Must be supported by THORChain.</td></tr><tr><td><code>ADD</code></td><td>The Add Liquidity handler.</td><td>also <code>a</code> <code>+</code></td></tr><tr><td><code>:POOL</code></td><td>The pool to add liquidity to.</td><td>Can be shortened.</td></tr><tr><td><code>:PAIREDADDR</code></td><td>The other address to link with. If on external chain, link to THOR address. If on THORChain, link to external address. If a paired address is found, the LP is matched and added. If none is found, the liquidity is put into pending. </td><td>Optional. If not specified, a single-sided add-liquidity action is created. </td></tr><tr><td><code>:AFFILIATE</code></td><td>The <a href="fees.md#affiliate-fee">affiliate</a> address. The affiliate is added in to the pool as an LP. </td><td>Optional. Must be THORName or THOR Address.</td></tr><tr><td><code>:FEE</code></td><td>The affiliate fee. Fee is allocated to the affiliate. </td><td>Optional. Limited from 0 to 1000 Basis Points.</td></tr></tbody></table>

**Examples:**

* **`ADD:POOL`** single-sided add liquidity.  If this is a position's first add, liquidity can only be withdrawn to the same address.
* **`+:POOL:PAIREDADDR`** add on both sides.&#x20;
* **`+:POOL:PAIREDADDR:AFFILIATE:FEE`** add with affiliate
* `+:BTC.BTC:`

### Withdraw Liquidity

Withdraw liquidity from a pool.\
A withdrawal can be either dual-sided (withdrawn based on pool's price) or entirely single-sided (converted to one side and sent out).

**`WD:POOL:BASISPOINTS:ASSET`**

<table><thead><tr><th width="209">Parameter</th><th width="305">Notes</th><th>Extra</th></tr></thead><tbody><tr><td>Payload</td><td>Send the dust threshold of the asset to cause the transaction to be picked up by THORChain. </td><td>Caution <a href="https://midgard.ninerealms.com/v2/thorchain/inbound_addresses">Dust Limits</a>: BTC,BCH,LTC chains 10k sats; DOGE 1m Sats; ETH 0 wei; THOR 0 RUNE. </td></tr><tr><td><code>WITHDRAW</code></td><td>The withdraw handler.</td><td>Also <code>-</code> <code>wd</code></td></tr><tr><td><code>:POOL</code></td><td>The pool to withdraw liquidity from.</td><td>Can be shortened.</td></tr><tr><td><code>:BASISPOINTS</code></td><td>Basis points (0-10000, where 10000=100%).</td><td></td></tr><tr><td><code>:ASSET</code></td><td>Single-sided withdraw to one side.</td><td>Optional. Can be shortened. Must be either RUNE or the ASSET.</td></tr></tbody></table>

**Examples:**

* **`WITHDRAW:POOL:10000`** dual-sided 100% withdraw liquidity.  If a single-address position, this withdraws single-sidedly instead.
* **`-:POOL:1000`** dual-sided 10% withdraw liquidity.&#x20;
* **`wd:POOL:5000:ASSET`** withdraw 50% liquidity as the asset specified while the rest stays in the pool, eg:
* `wd:BTC.BTC:5000:BTC.BTC`

### DONATE & RESERVE

Donate to a pool or the RESERVE.

**`DONATE:POOL`**

<table><thead><tr><th width="153.3006301940066">Parameter</th><th width="150">Notes</th><th>Extra</th></tr></thead><tbody><tr><td>Payload</td><td>The asset to donate to a THORChain pool.</td><td>Must be supported by THORChain. Can be RUNE or ASSET.</td></tr><tr><td><code>DONATE</code></td><td>The donate handler.</td><td>Also <code>%</code></td></tr><tr><td><code>:POOL</code></td><td>The pool to withdraw liquidity from.</td><td>Can be shortened.</td></tr></tbody></table>

**Example:** `DONATE:ETH.ETH` - Donate to the ETH pool.

**`RESERVE`**

<table><thead><tr><th width="154.85927710812584">Parameter</th><th width="187.65003293833834">Notes</th><th>Extra</th></tr></thead><tbody><tr><td>Payload</td><td>THOR.RUNE.</td><td>The RUNE to credit to the RESERVE.</td></tr><tr><td><code>RESERVE</code></td><td>The reserve handler.</td><td></td></tr></tbody></table>

### BOND, UNBOND & LEAVE

Perform node maintenance features. Also see [Pooled Nodes](https://docs.thorchain.org/thornodes/pooled-thornodes).

**`BOND:NODEADDR:PROVIDER:FEE`**

<table><thead><tr><th width="228.24376113012488">Parameter</th><th width="255">Notes</th><th>Extra</th></tr></thead><tbody><tr><td>Payload</td><td>The asset to bond to a  Node.</td><td>Must be RUNE.</td></tr><tr><td><code>BOND</code></td><td>The bond handler.</td><td>Anytime. </td></tr><tr><td><code>:NODEADDR</code></td><td>The node to bond with.</td><td></td></tr><tr><td><code>:PROVIDER</code></td><td>Whitelist in a provider.</td><td>Optional, add a provider</td></tr><tr><td><code>:FEE</code></td><td>Specify an Operator Fee in Basis Points.</td><td>Optional, default will be the mimir value (2000 Basis Points). Can be changed anytime. </td></tr></tbody></table>

**`UNBOND:NODEADDR:AMOUNT`**

<table><thead><tr><th width="153.3006301940066">Parameter</th><th width="150">Notes</th><th>Extra</th></tr></thead><tbody><tr><td>Payload</td><td>None required.</td><td>Use <code>MsgDeposit</code>.</td></tr><tr><td><code>UNBOND</code></td><td>The unbond handler.</td><td></td></tr><tr><td><code>:NODEADDR</code></td><td>The node to unbond from.</td><td>Must be in standby only.</td></tr><tr><td><code>:AMOUNT</code></td><td>The amount to unbond.</td><td>In 1e8 format. If setting more than actual bond, then capped at bond. </td></tr><tr><td><code>:PROVIDER</code></td><td>Unwhitelist a provider.</td><td>Optional, remove a provider</td></tr></tbody></table>

**`LEAVE:NODEADDR`**

<table><thead><tr><th width="153.3006301940066">Parameter</th><th width="150">Notes</th><th>Extra</th></tr></thead><tbody><tr><td>Payload</td><td>None required.</td><td>Use <code>MsgDeposit</code></td></tr><tr><td><code>LEAVE</code></td><td>The leave handler.</td><td></td></tr><tr><td><code>:NODEADDR</code></td><td>The node to force to leave.</td><td>If in Active, request a churn out to Standby for 1 churn cycle. If in Standby, forces a permanent leave. </td></tr></tbody></table>

**Examples:**&#x20;

* `BOND:thor19m4kqulyqvya339jfja84h6qp8tkjgxuxa4n4a`
* `UNBOND:thor1x2whgc2nt665y0kc44uywhynazvp0l8tp0vtu6:750000000000`
* `LEAVE:thor1hlhdm0ngr2j4lt8tt8wuvqxz6aus58j57nxnps`

### MIRGRATE

Internal memo type used to mark migration transactions between a retiring vault and a new Asgard vault during churn. Special THORChain triggered outbound tx without a related inbound tx.&#x20;

**`:MIGRATE`**

| Parameter      | Notes                           | Extra                       |
| -------------- | ------------------------------- | --------------------------- |
| Payload        | Assets migrating                |                             |
| `MIGRATE`      | The migrate Handler             |                             |
| `:BlockHeight` | THORChain Blockhight to migrate | Must be a valid blockheight |

[Example](https://viewblock.io/thorchain/tx/661AA4D05E75FD60FDE340B25716B840891B13F058E1756C8C9C335067DB1D9A): `MIGRATE:3494355`

### NOOP

Dev-centric functions to fix THORChain state. Caution: may cause loss of funds if not done exactly right at the right time.&#x20;

**`NOOP`**

<table><thead><tr><th width="146.1615145988245">Parameter</th><th width="263">Notes</th><th>Extra</th></tr></thead><tbody><tr><td>Payload</td><td>The asset to credit to a vault.</td><td>Must be ASSET or RUNE.</td></tr><tr><td><code>NOOP</code></td><td>The noop handler.</td><td>Adds to the vault balance, but does not add to the pool.</td></tr><tr><td><code>:NOVAULT</code></td><td>Do not credit the vault.</td><td>Optional. Just fix the insolvency issue.</td></tr></tbody></table>

### Refunds

The following are the conditions for refunds:

| Condition                | Notes                                                                                                        |
| ------------------------ | ------------------------------------------------------------------------------------------------------------ |
| Invalid `MEMO`           | If the `MEMO` is incorrect the user will be refunded.                                                        |
| Invalid Assets           | If the asset for the transaction is incorrect (adding an asset into a wrong pool) the user will be refunded. |
| Invalid Transaction Type | If the user is performing a multi-send vs a send for a particular transaction, they are refunded.            |
| Exceeding Price Limit    | If the final value achieved in a trade differs to expected, they are refunded.                               |

Refunds cost fees to prevent Denial of Service attacks. The user will pay the correct outbound fee for that chain.

### **Other Internal Memos**

* `donate` - add funds to a pool (example:`DONATE:ETH.ETH`)
* `consolidate` - consolidate UTXO transactions
* `ragnarok` - only used to delist pools.
* `yggdrasilfund` and `yggdrasilreturn` - no longer used as yggdrasil vaults are not longer used (ADR 002)
* `switch` - no longer used as killswich has ended
* `reserve` - no longer used.
