# Testing Guide for Buckley

This document provides comprehensive guidance for testing Buckley, including mock generation, test writing best practices, and coverage targets.

## Table of Contents

- [Quick Start](#quick-start)
- [Mock Generation](#mock-generation)
- [Testing Best Practices](#testing-best-practices)
- [Coverage Targets](#coverage-targets)
- [Running Tests](#running-tests)
- [Common Testing Patterns](#common-testing-patterns)

## Quick Start

```bash
# Run all tests
./scripts/test.sh

# Run CI-like preflight checks (formatting, tidy, tests, security scans)
./scripts/preflight.sh

# Run tests with race detector
GO_TEST_RACE=1 ./scripts/test.sh

# Run specific package tests
go test ./pkg/model -v

# Run tests without cache
GO_TEST_DISABLE_CACHE=1 ./scripts/test.sh

# Run with custom timeout
GO_TEST_TIMEOUT=5m ./scripts/test.sh
```

## Mock Generation

Buckley uses [Uber's gomock](https://github.com/uber-go/mock) for generating test mocks. All mocks are generated from interfaces using `mockgen`.

### Current Mocks

| Interface | Package | Mock File | Command |
|-----------|---------|-----------|---------|
| `TodoStore` | `pkg/tool/builtin` | `mock_todo_store_test.go` | `mockgen -package=builtin -destination=pkg/tool/builtin/mock_todo_store_test.go github.com/odvcencio/buckley/pkg/tool/builtin TodoStore` |
| `PlanStore` | `pkg/orchestrator` | `mock_plan_store_test.go` | `mockgen -package=orchestrator -destination=pkg/orchestrator/mock_plan_store_test.go github.com/odvcencio/buckley/pkg/orchestrator PlanStore` |
| `TokenCounter` | `pkg/artifact` | `mock_token_counter_test.go` | `mockgen -package=artifact -destination=pkg/artifact/mock_token_counter_test.go github.com/odvcencio/buckley/pkg/artifact TokenCounter` |
| `ModelClient` | `pkg/artifact` | `mock_model_client_test.go` | `mockgen -package=artifact -destination=pkg/artifact/mock_model_client_test.go github.com/odvcencio/buckley/pkg/artifact ModelClient` |
| `ModelClient` | `pkg/orchestrator/mocks` | `mock_model_client.go` | `mockgen -package=mocks -destination=pkg/orchestrator/mocks/mock_model_client.go github.com/odvcencio/buckley/pkg/orchestrator ModelClient` |
| `Handler` | `pkg/ipc/command` | `mock_handler_test.go` | `mockgen -package=command -destination=pkg/ipc/command/mock_handler_test.go github.com/odvcencio/buckley/pkg/ipc/command Handler` |
| `Observer` | `pkg/storage` | `mock_observer_test.go` | `mockgen -package=storage -destination=pkg/storage/mock_observer_test.go github.com/odvcencio/buckley/pkg/storage Observer` |
| `PersonaProvider` | `pkg/prompts` | `mock_persona_provider_test.go` | `mockgen -package=prompts -destination=pkg/prompts/mock_persona_provider_test.go github.com/odvcencio/buckley/pkg/prompts PersonaProvider` |
| `Provider` | `pkg/model` | `mock_provider_test.go` | `mockgen -package=model -destination=pkg/model/mock_provider_test.go github.com/odvcencio/buckley/pkg/model Provider` |
| `Tool` | `pkg/tool` | `mock_tool_test.go` | `mockgen -package=tool -destination=pkg/tool/mock_tool_test.go github.com/odvcencio/buckley/pkg/tool Tool` |
| `commandRunner` | `pkg/github` | `mock_runner_test.go` | `mockgen -package=github -destination=pkg/github/mock_runner_test.go github.com/odvcencio/buckley/pkg/github commandRunner` |
| `gitCommandRunner` | `pkg/session` | `mock_git_runner_test.go` | `mockgen -package=session -destination=pkg/session/mock_git_runner_test.go github.com/odvcencio/buckley/pkg/session gitCommandRunner` |

### Generating a New Mock

To generate a mock for a new interface:

```bash
mockgen -package=<package_name> \
  -destination=pkg/<package>/<path>/mock_<interface>_test.go \
  github.com/odvcencio/buckley/pkg/<package>/<path> <InterfaceName>
```

**Example:**
```bash
mockgen -package=storage \
  -destination=pkg/storage/mock_database_test.go \
  github.com/odvcencio/buckley/pkg/storage Database
```

### Mock Naming Conventions

- Mock files: `mock_<interface>_test.go` (for package-local mocks)
- Mock files: `mock_<interface>.go` (for shared mocks in `mocks/` subdirectory)
- Mock types: `Mock<InterfaceName>`
- Always use `_test.go` suffix to exclude from builds

## Testing Best Practices

### 1. Use Table-Driven Tests

```go
func TestCalculate(t *testing.T) {
    tests := []struct {
        name    string
        input   int
        want    int
        wantErr bool
    }{
        {"positive", 5, 10, false},
        {"zero", 0, 0, false},
        {"negative", -5, 0, true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Calculate(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("Calculate() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if got != tt.want {
                t.Errorf("Calculate() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### 2. Use gomock for Interface Testing

```go
func TestServiceWithMock(t *testing.T) {
    ctrl := gomock.NewController(t)
    defer ctrl.Finish()

    mockStore := NewMockTodoStore(ctrl)
    mockStore.EXPECT().GetTodos("session1").Return([]TodoItem{}, nil)

    service := NewService(mockStore)
    todos, err := service.List("session1")

    if err != nil {
        t.Fatalf("List() error = %v", err)
    }
    if len(todos) != 0 {
        t.Errorf("List() returned %d items, want 0", len(todos))
    }
}
```

### 3. Test Error Paths

Always test both success and failure scenarios:

```go
func TestOperation_Success(t *testing.T) {
    // Test happy path
}

func TestOperation_DatabaseError(t *testing.T) {
    // Test database failure
}

func TestOperation_InvalidInput(t *testing.T) {
    // Test validation errors
}
```

### 4. Use Subtests for Complex Scenarios

```go
func TestComplexOperation(t *testing.T) {
    t.Run("success path", func(t *testing.T) {
        // ...
    })

    t.Run("validation errors", func(t *testing.T) {
        t.Run("missing field", func(t *testing.T) {
            // ...
        })
        t.Run("invalid format", func(t *testing.T) {
            // ...
        })
    })
}
```

### 5. Clean Up Resources

```go
func TestWithCleanup(t *testing.T) {
    store, err := storage.New(t.TempDir() + "/test.db")
    if err != nil {
        t.Fatal(err)
    }
    defer store.Close() // Always clean up

    // Or use t.Cleanup for more complex scenarios
    t.Cleanup(func() {
        _ = store.Close()
    })
}
```

## Coverage Targets

Different package types have different coverage targets:

### Critical Packages (Target: 80%+)
- `pkg/storage` - Data persistence layer
- `pkg/model` - LLM model management (Current: 14.8%, +6.9% from Phase 3)
- `pkg/orchestrator` - Plan execution (Current: 32.4%, +2.7% from Phase 3)
- `pkg/conversation` - Message handling (Current: 69.2%, +16.3% from Phase 5)

### Business Logic (Target: 60%+)
- `pkg/tool/builtin` - Built-in tools
- `pkg/artifact` - Artifact management
- `pkg/skill` - Skill system (Current: 86.3%)
- `pkg/cost` - Cost tracking

### Integration Packages (Target: 40%+)
- `pkg/ipc` - IPC communication
- `pkg/github` - GitHub integration
- `pkg/session` - Session management

### UI/CLI (Target: 30%+)
- `pkg/ui` - Terminal UI
- `cmd/buckley` - CLI commands

### Support Packages (Target: 50%+)
- `pkg/config` - Configuration
- `pkg/errors` - Error handling
- `pkg/performance` - Performance utilities

## Advanced Testing

### Benchmarks

Buckley includes 22 benchmark tests for performance-critical operations:

**Conversation Benchmarks** (`pkg/conversation/benchmark_test.go`):
- Token estimation performance (short, medium, long messages)
- Message addition operations (user and assistant messages)
- Content extraction from multimodal messages
- Token counting across large conversations
- Message count access patterns

**Orchestrator Benchmarks** (`pkg/orchestrator/benchmark_test.go`):
- Plan serialization (small, medium, large plans)
- Plan deserialization and loading
- Plan listing operations (1, 10, 50, 100 plans)
- Identifier sanitization performance

**Tool Benchmarks** (`pkg/tool/builtin/benchmark_test.go`):
- Parameter schema validation
- Result serialization (success, errors, large data)
- TODO creation with varying list sizes (1, 5, 10, 50 items)

**Running Benchmarks:**
```bash
# Run all benchmarks
go test ./pkg/... -bench=. -benchmem

# Run specific package benchmarks
go test ./pkg/conversation -bench=. -benchmem

# Run specific benchmark
go test ./pkg/conversation -bench=BenchmarkEstimateTokens

# Compare before/after changes
go test ./pkg/conversation -bench=. -benchmem > before.txt
# ... make changes ...
go test ./pkg/conversation -bench=. -benchmem > after.txt
benchstat before.txt after.txt
```

### Fuzz Tests

Buckley includes 4 fuzz tests for input validation and robustness:

**Orchestrator Fuzz Tests** (`pkg/orchestrator/fuzz_test.go`):
- `FuzzSanitizeIdentifier` - Tests identifier sanitization with arbitrary inputs
- `FuzzPlanIDValidation` - Tests plan ID validation and path traversal prevention

**Tool Fuzz Tests** (`pkg/tool/builtin/fuzz_test.go`):
- `FuzzTodoToolParameters` - Tests TODO tool parameter parsing robustness
- `FuzzResultSerialization` - Tests result struct JSON serialization

**Running Fuzz Tests:**
```bash
# Run specific fuzz test for 10 seconds
go test ./pkg/orchestrator -fuzz=FuzzSanitizeIdentifier -fuzztime=10s

# Run all fuzz tests (quick check, 3s each)
go test ./pkg/orchestrator -fuzz=. -fuzztime=3s
go test ./pkg/tool/builtin -fuzz=. -fuzztime=3s

# Fuzz test to find crashes (longer duration)
go test ./pkg/orchestrator -fuzz=FuzzPlanIDValidation -fuzztime=5m
```

Fuzz tests validate invariants:
- No panics on malformed input
- Deterministic behavior (same input → same output)
- Proper error handling for edge cases
- Security properties (path traversal prevention, injection prevention)

## Running Tests

### Basic Test Execution

```bash
# Run all tests
./scripts/test.sh

# Run specific package
go test ./pkg/model

# Run specific test
go test ./pkg/model -run TestManagerInitialize

# Verbose output
go test ./pkg/model -v
```

### Test Flags

```bash
# Run with race detector (recommended for CI)
# Note: pkg/hunt may timeout with -race due to overhead; this is expected
GO_TEST_RACE=1 ./scripts/test.sh

# Disable test cache
GO_TEST_DISABLE_CACHE=1 ./scripts/test.sh

# Set custom timeout (default: 10m)
GO_TEST_TIMEOUT=30m ./scripts/test.sh

# Run all packages (including rarely-tested ones)
GO_TEST_TARGET=all ./scripts/test.sh

# Combine flags
GO_TEST_RACE=1 GO_TEST_TIMEOUT=30m ./scripts/test.sh
```

### Coverage Analysis

```bash
# Generate coverage report
go test ./pkg/... -coverprofile=coverage.out

# View coverage in browser
go tool cover -html=coverage.out

# Show coverage percentages
go test ./pkg/... -cover

# Coverage for specific package
go test ./pkg/model -coverprofile=model_coverage.out
go tool cover -func=model_coverage.out
```

## Common Testing Patterns

### Testing with Temporary Databases

```go
func TestWithDatabase(t *testing.T) {
    tempDir := t.TempDir()
    dbPath := filepath.Join(tempDir, "test.db")

    store, err := storage.New(dbPath)
    if err != nil {
        t.Fatalf("Failed to create store: %v", err)
    }
    t.Cleanup(func() { _ = store.Close() })

    // Use store in tests...
}
```

### Testing Context Cancellation

```go
func TestContextCancellation(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    err := LongRunningOperation(ctx)
    if err != context.DeadlineExceeded {
        t.Errorf("Expected context.DeadlineExceeded, got %v", err)
    }
}
```

### Testing with gomock Expectations

```go
func TestWithMultipleExpectations(t *testing.T) {
    ctrl := gomock.NewController(t)
    defer ctrl.Finish()

    mockClient := NewMockModelClient(ctrl)

    // Exact call
    mockClient.EXPECT().Complete(ctx, "model-1", "prompt").Return("response", nil)

    // Any matcher
    mockClient.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any()).Return("", errors.New("error"))

    // Call count
    mockClient.EXPECT().Complete(ctx, "model-2", "prompt").Return("ok", nil).Times(3)

    // Any times (0 or more)
    mockClient.EXPECT().Status().Return("ready").AnyTimes()
}
```

### Testing File Operations

```go
func TestFileOperations(t *testing.T) {
    tempDir := t.TempDir() // Automatically cleaned up

    testFile := filepath.Join(tempDir, "test.txt")
    content := []byte("test content")

    if err := os.WriteFile(testFile, content, 0644); err != nil {
        t.Fatalf("WriteFile failed: %v", err)
    }

    // Test file reading...
}
```

## Integration Testing

Integration tests are located in `tests/integration/` and test end-to-end workflows with real dependencies.

### Running Integration Tests

```bash
# Run all integration tests
go test -tags=integration ./tests/integration -v

# Run specific integration test
go test -tags=integration ./tests/integration -run TestStorageSessionLifecycle

# Skip integration tests (short mode)
go test -tags=integration ./tests/integration -short
```

### Available Integration Tests

**Storage Integration (`storage_integration_test.go`)**:
- `TestStorageSessionLifecycle` - Create, retrieve, and list sessions
- `TestStorageMessagePersistence` - Save and load messages with session stats
- `TestStorageSessionDeletion` - Delete sessions and verify cascade (partial)
- `TestGitSessionDetection` - Generate session IDs from git repository info
- `TestDatabaseConcurrency` - Multiple concurrent sessions and messages

### Writing Integration Tests

Integration tests should:
- Use build tag: `//go:build integration`
- Skip in short mode: `if testing.Short() { t.Skip(...) }`
- Use `t.TempDir()` for temporary files and databases
- Clean up resources with `defer`
- Test actual workflows, not mocked behavior

**Example:**
```go
//go:build integration
// +build integration

package integration

func TestStorageLifecycle(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }

    tempDir := t.TempDir()
    dbPath := filepath.Join(tempDir, "test.db")
    store, err := storage.New(dbPath)
    if err != nil {
        t.Fatalf("Failed to create storage: %v", err)
    }
    defer store.Close()

    // Test actual workflows...
}
```

### Known Issues

- `TestStorageSessionDeletion` partially fails due to foreign key constraints - session deletion may require CASCADE DELETE configuration in schema

## Continuous Integration

Recommended CI test command:

```bash
GO_TEST_RACE=1 GO_TEST_TIMEOUT=30m ./scripts/test.sh
```

This enables:
- Race detection for concurrency bugs
- Extended timeout for slower CI environments
- All standard package tests

## Troubleshooting

### Common Issues

**Issue: Tests fail with "database locked"**
```
Solution: Use WAL mode in SQLite tests:
db.Exec("PRAGMA journal_mode=WAL")
```

**Issue: Flaky tests**
```
Solution:
1. Enable race detector to find data races
2. Check for timing dependencies
3. Use proper synchronization (mutexes, channels)
4. Avoid time.Sleep() in tests
```

**Issue: gomock "missing call" errors**
```
Solution:
1. Verify EXPECT() calls match actual function calls exactly
2. Check that ctrl.Finish() is deferred
3. Use gomock.Any() for flexible matching
```

## Additional Resources

- [gomock Documentation](https://github.com/uber-go/mock)
- [Go Testing Package](https://pkg.go.dev/testing)
- [Table Driven Tests](https://github.com/golang/go/wiki/TableDrivenTests)
- [Go Test Comments](https://github.com/golang/go/wiki/TestComments)

## Contributing

When adding new tests:

1. Follow existing patterns in the package
2. Use table-driven tests for multiple scenarios
3. Generate mocks for new interfaces
4. Test both success and error paths
5. Add package coverage to this document
6. Update mock generation table if adding new mocks

---

**Last Updated:** 2025-11-18
**Mock System:** go.uber.org/mock v0.6.0
**Total Mocks:** 13 interfaces with go:generate directives
**Total Unit Tests:** 445+ test functions across all packages
**Benchmark Tests:** 22 performance benchmarks
**Fuzz Tests:** 4 fuzz tests for input validation
**Integration Tests:** 5 tests (4 passing, 1 partial)

**Recent Improvements:**
- **Phase 5** (Advanced Testing): Added 22 benchmarks, 4 fuzz tests, and 26 edge case tests for compaction
  - Conversation coverage: 52.9% → 69.2% (+16.3%)
  - Added comprehensive edge case testing for compaction logic
  - Fuzz tests validated robustness with 76K+ executions
- **Phase 4**: Added 13 go:generate directives, integration test suite, race detector support
- **Phase 3**: Added 29 tests across pkg/model (+13 tests, +6.9% coverage) and pkg/orchestrator (+14 tests, +2.7% coverage)
