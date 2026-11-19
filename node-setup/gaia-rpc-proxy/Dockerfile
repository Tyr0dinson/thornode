FROM scratch
COPY build/gaia-rpc-proxy /gaia-rpc-proxy
COPY build/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 10001:10001
ENV GOMEMLIMIT=256MiB
ENTRYPOINT ["/gaia-rpc-proxy"]
