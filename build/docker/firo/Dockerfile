# Keep the official amd64 daemon image; add Python for mocknet and health checks.
ARG DOCKER_MIRROR=
FROM ${DOCKER_MIRROR}firoorg/firod:0.14.18.2@sha256:a14a82a076252d72366b54c919b364ced5610e71ac8d23178eceb40bd824457e

USER root
# Keep distro packages current, as in the other chain images.
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /data /quorum && chown firod:firod /data /quorum
COPY healthcheck.py /usr/local/bin/firo-healthcheck
USER firod
VOLUME /data
EXPOSE 8168 8888
HEALTHCHECK --interval=30s --timeout=15s --start-period=5m --retries=3 \
    CMD ["python3", "/usr/local/bin/firo-healthcheck"]
ENTRYPOINT ["/bin/firod", "-datadir=/data", "-server", "-printtoconsole=1", "-txindex=1", "-prune=0"]
