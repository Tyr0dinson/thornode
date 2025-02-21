# Cardano Bifrost Designs

## Dolos & UTXoRPC

[UTXoRPC](https://utxorpc.org/cardano) on top of [dolos](https://github.com/txpipe/dolos). 

Dolos is a lightweight Cardano node implemented in Rust and features a GRPC API satisfying the
UTXoRPC spec.  Preliminary tests show dolos using < 1GB of memory and 10GB of chain data on disk
after syncing to tip from a snapshot.

[utxorpc/go-sdk](https://github.com/utxorpc/go-sdk) will be used to interact with Dolos's GRPC
API, and seems to roughly satisfy the existing utxo client in `bitfront/pkg/chainclients/utxo`
which should (hopefully) speed up initial development and testing.

Dolos does not yet implement [ReadTip](https://utxorpc.org/sync/intro#readtip), so bifrost must
be boostrapped with an initial block height and hash. Once bootstrapped
[FollowTip](https://utxorpc.org/sync/intro#readtip) will stream block slots and hashes to block
scanner storage so that either `FetchBlock` or `DumpHistory` can be used to retrieve block data.  It's also possible that `FollowTip` may be complete enough without any UTxO queries at all.

Dolos features a TX submission method but offers no support for serializing and signing.  It's API expects a properly CBOR encoded Cardano TX. For this [go-cardano-serialization](https://github.com/fivebinaries/go-cardano-serialization) (which satisfies the [shelley era CDDL spec](https://github.com/Emurgo/cardano-serialization-lib/blob/master/specs/shelley.cddl)) seems to be mosty complete and supports
building, serializing, and signing Cardano transactions.  Presumably the [Metadata field](https://github.com/fivebinaries/go-cardano-serialization/blob/ec9b850865882fb1122d3f373670a76eb6b33d55/tx/tx.go#L17)
will be used to carry tx memo operations.

**Pros:**

- Closely follows existing patterns in Bifrost reducing risk of integration issues.
- Leveraging existing Rust and Go libraries reduces development time.

**Cons:**

- Dolos is required as a sidecar to Bifrost in order to interact with the Cardano network.
- Stuck with assumptions of existing libraries (unclear if this is an issue yet)

## Cardano network (Ouroboros) protocol

Satisfy the node and/or client side of the Ouroboros network spec directly in Go, importantly [chain-sync](https://ouroboros-network.cardano.intersectmbo.org/pdfs/network-spec/network-spec.pdf#page=23&zoom=100,94,862) and [local tx submission](https://ouroboros-network.cardano.intersectmbo.org/pdfs/network-spec/network-spec.pdf#page=38&zoom=100,94,912), by using [blinklabs-io/gouroboros](https://github.com/blinklabs-io/gouroboros).

Cardano's modular design encourages the use of mini-protocols to interact with the network.
Many examples of lightweight nodes which could support bifrost adjacent use cases exist, such as [kupo (Haskell)](https://github.com/CardanoSolutions/kupo), [dolos (Rust)](https://github.com/txpipe/dolos), and [adder (Go)](https://github.com/blinklabs-io/adder).   Adder is built on top of [gouroboros](https://github.com/blinkslabs-io/gouroboros) which is a Go implementation of the Ouroboros network protocol.

Gouroboros seems to handle peer sharing so ostensibly is a proper node in the Cardano network.
Similar to dolos it does not handle tx building or signing, so something like
[go-cardano-serialization](https://github.com/fivebinaries/go-cardano-serialization) or an
in-house solution would be required.

**Pros:**

- Directly interacts with the Cardano network, no sidecar or external service required.
- Heavy lifting of network protocol implementation is already done.
- More flexible than dolos, can be used to interact with other Cardano network services.

**Cons:**

- A bit more discovery needed, implementation may look quite different than other Bifrost chain
clients since the "client" is in fact a Cardano node on the network.