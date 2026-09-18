package launchimage

import (
	"context"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/dockersandbox"
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

type rootBinding struct {
	file   *os.File
	source string
	info   os.FileInfo
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
