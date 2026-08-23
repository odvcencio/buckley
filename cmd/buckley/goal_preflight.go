package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/workspaceguard"
)

var errGoalPreflightBlocked = errors.New("goal launch preflight blocked")

const goalPreflightTimeout = 45 * time.Second
const goalPreflightCleanupTimeout = 15 * time.Second

type goalPreflightDeps struct {
	loadConfig   func(string) (*config.LaunchOperatorConfig, error)
	inspector    workspaceguard.Inspector
	getwd        func() (string, error)
	checkSandbox func(context.Context, string, *config.LaunchOperatorConfig, workspaceguard.Report) error
}

type goalPreflightOutput struct {
	Profile   workspaceguard.LaunchProfile `json:"profile"`
	Workspace workspaceguard.Report        `json:"workspace"`
}

func runGoalPreflight(args []string) error {
	deps := goalPreflightDeps{
		loadConfig: config.LoadLaunchOperatorConfig,
		inspector:  workspaceguard.NewGitInspector(workspaceguard.Options{}),
		getwd:      os.Getwd,
		checkSandbox: func(ctx context.Context, root string, cfg *config.LaunchOperatorConfig, report workspaceguard.Report) error {
			if cfg == nil {
				return tool.ErrLaunchSandboxUnavailable
			}
			launch, err := tool.NewLaunchRegistry(ctx, tool.LaunchRegistryOptions{
				WorkspaceRoot: root,
				WorkerImage:   cfg.WorkerImage,
			})
			if err != nil {
				if launch != nil {
					return reconcileGoalPreflightAdmissionError(launch, err)
				}
				return err
			}
			if !launch.MatchesWorkspace(report) {
				return errors.Join(tool.ErrLaunchSandboxPolicy, closeGoalPreflightLaunch(launch))
			}
			return closeGoalPreflightLaunch(launch)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), goalPreflightTimeout)
	defer cancel()
	return runGoalPreflightWith(ctx, args, os.Stdout, deps)
}

func goalPreflightAdmissionCause(err error) error {
	if errors.Is(err, tool.ErrLaunchSandboxPolicy) {
		return errors.Join(tool.ErrLaunchSandboxUnavailable, tool.ErrLaunchSandboxPolicy)
	}
	return tool.ErrLaunchSandboxUnavailable
}

func reconcileGoalPreflightAdmissionError(launch goalPreflightLaunchCloser, admissionErr error) error {
	cleanupErr := closeGoalPreflightLaunch(launch)
	if cleanupErr == nil {
		return goalPreflightAdmissionCause(admissionErr)
	}
	return errors.Join(goalPreflightAdmissionCause(admissionErr), cleanupErr)
}

func runGoalPreflightWith(ctx context.Context, args []string, out io.Writer, deps goalPreflightDeps) error {
	if err := rejectDuplicatePreflightFlags(args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("goal preflight", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profileValue := fs.String("launch-profile", "", "launch profile: gsxmail | gosx | tqwebp")
	workspaceValue := fs.String("workspace", "", "canonical Git workspace root (default: current directory)")
	jsonOutput := fs.Bool("json", false, "emit bounded JSON")
	if err := fs.Parse(args); err != nil {
		return errors.New("invalid goal preflight flags")
	}
	if fs.NArg() != 0 {
		return errors.New("usage: buckley goal preflight --launch-profile gsxmail|gosx|tqwebp [--workspace root] [--json]")
	}
	profile, err := workspaceguard.ResolveLaunchProfile(*profileValue)
	if err != nil {
		return err
	}
	workspaceRoot := strings.TrimSpace(*workspaceValue)
	if workspaceRoot == "" {
		if deps.getwd == nil {
			return errors.New("goal preflight workspace unavailable")
		}
		workspaceRoot, err = deps.getwd()
		if err != nil {
			return errors.New("goal preflight workspace unavailable")
		}
	}
	if deps.inspector == nil {
		return errors.New("goal preflight inspector unavailable")
	}
	report, err := deps.inspector.Inspect(ctx, workspaceguard.Request{Root: workspaceRoot})
	if err != nil {
		return errors.New("goal preflight workspace observation failed")
	}
	if err := report.Validate(); err != nil {
		return errors.New("goal preflight workspace observation failed")
	}
	initialWorkspace := report

	var cfg *config.LaunchOperatorConfig
	if report.Allowed {
		if deps.loadConfig == nil {
			report = workspaceguard.AddFindings(report, workspaceguard.Finding{Code: workspaceguard.ReasonConfigUnavailable})
		} else {
			cfg, err = deps.loadConfig(workspaceRoot)
			if err != nil || cfg == nil {
				report = workspaceguard.AddFindings(report, workspaceguard.Finding{Code: workspaceguard.ReasonConfigUnavailable})
			} else {
				report = workspaceguard.AddFindings(report, workspaceguard.CheckDiagnostics(workspaceguard.DiagnosticsPolicy{
					NetworkLogsEnabled:           cfg.Diagnostics.NetworkLogsEnabled,
					TelemetryPayloadsOverNetwork: cfg.Diagnostics.TelemetryPayloadsOverNetwork,
				})...)
			}
		}
		if deps.checkSandbox == nil || cfg == nil {
			report = workspaceguard.AddFindings(report, workspaceguard.Finding{Code: workspaceguard.ReasonSandboxUnavailable})
		} else if sandboxErr := deps.checkSandbox(ctx, workspaceRoot, cfg, initialWorkspace); sandboxErr != nil {
			code := workspaceguard.ReasonSandboxUnavailable
			label := ""
			var cleanup *tool.LaunchCleanupRequiredError
			if errors.As(sandboxErr, &cleanup) {
				code = workspaceguard.ReasonCleanupRequired
				label = cleanup.Container
			}
			if cleanup == nil && errors.Is(sandboxErr, tool.ErrLaunchSandboxPolicy) {
				code = workspaceguard.ReasonSandboxPolicy
			}
			report = workspaceguard.AddFindings(report, workspaceguard.Finding{Code: code, Label: label})
		}
		current, observeErr := deps.inspector.Inspect(ctx, workspaceguard.Request{Root: workspaceRoot})
		if observeErr != nil || current.Validate() != nil || !sameWorkspaceReport(initialWorkspace, current) {
			report = workspaceguard.AddFindings(report, workspaceguard.Finding{Code: workspaceguard.ReasonWorkspaceChanged, Label: "workspace"})
		}
	}
	if err := report.Validate(); err != nil {
		return errors.New("goal preflight result validation failed")
	}

	result := goalPreflightOutput{Profile: profile, Workspace: report}
	if *jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(true)
		if err := encoder.Encode(result); err != nil {
			return errors.New("write goal preflight output")
		}
	} else {
		if err := renderGoalPreflight(out, result); err != nil {
			return err
		}
	}
	if !report.Allowed {
		return fmt.Errorf("%w: %s", errGoalPreflightBlocked, joinedPreflightCodes(report.Findings))
	}
	return nil
}

type goalPreflightLaunchCloser interface {
	CloseContext(context.Context) error
}

func closeGoalPreflightLaunch(launch goalPreflightLaunchCloser) error {
	if launch == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), goalPreflightCleanupTimeout)
	defer cancel()
	var last error
	for {
		last = launch.CloseContext(ctx)
		if last == nil || !errors.Is(last, tool.ErrLaunchCleanupRequired) {
			return last
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last
		case <-timer.C:
		}
	}
}

func sameWorkspaceReport(left, right workspaceguard.Report) bool {
	if left.Allowed != right.Allowed || left.Evidence != right.Evidence || len(left.Findings) != len(right.Findings) {
		return false
	}
	for idx := range left.Findings {
		if left.Findings[idx] != right.Findings[idx] {
			return false
		}
	}
	return true
}

func renderGoalPreflight(out io.Writer, result goalPreflightOutput) error {
	status := "PASS"
	if !result.Workspace.Allowed {
		status = "BLOCKED"
	}
	limits := result.Profile.Limits
	lines := []string{
		"Goal launch preflight: " + status,
		fmt.Sprintf("  profile: %s · state: %s · enforced: %t · price: %s", result.Profile.ID, limits.State, limits.Enforced, limits.PricePolicy),
		fmt.Sprintf("  display limits: requests=%d input=%d output=%d total=%d output/request=%d", limits.ModelRequests, limits.InputTokens, limits.OutputTokens, limits.TotalTokens, limits.MaxOutputPerRequest),
		fmt.Sprintf("  display timeouts: request=%s turn=%s run=%s · capacity=%d · per-run=%d", limits.RequestTimeoutText, limits.TurnTimeoutText, limits.AbsoluteRunTimeText, limits.GlobalCapacity, limits.PerRunParallelism),
		fmt.Sprintf("  evidence: head=%s manifest=%s license=%s", shortDigest(result.Workspace.Evidence.HEAD), shortDigest(result.Workspace.Evidence.ManifestSHA256), result.Workspace.Evidence.LicenseID),
	}
	for _, finding := range result.Workspace.Findings {
		line := "  - " + string(finding.Code)
		if finding.Label != "" {
			line += ": " + finding.Label
		}
		lines = append(lines, line)
	}
	_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))
	if err != nil {
		return errors.New("write goal preflight output")
	}
	return nil
}

func rejectDuplicatePreflightFlags(args []string) error {
	known := map[string]bool{"launch-profile": true, "workspace": true, "json": true}
	seen := make(map[string]struct{}, len(known))
	for _, raw := range args {
		if raw == "--" {
			break
		}
		if !strings.HasPrefix(raw, "-") || raw == "-" {
			continue
		}
		name := strings.TrimLeft(raw, "-")
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			name = name[:idx]
		}
		if !known[name] {
			continue
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("goal preflight flag --%s may be supplied only once", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func joinedPreflightCodes(findings []workspaceguard.Finding) string {
	codes := make([]string, 0, len(findings))
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		code := string(finding.Code)
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return strings.Join(codes, ",")
}

func shortDigest(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
