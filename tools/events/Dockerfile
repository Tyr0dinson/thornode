########################################################################################
# Build
########################################################################################

FROM golang:1.22.2 AS build

ENV GOBIN=/go/bin
ENV GOPATH=/go
ENV CGO_ENABLED=0
ENV GOOS=linux

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TAG=mainnet
RUN make protob && go install -tags $TAG ./tools/events

########################################################################################
# Deploy
########################################################################################

FROM alpine:3

COPY --from=build /go/bin/events /usr/bin/

CMD ["events"]
