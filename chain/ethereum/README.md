# Eth Vault Contract

Vault Contract for Bifrost
* Incoming deposits register a `memo` and either
  - A. forward funds to chosen vault
  - B. give vault allowance to spend for specified asset
* Vaults can transfer allowance to spend to other vaults (asgard, yggdrasil)
* Vaults can transferOut up to their allowance to spend

This mirrors how vaults behave on other chains.

## THORChain Integration

### Bifrost - Observer

**ETH & ERC-20 Deposits**
Users query THORChain for the correct asgard, then call deposit with a memo:
* ETH/ERC20: `await VAULT.depositWithExpiry(vault, asset, amount, memo, expiration)`

Bifrost should parse contract events to read the `memo` for asset transfers.

*Note 1: ETHER uses asset `0x0000000000000000000000000000000000000000`*
*Note 2: expiration is in seconds*
*Note 3: `deposit` method without expiry was deprecated in RouterV4.1 and removed in RouterV5*

### Bifrost - Signer
Asgard/Ygg vaults should compose the transactions:
* ETH: `await web3.eth.sendTransaction({ from:0xself, value:value, to:to, data:memo })` 
* ERC20: `await VAULT.transferOut(to, asset, value, memo)` 
* Aggregation `await VAULT.transferOutAndCall(target, fromAsset, fromAmount, toAsset, recipient, amountOutMin, memo, flags)`

### Funding Ygg (only ERC20)
Ygg can be funded by transfering an allowance to spend:
`await VAULT.TransferAllowance(vault, asset, amount, memo)`

Ygg can return assets by batch transfering back:
`await VAULT.batchTransferAllowance(asgard[], assets[], amounts[], memos[])`

### Churning (only ERC20)
Asgard can transfer allowance to spend across 5 iterations:
`await VAULT.transferAllowance(newAsgard, asset, amount, memo)`

## Contract Design 

**Public Getters**
Tracks vault allowances
```solidity
mapping(address => mapping(address => uint)) public vaultAllowance;
```

**Events**
```solidity
event Deposit(address indexed to, address indexed asset, uint amount, string memo);
event TransferOut(address indexed vault, address indexed to, address asset, uint amount, string memo);
event TransferOutAndCall(address indexed vault, address target, uint amount, address finalAsset, address indexed to, uint256 amountOutMin, string memo, uint256[] flags)
event TransferAllowance(address indexed oldVault, address indexed newVault, address asset, uint amount, string memo);
event VaultTransfer(address indexed oldVault, address indexed newVault, Coin[] coins, string memo);
```

### Testing

```bash
npx hardhat clean
npx hardhat compile
npx hardhat test
```

## Deployment
Get ABI and BYTECODES from `/artifacts`
