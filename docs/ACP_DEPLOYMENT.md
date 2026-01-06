# ACP Deployment Guide

**Version**: 1.0.0
**Last Updated**: 2025-11-19
**Target Audience**: DevOps Engineers, System Administrators

## Table of Contents

1. [Overview](#overview)
2. [Prerequisites](#prerequisites)
3. [Security Configuration](#security-configuration)
4. [Deployment Architectures](#deployment-architectures)
5. [Step-by-Step Deployment](#step-by-step-deployment)
6. [Configuration Reference](#configuration-reference)
7. [Monitoring & Operations](#monitoring--operations)
8. [Troubleshooting](#troubleshooting)
9. [Production Checklist](#production-checklist)

---

## Overview

The Agent Communication Protocol (ACP) requires careful deployment to ensure security, reliability, and performance. This guide covers production deployment scenarios for Buckley's ACP implementation.

### Architecture Components

```
┌─────────────────┐
│  Agent Clients  │ (External agents, authenticated via JWT)
└────────┬────────┘
         │ TLS 1.3
         ▼
┌─────────────────┐
│  Load Balancer  │ (HAProxy, NGINX, or cloud LB)
└────────┬────────┘
         │
    ┌────┴────┬────────┬────────┐
    ▼         ▼        ▼        ▼
┌────────┐ ┌────────┐ ┌────────┐
│ ACP    │ │ ACP    │ │ ACP    │ (Stateless gRPC servers)
│ Server │ │ Server │ │ Server │
└───┬────┘ └───┬────┘ └───┬────┘
    │          │          │
    └──────────┴──────────┘
               │
         ┌─────┴─────┐
         ▼           ▼
    ┌────────┐  ┌────────┐
    │ SQLite │  │  NATS  │ (Event storage)
    │ Events │  │ Stream │
    └────────┘  └────────┘
```

---

## Prerequisites

### System Requirements

- **OS**: Linux (Ubuntu 22.04+, RHEL 8+, or equivalent)
- **CPU**: 2+ cores (4+ recommended for production)
- **RAM**: 4GB minimum (8GB+ recommended)
- **Disk**: 20GB+ SSD for event storage
- **Network**: Low-latency connection between ACP servers (< 10ms RTT)

### Software Dependencies

```bash
# Go 1.25.1 or later
go version

# TLS certificates (see Security Configuration)
openssl version

# Optional: NATS server for distributed event streaming
nats-server --version

# Optional: Docker for containerized deployment
docker --version
```

### Local KinD + NATS quickstart (full stack on localhost)

Use this to run ACP with JetStream locally (keeps SQLite for the CLI/TUI, but ACP event store goes to NATS inside KinD). Good for end-to-end testing with Mission Control and approvals.

1) Create KinD cluster with port mappings for IPC and NATS:
```bash
cat <<'EOF' > kind-buckley.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 4488 # Buckley IPC / web
    hostPort: 4488
  - containerPort: 4222 # NATS client
    hostPort: 4222
EOF
kind create cluster --config kind-buckley.yaml --name buckley
```

2) Install NATS JetStream:
```bash
helm repo add nats https://nats-io.github.io/k8s/helm/charts/
helm install nats nats/nats --set jetstream.enabled=true --namespace nats --create-namespace
```

3) Point Buckley to NATS (k8s defaults to NATS; override explicitly if needed):
```bash
kubectl create namespace buckley
kubectl create secret generic buckley-config -n buckley \
  --from-literal=BUCKLEY_ACP_EVENT_STORE=nats \
  --from-literal=BUCKLEY_ACP_NATS_URL=nats://nats.nats.svc.cluster.local:4222
```

4) Deploy Buckley (Helm or manifest) reading that secret. Reach the UI/IPCs at `http://127.0.0.1:4488`.

5) Tunnels: if exposing via Tailscale/ngrok, set `IPC.allowed_origins` to the tunnel domain and enable `IPC.require_token` (add basic auth if desired). Ensure your tunnel allows WebSocket upgrades.

### Approval bot (shelved design)

Planned lightweight auto-approver that listens to Mission Control pending changes and applies allow/deny rules:
- Embedded worker: gated by `approval_bot.enabled` in config; uses mission store directly.
- Sidecar bot: small Go service subscribing to NATS or `/api/mission/changes` and calling approve/reject with a bot reviewer.

Rules sketch: auto-approve small diffs in allowlisted paths; auto-reject denypaths; leave the rest pending. No code shipped yet—documented for future implementation.

### Fast start recipes
- **CLI only (local)**: `buckley serve --browser` (ships `/ws` + `/api/mission/events`). UI auto-loads if `--browser` is set; otherwise open `http://127.0.0.1:4488`.
- **Zed bridge**: Point Zed ACP client at the same host/port; provide API token if `IPC.require_token` is true. ACP RPCs execute through the orchestrator; tool approvals respect mission trust level.
- **Mission Control (web/desktop)**: Defaults to `ws://127.0.0.1:4488/ws` and `ws://127.0.0.1:4488/api/mission/events`. Override with `VITE_IPC_WS_URL` and `VITE_IPC_MISSION_WS_URL` when tunneling.
- **Tunnels (Tailscale/ngrok)**: Set `IPC.allowed_origins` to the external domain, enable `IPC.require_token`, and export `VITE_IPC_TOKEN` for the desktop app. Ensure the tunnel permits WebSocket upgrades to both endpoints.

### Security Considerations

- **TLS/mTLS**: Required for all production deployments
- **Secret Management**: Use HashiCorp Vault, AWS Secrets Manager, or similar
- **Network Segmentation**: Isolate ACP servers in private subnet
- **Firewall Rules**: Restrict access to authorized clients only

---

## Security Configuration

### 1. Generate TLS Certificates

#### Option A: Self-Signed (Development Only)

```bash
# Generate CA
openssl req -x509 -newkey rsa:4096 -days 365 -nodes \
  -keyout ca-key.pem -out ca-cert.pem \
  -subj "/CN=ACP-CA"

# Generate server certificate
openssl req -newkey rsa:4096 -nodes \
  -keyout server-key.pem -out server-req.pem \
  -subj "/CN=acp-server"

openssl x509 -req -in server-req.pem -days 365 \
  -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial \
  -out server-cert.pem

# Generate client certificate
openssl req -newkey rsa:4096 -nodes \
  -keyout client-key.pem -out client-req.pem \
  -subj "/CN=acp-client"

openssl x509 -req -in client-req.pem -days 365 \
  -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial \
  -out client-cert.pem
```

#### Option B: Let's Encrypt (Production)

```bash
# Install certbot
sudo apt-get install certbot

# Generate certificate
sudo certbot certonly --standalone -d acp.example.com

# Certificates located at:
# /etc/letsencrypt/live/acp.example.com/fullchain.pem
# /etc/letsencrypt/live/acp.example.com/privkey.pem
```

#### Option C: Cloud Provider (AWS, GCP, Azure)

See your cloud provider's certificate management service (AWS ACM, GCP Certificate Manager, Azure Key Vault).

### 2. Generate HMAC Secret Key

```bash
# Generate 64-byte random secret
openssl rand -base64 64 > hmac-secret.key

# Set restrictive permissions
chmod 600 hmac-secret.key
```

**⚠️ WARNING**: Never commit secret keys to version control!

### 3. Configure Secret Management

#### Using Environment Variables

```bash
export ACP_HMAC_SECRET=$(cat hmac-secret.key)
export ACP_TLS_CERT=/path/to/server-cert.pem
export ACP_TLS_KEY=/path/to/server-key.pem
export ACP_TLS_CA=/path/to/ca-cert.pem
```

#### Using HashiCorp Vault

```bash
# Store secret in Vault
vault kv put secret/acp/hmac-secret value="$(cat hmac-secret.key)"

# Retrieve in application
export ACP_HMAC_SECRET=$(vault kv get -field=value secret/acp/hmac-secret)
```

#### Using Kubernetes Secrets

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: acp-secrets
type: Opaque
data:
  hmac-secret: <base64-encoded-secret>
  tls-cert: <base64-encoded-cert>
  tls-key: <base64-encoded-key>
```

---

## Deployment Architectures

### 1. Single Server (Development/Testing)

```yaml
# Simplest deployment for development
Architecture: Single ACP server, SQLite event store
Use Case: Local development, CI/CD testing
Pros: Simple, no dependencies
Cons: No HA, limited scalability
```

**Deployment**:
```bash
./buckley-acp-server \
  --port 50051 \
  --hmac-secret "${ACP_HMAC_SECRET}" \
  --tls-cert "${ACP_TLS_CERT}" \
  --tls-key "${ACP_TLS_KEY}" \
  --event-store sqlite \
  --sqlite-path ./events.db
```

### 2. Multi-Server with NATS (Production)

```yaml
Architecture: 3+ ACP servers, NATS JetStream for events
Use Case: Production workloads, high availability
Pros: Scalable, fault-tolerant, distributed events
Cons: More complex, requires NATS cluster
```

**Deployment**:
```bash
# Start NATS cluster (3 nodes)
# Node 1
nats-server --cluster nats://0.0.0.0:6222 \
  --routes nats://node2:6222,nats://node3:6222

# Start ACP servers (point to NATS cluster)
./buckley-acp-server \
  --port 50051 \
  --hmac-secret "${ACP_HMAC_SECRET}" \
  --tls-cert "${ACP_TLS_CERT}" \
  --tls-key "${ACP_TLS_KEY}" \
  --event-store nats \
  --nats-url "nats://nats-cluster:4222"
```

### 3. Kubernetes Deployment (Cloud Native)

See [Kubernetes Deployment](#kubernetes-deployment) section below.

---

## Step-by-Step Deployment

### Step 1: Build the Binary

```bash
# Clone repository
git clone https://github.com/odvcencio/buckley.git
cd buckley

# Build ACP server
go build -o buckley-acp-server ./cmd/acp-server

# Verify build
./buckley-acp-server --version
```

### Step 2: Configure Environment

```bash
# Create configuration directory
mkdir -p /etc/buckley/acp

# Copy configuration file
cat > /etc/buckley/acp/config.yaml <<EOF
server:
  port: 50051
  tls:
    enabled: true
    cert_file: /etc/buckley/certs/server-cert.pem
    key_file: /etc/buckley/certs/server-key.pem
    ca_file: /etc/buckley/certs/ca-cert.pem
    require_client_cert: true  # Enable mTLS

auth:
  hmac_secret_file: /etc/buckley/secrets/hmac-secret.key
  token_ttl: 1h
  cleanup_interval: 1h

authorization:
  policy_file: /etc/buckley/acp/policy.yaml
  audit_log_enabled: true
  audit_log_path: /var/log/buckley/acp-audit.log

events:
  store: nats  # or "sqlite"
  nats_url: nats://nats-cluster:4222
  # sqlite_path: /var/lib/buckley/events.db

observability:
  metrics_port: 9090
  tracing_enabled: true
  log_level: info
  log_format: json

performance:
  max_concurrent_streams: 1000
  max_message_size_mb: 10
  circuit_breaker:
    max_failures: 5
    timeout: 30s
EOF
```

### Step 3: Create Systemd Service

```bash
# Create service file
sudo tee /etc/systemd/system/buckley-acp.service <<EOF
[Unit]
Description=Buckley ACP Server
After=network.target
Wants=nats.service

[Service]
Type=simple
User=buckley
Group=buckley
WorkingDirectory=/opt/buckley
ExecStart=/opt/buckley/buckley-acp-server --config /etc/buckley/acp/config.yaml
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

# Security hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/buckley /var/log/buckley

# Resource limits
LimitNOFILE=65536
LimitNPROC=4096

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd
sudo systemctl daemon-reload

# Enable and start service
sudo systemctl enable buckley-acp
sudo systemctl start buckley-acp

# Check status
sudo systemctl status buckley-acp
```

### Step 4: Configure Load Balancer

#### NGINX (gRPC Proxy)

```nginx
upstream acp_backend {
    # Enable HTTP/2 for gRPC
    server acp-server-1:50051;
    server acp-server-2:50051;
    server acp-server-3:50051;

    # Health checks
    keepalive 32;
}

server {
    listen 50051 ssl http2;
    server_name acp.example.com;

    # TLS configuration
    ssl_certificate /etc/nginx/ssl/acp-cert.pem;
    ssl_certificate_key /etc/nginx/ssl/acp-key.pem;
    ssl_protocols TLSv1.3;
    ssl_prefer_server_ciphers on;

    # gRPC settings
    grpc_pass grpc://acp_backend;

    # Timeouts
    grpc_read_timeout 300s;
    grpc_send_timeout 300s;

    # Logging
    access_log /var/log/nginx/acp-access.log;
    error_log /var/log/nginx/acp-error.log warn;
}
```

#### HAProxy (Alternative)

```haproxy
frontend acp_frontend
    bind *:50051 ssl crt /etc/haproxy/certs/acp.pem alpn h2
    mode tcp
    default_backend acp_backend

backend acp_backend
    mode tcp
    balance roundrobin
    option tcp-check

    server acp-1 acp-server-1:50051 check
    server acp-2 acp-server-2:50051 check
    server acp-3 acp-server-3:50051 check
```

### Step 5: Deploy NATS Cluster (Optional)

```bash
# Using Docker Compose
cat > docker-compose.yml <<EOF
version: '3.8'
services:
  nats-1:
    image: nats:latest
    command:
      - "--cluster"
      - "nats://0.0.0.0:6222"
      - "--routes"
      - "nats://nats-2:6222,nats://nats-3:6222"
      - "--jetstream"
    ports:
      - "4222:4222"
      - "6222:6222"
      - "8222:8222"

  nats-2:
    image: nats:latest
    command:
      - "--cluster"
      - "nats://0.0.0.0:6222"
      - "--routes"
      - "nats://nats-1:6222,nats://nats-3:6222"
      - "--jetstream"

  nats-3:
    image: nats:latest
    command:
      - "--cluster"
      - "nats://0.0.0.0:6222"
      - "--routes"
      - "nats://nats-1:6222,nats://nats-2:6222"
      - "--jetstream"
EOF

docker-compose up -d
```

---

## Kubernetes Deployment

### Deployment Manifest

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: buckley-acp
  namespace: buckley
spec:
  replicas: 3
  selector:
    matchLabels:
      app: buckley-acp
  template:
    metadata:
      labels:
        app: buckley-acp
    spec:
      containers:
      - name: acp-server
        image: buckley/acp-server:1.0.0
        ports:
        - containerPort: 50051
          name: grpc
        - containerPort: 9090
          name: metrics
        env:
        - name: ACP_HMAC_SECRET
          valueFrom:
            secretKeyRef:
              name: acp-secrets
              key: hmac-secret
        - name: ACP_TLS_CERT
          value: /etc/certs/server-cert.pem
        - name: ACP_TLS_KEY
          value: /etc/certs/server-key.pem
        volumeMounts:
        - name: certs
          mountPath: /etc/certs
          readOnly: true
        - name: config
          mountPath: /etc/buckley/acp
          readOnly: true
        resources:
          requests:
            cpu: 500m
            memory: 512Mi
          limits:
            cpu: 2000m
            memory: 2Gi
        livenessProbe:
          grpc:
            port: 50051
          initialDelaySeconds: 10
          periodSeconds: 10
        readinessProbe:
          grpc:
            port: 50051
          initialDelaySeconds: 5
          periodSeconds: 5
      volumes:
      - name: certs
        secret:
          secretName: acp-tls
      - name: config
        configMap:
          name: acp-config
---
apiVersion: v1
kind: Service
metadata:
  name: buckley-acp
  namespace: buckley
spec:
  type: LoadBalancer
  selector:
    app: buckley-acp
  ports:
  - port: 50051
    targetPort: 50051
    name: grpc
  - port: 9090
    targetPort: 9090
    name: metrics
---
apiVersion: v1
kind: Service
metadata:
  name: buckley-acp-headless
  namespace: buckley
spec:
  clusterIP: None
  selector:
    app: buckley-acp
  ports:
  - port: 50051
    name: grpc
```

### Apply Deployment

```bash
# Create namespace
kubectl create namespace buckley

# Create secrets
kubectl create secret generic acp-secrets \
  --from-file=hmac-secret=./hmac-secret.key \
  -n buckley

kubectl create secret tls acp-tls \
  --cert=./server-cert.pem \
  --key=./server-key.pem \
  -n buckley

# Create config map
kubectl create configmap acp-config \
  --from-file=config.yaml=./config.yaml \
  -n buckley

# Deploy
kubectl apply -f acp-deployment.yaml

# Verify
kubectl get pods -n buckley
kubectl logs -f -l app=buckley-acp -n buckley
```

---

## Configuration Reference

### Complete Configuration File

```yaml
# /etc/buckley/acp/config.yaml

server:
  port: 50051
  host: 0.0.0.0
  tls:
    enabled: true
    cert_file: /etc/buckley/certs/server-cert.pem
    key_file: /etc/buckley/certs/server-key.pem
    ca_file: /etc/buckley/certs/ca-cert.pem
    require_client_cert: true  # mTLS
    min_version: "1.3"  # TLS 1.3 only

auth:
  hmac_secret_file: /etc/buckley/secrets/hmac-secret.key
  token_ttl: 1h
  refresh_window: 15m  # Allow refresh 15min before expiry
  cleanup_interval: 1h
  max_concurrent_sessions: 10000

authorization:
  policy_file: /etc/buckley/acp/policy.yaml
  policy_reload_interval: 5m
  audit_log_enabled: true
  audit_log_path: /var/log/buckley/acp-audit.log
  audit_log_max_size_mb: 100
  audit_log_max_age_days: 90
  audit_log_retention_count: 10

events:
  store: nats  # "sqlite" or "nats"

  # SQLite configuration
  sqlite_path: /var/lib/buckley/events.db
  sqlite_wal_mode: true
  sqlite_busy_timeout: 5000

  # NATS configuration
  nats_url: nats://nats-cluster:4222
  nats_stream_name: buckley_events
  nats_max_age: 720h  # 30 days
  nats_max_bytes: 10737418240  # 10GB

  # Common configuration
  snapshot_interval: 1000  # events
  max_event_size_kb: 1024

observability:
  # Metrics
  metrics_enabled: true
  metrics_port: 9090
  metrics_path: /metrics

  # Tracing
  tracing_enabled: true
  tracing_exporter: otlp  # "stdout", "otlp", "jaeger"
  tracing_endpoint: http://jaeger:4318
  tracing_sample_rate: 0.1  # 10% sampling

  # Logging
  log_level: info  # "debug", "info", "warn", "error"
  log_format: json  # "json" or "text"
  log_output: stdout  # "stdout", "stderr", or file path

  # Event Streaming
  event_stream_enabled: true
  event_stream_port: 8080
  event_stream_path: /events
  event_stream_buffer_size: 1000

performance:
  # gRPC settings
  max_concurrent_streams: 1000
  max_message_size_mb: 10
  keepalive_time: 120s
  keepalive_timeout: 20s

  # Circuit breaker
  circuit_breaker:
    enabled: true
    max_failures: 5
    timeout: 30s
    max_requests: 3  # half-open state

  # Rate limiting
  rate_limit:
    enabled: true
    requests_per_second: 1000
    burst: 2000

security:
  # IP whitelisting
  allowed_ips:
    - 10.0.0.0/8
    - 172.16.0.0/12
    - 192.168.0.0/16

  # Time-based restrictions
  allowed_hours: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
  allowed_days: [0, 1, 2, 3, 4, 5, 6]  # 0=Sunday

  # Deny lists
  denied_agents: []
  denied_tools: []
```

---

## Monitoring & Operations

### Health Checks

```bash
# gRPC health check
grpcurl -plaintext localhost:50051 grpc.health.v1.Health/Check

# HTTP health check (metrics endpoint)
curl http://localhost:9090/metrics

# Systemd status
systemctl status buckley-acp
```

### Metrics Dashboard

```promql
# Authentication metrics
rate(acp_auth_token_generation_total[5m])
rate(acp_auth_token_validation_total{status="invalid"}[5m])

# Authorization metrics
rate(acp_authz_denied_access_total[5m])

# P2P metrics
histogram_quantile(0.99, rate(acp_p2p_message_duration_seconds_bucket[5m]))

# Event metrics
rate(acp_events_appended_total[5m])
```

### Log Aggregation

```bash
# Journald
journalctl -u buckley-acp -f --output json | jq

# File logs
tail -f /var/log/buckley/acp-audit.log | jq

# Kubernetes
kubectl logs -f -l app=buckley-acp -n buckley
```

### Backup & Restore

#### SQLite Events

```bash
# Backup
sqlite3 /var/lib/buckley/events.db ".backup /backup/events-$(date +%Y%m%d).db"

# Restore
sqlite3 /var/lib/buckley/events.db ".restore /backup/events-20251119.db"
```

#### NATS JetStream

```bash
# Backup
nats stream backup buckley_events /backup/nats-backup

# Restore
nats stream restore buckley_events /backup/nats-backup
```

---

## Troubleshooting

### Common Issues

#### 1. Authentication Failures

**Symptoms**: `codes.Unauthenticated` errors

**Diagnosis**:
```bash
# Check token validity
curl -H "Authorization: Bearer ${TOKEN}" http://localhost:9090/debug/token

# Check logs
journalctl -u buckley-acp | grep "authentication failed"
```

**Solutions**:
- Verify HMAC secret matches on all servers
- Check token expiration (`jwt.io` for decoding)
- Verify TLS certificate validity

#### 2. High Latency

**Symptoms**: Slow request responses

**Diagnosis**:
```bash
# Check metrics
curl http://localhost:9090/metrics | grep duration

# Enable debug logging
export ACP_LOG_LEVEL=debug
systemctl restart buckley-acp
```

**Solutions**:
- Scale horizontally (add more servers)
- Optimize event store (use NATS instead of SQLite)
- Increase circuit breaker timeout

#### 3. Event Store Issues

**Symptoms**: Events not persisted or retrieved

**Diagnosis**:
```bash
# SQLite
sqlite3 /var/lib/buckley/events.db "SELECT COUNT(*) FROM events;"

# NATS
nats stream info buckley_events
```

**Solutions**:
- Check disk space
- Verify NATS connectivity
- Review event size limits

### Debug Mode

```bash
# Enable debug logging
export ACP_LOG_LEVEL=debug
export ACP_TRACING_SAMPLE_RATE=1.0  # 100% tracing

# Run server in foreground
./buckley-acp-server --config /etc/buckley/acp/config.yaml
```

---

## Production Checklist

### Pre-Deployment

- [ ] Generate strong HMAC secret (64+ bytes)
- [ ] Configure TLS/mTLS with valid certificates
- [ ] Set up secret management (Vault, AWS Secrets Manager, etc.)
- [ ] Configure firewall rules and network segmentation
- [ ] Plan event storage strategy (SQLite vs NATS)
- [ ] Set up monitoring and alerting
- [ ] Review and customize tool policy
- [ ] Plan backup and disaster recovery

### Deployment

- [ ] Deploy NATS cluster (if using NATS)
- [ ] Deploy ACP servers (3+ for HA)
- [ ] Configure load balancer
- [ ] Set up health checks
- [ ] Enable metrics collection
- [ ] Configure log aggregation
- [ ] Test authentication and authorization
- [ ] Perform load testing

### Post-Deployment

- [ ] Monitor metrics for anomalies
- [ ] Review audit logs regularly
- [ ] Set up automated backups
- [ ] Document runbooks for common operations
- [ ] Schedule periodic security audits
- [ ] Plan for key rotation
- [ ] Establish on-call rotation

### Security Hardening

- [ ] Enable rate limiting
- [ ] Configure IP whitelisting
- [ ] Enable time-based restrictions (if applicable)
- [ ] Set up intrusion detection
- [ ] Implement deny lists for compromised agents
- [ ] Enable event signing (future enhancement)
- [ ] Encrypt event data at rest (future enhancement)
- [ ] Implement policy versioning

---

## Support

For issues and questions:
- GitHub Issues: https://github.com/odvcencio/buckley/issues
- Documentation: https://github.com/odvcencio/buckley/tree/main/docs
- Security Issues: security@buckley.dev (responsible disclosure)

---

**See Also**:
- [ACP API Documentation](./ACP_API.md)
- [ACP Security Audit](./ACP_SECURITY_AUDIT.md)
- [ACP Tool Policy](./ACP_TOOL_POLICY.md)
