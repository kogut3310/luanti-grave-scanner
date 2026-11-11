# syntax=docker/dockerfile:1
FROM golang:1.27.4-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
# Optional CA mount is useful in managed environments with an HTTPS proxy.
RUN --mount=type=secret,id=proxy_ca \
    if [ -f /run/secrets/proxy_ca ]; then export SSL_CERT_FILE=/run/secrets/proxy_ca; fi; \
    go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -o /out/luanti-grave-scanner .

FROM alpine:3.23
ARG APP_VERSION=unspecified
LABEL org.opencontainers.image.title="Luanti Grave Scanner" \
      org.opencontainers.image.version="${APP_VERSION}" \
      org.opencontainers.image.source="https://github.com/kogut3310/luanti-grave-scanner"
WORKDIR /app
COPY --from=builder /out/luanti-grave-scanner /usr/local/bin/luanti-grave-scanner
RUN adduser -D appuser && mkdir -p /data && chown appuser:appuser /data
USER appuser
ENV DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/luanti-grave-scanner"]
