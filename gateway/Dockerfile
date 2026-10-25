# syntax=docker/dockerfile:1
FROM golang:1.26-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Optional release version (empty keeps the default from main.go).
ARG VERSION=
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w ${VERSION:+-X main.version=$VERSION}" -o /out/peb-gateway ./cmd/peb-gateway

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends restic rclone ca-certificates tini iproute2 \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -d /home/container -s /bin/sh container
COPY --from=build /out/peb-gateway /usr/local/bin/peb-gateway
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

USER container
ENV USER=container HOME=/home/container
WORKDIR /home/container
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/entrypoint.sh"]
