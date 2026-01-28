# Local Cluster Deployment

Run the full Buckley stack locally using k3d.

## Prerequisites

- Docker Desktop or Docker Engine
- k3d (`brew install k3d` or see [k3d.io](https://k3d.io))
- kubectl
- Helm 3.x

## Resource Requirements

### Minimum Viable (tight)

For quick testing. Services may restart under load.

```
CPU:     2 cores
Memory:  4Gi
Storage: 50Gi
```

### Recommended (comfortable)

For local development with room to breathe.

```
CPU:     4 cores
Memory:  8Gi
Storage: 100Gi
```

### Production-like (multi-node)

For testing HA and realistic deployment.

```
Nodes:   3
CPU:     2 cores/node (6 total)
Memory:  4Gi/node (12 total)
Storage: 50Gi/node
```

## Component Resource Allocation

| Service | CPU Request | CPU Limit | Memory Request | Memory Limit | Storage |
|---------|-------------|-----------|----------------|--------------|---------|
| Context Service | 100m | 500m | 256Mi | 512Mi | - |
| PostgreSQL | 250m | 1000m | 512Mi | 1Gi | 20Gi |
| Qdrant | 500m | 2000m | 1Gi | 4Gi | 20Gi |
| NATS JetStream | 100m | 250m | 256Mi | 512Mi | 5Gi |
| Session Dashboard | 100m | 250m | 128Mi | 256Mi | - |
| **Total** | **1050m** | **4000m** | **2.1Gi** | **6.3Gi** | **45Gi** |

## Quick Start

### 1. Create Cluster

**Minimum:**
```bash
k3d cluster create buckley \
  --agents 0 \
  --servers 1 \
  --port "8080:80@loadbalancer" \
  --port "8443:443@loadbalancer"
```

**Recommended:**
```bash
k3d cluster create buckley \
  --agents 2 \
  --servers 1 \
  --port "8080:80@loadbalancer" \
  --port "8443:443@loadbalancer" \
  --k3s-arg "--disable=traefik@server:0"
```

**Production-like:**
```bash
k3d cluster create buckley \
  --agents 2 \
  --servers 3 \
  --port "8080:80@loadbalancer" \
  --port "8443:443@loadbalancer" \
  --k3s-arg "--disable=traefik@server:*"
```

### 2. Verify Cluster

```bash
kubectl cluster-info
kubectl get nodes
```

### 3. Add Helm Repos

```bash
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo add qdrant https://qdrant.github.io/qdrant-helm
helm repo add nats https://nats-io.github.io/k8s/helm/charts
helm repo update
```

### 4. Create Namespace

```bash
kubectl create namespace buckley
kubectl config set-context --current --namespace=buckley
```

### 5. Deploy Infrastructure

**PostgreSQL:**
```bash
helm install postgres bitnami/postgresql \
  --namespace buckley \
  --set auth.database=context \
  --set auth.username=context \
  --set auth.password=context \
  --set primary.persistence.size=20Gi \
  --set primary.resources.requests.cpu=250m \
  --set primary.resources.requests.memory=512Mi
```

**Qdrant:**
```bash
helm install qdrant qdrant/qdrant \
  --namespace buckley \
  --set replicaCount=1 \
  --set persistence.size=20Gi \
  --set resources.requests.cpu=500m \
  --set resources.requests.memory=1Gi
```

**NATS:**
```bash
helm install nats nats/nats \
  --namespace buckley \
  --set nats.jetstream.enabled=true \
  --set nats.jetstream.memStorage.enabled=true \
  --set nats.jetstream.memStorage.size=256Mi \
  --set nats.jetstream.fileStorage.enabled=true \
  --set nats.jetstream.fileStorage.size=5Gi
```

### 6. Deploy Buckley Services

```bash
# Context Service
helm install context-service ./deploy/charts/context-service \
  --namespace buckley \
  --set postgresql.enabled=false \
  --set postgresql.external.host=postgres-postgresql \
  --set qdrant.enabled=false \
  --set qdrant.external.host=qdrant \
  --set nats.enabled=false \
  --set nats.external.url=nats://nats:4222

# Session Dashboard (when available)
helm install session-dashboard ./deploy/charts/session-dashboard \
  --namespace buckley
```

### 7. Verify Deployment

```bash
kubectl get pods -n buckley
kubectl get svc -n buckley
```

**Check service health:**
```bash
# Context Service
kubectl port-forward svc/context-service 8080:8080 -n buckley &
curl http://localhost:8080/health/ready

# Qdrant
kubectl port-forward svc/qdrant 6333:6333 -n buckley &
curl http://localhost:6333/healthz

# NATS
kubectl port-forward svc/nats 8222:8222 -n buckley &
curl http://localhost:8222/healthz
```

## Configuration

### Connect Buckley CLI to Local Cluster

```yaml
# ~/.buckley/config.yaml
context_graph:
  enabled: true
  service_endpoint: http://localhost:8080
  nats_url: nats://localhost:4222
```

**Port forward for CLI access:**
```bash
kubectl port-forward svc/context-service 8080:8080 -n buckley &
kubectl port-forward svc/nats 4222:4222 -n buckley &
```

### Resource Tuning

**For memory-constrained environments:**
```yaml
# values-minimal.yaml
postgresql:
  primary:
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        cpu: 500m
        memory: 512Mi

qdrant:
  resources:
    requests:
      cpu: 250m
      memory: 512Mi
    limits:
      cpu: 1000m
      memory: 2Gi
```

Apply with:
```bash
helm upgrade context-service ./deploy/charts/context-service \
  -f values-minimal.yaml
```

## Cleanup

```bash
# Delete all services
helm uninstall context-service -n buckley
helm uninstall postgres -n buckley
helm uninstall qdrant -n buckley
helm uninstall nats -n buckley

# Delete cluster
k3d cluster delete buckley
```

## Troubleshooting

### Pods stuck in Pending

Check resources:
```bash
kubectl describe pod <pod-name> -n buckley
kubectl top nodes
```

Likely cause: Insufficient CPU/memory. Use minimal values or create larger cluster.

### Qdrant OOMKilled

Qdrant is memory-hungry. Increase limits:
```bash
helm upgrade qdrant qdrant/qdrant \
  --set resources.limits.memory=4Gi
```

### NATS JetStream not starting

Check storage:
```bash
kubectl get pvc -n buckley
```

May need to provision storage class or increase PVC size.

### Context Service can't connect to PostgreSQL

Check service DNS:
```bash
kubectl run debug --rm -it --image=busybox -- nslookup postgres-postgresql.buckley.svc.cluster.local
```

Verify credentials match between Helm installs.
