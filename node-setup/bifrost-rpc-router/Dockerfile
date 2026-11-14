FROM scratch
COPY build/bifrost-rpc-router /bifrost-rpc-router
COPY build/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 10001:10001
ENV GOMEMLIMIT=256MiB
ENTRYPOINT ["/bifrost-rpc-router"]
