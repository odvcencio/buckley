package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/workspaceguard"
)

type staticWorkspaceInspector struct {
	report workspaceguard.Report
	err    error
	root   string
}

type sequenceWorkspaceInspector struct {
	reports []workspaceguard.Report
	calls   int
}

func (s *sequenceWorkspaceInspector) Inspect(context.Context, workspaceguard.Request) (workspaceguard.Report, error) {
	if s.calls >= len(s.reports) {
		return workspaceguard.Report{}, errors.New("unexpected inspection")
	}
	report := s.reports[s.calls]
	s.calls++
	return report, nil
}

func (s *staticWorkspaceInspector) Inspect(_ context.Context, request workspaceguard.Request) (workspaceguard.Report, error) {
	s.root = request.Root
	return s.report, s.err
}

func allowedPreflightReport() workspaceguard.Report {
	return workspaceguard.Report{
		Allowed: true,
		Evidence: workspaceguard.Evidence{
			Schema:                workspaceguard.EvidenceSchema,
			RootSHA256:            strings.Repeat("d", 64),
			HEAD:                  strings.Repeat("a", 40),
			ManifestSHA256:        strings.Repeat("b", 64),
			LicenseID:             "MIT",
			LicensePath:           "LICENSE",
			LicenseSHA256:         strings.Repeat("e", 64),
			LicenseManifestSHA256: strings.Repeat("c", 64),
		},
	}
}

func defaultLaunchOperatorConfig() *config.LaunchOperatorConfig {
	return &config.LaunchOperatorConfig{}
}

func TestRunGoalPreflightWith_PassIsReadOnlyAndUsesExactProfile(t *testing.T) {
	if goalPreflightTimeout <= 0 || goalPreflightTimeout > time.Minute {
		t.Fatalf("goal preflight timeout = %s, want a bounded positive deadline", goalPreflightTimeout)
	}
	inspector := &staticWorkspaceInspector{report: allowedPreflightReport()}
	configCalls := 0
	sandboxCalls := 0
	deps := goalPreflightDeps{
		loadConfig: func(string) (*config.LaunchOperatorConfig, error) {
			configCalls++
			return defaultLaunchOperatorConfig(), nil
		},
		inspector: inspector,
		getwd:     func() (string, error) { return "/unused", nil },
		checkSandbox: func(_ context.Context, root string, _ *config.LaunchOperatorConfig, _ workspaceguard.Report) error {
			sandboxCalls++
			if root != "/workspace" {
				t.Fatalf("sandbox root = %q", root)
			}
			return nil
		},
	}
	var output bytes.Buffer
	if err := runGoalPreflightWith(context.Background(), []string{"--launch-profile", "gsxmail", "--workspace", "/workspace"}, &output, deps); err != nil {
		t.Fatalf("runGoalPreflightWith: %v", err)
	}
	if configCalls != 1 || sandboxCalls != 1 || inspector.root != "/workspace" {
		t.Fatalf("calls config=%d sandbox=%d inspector.root=%q", configCalls, sandboxCalls, inspector.root)
	}
	text := output.String()
	for _, expected := range []string{"Goal launch preflight: PASS", "profile: gsxmail", "requests=12", "input=6000000", "output=393216", "total=6393216", "request=15m0s", "turn=30m0s", "run=1h30m0s", "price: free_only", "enforced: false"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output missing %q:\n%s", expected, text)
		}
	}
}

func TestRunGoalPreflightWith_JSONProfilesExact(t *testing.T) {
	for _, tt := range []struct {
		profile  string
		requests int
		input    int64
		output   int64
		total    int64
		run      string
	}{{"gosx", 24, 12_000_000, 786_432, 12_786_432, "4h0m0s"}, {"tqwebp", 24, 12_000_000, 786_432, 12_786_432, "4h0m0s"}} {
		t.Run(tt.profile, func(t *testing.T) {
			var output bytes.Buffer
			err := runGoalPreflightWith(context.Background(), []string{"--launch-profile", tt.profile, "--workspace", "/workspace", "--json"}, &output, goalPreflightDeps{
				loadConfig:   func(string) (*config.LaunchOperatorConfig, error) { return defaultLaunchOperatorConfig(), nil },
				inspector:    &staticWorkspaceInspector{report: allowedPreflightReport()},
				checkSandbox: func(context.Context, string, *config.LaunchOperatorConfig, workspaceguard.Report) error { return nil },
			})
			if err != nil {
				t.Fatalf("runGoalPreflightWith: %v", err)
			}
			var decoded goalPreflightOutput
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			limits := decoded.Profile.Limits
			if limits.ModelRequests != tt.requests || limits.InputTokens != tt.input || limits.OutputTokens != tt.output || limits.TotalTokens != tt.total || limits.MaxOutputPerRequest != 32_768 || limits.AbsoluteRunTimeText != tt.run || limits.Enforced || limits.State != workspaceguard.LaunchStateAdmissionPending || limits.PricePolicy != workspaceguard.LaunchPricePolicyFreeOnly || limits.GlobalCapacity != 2 || limits.PerRunParallelism != 2 {
				t.Fatalf("limits = %+v", limits)
			}
		})
	}
}

func TestRunGoalPreflightWith_FailClosedReasonsAndSanitizedOutput(t *testing.T) {
	cases := []struct {
		name       string
		loadConfig func(string) (*config.LaunchOperatorConfig, error)
		sandboxErr error
		wantCode   workspaceguard.ReasonCode
	}{
		{name: "config unavailable", loadConfig: func(string) (*config.LaunchOperatorConfig, error) { return nil, errors.New("raw-config-secret") }, wantCode: workspaceguard.ReasonConfigUnavailable},
		{name: "network logging", loadConfig: func(string) (*config.LaunchOperatorConfig, error) {
			cfg := defaultLaunchOperatorConfig()
			cfg.Diagnostics.NetworkLogsEnabled = true
			return cfg, nil
		}, wantCode: workspaceguard.ReasonNetworkLogging},
		{name: "telemetry payloads", loadConfig: func(string) (*config.LaunchOperatorConfig, error) {
			cfg := defaultLaunchOperatorConfig()
			cfg.Diagnostics.TelemetryPayloadsOverNetwork = true
			return cfg, nil
		}, wantCode: workspaceguard.ReasonTelemetryPayloads},
		{name: "sandbox unavailable", loadConfig: func(string) (*config.LaunchOperatorConfig, error) { return defaultLaunchOperatorConfig(), nil }, sandboxErr: fmtLaunchUnavailable("raw-docker-secret"), wantCode: workspaceguard.ReasonSandboxUnavailable},
		{name: "sandbox policy", loadConfig: func(string) (*config.LaunchOperatorConfig, error) { return defaultLaunchOperatorConfig(), nil }, sandboxErr: errors.Join(tool.ErrLaunchSandboxUnavailable, tool.ErrLaunchSandboxPolicy, errors.New("raw-policy-secret")), wantCode: workspaceguard.ReasonSandboxPolicy},
		{name: "cleanup required", loadConfig: func(string) (*config.LaunchOperatorConfig, error) { return defaultLaunchOperatorConfig(), nil }, sandboxErr: &tool.LaunchCleanupRequiredError{Container: strings.Repeat("a", 64)}, wantCode: workspaceguard.ReasonCleanupRequired},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runGoalPreflightWith(context.Background(), []string{"--launch-profile", "gosx", "--workspace", "/workspace"}, &output, goalPreflightDeps{
				loadConfig: tt.loadConfig,
				inspector:  &staticWorkspaceInspector{report: allowedPreflightReport()},
				checkSandbox: func(context.Context, string, *config.LaunchOperatorConfig, workspaceguard.Report) error {
					return tt.sandboxErr
				},
			})
			if !errors.Is(err, errGoalPreflightBlocked) {
				t.Fatalf("error = %v, want blocked", err)
			}
			if !strings.Contains(output.String(), string(tt.wantCode)) {
				t.Fatalf("output missing %q: %s", tt.wantCode, output.String())
			}
			for _, secret := range []string{"raw-config-secret", "raw-docker-secret", "raw-policy-secret"} {
				if strings.Contains(output.String(), secret) || strings.Contains(err.Error(), secret) {
					t.Fatalf("raw error leaked %q: output=%q err=%v", secret, output.String(), err)
				}
			}
		})
	}
}

type preflightCloseSequence struct {
	errors []error
	calls  int
}

func (s *preflightCloseSequence) CloseContext(context.Context) error {
	s.calls++
	if s.calls <= len(s.errors) {
		return s.errors[s.calls-1]
	}
	return nil
}

func TestCloseGoalPreflightLaunch_RetriesExactCleanupOwner(t *testing.T) {
	target := strings.Repeat("b", 64)
	cleanup := &tool.LaunchCleanupRequiredError{Container: target}
	closer := &preflightCloseSequence{errors: []error{cleanup, cleanup}}
	if err := closeGoalPreflightLaunch(closer); err != nil {
		t.Fatalf("closeGoalPreflightLaunch: %v", err)
	}
	if closer.calls != 3 {
		t.Fatalf("cleanup calls = %d, want 3 exact-identity attempts", closer.calls)
	}
}

func TestReconcileGoalPreflightAdmissionError_DropsResolvedCleanupCondition(t *testing.T) {
	target := strings.Repeat("c", 64)
	cleanup := &tool.LaunchCleanupRequiredError{Container: target}
	closer := &preflightCloseSequence{errors: []error{cleanup}}
	err := reconcileGoalPreflightAdmissionError(closer, errors.Join(tool.ErrLaunchSandboxUnavailable, cleanup))
	if !errors.Is(err, tool.ErrLaunchSandboxUnavailable) || errors.Is(err, tool.ErrLaunchCleanupRequired) {
		t.Fatalf("reconciled admission error = %v", err)
	}
	if closer.calls != 2 {
		t.Fatalf("cleanup calls = %d, want retry through confirmed removal", closer.calls)
	}
}

func TestReconcileGoalPreflightAdmissionError_DropsStaleCleanupWhenRootCloseFails(t *testing.T) {
	target := strings.Repeat("d", 64)
	cleanup := &tool.LaunchCleanupRequiredError{Container: target}
	rootCloseErr := errors.New("bounded root close failed")
	closer := &preflightCloseSequence{errors: []error{rootCloseErr}}
	err := reconcileGoalPreflightAdmissionError(closer, errors.Join(tool.ErrLaunchSandboxUnavailable, cleanup))
	if !errors.Is(err, tool.ErrLaunchSandboxUnavailable) || !errors.Is(err, rootCloseErr) || errors.Is(err, tool.ErrLaunchCleanupRequired) {
		t.Fatalf("reconciled root-close error = %v", err)
	}
	if closer.calls != 1 {
		t.Fatalf("cleanup calls = %d, want one nonretryable root-close result", closer.calls)
	}
}

func TestRunGoalPreflightWith_StrictFlagsAndNoDependenciesOnRejectedInput(t *testing.T) {
	calls := 0
	deps := goalPreflightDeps{
		loadConfig: func(string) (*config.LaunchOperatorConfig, error) { calls++; return defaultLaunchOperatorConfig(), nil },
		inspector:  &staticWorkspaceInspector{report: allowedPreflightReport()},
		checkSandbox: func(context.Context, string, *config.LaunchOperatorConfig, workspaceguard.Report) error {
			calls++
			return nil
		},
	}
	for _, args := range [][]string{
		{"--launch-profile", "unknown"},
		{"--launch-profile", "gosx", "--launch-profile=gosx"},
		{"--launch-profile", "gosx", "--workspace", "/a", "--workspace=/b"},
		{"--launch-profile", "gosx", "--wat"},
		{"--launch-profile", "gosx", "positional"},
	} {
		if err := runGoalPreflightWith(context.Background(), args, &bytes.Buffer{}, deps); err == nil {
			t.Fatalf("args %q unexpectedly succeeded", args)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid inputs reached config/sandbox dependencies %d time(s)", calls)
	}
}

func TestRunGoalPreflightWith_BlockedWorkspaceDoesNotReadConfigOrProbeSandbox(t *testing.T) {
	calls := 0
	report := allowedPreflightReport()
	report.Allowed = false
	report.Findings = []workspaceguard.Finding{{Code: workspaceguard.ReasonSymlink, Label: ".buckley/config.yaml"}}
	var output bytes.Buffer
	err := runGoalPreflightWith(context.Background(), []string{"--launch-profile", "gosx", "--workspace", "/workspace"}, &output, goalPreflightDeps{
		loadConfig: func(string) (*config.LaunchOperatorConfig, error) {
			calls++
			return nil, errors.New("must not read")
		},
		inspector: &staticWorkspaceInspector{report: report},
		checkSandbox: func(context.Context, string, *config.LaunchOperatorConfig, workspaceguard.Report) error {
			calls++
			return errors.New("must not probe")
		},
	})
	if !errors.Is(err, errGoalPreflightBlocked) {
		t.Fatalf("error = %v, want blocked", err)
	}
	if calls != 0 {
		t.Fatalf("unsafe workspace reached config/sandbox dependencies %d time(s)", calls)
	}
	if !strings.Contains(output.String(), string(workspaceguard.ReasonSymlink)) {
		t.Fatalf("output missing reason: %s", output.String())
	}
}

func TestRunGoalPreflightWith_RejectsHostileInspectorProjection(t *testing.T) {
	report := allowedPreflightReport()
	report.Evidence.TrackedFiles = workspaceguard.MaxWorkspaceEntries
	report.Evidence.UntrackedFiles = 1
	calls := 0
	err := runGoalPreflightWith(context.Background(), []string{"--launch-profile", "gosx", "--workspace", "/workspace"}, &bytes.Buffer{}, goalPreflightDeps{
		loadConfig: func(string) (*config.LaunchOperatorConfig, error) {
			calls++
			return defaultLaunchOperatorConfig(), nil
		},
		inspector: &staticWorkspaceInspector{report: report},
		checkSandbox: func(context.Context, string, *config.LaunchOperatorConfig, workspaceguard.Report) error {
			calls++
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "workspace observation failed") {
		t.Fatalf("hostile report error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("hostile report reached dependencies %d time(s)", calls)
	}
}

func TestRunGoalPreflightWith_WorkspaceDriftAfterAdmissionBlocks(t *testing.T) {
	initial := allowedPreflightReport()
	changed := initial
	changed.Evidence.ManifestSHA256 = strings.Repeat("e", 64)
	inspector := &sequenceWorkspaceInspector{reports: []workspaceguard.Report{initial, changed}}
	var output bytes.Buffer
	err := runGoalPreflightWith(context.Background(), []string{"--launch-profile", "gosx", "--workspace", "/workspace"}, &output, goalPreflightDeps{
		loadConfig:   func(string) (*config.LaunchOperatorConfig, error) { return defaultLaunchOperatorConfig(), nil },
		inspector:    inspector,
		checkSandbox: func(context.Context, string, *config.LaunchOperatorConfig, workspaceguard.Report) error { return nil },
	})
	if !errors.Is(err, errGoalPreflightBlocked) {
		t.Fatalf("error = %v, want blocked", err)
	}
	if inspector.calls != 2 || !strings.Contains(output.String(), string(workspaceguard.ReasonWorkspaceChanged)) {
		t.Fatalf("drift not reported after %d calls: %s", inspector.calls, output.String())
	}
}

func TestRunGoalCommand_PreflightRejectsUnknownBeforeSideEffects(t *testing.T) {
	if err := runGoalCommand([]string{"preflight", "--launch-profile", "not-a-profile"}); err == nil {
		t.Fatal("unknown profile unexpectedly succeeded")
	}
}

func fmtLaunchUnavailable(detail string) error {
	return errors.Join(tool.ErrLaunchSandboxUnavailable, errors.New(detail))
}
