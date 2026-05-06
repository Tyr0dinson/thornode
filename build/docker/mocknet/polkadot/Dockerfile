FROM parity/polkadot:stable2603

USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
  curl=7.81.0-1ubuntu1.24 \
  jq=1.6-2.1ubuntu3.2 \
  python3=3.10.6-1~22.04.1 \
  && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh add-master.py /usr/local/bin/
RUN chmod +x /usr/local/bin/*

EXPOSE 9944 9945

USER parity

ENTRYPOINT ["entrypoint.sh"]