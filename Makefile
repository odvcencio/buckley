.PHONY: proto
proto:
	@echo "Generating protobuf files..."
	cd pkg/acp/proto && go generate
	cd pkg/ipc/proto && go generate
	@echo "No browser protobuf generation is required; Mission Control is GoSX server-rendered."

.PHONY: proto-install
proto-install:
	@echo "Installing protobuf tools..."
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

# GoSX Mission Control targets
.PHONY: web
web:
	@echo "Checking GoSX Mission Control UI..."
	go test ./pkg/ipc/gosxui

.PHONY: web-dev
web-dev:
	@echo "GoSX Mission Control is served by the Buckley daemon."
	@echo "Run: go run ./cmd/buckley serve --browser"

.PHONY: web-install
web-install:
	@echo "No browser package installation is required; GoSX is a Go dependency."

# Combined build
.PHONY: build-cli
build-cli:
	@echo "Building buckley (CLI only)..."
	CGO_ENABLED=0 go build -ldflags="-s -w" -o buckley ./cmd/buckley

.PHONY: build
build: build-cli

# Kubernetes batch execution (pkg/orchestrator/batch_coordinator.go) is gated
# behind the batch_k8s build tag so the default binary does not carry the
# k8s.io/client-go dependency tree. Use this target for batch-capable builds.
.PHONY: build-batch
build-batch:
	@echo "Building buckley (with Kubernetes batch support)..."
	CGO_ENABLED=0 go build -tags batch_k8s -ldflags="-s -w" -o buckley ./cmd/buckley

.PHONY: build-mission-control
build-mission-control:
	@echo "Building buckley (with GoSX Mission Control)..."
	CGO_ENABLED=0 go build -ldflags="-s -w" -o buckley ./cmd/buckley

.PHONY: dev
dev:
	@echo "Building and running buckley..."
	CGO_ENABLED=0 go build -ldflags="-s -w" -o buckley ./cmd/buckley && ./buckley

.PHONY: test
test:
	@echo "Running tests..."
	./scripts/test.sh

.PHONY: preflight
preflight:
	@echo "Running preflight checks..."
	./scripts/preflight.sh

.PHONY: smoke-serve
smoke-serve:
	@echo "Running serve smoke checks..."
	./scripts/smoke-serve.sh

.PHONY: smoke-helm-kind
smoke-helm-kind:
	@echo "Running Helm install/upgrade/rollback drill (kind)..."
	./scripts/smoke-helm-kind.sh

.PHONY: smoke-plan-execute
smoke-plan-execute:
	@echo "Running CLI plan/execute smoke (requires provider API key)..."
	./scripts/smoke-plan-execute.sh

# Browserd (Servo browser daemon) targets
.PHONY: build-browserd build-browserd-stub test-browserd install-browserd
build-browserd:
	cd apps/browserd && cargo build --release --features servo

build-browserd-stub:
	cd apps/browserd && cargo build --release

test-browserd:
	cd apps/browserd && cargo test --features servo

install-browserd: build-browserd
	cp apps/browserd/target/release/browserd $(HOME)/.local/bin/browserd

.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -f buckley

# Agent E2E Test targets
.PHONY: agent-test-build
agent-test-build:
	@echo "Building agent test driver..."
	mkdir -p scripts/agent-tests/.bin
	go build -o scripts/agent-tests/.bin/agent-test-driver scripts/agent-tests/*.go

.PHONY: agent-test-list
agent-test-list: agent-test-build
	@echo "Available agent test scenarios..."
	@./scripts/agent-tests/runner.sh --list

.PHONY: agent-test-smoke
agent-test-smoke: agent-test-build
	@echo "Running agent smoke test..."
	@./scripts/agent-tests/runner.sh --scenario scripts/agent-tests/scenarios/smoke.json --verbose

.PHONY: agent-test-all
agent-test-all: agent-test-build
	@echo "Running all agent E2E tests..."
	@./scripts/agent-tests/runner.sh --scenario scripts/agent-tests/scenarios/ --verbose

.PHONY: agent-test-demo
agent-test-demo: agent-test-build
	@echo "Running agent test demo mode..."
	@./scripts/agent-tests/runner.sh --verbose
