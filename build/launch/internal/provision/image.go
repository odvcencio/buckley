package provision

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ContractLabelKey      = "dev.m31labs.buckley.launch.contract"
	ContractLabelValue    = "worker-v1"
	ProbeLabelKey         = "dev.m31labs.buckley.launch.probe"
	ProbePath             = "/usr/local/bin/buckley-launch-probe-v1"
	SupervisorLabelKey    = "dev.m31labs.buckley.launch.supervisor"
	SupervisorPath        = "/usr/local/bin/buckley-launch-supervisor-v1"
	GoVersionLabelKey     = "dev.m31labs.buckley.launch.go-version"
	TinyGoVersionLabelKey = "dev.m31labs.buckley.launch.tinygo-version"
	BaseLabelKey          = "dev.m31labs.buckley.launch.base"
	ModuleLockLabelKey    = "dev.m31labs.buckley.launch.module-lock"
	ToolchainLockLabelKey = "dev.m31labs.buckley.launch.toolchain-lock"
)

var imageIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type ImageIdentity struct {
	ID           string   `json:"Id"`
	RepoDigests  []string `json:"RepoDigests"`
	OS           string   `json:"Os"`
	Architecture string   `json:"Architecture"`
	Config       struct {
		Labels     map[string]string `json:"Labels"`
		Env        []string          `json:"Env"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
	} `json:"Config"`
}

type WorkerContract struct {
	Schema              string `json:"schema"`
	Reference           string `json:"reference"`
	ImageID             string `json:"image_id"`
	OS                  string `json:"os"`
	Architecture        string `json:"architecture"`
	ModuleLockSHA256    string `json:"module_lock_sha256"`
	ToolchainLockSHA256 string `json:"toolchain_lock_sha256"`
}

func parseImageInspect(raw string) (ImageIdentity, error) {
	var identities []ImageIdentity
	decoder := json.NewDecoder(strings.NewReader(raw))
	// Docker adds many fields. Decode through a permissive alias while keeping
	// the bounded projection above; unknown top-level fields are intentional.
	if err := decoder.Decode(&identities); err != nil || rejectTrailingJSON(decoder) != nil || len(identities) != 1 {
		return ImageIdentity{}, errors.New("image identity is invalid")
	}
	identity := identities[0]
	if !imageIDPattern.MatchString(identity.ID) || identity.OS != "linux" || identity.Architecture != "amd64" {
		return ImageIdentity{}, errors.New("image platform identity is invalid")
	}
	if len(identity.RepoDigests) > 128 || len(identity.Config.Labels) > 128 || len(identity.Config.Env) > 256 {
		return ImageIdentity{}, errors.New("image metadata exceeds bounds")
	}
	return identity, nil
}

func validateWorkerIdentity(identity ImageIdentity, moduleLockDigest string, requiredReference string) error {
	if !imageIDPattern.MatchString(identity.ID) || identity.OS != "linux" || identity.Architecture != "amd64" || !sha256Pattern.MatchString(moduleLockDigest) {
		return errors.New("worker image identity is invalid")
	}
	requiredLabels := map[string]string{
		ContractLabelKey:      ContractLabelValue,
		ProbeLabelKey:         ProbePath,
		SupervisorLabelKey:    SupervisorPath,
		GoVersionLabelKey:     GoVersion,
		TinyGoVersionLabelKey: TinyGoVersion,
		BaseLabelKey:          BaseImageID,
		ModuleLockLabelKey:    "sha256:" + moduleLockDigest,
		ToolchainLockLabelKey: "sha256:" + expectedToolchainDigest(),
	}
	for key, expected := range requiredLabels {
		if identity.Config.Labels[key] != expected {
			return errors.New("worker image label contract mismatch")
		}
	}
	if requiredReference != "" {
		matched := false
		for _, reference := range identity.RepoDigests {
			if reference == requiredReference {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("worker image digest reference is missing")
		}
	}
	if len(identity.Config.Entrypoint) != 1 || identity.Config.Entrypoint[0] != "/bin/sleep" || len(identity.Config.Cmd) != 1 || identity.Config.Cmd[0] != "infinity" {
		return errors.New("worker image process contract mismatch")
	}
	for _, expected := range []string{
		"GOMODCACHE=/opt/buckley/modcache",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
	} {
		key, value, _ := strings.Cut(expected, "=")
		if !containsEnvironmentExact(identity.Config.Env, key, value) {
			return errors.New("worker image offline environment mismatch")
		}
	}
	return nil
}

func expectedToolchainDigest() string {
	data, err := canonicalJSON(ExpectedToolchain())
	if err != nil {
		return ""
	}
	return digestBytes(data)
}

func containsEnvironmentExact(values []string, key, expected string) bool {
	if len(values) > 256 {
		return false
	}
	prefix := key + "="
	matches := 0
	for _, value := range values {
		if len(value) > 4096 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return false
		}
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		matches++
		if value != prefix+expected {
			return false
		}
	}
	return matches == 1
}
