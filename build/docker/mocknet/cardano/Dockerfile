FROM ubuntu:latest

ARG CARDANO_VERSION=10.6.1
ARG CARDANO_OS=linux

RUN apt-get update && apt-get install --yes --no-install-recommends \
    jq \
    ca-certificates \
    wget \
    socat \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /root

RUN ARCHIVE=cardano-node-${CARDANO_VERSION}-${CARDANO_OS}.tar.gz && \
    wget --progress=dot:giga https://github.com/IntersectMBO/cardano-node/releases/download/${CARDANO_VERSION}/${ARCHIVE} && \
    tar -xvzf ${ARCHIVE} && \
    rm -rf ${ARCHIVE}

COPY entrypoint.sh /usr/local/bin/

RUN chmod u+x /usr/local/bin/entrypoint.sh

CMD [ "entrypoint.sh" ]

EXPOSE 3001
EXPOSE 3002