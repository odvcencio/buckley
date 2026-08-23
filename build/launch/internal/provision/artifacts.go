package provision

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxPackageStatusBytes   = 16 << 20
	maxModuleInventoryBytes = 4 << 20
	maxSBOMPackages         = 16384
)

type spdxDocument struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	Name              string             `json:"name"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo   `json:"creationInfo"`
	Packages          []spdxPackage      `json:"packages"`
	Relationships     []spdxRelationship `json:"relationships"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	SPDXID             string `json:"SPDXID"`
	Name               string `json:"name"`
	VersionInfo        string `json:"versionInfo,omitempty"`
	DownloadLocation   string `json:"downloadLocation"`
	FilesAnalyzed      bool   `json:"filesAnalyzed"`
	LicenseConcluded   string `json:"licenseConcluded"`
	LicenseDeclared    string `json:"licenseDeclared"`
	PrimaryPackageType string `json:"primaryPackagePurpose,omitempty"`
}

type spdxRelationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

type provenanceArtifact struct {
	Schema              string          `json:"schema"`
	Worker              WorkerContract  `json:"worker"`
	Toolchain           ToolchainLock   `json:"toolchain"`
	Context             ContextManifest `json:"context"`
	Modules             ModuleLock      `json:"modules"`
	BuildkitMetadata    string          `json:"buildkit_metadata"`
	BuildkitMetadataSHA string          `json:"buildkit_metadata_sha256"`
	BuildResultDigest   string          `json:"build_result_digest"`
	BuildConfigDigest   string          `json:"build_config_digest"`
	SBOM                string          `json:"sbom"`
	SBOMSHA256          string          `json:"sbom_sha256"`
}

func writeProvisionArtifacts(ctx context.Context, root string, opts SealOptions, result SealResult, identity ImageIdentity) error {
	metadataPath := filepath.Join(root, "buildkit-metadata.json")
	metadata, err := readStableRegular(metadataPath, maxCommandOutput)
	metadataIdentity, err := validateBuildMetadata(metadata, identity, result.Contract)
	if err != nil {
		return errors.New("buildkit metadata artifact is unavailable")
	}
	metadata, err = canonicalJSON(map[string]string{
		"containerimage.config.digest": metadataIdentity.ConfigDigest,
		"containerimage.digest":        metadataIdentity.ResultDigest,
	})
	if err != nil || writeArtifactBytes(root, "buildkit-metadata.json", metadata) != nil {
		return errors.New("buildkit metadata artifact is unavailable")
	}
	packages, err := collectImagePackages(ctx, opts.Runner, result.Contract.Reference, opts.Token)
	if err != nil {
		return err
	}
	packages = append(packages,
		spdxPackage{Name: "Go", VersionInfo: GoVersion, DownloadLocation: BaseReference, LicenseConcluded: "BSD-3-Clause", LicenseDeclared: "BSD-3-Clause", PrimaryPackageType: "APPLICATION"},
		spdxPackage{Name: "TinyGo", VersionInfo: TinyGoVersion, DownloadLocation: TinyGoURL, LicenseConcluded: "BSD-3-Clause", LicenseDeclared: "BSD-3-Clause", PrimaryPackageType: "APPLICATION"},
		spdxPackage{Name: "LLVM", VersionInfo: TinyGoLLVMVersion, DownloadLocation: TinyGoURL, LicenseConcluded: "Apache-2.0 WITH LLVM-exception", LicenseDeclared: "Apache-2.0 WITH LLVM-exception", PrimaryPackageType: "LIBRARY"},
	)
	packages = canonicalizePackages(packages)
	workerPackage := spdxPackage{Name: "buckley-oss-worker", VersionInfo: strings.TrimPrefix(metadataIdentity.ResultDigest, "sha256:"), DownloadLocation: result.Contract.Reference, LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION", PrimaryPackageType: "CONTAINER"}
	workerPackage.SPDXID = packageSPDXID(workerPackage)
	document := spdxDocument{
		SPDXVersion:       "SPDX-2.3",
		DataLicense:       "CC0-1.0",
		SPDXID:            "SPDXRef-DOCUMENT",
		Name:              "buckley-oss-worker",
		DocumentNamespace: "https://m31labs.dev/buckley/launch/sbom/" + strings.TrimPrefix(result.Contract.Reference[strings.LastIndex(result.Contract.Reference, "@")+1:], "sha256:"),
		CreationInfo:      spdxCreationInfo{Created: time.Now().UTC().Format(time.RFC3339), Creators: []string{"Tool: buckley-launch-provision-v1"}},
		Packages:          append([]spdxPackage{workerPackage}, packages...),
	}
	document.Relationships = append(document.Relationships, spdxRelationship{SPDXElementID: "SPDXRef-DOCUMENT", RelationshipType: "DESCRIBES", RelatedSPDXElement: workerPackage.SPDXID})
	for index := 1; index < len(document.Packages); index++ {
		document.Packages[index].SPDXID = packageSPDXID(document.Packages[index])
		document.Relationships = append(document.Relationships, spdxRelationship{SPDXElementID: workerPackage.SPDXID, RelationshipType: "CONTAINS", RelatedSPDXElement: document.Packages[index].SPDXID})
	}
	sbomBytes, err := canonicalJSON(document)
	if err != nil || len(sbomBytes) > maxCommandOutput {
		return errors.New("SBOM artifact exceeds bounds")
	}
	if err := writeArtifactBytes(root, "sbom.spdx.json", sbomBytes); err != nil {
		return err
	}
	if err := writeJSONArtifact(root, "module-lock.json", opts.ModuleLock); err != nil {
		return err
	}
	if err := writeJSONArtifact(root, "toolchain-lock.json", opts.Toolchain); err != nil {
		return err
	}
	if err := writeJSONArtifact(root, "operator-contract.json", result.Contract); err != nil {
		return err
	}
	metadataDigest := sha256.Sum256(metadata)
	sbomDigest := sha256.Sum256(sbomBytes)
	provenance := provenanceArtifact{
		Schema:              "buckley.launch.provenance.v1",
		Worker:              result.Contract,
		Toolchain:           opts.Toolchain,
		Context:             opts.Manifest,
		Modules:             opts.ModuleLock,
		BuildkitMetadata:    "buildkit-metadata.json",
		BuildkitMetadataSHA: hex.EncodeToString(metadataDigest[:]),
		BuildResultDigest:   metadataIdentity.ResultDigest,
		BuildConfigDigest:   metadataIdentity.ConfigDigest,
		SBOM:                "sbom.spdx.json",
		SBOMSHA256:          hex.EncodeToString(sbomDigest[:]),
	}
	if err := writeJSONArtifact(root, "provenance.json", provenance); err != nil {
		return err
	}
	artifactDir, err := filepath.Abs(root)
	if err != nil || filepath.Clean(artifactDir) != artifactDir {
		return errors.New("operator artifact directory is unavailable")
	}
	config := fmt.Sprintf("launch:\n  worker_image:\n    reference: %q\n    image_id: %q\n    os: linux\n    architecture: amd64\n    module_lock_sha256: %q\n    toolchain_lock_sha256: %q\n    artifact_dir: %q\n", result.Contract.Reference, identity.ID, result.Contract.ModuleLockSHA256, result.Contract.ToolchainLockSHA256, artifactDir)
	if len(config) > 2048 {
		return errors.New("operator configuration artifact exceeds bounds")
	}
	if err := writeOperatorConfig(root, []byte(config)); err != nil {
		return errors.New("operator configuration artifact write failed")
	}
	return nil
}

func writeOperatorConfig(root string, data []byte) error {
	temporary := filepath.Join(root, ".operator-config.yaml.tmp")
	final := filepath.Join(root, "operator-config.yaml")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, final); err != nil {
		return err
	}
	keep = true
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func writeJSONArtifact(root, name string, value any) error {
	data, err := canonicalJSON(value)
	if err != nil || len(data) > maxCommandOutput {
		return errors.New("provisioning artifact exceeds bounds")
	}
	return writeArtifactBytes(root, name, data)
}

func writeArtifactBytes(root, name string, data []byte) error {
	if !validRelativePath(name) || len(data) == 0 || len(data) > maxCommandOutput {
		return errors.New("provisioning artifact exceeds bounds")
	}
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		return errors.New("provisioning artifact write failed")
	}
	return nil
}

type buildMetadataIdentity struct {
	ResultDigest string
	ConfigDigest string
}

func validateBuildMetadata(data []byte, identity ImageIdentity, contract WorkerContract) (buildMetadataIdentity, error) {
	if len(data) == 0 || len(data) > maxCommandOutput || !imageIDPattern.MatchString(identity.ID) {
		return buildMetadataIdentity{}, errors.New("build metadata is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var values map[string]json.RawMessage
	if err := decoder.Decode(&values); err != nil || rejectTrailingJSON(decoder) != nil || len(values) > 128 {
		return buildMetadataIdentity{}, errors.New("build metadata is invalid")
	}
	readString := func(key string) (string, bool) {
		value, ok := values[key]
		if !ok || len(value) > 256 {
			return "", false
		}
		var decoded string
		if json.Unmarshal(value, &decoded) != nil || !validDigest(decoded) {
			return "", false
		}
		return decoded, true
	}
	resultDigest, resultOK := readString("containerimage.digest")
	configDigest, configOK := readString("containerimage.config.digest")
	contractDigest := ""
	if at := strings.LastIndex(contract.Reference, "@"); at >= 0 {
		contractDigest = contract.Reference[at+1:]
	}
	if !resultOK || !configOK || resultDigest != contractDigest || configDigest != identity.ID || identity.ID != contract.ImageID {
		return buildMetadataIdentity{}, errors.New("build metadata does not match sealed image")
	}
	return buildMetadataIdentity{ResultDigest: resultDigest, ConfigDigest: configDigest}, nil
}

func collectImagePackages(ctx context.Context, runner Runner, reference, owner string) (packages []spdxPackage, returnErr error) {
	if runner == nil || safeReference(reference) == "" {
		return nil, errors.New("SBOM image reference is invalid")
	}
	if !ownerPattern.MatchString(owner) {
		var err error
		owner, err = randomToken()
		if err != nil {
			return nil, errors.New("SBOM ownership is unavailable")
		}
	}
	name := "buckley-launch-sbom-" + owner
	operationCtx, cancel := boundedContext(ctx, dockerTimeout)
	defer cancel()
	output, err := runner.Run(operationCtx,
		"create", "--name", name,
		"--label", registryOwnerLabel+"="+owner,
		"--pull", "never", "--network", "none", "--read-only",
		"--security-opt", "no-new-privileges", "--cap-drop", "ALL",
		"--pids-limit", "32", "--memory", "64m", "--cpus", "0.25",
		"--entrypoint", "/bin/false", reference,
	)
	containerID := strings.TrimSpace(output)
	if err != nil || !containerIDPattern.MatchString(containerID) {
		identity, inspectErr := inspectImage(operationCtx, runner, reference)
		if inspectErr != nil {
			return nil, errors.New("SBOM container creation failed with cleanup pending")
		}
		ownedID, ownershipErr := resolveOwnedContainer(operationCtx, runner, name, owner, reference, identity.ID)
		if ownershipErr != nil {
			return nil, errors.New("SBOM container creation failed with cleanup pending")
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		cleanupErr := cleanupContainer(cleanupCtx, runner, ownedID)
		cleanupCancel()
		if cleanupErr != nil {
			return nil, joinCleanupRequired(errors.New("SBOM container creation failed"), "sbom-container", ownedID)
		}
		return nil, errors.New("SBOM container creation failed")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if cleanupErr := cleanupContainer(cleanupCtx, runner, containerID); cleanupErr != nil {
			packages = nil
			returnErr = joinCleanupRequired(returnErr, "sbom-container", containerID)
		}
	}()
	statusPath, err := reserveTemporaryPath("buckley-launch-dpkg-*.status")
	if err != nil {
		return nil, errors.New("SBOM temporary file creation failed")
	}
	defer os.Remove(statusPath)
	modulePath, err := reserveTemporaryPath("buckley-launch-modules-*.tsv")
	if err != nil {
		return nil, errors.New("SBOM temporary file creation failed")
	}
	defer os.Remove(modulePath)
	if _, err := runner.Run(operationCtx, "cp", containerID+":/var/lib/dpkg/status", statusPath); err != nil {
		return nil, errors.New("SBOM package inventory is unavailable")
	}
	if _, err := runner.Run(operationCtx, "cp", containerID+":/usr/share/buckley-launch/module-inventory.tsv", modulePath); err != nil {
		return nil, errors.New("SBOM module inventory is unavailable")
	}
	data, err := readStableRegular(statusPath, maxPackageStatusBytes)
	if err != nil {
		return nil, errors.New("SBOM package inventory is invalid")
	}
	packages, err = parseDpkgStatus(data)
	if err != nil {
		return nil, err
	}
	moduleData, err := readStableRegular(modulePath, maxModuleInventoryBytes)
	if err != nil {
		return nil, errors.New("SBOM module inventory is invalid")
	}
	modules, err := parseModuleInventory(moduleData)
	if err != nil {
		return nil, err
	}
	return append(packages, modules...), nil
}

func reserveTemporaryPath(pattern string) (string, error) {
	temp, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	path := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func parseDpkgStatus(data []byte) ([]spdxPackage, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	fields := map[string]string{}
	var packages []spdxPackage
	flush := func() error {
		if len(fields) == 0 {
			return nil
		}
		if fields["Status"] != "install ok installed" {
			fields = map[string]string{}
			return nil
		}
		name, version := fields["Package"], fields["Version"]
		if !safePackageField(name, 256) || !safePackageField(version, 256) {
			return errors.New("SBOM package metadata is invalid")
		}
		packages = append(packages, spdxPackage{Name: name, VersionInfo: version, DownloadLocation: "NOASSERTION", LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION", PrimaryPackageType: "OPERATING-SYSTEM"})
		if len(packages) > 8192 {
			return errors.New("SBOM package inventory exceeds bounds")
		}
		fields = map[string]string{}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, ok := strings.Cut(line, ": ")
		if ok && (key == "Package" || key == "Version" || key == "Status") {
			fields[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("SBOM package inventory scan failed")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return packages, nil
}

func parseModuleInventory(data []byte) ([]spdxPackage, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var packages []spdxPackage
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), "\t")
		if len(parts) != 2 || !safeModuleField(parts[0], 512) || !safeModuleField(parts[1], 256) {
			return nil, errors.New("SBOM module inventory is invalid")
		}
		packages = append(packages, spdxPackage{Name: parts[0], VersionInfo: parts[1], DownloadLocation: "NOASSERTION", LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION", PrimaryPackageType: "LIBRARY"})
		if len(packages) > maxSBOMPackages {
			return nil, errors.New("SBOM module inventory exceeds bounds")
		}
	}
	if err := scanner.Err(); err != nil || len(packages) == 0 {
		return nil, errors.New("SBOM module inventory scan failed")
	}
	return packages, nil
}

func safeModuleField(value string, max int) bool {
	if !safePackageField(value, max) || strings.ContainsAny(value, " \t") {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("._~+/-", character) {
			continue
		}
		return false
	}
	return true
}

func canonicalizePackages(packages []spdxPackage) []spdxPackage {
	byKey := make(map[string]spdxPackage, len(packages))
	for _, pkg := range packages {
		if safePackageField(pkg.Name, 512) && safePackageField(pkg.VersionInfo, 512) {
			byKey[pkg.Name+"\x00"+pkg.VersionInfo] = pkg
		}
	}
	result := make([]spdxPackage, 0, len(byKey))
	for _, pkg := range byKey {
		result = append(result, pkg)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].VersionInfo < result[j].VersionInfo
	})
	return result
}

func packageSPDXID(pkg spdxPackage) string {
	digest := sha256.Sum256([]byte(pkg.Name + "\x00" + pkg.VersionInfo))
	return "SPDXRef-Package-" + hex.EncodeToString(digest[:8])
}

func safePackageField(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}
