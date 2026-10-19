package subagent

import "m31labs.dev/buckley/pkg/agentcoord"

const (
	DefaultCompletedRunCacheEntries       = 64
	DefaultCompletedRunCacheBytes   int64 = 4 * 1024 * 1024
)

type completedRunCacheEntry struct {
	id    string
	bytes int64
}

func (c *Coordinator) cacheCompletedRun(snapshot Snapshot) {
	c.mu.Lock()
	bytes := c.completedRunBytesLocked(snapshot)
	c.completedCache = append(c.completedCache, completedRunCacheEntry{id: snapshot.ID, bytes: bytes})
	c.completedCacheBytes += bytes
	c.mu.Unlock()
	c.trimCompletedCache()
}

// Count retained contracts and projections without serializing another copy of
// large task bodies. Shared strings are conservatively charged to each owner.
func (c *Coordinator) completedRunBytesLocked(snapshot Snapshot) int64 {
	spec := c.runs[snapshot.ID]
	bytes := int64(2048) + retainedStringsBytes(
		snapshot.ID, snapshot.SessionID, snapshot.ParentSessionID, snapshot.ParentRunID,
		snapshot.TaskID, snapshot.TurnID, snapshot.AttemptID, snapshot.Agent, snapshot.Spec,
		snapshot.Task, string(snapshot.State), snapshot.Output, snapshot.Error,
		snapshot.Persona, snapshot.Model, string(snapshot.Tier), snapshot.Effort,
		snapshot.Isolation, snapshot.OutputSchema, snapshot.ApprovalPosture,
		spec.ID, spec.SessionID, spec.RunID, spec.ParentRunID, spec.ParentSessionID,
		spec.TurnID, spec.AttemptID, spec.Agent, spec.Spec, spec.Task, spec.Persona,
		spec.Model, spec.Tier, spec.Effort, spec.SystemPrompt, spec.Isolation,
		spec.OutputSchema, spec.ApprovalPosture, c.durabilityError[snapshot.ID],
	)
	for _, values := range [][]string{snapshot.AllowedTools, snapshot.WorkspaceClaims,
		spec.Dependencies, spec.AllowedTools, spec.WorkspaceClaims, c.evidenceByRun[snapshot.ID]} {
		bytes += retainedStringsBytes(values...)
	}
	bytes += retainedSourceScopeBytes(snapshot.SourceScope) + retainedSourceScopeBytes(spec.SourceScope)
	for key, value := range spec.Metadata {
		bytes += 128 + int64(len(key)+len(value))
	}
	messages := c.mailboxes[snapshot.ID]
	bytes += int64(cap(messages)) * 512
	for _, msg := range messages {
		bytes += retainedStringsBytes(msg.Version, msg.ID, msg.MessageID, msg.IdempotencyKey,
			msg.SessionID, msg.RunID, msg.ParentRunID, msg.TaskID, msg.TurnID, msg.CorrelationID,
			msg.CausationID, msg.AttemptID, msg.SourceAttemptID, msg.From, msg.To, msg.Kind,
			msg.Content, msg.ContentRef, msg.ContentDigest, msg.EnvelopeDigest, msg.MediaType,
			msg.Preview, msg.Delivery, msg.State, msg.LeaseOwner, msg.LastError)
		bytes += retainedStringsBytes(msg.EvidenceRefs...)
	}
	return bytes
}

func retainedStringsBytes(values ...string) int64 {
	bytes := int64(cap(values)) * 16
	for _, value := range values {
		bytes += int64(len(value))
	}
	return bytes
}

func retainedSourceScopeBytes(scope *agentcoord.SourceScope) int64 {
	if scope == nil {
		return 0
	}
	bytes := int64(32 + 32*cap(scope.Files))
	for _, file := range scope.Files {
		bytes += int64(len(file.Path))
	}
	return bytes
}

func (c *Coordinator) trimCompletedCache() {
	if c.manager == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := 0; index < len(c.completedCache) &&
		(len(c.completedCache) > c.completedCacheEntries || c.completedCacheBytes > c.completedCacheLimit); {
		entry := c.completedCache[index]
		if !c.manager.evictCompleted(entry.id) {
			index++
			continue
		}
		c.completedCacheBytes -= entry.bytes
		copy(c.completedCache[index:], c.completedCache[index+1:])
		c.completedCache[len(c.completedCache)-1] = completedRunCacheEntry{}
		c.completedCache = c.completedCache[:len(c.completedCache)-1]
		c.dropCachedRunLocked(entry.id)
	}
}

func (c *Coordinator) dropCachedRunLocked(id string) {
	delete(c.runs, id)
	delete(c.evidenceByRun, id)
	delete(c.durabilityError, id)
	delete(c.mailboxes, id)
}
