FROM parity/polkadot:stable2603

USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
  jq curl python3 && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh add-master.py /usr/local/bin/
RUN chmod +x /usr/local/bin/*

EXPOSE 9944 9945

USER parity

ENTRYPOINT ["entrypoint.sh"]
