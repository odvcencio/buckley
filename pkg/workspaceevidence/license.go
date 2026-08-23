// Package workspaceevidence observes bounded, root-bound workspace license
// evidence without importing the goal loop or any provider-facing package.
package workspaceevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	LicenseStatusRecognizedOSS = "recognized_oss"
	LicenseStatusMissing       = "missing"
	LicenseStatusAmbiguous     = "ambiguous"
	LicenseStatusUnsupported   = "unsupported"
	LicenseStatusProprietary   = "proprietary"
	LicenseStatusUnreadable    = "unreadable"
	LicenseStatusChanged       = "changed"
	LicenseStatusNotRequired   = "not_required"

	LicenseIDMIT      = "MIT"
	LicenseIDApache20 = "Apache-2.0"

	MaxLicenseFileSize = 64 << 10
)

var rootLicenseCandidates = [...]string{
	"LICENSE",
	"LICENSE.txt",
	"LICENSE.md",
	"COPYING",
	"COPYING.txt",
	"COPYING.md",
}

// CanonicalMITBody is the exact MIT license body accepted by the v1 catalog.
// It is exported so compatibility wrappers can keep their source-level test
// seam without duplicating the catalog text.
const CanonicalMITBody = `Permission is hereby granted, free of charge, to any person obtaining a copy
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

// LicenseEvidence is the compact immutable identity of a recognized root
// license. The license text itself never enters goal or workflow history.
type LicenseEvidence struct {
	File           string
	ID             string
	SHA256         string
	ManifestSHA256 string
}

func (e LicenseEvidence) IsZero() bool {
	return e.File == "" && e.ID == "" && e.SHA256 == "" && e.ManifestSHA256 == ""
}

func (e LicenseEvidence) Validate() error {
	if e.IsZero() {
		return nil
	}
	if !isRootLicenseCandidate(e.File) {
		return fmt.Errorf("workspaceevidence: workspace license file is invalid")
	}
	if e.ID != LicenseIDMIT && e.ID != LicenseIDApache20 {
		return fmt.Errorf("workspaceevidence: workspace license identifier is unsupported")
	}
	if !isSHA256Hex(e.SHA256) || !isSHA256Hex(e.ManifestSHA256) {
		return fmt.Errorf("workspaceevidence: workspace license digest is invalid")
	}
	return nil
}

// Inspection is a bounded host observation. Evidence is populated only for a
// single recognized root license.
type Inspection struct {
	Status   string
	Evidence LicenseEvidence
}

// NormalizeWorkspaceRoot resolves a workspace directory to one stable local
// identity. Symlinks are resolved so a caller cannot bind evidence to one
// directory while a later adapter addresses another through an alias.
func NormalizeWorkspaceRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("workspaceevidence: workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("workspaceevidence: resolve workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspaceevidence: resolve workspace root %s: %w", abs, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("workspaceevidence: inspect workspace root %s: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspaceevidence: workspace root is not a directory: %s", resolved)
	}
	return filepath.Clean(resolved), nil
}

// InspectWorkspaceLicense recognizes a deliberately small v1 catalog: exact
// canonical MIT and Apache-2.0 texts at the workspace root. It uses os.Root so
// candidate names cannot escape the canonical workspace and verifies that the
// opened file is the same regular, non-symlink file observed before and after
// the read.
func InspectWorkspaceLicense(workspaceRoot string) (Inspection, error) {
	return inspectWorkspaceLicense(workspaceRoot, nil)
}

// InspectWorkspaceLicenseWithHook is a deterministic test seam. Production
// callers should use InspectWorkspaceLicense; the callback runs after the
// first bounded read of each candidate and before the second read.
func InspectWorkspaceLicenseWithHook(workspaceRoot string, afterFirstRead func(string)) (Inspection, error) {
	return inspectWorkspaceLicense(workspaceRoot, afterFirstRead)
}

func inspectWorkspaceLicense(workspaceRoot string, afterFirstRead func(string)) (Inspection, error) {
	rootPath, err := NormalizeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return Inspection{Status: LicenseStatusUnreadable}, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return Inspection{Status: LicenseStatusUnreadable}, fmt.Errorf("workspaceevidence: open workspace root: %w", err)
	}
	defer root.Close()

	type candidate struct {
		name   string
		status string
		id     string
		digest string
	}
	var found []candidate
	for _, name := range rootLicenseCandidates {
		info, statErr := root.Lstat(name)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			found = append(found, candidate{name: name, status: LicenseStatusUnreadable})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxLicenseFileSize {
			found = append(found, candidate{name: name, status: LicenseStatusUnreadable})
			continue
		}

		first, firstInfo, ok := readStableRootLicense(root, name, info)
		if !ok {
			found = append(found, candidate{name: name, status: LicenseStatusChanged})
			continue
		}
		if afterFirstRead != nil {
			afterFirstRead(name)
		}
		second, secondInfo, ok := readStableRootLicense(root, name, firstInfo)
		firstDigest := SHA256Hex(first)
		secondDigest := SHA256Hex(second)
		if !ok || !sameLicenseMetadata(firstInfo, secondInfo) || firstDigest != secondDigest || !bytes.Equal(first, second) {
			found = append(found, candidate{name: name, status: LicenseStatusChanged})
			continue
		}

		digest := secondDigest
		id := RecognizeLicense(second)
		status := LicenseStatusRecognizedOSS
		if id == "" {
			status = LicenseStatusUnsupported
			if ContainsProprietaryMarker(second) {
				status = LicenseStatusProprietary
			}
		}
		found = append(found, candidate{name: name, status: status, id: id, digest: digest})
	}

	if len(found) == 0 {
		return Inspection{Status: LicenseStatusMissing}, nil
	}
	if len(found) != 1 {
		return Inspection{Status: LicenseStatusAmbiguous}, nil
	}
	item := found[0]
	if item.status != LicenseStatusRecognizedOSS {
		return Inspection{Status: item.status}, nil
	}
	evidence := LicenseEvidence{File: item.name, ID: item.id, SHA256: item.digest}
	evidence.ManifestSHA256 = SHA256Hex([]byte(strings.Join([]string{
		"buckley.workspace-license.v1",
		evidence.File,
		evidence.ID,
		evidence.SHA256,
	}, "\x00")))
	return Inspection{Status: LicenseStatusRecognizedOSS, Evidence: evidence}, nil
}

// MatchWorkspaceLicense re-inspects the exact canonical workspace and reports
// whether its current recognized evidence still equals the intake binding.
func MatchWorkspaceLicense(workspaceRoot string, expected LicenseEvidence) (Inspection, bool, error) {
	inspection, err := InspectWorkspaceLicense(workspaceRoot)
	if err != nil {
		return inspection, false, err
	}
	if expected.IsZero() {
		return inspection, inspection.Status == LicenseStatusRecognizedOSS, nil
	}
	if err := expected.Validate(); err != nil {
		return Inspection{Status: LicenseStatusChanged}, false, err
	}
	match := inspection.Status == LicenseStatusRecognizedOSS && inspection.Evidence == expected
	if !match {
		inspection.Status = LicenseStatusChanged
	}
	return inspection, match, nil
}

func readStableRootLicense(root *os.Root, name string, expected os.FileInfo) ([]byte, os.FileInfo, bool) {
	initial, err := root.Lstat(name)
	if err != nil || initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() ||
		initial.Size() <= 0 || initial.Size() > MaxLicenseFileSize ||
		!os.SameFile(expected, initial) || !sameLicenseMetadata(expected, initial) {
		return nil, nil, false
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, false
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, MaxLicenseFileSize+1))
	readDone, afterReadErr := file.Stat()
	closeErr := file.Close()
	after, lstatErr := root.Lstat(name)
	if statErr != nil || readErr != nil || afterReadErr != nil || closeErr != nil || lstatErr != nil ||
		len(data) > MaxLicenseFileSize || after.Mode()&os.ModeSymlink != 0 ||
		!opened.Mode().IsRegular() || !readDone.Mode().IsRegular() || !after.Mode().IsRegular() {
		return nil, nil, false
	}
	infos := []os.FileInfo{opened, readDone, after}
	for _, current := range infos {
		if !os.SameFile(initial, current) || !sameLicenseMetadata(initial, current) {
			return nil, nil, false
		}
	}
	return data, after, true
}

func sameLicenseMetadata(left, right os.FileInfo) bool {
	if left == nil || right == nil || left.Size() != right.Size() || left.Mode() != right.Mode() || !left.ModTime().Equal(right.ModTime()) {
		return false
	}
	leftSec, leftNsec, leftOK := licenseChangeTime(left)
	rightSec, rightNsec, rightOK := licenseChangeTime(right)
	if leftOK != rightOK {
		return false
	}
	return !leftOK || leftSec == rightSec && leftNsec == rightNsec
}

func licenseChangeTime(info os.FileInfo) (int64, int64, bool) {
	if info == nil || info.Sys() == nil {
		return 0, 0, false
	}
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0, 0, false
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	for _, fieldName := range []string{"Ctim", "Ctimespec"} {
		field := value.FieldByName(fieldName)
		if sec, nsec, ok := reflectedTimespec(field); ok {
			return sec, nsec, true
		}
	}
	sec, secOK := reflectedInt(value.FieldByName("Ctime"))
	nsec, nsecOK := reflectedInt(value.FieldByName("Ctimensec"))
	if secOK && nsecOK {
		return sec, nsec, true
	}
	return 0, 0, false
}

func reflectedTimespec(value reflect.Value) (int64, int64, bool) {
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return 0, 0, false
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	sec, secOK := reflectedInt(value.FieldByName("Sec"))
	nsec, nsecOK := reflectedInt(value.FieldByName("Nsec"))
	return sec, nsec, secOK && nsecOK
}

func reflectedInt(value reflect.Value) (int64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		uintValue := value.Uint()
		if uintValue > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(uintValue), true
	default:
		return 0, false
	}
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

// SHA256Hex returns the lowercase SHA-256 digest of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// RecognizeLicense returns the canonical catalog identifier for data, or an
// empty string when the bounded text is not recognized.
func RecognizeLicense(data []byte) string {
	if !utf8.Valid(data) {
		return ""
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, " \t\n") + "\n"
	if recognizeMIT(normalized) {
		return LicenseIDMIT
	}
	apache := strings.Trim(normalized, "\n") + "\n"
	if RecognizedExactLicenseDigest(SHA256Hex([]byte(apache))) == LicenseIDApache20 {
		return LicenseIDApache20
	}
	return ""
}

// RecognizedExactLicenseDigest maps the one canonical Apache-2.0 text digest
// to its catalog identifier.
func RecognizedExactLicenseDigest(digest string) string {
	if digest == "c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4" {
		return LicenseIDApache20
	}
	return ""
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
	return body == CanonicalMITBody
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

// ContainsProprietaryMarker reports the deliberately small proprietary marker
// catalog used for fail-closed status projection.
func ContainsProprietaryMarker(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "all rights reserved") || strings.Contains(lower, "proprietary and confidential")
}
