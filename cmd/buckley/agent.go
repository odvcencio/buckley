package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"m31labs.dev/buckley/pkg/agentspec"
)

func runAgentCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley agent <check|show> [args...]")
	}
	switch args[0] {
	case "check":
		return runAgentCheck(args[1:])
	case "show":
		return runAgentShow(args[1:])
	default:
		return fmt.Errorf("unknown agent subcommand: %s (use check or show)", args[0])
	}
}

func runAgentCheck(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: buckley agent check <agent.yaml>")
	}
	spec, diagnostics, err := loadAgentSpec(args[0])
	if err != nil {
		return err
	}
	if hasAgentSpecErrors(diagnostics) {
		fmt.Print(agentspec.RenderText(spec, diagnostics))
		return fmt.Errorf("agent spec has validation errors")
	}
	fmt.Printf("OK: %s is a valid Buckley agent spec\n", args[0])
	if len(diagnostics) > 0 {
		fmt.Print(agentspec.RenderText(spec, diagnostics))
	}
	return nil
}

func runAgentShow(args []string) error {
	fs := flag.NewFlagSet("agent show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: buckley agent show [--format text|json] <agent.yaml>")
	}
	spec, diagnostics, err := loadAgentSpec(fs.Arg(0))
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "", "text":
		fmt.Print(agentspec.RenderText(spec, diagnostics))
	case "json":
		data, err := agentspec.JSON(spec, diagnostics)
		if err != nil {
			return fmt.Errorf("encoding agent spec: %w", err)
		}
		fmt.Println(string(data))
	default:
		return fmt.Errorf("unknown format %q (use text or json)", *format)
	}
	return nil
}

func loadAgentSpec(path string) (*agentspec.Spec, []agentspec.Diagnostic, error) {
	spec, err := agentspec.LoadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return spec, spec.Validate(), nil
}

func hasAgentSpecErrors(diagnostics []agentspec.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == agentspec.SeverityError {
			return true
		}
	}
	return false
}
