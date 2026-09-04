ARG NODE_VERSION=stable2606-1

# Name stages because COPY --from does not expand variables.
FROM parity/chain-spec-builder:${NODE_VERSION} AS chain-spec-builder
FROM parity/polkadot:${NODE_VERSION} AS relay-chain

FROM parity/polkadot-omni-node:${NODE_VERSION}

USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
  curl=7.81.* \
  jq=1.6-* \
  python3=3.10.* \
  && rm -rf /var/lib/apt/lists/*

# Generate specs from the Asset Hub runtime.
COPY --from=chain-spec-builder /usr/local/bin/chain-spec-builder /usr/local/bin/chain-spec-builder

# Include the relay validator and its PVF workers.
COPY --from=relay-chain /usr/bin/polkadot /usr/local/bin/polkadot
COPY --from=relay-chain /usr/lib/polkadot/ /usr/lib/polkadot/

# Use the Asset Hub runtime matching mainnet specVersion 2003001.
ADD https://github.com/polkadot-fellows/runtimes/releases/download/v2.3.1/asset-hub-polkadot_runtime-v2003001.compact.compressed.wasm /usr/local/lib/asset-hub-polkadot-runtime.wasm

# Generate the plain Asset Hub spec for the local relay chain.
RUN chmod 644 /usr/local/lib/asset-hub-polkadot-runtime.wasm && \
  chain-spec-builder -c /usr/local/lib/plain-spec.json create \
  -n asset-hub-mocknet -i asset-hub-mocknet -t development \
  -p 1000 -c rococo_local_testnet \
  -r /usr/local/lib/asset-hub-polkadot-runtime.wasm \
  named-preset development && \
  chmod 644 /usr/local/lib/plain-spec.json

COPY entrypoint.sh add-master.py register-para.py /usr/local/bin/
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/add-master.py /usr/local/bin/register-para.py

# Allow the parity user to write generated specs.
RUN mkdir -p /shared && chown parity /shared

EXPOSE 9944 30333

USER parity

ENTRYPOINT ["entrypoint.sh"]
