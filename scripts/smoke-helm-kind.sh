#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

usage() {
  cat <<'EOF'
Usage: ./scripts/smoke-helm-kind.sh [--keep-cluster]

Creates a temporary kind cluster and runs a Helm install/upgrade/rollback drill
against the local Buckley Helm chart.

Requires: docker, helm, go (kind is installed to a temp dir if missing)
EOF
}

keep_cluster=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep-cluster) keep_cluster=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown flag: $1" >&2; usage; exit 2 ;;
  esac
  shift
done

for cmd in docker helm go; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing required command: $cmd" >&2
    exit 1
  fi
done

tmp="$(mktemp -d)"
cluster_name="buckley-smoke-$(date +%s)"
kubeconfig="$tmp/kubeconfig"

cleanup() {
  if [[ "${keep_cluster}" == "1" ]]; then
    echo "note: keeping kind cluster ${cluster_name} (kubeconfig: ${kubeconfig})" >&2
    return
  fi
  if command -v "$kind_bin" >/dev/null 2>&1; then
    "$kind_bin" delete cluster --name "$cluster_name" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

kind_bin="$(command -v kind || true)"
if [[ -z "$kind_bin" ]]; then
  echo "==> installing kind (temp)"
  mkdir -p "$tmp/bin"
  GOBIN="$tmp/bin" go install sigs.k8s.io/kind@v0.26.0
  kind_bin="$tmp/bin/kind"
fi

echo "==> kind create cluster (${cluster_name})"
"$kind_bin" create cluster --name "$cluster_name" --kubeconfig "$kubeconfig"
export KUBECONFIG="$kubeconfig"

image_repo="buckley"
image_tag="smoke"
image_ref="${image_repo}:${image_tag}"

echo "==> docker build (${image_ref})"
docker build -t "$image_ref" .

echo "==> kind load image (${image_ref})"
"$kind_bin" load docker-image "$image_ref" --name "$cluster_name"

release="buckley-smoke"
namespace="buckley-smoke"

common_values=(
  --set "image.repository=${image_repo}"
  --set "image.tag=${image_tag}"
  --set "image.pullPolicy=IfNotPresent"
  --set "sharedConfig.accessModes[0]=ReadWriteOnce"
  --set "batch.job.image=${image_ref}"
)

echo "==> helm install"
helm upgrade --install "$release" deploy/helm/buckley \
  --namespace "$namespace" \
  --create-namespace \
  "${common_values[@]}" \
  --wait \
  --wait-for-jobs \
  --timeout 10m

echo "==> helm upgrade"
helm upgrade "$release" deploy/helm/buckley \
  --namespace "$namespace" \
  "${common_values[@]}" \
  --set "metrics.public=true" \
  --wait \
  --wait-for-jobs \
  --timeout 10m

echo "==> helm rollback"
helm rollback "$release" 1 \
  --namespace "$namespace" \
  --wait \
  --wait-for-jobs \
  --timeout 10m

echo "✅ smoke helm-kind passed"

