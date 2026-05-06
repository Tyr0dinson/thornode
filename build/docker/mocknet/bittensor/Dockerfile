FROM ghcr.io/opentensor/subtensor-localnet:v3.3.13-393

RUN apt-get update && apt-get install -y --no-install-recommends \
  curl=8.5.* \
  jq=1.7.* \
  && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 9944 9945

ENTRYPOINT ["/entrypoint.sh"]