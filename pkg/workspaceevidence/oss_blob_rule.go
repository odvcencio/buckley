package workspaceevidence

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"path"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

const (
	MaxOSSPromptBlobBytes = 96 << 10

	maxOSSPromptPathBytes    = 4 << 10
	maxOSSPromptPathDepth    = 64
	ossBlobRuleVersion       = "buckley.workspaceevidence.oss-blob-rule.v2"
	ossBlobRuleBindingDomain = "buckley.workspaceevidence.oss-blob-rule.binding.v2"
	ossLicenseRuleVersion    = "buckley.workspaceevidence.canonical-oss-license.v1"
)

var (
	ErrOSSBlobRuleDenied    = errors.New("workspaceevidence: root license is not allowed by the OSS blob rule")
	ErrOSSBlobRuleInvalid   = errors.New("workspaceevidence: OSS blob rule is invalid")
	ErrOSSBlobRuleSpent     = errors.New("workspaceevidence: OSS blob rule is spent")
	ErrOSSPromptInvalid     = errors.New("workspaceevidence: OSS prompt must be an exact committed regular blob")
	ErrOSSPromptMismatch    = errors.New("workspaceevidence: OSS prompt bytes do not match the committed blob")
	ErrOSSBlobRuleLocalOnly = errors.New("workspaceevidence: OSS blob rule is local-only and cannot be serialized")
)

// OSSBlobRule is an opaque, host-formed, one-use authority binding one exact
// committed prompt blob to revalidated root-license evidence and a fresh run.
type OSSBlobRule struct {
	self                *OSSBlobRule
	evidence            RootLicenseBlobEvidence
	licenseRuleVersion  string
	licenseID           string
	promptPath          string
	promptMode          string
	promptBlobOID       string
	promptContentSHA256 [sha256.Size]byte
	runScope            [sha256.Size]byte
	binding             [sha256.Size]byte
	claimed             *atomic.Bool
}

// MintTrackedPromptOSSBlobRule forms one local authority for a prompt that is
// a regular blob in the exact commit already bound by evidence. Prompt bytes
// are read from the Git object database, never from the worktree.
func MintTrackedPromptOSSBlobRule(ctx context.Context, evidence RootLicenseBlobEvidence, repoRelativePromptPath string) (*OSSBlobRule, []byte, error) {
	boundedCtx, cancel, err := boundedEvidenceContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer cancel()

	promptPath, err := canonicalOSSPromptPath(repoRelativePromptPath)
	if err != nil {
		return nil, nil, err
	}
	if err := evidence.Revalidate(boundedCtx); err != nil {
		return nil, nil, fmt.Errorf("revalidate root license for OSS blob rule: %w", err)
	}
	licenseID, err := evaluateBoundOSSLicense(boundedCtx, evidence)
	if err != nil {
		return nil, nil, err
	}

	entry, prompt, err := readCommittedOSSPrompt(boundedCtx, evidence, promptPath)
	if err != nil {
		return nil, nil, err
	}
	if err := validateOSSPromptBytes(prompt); err != nil {
		return nil, nil, err
	}
	if err := revalidateRepositoryIdentity(boundedCtx, evidence.repository); err != nil {
		return nil, nil, ErrEvidenceStale
	}

	var runScope [sha256.Size]byte
	if _, err := rand.Read(runScope[:]); err != nil {
		return nil, nil, fmt.Errorf("generate OSS blob rule run scope: %w", err)
	}
	if runScope == ([sha256.Size]byte{}) {
		return nil, nil, fmt.Errorf("%w: empty run scope", ErrOSSBlobRuleInvalid)
	}

	rule := &OSSBlobRule{
		evidence:            evidence,
		licenseRuleVersion:  ossLicenseRuleVersion,
		licenseID:           licenseID,
		promptPath:          promptPath,
		promptMode:          entry.mode,
		promptBlobOID:       entry.oid,
		promptContentSHA256: sha256.Sum256(prompt),
		runScope:            runScope,
		claimed:             &atomic.Bool{},
	}
	rule.self = rule
	rule.binding = ossBlobRuleBinding(rule)
	return rule, bytes.Clone(prompt), nil
}

// ClaimForDispatch revalidates the exact repository, license, prompt object,
// and supplied prompt bytes before consuming this authority once.
func (r *OSSBlobRule) ClaimForDispatch(ctx context.Context, promptBytes []byte) ([sha256.Size]byte, error) {
	if err := validateOSSBlobRuleSeal(r); err != nil {
		return [sha256.Size]byte{}, err
	}
	if r.claimed.Load() {
		return [sha256.Size]byte{}, ErrOSSBlobRuleSpent
	}
	if err := validateOSSPromptBytes(promptBytes); err != nil {
		return [sha256.Size]byte{}, err
	}
	if subtle.ConstantTimeCompare(r.promptContentSHA256[:], sha256Bytes(promptBytes)) != 1 {
		return [sha256.Size]byte{}, ErrOSSPromptMismatch
	}

	boundedCtx, cancel, err := boundedEvidenceContext(ctx)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer cancel()
	if err := r.evidence.Revalidate(boundedCtx); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("revalidate root license for OSS dispatch: %w", err)
	}
	licenseID, err := evaluateBoundOSSLicense(boundedCtx, r.evidence)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if r.licenseRuleVersion != ossLicenseRuleVersion || licenseID != r.licenseID {
		return [sha256.Size]byte{}, ErrOSSBlobRuleDenied
	}
	entry, committedPrompt, err := readCommittedOSSPrompt(boundedCtx, r.evidence, r.promptPath)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if entry.mode != r.promptMode || entry.oid != r.promptBlobOID ||
		subtle.ConstantTimeCompare(r.promptContentSHA256[:], sha256Bytes(committedPrompt)) != 1 ||
		!bytes.Equal(committedPrompt, promptBytes) {
		return [sha256.Size]byte{}, ErrOSSPromptMismatch
	}
	if err := revalidateRepositoryIdentity(boundedCtx, r.evidence.repository); err != nil {
		return [sha256.Size]byte{}, ErrEvidenceStale
	}
	if err := revalidateExactCleanOSSDispatchWorktree(boundedCtx, r.evidence); err != nil {
		return [sha256.Size]byte{}, err
	}
	if !r.claimed.CompareAndSwap(false, true) {
		return [sha256.Size]byte{}, ErrOSSBlobRuleSpent
	}
	return r.binding, nil
}

func (OSSBlobRule) String() string {
	return "workspaceevidence.OSSBlobRule{redacted}"
}

func (OSSBlobRule) GoString() string {
	return "workspaceevidence.OSSBlobRule{redacted}"
}

func (OSSBlobRule) MarshalJSON() ([]byte, error) {
	return nil, ErrOSSBlobRuleLocalOnly
}

func (*OSSBlobRule) UnmarshalJSON([]byte) error {
	return ErrOSSBlobRuleLocalOnly
}

// evaluateBoundOSSLicense applies the OSS authority rule directly to the exact
// committed license bytes. The evidence SPDX hint is deliberately ignored: it
// remains diagnostic metadata and cannot grant authority.
func evaluateBoundOSSLicense(ctx context.Context, evidence RootLicenseBlobEvidence) (string, error) {
	content, err := readVerifiedGitObject(ctx, evidence.repository.root, "blob", evidence.blobOID, MaxLicenseBlobBytes, ErrOSSBlobRuleDenied)
	if err != nil {
		return "", fmt.Errorf("read committed license for OSS blob rule: %w", err)
	}
	contentSum := sha256.Sum256(content)
	if subtle.ConstantTimeCompare([]byte(evidence.contentSHA256), []byte(hex.EncodeToString(contentSum[:]))) != 1 {
		return "", ErrEvidenceStale
	}
	licenseID, allowed := evaluateCanonicalOSSLicense(content)
	if !allowed {
		return "", ErrOSSBlobRuleDenied
	}
	return licenseID, nil
}

// evaluateCanonicalOSSLicense is the versioned, deterministic OSS admission
// policy. It intentionally recognizes only exact canonical Apache-2.0, MIT,
// BSD-2-Clause, and BSD-3-Clause license forms.
func evaluateCanonicalOSSLicense(content []byte) (string, bool) {
	lines, normalized, err := normalizedLicenseText(content)
	if err != nil {
		return "", false
	}
	if matchesCanonicalApache20License(lines, normalized) {
		return "Apache-2.0", true
	}
	if body, ok := bodyAfterCopyright(lines, []string{"MIT License", "The MIT License (MIT)"}, false); ok &&
		body == collapseLicenseWhitespace(mitCanonicalBody) {
		return "MIT", true
	}
	if body, ok := bodyAfterCopyright(lines, []string{"BSD 2-Clause License"}, true); ok &&
		body == collapseLicenseWhitespace(bsd2CanonicalBody) {
		return "BSD-2-Clause", true
	}
	if body, ok := bodyAfterCopyright(lines, []string{"BSD 3-Clause License"}, true); ok &&
		body == collapseLicenseWhitespace(bsd3CanonicalBody) {
		return "BSD-3-Clause", true
	}
	return "", false
}

func matchesCanonicalApache20License(lines []string, normalized string) bool {
	canonicalLines, canonical, err := normalizedLicenseText([]byte(apache20CanonicalText))
	if err != nil {
		return false
	}
	if normalized == canonical {
		return true
	}

	const (
		termsEnd          = "END OF TERMS AND CONDITIONS"
		copyrightTemplate = "Copyright [yyyy] [name of copyright owner]"
	)
	actualTermsEnd := licenseLineIndex(lines, termsEnd)
	canonicalTermsEnd := licenseLineIndex(canonicalLines, termsEnd)
	if actualTermsEnd < 0 || canonicalTermsEnd < 0 ||
		collapseLicenseWhitespace(strings.Join(lines[:actualTermsEnd+1], "\n")) !=
			collapseLicenseWhitespace(strings.Join(canonicalLines[:canonicalTermsEnd+1], "\n")) {
		return false
	}

	copyrightLine := skipBlankLicenseLines(lines, actualTermsEnd+1)
	if copyrightLine >= len(lines) || !validCopyrightNotice(lines[copyrightLine]) {
		return false
	}
	actualBoilerplate := skipBlankLicenseLines(lines, copyrightLine+1)
	templateLine := licenseLineIndex(canonicalLines, copyrightTemplate)
	if actualBoilerplate >= len(lines) || templateLine < 0 {
		return false
	}
	canonicalBoilerplate := skipBlankLicenseLines(canonicalLines, templateLine+1)
	if canonicalBoilerplate >= len(canonicalLines) {
		return false
	}
	return collapseLicenseWhitespace(strings.Join(lines[actualBoilerplate:], "\n")) ==
		collapseLicenseWhitespace(strings.Join(canonicalLines[canonicalBoilerplate:], "\n"))
}

func licenseLineIndex(lines []string, target string) int {
	for i, line := range lines {
		if line == target {
			return i
		}
	}
	return -1
}

func canonicalOSSPromptPath(value string) (string, error) {
	if value == "" || len(value) > maxOSSPromptPathBytes || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n\\") || !utf8.ValidString(value) || strings.HasPrefix(value, "/") {
		return "", ErrOSSPromptInvalid
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrOSSPromptInvalid
	}
	components := strings.Split(clean, "/")
	if len(components) > maxOSSPromptPathDepth {
		return "", ErrOSSPromptInvalid
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return "", ErrOSSPromptInvalid
		}
	}
	return clean, nil
}

func validateOSSPromptBytes(prompt []byte) error {
	if len(prompt) == 0 || len(prompt) > MaxOSSPromptBlobBytes || !utf8.Valid(prompt) || bytes.IndexByte(prompt, 0) >= 0 {
		return ErrOSSPromptInvalid
	}
	return nil
}

func readCommittedOSSPrompt(ctx context.Context, evidence RootLicenseBlobEvidence, promptPath string) (gitTreeEntry, []byte, error) {
	objectHashLength := len(evidence.commitOID)
	if objectHashLength != 40 && objectHashLength != 64 {
		return gitTreeEntry{}, nil, ErrOSSBlobRuleInvalid
	}
	treeOID := evidence.rootTreeOID
	components := strings.Split(promptPath, "/")
	var entry gitTreeEntry
	for i, component := range components {
		found, err := committedTreeEntry(ctx, evidence.repository.root, treeOID, objectHashLength, component)
		if err != nil {
			return gitTreeEntry{}, nil, err
		}
		entry = found
		if i < len(components)-1 {
			if entry.kind != "tree" || (entry.mode != "40000" && entry.mode != "040000") {
				return gitTreeEntry{}, nil, ErrOSSPromptInvalid
			}
			treeOID = entry.oid
		}
	}
	if entry.kind != "blob" || (entry.mode != "100644" && entry.mode != "100755") {
		return gitTreeEntry{}, nil, ErrOSSPromptInvalid
	}
	prompt, err := readVerifiedGitObject(ctx, evidence.repository.root, "blob", entry.oid, MaxOSSPromptBlobBytes, ErrOSSPromptInvalid)
	if err != nil {
		return gitTreeEntry{}, nil, fmt.Errorf("read committed OSS prompt: %w", err)
	}
	return entry, prompt, nil
}

func committedTreeEntry(ctx context.Context, root, treeOID string, objectHashLength int, name string) (gitTreeEntry, error) {
	raw, err := readVerifiedGitObject(ctx, root, "tree", treeOID, maxRootTreeObjectBytes, ErrOSSPromptInvalid)
	if err != nil {
		return gitTreeEntry{}, fmt.Errorf("read committed OSS prompt tree: %w", err)
	}
	hashBytes := objectHashLength / 2
	var found gitTreeEntry
	matched := false
	for offset := 0; offset < len(raw); {
		modeEndRelative := bytes.IndexByte(raw[offset:], ' ')
		if modeEndRelative <= 0 {
			return gitTreeEntry{}, ErrOSSPromptInvalid
		}
		modeEnd := offset + modeEndRelative
		mode := string(raw[offset:modeEnd])
		nameStart := modeEnd + 1
		nameEndRelative := bytes.IndexByte(raw[nameStart:], 0)
		if nameEndRelative <= 0 {
			return gitTreeEntry{}, ErrOSSPromptInvalid
		}
		nameEnd := nameStart + nameEndRelative
		oidStart := nameEnd + 1
		oidEnd := oidStart + hashBytes
		if oidEnd > len(raw) {
			return gitTreeEntry{}, ErrOSSPromptInvalid
		}
		entryName := raw[nameStart:nameEnd]
		oid := hex.EncodeToString(raw[oidStart:oidEnd])
		offset = oidEnd
		if bytes.IndexByte(entryName, '/') >= 0 || len(oid) != objectHashLength || !isLowerHex(oid) {
			return gitTreeEntry{}, ErrOSSPromptInvalid
		}
		if string(entryName) != name {
			continue
		}
		if matched {
			return gitTreeEntry{}, ErrOSSPromptInvalid
		}
		found = gitTreeEntry{mode: mode, kind: gitTreeEntryKind(mode), oid: oid, path: name}
		matched = true
	}
	if !matched {
		return gitTreeEntry{}, ErrOSSPromptInvalid
	}
	return found, nil
}

func validateOSSBlobRuleSeal(rule *OSSBlobRule) error {
	if rule == nil || rule.self != rule || rule.claimed == nil || rule.runScope == ([sha256.Size]byte{}) ||
		rule.licenseRuleVersion != ossLicenseRuleVersion || rule.licenseID == "" ||
		rule.binding == ([sha256.Size]byte{}) || rule.binding != ossBlobRuleBinding(rule) {
		return ErrOSSBlobRuleInvalid
	}
	return nil
}

func ossBlobRuleBinding(rule *OSSBlobRule) [sha256.Size]byte {
	if rule == nil {
		return [sha256.Size]byte{}
	}
	hasher := sha256.New()
	writeOSSBlobRuleField(hasher, "domain", []byte(ossBlobRuleBindingDomain))
	writeOSSBlobRuleField(hasher, "rule-version", []byte(ossBlobRuleVersion))
	writeOSSBlobRuleField(hasher, "repository-id", []byte(rule.evidence.repositoryID))
	writeOSSBlobRuleField(hasher, "commit", []byte(rule.evidence.commitOID))
	writeOSSBlobRuleField(hasher, "root-tree", []byte(rule.evidence.rootTreeOID))
	writeOSSBlobRuleField(hasher, "license-path", []byte(rule.evidence.licensePath))
	writeOSSBlobRuleField(hasher, "license-blob", []byte(rule.evidence.blobOID))
	writeOSSBlobRuleField(hasher, "license-content-sha256", []byte(rule.evidence.contentSHA256))
	writeOSSBlobRuleField(hasher, "license-spdx-hint", []byte(rule.evidence.detectedSPDXHint))
	writeOSSBlobRuleField(hasher, "license-hint-version", []byte(rule.evidence.hintVersion))
	writeOSSBlobRuleField(hasher, "license-local-binding", []byte(rule.evidence.localBinding))
	writeOSSBlobRuleField(hasher, "license-rule-version", []byte(rule.licenseRuleVersion))
	writeOSSBlobRuleField(hasher, "license-id", []byte(rule.licenseID))
	writeOSSBlobRuleField(hasher, "prompt-path", []byte(rule.promptPath))
	writeOSSBlobRuleField(hasher, "prompt-mode", []byte(rule.promptMode))
	writeOSSBlobRuleField(hasher, "prompt-blob", []byte(rule.promptBlobOID))
	writeOSSBlobRuleField(hasher, "prompt-content-sha256", rule.promptContentSHA256[:])
	writeOSSBlobRuleField(hasher, "run-scope", rule.runScope[:])
	var binding [sha256.Size]byte
	copy(binding[:], hasher.Sum(nil))
	return binding
}

func writeOSSBlobRuleField(hasher hash.Hash, label string, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(label)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write([]byte(label))
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write(value)
}

func sha256Bytes(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}
