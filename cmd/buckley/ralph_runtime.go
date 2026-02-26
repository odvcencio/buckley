package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/filewatch"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/ralph"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// ralphRuntime holds the shared infrastructure components used by both
// runRalphCommand and runRalphResume. Call setupRalphRuntime to create one
// and Close when finished.
type ralphRuntime struct {
	cfg              *config.Config
	mgr              *model.Manager
	store            *storage.Store
	registry         *tool.Registry
	runner           *ralphHeadlessRunner
	backendRegistry  *ralph.BackendRegistry
	orchestrator     *ralph.Orchestrator
	memoryStore      *ralph.MemoryStore
	contextProcessor *ralph.ContextProcessor
	summaryGenerator *ralph.SummaryGenerator
	projectCtx       string
	logger           *ralph.Logger
}

// ralphRuntimeConfig contains the parameters needed to build a ralphRuntime.
type ralphRuntimeConfig struct {
	cfg         *config.Config
	mgr         *model.Manager
	store       *storage.Store
	controlCfg  *ralph.ControlConfig
	logger      *ralph.Logger
	session     *ralph.Session
	sessionID   string
	sandboxPath string
	workDir     string
	runDir      string
	timeout     time.Duration
}

// setupRalphRuntime creates the shared tool registry, headless runner,
// orchestrator, memory store, context processor, summary generator and
// project context that are identical between runRalphCommand and
// runRalphResume. The caller is responsible for calling Close.
func setupRalphRuntime(rc ralphRuntimeConfig) (*ralphRuntime, error) {
	rt := &ralphRuntime{
		cfg:   rc.cfg,
		mgr:   rc.mgr,
		store: rc.store,
	}

	// Tool registry
	rt.registry = tool.NewRegistry()
	rt.registry.SetWorkDir(rc.sandboxPath)
	rt.registry.ConfigureContainers(rc.cfg, rc.sandboxPath)
	rt.registry.ConfigureDockerSandbox(rc.cfg, rc.sandboxPath)
	rt.registry.SetSandboxConfig(rc.cfg.Sandbox.ToSandboxConfig(rc.sandboxPath))
	registerMCPTools(rc.cfg, rt.registry)

	fileWatcher := filewatch.NewFileWatcher(100)
	fileWatcher.Subscribe("*", func(change filewatch.FileChange) {
		if rc.logger != nil {
			rc.logger.LogFileChange(change)
		}
		if rc.session != nil && strings.TrimSpace(change.Path) != "" {
			rc.session.AddModifiedFile(change.Path)
		}
	})
	rt.registry.Use(tool.FileChangeTracking(fileWatcher))

	// Memory store
	if rc.controlCfg.Memory.Enabled {
		memoryPath := filepath.Join(rc.runDir, "memory.db")
		ms, err := ralph.NewMemoryStore(memoryPath)
		if err != nil {
			return nil, fmt.Errorf("create memory store: %w", err)
		}
		rt.memoryStore = ms
		rc.logger.SetEventSink(ms)
	}

	if rt.memoryStore != nil {
		rt.registry.Register(&builtin.SessionMemoryTool{
			Store:     rt.memoryStore,
			SessionID: rc.sessionID,
		})
	}

	// Headless runner
	runner, err := newRalphHeadlessRunner(rc.cfg, rc.mgr, rc.store, rt.registry, rc.logger, rc.sessionID, rc.sandboxPath, rc.timeout)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("creating headless runner: %w", err)
	}
	rt.runner = runner

	// Backend registry + orchestrator
	backendRegistry := ralph.NewBackendRegistry()
	for name, backend := range rc.controlCfg.Backends {
		if backend.Type == ralph.BackendTypeInternal {
			backendRegistry.Register(ralph.NewInternalBackend(name, runner, ralph.InternalOptions{
				PromptTemplate: backend.PromptTemplate,
			}))
		} else {
			backendRegistry.Register(ralph.NewExternalBackend(name, backend.Command, backend.Args, backend.Options))
		}
	}

	rt.backendRegistry = backendRegistry
	rt.orchestrator = ralph.NewOrchestrator(backendRegistry, rc.controlCfg)
	rt.orchestrator.SetLogger(rc.logger)
	rt.orchestrator.SetContextProvider(modelContextProvider{manager: rc.mgr})

	// Context processor
	if strings.TrimSpace(rc.controlCfg.ContextProcessing.Model) != "" {
		maxTokens := rc.controlCfg.ContextProcessing.MaxOutputTokens
		if maxTokens <= 0 {
			maxTokens = 500
		}
		rt.contextProcessor = ralph.NewContextProcessor(rc.mgr, rc.controlCfg.ContextProcessing.Model, maxTokens)
	}

	// Summary generator
	if strings.TrimSpace(rc.controlCfg.Memory.SummaryModel) != "" {
		rt.summaryGenerator = ralph.NewSummaryGenerator(rc.mgr, rc.controlCfg.Memory.SummaryModel, 500)
	}

	// Project context
	rt.projectCtx = ralph.BuildProjectContext(rc.workDir)
	rt.logger = rc.logger

	return rt, nil
}

// baseExecutorOpts returns the common executor options shared by both
// runRalphCommand and runRalphResume.
func (rt *ralphRuntime) baseExecutorOpts() []ralph.ExecutorOption {
	return []ralph.ExecutorOption{
		ralph.WithProgressWriter(os.Stdout),
		ralph.WithOrchestrator(rt.orchestrator),
		ralph.WithMemoryStore(rt.memoryStore),
		ralph.WithContextProcessor(rt.contextProcessor),
		ralph.WithSummaryGenerator(rt.summaryGenerator),
		ralph.WithProjectContext(rt.projectCtx),
	}
}

// runWithSignalHandler creates a cancellable context, registers signal
// handling, runs fn, and prints a completion summary.
func (rt *ralphRuntime) runWithSignalHandler(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		<-sigChan
		fmt.Fprintf(os.Stderr, "\nReceived interrupt, shutting down...\n")
		rt.runner.Stop()
		cancel()
	}()

	return fn(ctx)
}

// Close releases resources owned by the runtime.
func (rt *ralphRuntime) Close() {
	if rt.runner != nil {
		rt.runner.Stop()
	}
	if rt.memoryStore != nil {
		rt.memoryStore.Close()
	}
}

// printSessionStats prints the standard completion summary.
func printSessionStats(label string, stats ralph.SessionStats) {
	fmt.Printf("\n%s\n", label)
	fmt.Printf("  Iterations: %d\n", stats.Iteration)
	fmt.Printf("  Tokens: %d\n", stats.TotalTokens)
	fmt.Printf("  Cost: $%.4f\n", stats.TotalCost)
	fmt.Printf("  Files modified: %d\n", stats.FilesModified)
	fmt.Printf("  Elapsed: %s\n", stats.Elapsed.Round(time.Second))
}
