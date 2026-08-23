package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildAndSeal_FakeRunnerWritesBoundedOperatorArtifacts(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const registryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const networkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const workerID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const sbomID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	moduleLock := ModuleLock{Schema: ModuleLockSchema, Repositories: []RepositoryLock{{Name: "gsxmail", Commit: strings.Repeat("4", 40)}}}
	moduleBytes, err := canonicalJSON(moduleLock)
	if err != nil {
		t.Fatal(err)
	}
	moduleDigest := digestBytes(moduleBytes)
	exactRef := "127.0.0.1:49123/buckley/oss-worker@sha256:" + strings.Repeat("d", 64)
	workerLocal := testWorkerIdentity(workerID, "", moduleDigest)
	workerExact := testWorkerIdentity(workerID, exactRef, moduleDigest)
	registry := ImageIdentity{ID: "sha256:" + strings.Repeat("1", 64), RepoDigests: []string{RegistryRef}, OS: "linux", Architecture: "amd64"}
	contextRoot := t.TempDir()
	writeProvisionContractAssets(t, contextRoot)
	if err := os.WriteFile(filepath.Join(contextRoot, "module-lock.json"), moduleBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	toolchainBytes, err := os.ReadFile(filepath.Join(testAssetsRoot(t), "toolchain.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextRoot, "toolchain.lock"), toolchainBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	_, toolchainDigest, err := LoadToolchainLock(filepath.Join(contextRoot, "toolchain.lock"))
	if err != nil {
		t.Fatal(err)
	}
	contextDigest, contextEntries, contextBytes, err := digestContext(contextRoot)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	localTag := "buckley-launch-build-" + owner
	newRunner := func(failLocalCleanup bool) Runner {
		return runnerFunc(func(_ context.Context, args ...string) (string, error) {
			call := strings.Join(args, " ")
			switch {
			case call == "buildx version":
				return "github.com/docker/buildx " + BuildxVersion + " " + BuildxCommit + "\n", nil
			case call == "buildx inspect default":
				return "Name: default\nDriver: " + BuildKitDriver + "\nBuildKit version: " + BuildKitVersion + "\n", nil
			case strings.HasPrefix(call, "buildx build "):
				if !strings.Contains(call, "--build-arg SOURCE_DATE_EPOCH="+fmt.Sprint(SourceDateEpoch)) || !strings.Contains(call, "--platform "+Platform) || !strings.Contains(call, "--no-cache") || !strings.Contains(call, "--builder default") {
					return "", errors.New("reproducible build arguments missing")
				}
				for index := range args {
					if args[index] == "--metadata-file" && index+1 < len(args) {
						metadata := fmt.Sprintf("{\"containerimage.config.digest\":%q,\"containerimage.digest\":%q}\n", workerID, "sha256:"+strings.Repeat("d", 64))
						if err := os.WriteFile(args[index+1], []byte(metadata), 0o600); err != nil {
							return "", err
						}
					}
				}
				return "", nil
			case call == "image inspect "+localTag:
				return imageIdentityJSON(workerLocal), nil
			case call == "pull "+RegistryRef:
				return "", nil
			case call == "image inspect "+RegistryRef:
				return imageIdentityJSON(registry), nil
			case strings.HasPrefix(call, "network create "):
				return networkID, nil
			case strings.HasPrefix(call, "create --name buckley-launch-registry-"):
				return registryID, nil
			case call == "start -- "+registryID:
				return "", nil
			case call == "port "+registryID+" 5000/tcp":
				return "127.0.0.1:49123", nil
			case strings.HasPrefix(call, "tag "), strings.HasPrefix(call, "push "):
				return "", nil
			case strings.HasPrefix(call, "image inspect 127.0.0.1:49123/buckley/oss-worker:"), call == "image inspect "+exactRef:
				return imageIdentityJSON(workerExact), nil
			case call == "pull "+exactRef:
				return "", nil
			case call == "rm -f -- "+registryID, call == "network rm -- "+networkID:
				return "", nil
			case strings.HasPrefix(call, "create --name buckley-launch-sbom-"):
				return sbomID, nil
			case strings.HasPrefix(call, "cp "+sbomID+":/var/lib/dpkg/status "):
				path := args[len(args)-1]
				return "", os.WriteFile(path, []byte("Package: base-files\nStatus: install ok installed\nVersion: 13.8\n\n"), 0o600)
			case strings.HasPrefix(call, "cp "+sbomID+":/usr/share/buckley-launch/module-inventory.tsv "):
				path := args[len(args)-1]
				return "", os.WriteFile(path, []byte("example.com/dependency\tv1.2.3\n"), 0o600)
			case call == "rm -f -- "+sbomID:
				return "", nil
			case call == "image rm -- "+localTag && failLocalCleanup:
				return "", errors.New("cleanup failed")
			case strings.HasPrefix(call, "image rm -- "):
				return "", nil
			default:
				return "", fmt.Errorf("unexpected command: %s", call)
			}
		})
	}
	runner := newRunner(false)
	result, err := BuildAndSeal(context.Background(), SealOptions{
		ContextPath: contextRoot,
		Artifacts:   artifacts,
		Manifest:    ContextManifest{Schema: ContextManifestType, ContextSHA256: contextDigest, ModuleLockSHA256: moduleDigest, ToolchainSHA256: toolchainDigest, Entries: contextEntries, Bytes: contextBytes},
		ModuleLock:  moduleLock,
		Toolchain:   ExpectedToolchain(),
		Runner:      runner,
		readiness:   registryReadinessFunc(func(context.Context, string) error { return nil }),
		Token:       owner,
	})
	if err != nil {
		t.Fatalf("BuildAndSeal: %v", err)
	}
	if result.Contract.Reference != exactRef || result.Contract.ImageID != workerID {
		t.Fatalf("sealed contract = %+v", result.Contract)
	}
	for _, name := range []string{"buildkit-metadata.json", "module-lock.json", "toolchain-lock.json", "operator-config.yaml", "operator-contract.json", "provenance.json", "sbom.spdx.json"} {
		info, err := os.Stat(filepath.Join(artifacts, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCommandOutput {
			t.Fatalf("artifact %q = %+v, %v", name, info, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(artifacts, "operator-config.yaml"))
	if err != nil || !strings.Contains(string(config), exactRef) || !strings.Contains(string(config), workerID) {
		t.Fatalf("operator config = %q, %v", config, err)
	}
	sbomBytes, err := os.ReadFile(filepath.Join(artifacts, "sbom.spdx.json"))
	if err != nil {
		t.Fatal(err)
	}
	var provenance provenanceArtifact
	provenanceBytes, err := os.ReadFile(filepath.Join(artifacts, "provenance.json"))
	if err != nil || json.Unmarshal(provenanceBytes, &provenance) != nil {
		t.Fatalf("provenance artifact is invalid: %v", err)
	}
	if provenance.SBOMSHA256 != digestBytes(sbomBytes) || provenance.BuildConfigDigest != workerID || provenance.BuildResultDigest != "sha256:"+strings.Repeat("d", 64) {
		t.Fatalf("provenance bindings = %+v", provenance)
	}
	var sbom spdxDocument
	if err := json.Unmarshal(sbomBytes, &sbom); err != nil {
		t.Fatal(err)
	}
	joinedSBOM := string(sbomBytes)
	for _, expected := range []string{"buckley-oss-worker", "example.com/dependency", "base-files", "TinyGo", "LLVM"} {
		if !strings.Contains(joinedSBOM, expected) {
			t.Fatalf("SBOM missing actual image package %q", expected)
		}
	}
	if len(sbom.Relationships) < 2 || sbom.Relationships[0].RelationshipType != "DESCRIBES" || sbom.Relationships[1].RelationshipType != "CONTAINS" {
		t.Fatalf("SBOM dependency relationships = %+v", sbom.Relationships)
	}

	failedArtifacts := filepath.Join(t.TempDir(), "failed-artifacts")
	failedResult, err := BuildAndSeal(context.Background(), SealOptions{
		ContextPath: contextRoot,
		Artifacts:   failedArtifacts,
		Manifest:    ContextManifest{Schema: ContextManifestType, ContextSHA256: contextDigest, ModuleLockSHA256: moduleDigest, ToolchainSHA256: toolchainDigest, Entries: contextEntries, Bytes: contextBytes},
		ModuleLock:  moduleLock,
		Toolchain:   ExpectedToolchain(),
		Runner:      newRunner(true),
		readiness:   registryReadinessFunc(func(context.Context, string) error { return nil }),
		Token:       owner,
	})
	if err == nil || !strings.Contains(err.Error(), "temporary build image cleanup required: "+localTag) {
		t.Fatalf("late local-tag cleanup = %v, want bounded cleanup identity", err)
	}
	if failedResult != (SealResult{}) {
		t.Fatalf("failed cleanup returned published result: %+v", failedResult)
	}
	if _, statErr := os.Stat(failedArtifacts); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed artifact publication survived cleanup error: %v", statErr)
	}
}

func TestVerifyProvisionInputs_RequiresExactMutualBindings(t *testing.T) {
	moduleLock := ModuleLock{Schema: ModuleLockSchema, Repositories: []RepositoryLock{{
		Name: "gsxmail", Commit: strings.Repeat("4", 40), Manifests: []ManifestLock{{Path: "go.mod", SHA256: strings.Repeat("5", 64), Bytes: 8}},
	}}}
	moduleBytes, err := canonicalJSON(moduleLock)
	if err != nil {
		t.Fatal(err)
	}
	toolchainBytes, err := os.ReadFile(filepath.Join(testAssetsRoot(t), "toolchain.lock"))
	if err != nil {
		t.Fatal(err)
	}
	newFixture := func(t *testing.T) (string, SealOptions) {
		t.Helper()
		root := t.TempDir()
		writeProvisionContractAssets(t, root)
		if err := os.WriteFile(filepath.Join(root, "module-lock.json"), moduleBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "toolchain.lock"), toolchainBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		_, toolchainDigest, err := LoadToolchainLock(filepath.Join(root, "toolchain.lock"))
		if err != nil {
			t.Fatal(err)
		}
		lockCopy := moduleLock
		lockCopy.Repositories = append([]RepositoryLock(nil), moduleLock.Repositories...)
		for index := range lockCopy.Repositories {
			lockCopy.Repositories[index].Manifests = append([]ManifestLock(nil), moduleLock.Repositories[index].Manifests...)
		}
		return root, SealOptions{
			Manifest:   ContextManifest{ModuleLockSHA256: digestBytes(moduleBytes), ToolchainSHA256: toolchainDigest},
			ModuleLock: lockCopy,
			Toolchain:  ExpectedToolchain(),
		}
	}
	tests := []struct {
		name   string
		mutate func(*testing.T, string, *SealOptions)
	}{
		{name: "module object", mutate: func(_ *testing.T, _ string, opts *SealOptions) {
			opts.ModuleLock.Repositories[0].Commit = strings.Repeat("6", 40)
		}},
		{name: "module context bytes", mutate: func(t *testing.T, root string, _ *SealOptions) {
			if err := os.WriteFile(filepath.Join(root, "module-lock.json"), append([]byte(nil), []byte("{}\n")...), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "toolchain object", mutate: func(_ *testing.T, _ string, opts *SealOptions) {
			opts.Toolchain.GoVersion = "1.26.5"
		}},
		{name: "toolchain context bytes", mutate: func(t *testing.T, root string, _ *SealOptions) {
			if err := os.WriteFile(filepath.Join(root, "toolchain.lock"), append(toolchainBytes, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "toolchain manifest digest", mutate: func(_ *testing.T, _ string, opts *SealOptions) {
			opts.Manifest.ToolchainSHA256 = strings.Repeat("7", 64)
		}},
		{name: "TinyGo license bytes", mutate: func(t *testing.T, root string, _ *SealOptions) {
			if err := os.WriteFile(filepath.Join(root, "launch", "licenses", "TinyGo-LICENSE"), []byte("wrong\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Dockerfile frontend", mutate: func(t *testing.T, root string, _ *SealOptions) {
			if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("# syntax=docker/dockerfile:latest\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, opts := newFixture(t)
			test.mutate(t, root, &opts)
			if err := verifyProvisionInputs(root, opts); err == nil {
				t.Fatal("mismatched provision inputs accepted")
			}
		})
	}
	root, opts := newFixture(t)
	if err := verifyProvisionInputs(root, opts); err != nil {
		t.Fatalf("valid mutual bindings rejected: %v", err)
	}
}

func writeProvisionContractAssets(t *testing.T, root string) {
	t.Helper()
	assets := testAssetsRoot(t)
	for _, relative := range []string{"Dockerfile", "licenses/TinyGo-LICENSE"} {
		data, err := os.ReadFile(filepath.Join(assets, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		target := relative
		if relative != "Dockerfile" {
			target = filepath.ToSlash(filepath.Join("launch", relative))
		}
		absolute := filepath.Join(root, filepath.FromSlash(target))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVerifyBuilderContract_ExactVersionsOnly(t *testing.T) {
	valid := func(version, inspect string) Runner {
		return runnerFunc(func(_ context.Context, args ...string) (string, error) {
			switch strings.Join(args, " ") {
			case "buildx version":
				return version, nil
			case "buildx inspect default":
				return inspect, nil
			default:
				return "", errors.New("unexpected command")
			}
		})
	}
	version := "github.com/docker/buildx " + BuildxVersion + " " + BuildxCommit + "\n"
	inspect := "Name: default\nDriver: " + BuildKitDriver + "\nBuildKit version: " + BuildKitVersion + "\n"
	if err := verifyBuilderContract(context.Background(), valid(version, inspect), ExpectedToolchain()); err != nil {
		t.Fatalf("exact builder contract rejected: %v", err)
	}
	for _, test := range []struct {
		name    string
		version string
		inspect string
	}{
		{name: "buildx", version: strings.Replace(version, BuildxVersion, "v0.27.0", 1), inspect: inspect},
		{name: "buildkit", version: version, inspect: strings.Replace(inspect, BuildKitVersion, "v0.23.0", 1)},
		{name: "driver", version: version, inspect: strings.Replace(inspect, "Driver: "+BuildKitDriver, "Driver: docker-container", 1)},
		{name: "duplicate buildkit", version: version, inspect: inspect + "BuildKit version: " + BuildKitVersion + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyBuilderContract(context.Background(), valid(test.version, test.inspect), ExpectedToolchain()); err == nil {
				t.Fatal("mismatched builder contract accepted")
			}
		})
	}
}

func TestBuildMetadataAndModuleInventory_FailClosed(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("b", 64)
	contract := WorkerContract{ImageID: imageID, Reference: "127.0.0.1:49123/buckley/oss-worker@" + digest}
	identity := ImageIdentity{ID: imageID}
	valid := []byte(fmt.Sprintf("{\"containerimage.config.digest\":%q,\"containerimage.digest\":%q}\n", imageID, digest))
	if got, err := validateBuildMetadata(valid, identity, contract); err != nil || got.ConfigDigest != imageID || got.ResultDigest != digest {
		t.Fatalf("valid build metadata = %+v, %v", got, err)
	}
	for _, invalid := range [][]byte{
		[]byte("{}\n"),
		[]byte(fmt.Sprintf("{\"containerimage.config.digest\":%q,\"containerimage.digest\":%q}\n", "sha256:"+strings.Repeat("c", 64), digest)),
		[]byte(fmt.Sprintf("{\"containerimage.config.digest\":%q,\"containerimage.digest\":%q}\n{}", imageID, digest)),
	} {
		if _, err := validateBuildMetadata(invalid, identity, contract); err == nil {
			t.Fatalf("invalid build metadata accepted: %q", invalid)
		}
	}
	modules, err := parseModuleInventory([]byte("example.com/alpha\tv1.2.3\nexample.com/beta\tv0.0.0-20260101000000-abcdefabcdef\n"))
	if err != nil || len(modules) != 2 || modules[0].Name != "example.com/alpha" {
		t.Fatalf("module inventory = %+v, %v", modules, err)
	}
	for _, invalid := range [][]byte{[]byte(""), []byte("example.com/only-one-field\n"), []byte("example.com/bad name\tv1.0.0\n"), []byte("example.com/name\tv1.0.0\textra\n")} {
		if _, err := parseModuleInventory(invalid); err == nil {
			t.Fatalf("invalid module inventory accepted: %q", invalid)
		}
	}
}

func TestWorkerImageIdentity_RejectsTrailingAndEnvironmentOverrides(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	identity := testWorkerIdentity(imageID, "", digest)
	if err := validateWorkerIdentity(identity, digest, ""); err != nil {
		t.Fatalf("valid worker identity rejected: %v", err)
	}
	identity.Config.Env = append(identity.Config.Env, "GOPROXY=https://example.invalid")
	if err := validateWorkerIdentity(identity, digest, ""); err == nil {
		t.Fatal("duplicate online proxy override accepted")
	}
	raw := imageIdentityJSON(testWorkerIdentity(imageID, "", digest))
	if _, err := parseImageInspect(raw + "\n{}"); err == nil {
		t.Fatal("trailing image inspection payload accepted")
	}
}

func TestCleanupLostAcknowledgementAndOwnershipFailClosed(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var calls []string
	runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if strings.HasPrefix(call, "rm -f -- ") {
			return "", errors.New("ack lost")
		}
		if strings.HasPrefix(call, "container ls ") {
			return "", nil
		}
		return "", fmt.Errorf("unexpected command: %s", call)
	})
	if err := cleanupContainer(context.Background(), runner, id); err != nil {
		t.Fatalf("lost removal acknowledgement not reconciled: %v", err)
	}
	wrong := runnerFunc(func(_ context.Context, _ ...string) (string, error) {
		return id + "|sha256:" + strings.Repeat("b", 64) + "|wrong/image|wrong-owner", nil
	})
	if got, err := resolveOwnedContainer(context.Background(), wrong, "buckley-launch-registry-"+strings.Repeat("c", 32), strings.Repeat("c", 32), RegistryRef, "sha256:"+strings.Repeat("b", 64)); err == nil || got != "" {
		t.Fatalf("foreign name collision resolved as owned: %q, %v", got, err)
	}
}

func TestHTTPRegistryReadiness_BoundedLoopbackOnly(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		response.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := (httpRegistryReadiness{}).Wait(ctx, host); err != nil {
		t.Fatalf("delayed loopback registry readiness: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("readiness attempts = %d, want 3", calls.Load())
	}

	deadline, deadlineCancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer deadlineCancel()
	if err := (httpRegistryReadiness{}).Wait(deadline, "127.0.0.1:1"); err == nil {
		t.Fatal("unavailable loopback registry became ready")
	}
	if err := (httpRegistryReadiness{}).Wait(context.Background(), "example.com:5000"); err == nil {
		t.Fatal("non-loopback registry readiness accepted")
	}
	var redirected atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer external.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Location", external.URL)
		response.WriteHeader(http.StatusFound)
	}))
	defer redirector.Close()
	redirectCtx, redirectCancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer redirectCancel()
	if err := (httpRegistryReadiness{}).Wait(redirectCtx, strings.TrimPrefix(redirector.URL, "http://")); err == nil {
		t.Fatal("redirecting registry became ready")
	}
	if redirected.Load() != 0 {
		t.Fatalf("registry readiness followed %d external redirects", redirected.Load())
	}
}

func TestSealThroughLoopbackRegistry_ReconcilesLostCreateAcknowledgements(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const localTag = "buckley-launch-build-0123456789abcdef0123456789abcdef"
	const registryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const networkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const workerID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	registryImageID := "sha256:" + strings.Repeat("d", 64)
	registry := ImageIdentity{ID: registryImageID, RepoDigests: []string{RegistryRef}, OS: "linux", Architecture: "amd64"}
	for _, failAt := range []string{"network", "container"} {
		t.Run(failAt, func(t *testing.T) {
			var calls []string
			runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
				call := strings.Join(args, " ")
				calls = append(calls, call)
				switch {
				case call == "pull "+RegistryRef:
					return "", nil
				case call == "image inspect "+RegistryRef:
					return imageIdentityJSON(registry), nil
				case strings.HasPrefix(call, "network create "):
					if failAt == "network" {
						return "", errors.New("network create acknowledgement lost")
					}
					return networkID, nil
				case strings.HasPrefix(call, "network inspect -f "):
					return networkID + "|" + owner, nil
				case strings.HasPrefix(call, "create "):
					return "", errors.New("container create acknowledgement lost")
				case strings.HasPrefix(call, "inspect -f "):
					return registryID + "|" + registryImageID + "|" + RegistryRef + "|" + owner, nil
				case call == "rm -f -- "+registryID, call == "network rm -- "+networkID:
					return "", nil
				default:
					return "", fmt.Errorf("unexpected command: %s", call)
				}
			})
			if _, err := sealThroughLoopbackRegistry(context.Background(), runner, registryReadinessFunc(func(context.Context, string) error { return nil }), ExpectedToolchain(), localTag, workerID, strings.Repeat("e", 64), owner); err == nil {
				t.Fatal("lost create acknowledgement accepted")
			}
			joined := strings.Join(calls, "\n")
			if !strings.Contains(joined, "network rm -- "+networkID) {
				t.Fatalf("owned network was not reconciled:\n%s", joined)
			}
			if failAt == "container" && !strings.Contains(joined, "rm -f -- "+registryID) {
				t.Fatalf("owned container was not reconciled:\n%s", joined)
			}
		})
	}
}

func TestSealThroughLoopbackRegistry_LostCreateAcknowledgementReportsCleanupIdentity(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const localTag = "buckley-launch-build-0123456789abcdef0123456789abcdef"
	const registryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const networkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const workerID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	registryImageID := "sha256:" + strings.Repeat("d", 64)
	registry := ImageIdentity{ID: registryImageID, RepoDigests: []string{RegistryRef}, OS: "linux", Architecture: "amd64"}
	for _, failAt := range []string{"network", "container"} {
		t.Run(failAt, func(t *testing.T) {
			runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
				call := strings.Join(args, " ")
				switch {
				case call == "pull "+RegistryRef:
					return "", nil
				case call == "image inspect "+RegistryRef:
					return imageIdentityJSON(registry), nil
				case strings.HasPrefix(call, "network create "):
					if failAt == "network" {
						return "", errors.New("network create acknowledgement lost")
					}
					return networkID, nil
				case strings.HasPrefix(call, "network inspect -f "):
					return networkID + "|" + owner, nil
				case strings.HasPrefix(call, "create "):
					return "", errors.New("container create acknowledgement lost")
				case strings.HasPrefix(call, "inspect -f "):
					return registryID + "|" + registryImageID + "|" + RegistryRef + "|" + owner, nil
				case call == "rm -f -- "+registryID, call == "network rm -- "+networkID && failAt == "network":
					return "", errors.New("cleanup failure")
				case call == "network rm -- "+networkID:
					return "", nil
				case strings.HasPrefix(call, "container ls -a --no-trunc --filter id="+registryID):
					return registryID, nil
				case strings.HasPrefix(call, "network ls --no-trunc --filter id="+networkID):
					return networkID, nil
				default:
					return "", fmt.Errorf("unexpected command: %s", call)
				}
			})
			_, err := sealThroughLoopbackRegistry(context.Background(), runner, registryReadinessFunc(func(context.Context, string) error { return nil }), ExpectedToolchain(), localTag, workerID, strings.Repeat("e", 64), owner)
			wantResource, wantIdentity := "registry-network", networkID
			if failAt == "container" {
				wantResource, wantIdentity = "registry-container", registryID
			}
			var cleanup *CleanupRequiredError
			if err == nil || !errors.As(err, &cleanup) || cleanup.Resource != wantResource || cleanup.Identity != wantIdentity {
				t.Fatalf("lost %s acknowledgement cleanup = %+v, %v", failAt, cleanup, err)
			}
		})
	}
}

func TestSealThroughLoopbackRegistry_LostTagAcknowledgementReportsCleanupIdentity(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const localTag = "buckley-launch-build-0123456789abcdef0123456789abcdef"
	const registryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const networkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const workerID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	remoteTag := "127.0.0.1:49123/buckley/oss-worker:" + owner
	registry := ImageIdentity{ID: "sha256:" + strings.Repeat("d", 64), RepoDigests: []string{RegistryRef}, OS: "linux", Architecture: "amd64"}
	runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		switch {
		case call == "pull "+RegistryRef:
			return "", nil
		case call == "image inspect "+RegistryRef:
			return imageIdentityJSON(registry), nil
		case strings.HasPrefix(call, "network create "):
			return networkID, nil
		case strings.HasPrefix(call, "create "):
			return registryID, nil
		case call == "start -- "+registryID:
			return "", nil
		case call == "port "+registryID+" 5000/tcp":
			return "127.0.0.1:49123", nil
		case call == "tag "+localTag+" "+remoteTag:
			return "", errors.New("tag acknowledgement lost")
		case call == "image rm -- "+remoteTag:
			return "", errors.New("tag cleanup failure")
		case call == "image ls --no-trunc --quiet "+remoteTag:
			return workerID, nil
		case call == "rm -f -- "+registryID, call == "network rm -- "+networkID:
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", call)
		}
	})
	_, err := sealThroughLoopbackRegistry(context.Background(), runner, registryReadinessFunc(func(context.Context, string) error { return nil }), ExpectedToolchain(), localTag, workerID, strings.Repeat("e", 64), owner)
	var cleanup *CleanupRequiredError
	if err == nil || !strings.Contains(err.Error(), "worker image loopback tag failed") || !errors.As(err, &cleanup) || cleanup.Resource != "registry-image-tag" || cleanup.Identity != remoteTag {
		t.Fatalf("lost tag acknowledgement cleanup = %+v, %v", cleanup, err)
	}
}

type runnerFunc func(context.Context, ...string) (string, error)

func (f runnerFunc) Run(ctx context.Context, args ...string) (string, error) { return f(ctx, args...) }

type registryReadinessFunc func(context.Context, string) error

func (f registryReadinessFunc) Wait(ctx context.Context, host string) error { return f(ctx, host) }

func TestSealThroughLoopbackRegistry_ExactDigestSurvivesRegistryStop(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const localTag = "buckley-launch-build-0123456789abcdef0123456789abcdef"
	const registryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const networkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const workerID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const digest = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	remoteRepository := "127.0.0.1:49123/buckley/oss-worker"
	exactRef := remoteRepository + "@sha256:" + digest
	worker := testWorkerIdentity(workerID, exactRef, strings.Repeat("e", 64))
	registry := ImageIdentity{ID: "sha256:" + strings.Repeat("f", 64), RepoDigests: []string{RegistryRef}, OS: "linux", Architecture: "amd64"}
	var mu sync.Mutex
	var calls []string
	runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		switch {
		case call == "pull "+RegistryRef:
			return "", nil
		case call == "image inspect "+RegistryRef:
			return imageIdentityJSON(registry), nil
		case strings.HasPrefix(call, "network create "):
			return networkID + "\n", nil
		case strings.HasPrefix(call, "create "):
			return registryID + "\n", nil
		case call == "start -- "+registryID:
			return "", nil
		case call == "port "+registryID+" 5000/tcp":
			return "127.0.0.1:49123\n", nil
		case strings.HasPrefix(call, "tag "), strings.HasPrefix(call, "push "):
			return "", nil
		case strings.HasPrefix(call, "image inspect "+remoteRepository+":"), call == "image inspect "+exactRef:
			return imageIdentityJSON(worker), nil
		case call == "pull "+exactRef:
			return "", nil
		case call == "rm -f -- "+registryID, call == "network rm -- "+networkID, strings.HasPrefix(call, "image rm -- "+remoteRepository+":"):
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", call)
		}
	})
	contract, err := sealThroughLoopbackRegistry(context.Background(), runner, registryReadinessFunc(func(context.Context, string) error { return nil }), ExpectedToolchain(), localTag, workerID, strings.Repeat("e", 64), owner)
	if err != nil {
		t.Fatalf("sealThroughLoopbackRegistry: %v\ncalls=%v", err, calls)
	}
	if contract.Reference != exactRef || contract.ImageID != workerID || contract.OS != "linux" || contract.Architecture != "amd64" {
		t.Fatalf("contract = %+v", contract)
	}
	joined := strings.Join(calls, "\n")
	rmIndex := strings.Index(joined, "rm -f -- "+registryID)
	lastInspect := strings.LastIndex(joined, "image inspect "+exactRef)
	if rmIndex < 0 || lastInspect < rmIndex {
		t.Fatalf("post-stop exact inspection missing:\n%s", joined)
	}
	if !strings.Contains(joined, "network create --internal") || !strings.Contains(joined, "--publish 127.0.0.1::5000") || !strings.Contains(joined, "--label "+registryOwnerLabel+"="+owner) {
		t.Fatalf("registry ceremony did not stay owned/internal/loopback:\n%s", joined)
	}
}

func TestSealThroughLoopbackRegistry_FailureCleansOwnedResources(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const localTag = "buckley-launch-build-0123456789abcdef0123456789abcdef"
	const registryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const networkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const workerID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	registry := ImageIdentity{ID: "sha256:" + strings.Repeat("f", 64), RepoDigests: []string{RegistryRef}, OS: "linux", Architecture: "amd64"}
	var calls []string
	runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch {
		case call == "pull "+RegistryRef:
			return "", nil
		case call == "image inspect "+RegistryRef:
			return imageIdentityJSON(registry), nil
		case strings.HasPrefix(call, "network create "):
			return networkID, nil
		case strings.HasPrefix(call, "create "):
			return registryID, nil
		case call == "start -- "+registryID, call == "port "+registryID+" 5000/tcp", strings.HasPrefix(call, "tag "):
			if strings.HasPrefix(call, "port ") {
				return "127.0.0.1:49123", nil
			}
			return "", nil
		case strings.HasPrefix(call, "push "):
			return "", errors.New("push failed")
		case strings.HasPrefix(call, "image rm -- "), call == "rm -f -- "+registryID, call == "network rm -- "+networkID:
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", call)
		}
	})
	if _, err := sealThroughLoopbackRegistry(context.Background(), runner, registryReadinessFunc(func(context.Context, string) error { return nil }), ExpectedToolchain(), localTag, workerID, strings.Repeat("e", 64), owner); err == nil {
		t.Fatal("push failure was accepted")
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "rm -f -- "+registryID) || !strings.Contains(joined, "network rm -- "+networkID) {
		t.Fatalf("failure cleanup missing:\n%s", joined)
	}
}

func TestSealThroughLoopbackRegistry_PrimaryFailureReportsEveryCleanupIdentity(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const localTag = "buckley-launch-build-0123456789abcdef0123456789abcdef"
	const registryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const networkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const workerID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	remoteTag := "127.0.0.1:49123/buckley/oss-worker:" + owner
	registry := ImageIdentity{ID: "sha256:" + strings.Repeat("f", 64), RepoDigests: []string{RegistryRef}, OS: "linux", Architecture: "amd64"}
	runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		switch {
		case call == "pull "+RegistryRef:
			return "", nil
		case call == "image inspect "+RegistryRef:
			return imageIdentityJSON(registry), nil
		case strings.HasPrefix(call, "network create "):
			return networkID, nil
		case strings.HasPrefix(call, "create "):
			return registryID, nil
		case call == "start -- "+registryID:
			return "", nil
		case call == "port "+registryID+" 5000/tcp":
			return "127.0.0.1:49123", nil
		case call == "tag "+localTag+" "+remoteTag:
			return "", nil
		case call == "push "+remoteTag:
			return "", errors.New("primary push failure")
		case call == "image rm -- "+remoteTag, call == "rm -f -- "+registryID, call == "network rm -- "+networkID:
			return "", errors.New("cleanup failure")
		case call == "image ls --no-trunc --quiet "+remoteTag:
			return workerID, nil
		case strings.HasPrefix(call, "container ls -a --no-trunc --filter id="+registryID):
			return registryID, nil
		case strings.HasPrefix(call, "network ls --no-trunc --filter id="+networkID):
			return networkID, nil
		default:
			return "", fmt.Errorf("unexpected command: %s", call)
		}
	})
	_, err := sealThroughLoopbackRegistry(context.Background(), runner, registryReadinessFunc(func(context.Context, string) error { return nil }), ExpectedToolchain(), localTag, workerID, strings.Repeat("e", 64), owner)
	if err == nil || !strings.Contains(err.Error(), "worker image loopback push failed") || !errors.Is(err, ErrCleanupRequired) {
		t.Fatalf("primary plus cleanup error = %v", err)
	}
	for _, identity := range []string{remoteTag, registryID, networkID} {
		if !strings.Contains(err.Error(), identity) {
			t.Fatalf("cleanup error omitted owned identity %q: %v", identity, err)
		}
	}
	if got := strings.Count(err.Error(), ErrCleanupRequired.Error()); got != 3 {
		t.Fatalf("cleanup error count = %d, want 3: %v", got, err)
	}
}

func TestCollectImagePackages_PrimaryFailureReportsCleanupIdentity(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const containerID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	reference := "127.0.0.1:49123/buckley/oss-worker@sha256:" + strings.Repeat("d", 64)
	runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(call, "create --name buckley-launch-sbom-"):
			return containerID, nil
		case strings.HasPrefix(call, "cp "+containerID+":/var/lib/dpkg/status "):
			return "", errors.New("primary inventory failure")
		case call == "rm -f -- "+containerID:
			return "", errors.New("cleanup failure")
		case strings.HasPrefix(call, "container ls -a --no-trunc --filter id="+containerID):
			return containerID, nil
		default:
			return "", fmt.Errorf("unexpected command: %s", call)
		}
	})
	packages, err := collectImagePackages(context.Background(), runner, reference, owner)
	if packages != nil || err == nil || !strings.Contains(err.Error(), "SBOM package inventory is unavailable") || !errors.Is(err, ErrCleanupRequired) || !strings.Contains(err.Error(), containerID) {
		t.Fatalf("SBOM primary plus cleanup = packages=%v err=%v", packages, err)
	}
	var cleanup *CleanupRequiredError
	if !errors.As(err, &cleanup) || cleanup.Resource != "sbom-container" || cleanup.Identity != containerID {
		t.Fatalf("typed SBOM cleanup = %+v, %v", cleanup, err)
	}
}

func TestCollectImagePackages_LostCreateAcknowledgementReportsCleanupIdentity(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const containerID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	reference := "127.0.0.1:49123/buckley/oss-worker@sha256:" + strings.Repeat("d", 64)
	imageID := "sha256:" + strings.Repeat("c", 64)
	identity := ImageIdentity{ID: imageID, RepoDigests: []string{reference}, OS: "linux", Architecture: "amd64"}
	runner := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(call, "create --name buckley-launch-sbom-"):
			return "", errors.New("create acknowledgement lost")
		case call == "image inspect "+reference:
			return imageIdentityJSON(identity), nil
		case strings.HasPrefix(call, "inspect -f "):
			return containerID + "|" + imageID + "|" + reference + "|" + owner, nil
		case call == "rm -f -- "+containerID:
			return "", errors.New("cleanup failure")
		case strings.HasPrefix(call, "container ls -a --no-trunc --filter id="+containerID):
			return containerID, nil
		default:
			return "", fmt.Errorf("unexpected command: %s", call)
		}
	})
	packages, err := collectImagePackages(context.Background(), runner, reference, owner)
	var cleanup *CleanupRequiredError
	if packages != nil || err == nil || !strings.Contains(err.Error(), "SBOM container creation failed") || !errors.As(err, &cleanup) || cleanup.Resource != "sbom-container" || cleanup.Identity != containerID {
		t.Fatalf("lost SBOM create acknowledgement cleanup = packages=%v cleanup=%+v err=%v", packages, cleanup, err)
	}
}

func TestParseLoopbackPort_FailClosed(t *testing.T) {
	for _, value := range []string{"0.0.0.0:49123", "[::1]:49123", "127.0.0.1:80", "127.0.0.1:not-a-port", "127.0.0.1:49123\n127.0.0.1:49124"} {
		if got, err := parseLoopbackPort(value, nil); err == nil || got != "" {
			t.Fatalf("parseLoopbackPort(%q) = %q, %v", value, got, err)
		}
	}
	if got, err := parseLoopbackPort("127.0.0.1:49123\n", nil); err != nil || got != "127.0.0.1:49123" {
		t.Fatalf("valid loopback = %q, %v", got, err)
	}
}

func TestParseDpkgStatus_BoundedInstalledProjection(t *testing.T) {
	data := []byte("Package: alpha\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1.2.3-1\nDescription: secret body ignored\n\nPackage: removed\nStatus: deinstall ok config-files\nVersion: 9\n\n")
	packages, err := parseDpkgStatus(data)
	if err != nil || len(packages) != 1 || packages[0].Name != "alpha" || packages[0].VersionInfo != "1.2.3-1" {
		t.Fatalf("parseDpkgStatus = %+v, %v", packages, err)
	}
	if _, err := parseDpkgStatus([]byte("Package: bad\x01\nStatus: install ok installed\nVersion: 1\n\n")); err == nil {
		t.Fatal("control-bearing package accepted")
	}
}

func testWorkerIdentity(imageID, reference, moduleDigest string) ImageIdentity {
	identity := ImageIdentity{ID: imageID, RepoDigests: []string{reference}, OS: "linux", Architecture: "amd64"}
	identity.Config.Labels = map[string]string{
		ContractLabelKey: ContractLabelValue, ProbeLabelKey: ProbePath, SupervisorLabelKey: SupervisorPath,
		GoVersionLabelKey: GoVersion, TinyGoVersionLabelKey: TinyGoVersion, BaseLabelKey: BaseImageID, ModuleLockLabelKey: "sha256:" + moduleDigest,
		ToolchainLockLabelKey: "sha256:" + expectedToolchainDigest(),
	}
	identity.Config.Env = []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOMODCACHE=/opt/buckley/modcache"}
	identity.Config.Entrypoint = []string{"/bin/sleep"}
	identity.Config.Cmd = []string{"infinity"}
	return identity
}
