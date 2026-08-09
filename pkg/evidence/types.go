// Package evidence implements the Buckley evidence store: a content-addressed,
// typed record of durable artifacts (source, tool results, diffs, reviews,
// checkpoints, and similar objects) that the context fabric and run ledger
// reference by ID.
//
// See docs/architecture/decisions/0001-sqlite-with-wal-mode.md for the
// persistence conventions this package follows.
package evidence

import (
	"crypto/sha256"
	"encoding/base32"
	"time"
)

// Kind identifies the category of an evidence object.
type Kind string

// Evidence object kinds.
const (
	KindSource          Kind = "source"
	KindProjection      Kind = "projection"
	KindContextBundle   Kind = "context_bundle"
	KindContextManifest Kind = "context_manifest"
	KindModelRequest    Kind = "model_request"
	KindModelResponse   Kind = "model_response"
	KindToolRequest     Kind = "tool_request"
	KindToolResult      Kind = "tool_result"
	KindCommandOutput   Kind = "command_output"
	KindTestOutput      Kind = "test_output"
	KindDiff            Kind = "diff"
	KindReview          Kind = "review"
	KindCommitProposal  Kind = "commit_proposal"
	KindCheckpoint      Kind = "checkpoint"
	KindSubagentReport  Kind = "subagent_report"
	KindMemoryRecall    Kind = "memory_recall"
)

// Sensitivity classifies how restricted an evidence object's content is.
type Sensitivity string

// Sensitivity levels, ordered from least to most restricted.
const (
	SensitivityPublic         Sensitivity = "public"
	SensitivityWorkspace      Sensitivity = "workspace"
	SensitivityConfidential   Sensitivity = "confidential"
	SensitivitySecretDetected Sensitivity = "secret_detected"
)

// Storage identifies where an object's body currently lives.
type Storage string

// Storage tiers.
const (
	StorageInline Storage = "inline"
	StorageBlob   Storage = "blob"
)

// InlineThreshold is the largest object body, in bytes, that MAY stay inline
// in SQLite. Larger bodies MUST be written as zstd-compressed blob files (see
// spec section 13.2).
const InlineThreshold = 8 * 1024

// Object is a single durable evidence record.
type Object struct {
	ID              string
	Kind            Kind
	MediaType       string
	Encoding        string
	ContentSHA256   string
	ByteCount       int64
	EstimatedTokens int
	Sensitivity     Sensitivity
	Storage         Storage
	InlineBody      []byte
	BlobPath        string
	Metadata        map[string]any
	CreatedAt       time.Time
}

// ObjectSummary is a lightweight projection of Object returned by Query. It
// omits body bytes so that listing large result sets never materializes
// evidence content.
type ObjectSummary struct {
	ID              string
	Kind            Kind
	MediaType       string
	ContentSHA256   string
	ByteCount       int64
	EstimatedTokens int
	Sensitivity     Sensitivity
	Storage         Storage
	Metadata        map[string]any
	CreatedAt       time.Time
}

// idEncoding is the base32 alphabet used for evidence IDs. Standard
// (uppercase, unpadded) encoding matches the ULID convention already used
// elsewhere in Buckley (github.com/oklog/ulid), so evidence and run IDs read
// consistently side by side.
var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// idFieldSeparator delimits the kind and media-type fields hashed into an
// evidence ID. The spec's formula, "sha256(kind + media_type + content)",
// does not specify a separator; without one, ("ab","c",body) and
// ("a","bc",body) would hash identically. A NUL byte cannot appear in a Kind
// or MediaType value in practice, so it is used here to remove that
// ambiguity while leaving the digest of (kind, media_type) with no content
// unaffected.
const idFieldSeparator = "\x00"

// ComputeContentID derives the deterministic evidence ID for a given kind,
// media type, and content body:
//
//	ev_<base32(sha256(kind + media_type + content))[:26]>
//
// Identical content of the same kind and media type always produces the same
// ID, which is how the store deduplicates objects (section 13.1).
func ComputeContentID(kind Kind, mediaType string, content []byte) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte(idFieldSeparator))
	h.Write([]byte(mediaType))
	h.Write([]byte(idFieldSeparator))
	h.Write(content)
	sum := h.Sum(nil)

	encoded := idEncoding.EncodeToString(sum)
	if len(encoded) > 26 {
		encoded = encoded[:26]
	}
	return "ev_" + encoded
}

// ContentSHA256Hex returns the lowercase hex-encoded SHA-256 digest of
// content. It is the value stored in Object.ContentSHA256.
func ContentSHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hexEncode(sum[:])
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0f]
	}
	return string(out)
}
