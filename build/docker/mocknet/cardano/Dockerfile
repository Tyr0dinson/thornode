FROM ubuntu:24.04

ARG CARDANO_VERSION=10.6.1
ARG CARDANO_OS=linux

RUN apt-get update && apt-get install --yes --no-install-recommends \
    jq=1.7.1-3ubuntu0.24.04.1 \
    ca-certificates=20240203 \
    wget=1.21.4-1ubuntu4.1 \
    socat=1.8.0.0-4build3 \
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