package launchimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/dockersandbox"
	"m31labs.dev/buckley/pkg/launchcontract"
	"m31labs.dev/buckley/pkg/secretsafety"
)

const (
	maxLaunchArtifactBytes      = 32 << 20
	maxLaunchArtifactTotalBytes = 64 << 20
	maxLaunchArtifactEntries    = 16_384
	launchBaseReference         = "golang:1.26-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36"
	launchToolchainSchema       = "buckley.launch.toolchain.v1"
	launchContextSchema         = "buckley.launch.context.v1"
	launchModuleSchema          = "buckley.launch.modules.v1"
	launchProvenanceSchema      = "buckley.launch.provenance.v1"
	launchTinyGoURL             = "https://github.com/tinygo-org/tinygo/releases/download/v0.41.1/tinygo0.41.1.linux-amd64.tar.gz"
	launchTinyGoSHA256          = "e156d1d93a376eef639a4143d13be07e8c463fb6cf2d7d447698ed4474d23e91"
	launchTinyGoLicenseURL      = "https://raw.githubusercontent.com/tinygo-org/tinygo/v0.41.1/LICENSE"
	launchTinyGoLicenseSHA256   = "4cb7d99a97ebd57584ea8398898c4b0bbbcb39662330712b43153efdad308766"
	launchDockerfileFrontend    = "docker/dockerfile:1.12@sha256:93bfd3b68c109427185cd78b4779fc82b484b0b7618e36d0f104d4d801e66d25"
	launchRegistryReference     = "docker.io/library/registry@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e"
	WorkerEvidenceSchema        = "buckley.launch.worker-contract.v1"
	WorkerContract              = "worker-v1"
	WorkerOS                    = "linux"
	WorkerArchitecture          = "amd64"
	WorkerGoVersion             = "1.26.6"
	WorkerTinyGoVersion         = "0.41.1"
	trustedDockerBinary         = "/usr/bin/docker"
	ContractLabelKey            = "dev.m31labs.buckley.launch.contract"
	ProbeLabelKey               = "dev.m31labs.buckley.launch.probe"
	ProbePath                   = "/usr/local/bin/buckley-launch-probe-v1"
	SupervisorLabelKey          = "dev.m31labs.buckley.launch.supervisor"
	SupervisorPath              = "/usr/local/bin/buckley-launch-supervisor-v1"
	GoVersionLabelKey           = "dev.m31labs.buckley.launch.go-version"
	TinyGoLabelKey              = "dev.m31labs.buckley.launch.tinygo-version"
	BaseLabelKey                = "dev.m31labs.buckley.launch.base"
	BaseImageID                 = "sha256:df664c2b56a98910721a529a9a74e20181c607ac32528e758a1dcfd522a9f011"
	ModuleLockLabelKey          = "dev.m31labs.buckley.launch.module-lock"
	ToolchainLockLabelKey       = "dev.m31labs.buckley.launch.toolchain-lock"
	workerEvidenceSchema        = WorkerEvidenceSchema
	workerContract              = WorkerContract
	workerOS                    = WorkerOS
	workerArchitecture          = WorkerArchitecture
	workerGoVersion             = WorkerGoVersion
	workerTinyGoVersion         = WorkerTinyGoVersion
	launchContractLabelKey      = ContractLabelKey
	launchProbeLabelKey         = ProbeLabelKey
	launchProbePath             = ProbePath
	launchSupervisorLabelKey    = SupervisorLabelKey
	launchSupervisorPath        = SupervisorPath
	launchGoVersionLabelKey     = GoVersionLabelKey
	launchTinyGoLabelKey        = TinyGoLabelKey
	launchBaseLabelKey          = BaseLabelKey
	launchBaseImageID           = BaseImageID
	launchModuleLockLabelKey    = ModuleLockLabelKey
	launchToolchainLockLabelKey = ToolchainLockLabelKey
	launchGoVersion             = WorkerGoVersion
	launchTinyGoVersion         = WorkerTinyGoVersion
)

var (
	launchImagePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,511}$`)
	launchDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var launchArtifactFiles = map[string]int64{
	"operator-config.yaml":   2048,
	"operator-contract.json": 16 << 10,
	"buildkit-metadata.json": 16 << 10,
	"module-lock.json":       8 << 20,
	"toolchain-lock.json":    16 << 10,
	"sbom.spdx.json":         maxLaunchArtifactBytes,
	"provenance.json":        maxLaunchArtifactBytes,
}

type imageInspector interface {
	InspectImage(context.Context) (dockersandbox.ImageIdentity, error)
}

// Projection is the bounded, non-authoritative public view of a verified
// worker image. Only Proof is accepted by launch admission.
type Projection struct {
	Schema              string
	Contract            string
	Reference           string
	ImageID             string
	ManifestDigest      string
	ConfigDigest        string
	SBOMSHA256          string
	ProvenanceSHA256    string
	OS                  string
	Architecture        string
	ContextSHA256       string
	ModuleLockSHA256    string
	ToolchainLockSHA256 string
	GoVersion           string
	TinyGoVersion       string
}

// Proof has package-private state and can only be produced by Verifier.
type Proof struct{ projection Projection }

func (p Proof) Snapshot() Projection { return p.projection }

type rootBinding struct {
	file   *os.File
	source string
	info   os.FileInfo
}

func (b *rootBinding) Source() string {
	if b == nil {
		return ""
	}
	return b.source
}

func (b *rootBinding) Close() error {
	if b == nil || b.file == nil {
		return nil
	}
	err := b.file.Close()
	b.file = nil
	return err
}

// Verifier retains a no-follow handle to the operator-owned sealed
// artifacts and re-inspects the exact local repo@digest before every
// admission. It never pulls, creates, or starts a container.
type Verifier struct {
	contract  config.LaunchWorkerImageConfig
	inspector imageInspector
	binding   *rootBinding
	root      *os.Root
	rootInfo  os.FileInfo
}

func NewVerifier(contract config.LaunchWorkerImageConfig) (*Verifier, error) {
	if _, err := resolveTrustedDockerBinary(); err != nil {
		return nil, errors.New("launchimage: trusted Docker image inspector is unavailable")
	}
	current, err := user.Current()
	if err != nil || validateLaunchUser(current) != nil {
		return nil, errors.New("launchimage: observer account is unavailable")
	}
	if err := ValidateContract(contract); err != nil {
		return nil, err
	}
	inspector := dockersandbox.New(config.DockerSandboxConfig{
		Image: contract.Reference, Binary: trustedDockerBinary,
		MaxOutputBytes: 1 << 20, IsolatedClientEnv: true,
	}, dockersandbox.WithLaunchAdmission())
	return newVerifier(contract, inspector, current.Uid)
}

func newVerifier(contract config.LaunchWorkerImageConfig, inspector imageInspector, uidText string) (*Verifier, error) {
	if inspector == nil || validateLaunchImageContract(contract) != nil || contract.ArtifactDir == "" {
		return nil, errors.New("launchimage: evidence source is invalid")
	}
	uid, err := strconv.ParseUint(uidText, 10, 32)
	if err != nil || uid == 0 {
		return nil, errors.New("launchimage: observer account is invalid")
	}
	if err := validateLaunchArtifactPath(contract.ArtifactDir, uint32(uid)); err != nil {
		return nil, errors.New("launchimage: artifact directory is unavailable")
	}
	binding, err := openRootBinding(contract.ArtifactDir)
	if err != nil {
		return nil, errors.New("launchimage: artifact binding is unavailable")
	}
	root, err := os.OpenRoot(binding.Source())
	if err != nil {
		_ = binding.Close()
		return nil, errors.New("launchimage: artifact root is unavailable")
	}
	info, err := os.Stat(binding.Source())
	if err != nil || !info.IsDir() {
		_ = root.Close()
		_ = binding.Close()
		return nil, errors.New("launchimage: artifact identity is unavailable")
	}
	return &Verifier{contract: contract, inspector: inspector, binding: binding, root: root, rootInfo: info}, nil
}

func (o *Verifier) Close() error {
	if o == nil {
		return nil
	}
	var errs []error
	if o.root != nil {
		errs = append(errs, o.root.Close())
		o.root = nil
	}
	if o.binding != nil {
		errs = append(errs, o.binding.Close())
		o.binding = nil
	}
	return errors.Join(errs...)
}

func (o *Verifier) Verify(ctx context.Context, workspaceRoot string, profile launchcontract.ProfileDescriptor) (Proof, error) {
	if o == nil || o.root == nil || o.binding == nil || o.inspector == nil || profile.Validate() != nil || workspaceRoot == "" {
		return Proof{}, errors.New("launchimage: verifier is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Proof{}, err
	}
	if launchPathsOverlap(workspaceRoot, o.contract.ArtifactDir) || !o.rootStillBound() {
		return Proof{}, errors.New("launchimage: artifact authority overlaps or changed")
	}
	identity, err := o.inspector.InspectImage(ctx)
	if err != nil || validateLaunchImageIdentity(o.contract, identity) != nil {
		return Proof{}, errors.New("launchimage: sealed image inspection failed")
	}
	artifacts := make(map[string][]byte, len(launchArtifactFiles))
	total := int64(0)
	for _, name := range []string{"operator-config.yaml", "operator-contract.json", "buildkit-metadata.json", "module-lock.json", "toolchain-lock.json", "sbom.spdx.json", "provenance.json"} {
		if err := ctx.Err(); err != nil {
			return Proof{}, err
		}
		data, err := readStableLaunchArtifact(o.root, name, launchArtifactFiles[name])
		if err != nil || secretsafety.SecretContent(data) {
			return Proof{}, errors.New("launchimage: sealed artifact is invalid")
		}
		total += int64(len(data))
		if total > maxLaunchArtifactTotalBytes {
			return Proof{}, errors.New("launchimage: sealed artifacts exceed their bound")
		}
		artifacts[name] = data
	}
	observation, err := validateLaunchArtifacts(o.contract, identity, artifacts)
	if err != nil || !o.rootStillBound() {
		return Proof{}, errors.New("launchimage: sealed artifact linkage is invalid")
	}
	return Proof{projection: observation}, nil
}

func (o *Verifier) rootStillBound() bool {
	if o == nil || o.rootInfo == nil || o.binding == nil {
		return false
	}
	current, err := os.Lstat(o.contract.ArtifactDir)
	return err == nil && current.Mode()&os.ModeSymlink == 0 && current.IsDir() && os.SameFile(o.rootInfo, current)
}

func launchPathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return contains(left, right) || contains(right, left)
}

func readStableLaunchArtifact(root *os.Root, name string, maximum int64) ([]byte, error) {
	return readStableLaunchArtifactWithHook(root, name, maximum, nil)
}

func readStableLaunchArtifactWithHook(root *os.Root, name string, maximum int64, afterFirst func()) ([]byte, error) {
	if root == nil || filepath.Base(name) != name || maximum <= 0 || maximum > maxLaunchArtifactBytes {
		return nil, errors.New("artifact request is invalid")
	}
	before, err := root.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || launchFileHasMultipleLinks(before) || before.Size() <= 0 || before.Size() > maximum {
		return nil, errors.New("artifact is unavailable")
	}
	read := func() ([]byte, os.FileInfo, error) {
		file, err := root.Open(name)
		if err != nil {
			return nil, nil, err
		}
		opened, statErr := file.Stat()
		data, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
		afterRead, afterErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || readErr != nil || afterErr != nil || closeErr != nil || int64(len(data)) != opened.Size() || int64(len(data)) > maximum || !stableArtifactInfo(before, opened) || !stableArtifactInfo(opened, afterRead) {
			return nil, nil, errors.New("artifact changed during read")
		}
		return data, afterRead, nil
	}
	first, firstInfo, err := read()
	if err != nil {
		return nil, err
	}
	if afterFirst != nil {
		afterFirst()
	}
	second, secondInfo, err := read()
	after, afterErr := root.Lstat(name)
	if err != nil || afterErr != nil || !bytes.Equal(first, second) || !stableArtifactInfo(firstInfo, secondInfo) || !stableArtifactInfo(secondInfo, after) {
		return nil, errors.New("artifact changed during read")
	}
	return first, nil
}

func stableArtifactInfo(left, right os.FileInfo) bool {
	if left == nil || right == nil || !left.Mode().IsRegular() || !right.Mode().IsRegular() || launchFileHasMultipleLinks(left) || launchFileHasMultipleLinks(right) || !os.SameFile(left, right) || left.Size() != right.Size() || left.Mode() != right.Mode() || !left.ModTime().Equal(right.ModTime()) {
		return false
	}
	leftSec, leftNsec, leftOK := launchFileChangeIdentity(left)
	rightSec, rightNsec, rightOK := launchFileChangeIdentity(right)
	return leftOK == rightOK && (!leftOK || leftSec == rightSec && leftNsec == rightNsec)
}

type artifactWorker struct {
	Schema              string `json:"schema"`
	Reference           string `json:"reference"`
	ImageID             string `json:"image_id"`
	OS                  string `json:"os"`
	Architecture        string `json:"architecture"`
	ModuleLockSHA256    string `json:"module_lock_sha256"`
	ToolchainLockSHA256 string `json:"toolchain_lock_sha256"`
}

type artifactManifestLock struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type artifactRepositoryLock struct {
	Name      string                 `json:"name"`
	Commit    string                 `json:"commit"`
	Manifests []artifactManifestLock `json:"manifests"`
}

type artifactModuleLock struct {
	Schema       string                   `json:"schema"`
	Repositories []artifactRepositoryLock `json:"repositories"`
}

type artifactContext struct {
	Schema           string `json:"schema"`
	ContextSHA256    string `json:"context_sha256"`
	ModuleLockSHA256 string `json:"module_lock_sha256"`
	ToolchainSHA256  string `json:"toolchain_lock_sha256"`
	Entries          int    `json:"entries"`
	Bytes            int64  `json:"bytes"`
}

type artifactToolchain struct {
	Schema              string `json:"schema"`
	Platform            string `json:"platform"`
	BaseRef             string `json:"base_ref"`
	BaseImageID         string `json:"base_image_id"`
	GoVersion           string `json:"go_version"`
	TinyGoVersion       string `json:"tinygo_version"`
	TinyGoLLVMVersion   string `json:"tinygo_llvm_version"`
	TinyGoURL           string `json:"tinygo_url"`
	TinyGoSHA256        string `json:"tinygo_sha256"`
	TinyGoLicenseURL    string `json:"tinygo_license_url"`
	TinyGoLicenseSHA256 string `json:"tinygo_license_sha256"`
	DockerfileFrontend  string `json:"dockerfile_frontend"`
	BuildxVersion       string `json:"buildx_version"`
	BuildxCommit        string `json:"buildx_commit"`
	BuildKitVersion     string `json:"buildkit_version"`
	BuildKitDriver      string `json:"buildkit_driver"`
	SourceDateEpoch     int64  `json:"source_date_epoch"`
	RegistryRef         string `json:"registry_ref"`
	RegistryVersion     string `json:"registry_version"`
	RegistryLicense     string `json:"registry_license"`
}

type artifactProvenance struct {
	Schema              string             `json:"schema"`
	Worker              artifactWorker     `json:"worker"`
	Toolchain           artifactToolchain  `json:"toolchain"`
	Context             artifactContext    `json:"context"`
	Modules             artifactModuleLock `json:"modules"`
	BuildkitMetadata    string             `json:"buildkit_metadata"`
	BuildkitMetadataSHA string             `json:"buildkit_metadata_sha256"`
	BuildResultDigest   string             `json:"build_result_digest"`
	BuildConfigDigest   string             `json:"build_config_digest"`
	SBOM                string             `json:"sbom"`
	SBOMSHA256          string             `json:"sbom_sha256"`
}

type artifactSPDX struct {
	SPDXVersion       string `json:"spdxVersion"`
	DataLicense       string `json:"dataLicense"`
	SPDXID            string `json:"SPDXID"`
	Name              string `json:"name"`
	DocumentNamespace string `json:"documentNamespace"`
	CreationInfo      struct {
		Created  string   `json:"created"`
		Creators []string `json:"creators"`
	} `json:"creationInfo"`
	Packages []struct {
		SPDXID             string `json:"SPDXID"`
		Name               string `json:"name"`
		VersionInfo        string `json:"versionInfo,omitempty"`
		DownloadLocation   string `json:"downloadLocation"`
		FilesAnalyzed      bool   `json:"filesAnalyzed"`
		LicenseConcluded   string `json:"licenseConcluded"`
		LicenseDeclared    string `json:"licenseDeclared"`
		PrimaryPackageType string `json:"primaryPackagePurpose,omitempty"`
	} `json:"packages"`
	Relationships []struct {
		SPDXElementID      string `json:"spdxElementId"`
		RelationshipType   string `json:"relationshipType"`
		RelatedSPDXElement string `json:"relatedSpdxElement"`
	} `json:"relationships"`
}

func validateLaunchArtifacts(contract config.LaunchWorkerImageConfig, identity dockersandbox.ImageIdentity, values map[string][]byte) (Projection, error) {
	expectedConfig := fmt.Sprintf("launch:\n  worker_image:\n    reference: %q\n    image_id: %q\n    os: linux\n    architecture: amd64\n    module_lock_sha256: %q\n    toolchain_lock_sha256: %q\n    artifact_dir: %q\n", contract.Reference, contract.ImageID, contract.ModuleLockSHA256, contract.ToolchainLockSHA256, contract.ArtifactDir)
	if !bytes.Equal(values["operator-config.yaml"], []byte(expectedConfig)) {
		return Projection{}, errors.New("operator snippet mismatch")
	}
	var worker artifactWorker
	if strictLaunchArtifactJSON(values["operator-contract.json"], &worker) != nil || worker != (artifactWorker{Schema: workerEvidenceSchema, Reference: contract.Reference, ImageID: contract.ImageID, OS: contract.OS, Architecture: contract.Architecture, ModuleLockSHA256: contract.ModuleLockSHA256, ToolchainLockSHA256: contract.ToolchainLockSHA256}) {
		return Projection{}, errors.New("worker contract mismatch")
	}
	metadata := map[string]string{}
	if strictLaunchArtifactJSON(values["buildkit-metadata.json"], &metadata) != nil || len(metadata) != 2 || metadata["containerimage.digest"] != strings.TrimPrefix(contract.Reference[strings.LastIndex(contract.Reference, "@")+1:], "") || metadata["containerimage.config.digest"] != contract.ImageID {
		return Projection{}, errors.New("build metadata mismatch")
	}
	var modules artifactModuleLock
	if strictLaunchArtifactJSON(values["module-lock.json"], &modules) != nil || validateArtifactModules(modules) != nil || digestData(values["module-lock.json"]) != contract.ModuleLockSHA256 {
		return Projection{}, errors.New("module lock mismatch")
	}
	var provenance artifactProvenance
	if strictLaunchArtifactJSON(values["provenance.json"], &provenance) != nil || provenance.Schema != launchProvenanceSchema || provenance.Worker != worker || !reflect.DeepEqual(provenance.Modules, modules) {
		return Projection{}, errors.New("provenance mismatch")
	}
	if provenance.BuildkitMetadata != "buildkit-metadata.json" || provenance.BuildkitMetadataSHA != digestData(values["buildkit-metadata.json"]) || provenance.BuildResultDigest != metadata["containerimage.digest"] || provenance.BuildConfigDigest != metadata["containerimage.config.digest"] || provenance.SBOM != "sbom.spdx.json" || provenance.SBOMSHA256 != digestData(values["sbom.spdx.json"]) {
		return Projection{}, errors.New("provenance linkage mismatch")
	}
	if validateArtifactContext(provenance.Context, contract, values["module-lock.json"]) != nil || validateArtifactToolchain(provenance.Toolchain) != nil {
		return Projection{}, errors.New("toolchain or context mismatch")
	}
	var toolchainFile artifactToolchain
	if strictLaunchArtifactJSON(values["toolchain-lock.json"], &toolchainFile) != nil || toolchainFile != provenance.Toolchain || digestData(values["toolchain-lock.json"]) != contract.ToolchainLockSHA256 || contract.ToolchainLockSHA256 != provenance.Context.ToolchainSHA256 {
		return Projection{}, errors.New("toolchain digest mismatch")
	}
	var sbom artifactSPDX
	if strictLaunchArtifactJSON(values["sbom.spdx.json"], &sbom) != nil || validateArtifactSBOM(sbom, contract) != nil {
		return Projection{}, errors.New("SBOM mismatch")
	}
	_, manifestDigest, _ := strings.Cut(contract.Reference, "@")
	return Projection{
		Schema: workerEvidenceSchema, Contract: workerContract,
		Reference: contract.Reference, ImageID: identity.ID, ManifestDigest: manifestDigest,
		ConfigDigest: identity.ID, SBOMSHA256: provenance.SBOMSHA256,
		ProvenanceSHA256: digestData(values["provenance.json"]), OS: identity.OS, Architecture: identity.Architecture,
		ContextSHA256: provenance.Context.ContextSHA256, ModuleLockSHA256: provenance.Context.ModuleLockSHA256,
		ToolchainLockSHA256: provenance.Context.ToolchainSHA256,
		GoVersion:           workerGoVersion, TinyGoVersion: workerTinyGoVersion,
	}, nil
}

func strictLaunchArtifactJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > maxLaunchArtifactBytes || launchcontract.RejectDuplicateJSONKeys(data) != nil {
		return errors.New("artifact JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("artifact JSON is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("artifact JSON has trailing data")
	}
	return nil
}

func validateArtifactModules(value artifactModuleLock) error {
	if value.Schema != launchModuleSchema || len(value.Repositories) != 3 {
		return errors.New("module lock shape")
	}
	wanted := []struct {
		name      string
		manifests []string
	}{
		{name: "gsxmail", manifests: []string{"go.mod", "go.sum", "LICENSE"}},
		{name: "gosx", manifests: []string{"go.mod", "go.sum", "LICENSE", "editor/go.mod", "editor/go.sum", "cmd/buildbootstrap/go.mod", "cmd/buildbootstrap/go.sum"}},
		{name: "tqwebp", manifests: []string{"go.mod", "go.sum", "LICENSE", "bench/deepteams/go.mod", "bench/deepteams/go.sum"}},
	}
	for index, repository := range value.Repositories {
		if repository.Name != wanted[index].name || len(repository.Commit) != 40 && len(repository.Commit) != 64 || !isLowerHex(repository.Commit) || len(repository.Manifests) != len(wanted[index].manifests) {
			return errors.New("module repository shape")
		}
		for manifestIndex, manifest := range repository.Manifests {
			if manifest.Path != wanted[index].manifests[manifestIndex] || filepath.IsAbs(manifest.Path) || filepath.Clean(manifest.Path) != manifest.Path || strings.HasPrefix(manifest.Path, "../") || !isDigest(manifest.SHA256) || manifest.Bytes <= 0 || manifest.Bytes > 8<<20 {
				return errors.New("module manifest shape")
			}
		}
	}
	return nil
}

func validateArtifactContext(value artifactContext, contract config.LaunchWorkerImageConfig, moduleBytes []byte) error {
	if value.Schema != launchContextSchema || !isDigest(value.ContextSHA256) || value.ModuleLockSHA256 != contract.ModuleLockSHA256 || value.ModuleLockSHA256 != digestData(moduleBytes) || value.ToolchainSHA256 != contract.ToolchainLockSHA256 || !isDigest(value.ToolchainSHA256) || value.Entries <= 0 || value.Entries > 64 || value.Bytes <= 0 || value.Bytes > 32<<20 {
		return errors.New("context shape")
	}
	return nil
}

func validateArtifactToolchain(value artifactToolchain) error {
	if value.Schema != launchToolchainSchema || value.Platform != "linux/amd64" || value.BaseRef != launchBaseReference || value.BaseImageID != launchBaseImageID || value.GoVersion != launchGoVersion || value.TinyGoVersion != launchTinyGoVersion || value.TinyGoLLVMVersion != "20.1.1" || value.TinyGoURL != launchTinyGoURL || value.TinyGoSHA256 != launchTinyGoSHA256 || value.TinyGoLicenseURL != launchTinyGoLicenseURL || value.TinyGoLicenseSHA256 != launchTinyGoLicenseSHA256 || value.DockerfileFrontend != launchDockerfileFrontend || value.BuildxVersion != "v0.28.0" || value.BuildxCommit != "b1281b81bba797b21d9eaf256e6a13eb14419836" || value.BuildKitVersion != "v0.24.0" || value.BuildKitDriver != "docker" || value.SourceDateEpoch != 946684800 || value.RegistryRef != launchRegistryReference || value.RegistryVersion != "3.0.0" || value.RegistryLicense != "Apache-2.0" {
		return errors.New("toolchain identity")
	}
	return nil
}

func validateArtifactSBOM(value artifactSPDX, contract config.LaunchWorkerImageConfig) error {
	if value.SPDXVersion != "SPDX-2.3" || value.DataLicense != "CC0-1.0" || value.SPDXID != "SPDXRef-DOCUMENT" || value.Name != "buckley-oss-worker" || len(value.Packages) == 0 || len(value.Packages) > maxLaunchArtifactEntries || len(value.Relationships) == 0 || len(value.Relationships) > maxLaunchArtifactEntries+1 {
		return errors.New("SBOM shape")
	}
	if _, err := time.Parse(time.RFC3339, value.CreationInfo.Created); err != nil || !reflect.DeepEqual(value.CreationInfo.Creators, []string{"Tool: buckley-launch-provision-v1"}) {
		return errors.New("SBOM creation")
	}
	manifest := strings.TrimPrefix(contract.Reference[strings.LastIndex(contract.Reference, "@")+1:], "sha256:")
	if value.DocumentNamespace != "https://m31labs.dev/buckley/launch/sbom/"+manifest {
		return errors.New("SBOM namespace")
	}
	found := 0
	for _, pkg := range value.Packages {
		if pkg.Name == "buckley-oss-worker" {
			found++
			if pkg.VersionInfo != manifest || pkg.DownloadLocation != contract.Reference || pkg.PrimaryPackageType != "CONTAINER" {
				return errors.New("SBOM worker package")
			}
		}
	}
	if found != 1 {
		return errors.New("SBOM worker package")
	}
	return nil
}

func digestData(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isDigest(value string) bool { return len(value) == 64 && isLowerHex(value) }

func isLowerHex(value string) bool {
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ValidateContract verifies the immutable operator image coordinates without
// inspecting Docker. It does not mint a Proof.
func ValidateContract(contract config.LaunchWorkerImageConfig) error {
	return validateLaunchImageContract(contract)
}

func validateLaunchImageContract(contract config.LaunchWorkerImageConfig) error {
	ref := contract.Reference
	if ref != strings.TrimSpace(ref) || !launchImagePattern.MatchString(ref) || strings.Count(ref, "@") != 1 {
		return errors.New("launch worker image reference is invalid")
	}
	repository, digest, ok := strings.Cut(ref, "@sha256:")
	if !ok || repository == "" || !launchDigestPattern.MatchString(digest) || strings.Contains(repository, "//") || strings.Contains(repository, "..") {
		return errors.New("launch worker image reference is not canonical")
	}
	last := repository
	if slash := strings.LastIndexByte(repository, '/'); slash >= 0 {
		last = repository[slash+1:]
	}
	if last == "" || strings.Contains(last, ":") || strings.HasPrefix(last, ".") || strings.HasPrefix(last, "-") || strings.HasSuffix(last, ".") || strings.HasSuffix(last, "-") {
		return errors.New("launch worker image repository is invalid")
	}
	if !strings.HasPrefix(contract.ImageID, "sha256:") || !launchDigestPattern.MatchString(strings.TrimPrefix(contract.ImageID, "sha256:")) {
		return errors.New("launch worker image ID is invalid")
	}
	if contract.OS != workerOS || runtime.GOARCH != workerArchitecture || contract.Architecture != workerArchitecture {
		return errors.New("launch worker image platform is invalid")
	}
	if !launchDigestPattern.MatchString(contract.ModuleLockSHA256) || !launchDigestPattern.MatchString(contract.ToolchainLockSHA256) {
		return errors.New("launch worker lock digest is invalid")
	}
	return nil
}

// ValidateIdentity checks a read-only Docker projection but does not mint a
// Proof; complete artifact verification remains mandatory.
func ValidateIdentity(contract config.LaunchWorkerImageConfig, identity dockersandbox.ImageIdentity) error {
	return validateLaunchImageIdentity(contract, identity)
}

func validateLaunchImageIdentity(contract config.LaunchWorkerImageConfig, identity dockersandbox.ImageIdentity) error {
	if err := validateLaunchImageContract(contract); err != nil {
		return err
	}
	if identity.ID != contract.ImageID || identity.OS != contract.OS || identity.Architecture != contract.Architecture {
		return errors.New("launch worker image identity mismatch")
	}
	matched := false
	for _, digest := range identity.RepoDigests {
		if digest == contract.Reference {
			matched = true
			break
		}
	}
	if !matched || identity.Labels[launchContractLabelKey] != workerContract || identity.Labels[launchProbeLabelKey] != launchProbePath || identity.Labels[launchSupervisorLabelKey] != launchSupervisorPath ||
		identity.Labels[launchGoVersionLabelKey] != workerGoVersion || identity.Labels[launchTinyGoLabelKey] != workerTinyGoVersion || identity.Labels[launchBaseLabelKey] != launchBaseImageID || identity.Labels[launchModuleLockLabelKey] != "sha256:"+contract.ModuleLockSHA256 || identity.Labels[launchToolchainLockLabelKey] != "sha256:"+contract.ToolchainLockSHA256 {
		return errors.New("launch worker image contract mismatch")
	}
	if len(identity.Entrypoint) != 1 || identity.Entrypoint[0] != "/bin/sleep" || len(identity.Cmd) != 1 || identity.Cmd[0] != "infinity" {
		return errors.New("launch worker image process contract mismatch")
	}
	for key, expected := range map[string]string{
		"GOTOOLCHAIN": "local", "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off", "GOMODCACHE": "/opt/buckley/modcache",
	} {
		if !launchEnvironmentHasExact(identity.Env, key, expected) {
			return errors.New("launch worker image offline environment mismatch")
		}
	}
	return nil
}

func launchEnvironmentHasExact(environment []string, key, expected string) bool {
	if len(environment) > 256 {
		return false
	}
	prefix := key + "="
	matches := 0
	for _, entry := range environment {
		if len(entry) > 4096 || !utf8.ValidString(entry) || strings.IndexFunc(entry, unicode.IsControl) >= 0 {
			return false
		}
		if strings.HasPrefix(entry, prefix) {
			matches++
			if entry != prefix+expected {
				return false
			}
		}
	}
	return matches == 1
}

func validateLaunchUser(current *user.User) error {
	if current == nil || current.Uid != strings.TrimSpace(current.Uid) || current.Gid != strings.TrimSpace(current.Gid) {
		return errors.New("launch user is invalid")
	}
	uid, uidErr := strconv.ParseUint(current.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(current.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
		return errors.New("launch user must be nonroot")
	}
	return nil
}

func resolveTrustedDockerBinary() (string, error) {
	info, err := os.Lstat(trustedDockerBinary)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("trusted docker client is unavailable")
	}
	return trustedDockerBinary, nil
}
