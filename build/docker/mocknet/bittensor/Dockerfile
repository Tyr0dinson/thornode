FROM ghcr.io/opentensor/subtensor-localnet:devnet-ready

RUN apt-get update && apt-get install -y --no-install-recommends \
  jq curl && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 9944 9945

ENTRYPOINT ["/entrypoint.sh"]
