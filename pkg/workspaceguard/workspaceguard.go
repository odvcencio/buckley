package workspaceguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/launchcontract"
	"m31labs.dev/buckley/pkg/secretsafety"
	"m31labs.dev/buckley/pkg/workspaceevidence"
)

const (
	EvidenceSchema        = "buckley.workspace-preflight.v1"
	MaxFindings           = 32
	MaxSafeLabelBytes     = 160
	MaxWorkspaceEntries   = 100_000
	MaxSnapshotFileBytes  = 4 << 20
	MaxSnapshotTotalBytes = 64 << 20
	MaxTrackedTextBytes   = 256 << 20
	MaxTrackedTotalBytes  = 1 << 30
	maxGitOutputBytes     = 16 << 20
	gitCommandTimeout     = 10 * time.Second
	trustedGitBinary      = "/usr/bin/git"
	trackedPrefixBytes    = 64 << 10
)

type ReasonCode string

const (
	ReasonNonWorktree        ReasonCode = "workspace_not_git_worktree"
	ReasonRootNotCanonical   ReasonCode = "workspace_root_not_canonical"
	ReasonRootMismatch       ReasonCode = "git_root_mismatch"
	ReasonInvalidHEAD        ReasonCode = "git_head_invalid"
	ReasonTrackedDirty       ReasonCode = "tracked_baseline_dirty"
	ReasonSecretPath         ReasonCode = "secret_risk_path"
	ReasonSecretContent      ReasonCode = "secret_risk_content"
	ReasonUnreadablePath     ReasonCode = "workspace_path_unreadable"
	ReasonUnsafePath         ReasonCode = "workspace_path_unsafe"
	ReasonSymlink            ReasonCode = "workspace_symlink"
	ReasonNonRegular         ReasonCode = "workspace_nonregular"
	ReasonNestedGit          ReasonCode = "nested_git_boundary"
	ReasonGitmodules         ReasonCode = "gitmodules_present"
	ReasonSubmodule          ReasonCode = "git_submodule_present"
	ReasonCapacity           ReasonCode = "workspace_capacity_exceeded"
	ReasonWorkspaceChanged   ReasonCode = "workspace_changed_during_scan"
	ReasonLicense            ReasonCode = "workspace_license_unrecognized"
	ReasonNetworkLogging     ReasonCode = "network_body_logging_enabled"
	ReasonTelemetryPayloads  ReasonCode = "telemetry_payload_network_enabled"
	ReasonConfigUnavailable  ReasonCode = "preflight_configuration_unavailable"
	ReasonSandboxUnavailable ReasonCode = "launch_sandbox_unavailable"
	ReasonSandboxPolicy      ReasonCode = "launch_sandbox_policy_invalid"
	ReasonCleanupRequired    ReasonCode = "launch_cleanup_required"
)

// Finding is a bounded, body-free explanation of a failed admission check.
type Finding struct {
	Code  ReasonCode `json:"code"`
	Label string     `json:"label,omitempty"`
}

// Evidence is a compact binding to the inspected workspace state.
type Evidence struct {
	Schema                string `json:"schema"`
	RootSHA256            string `json:"rootSha256,omitempty"`
	HEAD                  string `json:"head,omitempty"`
	ManifestSHA256        string `json:"manifestSha256,omitempty"`
	LicenseID             string `json:"licenseId,omitempty"`
	LicensePath           string `json:"licensePath,omitempty"`
	LicenseSHA256         string `json:"licenseSha256,omitempty"`
	LicenseManifestSHA256 string `json:"licenseManifestSha256,omitempty"`
	TrackedFiles          int    `json:"trackedFiles"`
	UntrackedFiles        int    `json:"untrackedFiles"`
	IgnoredFiles          int    `json:"ignoredFiles"`
}

// LaunchProjection is a bounded public view. It is not accepted as admission
// authority; only the package-opaque LaunchProof is.
type LaunchProjection struct {
	Schema                string
	CanonicalRoot         string
	RootSHA256            string
	HEAD                  string
	ManifestSHA256        string
	PreflightSHA256       string
	LicenseID             string
	LicensePath           string
	LicenseSHA256         string
	LicenseManifestSHA256 string
}

type LaunchProof struct{ projection LaunchProjection }

func (p LaunchProof) Snapshot() LaunchProjection { return p.projection }

// Report is safe for CLI and durable audit projections.
type Report struct {
	Allowed      bool      `json:"allowed"`
	Evidence     Evidence  `json:"evidence"`
	Findings     []Finding `json:"findings,omitempty"`
	rootIdentity string
}

func (r Report) MatchesRoot(binding *RootBinding) bool {
	return r.rootIdentity != "" && binding != nil && binding.file != nil && binding.identity == r.rootIdentity
}

func (r Report) Validate() error {
	if r.Evidence.Schema != EvidenceSchema || len(r.Findings) > MaxFindings {
		return errors.New("workspaceguard: report shape is invalid")
	}
	for _, digest := range []string{r.Evidence.RootSHA256, r.Evidence.ManifestSHA256, r.Evidence.LicenseSHA256, r.Evidence.LicenseManifestSHA256} {
		if digest != "" && !validDigest(digest) {
			return errors.New("workspaceguard: evidence digest is invalid")
		}
	}
	if r.Evidence.HEAD != "" && !validObjectID(r.Evidence.HEAD) {
		return errors.New("workspaceguard: HEAD evidence is invalid")
	}
	if r.Evidence.LicenseID != "" && r.Evidence.LicenseID != workspaceevidence.LicenseIDMIT && r.Evidence.LicenseID != workspaceevidence.LicenseIDApache20 {
		return errors.New("workspaceguard: license evidence is invalid")
	}
	for _, count := range []int{r.Evidence.TrackedFiles, r.Evidence.UntrackedFiles, r.Evidence.IgnoredFiles} {
		if count < 0 || count > MaxWorkspaceEntries {
			return errors.New("workspaceguard: evidence count is invalid")
		}
	}
	entryTotal := int64(r.Evidence.TrackedFiles) + int64(r.Evidence.UntrackedFiles) + int64(r.Evidence.IgnoredFiles)
	if entryTotal > MaxWorkspaceEntries {
		return errors.New("workspaceguard: evidence count is invalid")
	}
	licenseEmpty := r.Evidence.LicenseID == "" && r.Evidence.LicensePath == "" && r.Evidence.LicenseSHA256 == "" && r.Evidence.LicenseManifestSHA256 == ""
	licenseComplete := r.Evidence.LicenseID != "" && r.Evidence.LicensePath != "" && r.Evidence.LicenseSHA256 != "" && r.Evidence.LicenseManifestSHA256 != ""
	if !licenseEmpty && !licenseComplete {
		return errors.New("workspaceguard: license evidence is incomplete")
	}
	if licenseComplete {
		if evidence := (workspaceevidence.LicenseEvidence{File: r.Evidence.LicensePath, ID: r.Evidence.LicenseID, SHA256: r.Evidence.LicenseSHA256, ManifestSHA256: r.Evidence.LicenseManifestSHA256}); evidence.Validate() != nil {
			return errors.New("workspaceguard: license evidence is invalid")
		}
	}
	if r.Allowed {
		if len(r.Findings) != 0 || r.Evidence.RootSHA256 == "" || r.Evidence.HEAD == "" || r.Evidence.ManifestSHA256 == "" || !licenseComplete {
			return errors.New("workspaceguard: allowed report is incomplete")
		}
	} else if len(r.Findings) == 0 {
		return errors.New("workspaceguard: blocked report has no reason")
	}
	seen := make(map[string]struct{}, len(r.Findings))
	var prior string
	for _, finding := range r.Findings {
		if !knownReasonCode(finding.Code) || finding.Label != safeLabel(finding.Label) || len(finding.Label) > MaxSafeLabelBytes {
			return errors.New("workspaceguard: finding is invalid")
		}
		key := string(finding.Code) + "\x00" + finding.Label
		if prior != "" && key < prior {
			return errors.New("workspaceguard: findings are not canonical")
		}
		if _, ok := seen[key]; ok {
			return errors.New("workspaceguard: duplicate finding")
		}
		seen[key] = struct{}{}
		prior = key
	}
	return nil
}

type Request struct {
	Root string
}

// Inspector is the launch-admission workspace observation port.
type Inspector interface {
	Inspect(context.Context, Request) (Report, error)
}

// GitRunner is the read-only Git observation port.
type GitRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type Options struct {
	Git GitRunner
	// AfterSnapshot is a deterministic test seam. Production leaves it nil.
	AfterSnapshot func()
}

type GitInspector struct {
	git           GitRunner
	afterSnapshot func()
}

func NewGitInspector(opts Options) *GitInspector {
	runner := opts.Git
	if runner == nil {
		binary, err := resolveTrustedGitBinary()
		runner = execGitRunner{binary: binary, unavailable: err}
	}
	return &GitInspector{git: runner, afterSnapshot: opts.AfterSnapshot}
}

// DiagnosticsPolicy is the only configuration projection needed by launch
// admission. It intentionally cannot carry credentials or request bodies.
type DiagnosticsPolicy struct {
	NetworkLogsEnabled           bool
	TelemetryPayloadsOverNetwork bool
}

func CheckDiagnostics(policy DiagnosticsPolicy) []Finding {
	var findings []Finding
	if policy.NetworkLogsEnabled {
		findings = append(findings, Finding{Code: ReasonNetworkLogging})
	}
	if policy.TelemetryPayloadsOverNetwork {
		findings = append(findings, Finding{Code: ReasonTelemetryPayloads})
	}
	return findings
}

func AddFindings(report Report, findings ...Finding) Report {
	collector := findingCollector{report: &report}
	for _, finding := range findings {
		collector.add(finding.Code, finding.Label)
	}
	return collector.finish()
}

func (i *GitInspector) Inspect(ctx context.Context, req Request) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	report := Report{Evidence: Evidence{Schema: EvidenceSchema}}
	collector := findingCollector{report: &report}

	raw := strings.TrimSpace(req.Root)
	canonical, err := workspaceevidence.NormalizeWorkspaceRoot(raw)
	if err != nil {
		collector.add(ReasonNonWorktree, "workspace")
		return collector.finish(), nil
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		collector.add(ReasonRootNotCanonical, "workspace")
		return collector.finish(), nil
	}
	if filepath.Clean(abs) != canonical {
		collector.add(ReasonRootNotCanonical, "workspace")
	}
	report.Evidence.RootSHA256 = digestString(canonical)

	binding, err := OpenRootBinding(canonical)
	if err != nil {
		collector.add(ReasonUnreadablePath, "workspace")
		return collector.finish(), nil
	}
	defer binding.Close()
	report.rootIdentity = binding.identity
	rootBefore := binding.info
	boundRoot := binding.Source()

	top, err := i.git.Run(ctx, boundRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		collector.add(ReasonNonWorktree, "workspace")
		return collector.finish(), nil
	}
	gitRoot, err := workspaceevidence.NormalizeWorkspaceRoot(strings.TrimSpace(string(top)))
	if err != nil || gitRoot != canonical {
		collector.add(ReasonRootMismatch, "workspace")
		return collector.finish(), nil
	}

	head, err := i.git.Run(ctx, boundRoot, "rev-parse", "--verify", "HEAD^{commit}")
	headText := strings.TrimSpace(string(head))
	if err != nil || !validObjectID(headText) {
		collector.add(ReasonInvalidHEAD, "HEAD")
		return collector.finish(), nil
	}
	report.Evidence.HEAD = headText

	trackedStatus, err := i.git.Run(ctx, boundRoot, "status", "--porcelain=v2", "--untracked-files=no", "--ignore-submodules=none", "-z")
	if err != nil {
		return Report{}, fmt.Errorf("workspaceguard: inspect tracked status: %w", err)
	}
	if len(trackedStatus) != 0 {
		collector.add(ReasonTrackedDirty, "tracked")
	}

	trackedRaw, err := i.git.Run(ctx, boundRoot, "ls-files", "--stage", "-z", "--")
	if err != nil {
		return Report{}, fmt.Errorf("workspaceguard: enumerate tracked files: %w", err)
	}
	trackedFlagsRaw, err := i.git.Run(ctx, boundRoot, "ls-files", "-v", "-z", "--")
	if err != nil {
		return Report{}, fmt.Errorf("workspaceguard: inspect tracked flags: %w", err)
	}
	untrackedRaw, err := i.git.Run(ctx, boundRoot, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return Report{}, fmt.Errorf("workspaceguard: enumerate untracked files: %w", err)
	}
	ignoredRaw, err := i.git.Run(ctx, boundRoot, "ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--")
	if err != nil {
		return Report{}, fmt.Errorf("workspaceguard: enumerate ignored files: %w", err)
	}

	tracked, trackedRecords := parseTracked(trackedRaw, &collector)
	if !canonicalTrackedFlags(trackedFlagsRaw, tracked) {
		collector.add(ReasonTrackedDirty, "index-flags")
	}
	untracked := parsePathList(untrackedRaw, &collector)
	ignored := parsePathList(ignoredRaw, &collector)
	report.Evidence.TrackedFiles = len(tracked)
	report.Evidence.UntrackedFiles = len(untracked)
	report.Evidence.IgnoredFiles = len(ignored)
	if int64(len(tracked))+int64(len(untracked))+int64(len(ignored)) > MaxWorkspaceEntries {
		collector.add(ReasonCapacity, "entries")
		return collector.finish(), nil
	}

	root, err := os.OpenRoot(boundRoot)
	if err != nil {
		collector.add(ReasonUnreadablePath, "workspace")
		return collector.finish(), nil
	}
	defer root.Close()

	manifest := append([]string(nil), trackedRecords...)
	manifest = append(manifest, EvidenceSchema, "root\x00"+report.Evidence.RootSHA256, "head\x00"+headText)
	if err := scanFilesystem(ctx, root, &collector); err != nil {
		return Report{}, err
	}

	trackedBytes := int64(0)
	seen := make(map[string]string, len(tracked)+len(untracked)+len(ignored))
	snapshots := make(map[string]fileSnapshot, len(tracked)+len(untracked)+len(ignored))
	for _, path := range tracked {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		seen[path] = "tracked"
		if secretsafety.CredentialPath(path) {
			collector.add(ReasonSecretPath, safeLabel(path))
		}
		record, size, scanErr := inspectTrackedFile(root, path)
		if scanErr != nil {
			code := classifyPathError(scanErr)
			collector.add(code, safeLabel(path))
			snapshots[path] = fileSnapshot{kind: "tracked", errorCode: code}
			continue
		}
		snapshots[path] = fileSnapshot{kind: "tracked", record: record, size: size}
		if !record.binary {
			trackedBytes += size
			if size > MaxTrackedTextBytes || trackedBytes > MaxTrackedTotalBytes {
				collector.add(ReasonCapacity, "tracked-text-bytes")
				break
			}
			if record.secret {
				collector.add(ReasonSecretContent, safeLabel(path))
			}
		}
	}

	totalBytes := int64(0)
	for _, item := range []struct {
		kind  string
		paths []string
	}{{"untracked", untracked}, {"ignored", ignored}} {
		for _, path := range item.paths {
			if err := ctx.Err(); err != nil {
				return Report{}, err
			}
			if prior, ok := seen[path]; ok {
				collector.add(ReasonUnsafePath, safeLabel(path))
				manifest = append(manifest, "duplicate\x00"+prior+"\x00"+item.kind+"\x00"+path)
				continue
			}
			seen[path] = item.kind
			if secretsafety.SensitivePath(path) {
				collector.add(ReasonSecretPath, safeLabel(path))
			}
			record, size, scanErr := inspectSnapshotFile(root, path)
			if scanErr != nil {
				code := classifyPathError(scanErr)
				collector.add(code, safeLabel(path))
				snapshots[path] = fileSnapshot{kind: item.kind, errorCode: code}
				continue
			}
			snapshots[path] = fileSnapshot{kind: item.kind, record: record, size: size}
			totalBytes += size
			if totalBytes > MaxSnapshotTotalBytes {
				collector.add(ReasonCapacity, "snapshot-bytes")
				break
			}
			if record.secret {
				collector.add(ReasonSecretContent, safeLabel(path))
			}
			manifest = append(manifest, item.kind+"\x00"+path+"\x00"+record.digest+"\x00"+strconv.FormatInt(size, 10))
		}
	}

	license, licenseErr := workspaceevidence.InspectWorkspaceLicense(boundRoot)
	if licenseErr != nil || license.Status != workspaceevidence.LicenseStatusRecognizedOSS {
		collector.add(ReasonLicense, safeLabel(license.Status))
	} else {
		report.Evidence.LicenseID = license.Evidence.ID
		report.Evidence.LicensePath = license.Evidence.File
		report.Evidence.LicenseSHA256 = license.Evidence.SHA256
		report.Evidence.LicenseManifestSHA256 = license.Evidence.ManifestSHA256
		manifest = append(manifest, "license\x00"+license.Evidence.ManifestSHA256)
	}

	if i.afterSnapshot != nil {
		i.afterSnapshot()
	}
	if !i.snapshotStable(ctx, canonical, boundRoot, rootBefore, root, snapshots, headText, trackedStatus, trackedRaw, trackedFlagsRaw, untrackedRaw, ignoredRaw) {
		collector.add(ReasonWorkspaceChanged, "workspace")
	}
	sort.Strings(manifest)
	report.Evidence.ManifestSHA256 = digestString(strings.Join(manifest, "\n"))
	return collector.finish(), nil
}

// VerifyLaunch accepts only an
// allowed report still bound to the exact no-follow root identity inspected by
// GitInspector and emits no source bodies.
func (i *GitInspector) VerifyLaunch(ctx context.Context, root string, profile launchcontract.ProfileDescriptor) (LaunchProof, error) {
	if i == nil || profile.Validate() != nil || profile.License.EvidenceSchema != EvidenceSchema {
		return LaunchProof{}, errors.New("workspaceguard: launch workspace verifier is unavailable")
	}
	canonical, err := workspaceevidence.NormalizeWorkspaceRoot(root)
	if err != nil || canonical != root {
		return LaunchProof{}, errors.New("workspaceguard: launch workspace root is not canonical")
	}
	report, err := i.Inspect(ctx, Request{Root: canonical})
	if err != nil {
		return LaunchProof{}, err
	}
	if !report.Allowed || report.Validate() != nil || !profileAllowsLicense(profile.License.AllowedIDs, report.Evidence.LicenseID) {
		return LaunchProof{}, errors.New("workspaceguard: launch workspace is not admitted")
	}
	binding, err := OpenRootBinding(canonical)
	if err != nil {
		return LaunchProof{}, errors.New("workspaceguard: launch workspace binding is unavailable")
	}
	defer binding.Close()
	if !report.MatchesRoot(binding) {
		return LaunchProof{}, errors.New("workspaceguard: launch workspace changed after inspection")
	}
	evidenceProjection := struct {
		Allowed  bool     `json:"allowed"`
		Evidence Evidence `json:"evidence"`
	}{Allowed: report.Allowed, Evidence: report.Evidence}
	encoded, err := json.Marshal(evidenceProjection)
	if err != nil || len(encoded) == 0 || len(encoded) > 16<<10 {
		return LaunchProof{}, errors.New("workspaceguard: launch workspace evidence exceeds its bound")
	}
	projection := LaunchProjection{
		Schema: EvidenceSchema, CanonicalRoot: canonical,
		RootSHA256: report.Evidence.RootSHA256, HEAD: report.Evidence.HEAD,
		ManifestSHA256: report.Evidence.ManifestSHA256, PreflightSHA256: digestBytes(encoded),
		LicenseID: report.Evidence.LicenseID, LicensePath: report.Evidence.LicensePath,
		LicenseSHA256:         report.Evidence.LicenseSHA256,
		LicenseManifestSHA256: report.Evidence.LicenseManifestSHA256,
	}
	return LaunchProof{projection: projection}, nil
}

func profileAllowsLicense(allowed []string, actual string) bool {
	for _, id := range allowed {
		if id == actual {
			return true
		}
	}
	return false
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

type snapshotRecord struct {
	digest string
	secret bool
	binary bool
}

type fileSnapshot struct {
	kind      string
	record    snapshotRecord
	size      int64
	errorCode ReasonCode
}

func inspectTrackedFile(root *os.Root, path string) (snapshotRecord, int64, error) {
	return inspectTrackedFileWithHook(root, path, nil)
}

func inspectTrackedFileWithHook(root *os.Root, path string, afterContentRead func()) (snapshotRecord, int64, error) {
	if !validRelativePath(path) {
		return snapshotRecord{}, 0, errUnsafePath
	}
	info, err := root.Lstat(filepath.FromSlash(path))
	if err != nil {
		return snapshotRecord{}, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return snapshotRecord{}, 0, errSymlink
	}
	if !info.Mode().IsRegular() {
		return snapshotRecord{}, 0, errNonRegular
	}
	if hasMultipleLinks(info) {
		return snapshotRecord{}, 0, errUnsafePath
	}
	if info.Size() < 0 {
		return snapshotRecord{}, 0, errCapacity
	}
	file, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return snapshotRecord{}, 0, err
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return snapshotRecord{}, 0, errUnreadable
	}

	prefixLimit := int64(trackedPrefixBytes)
	if info.Size() < prefixLimit {
		prefixLimit = info.Size()
	}
	prefix := make([]byte, prefixLimit)
	read, readErr := io.ReadFull(file, prefix)
	if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
		readErr = nil
	}
	prefix = prefix[:read]
	// An extension is only a disclosure hint. Admission classifies the bytes
	// themselves so plain-text credentials cannot hide behind a binary suffix.
	binary := secretsafety.BinaryContent(prefix)
	hasher := sha256.New()
	secret := false
	if !binary {
		if info.Size() > MaxTrackedTextBytes {
			_ = file.Close()
			return snapshotRecord{}, 0, errCapacity
		}
		if _, err := hasher.Write(prefix); err != nil {
			_ = file.Close()
			return snapshotRecord{}, 0, errUnreadable
		}
		scanner := newSecretStreamScanner()
		scanner.Write(prefix)
		buffer := make([]byte, 64<<10)
		for {
			n, streamErr := file.Read(buffer)
			if n > 0 {
				if _, err := hasher.Write(buffer[:n]); err != nil {
					_ = file.Close()
					return snapshotRecord{}, 0, errUnreadable
				}
				scanner.Write(buffer[:n])
			}
			if errors.Is(streamErr, io.EOF) {
				break
			}
			if streamErr != nil {
				_ = file.Close()
				return snapshotRecord{}, 0, errUnreadable
			}
		}
		secret = scanner.Secret()
	}
	if afterContentRead != nil {
		afterContentRead()
	}
	afterRead, afterReadErr := file.Stat()
	closeErr := file.Close()
	after, lstatErr := root.Lstat(filepath.FromSlash(path))
	if readErr != nil || afterReadErr != nil || closeErr != nil || lstatErr != nil {
		return snapshotRecord{}, 0, errUnreadable
	}
	for _, current := range []os.FileInfo{opened, afterRead, after} {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || hasMultipleLinks(current) || !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return snapshotRecord{}, 0, errChanged
		}
	}
	digest := "binary"
	if !binary {
		digest = hex.EncodeToString(hasher.Sum(nil))
	}
	return snapshotRecord{digest: digest, secret: secret, binary: binary}, info.Size(), nil
}

type secretStreamScanner struct {
	tail   []byte
	secret bool
}

func newSecretStreamScanner() *secretStreamScanner { return &secretStreamScanner{} }

func (s *secretStreamScanner) Write(chunk []byte) {
	if s == nil || s.secret || len(chunk) == 0 {
		return
	}
	combined := make([]byte, 0, len(s.tail)+len(chunk))
	combined = append(combined, s.tail...)
	combined = append(combined, chunk...)
	if secretsafety.SecretContent(combined) {
		s.secret = true
		return
	}
	const overlap = 2048
	if len(combined) > overlap {
		combined = combined[len(combined)-overlap:]
	}
	s.tail = append(s.tail[:0], combined...)
}

func (s *secretStreamScanner) Secret() bool { return s != nil && s.secret }

func inspectSnapshotFile(root *os.Root, path string) (snapshotRecord, int64, error) {
	return inspectSnapshotFileWithHook(root, path, nil)
}

func inspectSnapshotFileWithHook(root *os.Root, path string, afterContentRead func()) (snapshotRecord, int64, error) {
	if !validRelativePath(path) {
		return snapshotRecord{}, 0, errUnsafePath
	}
	info, err := root.Lstat(filepath.FromSlash(path))
	if err != nil {
		return snapshotRecord{}, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return snapshotRecord{}, 0, errSymlink
	}
	if !info.Mode().IsRegular() {
		return snapshotRecord{}, 0, errNonRegular
	}
	if hasMultipleLinks(info) {
		return snapshotRecord{}, 0, errUnsafePath
	}
	if info.Size() < 0 || info.Size() > MaxSnapshotFileBytes {
		return snapshotRecord{}, 0, errCapacity
	}
	file, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return snapshotRecord{}, 0, err
	}
	opened, statErr := file.Stat()
	content, readErr := io.ReadAll(io.LimitReader(file, MaxSnapshotFileBytes+1))
	if afterContentRead != nil {
		afterContentRead()
	}
	afterRead, afterReadErr := file.Stat()
	closeErr := file.Close()
	after, lstatErr := root.Lstat(filepath.FromSlash(path))
	if statErr != nil || readErr != nil || afterReadErr != nil || closeErr != nil || lstatErr != nil {
		return snapshotRecord{}, 0, errUnreadable
	}
	if len(content) > MaxSnapshotFileBytes {
		return snapshotRecord{}, 0, errCapacity
	}
	for _, current := range []os.FileInfo{opened, afterRead, after} {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || hasMultipleLinks(current) || !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return snapshotRecord{}, 0, errChanged
		}
	}
	sum := sha256.Sum256(content)
	return snapshotRecord{digest: hex.EncodeToString(sum[:]), secret: secretsafety.SecretContent(content)}, info.Size(), nil
}

func scanFilesystem(ctx context.Context, root *os.Root, collector *findingCollector) error {
	count := 0
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			collector.add(ReasonUnreadablePath, safeLabel(filepath.ToSlash(path)))
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == "." {
			return nil
		}
		rel := filepath.ToSlash(path)
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(rel) == ".git" {
			collector.add(ReasonNestedGit, safeLabel(rel))
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !validRelativePath(rel) {
			collector.add(ReasonUnsafePath, safeLabel(rel))
			return nil
		}
		count++
		if count > MaxWorkspaceEntries {
			collector.add(ReasonCapacity, "entries")
			return filepath.SkipAll
		}
		if filepath.Base(rel) == ".gitmodules" {
			collector.add(ReasonGitmodules, safeLabel(rel))
		}
		info, err := entry.Info()
		if err != nil {
			collector.add(ReasonUnreadablePath, safeLabel(rel))
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			collector.add(ReasonSymlink, safeLabel(rel))
			return nil
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			collector.add(ReasonNonRegular, safeLabel(rel))
		} else if info.Mode().IsRegular() && hasMultipleLinks(info) {
			collector.add(ReasonUnsafePath, safeLabel(rel))
		}
		return nil
	})
}

func (i *GitInspector) snapshotStable(ctx context.Context, canonicalRoot, boundRoot string, before os.FileInfo, root *os.Root, snapshots map[string]fileSnapshot, head string, values ...[]byte) bool {
	after, err := os.Lstat(canonicalRoot)
	if err != nil || !os.SameFile(before, after) || !after.IsDir() {
		return false
	}
	commands := [][]string{
		{"rev-parse", "--verify", "HEAD^{commit}"},
		{"status", "--porcelain=v2", "--untracked-files=no", "--ignore-submodules=none", "-z"},
		{"ls-files", "--stage", "-z", "--"},
		{"ls-files", "-v", "-z", "--"},
		{"ls-files", "--others", "--exclude-standard", "-z", "--"},
		{"ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--"},
	}
	for idx, command := range commands {
		got, err := i.git.Run(ctx, boundRoot, command...)
		if err != nil {
			return false
		}
		if idx == 0 {
			if strings.TrimSpace(string(got)) != head {
				return false
			}
			continue
		}
		if !stringEqualBytes(got, values[idx-1]) {
			return false
		}
	}
	paths := make([]string, 0, len(snapshots))
	for path := range snapshots {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if ctx.Err() != nil {
			return false
		}
		expected := snapshots[path]
		var record snapshotRecord
		var size int64
		var readErr error
		if expected.kind == "tracked" {
			record, size, readErr = inspectTrackedFile(root, path)
		} else {
			record, size, readErr = inspectSnapshotFile(root, path)
		}
		if readErr != nil {
			if expected.errorCode == "" || classifyPathError(readErr) != expected.errorCode {
				return false
			}
			continue
		}
		if expected.errorCode != "" || size != expected.size || record != expected.record {
			return false
		}
	}
	return true
}

func canonicalTrackedFlags(raw []byte, tracked []string) bool {
	paths := make([]string, 0, len(tracked))
	for _, item := range splitNUL(raw) {
		if len(item) < 3 || item[0] != 'H' || item[1] != ' ' {
			return false
		}
		path := item[2:]
		if !validRelativePath(path) {
			return false
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) != len(tracked) {
		return false
	}
	for idx := range paths {
		if paths[idx] != tracked[idx] {
			return false
		}
	}
	return true
}

func parseTracked(raw []byte, collector *findingCollector) ([]string, []string) {
	paths := make([]string, 0)
	records := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range splitNUL(raw) {
		meta, path, ok := strings.Cut(item, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !validRelativePath(path) || !validObjectID(fields[1]) || fields[2] != "0" {
			collector.add(ReasonUnsafePath, safeLabel(path))
			continue
		}
		mode := fields[0]
		if mode == "160000" {
			collector.add(ReasonSubmodule, safeLabel(path))
		}
		if mode == "120000" {
			collector.add(ReasonSymlink, safeLabel(path))
		}
		if filepath.Base(filepath.ToSlash(path)) == ".gitmodules" {
			collector.add(ReasonGitmodules, safeLabel(path))
		}
		if _, exists := seen[path]; exists {
			collector.add(ReasonUnsafePath, safeLabel(path))
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		records = append(records, "tracked\x00"+mode+"\x00"+fields[1]+"\x00"+path)
	}
	sort.Strings(paths)
	return paths, records
}

func parsePathList(raw []byte, collector *findingCollector) []string {
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	for _, path := range splitNUL(raw) {
		path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if !validRelativePath(path) {
			collector.add(ReasonUnsafePath, safeLabel(path))
			continue
		}
		if _, ok := seen[path]; ok {
			collector.add(ReasonUnsafePath, safeLabel(path))
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func splitNUL(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

type findingCollector struct {
	report    *Report
	truncated bool
}

func (c *findingCollector) add(code ReasonCode, label string) {
	if c == nil || c.report == nil || code == "" {
		return
	}
	label = safeLabel(label)
	for _, finding := range c.report.Findings {
		if finding.Code == code && finding.Label == label {
			return
		}
	}
	if len(c.report.Findings) >= MaxFindings {
		if !c.truncated {
			c.truncated = true
			c.report.Findings[MaxFindings-1] = Finding{Code: ReasonCapacity, Label: "findings"}
		}
		return
	}
	c.report.Findings = append(c.report.Findings, Finding{Code: code, Label: label})
}

func (c *findingCollector) finish() Report {
	sort.Slice(c.report.Findings, func(a, b int) bool {
		if c.report.Findings[a].Code != c.report.Findings[b].Code {
			return c.report.Findings[a].Code < c.report.Findings[b].Code
		}
		return c.report.Findings[a].Label < c.report.Findings[b].Label
	})
	c.report.Allowed = len(c.report.Findings) == 0
	return *c.report
}

type execGitRunner struct {
	binary      string
	unavailable error
}

func (r execGitRunner) Run(ctx context.Context, root string, args ...string) ([]byte, error) {
	if r.unavailable != nil || r.binary != trustedGitBinary {
		return nil, errors.New("trusted git client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	base := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "diff.external=",
		"--no-pager", "-C", root,
	}
	cmd := exec.CommandContext(commandCtx, r.binary, append(base, args...)...)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=/nonexistent",
		"XDG_CONFIG_HOME=/nonexistent",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	}
	var stdout boundedBuffer
	stdout.max = maxGitOutputBytes
	var stderr boundedBuffer
	stderr.max = 4096
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git observation failed: %w", err)
	}
	return stdout.Bytes(), nil
}

func resolveTrustedGitBinary() (string, error) {
	info, err := os.Lstat(trustedGitBinary)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("trusted git client is unavailable")
	}
	return trustedGitBinary, nil
}

type boundedBuffer struct {
	data []byte
	max  int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > b.max {
		remaining := b.max - len(b.data)
		if remaining > 0 {
			b.data = append(b.data, p[:remaining]...)
		}
		return len(p), errCapacity
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return append([]byte(nil), b.data...) }

var (
	errUnsafePath = errors.New("unsafe path")
	errSymlink    = errors.New("symlink")
	errNonRegular = errors.New("nonregular")
	errCapacity   = errors.New("capacity")
	errChanged    = errors.New("changed")
	errUnreadable = errors.New("unreadable")
)

func classifyPathError(err error) ReasonCode {
	switch {
	case errors.Is(err, errUnsafePath):
		return ReasonUnsafePath
	case errors.Is(err, errSymlink):
		return ReasonSymlink
	case errors.Is(err, errNonRegular):
		return ReasonNonRegular
	case errors.Is(err, errCapacity):
		return ReasonCapacity
	case errors.Is(err, errChanged):
		return ReasonWorkspaceChanged
	default:
		return ReasonUnreadablePath
	}
}

func validRelativePath(path string) bool {
	if path == "" || path == "." || filepath.IsAbs(path) || secretsafety.UnsafePath(path) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return clean == path && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.Contains("/"+clean+"/", "/.git/")
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func knownReasonCode(code ReasonCode) bool {
	switch code {
	case ReasonNonWorktree, ReasonRootNotCanonical, ReasonRootMismatch, ReasonInvalidHEAD,
		ReasonTrackedDirty, ReasonSecretPath, ReasonSecretContent, ReasonUnreadablePath,
		ReasonUnsafePath, ReasonSymlink, ReasonNonRegular, ReasonNestedGit, ReasonGitmodules,
		ReasonSubmodule, ReasonCapacity, ReasonWorkspaceChanged, ReasonLicense,
		ReasonNetworkLogging, ReasonTelemetryPayloads, ReasonConfigUnavailable,
		ReasonSandboxUnavailable, ReasonSandboxPolicy, ReasonCleanupRequired:
		return true
	default:
		return false
	}
}

func safeLabel(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if !utf8.ValidString(value) {
		return "path-" + digestString(value)[:12]
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "path-" + digestString(value)[:12]
		}
	}
	if len(value) > MaxSafeLabelBytes {
		end := MaxSafeLabelBytes - 13
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		return value[:end] + "-" + digestString(value)[:12]
	}
	return value
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func stringEqualBytes(left, right []byte) bool {
	return string(left) == string(right)
}
