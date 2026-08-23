//go:build launch_live

package tool

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

const launchLiveOptIn = "BUCKLEY_LAUNCH_LIVE"

func TestLaunchWorkerImage_LiveSentinel(t *testing.T) {
	if os.Getenv(launchLiveOptIn) != "1" {
		t.Skip("set BUCKLEY_LAUNCH_LIVE=1 after provisioning a sealed worker image")
	}
	contract := readLiveWorkerContract(t)
	dockerConfig := t.TempDir()
	before := liveLaunchContainers(t, dockerConfig)
	workspace := newLiveMITWorkspace(t)
	hostSentinelRoot := t.TempDir()
	hostSentinel := filepath.Join(hostSentinelRoot, "host-only-secret")
	if err := os.WriteFile(hostSentinel, []byte("must-not-enter-container"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "live-sentinel-must-not-cross")
	hostHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	registry, err := NewLaunchRegistry(context.Background(), LaunchRegistryOptions{WorkspaceRoot: workspace, WorkerImage: contract})
	if err != nil {
		t.Fatalf("sealed launch image admission failed: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := registry.Close(); err != nil {
				t.Errorf("live launch cleanup: %v", err)
			}
		}
	})

	wantTools := []string{"edit_file", "list_files", "read_file", "run_shell", "run_tests", "search_files", "write_file"}
	var gotTools []string
	for _, available := range registry.List() {
		gotTools = append(gotTools, available.Name())
	}
	sort.Strings(gotTools)
	if strings.Join(gotTools, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("launch tools = %v, want %v", gotTools, wantTools)
	}

	afterAdmission := liveLaunchContainers(t, dockerConfig)
	containerID := exactAddedContainer(t, before, afterAdmission)
	assertLiveContainerPolicy(t, dockerConfig, containerID, contract)

	runLiveCommand(t, registry, "run_shell", `test ! -r /workspace/.git`)
	runLiveCommand(t, registry, "run_shell", `test ! -S /var/run/docker.sock`)
	runLiveCommand(t, registry, "run_shell", `test "$(id -u)" != 0`)
	runLiveCommand(t, registry, "run_shell", `grep -Eq '^NoNewPrivs:[[:space:]]+1$' /proc/self/status`)
	runLiveCommand(t, registry, "run_shell", `grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status`)
	runLiveCommand(t, registry, "run_shell", `test "$(ls -1 /sys/class/net | tr '\n' ' ')" = "lo "`)
	runLiveCommand(t, registry, "run_shell", `test ! -e /buckley-host-sentinel && test ! -e /root/.buckley/config.yaml && test ! -e /var/lib/buckley && test ! -e /var/lib/hyphae`)
	runLiveCommand(t, registry, "run_shell", `test ! -e `+shellQuote(hostSentinel))
	runLiveCommand(t, registry, "run_shell", `test ! -e `+shellQuote(hostHome))
	runLiveCommand(t, registry, "run_shell", `! env | grep -q '^OPENROUTER_API_KEY='`)
	runLiveCommand(t, registry, "run_shell", `test "$HOME" = /buckley-home && test "$TMPDIR" = /tmp && touch "$HOME/home-write" /tmp/tmp-write`)
	runLiveCommand(t, registry, "run_shell", `! touch /usr/local/bin/rootfs-write-test 2>/dev/null`)
	runLiveCommand(t, registry, "run_shell", `test "$(cat /sys/fs/cgroup/memory.max)" = 2147483648 && test "$(cat /sys/fs/cgroup/pids.max)" = 512 && awk '{exit !($1/$2 == 2)}' /sys/fs/cgroup/cpu.max`)
	runLiveCommand(t, registry, "run_shell", `test "$(stat -f -c %T /tmp)" = tmpfs`)
	runLiveCommand(t, registry, "run_shell", `test "$(go version | awk '{print $3}')" = go1.26.6 && test "$(tinygo version | awk '{print $3}')" = 0.41.1 && command -v git make gcc >/dev/null`)

	runLiveCommand(t, registry, "run_tests", `go test ./...`)
	runLiveCommand(t, registry, "run_shell", `tinygo build -target wasm -o /tmp/sentinel.wasm . && test -s /tmp/sentinel.wasm`)
	runLiveCommand(t, registry, "run_shell", `(sleep 600 &) ; exit 0`)
	runLiveCommand(t, registry, "run_shell", `test "$(pgrep -x sleep | wc -l)" -eq 1`)
	runLiveCanonicalModuleSentinels(t, contract)

	if output, err := liveDockerCommand(dockerConfig, "kill", "--", containerID).CombinedOutput(); err != nil {
		t.Fatalf("kill prepared sentinel container: %v (%s)", err, output)
	}
	if _, err := registry.ExecuteWithContext(context.Background(), "run_shell", map[string]any{"command": "true"}); !errors.Is(err, ErrLaunchSandboxUnavailable) {
		t.Fatalf("dead prepared container execute error = %v, want unavailable", err)
	}
	if got := liveLaunchContainers(t, dockerConfig); len(got) > len(afterAdmission) {
		t.Fatalf("dead launch container was recreated: before=%v after=%v", afterAdmission, got)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("close live launch registry: %v", err)
	}
	closed = true
	if got := liveLaunchContainers(t, dockerConfig); !sameStringSet(got, before) {
		t.Fatalf("launch container cleanup mismatch: before=%v after=%v", before, got)
	}
}

func readLiveWorkerContract(t *testing.T) config.LaunchWorkerImageConfig {
	t.Helper()
	path := os.Getenv("BUCKLEY_LAUNCH_LIVE_CONTRACT")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4096 {
		t.Fatal("BUCKLEY_LAUNCH_LIVE_CONTRACT must name a bounded regular operator-contract.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read live worker contract")
	}
	contract, err := decodeLiveWorkerContract(data)
	if err != nil {
		t.Fatal("live worker contract is invalid")
	}
	return contract
}

func decodeLiveWorkerContract(data []byte) (config.LaunchWorkerImageConfig, error) {
	var envelope struct {
		Schema              string `json:"schema"`
		Reference           string `json:"reference"`
		ImageID             string `json:"image_id"`
		OS                  string `json:"os"`
		Architecture        string `json:"architecture"`
		ModuleLockSHA256    string `json:"module_lock_sha256"`
		ToolchainLockSHA256 string `json:"toolchain_lock_sha256"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.Schema != "buckley.launch.worker-contract.v1" {
		return config.LaunchWorkerImageConfig{}, errors.New("live worker contract is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return config.LaunchWorkerImageConfig{}, errors.New("live worker contract has trailing data")
	}
	contract := config.LaunchWorkerImageConfig{Reference: envelope.Reference, ImageID: envelope.ImageID, OS: envelope.OS, Architecture: envelope.Architecture, ModuleLockSHA256: envelope.ModuleLockSHA256, ToolchainLockSHA256: envelope.ToolchainLockSHA256}
	if err := validateLaunchImageContract(contract); err != nil {
		return config.LaunchWorkerImageConfig{}, errors.New("live worker contract is invalid")
	}
	return contract, nil
}

func TestDecodeLiveWorkerContract_FailClosed(t *testing.T) {
	valid := validLaunchImageContract()
	data, err := json.Marshal(map[string]any{
		"schema": "buckley.launch.worker-contract.v1", "reference": valid.Reference, "image_id": valid.ImageID,
		"os": valid.OS, "architecture": valid.Architecture, "module_lock_sha256": valid.ModuleLockSHA256,
		"toolchain_lock_sha256": valid.ToolchainLockSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeLiveWorkerContract(data); err != nil {
		t.Fatalf("valid live contract rejected: %v", err)
	}
	for _, invalid := range [][]byte{append(append([]byte(nil), data...), []byte("\n{}")...), []byte("{}"), append(append([]byte(nil), data...), 0)} {
		if _, err := decodeLiveWorkerContract(invalid); err == nil {
			t.Fatalf("invalid live contract accepted: %q", invalid)
		}
	}
}

func newLiveMITWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"LICENSE": `MIT License

Copyright (c) 2026 Buckley live sentinel

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`,
		"go.mod":       "module example.com/buckley-live-sentinel\n\ngo 1.26.0\n",
		"main.go":      "package main\n\nfunc main() {}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestSentinel(t *testing.T) {}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runLiveGit(t, root, "init", "-q")
	runLiveGit(t, root, "add", "--", "LICENSE", "go.mod", "main.go", "main_test.go")
	runLiveGit(t, root, "-c", "user.name=Buckley Sentinel", "-c", "user.email=sentinel@example.invalid", "commit", "-qm", "sentinel")
	return root
}

func runLiveGit(t *testing.T, root string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.Command("/usr/bin/git", commandArgs...)
	cmd.Env = []string{"HOME=/nonexistent", "LC_ALL=C", "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("live git fixture: %v (%s)", err, output)
	}
}

func runLiveCommand(t *testing.T, registry *LaunchRegistry, toolName, command string) string {
	return runLiveCommandIn(t, registry, toolName, command, ".")
}

func runLiveCommandIn(t *testing.T, registry *LaunchRegistry, toolName, command, workingDirectory string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := registry.ExecuteWithContext(ctx, toolName, map[string]any{"command": command, "working_directory": workingDirectory, "timeout_seconds": int64(590)})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("%s failed: result=%+v err=%v", toolName, result, err)
	}
	stdout, _ := result.Data["stdout"].(string)
	return stdout
}

func runLiveCanonicalModuleSentinels(t *testing.T, contract config.LaunchWorkerImageConfig) {
	t.Helper()
	tests := []struct {
		name     string
		env      string
		commands []struct {
			directory string
			command   string
		}
	}{
		{name: "gsxmail", env: "BUCKLEY_LAUNCH_LIVE_GSXMAIL", commands: []struct {
			directory string
			command   string
		}{{directory: ".", command: "GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly go test -run '^$' -count=0 ./..."}}},
		{name: "gosx", env: "BUCKLEY_LAUNCH_LIVE_GOSX", commands: []struct {
			directory string
			command   string
		}{
			{directory: ".", command: "GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly go test -run '^$' -count=0 ./..."},
			{directory: "editor", command: "GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly go test -run '^$' -count=0 ./..."},
			{directory: "cmd/buildbootstrap", command: "GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly go test -run '^$' -count=0 ./..."},
			{directory: ".", command: "GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly go run ./cmd/gosx build-runtime /tmp/gosx-runtime && test -s /tmp/gosx-runtime/gosx-runtime.wasm && test -s /tmp/gosx-runtime/gosx-runtime-islands.wasm"},
		}},
		{name: "tqwebp", env: "BUCKLEY_LAUNCH_LIVE_TQWEBP", commands: []struct {
			directory string
			command   string
		}{
			{directory: ".", command: "GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly go test -run '^$' -count=0 ./..."},
			{directory: "bench/deepteams", command: "GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly go test -run '^$' -count=0 ./..."},
		}},
	}
	for _, test := range tests {
		t.Run("offline_"+test.name, func(t *testing.T) {
			source := os.Getenv(test.env)
			if source == "" {
				t.Fatalf("%s is required for canonical offline module coverage", test.env)
			}
			workspace := archiveLiveRepository(t, source)
			registry, err := NewLaunchRegistry(context.Background(), LaunchRegistryOptions{WorkspaceRoot: workspace, WorkerImage: contract})
			if err != nil {
				t.Fatalf("admit canonical %s workspace: %v", test.name, err)
			}
			closed := false
			t.Cleanup(func() {
				if !closed {
					_ = registry.Close()
				}
			})
			for _, command := range test.commands {
				runLiveCommandIn(t, registry, "run_tests", command.command, command.directory)
			}
			if err := registry.Close(); err != nil {
				t.Fatalf("close canonical %s registry: %v", test.name, err)
			}
			closed = true
		})
	}
}

func archiveLiveRepository(t *testing.T, source string) string {
	t.Helper()
	workspace := t.TempDir()
	archive, err := os.CreateTemp(t.TempDir(), "launch-source-*.tar")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := archive.Name()
	command := exec.Command("/usr/bin/git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-C", source, "archive", "--format=tar", "HEAD")
	command.Env = []string{"HOME=/nonexistent", "LC_ALL=C", "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	command.Stdout = archive
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		_ = archive.Close()
		t.Fatal("archive canonical live repository")
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(io.LimitReader(file, 2<<30))
	entries := 0
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("read canonical repository archive")
		}
		entries++
		total += header.Size
		relative := filepath.Clean(filepath.FromSlash(header.Name))
		if entries > 200_000 || total > 2<<30 || !filepath.IsLocal(relative) || relative == "." || relative == ".git" || strings.HasPrefix(filepath.ToSlash(relative), ".git/") {
			t.Fatal("canonical repository archive exceeds safe bounds")
		}
		target := filepath.Join(workspace, relative)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > 512<<20 {
				t.Fatal("canonical repository file exceeds safe bound")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o600)
			if header.FileInfo().Mode()&0o111 != 0 {
				mode = 0o700
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				t.Fatal(err)
			}
			written, copyErr := io.CopyN(output, reader, header.Size)
			closeErr := output.Close()
			if copyErr != nil || closeErr != nil || written != header.Size {
				t.Fatal("extract canonical repository file")
			}
		default:
			t.Fatal("canonical repository archive contains a link or nonregular entry")
		}
	}
	runLiveGit(t, workspace, "init", "-q")
	runLiveGit(t, workspace, "add", "-f", "--", ".")
	runLiveGit(t, workspace, "-c", "user.name=Buckley Sentinel", "-c", "user.email=sentinel@example.invalid", "commit", "-qm", "canonical source snapshot")
	return workspace
}

func liveLaunchContainers(t *testing.T, dockerConfig string) []string {
	t.Helper()
	cmd := liveDockerCommand(dockerConfig, "container", "ls", "-a", "--no-trunc", "--filter", "label=dev.m31labs.buckley.launch.owner", "--format", "{{.ID}}")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("list launch containers: %v", err)
	}
	var values []string
	for _, value := range strings.Fields(string(output)) {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func exactAddedContainer(t *testing.T, before, after []string) string {
	t.Helper()
	known := make(map[string]bool, len(before))
	for _, value := range before {
		known[value] = true
	}
	var added []string
	for _, value := range after {
		if !known[value] {
			added = append(added, value)
		}
	}
	if len(added) != 1 {
		t.Fatalf("added launch containers = %v, want exactly one", added)
	}
	return added[0]
}

func assertLiveContainerPolicy(t *testing.T, dockerConfig, containerID string, contract config.LaunchWorkerImageConfig) {
	t.Helper()
	format := `{{json .}}`
	output, err := liveDockerCommand(dockerConfig, "inspect", "-f", format, "--", containerID).Output()
	if err != nil {
		t.Fatalf("inspect live container: %v", err)
	}
	var raw struct {
		Image  string `json:"Image"`
		Config struct {
			Image string   `json:"Image"`
			User  string   `json:"User"`
			Env   []string `json:"Env"`
		} `json:"Config"`
		HostConfig struct {
			ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
			NetworkMode    string            `json:"NetworkMode"`
			NanoCPUs       int64             `json:"NanoCpus"`
			Memory         int64             `json:"Memory"`
			PidsLimit      int64             `json:"PidsLimit"`
			CapDrop        []string          `json:"CapDrop"`
			SecurityOpt    []string          `json:"SecurityOpt"`
			Tmpfs          map[string]string `json:"Tmpfs"`
		} `json:"HostConfig"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		t.Fatalf("decode live container inspection: %v", err)
	}
	if raw.Image != contract.ImageID || raw.Config.Image != contract.Reference || raw.Config.User == "" || strings.HasPrefix(raw.Config.User, "0:") {
		t.Fatalf("live container image/user mismatch: image=%q config=%q user=%q", raw.Image, raw.Config.Image, raw.Config.User)
	}
	if !raw.HostConfig.ReadonlyRootfs || raw.HostConfig.NetworkMode != "none" || raw.HostConfig.NanoCPUs != 2_000_000_000 || raw.HostConfig.Memory != 2<<30 || raw.HostConfig.PidsLimit != 512 {
		t.Fatalf("live container bounds mismatch: %+v", raw.HostConfig)
	}
	if !containsFold(raw.HostConfig.CapDrop, "ALL") || !containsFold(raw.HostConfig.SecurityOpt, "no-new-privileges") || !strings.Contains(raw.HostConfig.Tmpfs["/tmp"], "size=512m") || !strings.Contains(raw.HostConfig.Tmpfs["/buckley-home"], "size=512m") {
		t.Fatalf("live container hardening mismatch: %+v", raw.HostConfig)
	}
	for _, item := range raw.Config.Env {
		if strings.HasPrefix(item, "OPENROUTER_API_KEY=") {
			t.Fatal("host secret entered live container environment")
		}
	}
}

func liveDockerCommand(dockerConfig string, args ...string) *exec.Cmd {
	command := exec.Command(trustedDockerBinary, args...)
	command.Env = []string{
		"DOCKER_CONFIG=" + dockerConfig,
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"HOME=/nonexistent",
		"LC_ALL=C",
		"PATH=/usr/bin:/bin",
	}
	return command
}

func shellQuote(value string) string { return strconv.Quote(value) }

func sameStringSet(left, right []string) bool {
	return fmt.Sprint(left) == fmt.Sprint(right)
}
