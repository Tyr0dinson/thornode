FROM scratch
COPY build/tron-rpc-proxy /tron-rpc-proxy
COPY build/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 10001:10001
ENV GOMEMLIMIT=384MiB
ENTRYPOINT ["/tron-rpc-proxy"]
