//go:build !batch_k8s

// Package orchestrator's no-op batch coordinator stub. This is the default
// build: it keeps pkg/orchestrator (and the CLI binary) free of the
// Kubernetes client-go dependency tree. Build with -tags batch_k8s (see
// `make build-batch`) to get the real Kubernetes-backed implementation in
// batch_coordinator.go.
package orchestrator

import (
	"context"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

// errBatchUnsupported is returned by every BatchCoordinator method in
// builds without the batch_k8s tag.
var errBatchUnsupported = fmt.Errorf("batch: built without batch support; rebuild with -tags batch_k8s (see `make build-batch`)")

// BatchCoordinator is a stand-in for the Kubernetes-backed batch
// coordinator. It carries no state; every method reports errBatchUnsupported.
type BatchCoordinator struct{}

// BatchTaskResult mirrors the batch_k8s build's result shape so callers
// compile unchanged regardless of build tag.
type BatchTaskResult struct {
	JobName      string
	RemoteBranch string
}

// NewBatchCoordinator returns nil (no error) when batch is disabled in cfg,
// matching the batch_k8s build's behavior. If batch is enabled, it reports
// errBatchUnsupported so the operator knows to rebuild with -tags batch_k8s.
func NewBatchCoordinator(cfg config.BatchConfig, workflow *WorkflowManager) (*BatchCoordinator, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	return nil, errBatchUnsupported
}

// Enabled always reports false in builds without batch_k8s.
func (b *BatchCoordinator) Enabled() bool {
	return false
}

// DispatchTask always fails in builds without batch_k8s.
func (b *BatchCoordinator) DispatchTask(ctx context.Context, plan *Plan, task *Task) (*BatchTaskResult, error) {
	return nil, errBatchUnsupported
}

// CleanupWorkspaces always fails in builds without batch_k8s.
func (b *BatchCoordinator) CleanupWorkspaces(ctx context.Context, olderThan time.Duration) (int, error) {
	return 0, errBatchUnsupported
}
