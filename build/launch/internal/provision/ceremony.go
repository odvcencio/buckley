package provision

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	registryOwnerLabel   = "dev.m31labs.buckley.provision.owner"
	buildTimeout         = 45 * time.Minute
	dockerTimeout        = 2 * time.Minute
	registryTimeout      = 20 * time.Minute
	registryReadyTimeout = 10 * time.Second
	cleanupTimeout       = 30 * time.Second
)

var (
	containerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	localTagPattern    = regexp.MustCompile(`^buckley-launch-build-[0-9a-f]{32}$`)
	ownerPattern       = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type SealOptions struct {
	ContextPath string
	Artifacts   string
	Manifest    ContextManifest
	ModuleLock  ModuleLock
	Toolchain   ToolchainLock
	Runner      Runner
	readiness   registryReadiness
	Token       string
}

type SealResult struct {
	Contract       WorkerContract
	BuildMetadata  string
	SBOM           string
	Provenance     string
	OperatorConfig string
}

func BuildAndSeal(ctx context.Context, opts SealOptions) (result SealResult, returnErr error) {
	if opts.Runner == nil || opts.Toolchain.ValidateRuntime() != nil || !sha256Pattern.MatchString(opts.Manifest.ModuleLockSHA256) {
		return result, errors.New("sealed provisioning options are invalid")
	}
	contextRoot, err := canonicalDirectory(opts.ContextPath)
	if err != nil {
		return result, errors.New("sealed build context is invalid")
	}
	if err := verifyContextManifest(contextRoot, opts.Manifest); err != nil {
		return result, err
	}
	if err := verifyProvisionInputs(contextRoot, opts); err != nil {
		return result, err
	}
	if err := verifyBuilderContract(ctx, opts.Runner, opts.Toolchain); err != nil {
		return result, err
	}
	if err := createEmptyDirectory(opts.Artifacts); err != nil {
		return result, errors.New("artifact destination is invalid")
	}
	complete := false
	defer func() {
		if !complete || returnErr != nil {
			_ = os.RemoveAll(opts.Artifacts)
		}
	}()
	owner := opts.Token
	if owner == "" {
		owner, err = randomToken()
		if err != nil {
			return result, errors.New("provisioning identity is unavailable")
		}
	}
	if !ownerPattern.MatchString(owner) {
		return result, errors.New("provisioning identity is invalid")
	}
	localTag := "buckley-launch-build-" + owner
	localCleanupPending := true
	defer func() {
		if !localCleanupPending {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		cleanupErr := cleanupImageTag(cleanupCtx, opts.Runner, localTag)
		if cleanupErr != nil {
			cleanupFailure := fmt.Errorf("temporary build image cleanup required: %s", localTag)
			if returnErr == nil {
				returnErr = cleanupFailure
			} else {
				returnErr = errors.Join(returnErr, cleanupFailure)
			}
		}
	}()
	metadataPath := filepath.Join(opts.Artifacts, "buildkit-metadata.json")
	buildCtx, cancel := boundedContext(ctx, buildTimeout)
	_, err = opts.Runner.Run(buildCtx,
		"buildx", "build",
		"--builder", "default",
		"--platform", Platform,
		"--network", "default",
		"--no-cache",
		"--pull",
		"--provenance=mode=max",
		"--sbom=true",
		"--metadata-file", metadataPath,
		"--load",
		"--tag", localTag,
		"--build-arg", "BASE_REF="+opts.Toolchain.BaseRef,
		"--build-arg", "BASE_IMAGE_ID="+opts.Toolchain.BaseImageID,
		"--build-arg", "GO_VERSION="+opts.Toolchain.GoVersion,
		"--build-arg", "TINYGO_VERSION="+opts.Toolchain.TinyGoVersion,
		"--build-arg", "TINYGO_URL="+opts.Toolchain.TinyGoURL,
		"--build-arg", "TINYGO_SHA256="+opts.Toolchain.TinyGoSHA256,
		"--build-arg", "MODULE_LOCK_SHA256="+opts.Manifest.ModuleLockSHA256,
		"--build-arg", "TOOLCHAIN_LOCK_SHA256="+opts.Manifest.ToolchainSHA256,
		"--build-arg", "SOURCE_DATE_EPOCH="+strconv.FormatInt(opts.Toolchain.SourceDateEpoch, 10),
		contextRoot,
	)
	cancel()
	if err != nil {
		return result, errors.New("sealed worker image build failed")
	}
	if err := verifyContextManifest(contextRoot, opts.Manifest); err != nil {
		return result, err
	}
	if err := verifyProvisionInputs(contextRoot, opts); err != nil {
		return result, err
	}
	localIdentity, err := inspectImage(ctx, opts.Runner, localTag)
	if err != nil || validateWorkerIdentity(localIdentity, opts.Manifest.ModuleLockSHA256, "") != nil {
		return result, errors.New("built worker image failed its sealed contract")
	}
	readiness := opts.readiness
	if readiness == nil {
		readiness = httpRegistryReadiness{}
	}
	contract, sealErr := sealThroughLoopbackRegistry(ctx, opts.Runner, readiness, opts.Toolchain, localTag, localIdentity.ID, opts.Manifest.ModuleLockSHA256, owner)
	if sealErr != nil {
		return result, sealErr
	}
	result.Contract = contract
	if err := writeProvisionArtifacts(ctx, opts.Artifacts, opts, result, localIdentity); err != nil {
		return result, err
	}
	result.BuildMetadata = metadataPath
	result.SBOM = filepath.Join(opts.Artifacts, "sbom.spdx.json")
	result.Provenance = filepath.Join(opts.Artifacts, "provenance.json")
	result.OperatorConfig = filepath.Join(opts.Artifacts, "operator-config.yaml")
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
	cleanupErr := cleanupImageTag(cleanupCtx, opts.Runner, localTag)
	cleanupCancel()
	if cleanupErr != nil {
		return SealResult{}, fmt.Errorf("temporary build image cleanup required: %s", localTag)
	}
	localCleanupPending = false
	complete = true
	return result, nil
}

func verifyBuilderContract(ctx context.Context, runner Runner, toolchain ToolchainLock) error {
	operationCtx, cancel := boundedContext(ctx, dockerTimeout)
	defer cancel()
	version, err := runner.Run(operationCtx, "buildx", "version")
	wantVersion := "github.com/docker/buildx " + toolchain.BuildxVersion + " " + toolchain.BuildxCommit
	if err != nil || strings.TrimSpace(version) != wantVersion {
		return errors.New("sealed buildx contract mismatch")
	}
	inspect, err := runner.Run(operationCtx, "buildx", "inspect", "default")
	if err != nil || !exactBuilderLine(inspect, "Driver:", toolchain.BuildKitDriver) || !exactBuilderLine(inspect, "BuildKit version:", toolchain.BuildKitVersion) {
		return errors.New("sealed BuildKit contract mismatch")
	}
	return nil
}

func exactBuilderLine(output, prefix, expected string) bool {
	if len(output) > maxCommandOutput || strings.ContainsAny(expected, "\r\n\x00") {
		return false
	}
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
			if strings.TrimSpace(strings.TrimPrefix(line, prefix)) != expected {
				return false
			}
		}
	}
	return count == 1
}

func verifyContextManifest(root string, expected ContextManifest) error {
	if expected.Schema != ContextManifestType || !sha256Pattern.MatchString(expected.ContextSHA256) || !sha256Pattern.MatchString(expected.ModuleLockSHA256) || !sha256Pattern.MatchString(expected.ToolchainSHA256) || expected.Entries <= 0 || expected.Entries > maxContextEntries || expected.Bytes <= 0 || expected.Bytes > maxManifestTotal {
		return errors.New("sealed context manifest is invalid")
	}
	digest, entries, size, err := digestContext(root)
	if err != nil || digest != expected.ContextSHA256 || entries != expected.Entries || size != expected.Bytes {
		return errors.New("sealed build context changed after synthesis")
	}
	return nil
}

func verifyProvisionInputs(root string, opts SealOptions) error {
	if opts.ModuleLock.Schema != ModuleLockSchema || opts.Toolchain != ExpectedToolchain() {
		return errors.New("sealed input lock contract mismatch")
	}
	moduleBytes, err := canonicalJSON(opts.ModuleLock)
	if err != nil || digestBytes(moduleBytes) != opts.Manifest.ModuleLockSHA256 {
		return errors.New("module lock digest mismatch")
	}
	contextModuleBytes, err := readStableRegular(filepath.Join(root, "module-lock.json"), maxManifestBytes)
	if err != nil || !bytes.Equal(contextModuleBytes, moduleBytes) {
		return errors.New("context module lock mismatch")
	}
	contextToolchain, toolchainDigest, err := LoadToolchainLock(filepath.Join(root, "toolchain.lock"))
	if err != nil || contextToolchain != opts.Toolchain || toolchainDigest != opts.Manifest.ToolchainSHA256 {
		return errors.New("context toolchain lock mismatch")
	}
	license, err := readStableRegular(filepath.Join(root, "launch", "licenses", "TinyGo-LICENSE"), maxManifestBytes)
	if err != nil || digestBytes(license) != opts.Toolchain.TinyGoLicenseSHA256 || len(license) != 1835 || license[len(license)-1] != '\n' {
		return errors.New("context TinyGo license mismatch")
	}
	dockerfile, err := readStableRegular(filepath.Join(root, "Dockerfile"), maxManifestBytes)
	if err != nil || !bytes.HasPrefix(dockerfile, []byte("# syntax="+opts.Toolchain.DockerfileFrontend+"\n")) {
		return errors.New("context Dockerfile frontend mismatch")
	}
	return nil
}

func sealThroughLoopbackRegistry(ctx context.Context, runner Runner, readiness registryReadiness, toolchain ToolchainLock, localTag, imageID, moduleLockDigest, owner string) (contract WorkerContract, returnErr error) {
	if runner == nil || readiness == nil || !localTagPattern.MatchString(localTag) || !ownerPattern.MatchString(owner) || !imageIDPattern.MatchString(imageID) {
		return contract, errors.New("registry ceremony inputs are invalid")
	}
	operationCtx, cancel := boundedContext(ctx, registryTimeout)
	defer cancel()
	if _, err := runner.Run(operationCtx, "pull", toolchain.RegistryRef); err != nil {
		return contract, errors.New("pinned registry image is unavailable")
	}
	registryIdentity, err := inspectImage(operationCtx, runner, toolchain.RegistryRef)
	if err != nil || !containsString(registryIdentity.RepoDigests, toolchain.RegistryRef) || registryIdentity.OS != "linux" || registryIdentity.Architecture != "amd64" {
		return contract, errors.New("pinned registry image identity mismatch")
	}

	networkName := "buckley-launch-registry-net-" + owner
	registryName := "buckley-launch-registry-" + owner
	networkOutput, err := runner.Run(operationCtx, "network", "create", "--internal", "--label", registryOwnerLabel+"="+owner, networkName)
	networkID := strings.TrimSpace(networkOutput)
	if err != nil || !containerIDPattern.MatchString(networkID) {
		ownedID, ownershipErr := resolveOwnedNetwork(operationCtx, runner, networkName, owner)
		if ownershipErr != nil {
			return contract, errors.New("loopback registry network creation failed with cleanup pending")
		}
		networkID = ownedID
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		cleanupErr := cleanupNetwork(cleanupCtx, runner, networkID)
		cleanupCancel()
		if cleanupErr != nil {
			return contract, joinCleanupRequired(errors.New("loopback registry network creation failed"), "registry-network", networkID)
		}
		return contract, errors.New("loopback registry network creation failed")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if networkID == "" {
			return
		}
		if cleanupErr := cleanupNetwork(cleanupCtx, runner, networkID); cleanupErr != nil {
			returnErr = joinCleanupRequired(returnErr, "registry-network", networkID)
		}
	}()

	containerOutput, err := runner.Run(operationCtx,
		"create",
		"--name", registryName,
		"--label", registryOwnerLabel+"="+owner,
		"--network", networkName,
		"--pull", "never",
		"--read-only",
		"--tmpfs", "/var/lib/registry:size=1g",
		"--tmpfs", "/tmp:size=64m",
		"--publish", "127.0.0.1::5000",
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
		"--pids-limit", "128",
		"--memory", "256m",
		"--cpus", "0.5",
		toolchain.RegistryRef,
	)
	registryID := strings.TrimSpace(containerOutput)
	if err != nil || !containerIDPattern.MatchString(registryID) {
		ownedID, ownershipErr := resolveOwnedContainer(operationCtx, runner, registryName, owner, toolchain.RegistryRef, registryIdentity.ID)
		if ownershipErr != nil {
			return contract, errors.New("loopback registry creation failed with cleanup pending")
		}
		registryID = ownedID
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		cleanupErr := cleanupContainer(cleanupCtx, runner, registryID)
		cleanupCancel()
		if cleanupErr != nil {
			return contract, joinCleanupRequired(errors.New("loopback registry creation failed"), "registry-container", registryID)
		}
		return contract, errors.New("loopback registry creation failed")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if registryID == "" {
			return
		}
		if cleanupErr := cleanupContainer(cleanupCtx, runner, registryID); cleanupErr != nil {
			returnErr = joinCleanupRequired(returnErr, "registry-container", registryID)
		}
	}()
	if _, err := runner.Run(operationCtx, "start", "--", registryID); err != nil {
		return contract, errors.New("loopback registry start failed")
	}
	portOutput, err := runner.Run(operationCtx, "port", registryID, "5000/tcp")
	registryHost, err := parseLoopbackPort(portOutput, err)
	if err != nil {
		return contract, err
	}
	readyCtx, readyCancel := context.WithTimeout(operationCtx, registryReadyTimeout)
	readyErr := readiness.Wait(readyCtx, registryHost)
	readyCancel()
	if readyErr != nil {
		return contract, errors.New("loopback registry did not become ready")
	}
	remoteRepository := registryHost + "/buckley/oss-worker"
	remoteTag := remoteRepository + ":" + owner
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if cleanupErr := cleanupImageReference(cleanupCtx, runner, remoteTag); cleanupErr != nil {
			returnErr = joinCleanupRequired(returnErr, "registry-image-tag", remoteTag)
		}
	}()
	if _, err := runner.Run(operationCtx, "tag", localTag, remoteTag); err != nil {
		return contract, errors.New("worker image loopback tag failed")
	}
	if _, err := runner.Run(operationCtx, "push", remoteTag); err != nil {
		return contract, errors.New("worker image loopback push failed")
	}
	remoteIdentity, err := inspectImage(operationCtx, runner, remoteTag)
	if err != nil {
		return contract, errors.New("worker image loopback digest is unavailable")
	}
	digestRef := exactRepositoryDigest(remoteRepository, remoteIdentity.RepoDigests)
	if digestRef == "" {
		return contract, errors.New("worker image loopback digest is invalid")
	}
	if _, err := runner.Run(operationCtx, "pull", digestRef); err != nil {
		return contract, errors.New("worker image exact digest pull failed")
	}
	exactIdentity, err := inspectImage(operationCtx, runner, digestRef)
	if err != nil || exactIdentity.ID != imageID || validateWorkerIdentity(exactIdentity, moduleLockDigest, digestRef) != nil {
		return contract, errors.New("worker image exact digest contract failed")
	}

	// Remove the registry before the final inspection. The exact reference must
	// remain usable from Docker's local content store with no registry process.
	if err := cleanupContainer(operationCtx, runner, registryID); err != nil {
		return contract, errors.New("loopback registry stop failed")
	}
	registryID = ""
	if err := cleanupNetwork(operationCtx, runner, networkID); err != nil {
		return contract, errors.New("loopback registry network stop failed")
	}
	networkID = ""
	postStopIdentity, err := inspectImage(operationCtx, runner, digestRef)
	if err != nil || postStopIdentity.ID != imageID || validateWorkerIdentity(postStopIdentity, moduleLockDigest, digestRef) != nil {
		return contract, errors.New("worker image exact digest was not retained locally")
	}
	return WorkerContract{Schema: "buckley.launch.worker-contract.v1", Reference: digestRef, ImageID: imageID, OS: "linux", Architecture: "amd64", ModuleLockSHA256: moduleLockDigest, ToolchainLockSHA256: expectedToolchainDigest()}, nil
}

func inspectImage(ctx context.Context, runner Runner, reference string) (ImageIdentity, error) {
	if runner == nil || len(reference) == 0 || len(reference) > 512 || strings.ContainsAny(reference, "\r\n\x00") {
		return ImageIdentity{}, errors.New("image reference is invalid")
	}
	operationCtx, cancel := boundedContext(ctx, dockerTimeout)
	defer cancel()
	raw, err := runner.Run(operationCtx, "image", "inspect", reference)
	if err != nil || len(raw) > maxCommandOutput {
		return ImageIdentity{}, errors.New("image inspection failed")
	}
	return parseImageInspect(raw)
}

func parseLoopbackPort(output string, operationErr error) (string, error) {
	if operationErr != nil || len(output) > 128 {
		return "", errors.New("loopback registry port is unavailable")
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(output))
	if err != nil || host != "127.0.0.1" {
		return "", errors.New("registry did not bind to loopback")
	}
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value < 1024 {
		return "", errors.New("loopback registry port is invalid")
	}
	return net.JoinHostPort(host, port), nil
}

func exactRepositoryDigest(repository string, repoDigests []string) string {
	prefix := repository + "@sha256:"
	for _, candidate := range repoDigests {
		if strings.HasPrefix(candidate, prefix) && len(candidate) == len(prefix)+64 && validDigest(strings.TrimPrefix(candidate, repository+"@")) {
			return candidate
		}
	}
	return ""
}

func randomToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func imageIdentityJSON(identity ImageIdentity) string {
	data, _ := json.Marshal([]ImageIdentity{identity})
	return string(data)
}

func safeReference(reference string) string {
	if len(reference) > 512 || strings.ContainsAny(reference, "\r\n\x00") {
		return ""
	}
	return reference
}

func boundedName(prefix, owner string) string {
	if !ownerPattern.MatchString(owner) {
		return ""
	}
	return fmt.Sprintf("%s%s", prefix, owner)
}
