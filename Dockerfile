# Build stage
FROM golang:1.25.5 AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /weave ./cmd/weave

# Runtime stage
FROM python:3.12-slim

WORKDIR /app

# Install Python dependencies if needed
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Copy binary from builder
COPY --from=builder /weave /usr/local/bin/weave

# Copy Python SDK
COPY --from=builder /app/sdk/python /usr/local/lib/weave-sdk
ENV PYTHONPATH="${PYTHONPATH}:/usr/local/lib/weave-sdk"

# Create examples directory
COPY examples /app/examples

# Set up a simple entrypoint
ENTRYPOINT ["/usr/local/bin/weave"]
CMD ["--help"]