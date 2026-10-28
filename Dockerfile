FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod .
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/luanti-grave-scanner .

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /out/luanti-grave-scanner /usr/local/bin/luanti-grave-scanner
COPY web ./web
RUN adduser -D appuser && mkdir -p /data && chown -R appuser:appuser /data
USER appuser
ENV DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/luanti-grave-scanner"]
