FROM node:22-bookworm-slim AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ ./internal/
COPY cmd/ ./cmd/
RUN CGO_ENABLED=0 go build -trimpath -o /out/ixp-manager ./cmd/ixp-manager && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-agent ./cmd/ixp-agent && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-netconf ./cmd/ixp-netconf && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-routeros ./cmd/ixp-routeros && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-admin ./cmd/ixp-admin && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-backup ./cmd/ixp-backup && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-collector ./cmd/ixp-collector && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-policy ./cmd/ixp-policy && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-snmp ./cmd/ixp-snmp && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-sflow ./cmd/ixp-sflow && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-traffic-import ./cmd/ixp-traffic-import && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-sync ./cmd/ixp-sync && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-provision ./cmd/ixp-provision && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-openbgpd ./cmd/ixp-openbgpd && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-as112 ./cmd/ixp-as112 && \
    CGO_ENABLED=0 go build -trimpath -o /out/ixp-migrate ./cmd/ixp-migrate

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates bird2 frr frr-rpki-rtrlib openbgpd bind9-utils pdns-server pdns-backend-bind openssh-client && \
    rm -rf /var/lib/apt/lists/* && useradd -r -u 10001 -m moedove && \
    install -d -o moedove -g moedove -m 0700 /var/lib/moedove /var/tmp/frr /var/lib/frr /run/frr
COPY --from=go /out/ /usr/local/bin/
COPY --from=web /src/web/dist/ /app/web/
USER 10001:10001
WORKDIR /app
EXPOSE 8080
VOLUME ["/var/lib/moedove"]
ENTRYPOINT ["ixp-manager"]
CMD ["-listen", "0.0.0.0:8080", "-data", "/var/lib/moedove", "-web", "/app/web", "-bird", "/usr/sbin/bird", "-frr", "/usr/lib/frr/bgpd", "-openbgpd", "/usr/sbin/bgpd", "-named-checkzone", "/usr/bin/named-checkzone", "-pdns-server", "/usr/sbin/pdns_server"]
