// Package workspaceevidence inspects immutable, content-addressed facts about
// the exact Git state a trusted Buckley host is considering for dispatch.
//
// RootLicenseBlobEvidence is dormant local evidence, not an OSS decision,
// approval, capability, or no-ZDR authority. Its detected SPDX value is only a
// heuristic hint. A trusted coordinator may mint one-use no-ZDR authority only
// through a separately formed, host-sealed OSSBlobRule that binds the exact
// content and blob identities, rule version, repository scope, and run scope.
package workspaceevidence

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git SHA-1 object identity, not a security primitive.
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// SPDXHintClassifierVersion is bound into every evidence record and changes
	// whenever the candidate-name or full-text hint corpus changes. It does not
	// identify an authorization policy.
	SPDXHintClassifierVersion = "buckley-root-license-spdx-hint-v2"

	// MaxLicenseBlobBytes bounds license material read from the Git object
	// database. The longest supported hint text, Apache-2.0, is well below it.
	MaxLicenseBlobBytes = 64 << 10

	maxCommitObjectBytes        = 1 << 20
	maxRootTreeObjectBytes      = 1 << 20
	maxGitMetadataBytes         = 8 << 10
	maxGitErrorBytes            = 8 << 10
	evidenceInspectionTimeout   = 15 * time.Second
	gitWaitDelay                = time.Second
	maxTrustedGitExecutableSize = 128 << 20
	evidenceBindingDomain       = "buckley.workspaceevidence.root-license-blob.v3"
	repositoryIDDomain          = "buckley.workspaceevidence.repository-id.v1"
)

var (
	ErrInvalidRepositoryRoot = errors.New("workspaceevidence: repository root must be the canonical Git top-level")
	ErrInvalidCommitOID      = errors.New("workspaceevidence: commit must be an exact full commit OID")
	ErrLicenseNotFound       = errors.New("workspaceevidence: no root license candidate exists in the commit")
	ErrLicenseAmbiguous      = errors.New("workspaceevidence: root license candidates are ambiguous")
	ErrInvalidLicenseEntry   = errors.New("workspaceevidence: root license candidate is not a regular blob")
	ErrLicenseTooLarge       = errors.New("workspaceevidence: root license blob exceeds the size limit")
	ErrGitOutputLimit        = errors.New("workspaceevidence: bounded Git output limit exceeded")
	ErrGitCommandFailed      = errors.New("workspaceevidence: Git command failed")
	ErrEvidenceInvalid       = errors.New("workspaceevidence: root license blob evidence is invalid")
	ErrEvidenceStale         = errors.New("workspaceevidence: root license blob evidence is stale")
	ErrEvidenceLocalOnly     = errors.New("workspaceevidence: root license blob evidence is local-only and cannot be serialized")
)

type gitExecutableIdentity struct {
	path          string
	info          os.FileInfo
	contentSHA256 [sha256.Size]byte
	err           error
}

var trustedGitExecutable = captureTrustedGitExecutable()

type repositoryIdentity struct {
	root          string
	gitDir        string
	commonGitDir  string
	rootInfo      os.FileInfo
	gitDirInfo    os.FileInfo
	commonDirInfo os.FileInfo
}

// RootLicenseBlobEvidence contains immutable, local-only facts about one root
// license blob at an exact commit. Host paths, filesystem identities, and
// path-derived bindings deliberately have no public accessors. This value has
// no authority and cannot approve OSS or no-ZDR use.
type RootLicenseBlobEvidence struct {
	repository       repositoryIdentity
	repositoryID     string
	commitOID        string
	rootTreeOID      string
	licensePath      string
	blobOID          string
	contentSHA256    string
	detectedSPDXHint string
	hintVersion      string
	localBinding     string
}

// CommitOID returns the exact verified commit object identity.
func (e RootLicenseBlobEvidence) CommitOID() string { return e.commitOID }

// DetectedSPDXHint returns a conservative, non-authoritative text-pattern hint.
// It is not an OSS determination and cannot confer approval or no-ZDR authority.
func (e RootLicenseBlobEvidence) DetectedSPDXHint() string { return e.detectedSPDXHint }

// String prevents generic formatting from exposing host paths.
func (RootLicenseBlobEvidence) String() string {
	return "workspaceevidence.RootLicenseBlobEvidence{redacted}"
}

// GoString prevents %#v formatting from exposing host paths.
func (RootLicenseBlobEvidence) GoString() string {
	return "workspaceevidence.RootLicenseBlobEvidence{redacted}"
}

// MarshalJSON fails closed because evidence contains run-local filesystem
// identities. Durable resume must perform a fresh inspection.
func (RootLicenseBlobEvidence) MarshalJSON() ([]byte, error) { return nil, ErrEvidenceLocalOnly }

// UnmarshalJSON is intentionally unsupported. Durable resume must reinspect
// the repository rather than restoring stale local evidence.
func (*RootLicenseBlobEvidence) UnmarshalJSON([]byte) error { return ErrEvidenceLocalOnly }

// InspectRootLicenseBlob gathers exact object facts and a non-authoritative
// SPDX text hint from a root blob in exactCommitOID. It does not decide OSS
// status or mint any authority. canonicalGitTopLevel must already be absolute,
// clean, symlink-resolved, and equal to Git's top-level. The worktree is never
// consulted for license bytes.
func InspectRootLicenseBlob(ctx context.Context, canonicalGitTopLevel, exactCommitOID string) (RootLicenseBlobEvidence, error) {
	boundedCtx, cancel, err := boundedEvidenceContext(ctx)
	if err != nil {
		return RootLicenseBlobEvidence{}, err
	}
	defer cancel()
	return inspectRootLicenseBlob(boundedCtx, canonicalGitTopLevel, exactCommitOID)
}

func inspectRootLicenseBlob(ctx context.Context, canonicalGitTopLevel, exactCommitOID string) (RootLicenseBlobEvidence, error) {
	repository, err := captureRepositoryIdentity(ctx, canonicalGitTopLevel)
	if err != nil {
		return RootLicenseBlobEvidence{}, err
	}
	commitOID, rootTreeOID, objectHashLength, err := requireExactCommitOID(ctx, repository.root, exactCommitOID)
	if err != nil {
		return RootLicenseBlobEvidence{}, err
	}
	entry, err := committedRootLicenseEntry(ctx, repository.root, rootTreeOID, objectHashLength)
	if err != nil {
		return RootLicenseBlobEvidence{}, err
	}

	content, err := readVerifiedGitObject(ctx, repository.root, "blob", entry.oid, MaxLicenseBlobBytes, ErrLicenseTooLarge)
	if err != nil {
		return RootLicenseBlobEvidence{}, fmt.Errorf("read root license blob: %w", err)
	}
	// Hint detection never controls whether exact blob evidence is formed.
	// Unknown, altered, or non-text license material produces an empty hint.
	spdxHint := detectSPDXHint(content)
	if err := revalidateRepositoryIdentity(ctx, repository); err != nil {
		return RootLicenseBlobEvidence{}, err
	}

	contentSum := sha256.Sum256(content)
	evidence := RootLicenseBlobEvidence{
		repository:       repository,
		repositoryID:     repositoryIdentifier(repository),
		commitOID:        commitOID,
		rootTreeOID:      rootTreeOID,
		licensePath:      entry.path,
		blobOID:          entry.oid,
		contentSHA256:    hex.EncodeToString(contentSum[:]),
		detectedSPDXHint: spdxHint,
		hintVersion:      SPDXHintClassifierVersion,
	}
	evidence.localBinding = rootLicenseEvidenceBinding(evidence)
	return evidence, nil
}

// Revalidate repeats the repository-identity and object-chain checks before a
// future trusted host rule evaluates this evidence. Revalidation establishes
// only local factual freshness; it does not authorize OSS or no-ZDR use.
func (e RootLicenseBlobEvidence) Revalidate(ctx context.Context) error {
	boundedCtx, cancel, err := boundedEvidenceContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if e.localBinding == "" || e.localBinding != rootLicenseEvidenceBinding(e) {
		return ErrEvidenceInvalid
	}
	if err := revalidateRepositoryIdentity(boundedCtx, e.repository); err != nil {
		return ErrEvidenceStale
	}
	fresh, err := inspectRootLicenseBlob(boundedCtx, e.repository.root, e.commitOID)
	if err != nil || fresh.localBinding != e.localBinding {
		return ErrEvidenceStale
	}
	return nil
}

func boundedEvidenceContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, ErrEvidenceInvalid
	}
	boundedCtx, cancel := context.WithTimeout(ctx, evidenceInspectionTimeout)
	return boundedCtx, cancel, nil
}

func captureRepositoryIdentity(ctx context.Context, root string) (repositoryIdentity, error) {
	if root == "" || strings.TrimSpace(root) != root || strings.ContainsAny(root, "\x00\r\n") || !filepath.IsAbs(root) {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	clean := filepath.Clean(root)
	if clean != root {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || filepath.Clean(resolved) != clean {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	topRaw, err := boundedGitOutput(ctx, clean, maxGitMetadataBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	top, topInfo, err := canonicalGitDirectory(topRaw, clean)
	if err != nil || top != clean || !os.SameFile(info, topInfo) {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	gitDirRaw, err := boundedGitOutput(ctx, clean, maxGitMetadataBytes, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	gitDir, gitDirInfo, err := canonicalGitDirectory(gitDirRaw, clean)
	if err != nil {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	commonRaw, err := boundedGitOutput(ctx, clean, maxGitMetadataBytes, "rev-parse", "--git-common-dir")
	if err != nil {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	commonDir, commonInfo, err := canonicalGitDirectory(commonRaw, clean)
	if err != nil {
		return repositoryIdentity{}, ErrInvalidRepositoryRoot
	}
	return repositoryIdentity{
		root:          clean,
		gitDir:        gitDir,
		commonGitDir:  commonDir,
		rootInfo:      info,
		gitDirInfo:    gitDirInfo,
		commonDirInfo: commonInfo,
	}, nil
}

func canonicalGitDirectory(raw []byte, relativeTo string) (string, os.FileInfo, error) {
	path, err := singleGitLine(raw)
	if err != nil || strings.ContainsRune(path, '\x00') {
		return "", nil, ErrInvalidRepositoryRoot
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(relativeTo, path)
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != path {
		return "", nil, ErrInvalidRepositoryRoot
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", nil, ErrInvalidRepositoryRoot
	}
	return path, info, nil
}

func revalidateRepositoryIdentity(ctx context.Context, expected repositoryIdentity) error {
	if expected.rootInfo == nil || expected.gitDirInfo == nil || expected.commonDirInfo == nil {
		return ErrEvidenceInvalid
	}
	current, err := captureRepositoryIdentity(ctx, expected.root)
	if err != nil || current.root != expected.root || current.gitDir != expected.gitDir || current.commonGitDir != expected.commonGitDir ||
		!os.SameFile(expected.rootInfo, current.rootInfo) ||
		!os.SameFile(expected.gitDirInfo, current.gitDirInfo) ||
		!os.SameFile(expected.commonDirInfo, current.commonDirInfo) {
		return ErrEvidenceStale
	}
	return nil
}

func requireExactCommitOID(ctx context.Context, root, oid string) (string, string, int, error) {
	if oid == "" || strings.TrimSpace(oid) != oid || oid != strings.ToLower(oid) {
		return "", "", 0, ErrInvalidCommitOID
	}
	formatRaw, err := boundedGitOutput(ctx, root, maxGitMetadataBytes, "rev-parse", "--show-object-format")
	if err != nil {
		return "", "", 0, ErrInvalidCommitOID
	}
	format, err := singleGitLine(formatRaw)
	if err != nil {
		return "", "", 0, ErrInvalidCommitOID
	}
	hashLength := 0
	switch format {
	case "sha1":
		hashLength = 40
	case "sha256":
		hashLength = 64
	default:
		return "", "", 0, ErrInvalidCommitOID
	}
	if len(oid) != hashLength || !isLowerHex(oid) {
		return "", "", 0, ErrInvalidCommitOID
	}
	commit, err := readVerifiedGitObject(ctx, root, "commit", oid, maxCommitObjectBytes, ErrInvalidCommitOID)
	if err != nil {
		return "", "", 0, ErrInvalidCommitOID
	}
	rootTreeOID, err := commitRootTreeOID(commit, hashLength)
	if err != nil {
		return "", "", 0, ErrInvalidCommitOID
	}
	return oid, rootTreeOID, hashLength, nil
}

type gitTreeEntry struct {
	mode string
	kind string
	oid  string
	path string
}

func commitRootTreeOID(commit []byte, objectHashLength int) (string, error) {
	headerEnd := bytes.Index(commit, []byte("\n\n"))
	if headerEnd <= 0 || bytes.IndexByte(commit[:headerEnd], 0) >= 0 {
		return "", ErrInvalidCommitOID
	}
	headers := bytes.Split(commit[:headerEnd], []byte{'\n'})
	if len(headers) == 0 || !bytes.HasPrefix(headers[0], []byte("tree ")) {
		return "", ErrInvalidCommitOID
	}
	treeOID := string(bytes.TrimPrefix(headers[0], []byte("tree ")))
	if len(treeOID) != objectHashLength || !isLowerHex(treeOID) {
		return "", ErrInvalidCommitOID
	}
	for _, line := range headers[1:] {
		if bytes.HasPrefix(line, []byte("tree ")) {
			return "", ErrInvalidCommitOID
		}
	}
	return treeOID, nil
}

func committedRootLicenseEntry(ctx context.Context, root, rootTreeOID string, objectHashLength int) (gitTreeEntry, error) {
	raw, err := readVerifiedGitObject(ctx, root, "tree", rootTreeOID, maxRootTreeObjectBytes, ErrInvalidLicenseEntry)
	if err != nil {
		return gitTreeEntry{}, ErrInvalidLicenseEntry
	}
	var candidates []gitTreeEntry
	hashBytes := objectHashLength / 2
	for offset := 0; offset < len(raw); {
		modeEnd := bytes.IndexByte(raw[offset:], ' ')
		if modeEnd <= 0 {
			return gitTreeEntry{}, ErrInvalidLicenseEntry
		}
		modeEnd += offset
		mode := string(raw[offset:modeEnd])
		nameStart := modeEnd + 1
		nameEndRelative := bytes.IndexByte(raw[nameStart:], 0)
		if nameEndRelative <= 0 {
			return gitTreeEntry{}, ErrInvalidLicenseEntry
		}
		nameEnd := nameStart + nameEndRelative
		oidStart := nameEnd + 1
		oidEnd := oidStart + hashBytes
		if oidEnd > len(raw) {
			return gitTreeEntry{}, ErrInvalidLicenseEntry
		}
		pathBytes := raw[nameStart:nameEnd]
		oid := hex.EncodeToString(raw[oidStart:oidEnd])
		offset = oidEnd
		if bytes.IndexByte(pathBytes, '/') >= 0 {
			return gitTreeEntry{}, ErrInvalidLicenseEntry
		}
		path := string(pathBytes)
		if !rootLicenseCandidate(path) {
			continue
		}
		kind := gitTreeEntryKind(mode)
		candidates = append(candidates, gitTreeEntry{mode: mode, kind: kind, oid: oid, path: path})
	}
	if len(candidates) == 0 {
		return gitTreeEntry{}, ErrLicenseNotFound
	}
	if len(candidates) > 1 {
		return gitTreeEntry{}, ErrLicenseAmbiguous
	}
	entry := candidates[0]
	if entry.kind != "blob" || (entry.mode != "100644" && entry.mode != "100755") {
		return gitTreeEntry{}, ErrInvalidLicenseEntry
	}
	if len(entry.oid) != objectHashLength || !isLowerHex(entry.oid) {
		return gitTreeEntry{}, ErrInvalidLicenseEntry
	}
	return entry, nil
}

func gitTreeEntryKind(mode string) string {
	switch mode {
	case "100644", "100755", "120000":
		return "blob"
	case "40000", "040000":
		return "tree"
	case "160000":
		return "commit"
	default:
		return ""
	}
}

func rootLicenseCandidate(path string) bool {
	switch path {
	case "LICENSE", "LICENSE.txt", "LICENSE.md",
		"LICENCE", "LICENCE.txt", "LICENCE.md",
		"COPYING", "COPYING.txt", "COPYING.md":
		return true
	default:
		return false
	}
}

func readVerifiedGitObject(ctx context.Context, root, kind, oid string, limit int, tooLarge error) ([]byte, error) {
	sizeRaw, err := boundedGitOutput(ctx, root, maxGitMetadataBytes, "cat-file", "-s", oid)
	if err != nil {
		return nil, err
	}
	sizeText, err := singleGitLine(sizeRaw)
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 0 {
		return nil, ErrGitOutputLimit
	}
	if size > int64(limit) {
		return nil, tooLarge
	}
	content, err := boundedGitOutput(ctx, root, limit, "cat-file", kind, oid)
	if err != nil {
		if errors.Is(err, ErrGitOutputLimit) {
			return nil, tooLarge
		}
		return nil, err
	}
	if int64(len(content)) != size {
		return nil, ErrGitOutputLimit
	}
	if err := verifyGitObjectIdentity(kind, content, oid); err != nil {
		return nil, err
	}
	return content, nil
}

func verifyGitObjectIdentity(kind string, content []byte, wantOID string) error {
	if kind != "blob" && kind != "commit" && kind != "tree" {
		return ErrInvalidLicenseEntry
	}
	header := []byte(kind + " " + strconv.Itoa(len(content)) + "\x00")
	var gotOID string
	switch len(wantOID) {
	case 40:
		hash := sha1.New() //nolint:gosec // Git SHA-1 object identity, not a security primitive.
		_, _ = hash.Write(header)
		_, _ = hash.Write(content)
		gotOID = hex.EncodeToString(hash.Sum(nil))
	case 64:
		hash := sha256.New()
		_, _ = hash.Write(header)
		_, _ = hash.Write(content)
		gotOID = hex.EncodeToString(hash.Sum(nil))
	default:
		return ErrInvalidLicenseEntry
	}
	if gotOID != wantOID {
		return ErrInvalidLicenseEntry
	}
	return nil
}

// detectSPDXHint is a deterministic text-pattern hint, not semantic analysis,
// legal proof, OSS admission, or authority. It deliberately trades false
// negatives for a narrow, reproducible observation.
func detectSPDXHint(content []byte) string {
	lines, normalized, err := normalizedLicenseText(content)
	if err != nil {
		return ""
	}
	if normalized == collapseLicenseWhitespace(apache20CanonicalText) {
		return "Apache-2.0"
	}
	if body, ok := bodyAfterCopyright(lines, []string{"MIT License", "The MIT License (MIT)"}, false); ok &&
		body == collapseLicenseWhitespace(mitCanonicalBody) {
		return "MIT"
	}
	if body, ok := bodyAfterCopyright(lines, []string{"BSD 2-Clause License"}, true); ok &&
		body == collapseLicenseWhitespace(bsd2CanonicalBody) {
		return "BSD-2-Clause"
	}
	if body, ok := bodyAfterCopyright(lines, []string{"BSD 3-Clause License"}, true); ok &&
		body == collapseLicenseWhitespace(bsd3CanonicalBody) {
		return "BSD-3-Clause"
	}
	return ""
}

func normalizedLicenseText(content []byte) ([]string, string, error) {
	if len(content) == 0 {
		return nil, "", fmt.Errorf("empty license")
	}
	if len(content) > MaxLicenseBlobBytes {
		return nil, "", ErrLicenseTooLarge
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, "", fmt.Errorf("license is not plain UTF-8 text")
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if strings.ContainsRune(text, '\r') {
		return nil, "", fmt.Errorf("license contains a bare carriage return")
	}
	rawLines := strings.Split(text, "\n")
	lines := make([]string, len(rawLines))
	for i, line := range rawLines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, collapseLicenseWhitespace(strings.Join(lines, "\n")), nil
}

func bodyAfterCopyright(lines, titles []string, allowAllRightsReserved bool) (string, bool) {
	i := skipBlankLicenseLines(lines, 0)
	titleMatched := false
	for _, title := range titles {
		if i < len(lines) && lines[i] == title {
			i = skipBlankLicenseLines(lines, i+1)
			titleMatched = true
			break
		}
	}
	if !titleMatched || i >= len(lines) || !validCopyrightNotice(lines[i]) {
		return "", false
	}
	i = skipBlankLicenseLines(lines, i+1)
	if allowAllRightsReserved && i < len(lines) && lines[i] == "All rights reserved." {
		i = skipBlankLicenseLines(lines, i+1)
	}
	if i >= len(lines) {
		return "", false
	}
	return collapseLicenseWhitespace(strings.Join(lines[i:], "\n")), true
}

func skipBlankLicenseLines(lines []string, i int) int {
	for i < len(lines) && lines[i] == "" {
		i++
	}
	return i
}

func validCopyrightNotice(line string) bool {
	if len(line) < len("Copyright 0000 X") || len(line) > 192 {
		return false
	}
	prefixes := []string{"Copyright (c) ", "Copyright © ", "Copyright "}
	remainder := ""
	for _, prefix := range prefixes {
		if strings.HasPrefix(line, prefix) {
			remainder = strings.TrimPrefix(line, prefix)
			break
		}
	}
	years, holder, ok := strings.Cut(remainder, " ")
	if !ok || !validCopyrightYears(years) || !validCopyrightHolder(holder) {
		return false
	}
	return true
}

func validCopyrightYears(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if len(part) != 4 {
			return false
		}
		year, err := strconv.Atoi(part)
		if err != nil || year < 1000 || year > 2999 {
			return false
		}
	}
	if len(parts) == 2 && parts[1] < parts[0] {
		return false
	}
	return true
}

func validCopyrightHolder(holder string) bool {
	if holder == "" || strings.TrimSpace(holder) != holder || len(holder) > 128 || containsCopyrightPolicyVocabulary(holder) {
		return false
	}
	identities := strings.Split(holder, " / ")
	if len(identities) == 0 || len(identities) > 3 {
		return false
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if identity == "" || strings.ContainsRune(identity, '/') || !validCopyrightIdentity(identity) {
			return false
		}
		if _, duplicate := seen[identity]; duplicate {
			return false
		}
		seen[identity] = struct{}{}
	}
	return true
}

func validCopyrightIdentity(identity string) bool {
	tokens := strings.Split(identity, " ")
	if len(tokens) < 2 || len(tokens) > 6 {
		return false
	}
	if recognizedOrganizationSuffix(tokens[len(tokens)-1]) {
		for i, token := range tokens[:len(tokens)-1] {
			if copyrightNameConnector(token) {
				if i == 0 || i == len(tokens)-2 {
					return false
				}
				continue
			}
			if !validCopyrightNameToken(token) {
				return false
			}
		}
		return true
	}
	// A bare identity is deliberately limited to a conventional two-token
	// person/name shape. Longer identities require a recognized organization
	// suffix; otherwise ordinary policy prose is too easy to disguise as a
	// holder.
	if len(tokens) != 2 {
		return false
	}
	for _, token := range tokens {
		if copyrightNameConnector(token) || !validCopyrightNameToken(token) {
			return false
		}
	}
	return true
}

func validCopyrightNameToken(token string) bool {
	if token == "" || len(token) > 48 {
		return false
	}
	for _, r := range token {
		if r > unicode.MaxASCII {
			return false
		}
	}
	runes := []rune(token)
	if len(runes) == 0 || (!(runes[0] >= 'A' && runes[0] <= 'Z') && !(runes[0] >= '0' && runes[0] <= '9')) {
		return false
	}
	for i, r := range runes {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || i == 0 && r >= 'A' && r <= 'Z' {
			continue
		}
		switch r {
		case '\'', '-':
			if i == 0 || i == len(runes)-1 ||
				!asciiNameLetterOrDigit(runes[i-1]) ||
				!(runes[i+1] >= 'A' && runes[i+1] <= 'Z') {
				return false
			}
		case 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M',
			'N', 'O', 'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z':
			if i == 0 || (runes[i-1] != '\'' && runes[i-1] != '-') {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func asciiNameLetterOrDigit(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

func copyrightNameConnector(token string) bool {
	switch token {
	case "&", "and", "of", "the":
		return true
	default:
		return false
	}
}

func recognizedOrganizationSuffix(token string) bool {
	switch token {
	case "Association", "Authors", "Co.", "Company", "Conservancy", "Contributors",
		"Corp.", "Corporation", "Foundation", "Group", "Inc.", "Incorporated",
		"Institute", "Laboratories", "Labs", "Limited", "LLC", "Ltd.", "Project", "Team":
		return true
	default:
		return false
	}
}

func containsCopyrightPolicyVocabulary(holder string) bool {
	words := strings.FieldsFunc(holder, func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	for _, word := range words {
		if copyrightPolicyWord(strings.ToLower(word)) {
			return true
		}
	}
	return false
}

func copyrightPolicyWord(word string) bool {
	switch word {
	case "academic", "all", "allowed", "approval", "authorized", "closed", "commercial",
		"condition", "conditions", "confidential", "consent", "copy", "copying", "denied",
		"deny", "derivative", "derivatives", "distribution", "educational", "evaluation",
		"except", "exploit", "exploitation", "fee", "field", "forbidden", "grant", "granted",
		"gratis", "internal", "legal", "licence", "license", "military", "modification",
		"must", "no", "noncommercial", "not", "nuclear", "only", "paid", "patent", "payment",
		"permission", "permitted", "personal", "private", "production", "profit", "prohibited",
		"proprietary", "public", "redistribution", "required", "requires", "research", "resale",
		"reserved", "restricted", "restriction", "rights", "royalty", "sale", "sell", "separate",
		"shall", "source", "subject", "sublicense", "trademark", "trial", "unauthorized",
		"unlicensed", "usage", "use", "warranty", "without", "written":
		return true
	default:
		return false
	}
}

func collapseLicenseWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func repositoryIdentifier(repository repositoryIdentity) string {
	return digestStrings(repositoryIDDomain, repository.root, repository.gitDir, repository.commonGitDir)
}

// rootLicenseEvidenceBinding is a private, path-derived local consistency
// check. It is neither portable provenance nor an authenticated capability.
func rootLicenseEvidenceBinding(e RootLicenseBlobEvidence) string {
	return digestStrings(
		evidenceBindingDomain,
		e.repository.root,
		e.repository.gitDir,
		e.repository.commonGitDir,
		e.repositoryID,
		e.commitOID,
		e.rootTreeOID,
		e.licensePath,
		e.blobOID,
		e.contentSHA256,
		e.detectedSPDXHint,
		e.hintVersion,
	)
}

func digestStrings(values ...string) string {
	h := sha256.New()
	for _, value := range values {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func singleGitLine(raw []byte) (string, error) {
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	raw = bytes.TrimSuffix(raw, []byte("\r"))
	if len(raw) == 0 || bytes.ContainsAny(raw, "\x00\r\n") {
		return "", fmt.Errorf("expected one non-empty Git output line")
	}
	return string(raw), nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, ErrGitOutputLimit
	}
	if len(p) <= remaining {
		return b.buffer.Write(p)
	}
	_, _ = b.buffer.Write(p[:remaining])
	b.exceeded = true
	return remaining, ErrGitOutputLimit
}

func captureTrustedGitExecutable() gitExecutableIdentity {
	path, err := exec.LookPath("git")
	if err != nil {
		return gitExecutableIdentity{err: ErrGitCommandFailed}
	}
	return captureGitExecutableAt(path)
}

func captureGitExecutableAt(path string) gitExecutableIdentity {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return gitExecutableIdentity{err: ErrGitCommandFailed}
	}
	path = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return gitExecutableIdentity{err: ErrGitCommandFailed}
	}
	path = filepath.Clean(resolved)
	info, err := os.Stat(path)
	if err != nil || !validGitExecutableInfo(info) {
		return gitExecutableIdentity{err: ErrGitCommandFailed}
	}
	digest, err := hashGitExecutable(path, info)
	if err != nil {
		return gitExecutableIdentity{err: ErrGitCommandFailed}
	}
	return gitExecutableIdentity{path: path, info: info, contentSHA256: digest}
}

func revalidateTrustedGitExecutable() (string, error) {
	return revalidateGitExecutable(trustedGitExecutable)
}

func revalidateGitExecutable(identity gitExecutableIdentity) (string, error) {
	if identity.err != nil || identity.path == "" || identity.info == nil {
		return "", ErrGitCommandFailed
	}
	current, err := os.Stat(identity.path)
	if err != nil || !validGitExecutableInfo(current) || !os.SameFile(identity.info, current) {
		return "", ErrGitCommandFailed
	}
	digest, err := hashGitExecutable(identity.path, current)
	if err != nil || digest != identity.contentSHA256 {
		return "", ErrGitCommandFailed
	}
	return identity.path, nil
}

func validGitExecutableInfo(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= maxTrustedGitExecutableSize
}

func hashGitExecutable(path string, expected os.FileInfo) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if !validGitExecutableInfo(expected) {
		return zero, ErrGitCommandFailed
	}
	file, err := os.Open(path)
	if err != nil {
		return zero, ErrGitCommandFailed
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !validGitExecutableInfo(opened) || !os.SameFile(expected, opened) ||
		opened.Size() != expected.Size() || opened.Mode() != expected.Mode() ||
		!opened.ModTime().Equal(expected.ModTime()) {
		return zero, ErrGitCommandFailed
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxTrustedGitExecutableSize+1))
	if err != nil || written != opened.Size() || written > maxTrustedGitExecutableSize {
		return zero, ErrGitCommandFailed
	}
	final, err := os.Stat(path)
	if err != nil || !os.SameFile(opened, final) || final.Size() != opened.Size() ||
		final.Mode() != opened.Mode() || !final.ModTime().Equal(opened.ModTime()) {
		return zero, ErrGitCommandFailed
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func boundedGitOutput(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
	if ctx == nil || len(args) == 0 || limit <= 0 {
		return nil, ErrEvidenceInvalid
	}
	gitPath, err := revalidateTrustedGitExecutable()
	if err != nil {
		return nil, err
	}
	cmdArgs := append([]string{"--no-pager"}, args...)
	// The executable is selected once from the trusted host environment, then
	// its canonical path, filesystem identity, and bounded content digest are
	// revalidated before each invocation. Later model-controlled environments
	// are not consulted; the trusted host must prevent concurrent mutation.
	cmd := exec.CommandContext(ctx, gitPath, cmdArgs...)
	cmd.Dir = root
	cmd.WaitDelay = gitWaitDelay
	cmd.Env = []string{
		"LC_ALL=C",
		"LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: maxGitErrorBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrGitOutputLimit
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("git %s: %w", args[0], ErrGitCommandFailed)
	}
	return append([]byte(nil), stdout.buffer.Bytes()...), nil
}

const mitCanonicalBody = `
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
`

const bsd2CanonicalBody = `
Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
this list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
this list of conditions and the following disclaimer in the documentation
and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.
`

const bsd3CanonicalBody = `
Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
this list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
this list of conditions and the following disclaimer in the documentation
and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.
`

const apache20CanonicalText = `
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "[]"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

   Copyright [yyyy] [name of copyright owner]

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.`
