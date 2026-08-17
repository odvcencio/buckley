package commitmsg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ChangeMetadata is privacy-preserving provenance for a generated commit.
//
// The digest binds the message to the exact staged diff that was reviewed,
// while the aggregate counts provide useful context without copying paths,
// filenames, or source text into the commit history.
type ChangeMetadata struct {
	Digest      string
	Files       int
	Insertions  int
	Deletions   int
	BinaryFiles int
}

var changeDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Valid reports whether the metadata is safe to render.
func (m ChangeMetadata) Valid() bool {
	return changeDigestPattern.MatchString(strings.ToLower(strings.TrimSpace(m.Digest))) &&
		m.Files >= 0 && m.Insertions >= 0 && m.Deletions >= 0 && m.BinaryFiles >= 0
}

// AppendChangeMetadata appends canonical Buckley trailers to message.
// Existing Buckley trailers are removed first so retries and user edits do
// not create contradictory provenance records. Invalid metadata is ignored.
func AppendChangeMetadata(message string, metadata ChangeMetadata) string {
	if !metadata.Valid() {
		return ensureTrailingNewline(strings.TrimSpace(message))
	}

	base := stripBuckleyTrailers(message)
	if base == "" {
		return fmt.Sprintf("Buckley-Change-Hash: %s\nBuckley-Change-Stats: %s\n",
			normalizedDigest(metadata.Digest), metadata.statsValue())
	}
	return base + "\n\nBuckley-Change-Hash: " + normalizedDigest(metadata.Digest) +
		"\nBuckley-Change-Stats: " + metadata.statsValue() + "\n"
}

func (m ChangeMetadata) statsValue() string {
	return "files=" + strconv.Itoa(m.Files) +
		" insertions=" + strconv.Itoa(m.Insertions) +
		" deletions=" + strconv.Itoa(m.Deletions) +
		" binaries=" + strconv.Itoa(m.BinaryFiles)
}

func normalizedDigest(digest string) string {
	return strings.ToLower(strings.TrimSpace(digest))
}

func ensureTrailingNewline(message string) string {
	message = strings.TrimRight(message, "\n")
	if message == "" {
		return ""
	}
	return message + "\n"
}

func stripBuckleyTrailers(message string) string {
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	filtered := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Buckley-Change-Hash:") ||
			strings.HasPrefix(trimmed, "Buckley-Change-Stats:") {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.TrimSpace(strings.Join(filtered, "\n"))
}
