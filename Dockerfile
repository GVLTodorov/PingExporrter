# === Build Stage ===
FROM golang:1.25 AS builder

ENV CGO_ENABLED=0

ARG VERSION=dev

# Set the working directory
WORKDIR /app

# Copy Go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy the source code
COPY . .

# Run tests before building the release binary
RUN go test ./...

# Build the Go application for Linux
RUN go build -ldflags "-X main.version=${VERSION}" -o ping-exporter .

# === Run Stage ===
FROM alpine:latest

# setcap grants the compiled binary CAP_NET_RAW so it can send/receive raw
# ICMP echo as a non-root user, without the compose file needing
# privileged/cap_add/NET_RAW/user: overrides — see
# PING_EXPORTER_REQUIREMENTS.md section 6.
RUN apk --no-cache add libcap && \
    adduser -D -H -u 10001 exporter

WORKDIR /app

COPY --from=builder /app/ping-exporter .

RUN setcap cap_net_raw+ep ./ping-exporter

USER exporter

# Expose the metrics port
EXPOSE 9427

# Command to run the exporter
CMD ["./ping-exporter"]
