package goalloop

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/workspaceevidence"
)

const (
	LicenseStatusRecognizedOSS = workspaceevidence.LicenseStatusRecognizedOSS
	LicenseStatusMissing       = workspaceevidence.LicenseStatusMissing
	LicenseStatusAmbiguous     = workspaceevidence.LicenseStatusAmbiguous
	LicenseStatusUnsupported   = workspaceevidence.LicenseStatusUnsupported
	LicenseStatusProprietary   = workspaceevidence.LicenseStatusProprietary
	LicenseStatusUnreadable    = workspaceevidence.LicenseStatusUnreadable
	LicenseStatusChanged       = workspaceevidence.LicenseStatusChanged
	LicenseStatusNotRequired   = workspaceevidence.LicenseStatusNotRequired

	LicenseIDMIT      = workspaceevidence.LicenseIDMIT
	LicenseIDApache20 = workspaceevidence.LicenseIDApache20

	maxLicenseFileSize = workspaceevidence.MaxLicenseFileSize
	canonicalMITBody   = workspaceevidence.CanonicalMITBody
)

var rootLicenseCandidates = [...]string{
	"LICENSE",
	"LICENSE.txt",
	"LICENSE.md",
	"COPYING",
	"COPYING.txt",
	"COPYING.md",
}

// WorkspaceLicenseEvidence is retained as a source-compatible alias for the
// dependency-free workspaceevidence contract.
type WorkspaceLicenseEvidence = workspaceevidence.LicenseEvidence

// WorkspaceLicenseInspection is retained as a source-compatible alias for the
// dependency-free workspaceevidence observation.
type WorkspaceLicenseInspection = workspaceevidence.Inspection

// InspectWorkspaceLicense recognizes the bounded root-license catalog. The
// implementation lives in the leaf workspaceevidence package so adapters can
// use it without importing the goal loop.
func InspectWorkspaceLicense(workspaceRoot string) (WorkspaceLicenseInspection, error) {
	return workspaceevidence.InspectWorkspaceLicense(workspaceRoot)
}

// MatchWorkspaceLicense re-inspects the canonical root and compares its exact
// evidence with the intake binding.
func MatchWorkspaceLicense(workspaceRoot string, expected WorkspaceLicenseEvidence) (WorkspaceLicenseInspection, bool, error) {
	return workspaceevidence.MatchWorkspaceLicense(workspaceRoot, expected)
}

// inspectWorkspaceLicense preserves the package-private deterministic test seam
// used by existing goalloop tests.
func inspectWorkspaceLicense(workspaceRoot string, afterFirstRead func(string)) (WorkspaceLicenseInspection, error) {
	return workspaceevidence.InspectWorkspaceLicenseWithHook(workspaceRoot, afterFirstRead)
}

func isRootLicenseCandidate(name string) bool {
	for _, candidate := range rootLicenseCandidates {
		if name == candidate {
			return true
		}
	}
	return false
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256Hex(data []byte) string {
	return workspaceevidence.SHA256Hex(data)
}

func recognizeLicense(data []byte) string {
	return workspaceevidence.RecognizeLicense(data)
}

func recognizedExactLicenseDigest(digest string) string {
	return workspaceevidence.RecognizedExactLicenseDigest(digest)
}

func recognizeMIT(text string) bool {
	const prefix = "MIT License\n\n"
	if !strings.HasPrefix(text, prefix) {
		return false
	}
	rest := strings.TrimPrefix(text, prefix)
	copyright, body, ok := strings.Cut(rest, "\n\n")
	if !ok || copyright == "" || len(copyright) > 512 {
		return false
	}
	for _, line := range strings.Split(copyright, "\n") {
		if !strings.HasPrefix(line, "Copyright (c) ") || len(line) <= len("Copyright (c) ") || !safeLicenseHeader(line) {
			return false
		}
	}
	return body == canonicalMITBody
}

func safeLicenseHeader(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func containsProprietaryMarker(data []byte) bool {
	return workspaceevidence.ContainsProprietaryMarker(data)
}
