package headless

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
)

type shutdownTool struct {
	fakeEchoTool
	closed atomic.Int32
}

func (s *shutdownTool) Close() error {
	s.closed.Add(1)
	return nil
}

func TestRunnerStop_ClosesSessionTools(t *testing.T) {
	for _, mode := range []string{"inert", "legacy", "durable"} {
		t.Run(mode, func(t *testing.T) {
			store := newTestStore(t)
			tools := tool.NewEmptyRegistry()
			probe := &shutdownTool{}
			tools.Register(probe)
			cfg := RunnerConfig{
				Session:      &storage.Session{ID: "shutdown-" + mode},
				ModelManager: newTestModelManager(t),
				Tools:        tools,
				Store:        store,
			}
			if mode == "durable" {
				cfg.EvidenceStore, cfg.RunLedger = newRegistryDurableStores(t, store)
				cfg.CommandJournal = store
				cfg.StepJournal = cfg.RunLedger.(agentloop.DurableStepJournal)
			}
			runner, err := newInertRunner(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(runner.Stop)
			if mode != "inert" {
				if err := runner.activate(); err != nil {
					t.Fatal(err)
				}
			}
			runner.Stop()
			runner.Stop()
			if got := probe.closed.Load(); got != 1 {
				t.Fatalf("session tool closed %d times, want once", got)
			}
			if err := runner.HandleSessionCommand(command.SessionCommand{Type: "input", Content: "after shutdown"}); err == nil {
				t.Fatal("stopped runner accepted work")
			}
		})
	}
}

func TestRunnerStop_DrainsCommandBeforeClosingTools(t *testing.T) {
	store := newTestStore(t)
	if err := store.EnsureSession("shutdown-active"); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	tools := tool.NewEmptyRegistry()
	probe := &shutdownTool{}
	tools.Register(probe)
	runner, err := NewRunner(RunnerConfig{
		Session:      &storage.Session{ID: "shutdown-active"},
		ModelManager: newTestModelManager(t),
		Tools:        tools,
		Store:        store,
		Emitter: eventEmitterFunc(func(event RunnerEvent) {
			if event.Type == EventCommandStarted {
				close(started)
				<-release
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		runner.Stop()
	})
	if err := runner.HandleSessionCommand(command.SessionCommand{Type: "input", Content: "stop before the turn starts"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("command did not start")
	}
	stopped := make(chan struct{})
	go func() {
		runner.Stop()
		close(stopped)
	}()
	<-runner.commandStop
	select {
	case <-stopped:
		t.Fatal("Stop returned while a command was running")
	case <-time.After(25 * time.Millisecond):
	}
	if probe.closed.Load() != 0 {
		t.Fatal("tools closed while a command was running")
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not drain the command")
	}
	if probe.closed.Load() != 1 || runner.State() != StateStopped {
		t.Fatalf("shutdown lost terminal state or cleanup: state=%s closed=%d", runner.State(), probe.closed.Load())
	}
}
