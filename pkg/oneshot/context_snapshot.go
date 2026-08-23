package oneshot

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"sort"
)

const contextSnapshotDomain = "buckley.oneshot.context-snapshot.v1\x00"

// ContextSnapshot is an immutable, self-bound set of already gathered context
// sources. Its contents are deliberately private so callers cannot substitute
// live context gathering after admission.
type ContextSnapshot struct {
	self    *ContextSnapshot
	sources map[string]string
	digest  [sha256.Size]byte
}

// NewContextSnapshot clones and seals already gathered context sources.
func NewContextSnapshot(sources map[string]string) (*ContextSnapshot, error) {
	snapshot := &ContextSnapshot{sources: cloneContextSources(sources)}
	snapshot.self = snapshot
	snapshot.digest = digestContextSources(snapshot.sources)
	return snapshot, nil
}

func (s *ContextSnapshot) context() (*Context, error) {
	if s == nil || s.self != s {
		return nil, fmt.Errorf("invalid immutable context snapshot")
	}
	want := digestContextSources(s.sources)
	if subtle.ConstantTimeCompare(s.digest[:], want[:]) != 1 {
		return nil, fmt.Errorf("immutable context snapshot changed")
	}

	ctx := &Context{Sources: cloneContextSources(s.sources)}
	for _, content := range ctx.Sources {
		ctx.Tokens += contextEstimateTokens(content)
	}
	return ctx, nil
}

func cloneContextSources(sources map[string]string) map[string]string {
	cloned := make(map[string]string, len(sources))
	for label, content := range sources {
		cloned[label] = content
	}
	return cloned
}

func digestContextSources(sources map[string]string) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write([]byte(contextSnapshotDomain))
	labels := make([]string, 0, len(sources))
	for label := range sources {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(labels)))
	_, _ = h.Write(size[:])
	for _, label := range labels {
		writeContextSnapshotField(h, []byte(label))
		writeContextSnapshotField(h, []byte(sources[label]))
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

type contextSnapshotHash interface {
	Write([]byte) (int, error)
}

func writeContextSnapshotField(h contextSnapshotHash, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}
