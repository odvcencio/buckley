package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/odvcencio/arbiter"
	"github.com/odvcencio/buckley/pkg/rules"
)

// runRulesCommand dispatches buckley rules subcommands.
func runRulesCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley rules <list|check|eval> [args...]")
	}
	switch args[0] {
	case "list":
		return runRulesList()
	case "check":
		return runRulesCheck(args[1:])
	case "eval":
		return runRulesEval(args[1:])
	default:
		return fmt.Errorf("unknown rules subcommand: %s (use list, check, or eval)", args[0])
	}
}

// runRulesList lists all loaded domains and whether each uses an embedded
// default or a user override from ~/.buckley/rules/.
func runRulesList() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home dir: %w", err)
	}
	overrideDir := filepath.Join(home, ".buckley", "rules")

	e, err := rules.NewEngine(rules.WithUserOverrides(overrideDir))
	if err != nil {
		return fmt.Errorf("initializing rules engine: %w", err)
	}

	domains := e.Domains()
	sort.Strings(domains)

	fmt.Printf("%-20s  %s\n", "DOMAIN", "SOURCE")
	fmt.Println(strings.Repeat("-", 40))
	for _, d := range domains {
		source := "embedded (default)"
		overridePath := filepath.Join(overrideDir, d+".arb")
		if _, statErr := os.Stat(overridePath); statErr == nil {
			source = "user override: " + overridePath
		}
		fmt.Printf("%-20s  %s\n", d, source)
	}
	return nil
}

// runRulesCheck validates that the given .arb file compiles without errors.
func runRulesCheck(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: buckley rules check <file.arb>")
	}
	path := args[0]
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	_, err = arbiter.CompileFull(src)
	if err != nil {
		return fmt.Errorf("compile error in %s: %w", path, err)
	}
	fmt.Printf("OK: %s compiles successfully\n", path)
	return nil
}

// runRulesEval evaluates a domain against JSON facts and prints matched rules.
func runRulesEval(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: buckley rules eval <domain> <facts.json>")
	}
	domain := args[0]
	factsPath := args[1]

	factsData, err := os.ReadFile(factsPath)
	if err != nil {
		return fmt.Errorf("reading facts file %s: %w", factsPath, err)
	}

	var facts map[string]any
	if err := json.Unmarshal(factsData, &facts); err != nil {
		return fmt.Errorf("parsing facts JSON: %w", err)
	}

	home, _ := os.UserHomeDir()
	var engineOpts []rules.Option
	if home != "" {
		engineOpts = append(engineOpts, rules.WithUserOverrides(filepath.Join(home, ".buckley", "rules")))
	}

	e, err := rules.NewEngine(engineOpts...)
	if err != nil {
		return fmt.Errorf("initializing rules engine: %w", err)
	}

	matched, err := rules.EvalMap(e, domain, facts)
	if err != nil {
		return fmt.Errorf("evaluating domain %q: %w", domain, err)
	}

	if len(matched) == 0 {
		fmt.Printf("domain %q: no rules matched\n", domain)
		return nil
	}

	fmt.Printf("domain %q: %d rule(s) matched\n\n", domain, len(matched))
	for _, m := range matched {
		fmt.Printf("  rule:     %s\n", m.Name)
		fmt.Printf("  priority: %d\n", m.Priority)
		fmt.Printf("  action:   %s\n", m.Action)
		if len(m.Params) > 0 {
			fmt.Printf("  params:\n")
			keys := make([]string, 0, len(m.Params))
			for k := range m.Params {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("    %s: %v\n", k, m.Params[k])
			}
		}
		fmt.Println()
	}
	return nil
}
