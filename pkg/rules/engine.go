package rules

import (
	"fmt"
	"sync"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/strategy"
	"m31labs.dev/arbiter/vm"
)

// Engine compiles all .arb domains at startup and provides typed evaluation.
type Engine struct {
	compiled map[string]*arbiter.Program
	loader   *Loader
	mu       sync.RWMutex
}

// Option configures engine construction.
type Option func(*engineOpts)

type engineOpts struct {
	overrideDir string
}

// WithUserOverrides sets a directory to check for user .arb overrides.
func WithUserOverrides(dir string) Option {
	return func(o *engineOpts) { o.overrideDir = dir }
}

// NewEngine compiles all rule domains and returns a ready engine.
func NewEngine(opts ...Option) (*Engine, error) {
	o := &engineOpts{}
	for _, fn := range opts {
		fn(o)
	}

	loader := NewLoader(o.overrideDir)
	domains, err := loader.Domains()
	if err != nil {
		return nil, fmt.Errorf("listing rule domains: %w", err)
	}

	compiled := make(map[string]*arbiter.Program, len(domains))
	for _, domain := range domains {
		src, err := loader.Load(domain)
		if err != nil {
			return nil, fmt.Errorf("loading domain %q: %w", domain, err)
		}
		prog, err := arbiter.Compile(src)
		if err != nil {
			return nil, fmt.Errorf("compiling domain %q: %w", domain, err)
		}
		compiled[domain] = prog
	}

	return &Engine{
		compiled: compiled,
		loader:   loader,
	}, nil
}

func (e *Engine) getProgram(domain string) (*arbiter.Program, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	prog, ok := e.compiled[domain]
	if !ok {
		return nil, fmt.Errorf("unknown rule domain: %s", domain)
	}
	return prog, nil
}

// Eval evaluates rule-based domains against a typed struct.
func Eval[T any](e *Engine, domain string, facts T) ([]vm.MatchedRule, error) {
	prog, err := e.getProgram(domain)
	if err != nil {
		return nil, err
	}
	return arbiter.EvalTyped(prog, facts)
}

// EvalGoverned evaluates rule-based domains with governance.
func EvalGoverned[T any](e *Engine, domain string, facts T, ctx map[string]any) ([]vm.MatchedRule, *govern.Arbitrace, error) {
	prog, err := e.getProgram(domain)
	if err != nil {
		return nil, nil, err
	}
	return arbiter.EvalGovernedTyped(prog, facts, prog.Segments, ctx)
}

// EvalStrategy evaluates strategy-based domains with composed facts.
func (e *Engine) EvalStrategy(domain, name string, facts map[string]any) (strategy.Result, error) {
	prog, err := e.getProgram(domain)
	if err != nil {
		return strategy.Result{}, err
	}
	if prog.Strategies == nil {
		return strategy.Result{}, fmt.Errorf("nil compiled strategies")
	}
	return prog.Strategies.Evaluate(name, facts)
}

// Reload recompiles a single domain from disk.
func (e *Engine) Reload(domain string) error {
	src, err := e.loader.Load(domain)
	if err != nil {
		return fmt.Errorf("loading domain %q for reload: %w", domain, err)
	}
	prog, err := arbiter.Compile(src)
	if err != nil {
		return fmt.Errorf("compiling domain %q for reload: %w", domain, err)
	}
	e.mu.Lock()
	e.compiled[domain] = prog
	e.mu.Unlock()
	return nil
}

// Domains returns the list of loaded domain names.
func (e *Engine) Domains() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	domains := make([]string, 0, len(e.compiled))
	for d := range e.compiled {
		domains = append(domains, d)
	}
	return domains
}

// EvalMap evaluates a rule-based domain against a flat map of facts.
// It is intended for CLI tooling where facts come from JSON rather than typed structs.
func EvalMap(e *Engine, domain string, facts map[string]any) ([]vm.MatchedRule, error) {
	e.mu.RLock()
	prog, ok := e.compiled[domain]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown rule domain: %s", domain)
	}
	dc := arbiter.DataFromMap(facts, prog)
	return arbiter.Eval(prog, dc)
}
