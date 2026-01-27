.PHONY: proto
proto:
	@echo "Generating protobuf files..."
	cd pkg/acp/proto && go generate
	cd pkg/ipc/proto && go generate
	cd web && ./node_modules/.bin/buf generate

.PHONY: proto-install
proto-install:
	@echo "Installing protobuf tools..."
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

# Web UI targets
.PHONY: web
web:
	@echo "Building web UI..."
	cd web && bun install && bun run build

.PHONY: web-dev
web-dev:
	@echo "Starting web dev server..."
	cd web && bun run dev

.PHONY: web-install
web-install:
	@echo "Installing web dependencies..."
	cd web && bun install

# Combined build
.PHONY: build-cli
build-cli:
	@echo "Building buckley (CLI only)..."
	CGO_ENABLED=0 go build -o buckley ./cmd/buckley

.PHONY: build
build: web build-cli

.PHONY: dev
dev:
	@echo "Building and running buckley..."
	CGO_ENABLED=0 go build -o buckley ./cmd/buckley && ./buckley

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

.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -f buckley
	rm -rf pkg/ipc/ui/assets pkg/ipc/ui/index.html

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
