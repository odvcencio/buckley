package launchimage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/dockersandbox"
	"m31labs.dev/buckley/pkg/launchcontract"
)

type launchTestSandbox struct {
	mu         sync.Mutex
	identity   *dockersandbox.ImageIdentity
	imageErr   error
	imageCalls int
}

func (s *launchTestSandbox) InspectImage(context.Context) (dockersandbox.ImageIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imageCalls++
	if s.identity == nil {
		return dockersandbox.ImageIdentity{}, s.imageErr
	}
	return *s.identity, s.imageErr
}

func TestVerifier_ValidatesExactSealedArtifactsAndImage(t *testing.T) {
	contract, identity := writeLaunchArtifactFixture(t)
	sandbox := &launchTestSandbox{identity: &identity}
	observer, err := newVerifier(contract, sandbox, strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	profile, err := launchcontract.ResolveProfile("gosx")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	proof, err := observer.Verify(context.Background(), workspace, profile)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	observation := proof.Snapshot()
	if observation.Reference != contract.Reference || observation.ImageID != contract.ImageID || observation.ModuleLockSHA256 != contract.ModuleLockSHA256 || observation.GoVersion != launchGoVersion || observation.TinyGoVersion != launchTinyGoVersion {
		t.Fatalf("observation = %+v", observation)
	}
	sandbox.mu.Lock()
	imageCalls := sandbox.imageCalls
	sandbox.mu.Unlock()
	if imageCalls != 1 {
		t.Fatalf("image inspect calls = %d", imageCalls)
	}
}

func TestVerifier_FailsClosedOnArtifactsImageOverlapAndCancellation(t *testing.T) {
	profile, err := launchcontract.ResolveProfile("gsxmail")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, config.LaunchWorkerImageConfig, *dockersandbox.ImageIdentity)
	}{
		{name: "operator snippet", mutate: func(t *testing.T, c config.LaunchWorkerImageConfig, _ *dockersandbox.ImageIdentity) {
			writeArtifactTestFile(t, c.ArtifactDir, "operator-config.yaml", []byte("launch: {}\n"))
		}},
		{name: "unknown contract field", mutate: func(t *testing.T, c config.LaunchWorkerImageConfig, _ *dockersandbox.ImageIdentity) {
			writeArtifactTestFile(t, c.ArtifactDir, "operator-contract.json", []byte(`{"schema":"buckley.launch.worker-contract.v1","extra":true}`))
		}},
		{name: "trailing metadata", mutate: func(t *testing.T, c config.LaunchWorkerImageConfig, _ *dockersandbox.ImageIdentity) {
			path := filepath.Join(c.ArtifactDir, "buildkit-metadata.json")
			data, _ := os.ReadFile(path)
			writeArtifactTestFile(t, c.ArtifactDir, "buildkit-metadata.json", append(data, []byte("\n{}")...))
		}},
		{name: "secret artifact", mutate: func(t *testing.T, c config.LaunchWorkerImageConfig, _ *dockersandbox.ImageIdentity) {
			writeArtifactTestFile(t, c.ArtifactDir, "sbom.spdx.json", []byte(`{"authorization":"Bearer aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5z"}`))
		}},
		{name: "module drift", mutate: func(t *testing.T, c config.LaunchWorkerImageConfig, _ *dockersandbox.ImageIdentity) {
			path := filepath.Join(c.ArtifactDir, "module-lock.json")
			data, _ := os.ReadFile(path)
			data[len(data)-2] ^= 1
			writeArtifactTestFile(t, c.ArtifactDir, "module-lock.json", data)
		}},
		{name: "six module root coverage", mutate: func(t *testing.T, c config.LaunchWorkerImageConfig, _ *dockersandbox.ImageIdentity) {
			path := filepath.Join(c.ArtifactDir, "module-lock.json")
			data, _ := os.ReadFile(path)
			var modules artifactModuleLock
			if err := json.Unmarshal(data, &modules); err != nil {
				t.Fatal(err)
			}
			modules.Repositories[1].Manifests = modules.Repositories[1].Manifests[:5]
			writeArtifactTestFile(t, c.ArtifactDir, "module-lock.json", marshalArtifactFixture(t, modules))
		}},
		{name: "toolchain bytes", mutate: func(t *testing.T, c config.LaunchWorkerImageConfig, _ *dockersandbox.ImageIdentity) {
			path := filepath.Join(c.ArtifactDir, "toolchain-lock.json")
			data, _ := os.ReadFile(path)
			var toolchain artifactToolchain
			if err := json.Unmarshal(data, &toolchain); err != nil {
				t.Fatal(err)
			}
			toolchain.SourceDateEpoch++
			writeArtifactTestFile(t, c.ArtifactDir, "toolchain-lock.json", marshalArtifactFixture(t, toolchain))
		}},
		{name: "image identity", mutate: func(_ *testing.T, _ config.LaunchWorkerImageConfig, i *dockersandbox.ImageIdentity) {
			i.Labels[launchContractLabelKey] = "attacker"
		}},
		{name: "toolchain image label", mutate: func(_ *testing.T, _ config.LaunchWorkerImageConfig, i *dockersandbox.ImageIdentity) {
			i.Labels[launchToolchainLockLabelKey] = "sha256:" + strings.Repeat("0", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			contract, identity := writeLaunchArtifactFixture(t)
			test.mutate(t, contract, &identity)
			observer, err := newVerifier(contract, &launchTestSandbox{identity: &identity}, strconv.Itoa(os.Getuid()))
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			if _, err := observer.Verify(context.Background(), t.TempDir(), profile); err == nil {
				t.Fatal("invalid image evidence was admitted")
			}
		})
	}

	contract, identity := writeLaunchArtifactFixture(t)
	sandbox := &launchTestSandbox{identity: &identity}
	observer, err := newVerifier(contract, sandbox, strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observer.Verify(ctx, t.TempDir(), profile); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled observation error = %v", err)
	}
	sandbox.mu.Lock()
	if sandbox.imageCalls != 0 {
		t.Fatalf("canceled observation inspected image %d times", sandbox.imageCalls)
	}
	sandbox.mu.Unlock()
	if _, err := observer.Verify(context.Background(), contract.ArtifactDir, profile); err == nil {
		t.Fatal("workspace/artifact overlap was admitted")
	}
}

func TestReadStableLaunchArtifact_RejectsSymlinkHardlinkAndMutation(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writeArtifactTestFile(t, rootPath, "value.json", []byte(`{"value":1}`))
	if _, err := readStableLaunchArtifactWithHook(root, "value.json", 1024, func() {
		writeArtifactTestFile(t, rootPath, "value.json", []byte(`{"value":2}`))
	}); err == nil {
		t.Fatal("mutated artifact was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"value":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(rootPath, "value.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(rootPath, "value.json")); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if _, err := readStableLaunchArtifact(root, "value.json", 1024); err == nil {
		t.Fatal("hardlinked artifact was accepted")
	}
	if err := os.Remove(filepath.Join(rootPath, "value.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "value.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := readStableLaunchArtifact(root, "value.json", 1024); err == nil {
		t.Fatal("symlinked artifact was accepted")
	}
}

func writeLaunchArtifactFixture(t *testing.T) (config.LaunchWorkerImageConfig, dockersandbox.ImageIdentity) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	modules := artifactModuleLock{Schema: launchModuleSchema}
	for index, spec := range []struct {
		name  string
		paths []string
	}{
		{name: "gsxmail", paths: []string{"go.mod", "go.sum", "LICENSE"}},
		{name: "gosx", paths: []string{"go.mod", "go.sum", "LICENSE", "editor/go.mod", "editor/go.sum", "cmd/buildbootstrap/go.mod", "cmd/buildbootstrap/go.sum"}},
		{name: "tqwebp", paths: []string{"go.mod", "go.sum", "LICENSE", "bench/deepteams/go.mod", "bench/deepteams/go.sum"}},
	} {
		repo := artifactRepositoryLock{Name: spec.name, Commit: strings.Repeat(strconv.Itoa(index+1), 40)}
		for manifestIndex, path := range spec.paths {
			repo.Manifests = append(repo.Manifests, artifactManifestLock{Path: path, SHA256: strings.Repeat(string(rune('a'+index)), 64), Bytes: int64(manifestIndex + 1)})
		}
		modules.Repositories = append(modules.Repositories, repo)
	}
	moduleBytes := marshalArtifactFixture(t, modules)
	moduleDigest := digestData(moduleBytes)
	toolchain := testArtifactToolchain()
	toolchainBytes := marshalArtifactFixture(t, toolchain)
	manifestDigest := "sha256:" + strings.Repeat("7", 64)
	contract := config.LaunchWorkerImageConfig{
		Reference: "127.0.0.1:5000/buckley/worker@" + manifestDigest,
		ImageID:   "sha256:" + strings.Repeat("8", 64), OS: "linux", Architecture: "amd64",
		ModuleLockSHA256: moduleDigest, ToolchainLockSHA256: digestData(toolchainBytes), ArtifactDir: root,
	}
	identity := validLaunchImageIdentityFor(contract)
	worker := artifactWorker{Schema: workerEvidenceSchema, Reference: contract.Reference, ImageID: contract.ImageID, OS: contract.OS, Architecture: contract.Architecture, ModuleLockSHA256: contract.ModuleLockSHA256, ToolchainLockSHA256: contract.ToolchainLockSHA256}
	metadata := map[string]string{"containerimage.digest": manifestDigest, "containerimage.config.digest": contract.ImageID}
	metadataBytes := marshalArtifactFixture(t, metadata)
	contextManifest := artifactContext{Schema: launchContextSchema, ContextSHA256: strings.Repeat("4", 64), ModuleLockSHA256: moduleDigest, ToolchainSHA256: digestData(toolchainBytes), Entries: 12, Bytes: 4096}
	manifest := strings.TrimPrefix(manifestDigest, "sha256:")
	sbom := map[string]any{
		"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT", "name": "buckley-oss-worker",
		"documentNamespace": "https://m31labs.dev/buckley/launch/sbom/" + manifest,
		"creationInfo":      map[string]any{"created": time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC).Format(time.RFC3339), "creators": []string{"Tool: buckley-launch-provision-v1"}},
		"packages":          []map[string]any{{"SPDXID": "SPDXRef-Package-worker", "name": "buckley-oss-worker", "versionInfo": manifest, "downloadLocation": contract.Reference, "filesAnalyzed": false, "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION", "primaryPackagePurpose": "CONTAINER"}},
		"relationships":     []map[string]any{{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES", "relatedSpdxElement": "SPDXRef-Package-worker"}},
	}
	sbomBytes := marshalArtifactFixture(t, sbom)
	provenance := artifactProvenance{
		Schema: launchProvenanceSchema, Worker: worker, Toolchain: toolchain, Context: contextManifest, Modules: modules,
		BuildkitMetadata: "buildkit-metadata.json", BuildkitMetadataSHA: digestData(metadataBytes),
		BuildResultDigest: manifestDigest, BuildConfigDigest: contract.ImageID,
		SBOM: "sbom.spdx.json", SBOMSHA256: digestData(sbomBytes),
	}
	operatorConfig := []byte("launch:\n  worker_image:\n    reference: " + strconv.Quote(contract.Reference) + "\n    image_id: " + strconv.Quote(contract.ImageID) + "\n    os: linux\n    architecture: amd64\n    module_lock_sha256: " + strconv.Quote(contract.ModuleLockSHA256) + "\n    toolchain_lock_sha256: " + strconv.Quote(contract.ToolchainLockSHA256) + "\n    artifact_dir: " + strconv.Quote(root) + "\n")
	for name, data := range map[string][]byte{
		"operator-config.yaml": operatorConfig, "operator-contract.json": marshalArtifactFixture(t, worker),
		"buildkit-metadata.json": metadataBytes, "module-lock.json": moduleBytes,
		"toolchain-lock.json": toolchainBytes,
		"sbom.spdx.json":      sbomBytes, "provenance.json": marshalArtifactFixture(t, provenance),
	} {
		writeArtifactTestFile(t, root, name, data)
	}
	return contract, identity
}

func validLaunchImageIdentityFor(contract config.LaunchWorkerImageConfig) dockersandbox.ImageIdentity {
	return dockersandbox.ImageIdentity{
		ID: contract.ImageID, RepoDigests: []string{contract.Reference}, OS: contract.OS, Architecture: contract.Architecture,
		Labels: map[string]string{
			launchContractLabelKey: workerContract, launchProbeLabelKey: launchProbePath,
			launchSupervisorLabelKey: launchSupervisorPath, launchGoVersionLabelKey: launchGoVersion,
			launchTinyGoLabelKey: launchTinyGoVersion, launchBaseLabelKey: launchBaseImageID,
			launchModuleLockLabelKey:    "sha256:" + contract.ModuleLockSHA256,
			launchToolchainLockLabelKey: "sha256:" + contract.ToolchainLockSHA256,
		},
		Env:        []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOMODCACHE=/opt/buckley/modcache"},
		Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"infinity"},
	}
}

func testArtifactToolchain() artifactToolchain {
	return artifactToolchain{
		Schema: launchToolchainSchema, Platform: "linux/amd64", BaseRef: launchBaseReference, BaseImageID: launchBaseImageID,
		GoVersion: launchGoVersion, TinyGoVersion: launchTinyGoVersion, TinyGoLLVMVersion: "20.1.1",
		TinyGoURL: launchTinyGoURL, TinyGoSHA256: launchTinyGoSHA256,
		TinyGoLicenseURL: launchTinyGoLicenseURL, TinyGoLicenseSHA256: launchTinyGoLicenseSHA256,
		DockerfileFrontend: launchDockerfileFrontend, BuildxVersion: "v0.28.0", BuildxCommit: "b1281b81bba797b21d9eaf256e6a13eb14419836",
		BuildKitVersion: "v0.24.0", BuildKitDriver: "docker", SourceDateEpoch: 946684800,
		RegistryRef: launchRegistryReference, RegistryVersion: "3.0.0", RegistryLicense: "Apache-2.0",
	}
}

func marshalArtifactFixture(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeArtifactTestFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
