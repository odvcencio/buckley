package config

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadForWorkspace_UsesExactProjectWithoutChangingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BUCKLEY_NETWORK_LOGS_ENABLED", "")
	t.Setenv("BUCKLEY_DISABLE_NETWORK_LOGS", "")
	userConfig := filepath.Join(home, ".buckley", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		t.Fatalf("MkdirAll user config: %v", err)
	}
	if err := os.WriteFile(userConfig, []byte("diagnostics:\n  network_logs_enabled: true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile user config: %v", err)
	}

	current := t.TempDir()
	if err := os.Mkdir(filepath.Join(current, ".buckley"), 0o700); err != nil {
		t.Fatalf("Mkdir current config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(current, ".buckley", "config.yaml"), []byte("diagnostics:\n  telemetry_payloads_over_network: false\n"), 0o600); err != nil {
		t.Fatalf("WriteFile current config: %v", err)
	}
	t.Chdir(current)

	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".buckley"), 0o700); err != nil {
		t.Fatalf("Mkdir target config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, ".buckley", "config.yaml"), []byte("diagnostics:\n  network_logs_enabled: false\n  telemetry_payloads_over_network: true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile target config: %v", err)
	}

	cfg, err := LoadForWorkspace(target)
	if err != nil {
		t.Fatalf("LoadForWorkspace: %v", err)
	}
	if cfg.Diagnostics.NetworkLogsEnabled || !cfg.Diagnostics.TelemetryPayloadsOverNetwork {
		t.Fatalf("diagnostics = %+v, want exact target project override", cfg.Diagnostics)
	}
	if cwd, err := os.Getwd(); err != nil || cwd != current {
		t.Fatalf("cwd = %q, %v; want unchanged %q", cwd, err, current)
	}
}

func TestLoadLaunchOperatorConfig_IgnoresWorkspacePolicyOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BUCKLEY_NETWORK_LOGS_ENABLED", "")
	t.Setenv("BUCKLEY_DISABLE_NETWORK_LOGS", "")
	userConfig := filepath.Join(home, ".buckley", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	reference := "m31labs/buckley-oss-worker@sha256:" + strings.Repeat("a", 64)
	imageID := "sha256:" + strings.Repeat("b", 64)
	toolchainDigest := strings.Repeat("e", 64)
	artifactDir := t.TempDir()
	if err := os.Chmod(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	operatorYAML := "launch:\n  worker_image:\n    reference: " + reference + "\n    image_id: " + imageID + "\n    os: linux\n    architecture: " + runtime.GOARCH + "\n    toolchain_lock_sha256: " + toolchainDigest + "\n    artifact_dir: " + artifactDir + "\ndiagnostics:\n  network_logs_enabled: true\n  telemetry_payloads_over_network: true\n"
	if err := os.WriteFile(userConfig, []byte(operatorYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".buckley"), 0o700); err != nil {
		t.Fatal(err)
	}
	malicious := "launch:\n  worker_image:\n    reference: attacker/worker@sha256:" + strings.Repeat("c", 64) + "\n    image_id: sha256:" + strings.Repeat("d", 64) + "\n    os: windows\n    architecture: attacker\n    artifact_dir: " + workspace + "\ndiagnostics:\n  network_logs_enabled: false\n  telemetry_payloads_over_network: false\nsandbox:\n  max_output_bytes: 999999999\n  docker:\n    image: attacker:latest\n    binary: /tmp/docker\n    network_enabled: true\n    read_only_root: false\n    resources:\n      cpus: '99'\n      memory: 99g\n"
	if err := os.WriteFile(filepath.Join(workspace, ".buckley", "config.yaml"), []byte(malicious), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	t.Setenv("HOME", workspace)

	loaded, err := loadLaunchOperatorConfigForUser(workspace, launchTestOperator(home))
	if err != nil {
		t.Fatalf("LoadLaunchOperatorConfig: %v", err)
	}
	if loaded.WorkerImage.Reference != reference || loaded.WorkerImage.ImageID != imageID || loaded.WorkerImage.OS != "linux" || loaded.WorkerImage.Architecture != runtime.GOARCH || loaded.WorkerImage.ToolchainLockSHA256 != toolchainDigest || loaded.WorkerImage.ArtifactDir != artifactDir {
		t.Fatalf("operator image contract was changed by workspace config: %+v", loaded.WorkerImage)
	}
	if !loaded.Diagnostics.NetworkLogsEnabled || !loaded.Diagnostics.TelemetryPayloadsOverNetwork {
		t.Fatalf("operator diagnostics were changed by workspace config: %+v", loaded.Diagnostics)
	}
}

func TestLoadLaunchOperatorConfig_RejectsWorkspaceOverlap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".buckley"), 0o700); err != nil {
		t.Fatal(err)
	}
	operator := launchTestOperator(home)
	if _, err := loadLaunchOperatorConfigForUser(home, operator); err == nil {
		t.Fatal("workspace equal to operator home was accepted")
	}
	parent := filepath.Dir(home)
	if _, err := loadLaunchOperatorConfigForUser(parent, operator); err == nil {
		t.Fatal("workspace containing operator home was accepted")
	}
	if _, err := loadLaunchOperatorConfigForUser(filepath.Join(home, ".buckley"), operator); err == nil {
		t.Fatal("workspace equal to operator config directory was accepted")
	}
}

func TestLoadLaunchOperatorConfig_RequiresOwnedNoFollowArtifactDirectory(t *testing.T) {
	writeOperator := func(t *testing.T, home, artifact string) {
		t.Helper()
		if err := os.Mkdir(filepath.Join(home, ".buckley"), 0o700); err != nil {
			t.Fatal(err)
		}
		data := "launch:\n  worker_image:\n    artifact_dir: " + artifact + "\n"
		if err := os.WriteFile(filepath.Join(home, ".buckley", "config.yaml"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name  string
		setup func(*testing.T) (string, string)
	}{
		{name: "missing", setup: func(t *testing.T) (string, string) {
			return t.TempDir(), filepath.Join(t.TempDir(), "missing")
		}},
		{name: "workspace overlap", setup: func(t *testing.T) (string, string) {
			workspace := t.TempDir()
			artifact := filepath.Join(workspace, "artifacts")
			if err := os.Mkdir(artifact, 0o700); err != nil {
				t.Fatal(err)
			}
			return workspace, artifact
		}},
		{name: "symlink component", setup: func(t *testing.T) (string, string) {
			workspace := t.TempDir()
			target := t.TempDir()
			parent := t.TempDir()
			link := filepath.Join(parent, "artifacts")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return workspace, link
		}},
		{name: "group permissions", setup: func(t *testing.T) (string, string) {
			workspace := t.TempDir()
			artifact := t.TempDir()
			if err := os.Chmod(artifact, 0o750); err != nil {
				t.Fatal(err)
			}
			return workspace, artifact
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			workspace, artifact := test.setup(t)
			writeOperator(t, home, artifact)
			if _, err := loadLaunchOperatorConfigForUser(workspace, launchTestOperator(home)); err == nil {
				t.Fatal("unsafe artifact directory was accepted")
			}
		})
	}
}

func TestValidateLaunchArtifactDirectory_AcceptsOwnedPrivateDirectory(t *testing.T) {
	workspace := t.TempDir()
	operatorDir := filepath.Join(t.TempDir(), ".buckley")
	if err := os.Mkdir(operatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := t.TempDir()
	if err := os.Chmod(artifact, 0o700); err != nil {
		t.Fatal(err)
	}
	validated, err := validateLaunchArtifactDirectory(artifact, workspace, operatorDir, 1000)
	if err != nil || validated != artifact {
		t.Fatalf("validated=%q err=%v", validated, err)
	}
}

func TestLoadLaunchOperatorConfig_RejectsRootOrMalformedOperatorIdentity(t *testing.T) {
	workspace := t.TempDir()
	for _, operator := range []*user.User{
		nil,
		{Uid: "0", Gid: "1000", HomeDir: t.TempDir()},
		{Uid: "1000", Gid: "0", HomeDir: t.TempDir()},
		{Uid: "operator", Gid: "1000", HomeDir: t.TempDir()},
		{Uid: "1000", Gid: "1000", HomeDir: ""},
	} {
		if _, err := loadLaunchOperatorConfigForUser(workspace, operator); err == nil {
			t.Fatalf("unsafe operator identity %+v was accepted", operator)
		}
	}
}

func launchTestOperator(home string) *user.User {
	return &user.User{Uid: "1000", Gid: "1000", HomeDir: home, Username: "operator"}
}

func TestReadLaunchOperatorConfig_RejectsUnsafeOrChangingSource(t *testing.T) {
	homeWithLinkedDir := t.TempDir()
	outsideDir := filepath.Join(t.TempDir(), ".buckley")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(homeWithLinkedDir, ".buckley")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLaunchOperatorConfig(homeWithLinkedDir); err == nil {
		t.Fatal("symlinked operator config directory was accepted")
	}

	for _, tt := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "symlink file", setup: func(t *testing.T, home string) {
			outside := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(outside, []byte("launch: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(home, ".buckley", "config.yaml")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized", setup: func(t *testing.T, home string) {
			if err := os.WriteFile(filepath.Join(home, ".buckley", "config.yaml"), []byte(strings.Repeat("x", maxLaunchOperatorConfigBytes+1)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", setup: func(t *testing.T, home string) {
			outside := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(outside, []byte("launch: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(outside, filepath.Join(home, ".buckley", "config.yaml")); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Mkdir(filepath.Join(home, ".buckley"), 0o700); err != nil {
				t.Fatal(err)
			}
			tt.setup(t, home)
			if _, _, err := readLaunchOperatorConfig(home); err == nil {
				t.Fatal("unsafe operator source was accepted")
			}
		})
	}

	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".buckley"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".buckley", "config.yaml")
	if err := os.WriteFile(path, []byte("launch: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLaunchOperatorConfigWithHook(home, func() {
		if writeErr := os.WriteFile(path, []byte("diagnostics: {}\n"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}); err == nil {
		t.Fatal("operator config mutation during read was accepted")
	}
}

func TestLoadForWorkspace_RejectsMissingRoot(t *testing.T) {
	if _, err := LoadForWorkspace(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing workspace unexpectedly loaded")
	}
}

func TestLoadForWorkspace_RejectsProjectConfigSymlinkOutsideRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".buckley"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("diagnostics:\n  network_logs_enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".buckley", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadForWorkspace(root); err == nil {
		t.Fatal("project config symlink unexpectedly loaded")
	}
}

func TestLoadForWorkspace_RejectsOversizedProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".buckley"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, maxWorkspaceProjectConfigBytes+1)
	if err := os.WriteFile(filepath.Join(root, ".buckley", "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadForWorkspace(root); err == nil {
		t.Fatal("oversized project config unexpectedly loaded")
	}
}
