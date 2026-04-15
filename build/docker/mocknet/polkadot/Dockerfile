FROM parity/polkadot:stable2603

USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
  curl=7.81.0-1 \
  jq=1.6-2.1ubuntu3 \
  python3=3.10.4-0ubuntu2 \
  && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh add-master.py /usr/local/bin/
RUN chmod +x /usr/local/bin/*

EXPOSE 9944 9945

USER parity

ENTRYPOINT ["entrypoint.sh"]