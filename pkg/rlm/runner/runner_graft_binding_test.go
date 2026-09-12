package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/graft"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
)

func TestNewWithRulesEngineWiresEagerRuntime(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	runner := newGraftBindingTestRunner(t, nil, WithRulesEngine(engine))
	field := reflect.ValueOf(runner.runtime).Elem().FieldByName("engine")
	if !field.IsValid() || field.Kind() != reflect.Pointer || field.Pointer() != reflect.ValueOf(engine).Pointer() {
		t.Fatal("eager runtime did not capture the supplied rules engine")
	}
}

func TestNewWithGraftClientWiresEagerRuntime(t *testing.T) {
	client := &graft.Client{}
	runner := newGraftBindingTestRunner(t, WithGraftClient(client))

	if got, want := runtimeGraftClientPointer(t, runner), reflect.ValueOf(client).Pointer(); got != want {
		t.Fatalf("runtime Graft client = %#x, want %#x", got, want)
	}
}

func TestRunnerSetGraftClientDoesNotRebindInitializedRuntime(t *testing.T) {
	initial := &graft.Client{}
	runner := newGraftBindingTestRunner(t, WithGraftClient(initial))

	runner.SetGraftClient(&graft.Client{})

	if got, want := runtimeGraftClientPointer(t, runner), reflect.ValueOf(initial).Pointer(); got != want {
		t.Fatalf("runtime Graft client after SetGraftClient = %#x, want original %#x", got, want)
	}
}

func TestConstructorDependenciesSurviveLazyInitialization(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	client := &graft.Client{}
	runner := New(nil, nil, tool.NewEmptyRegistry(), nil, nil, nil, WithRulesEngine(engine), WithGraftClient(client))
	if runner.runtime != nil {
		t.Fatal("runtime should not initialize without a model manager")
	}
	runner.models = newGraftBindingTestRunner(t).models
	if err := runner.ensureRuntime(); err != nil {
		t.Fatal(err)
	}
	if got := runtimeGraftClientPointer(t, runner); got != reflect.ValueOf(client).Pointer() {
		t.Fatal("lazy runtime lost the constructor Graft client")
	}
	if got := reflect.ValueOf(runner.runtime).Elem().FieldByName("engine").Pointer(); got != reflect.ValueOf(engine).Pointer() {
		t.Fatal("lazy runtime lost the constructor rules engine")
	}
}

func newGraftBindingTestRunner(t *testing.T, opts ...Option) *Runner {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/models" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"graft-binding-model","context_length":128000,"pricing":{"prompt":"0.000001","completion":"0.000002"},"supported_parameters":["tools"]}]}`)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Models.Execution = "openai_compatible/graft-binding-model"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	runner := New(nil, mgr, tool.NewEmptyRegistry(), cfg, nil, nil, opts...)
	if runner.runtime == nil {
		t.Fatal("New did not eagerly initialize the runtime")
	}
	return runner
}

// runtimeGraftClientPointer is a narrow test-only observation of Runtime's
// dependency capture. Runner intentionally exposes no runtime-rebinding API.
func runtimeGraftClientPointer(t *testing.T, runner *Runner) uintptr {
	t.Helper()
	runtimeValue := reflect.ValueOf(runner.runtime)
	if runtimeValue.Kind() != reflect.Pointer || runtimeValue.IsNil() {
		t.Fatal("runtime is nil")
	}
	client := runtimeValue.Elem().FieldByName("graftClient")
	if !client.IsValid() || client.Kind() != reflect.Pointer {
		t.Fatal("runtime graft client field unavailable")
	}
	return client.Pointer()
}
