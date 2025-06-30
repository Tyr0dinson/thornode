# Oracle

## Overview

The price oracle is a component running in Bifrost and responsible for providing crypto asset prices to Thornode, so they can be consumed by applications on the app layer. Each node runs an instance of the oracle and reporting observed prices independently.

## Concept

### Oracle (Bifrost)

#### Price observation

The Oracle component is running in Bifrost and consists of a multitude of different price providers for different centralized exchanges. These providers either subscribe to websocket feeds or poll API endpoints in a specified interval.

Every second, the oracle collects all rates of all configured trading pairs from all enabled providers. It then calculates the USD value of each base asset and filters them for outliers.

#### Calculating USD value

The oracle tries to calculate the USD rates for every asset by "traversing" the trading pairs from USD to base asset. That means, for the trading pair `BTC/USDT`, it first collects all available `USDT/USD` rates and then computes the USD value for pairs with USDT as quote asset.

That means, that the oracle needs enough assets paired with USD to begin with. It will, for example, use `BTC/USD` to calculate the `USDT/USD` rate from `BTC/USDT`, if it has no, or not enough `USDT/USD` rates collected.

#### Catching outliers

Before sending the USD rates for every asset, the oracle also checks for outliers, in case one of the providers reports prices. It is doing that by calculating the median absolute deviation of all prices for each asset and removes all prices outside the set boundaries.

#### Volume weighted average

All remaining USD rates for every asset are averaged into a single value weighted by the 24h trading volume, reported by the provider.

### P2P Gossip (Bifrost)

#### Broadcast price feeds

To get the most recent prices into Thorchain, the oracle broadcasts the final USD rates, as soon as it finishes its computation. This is currently set to 1s.

#### Attestation handling

Price feeds use the same gossip mechanism like observed L1 transactions or fee updates, but with a twist: Because each node likely observes slightly different prices (due to latency to providers, different configured providers etc...), nodes can't verify or attest the rates they received from their peers. They only verify the signature of the feeds and forward them to their Thornode instance.


#### Timestamps

To avoid replacing a recent price feed with an older one, each feed message provides a timestamp of when it is sent by the oracle. Older messages are simply discarded.

#### Signatures

Each price feed payload is signed by the sending node and and indexed by its public key. This way it is guaranteed, that the price data can't be manipulated or forged by other nodes.

#### Batching

To reduce calls to Thornode but allow for fast updates (multiple times per second), new price feeds are grouped into a `MsgPriceFeedsBatch` and executed with a 100ms delay. So the gRPC endpoint is hit less than 10 times per second instead of 120 times.

### Enshrined Bifrost (Thornode)

#### Processing

The final calculation of the on chain price is done in the message handler of `MsgPriceFeedsBatch`. This message contains all observed price feeds ov every node up to this point. It is a single batch transaction to prevent reorder attacks. On every `BeginBlock` all oracle prices are removed, so if a node tries to execute a message before the price feed transaction, there are no oracle prices and the transaction fails.

#### Final on-chain price

To calculate the final on-chain prices, Thornode first checks the signature of each provided price feed and calculates the median absolute deviation for each asset. Prices from nodes above or below the allowed threshold are discarded. 

##### Stale price feeds
Price feeds that are older than the timestamp of the last block are discarded as well. This is to prevent a malicious node from storing old prices with valid signatures and use them for price manipulation.

##### Simple majority

If a simple majority of nodes have provided a valid price for an asset, this price is persisted to the KVStore and can be consumed inside the current block.