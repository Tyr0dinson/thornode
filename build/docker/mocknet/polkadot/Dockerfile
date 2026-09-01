ARG NODE_VERSION=stable1603-4

# BuildKit does not expand variables in COPY --from, so the tagged image is
# pulled as a named stage here and copied from by that name below.
FROM parity/chain-spec-builder:${NODE_VERSION} AS chain-spec-builder

FROM parity/polkadot-omni-node:${NODE_VERSION}

USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
  curl=7.81.* \
  jq=1.6-* \
  python3=3.10.* \
  && rm -rf /var/lib/apt/lists/*

# chain-spec-builder generates the chain spec from the real Asset Hub runtime
COPY --from=chain-spec-builder /usr/local/bin/chain-spec-builder /usr/local/bin/chain-spec-builder

# Production Asset Hub Polkadot runtime (spec_name "statemint"), pinned to the
# fellowship release matching mainnet specVersion 2003001, so mocknet exercises
# the same pallet indexes, signed extensions (ChargeAssetTxPayment), and
# existential deposit as production.
ADD https://github.com/polkadot-fellows/runtimes/releases/download/v2.3.1/asset-hub-polkadot_runtime-v2003001.compact.compressed.wasm /usr/local/lib/asset-hub-polkadot-runtime.wasm

# Generate the plain chain spec at build time (deterministic; genesis balances
# are patched per-container in the entrypoint).
RUN chmod 644 /usr/local/lib/asset-hub-polkadot-runtime.wasm && \
  chain-spec-builder -c /usr/local/lib/plain-spec.json create \
  -n asset-hub-mocknet -i asset-hub-mocknet -t development \
  -p 1000 -c polkadot \
  -r /usr/local/lib/asset-hub-polkadot-runtime.wasm \
  named-preset development && \
  chmod 644 /usr/local/lib/plain-spec.json

COPY entrypoint.sh add-master.py /usr/local/bin/
RUN chmod +x /usr/local/bin/*

EXPOSE 9944 9945

USER parity

ENTRYPOINT ["entrypoint.sh"]
