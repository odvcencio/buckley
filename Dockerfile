# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build with version info
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 go build -ldflags="-s -w \
    -X main.version=${VERSION} \
    -X main.commit=${COMMIT} \
    -X main.buildDate=${BUILD_DATE}" \
    -o /buckley ./cmd/buckley

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache \
    ca-certificates \
    git \
    openssh-client \
    bash \
    curl \
    jq

# Create non-root user
RUN addgroup -g 1000 buckley && \
    adduser -u 1000 -G buckley -h /home/buckley -D buckley

# Copy binary
COPY --from=builder /buckley /usr/local/bin/buckley

# Copy web assets
COPY --from=builder /src/pkg/ipc/ui /app/assets

# Setup directories
RUN mkdir -p /home/buckley/.buckley /buckley/projects /buckley/shared && \
    chown -R buckley:buckley /home/buckley /buckley

USER buckley
WORKDIR /home/buckley

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:4488/healthz || exit 1

EXPOSE 4488

ENTRYPOINT ["buckley"]
CMD ["serve", "--bind", "0.0.0.0:4488", "--browser", "--assets", "/app/assets", "--require-token", "--generate-token", "--token-file", "~/.buckley/ipc-token"]
