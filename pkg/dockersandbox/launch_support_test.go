package dockersandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

type launchDockerCall struct {
	name string
	args []string
}

const (
	testLaunchProbe      = "/usr/local/bin/buckley-launch-probe-v1"
	testLaunchSupervisor = "/usr/local/bin/buckley-launch-supervisor-v1"
	testSupervisorToken  = "buckley-launch-supervisor-v1"
)

type launchDockerRunner struct {
	mu      sync.Mutex
	calls   []launchDockerCall
	handler func(context.Context, string, []string) (string, string, error)
}

func (r *launchDockerRunner) run(ctx context.Context, name string, args ...string) (string, string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, launchDockerCall{name: name, args: append([]string(nil), args...)})
	handler := r.handler
	r.mu.Unlock()
	if handler != nil {
		return handler(ctx, name, args)
	}
	return "", "", nil
}

func (r *launchDockerRunner) snapshot() []launchDockerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := make([]launchDockerCall, len(r.calls))
	copy(calls, r.calls)
	return calls
}

func argPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func launchCreateOwnership(args []string) (string, string) {
	var name, owner string
	for idx := 0; idx+1 < len(args); idx++ {
		switch args[idx] {
		case "--name":
			name = args[idx+1]
		case "--label":
			owner = strings.TrimPrefix(args[idx+1], launchOwnerLabel+"=")
		}
	}
	return name, owner
}

func launchOwnershipOutput(id, imageID, imageRef, owner string) string {
	return strings.Join([]string{id, imageID, imageRef, owner}, "|") + "\n"
}

func TestBuildCreateArgs_StrictLaunchIsolation(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	network := false
	cfg := config.DockerSandboxConfig{
		Image:             "ubuntu:24.04",
		WorkspaceMount:    "/workspace",
		ContainerUser:     "1234:5678",
		ReadOnlyRoot:      true,
		EphemeralHome:     true,
		HideGitMetadata:   true,
		StrictCleanup:     true,
		NeverPull:         true,
		Entrypoint:        "/bin/sleep",
		IsolatedClientEnv: true,
		NetworkEnabled:    &network,
		Resources: config.ResourceLimitsConfig{
			CPUs:      "2.0",
			Memory:    "1g",
			PidsLimit: 300,
			TmpfsSize: "128m",
		},
	}
	args := buildCreateArgs(cfg, workspace, "strict-launch")

	checks := [][2]string{
		{"--pull", "never"},
		{"--entrypoint", "/bin/sleep"},
		{"--read-only", ""},
		{"--mount", "type=bind,source=" + workspace + ",destination=/workspace"},
		{"--tmpfs", "/tmp:size=128m"},
		{"--tmpfs", "/buckley-home:size=128m,mode=0700,uid=1234,gid=5678"},
		{"--env", "HOME=/buckley-home"},
		{"--env", "TMPDIR=/tmp"},
		{"--env", "TMP=/tmp"},
		{"--env", "TEMP=/tmp"},
		{"--mount", "type=tmpfs,destination=/workspace/.git,tmpfs-mode=000"},
		{"--network", "none"},
		{"--cpus", "2.0"},
		{"--memory", "1g"},
		{"--pids-limit", "300"},
		{"--user", "1234:5678"},
	}
	for _, check := range checks {
		t.Run(check[0]+"="+check[1], func(t *testing.T) {
			if check[0] == "--read-only" {
				if !containsArg(args, "--read-only") {
					t.Errorf("args = %v", args)
				}
				return
			}
			if !argPair(args, check[0], check[1]) {
				t.Errorf("args missing %q %q: %v", check[0], check[1], args)
			}
		})
	}
	if !containsArg(args, "ubuntu:24.04") || !containsArg(args, "infinity") {
		t.Fatalf("args missing image/entrypoint: %v", args)
	}
	for idx, arg := range args {
		if arg == "sleep" && (idx == 0 || args[idx-1] != "--entrypoint") {
			t.Fatalf("strict image command duplicated sleep: %v", args)
		}
	}
}

func TestBuildCreateArgs_HidesMissingGitWithReadOnlyBind(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DockerSandboxConfig{HideGitMetadata: true}
	args := buildCreateArgs(cfg, workspace, "missing-git")
	want := "type=bind,source=/dev/null,destination=/workspace/.git,readonly"
	if !argPair(args, "--mount", want) {
		t.Fatalf("args missing missing-.git overlay %q: %v", want, args)
	}
}

func TestBuildCreateArgs_GenericSandboxDoesNotForceNeverPull(t *testing.T) {
	args := buildCreateArgs(config.DockerSandboxConfig{Image: "ubuntu:24.04"}, "", "generic")
	if containsArg(args, "--pull") {
		t.Fatalf("generic sandbox unexpectedly changed pull policy: %v", args)
	}
}

func TestDockerSandbox_ImageReadyUsesLocalInspectOnly(t *testing.T) {
	runner := &launchDockerRunner{}
	runner.handler = func(_ context.Context, name string, args []string) (string, string, error) {
		if name != "docker" || len(args) != 3 || args[0] != "image" || args[1] != "inspect" || args[2] != "ubuntu:24.04" {
			return "", "", fmt.Errorf("unexpected command %s %v", name, args)
		}
		return "image id\n", "", nil
	}
	sandbox := New(config.DockerSandboxConfig{Image: "ubuntu:24.04"}, WithCommandRunner(runner.run))
	if err := sandbox.ImageReady(context.Background()); err != nil {
		t.Fatalf("ImageReady returned error: %v", err)
	}
	calls := runner.snapshot()
	if len(calls) != 1 || calls[0].args[0] != "image" {
		t.Fatalf("ImageReady calls = %#v, want one image inspect", calls)
	}
	for _, call := range calls {
		if call.name == "docker" && len(call.args) > 0 && call.args[0] == "pull" {
			t.Fatal("ImageReady attempted a network pull")
		}
	}

	runner = &launchDockerRunner{handler: func(_ context.Context, _ string, _ []string) (string, string, error) {
		return "", "not found", errors.New("exit status 1")
	}}
	sandbox = New(config.DockerSandboxConfig{Image: "ubuntu:24.04"}, WithCommandRunner(runner.run))
	if err := sandbox.ImageReady(context.Background()); err == nil {
		t.Fatal("ImageReady accepted a missing local image")
	}
}

func TestDockerSandbox_InspectImageReturnsBoundedIdentity(t *testing.T) {
	want := ImageIdentity{
		ID:           "sha256:" + strings.Repeat("b", 64),
		RepoDigests:  []string{"m31labs/worker@sha256:" + strings.Repeat("a", 64)},
		OS:           "linux",
		Architecture: "amd64",
		Labels:       map[string]string{"dev.m31labs.buckley.launch.contract": "worker-v1"},
		Env:          []string{"GOPROXY=off"},
		Entrypoint:   []string{"/bin/sleep"},
		Cmd:          []string{"infinity"},
	}
	raw := []map[string]any{{
		"Id": want.ID, "RepoDigests": want.RepoDigests, "Os": want.OS, "Architecture": want.Architecture,
		"Config": map[string]any{"Labels": want.Labels, "Env": want.Env, "Entrypoint": want.Entrypoint, "Cmd": want.Cmd},
	}}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	runner := &launchDockerRunner{handler: func(_ context.Context, _ string, args []string) (string, string, error) {
		if fmt.Sprint(args) != fmt.Sprint([]string{"image", "inspect", want.RepoDigests[0]}) {
			return "", "", fmt.Errorf("unexpected args %v", args)
		}
		return string(encoded), "", nil
	}}
	sandbox := New(config.DockerSandboxConfig{Image: want.RepoDigests[0], MaxOutputBytes: 1 << 20}, WithCommandRunner(runner.run))
	got, err := sandbox.InspectImage(context.Background())
	if err != nil {
		t.Fatalf("InspectImage: %v", err)
	}
	if got.ID != want.ID || got.OS != want.OS || got.Architecture != want.Architecture || fmt.Sprint(got.RepoDigests) != fmt.Sprint(want.RepoDigests) || got.Labels["dev.m31labs.buckley.launch.contract"] != "worker-v1" || fmt.Sprint(got.Env) != fmt.Sprint(want.Env) || fmt.Sprint(got.Entrypoint) != fmt.Sprint(want.Entrypoint) || fmt.Sprint(got.Cmd) != fmt.Sprint(want.Cmd) {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
	for _, raw := range []string{"{}", "[]", "[{},{}]", "not-json", string(encoded) + outputTruncatedMarker} {
		runner := &launchDockerRunner{handler: func(context.Context, string, []string) (string, string, error) { return raw, "", nil }}
		sandbox := New(config.DockerSandboxConfig{Image: want.RepoDigests[0]}, WithCommandRunner(runner.run))
		if _, err := sandbox.InspectImage(context.Background()); err == nil {
			t.Fatalf("InspectImage accepted malformed identity %q", raw)
		}
	}
}

func TestDockerSandbox_LaunchAdmissionPinsOneContainerAndNeverRecreates(t *testing.T) {
	containerID := strings.Repeat("c", 64)
	imageID := "sha256:" + strings.Repeat("b", 64)
	workspaceIdentity := "2049:12345"
	running := true
	createCalls := 0
	startCalls := 0
	runner := &launchDockerRunner{handler: func(ctx context.Context, _ string, args []string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "create":
			createCalls++
			return containerID + "\n", "", nil
		case len(args) > 0 && args[0] == "start":
			startCalls++
			return "", "", nil
		case len(args) == 5 && args[0] == "inspect" && args[2] == "{{.State.Running}}":
			if running {
				return "true\n", "", nil
			}
			return "false\n", "", nil
		case len(args) == 5 && args[0] == "inspect" && args[2] == "{{.Image}}":
			return imageID + "\n", "", nil
		case len(args) == 5 && args[0] == "exec" && args[3] == testLaunchProbe:
			return workspaceIdentity + "\n", "", nil
		case len(args) == 5 && args[0] == "exec" && args[3] == testLaunchSupervisor && args[4] == "--self-test":
			return testSupervisorToken + "\n", "", nil
		case len(args) > 0 && args[0] == "exec":
			return "ok", "", nil
		case len(args) > 0 && args[0] == "rm":
			return "", "", nil
		default:
			return "", "", fmt.Errorf("unexpected docker args %v", args)
		}
	}}
	sandbox := New(config.DockerSandboxConfig{Image: "m31labs/worker@sha256:" + strings.Repeat("a", 64), StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
	if err := sandbox.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := sandbox.VerifyPrepared(context.Background(), PreparedVerification{ImageID: imageID, WorkspaceIdentity: workspaceIdentity, ProbePath: testLaunchProbe, SupervisorPath: testLaunchSupervisor, SupervisorToken: testSupervisorToken}); err != nil {
		t.Fatalf("VerifyPrepared: %v", err)
	}
	if _, err := sandbox.Execute(context.Background(), Request{Command: "true", Timeout: time.Second}); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	foundSupervisedExec := false
	for _, call := range runner.snapshot() {
		for idx, arg := range call.args {
			if arg == testLaunchSupervisor && idx+4 < len(call.args) && call.args[idx+1] == "--" && call.args[idx+2] == "/bin/sh" && call.args[idx+3] == "-c" && call.args[idx+4] == "true" {
				foundSupervisedExec = true
			}
		}
	}
	if !foundSupervisedExec {
		t.Fatalf("launch command did not use the trusted supervisor: %#v", runner.snapshot())
	}
	running = false
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := sandbox.Execute(context.Background(), Request{Command: "true", Timeout: time.Second}); err == nil {
			t.Fatalf("dead prepared container execute %d unexpectedly succeeded", attempt+1)
		}
	}
	if createCalls != 1 || startCalls != 1 {
		t.Fatalf("container lifecycle create=%d start=%d, want exactly 1/1", createCalls, startCalls)
	}
}

func TestDockerSandbox_LaunchSupervisorFailurePoisonsAndRemovesContainer(t *testing.T) {
	containerID := strings.Repeat("c", 64)
	var removed bool
	runner := &launchDockerRunner{handler: func(_ context.Context, _ string, args []string) (string, string, error) {
		switch {
		case len(args) == 5 && args[0] == "inspect" && args[2] == "{{.State.Running}}":
			return "true\n", "", nil
		case len(args) > 0 && args[0] == "exec":
			err := exec.Command("/bin/sh", "-c", "exit 125").Run()
			return "", "", err
		case len(args) == 4 && args[0] == "rm" && args[1] == "-f" && args[3] == containerID:
			removed = true
			return "", "", nil
		default:
			return "", "", fmt.Errorf("unexpected docker args %v", args)
		}
	}}
	sandbox := New(config.DockerSandboxConfig{Image: "m31labs/worker@sha256:" + strings.Repeat("a", 64), StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
	sandbox.containerID = containerID
	sandbox.preparedID = containerID
	sandbox.admitted = true
	sandbox.launchSupervisor = testLaunchSupervisor
	result, err := sandbox.Execute(context.Background(), Request{Command: "true", Timeout: time.Second})
	if err == nil || result == nil || result.ExitCode != launchSupervisorFailureExitCode {
		t.Fatalf("supervisor failure = result=%+v err=%v", result, err)
	}
	if !removed || sandbox.containerID != "" || sandbox.admitted {
		t.Fatalf("terminal supervisor cleanup removed=%v id=%q admitted=%v", removed, sandbox.containerID, sandbox.admitted)
	}
}

func TestDockerSandbox_VerifyPreparedRejectsImageOrWorkspaceMismatch(t *testing.T) {
	for _, tt := range []struct {
		name      string
		imageOut  string
		probeOut  string
		wantImage string
		wantRoot  string
	}{
		{name: "image", imageOut: "sha256:" + strings.Repeat("c", 64), probeOut: "1:2", wantImage: "sha256:" + strings.Repeat("b", 64), wantRoot: "1:2"},
		{name: "workspace", imageOut: "sha256:" + strings.Repeat("b", 64), probeOut: "9:9", wantImage: "sha256:" + strings.Repeat("b", 64), wantRoot: "1:2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			containerID := strings.Repeat("d", 64)
			execCalls := 0
			runner := &launchDockerRunner{handler: func(_ context.Context, _ string, args []string) (string, string, error) {
				switch {
				case len(args) > 0 && args[0] == "create":
					return containerID, "", nil
				case len(args) > 0 && args[0] == "start":
					return "", "", nil
				case len(args) > 2 && args[0] == "inspect" && args[2] == "{{.State.Running}}":
					return "true", "", nil
				case len(args) > 2 && args[0] == "inspect" && args[2] == "{{.Image}}":
					return tt.imageOut, "", nil
				case len(args) > 3 && args[0] == "exec" && args[3] == testLaunchProbe:
					return tt.probeOut, "", nil
				case len(args) > 3 && args[0] == "exec" && args[3] == testLaunchSupervisor:
					return testSupervisorToken, "", nil
				case len(args) > 0 && args[0] == "exec":
					execCalls++
					return "", "", nil
				case len(args) > 0 && args[0] == "rm":
					return "", "", nil
				default:
					return "", "", fmt.Errorf("unexpected args %v", args)
				}
			}}
			sandbox := New(config.DockerSandboxConfig{Image: "m31labs/worker@sha256:" + strings.Repeat("a", 64), StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
			if err := sandbox.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := sandbox.VerifyPrepared(context.Background(), PreparedVerification{ImageID: tt.wantImage, WorkspaceIdentity: tt.wantRoot, ProbePath: testLaunchProbe, SupervisorPath: testLaunchSupervisor, SupervisorToken: testSupervisorToken}); err == nil {
				t.Fatal("mismatched prepared container was admitted")
			}
			if _, err := sandbox.Execute(context.Background(), Request{Command: "true", Timeout: time.Second}); err == nil {
				t.Fatal("unadmitted prepared container executed")
			}
			if execCalls != 0 {
				t.Fatalf("unadmitted command exec calls = %d", execCalls)
			}
			if err := sandbox.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDockerSandbox_RequestTimeoutCoversContainerEnsure(t *testing.T) {
	runner := &launchDockerRunner{handler: func(ctx context.Context, _ string, args []string) (string, string, error) {
		if len(args) > 0 && args[0] == "create" {
			<-ctx.Done()
			return "", "", ctx.Err()
		}
		return "", "", nil
	}}
	sandbox := New(config.DockerSandboxConfig{Image: "ubuntu:24.04"}, WithCommandRunner(runner.run))
	started := time.Now()
	if _, err := sandbox.Execute(context.Background(), Request{Command: "true", Timeout: 25 * time.Millisecond}); err == nil {
		t.Fatal("timed out container ensure unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("container ensure ignored request timeout: %s", elapsed)
	}
}

func TestDockerSandbox_LaunchInvalidContainerIDCleansOnlyOwnedContainer(t *testing.T) {
	imageRef := "m31labs/worker@sha256:" + strings.Repeat("a", 64)
	containerID := strings.Repeat("b", 64)
	var createdName, owner string
	var removed string
	runner := &launchDockerRunner{handler: func(_ context.Context, _ string, args []string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "create":
			createdName, owner = launchCreateOwnership(args)
			return "--attacker-output\n", "", nil
		case len(args) > 0 && args[0] == "inspect" && args[len(args)-1] == createdName:
			return launchOwnershipOutput(containerID, "", imageRef, owner), "", nil
		case len(args) == 4 && args[0] == "rm" && args[2] == "--":
			removed = args[3]
			return "", "", nil
		default:
			return "", "", nil
		}
	}}
	sandbox := New(config.DockerSandboxConfig{Image: imageRef, StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
	if err := sandbox.Prepare(context.Background()); err == nil {
		t.Fatal("invalid container ID unexpectedly admitted")
	}
	if createdName == "" || len(owner) != 32 || removed != containerID {
		t.Fatalf("cleanup target = %q, created name/owner = %q/%q", removed, createdName, owner)
	}
}

func TestDockerSandbox_LaunchCreateNameCollisionNeverDeletesUnownedContainer(t *testing.T) {
	imageRef := "m31labs/worker@sha256:" + strings.Repeat("a", 64)
	containerID := strings.Repeat("c", 64)
	var createdName string
	rmCalls := 0
	runner := &launchDockerRunner{handler: func(_ context.Context, _ string, args []string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "create":
			createdName, _ = launchCreateOwnership(args)
			return "", "name already in use", errors.New("create failed")
		case len(args) > 0 && args[0] == "inspect" && args[len(args)-1] == createdName:
			return launchOwnershipOutput(containerID, "", imageRef, strings.Repeat("0", 32)), "", nil
		case len(args) > 0 && args[0] == "rm":
			rmCalls++
			return "", "", nil
		default:
			return "", "", fmt.Errorf("unexpected args %v", args)
		}
	}}
	sandbox := New(config.DockerSandboxConfig{Image: imageRef, StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
	if err := sandbox.Prepare(context.Background()); err == nil {
		t.Fatal("name collision unexpectedly prepared")
	}
	if err := sandbox.Close(); err != nil {
		t.Fatalf("Close after unowned collision: %v", err)
	}
	if rmCalls != 0 || sandbox.containerName != "" || sandbox.containerID != "" {
		t.Fatalf("unowned collision cleanup rm=%d name=%q id=%q", rmCalls, sandbox.containerName, sandbox.containerID)
	}
}

func TestDockerSandbox_LaunchLostRemoveAckReconcilesAbsentContainer(t *testing.T) {
	containerID := strings.Repeat("d", 64)
	rmCalls := 0
	runner := &launchDockerRunner{handler: func(_ context.Context, _ string, args []string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "create":
			return containerID, "", nil
		case len(args) > 0 && args[0] == "start":
			return "", "", nil
		case len(args) > 0 && args[0] == "rm":
			rmCalls++
			return "", "lost acknowledgement", errors.New("connection closed")
		case len(args) > 1 && args[0] == "container" && args[1] == "ls":
			return "", "", nil
		default:
			return "", "", fmt.Errorf("unexpected args %v", args)
		}
	}}
	sandbox := New(config.DockerSandboxConfig{Image: "m31labs/worker@sha256:" + strings.Repeat("a", 64), StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
	if err := sandbox.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Close(); err != nil {
		t.Fatalf("lost remove acknowledgement: %v", err)
	}
	if err := sandbox.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	if rmCalls != 1 || sandbox.containerID != "" || sandbox.containerName != "" {
		t.Fatalf("lost acknowledgement cleanup rm=%d id=%q name=%q", rmCalls, sandbox.containerID, sandbox.containerName)
	}
}

func TestDockerSandbox_LaunchStartFailureSuccessfulCleanupClearsOwnership(t *testing.T) {
	containerID := strings.Repeat("e", 64)
	rmCalls := 0
	runner := &launchDockerRunner{handler: func(_ context.Context, _ string, args []string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "create":
			return containerID, "", nil
		case len(args) > 0 && args[0] == "start":
			return "", "start failed", errors.New("start failed")
		case len(args) > 0 && args[0] == "rm":
			rmCalls++
			return "", "", nil
		default:
			return "", "", nil
		}
	}}
	sandbox := New(config.DockerSandboxConfig{Image: "m31labs/worker@sha256:" + strings.Repeat("a", 64), StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
	if err := sandbox.Prepare(context.Background()); err == nil {
		t.Fatal("failed start unexpectedly prepared")
	}
	if sandbox.containerID != "" || sandbox.containerName != "" || sandbox.preparedID != "" {
		t.Fatalf("successful start cleanup retained ownership: id=%q name=%q prepared=%q", sandbox.containerID, sandbox.containerName, sandbox.preparedID)
	}
	if err := sandbox.Close(); err != nil {
		t.Fatal(err)
	}
	if rmCalls != 1 {
		t.Fatalf("removed start-failed container %d times, want exactly once", rmCalls)
	}
}

func TestDockerSandbox_LaunchCloseReturnsSafeRetryableCleanupIdentity(t *testing.T) {
	containerID := strings.Repeat("f", 64)
	rmCalls := 0
	runner := &launchDockerRunner{handler: func(_ context.Context, _ string, args []string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "create":
			return containerID, "", nil
		case len(args) > 0 && args[0] == "start":
			return "", "", nil
		case len(args) > 0 && args[0] == "rm":
			rmCalls++
			if rmCalls == 1 {
				return "", "private daemon detail", errors.New("private daemon detail")
			}
			return "", "", nil
		case len(args) > 1 && args[0] == "container" && args[1] == "ls":
			return containerID + "\n", "", nil
		default:
			return "", "", nil
		}
	}}
	sandbox := New(config.DockerSandboxConfig{Image: "m31labs/worker@sha256:" + strings.Repeat("a", 64), StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
	if err := sandbox.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := sandbox.Close()
	var cleanup *CleanupRequiredError
	if !errors.As(err, &cleanup) || cleanup.Container != containerID || strings.Contains(err.Error(), "private daemon detail") {
		t.Fatalf("safe cleanup error = %#v, %v", cleanup, err)
	}
	if err := sandbox.Close(); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
}

func TestLaunchContainerID_RequiresExactLowercaseHex(t *testing.T) {
	if !dockerContainerIDPattern.MatchString(strings.Repeat("a", 64)) {
		t.Fatal("canonical container ID was rejected")
	}
	for _, value := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64), "--attacker"} {
		if dockerContainerIDPattern.MatchString(value) {
			t.Fatalf("invalid container ID %q was accepted", value)
		}
	}
}

func TestDockerSandbox_LaunchTimeoutPoisonsBeforeFailedCleanup(t *testing.T) {
	containerID := strings.Repeat("d", 64)
	imageID := "sha256:" + strings.Repeat("e", 64)
	createCalls := 0
	execCalls := 0
	rmCalls := 0
	runner := &launchDockerRunner{handler: func(ctx context.Context, _ string, args []string) (string, string, error) {
		switch {
		case len(args) > 0 && args[0] == "create":
			createCalls++
			return containerID + "\n", "", nil
		case len(args) > 0 && args[0] == "start":
			return "", "", nil
		case len(args) > 0 && args[0] == "inspect" && args[2] == "{{.State.Running}}":
			return "true\n", "", nil
		case len(args) > 0 && args[0] == "inspect" && args[2] == "{{.Image}}":
			return imageID + "\n", "", nil
		case len(args) > 3 && args[0] == "exec" && args[3] == testLaunchProbe:
			return "1:2\n", "", nil
		case len(args) > 3 && args[0] == "exec" && args[3] == testLaunchSupervisor:
			return testSupervisorToken + "\n", "", nil
		case len(args) > 0 && args[0] == "exec":
			execCalls++
			<-ctx.Done()
			return "", "", ctx.Err()
		case len(args) > 0 && args[0] == "rm":
			rmCalls++
			if rmCalls == 1 {
				return "", "", errors.New("cleanup failed")
			}
			return "", "", nil
		default:
			return "", "", fmt.Errorf("unexpected args %v", args)
		}
	}}
	sandbox := New(config.DockerSandboxConfig{Image: "m31labs/worker@sha256:" + strings.Repeat("a", 64), StrictCleanup: true}, WithCommandRunner(runner.run), WithLaunchAdmission())
	if err := sandbox.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.VerifyPrepared(context.Background(), PreparedVerification{ImageID: imageID, WorkspaceIdentity: "1:2", ProbePath: testLaunchProbe, SupervisorPath: testLaunchSupervisor, SupervisorToken: testSupervisorToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.Execute(context.Background(), Request{Command: "sleep", Timeout: 20 * time.Millisecond}); err == nil {
		t.Fatal("timeout cleanup failure was suppressed")
	}
	if _, err := sandbox.Execute(context.Background(), Request{Command: "true", Timeout: time.Second}); err == nil {
		t.Fatal("poisoned launch container executed again")
	}
	if createCalls != 1 || execCalls != 1 {
		t.Fatalf("lifecycle create=%d exec=%d, want 1/1", createCalls, execCalls)
	}
	if err := sandbox.Close(); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
}

func TestBoundedCommandRunner_IsolatesStrictDockerEnvironment(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://attacker.invalid:2375")
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("HOST_SECRET_SENTINEL", "must-not-pass")
	stdout, stderr, err := boundedCommandRunner(4096, true)(context.Background(), "/usr/bin/env")
	if err != nil {
		t.Fatalf("boundedCommandRunner: %v (%s)", err, stderr)
	}
	for _, want := range []string{"DOCKER_HOST=unix:///var/run/docker.sock", "DOCKER_CONFIG=/nonexistent", "HOME=/nonexistent", "PATH=/usr/bin:/bin", "LC_ALL=C"} {
		if !strings.Contains(stdout, want+"\n") {
			t.Fatalf("isolated environment missing %q: %s", want, stdout)
		}
	}
	for _, forbidden := range []string{"attacker.invalid", "HOST_SECRET_SENTINEL", "must-not-pass"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("isolated environment leaked %q: %s", forbidden, stdout)
		}
	}
}

func TestDockerSandbox_OutputTruncation(t *testing.T) {
	runner := &launchDockerRunner{}
	runner.handler = func(ctx context.Context, name string, args []string) (string, string, error) {
		switch {
		case name != "docker":
			return "", "", fmt.Errorf("unexpected executable %q", name)
		case len(args) > 0 && args[0] == "create":
			return "container-id\n", "", nil
		case len(args) > 0 && args[0] == "start":
			return "", "", nil
		case len(args) > 0 && args[0] == "exec":
			return strings.Repeat("o", 200), strings.Repeat("e", 200), nil
		default:
			return "", "", nil
		}
	}
	sandbox := New(config.DockerSandboxConfig{
		Image:          "ubuntu:24.04",
		WorkspaceMount: "/workspace",
		MaxOutputBytes: 64,
	}, WithCommandRunner(runner.run), WithWorkspacePath(t.TempDir()))
	result, err := sandbox.Execute(context.Background(), Request{Command: "printf output"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || !result.OutputTruncated {
		t.Fatalf("result = %#v, want OutputTruncated", result)
	}
	stdoutLimit, stderrLimit := splitOutputLimit(64)
	if len(result.Stdout) > stdoutLimit || len(result.Stderr) > stderrLimit {
		t.Fatalf("bounded output lengths = %d/%d, limits = %d/%d", len(result.Stdout), len(result.Stderr), stdoutLimit, stderrLimit)
	}
	if !strings.Contains(result.Stdout, "output truncated") || !strings.Contains(result.Stderr, "output truncated") {
		t.Fatalf("truncation marker missing: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestDockerSandbox_StrictTimeoutCleansContainer(t *testing.T) {
	runner := &launchDockerRunner{}
	runner.handler = func(ctx context.Context, name string, args []string) (string, string, error) {
		switch {
		case name == "docker" && len(args) > 0 && args[0] == "create":
			return "timeout-container\n", "", nil
		case name == "docker" && len(args) > 0 && args[0] == "start":
			return "", "", nil
		case name == "docker" && len(args) > 0 && args[0] == "exec":
			<-ctx.Done()
			return "partial", "", ctx.Err()
		case name == "docker" && len(args) > 0 && args[0] == "rm":
			return "", "", nil
		default:
			return "", "", nil
		}
	}
	sandbox := New(config.DockerSandboxConfig{
		Image:         "ubuntu:24.04",
		StrictCleanup: true,
	}, WithCommandRunner(runner.run))
	result, err := sandbox.Execute(context.Background(), Request{Command: "sleep 60", Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("strict timeout returned error: %v", err)
	}
	if result == nil || !result.Killed || result.ExitCode != 137 {
		t.Fatalf("timeout result = %#v", result)
	}
	if !hasDockerCall(runner.snapshot(), "rm", "-f", "--", "timeout-container") {
		t.Fatalf("strict timeout did not remove container: %#v", runner.snapshot())
	}
}

func TestDockerSandbox_StrictTimeoutPropagatesCleanupFailure(t *testing.T) {
	runner := &launchDockerRunner{}
	runner.handler = func(ctx context.Context, name string, args []string) (string, string, error) {
		switch {
		case name == "docker" && len(args) > 0 && args[0] == "create":
			return "timeout-container\n", "", nil
		case name == "docker" && len(args) > 0 && args[0] == "start":
			return "", "", nil
		case name == "docker" && len(args) > 0 && args[0] == "exec":
			<-ctx.Done()
			return "", "", ctx.Err()
		case name == "docker" && len(args) > 0 && args[0] == "rm":
			return "", "rm failed", errors.New("remove failed")
		default:
			return "", "", nil
		}
	}
	sandbox := New(config.DockerSandboxConfig{Image: "ubuntu:24.04", StrictCleanup: true}, WithCommandRunner(runner.run))
	result, err := sandbox.Execute(context.Background(), Request{Command: "sleep 60", Timeout: 20 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("timeout cleanup error = %v, want cleanup failure", err)
	}
	if result == nil || !result.Killed {
		t.Fatalf("timeout result = %#v, want killed result", result)
	}
}

func TestDockerSandbox_StrictClosePropagatesCleanupFailure(t *testing.T) {
	runner := &launchDockerRunner{}
	rmCalls := 0
	runner.handler = func(_ context.Context, name string, args []string) (string, string, error) {
		switch {
		case name == "docker" && len(args) > 0 && args[0] == "create":
			return "close-container\n", "", nil
		case name == "docker" && len(args) > 0 && args[0] == "start":
			return "", "", nil
		case name == "docker" && len(args) > 0 && args[0] == "exec":
			return "ok", "", nil
		case name == "docker" && len(args) > 0 && args[0] == "rm":
			rmCalls++
			if rmCalls == 1 {
				return "", "permission denied", errors.New("remove failed")
			}
			return "", "", nil
		default:
			return "", "", nil
		}
	}
	sandbox := New(config.DockerSandboxConfig{Image: "ubuntu:24.04", StrictCleanup: true}, WithCommandRunner(runner.run))
	if _, err := sandbox.Execute(context.Background(), Request{Command: "true"}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if err := sandbox.Close(); err == nil || !strings.Contains(err.Error(), "removing sandbox container") {
		t.Fatalf("Close error = %v, want cleanup failure", err)
	}
	if err := sandbox.Close(); err != nil {
		t.Fatalf("retry Close error = %v", err)
	}
	if rmCalls != 2 {
		t.Fatalf("rm calls = %d, want retry", rmCalls)
	}
}

func TestDockerSandbox_StrictStartCleanupFailureRetainsRetryableIdentity(t *testing.T) {
	runner := &launchDockerRunner{}
	rmCalls := 0
	runner.handler = func(_ context.Context, name string, args []string) (string, string, error) {
		switch {
		case name == "docker" && len(args) > 0 && args[0] == "create":
			return "start-failed-container\n", "", nil
		case name == "docker" && len(args) > 0 && args[0] == "start":
			return "", "start failed", errors.New("start failed")
		case name == "docker" && len(args) > 0 && args[0] == "rm":
			rmCalls++
			if rmCalls == 1 {
				return "", "remove failed", errors.New("remove failed")
			}
			return "", "", nil
		default:
			return "", "", nil
		}
	}
	sandbox := New(config.DockerSandboxConfig{Image: "ubuntu:24.04", StrictCleanup: true}, WithCommandRunner(runner.run))
	if _, err := sandbox.Execute(context.Background(), Request{Command: "true"}); err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("Execute error = %v, want start cleanup failure", err)
	}
	if sandbox.containerID != "start-failed-container" {
		t.Fatalf("retained container identity = %q", sandbox.containerID)
	}
	if err := sandbox.Close(); err != nil {
		t.Fatalf("retry cleanup Close: %v", err)
	}
	if rmCalls != 2 {
		t.Fatalf("rm calls = %d, want initial attempt plus retry", rmCalls)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasDockerCall(calls []launchDockerCall, want ...string) bool {
	for _, call := range calls {
		if call.name != "docker" || len(call.args) != len(want) {
			continue
		}
		match := true
		for i := range want {
			if call.args[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
