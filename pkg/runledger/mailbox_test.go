package runledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func TestMailbox_EnqueueIdempotencyAndConflict(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-mailbox", "run-mailbox")
	base := agentcoord.Message{
		MessageID:      "msg-turn-1",
		SessionID:      "session-mailbox",
		RunID:          "run-mailbox",
		To:             "run-mailbox",
		From:           "operator",
		Kind:           "steer",
		IdempotencyKey: "turn-1",
		ContentRef:     "ev-1",
		ContentDigest:  "digest-a",
		ByteCount:      7,
		Preview:        "bounded",
	}
	first, created, err := store.EnqueueOperatorSteer(ctx, base)
	if err != nil || !created {
		t.Fatalf("first enqueue = %+v, created=%v, err=%v", first, created, err)
	}
	second, created, err := store.EnqueueOperatorSteer(ctx, base)
	if err != nil || created || second.Sequence != first.Sequence || second.ID != first.ID {
		t.Fatalf("duplicate enqueue = %+v, created=%v, err=%v", second, created, err)
	}
	differentMessageID := base
	differentMessageID.MessageID = "msg-turn-1-drift"
	if _, _, err := store.EnqueueOperatorSteer(ctx, differentMessageID); !errors.Is(err, ErrMailboxIdempotencyConflict) {
		t.Fatalf("message id drift error = %v, want ErrMailboxIdempotencyConflict", err)
	}
	base.ContentDigest = "digest-b"
	if _, _, err := store.EnqueueOperatorSteer(ctx, base); !errors.Is(err, ErrMailboxIdempotencyConflict) {
		t.Fatalf("digest drift error = %v, want ErrMailboxIdempotencyConflict", err)
	}
	drifts := []struct {
		name   string
		mutate func(*agentcoord.Message)
		want   error
	}{
		{name: "content ref", mutate: func(message *agentcoord.Message) { message.ContentRef = "ev-2" }},
		{name: "media type", mutate: func(message *agentcoord.Message) { message.MediaType = "text/plain" }},
		{name: "byte count", mutate: func(message *agentcoord.Message) { message.ByteCount++ }},
		{name: "preview", mutate: func(message *agentcoord.Message) { message.Preview = "different preview" }},
		{name: "correlation", mutate: func(message *agentcoord.Message) { message.CorrelationID = "corr-2" }},
		{name: "causation", mutate: func(message *agentcoord.Message) { message.CausationID = "cause-2" }},
		{name: "turn", mutate: func(message *agentcoord.Message) { message.TurnID = "turn-2" }},
		{name: "task", mutate: func(message *agentcoord.Message) { message.TaskID = "task-2" }, want: ErrMailboxInvalid},
		{name: "parent", mutate: func(message *agentcoord.Message) { message.ParentRunID = "parent-2" }, want: ErrMailboxInvalid},
		{name: "to", mutate: func(message *agentcoord.Message) { message.To = "other-target" }, want: ErrMailboxInvalid},
		{name: "schema version", mutate: func(message *agentcoord.Message) { message.Version = "m31.agent.message.v2" }, want: ErrMailboxInvalid},
	}
	for _, test := range drifts {
		t.Run(test.name, func(t *testing.T) {
			message := agentcoord.Message{
				MessageID: "msg-turn-1",
				SessionID: "session-mailbox", RunID: "run-mailbox", To: "run-mailbox", From: "operator", Kind: "steer",
				IdempotencyKey: "turn-1", ContentRef: "ev-1", ContentDigest: "digest-a", ByteCount: 7, Preview: "bounded",
			}
			test.mutate(&message)
			want := test.want
			if want == nil {
				want = ErrMailboxIdempotencyConflict
			}
			if _, _, err := store.EnqueueOperatorSteer(ctx, message); !errors.Is(err, want) {
				t.Fatalf("envelope drift error = %v, want %v", err, want)
			}
		})
	}
	if _, err := store.db.Exec(`UPDATE agent_mailbox SET envelope_digest = '' WHERE message_id = ?`, first.ID); err == nil {
		t.Fatal("storage accepted a malformed envelope digest")
	}
}

func TestMailbox_EnqueueRejectsNoncanonicalTargetBeforeDurableSideEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*agentcoord.Message)
	}{
		{name: "schema", mutate: func(message *agentcoord.Message) { message.Version = "m31.agent.message.v2" }},
		{name: "target", mutate: func(message *agentcoord.Message) { message.To = "run-other" }},
		{name: "parent", mutate: func(message *agentcoord.Message) { message.ParentRunID = "run-other-parent" }},
		{name: "task", mutate: func(message *agentcoord.Message) { message.TaskID = "task-other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMailboxTestStore(t)
			ctx := context.Background()
			if _, err := store.StartRun(ctx, AgentRun{
				RunID: "run-canonical-target", SessionID: "session-canonical-target",
				ParentRunID: "run-parent", TaskID: "task-target", Status: "queued",
			}); err != nil {
				t.Fatal(err)
			}
			message := agentcoord.Message{
				MessageID: "message-invalid-first", IdempotencyKey: "invalid-first",
				SessionID: "session-canonical-target", RunID: "run-canonical-target",
				ParentRunID: "run-parent", TaskID: "task-target", To: "run-canonical-target",
				ContentRef: "evidence-invalid", ContentDigest: "digest-invalid", ByteCount: 1,
			}
			test.mutate(&message)
			if _, _, err := store.EnqueueOperatorSteer(ctx, message); !errors.Is(err, ErrMailboxInvalid) {
				t.Fatalf("enqueue error=%v, want ErrMailboxInvalid", err)
			}
			for _, table := range []string{"agent_mailbox", "run_events", "run_event_ralph_outbox"} {
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("%s rows=%d after rejected first insert", table, count)
				}
			}
		})
	}
}

func TestMailbox_EnqueueRejectsUnsafeIdentifierTextBeforeSideEffectsAndRepairsIdempotently(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*agentcoord.Message)
		repair func(*agentcoord.Message, AgentRun)
	}{
		{
			name: "invalid UTF-8 message id",
			mutate: func(message *agentcoord.Message) {
				message.MessageID = string([]byte{'m', 'e', 's', 's', 'a', 'g', 'e', '-', 0xff})
			},
			repair: func(message *agentcoord.Message, _ AgentRun) { message.MessageID = "message-safe" },
		},
		{
			name:   "internal source control",
			mutate: func(message *agentcoord.Message) { message.From = "run-source\x00forged" },
			repair: func(message *agentcoord.Message, source AgentRun) { message.From = source.RunID },
		},
		{
			name:   "internal kind control",
			mutate: func(message *agentcoord.Message) { message.Kind = "message\nforged" },
			repair: func(message *agentcoord.Message, _ AgentRun) { message.Kind = "message" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMailboxTestStore(t)
			ctx := context.Background()
			source, _, err := store.EnsureRunContract(ctx, AgentRun{
				RunID: "run-safe-source", SessionID: "session-safe-text", TaskID: "task-safe-source", Status: "running",
			}, "digest-safe-source", "evidence-safe-source")
			if err != nil {
				t.Fatal(err)
			}
			target, _, err := store.EnsureRunContract(ctx, AgentRun{
				RunID: "run-safe-target", SessionID: source.SessionID, ParentRunID: source.RunID,
				TaskID: "task-safe-target", Status: "queued",
			}, "digest-safe-target", "evidence-safe-target")
			if err != nil {
				t.Fatal(err)
			}
			lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
				SessionID: source.SessionID, RunID: source.RunID, TaskID: source.TaskID,
				AttemptID: "attempt-safe-source", LeaseDuration: time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			message := agentcoord.Message{
				MessageID: "message-safe", IdempotencyKey: "key-safe",
				SessionID: target.SessionID, RunID: target.RunID, ParentRunID: target.ParentRunID,
				TaskID: target.TaskID, From: source.RunID, To: target.RunID, Kind: "message",
				SourceAttemptID: lease.AttemptID, SourceLeaseGeneration: lease.LeaseGeneration,
				ContentRef: "evidence-safe-message", ContentDigest: "digest-safe-message", ByteCount: 1,
			}
			test.mutate(&message)
			if _, _, err := store.Enqueue(ctx, message); !errors.Is(err, ErrMailboxInvalid) {
				t.Fatalf("unsafe enqueue error=%v, want ErrMailboxInvalid", err)
			}
			for _, table := range []string{"agent_mailbox", "run_events", "run_event_ralph_outbox"} {
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("%s rows=%d after unsafe enqueue", table, count)
				}
			}

			test.repair(&message, source)
			first, created, err := store.Enqueue(ctx, message)
			if err != nil || !created {
				t.Fatalf("repaired enqueue=%+v created=%t err=%v", first, created, err)
			}
			second, created, err := store.Enqueue(ctx, message)
			if err != nil || created || second.MessageID != first.MessageID || second.Sequence != first.Sequence {
				t.Fatalf("repaired duplicate=%+v created=%t err=%v", second, created, err)
			}
			page, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
				SessionID: target.SessionID, RunID: target.RunID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Messages) != 1 || page.Messages[0].MessageID != first.MessageID ||
				page.Messages[0].Direction != agentcoord.MailboxFromParent || page.Messages[0].PeerRunID != source.RunID {
				t.Fatalf("observable repaired mailbox=%+v", page)
			}
		})
	}
}

func TestMailbox_ReservedOperatorProvenanceRequiresTrustedPath(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-operator", "run-operator-target")

	base := agentcoord.Message{
		MessageID: "msg-operator-forgery", SessionID: "session-operator", RunID: "run-operator-target", To: "run-operator-target",
		IdempotencyKey: "operator-forgery", ContentRef: "ev-operator-forgery", ContentDigest: "digest-operator-forgery", ByteCount: 1,
	}
	for _, provenance := range []struct{ from, kind string }{
		{from: agentcoord.OperatorIdentity, kind: "message"},
		{from: "run-user", kind: agentcoord.OperatorSteerKind},
		{from: "OpErAtOr", kind: "message"},
	} {
		message := base
		message.From, message.Kind = provenance.from, provenance.kind
		if _, _, err := store.Enqueue(ctx, message); !errors.Is(err, ErrMailboxReservedProvenance) {
			t.Fatalf("generic provenance from=%q kind=%q error=%v, want ErrMailboxReservedProvenance", provenance.from, provenance.kind, err)
		}
	}

	trusted := base
	trusted.MessageID = "msg-operator-trusted"
	trusted.IdempotencyKey = "operator-trusted"
	persisted, created, err := store.EnqueueOperatorSteer(ctx, trusted)
	if err != nil || !created || persisted.From != agentcoord.OperatorIdentity || persisted.Kind != agentcoord.OperatorSteerKind {
		t.Fatalf("trusted steer=%+v created=%t err=%v", persisted, created, err)
	}
	badFence := trusted
	badFence.MessageID = "msg-operator-bad-fence"
	badFence.IdempotencyKey = "operator-bad-fence"
	badFence.AttemptID = "attempt-invented"
	badFence.LeaseGeneration = 1
	if _, _, err := store.EnqueueOperatorSteer(ctx, badFence); !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("trusted steer with foreign target fence=%v, want ErrAttachmentStale", err)
	}

	if _, err := store.StartRun(ctx, AgentRun{RunID: "OpErAtOr", SessionID: "session-reserved"}); !errors.Is(err, ErrReservedAgentIdentity) {
		t.Fatalf("reserved StartRun=%v, want ErrReservedAgentIdentity", err)
	}
	if _, _, err := store.EnsureRunContract(ctx, AgentRun{RunID: agentcoord.OperatorIdentity, SessionID: "session-reserved"}, "digest", "evidence"); !errors.Is(err, ErrReservedAgentIdentity) {
		t.Fatalf("reserved EnsureRunContract=%v, want ErrReservedAgentIdentity", err)
	}
	if _, err := store.StartRun(ctx, AgentRun{RunID: "run-reserved-parent", SessionID: "session-reserved", ParentRunID: agentcoord.OperatorIdentity}); !errors.Is(err, ErrReservedAgentIdentity) {
		t.Fatalf("reserved parent StartRun=%v, want ErrReservedAgentIdentity", err)
	}
}

func TestMailbox_EnvelopeDigestSeparatesImmutableFromDeliveryFence(t *testing.T) {
	base := agentcoord.Message{
		Version: agentcoord.MessageSchemaVersion, MessageID: "msg-envelope", SessionID: "session-envelope", RunID: "run-child",
		ParentRunID: "run-parent", TaskID: "task-envelope", TurnID: "turn-envelope", IdempotencyKey: "key-envelope",
		CorrelationID: "correlation-envelope", CausationID: "causation-envelope", From: "run-parent", To: "run-child", Kind: "message",
		SourceAttemptID: "attempt-parent", SourceLeaseGeneration: 7, ContentRef: "ev-envelope", ContentDigest: "digest-envelope",
		MediaType: "text/plain", ByteCount: 4, Preview: "body", AttemptID: "attempt-child-a", LeaseGeneration: 10,
	}
	want, err := mailboxEnvelopeDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	mutable := base
	mutable.AttemptID = "attempt-child-b"
	mutable.LeaseGeneration = 11
	if got, err := mailboxEnvelopeDigest(mutable); err != nil || got != want {
		t.Fatalf("delivery fence changed immutable digest: got=%q want=%q err=%v", got, want, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*agentcoord.Message)
	}{
		{name: "message id", mutate: func(message *agentcoord.Message) { message.MessageID = "msg-envelope-other" }},
		{name: "from", mutate: func(message *agentcoord.Message) { message.From = "run-other-parent" }},
		{name: "kind", mutate: func(message *agentcoord.Message) { message.Kind = "result" }},
		{name: "source attempt", mutate: func(message *agentcoord.Message) { message.SourceAttemptID = "attempt-parent-other" }},
		{name: "source generation", mutate: func(message *agentcoord.Message) { message.SourceLeaseGeneration++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			got, err := mailboxEnvelopeDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatalf("immutable %s drift did not change digest", test.name)
			}
		})
	}
}

func TestMailbox_NackReplacementAndExactRetryPreserveEnvelopeDigest(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-redelivery-digest", "run-redelivery-digest")
	first, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-redelivery-digest", RunID: "run-redelivery-digest", AttemptID: "attempt-redelivery-a", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := agentcoord.Message{
		SessionID: "session-redelivery-digest", RunID: "run-redelivery-digest", To: "run-redelivery-digest",
		IdempotencyKey: "key-redelivery-digest", ContentRef: "ev-redelivery-digest", ContentDigest: "digest-redelivery", ByteCount: 1,
	}
	persisted, created, err := store.EnqueueOperatorSteer(ctx, message)
	if err != nil || !created {
		t.Fatalf("initial enqueue=%+v created=%t err=%v", persisted, created, err)
	}
	initialRetry, created, err := store.EnqueueOperatorSteer(ctx, message)
	if err != nil || created || initialRetry.ID != persisted.ID || initialRetry.EnvelopeDigest != persisted.EnvelopeDigest {
		t.Fatalf("initial omitted-id retry=%+v created=%t err=%v", initialRetry, created, err)
	}
	messageID := persisted.ID
	owner := "worker-redelivery-a"
	claimed, err := store.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: message.SessionID, RunID: message.RunID, MessageID: messageID, Owner: owner,
		AttemptID: first.AttemptID, LeaseGeneration: first.LeaseGeneration, Limit: 1,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if err := store.Nack(ctx, agentcoord.MailboxNackRequest{MailboxAckRequest: agentcoord.MailboxAckRequest{
		SessionID: message.SessionID, RunID: message.RunID, MessageID: messageID, Owner: owner,
		AttemptID: first.AttemptID, LeaseGeneration: first.LeaseGeneration,
	}, Reason: "replacement"}); err != nil {
		t.Fatal(err)
	}
	nackedRetry, created, err := store.EnqueueOperatorSteer(ctx, message)
	if err != nil || created || nackedRetry.ID != persisted.ID || nackedRetry.EnvelopeDigest != persisted.EnvelopeDigest {
		t.Fatalf("post-Nack omitted-id retry=%+v created=%t err=%v", nackedRetry, created, err)
	}
	if err := store.Detach(ctx, agentcoord.AttachmentDetachRequest{
		SessionID: message.SessionID, RunID: message.RunID, AttemptID: first.AttemptID, LeaseGeneration: first.LeaseGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: message.SessionID, RunID: message.RunID, AttemptID: "attempt-redelivery-b", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, created, err := store.EnqueueOperatorSteer(ctx, message)
	if err != nil || created || retried.ID != persisted.ID || retried.EnvelopeDigest != persisted.EnvelopeDigest {
		t.Fatalf("exact retry=%+v created=%t err=%v", retried, created, err)
	}
	claimed, err = store.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: message.SessionID, RunID: message.RunID, MessageID: messageID, Owner: "worker-redelivery-b",
		AttemptID: second.AttemptID, LeaseGeneration: second.LeaseGeneration, Limit: 1,
	})
	if err != nil || len(claimed) != 1 || claimed[0].LeaseGeneration != second.LeaseGeneration {
		t.Fatalf("replacement claim=%+v err=%v", claimed, err)
	}
	if digest, err := mailboxEnvelopeDigest(claimed[0]); err != nil || digest != persisted.EnvelopeDigest {
		t.Fatalf("claimed digest=%q want=%q err=%v", digest, persisted.EnvelopeDigest, err)
	}
	if _, err := store.db.Exec(`UPDATE agent_mailbox SET envelope_digest = ? WHERE message_id = ?`, strings.Repeat("a", 64), messageID); err != nil {
		t.Fatal(err)
	}
	if err := refreshAgentMailboxEnvelopeDigests(store.db); err != nil {
		t.Fatal(err)
	}
	backfilled, err := store.GetMailboxMessage(ctx, message.SessionID, message.RunID, messageID)
	if err != nil || backfilled.EnvelopeDigest != persisted.EnvelopeDigest {
		t.Fatalf("backfilled envelope=%+v err=%v", backfilled, err)
	}
}

func TestMailbox_ConcurrentOmittedMessageIDDuplicatesConverge(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-omitted-id-concurrent", "run-omitted-id-concurrent")
	message := agentcoord.Message{
		SessionID: "session-omitted-id-concurrent", RunID: "run-omitted-id-concurrent", To: "run-omitted-id-concurrent",
		IdempotencyKey: "key-omitted-id-concurrent", ContentRef: "ev-omitted-id-concurrent",
		ContentDigest: "digest-omitted-id-concurrent", ByteCount: 1,
	}
	const callers = 32
	type enqueueResult struct {
		message agentcoord.Message
		created bool
		err     error
	}
	results := make(chan enqueueResult, callers)
	var wg sync.WaitGroup
	for index := 0; index < callers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			persisted, created, err := store.EnqueueOperatorSteer(ctx, message)
			results <- enqueueResult{message: persisted, created: created, err: err}
		}()
	}
	wg.Wait()
	close(results)
	wantID := deterministicMailboxMessageID(message.SessionID, message.RunID, message.IdempotencyKey)
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent omitted-id enqueue=%v", result.err)
		}
		if result.created {
			createdCount++
		}
		if result.message.ID != wantID {
			t.Fatalf("concurrent message id=%q, want %q", result.message.ID, wantID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count=%d, want 1", createdCount)
	}
	rows, err := store.List(ctx, agentcoord.MailboxQuery{SessionID: message.SessionID, RunID: message.RunID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("converged rows=%+v err=%v", rows, err)
	}
}

func TestMailbox_OmittedMessageIDIsScopedBySessionAndRun(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	scopes := []struct{ sessionID, runID string }{
		{sessionID: "session-scope-a", runID: "run-scope-a"},
		{sessionID: "session-scope-a", runID: "run-scope-b"},
		{sessionID: "session-scope-b", runID: "run-scope-c"},
	}
	ids := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		seedMailboxRun(t, store, scope.sessionID, scope.runID)
		persisted, created, err := store.EnqueueOperatorSteer(ctx, agentcoord.Message{
			SessionID: scope.sessionID, RunID: scope.runID, To: scope.runID,
			IdempotencyKey: "shared-scope-key", ContentRef: "ev-" + scope.runID,
			ContentDigest: "digest-" + scope.runID, ByteCount: 1,
		})
		if err != nil || !created {
			t.Fatalf("scope %+v enqueue=%+v created=%t err=%v", scope, persisted, created, err)
		}
		want := deterministicMailboxMessageID(scope.sessionID, scope.runID, "shared-scope-key")
		if persisted.ID != want {
			t.Fatalf("scope %+v id=%q, want %q", scope, persisted.ID, want)
		}
		if _, duplicate := ids[persisted.ID]; duplicate {
			t.Fatalf("scoped message id collided: %q", persisted.ID)
		}
		ids[persisted.ID] = struct{}{}
	}
}

func TestMailbox_ConcurrentEnqueueSequencesAreUniqueAndOrdered(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-concurrent", "run-concurrent")
	const count = 32
	sequences := make(chan int64, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			message, _, err := store.EnqueueOperatorSteer(ctx, agentcoord.Message{
				SessionID:      "session-concurrent",
				RunID:          "run-concurrent",
				To:             "run-concurrent",
				From:           "operator",
				Kind:           "steer",
				IdempotencyKey: fmt.Sprintf("key-%02d", index),
				ContentRef:     fmt.Sprintf("ev-%02d", index),
				ContentDigest:  fmt.Sprintf("digest-%02d", index),
				ByteCount:      int64(index + 1),
			})
			if err != nil {
				errs <- err
				return
			}
			sequences <- message.Sequence
		}(i)
	}
	wg.Wait()
	close(sequences)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent enqueue: %v", err)
	}
	got := make([]int64, 0, count)
	for sequence := range sequences {
		got = append(got, sequence)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != count {
		t.Fatalf("sequences=%v, want %d rows", got, count)
	}
	for index, sequence := range got {
		want := int64(index + 1)
		if sequence != want {
			t.Fatalf("sequence[%d]=%d, want %d", index, sequence, want)
		}
	}
}

func TestMailbox_ClaimExpiryRedeliveryAndFence(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-claims", "run-claims")
	firstLease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-claims", RunID: "run-claims", AttemptID: "attempt-1", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.EnqueueOperatorSteer(ctx, agentcoord.Message{
		SessionID:      "session-claims",
		RunID:          "run-claims",
		To:             "run-claims",
		From:           "operator",
		Kind:           "steer",
		IdempotencyKey: "key-1",
		ContentRef:     "ev-1",
		ContentDigest:  "digest-1",
		ByteCount:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: "session-claims", RunID: "run-claims", MessageID: message.ID,
		Owner: "worker-1", AttemptID: "attempt-1", LeaseGeneration: 1,
		LeaseDuration: time.Second,
	})
	if err != nil || len(claim) != 1 || claim[0].State != agentcoord.MessageClaimed {
		t.Fatalf("claim=%+v, err=%v", claim, err)
	}
	missingAttempt := agentcoord.MailboxAckRequest{
		SessionID: "session-claims", RunID: "run-claims", MessageID: message.ID,
		Owner: "worker-1", LeaseGeneration: 1,
	}
	if err := store.Ack(ctx, missingAttempt); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("omitted attempt ack=%v, want ErrMailboxInvalid", err)
	}
	wrongAttempt := missingAttempt
	wrongAttempt.AttemptID = "attempt-wrong"
	if err := store.Nack(ctx, agentcoord.MailboxNackRequest{MailboxAckRequest: wrongAttempt}); !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("wrong attempt nack=%v, want ErrAttachmentStale", err)
	}
	ack := agentcoord.MailboxAckRequest{
		SessionID: "session-claims", RunID: "run-claims", MessageID: message.ID,
		Owner: "worker-2", AttemptID: "attempt-1", LeaseGeneration: 1,
	}
	if err := store.Ack(ctx, ack); !errors.Is(err, ErrMailboxClaimOwner) {
		t.Fatalf("wrong owner ack=%v, want ErrMailboxClaimOwner", err)
	}
	if expired, err := store.Expire(ctx, "session-claims", "run-claims", time.Now().UTC().Add(2*time.Second)); err != nil || expired != 1 {
		t.Fatalf("expire=%d, err=%v", expired, err)
	}
	if err := store.Detach(ctx, agentcoord.AttachmentDetachRequest{
		SessionID: "session-claims", RunID: "run-claims", AttemptID: firstLease.AttemptID, LeaseGeneration: firstLease.LeaseGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	secondLease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-claims", RunID: "run-claims", AttemptID: "attempt-2", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err = store.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: "session-claims", RunID: "run-claims", MessageID: message.ID,
		Owner: "worker-2", AttemptID: secondLease.AttemptID, LeaseGeneration: secondLease.LeaseGeneration,
		LeaseDuration: time.Second,
	})
	if err != nil || len(claim) != 1 || claim[0].AttemptID != "attempt-2" {
		t.Fatalf("redelivery claim=%+v, err=%v", claim, err)
	}
	ack.Owner, ack.AttemptID, ack.LeaseGeneration = "worker-1", "attempt-1", 1
	if err := store.Ack(ctx, ack); !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("stale ack=%v, want ErrAttachmentStale", err)
	}
	ack.Owner, ack.AttemptID, ack.LeaseGeneration = "worker-2", secondLease.AttemptID, secondLease.LeaseGeneration
	if err := store.Ack(ctx, ack); err != nil {
		t.Fatalf("current ack=%v", err)
	}
	stored, err := store.GetMailboxMessage(ctx, "session-claims", "run-claims", message.ID)
	if err != nil || stored.State != agentcoord.MessageProcessed {
		t.Fatalf("stored=%+v, err=%v", stored, err)
	}
}

func TestMailbox_RejectsOrphanAndCrossSessionRows(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	message := agentcoord.Message{
		SessionID: "session-owner", RunID: "run-orphan", To: "run-orphan", Kind: "message",
		IdempotencyKey: "key", ContentRef: "ev", ContentDigest: "digest", ByteCount: 1,
	}
	if _, _, err := store.Enqueue(ctx, message); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("orphan enqueue=%v, want ErrMailboxInvalid", err)
	}
	seedMailboxRun(t, store, "session-owner", "run-owned-mailbox")
	message.RunID = "run-owned-mailbox"
	message.To = message.RunID
	message.SessionID = "session-foreign"
	if _, _, err := store.Enqueue(ctx, message); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("cross-session enqueue=%v, want ErrMailboxInvalid", err)
	}
	if _, err := store.List(ctx, agentcoord.MailboxQuery{SessionID: "session-foreign", RunID: "run-owned-mailbox"}); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("cross-session list=%v, want ErrMailboxInvalid", err)
	}
}

func TestMailbox_SourceRunRequiresCurrentExactAttachment(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-source", "run-parent-source")
	if _, err := store.StartRun(ctx, AgentRun{
		RunID: "run-child-source", SessionID: "session-source", ParentRunID: "run-parent-source", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-source", RunID: "run-child-source", AttemptID: "attempt-source-1", LeaseDuration: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForAttachmentExpiry(t, store, "session-source", "run-child-source")
	second, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-source", RunID: "run-child-source", AttemptID: "attempt-source-2", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := agentcoord.Message{
		SessionID: "session-source", RunID: "run-parent-source", To: "run-parent-source",
		From: "run-child-source", Kind: "result", IdempotencyKey: "source-result",
		ContentRef: "ev-source", ContentDigest: "digest-source", ByteCount: 1,
		SourceAttemptID: first.AttemptID, SourceLeaseGeneration: first.LeaseGeneration,
	}
	if _, _, err := store.Enqueue(ctx, message); !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("stale source enqueue=%v, want ErrAttachmentStale", err)
	}
	message.SourceAttemptID = ""
	message.SourceLeaseGeneration = 0
	if _, _, err := store.Enqueue(ctx, message); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("omitted source fence=%v, want ErrMailboxInvalid", err)
	}
	message.SourceAttemptID = second.AttemptID
	message.SourceLeaseGeneration = second.LeaseGeneration
	message.RunID = "run-child-source"
	message.To = message.RunID
	if _, _, err := store.Enqueue(ctx, message); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("non-parent target=%v, want ErrMailboxInvalid", err)
	}
	message.RunID = "run-parent-source"
	message.To = message.RunID
	if _, created, err := store.Enqueue(ctx, message); err != nil || !created {
		t.Fatalf("current source enqueue created=%t err=%v", created, err)
	}
	parentLease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-source", RunID: "run-parent-source", AttemptID: "attempt-parent-source", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	message = agentcoord.Message{
		SessionID: "session-source", RunID: "run-child-source", To: "run-child-source",
		From: "run-parent-source", Kind: "message", IdempotencyKey: "parent-message",
		ContentRef: "ev-parent-steer", ContentDigest: "digest-parent-steer", ByteCount: 1,
		AttemptID: second.AttemptID, LeaseGeneration: second.LeaseGeneration,
		SourceAttemptID: parentLease.AttemptID, SourceLeaseGeneration: parentLease.LeaseGeneration,
	}
	if _, created, err := store.Enqueue(ctx, message); err != nil || !created {
		t.Fatalf("parent-to-child enqueue created=%t err=%v", created, err)
	}
	invalidSource := message
	invalidSource.IdempotencyKey = "missing-source"
	invalidSource.From = ""
	invalidSource.SourceAttemptID = ""
	invalidSource.SourceLeaseGeneration = 0
	if _, _, err := store.Enqueue(ctx, invalidSource); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("missing source enqueue=%v, want ErrMailboxInvalid", err)
	}
	invalidSource.IdempotencyKey = "unknown-source"
	invalidSource.From = "run-unknown-source"
	invalidSource.SourceAttemptID = "attempt-unknown"
	invalidSource.SourceLeaseGeneration = 1
	if _, _, err := store.Enqueue(ctx, invalidSource); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("unknown source enqueue=%v, want ErrMailboxInvalid", err)
	}
	seedMailboxRun(t, store, "session-source", "run-unrelated-source")
	unrelatedLease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-source", RunID: "run-unrelated-source", AttemptID: "attempt-unrelated-source", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidSource.IdempotencyKey = "unrelated-source"
	invalidSource.From = "run-unrelated-source"
	invalidSource.SourceAttemptID = unrelatedLease.AttemptID
	invalidSource.SourceLeaseGeneration = unrelatedLease.LeaseGeneration
	if _, _, err := store.Enqueue(ctx, invalidSource); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("unrelated source enqueue=%v, want ErrMailboxInvalid", err)
	}
	seedMailboxRun(t, store, "session-other-source", "run-cross-source")
	crossLease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-other-source", RunID: "run-cross-source", AttemptID: "attempt-cross-source", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidSource.IdempotencyKey = "cross-source"
	invalidSource.From = "run-cross-source"
	invalidSource.SourceAttemptID = crossLease.AttemptID
	invalidSource.SourceLeaseGeneration = crossLease.LeaseGeneration
	if _, _, err := store.Enqueue(ctx, invalidSource); !errors.Is(err, ErrMailboxInvalid) {
		t.Fatalf("cross-session source enqueue=%v, want ErrMailboxInvalid", err)
	}
}

func TestMailbox_FencedDeliveryMutationsRequireCompleteIdentity(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-no-attachment", "run-no-attachment")
	unattached, _, err := store.EnqueueOperatorSteer(ctx, agentcoord.Message{
		SessionID: "session-no-attachment", RunID: "run-no-attachment", To: "run-no-attachment", From: "operator", Kind: "steer",
		IdempotencyKey: "no-attachment", ContentRef: "ev-no-attachment", ContentDigest: "digest-no-attachment", ByteCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: "session-no-attachment", RunID: "run-no-attachment", MessageID: unattached.ID, Owner: "worker-no-attachment",
		AttemptID: "attempt-invented", LeaseGeneration: 1, Limit: 1,
	}); !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("unattached Claim = %v, want ErrAttachmentStale", err)
	}
	seedMailboxRun(t, store, "session-required-fence", "run-required-fence")
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-required-fence", RunID: "run-required-fence", AttemptID: "attempt-required-fence", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.EnqueueOperatorSteer(ctx, agentcoord.Message{
		SessionID: "session-required-fence", RunID: "run-required-fence", To: "run-required-fence", From: "operator", Kind: "steer",
		IdempotencyKey: "required-fence", ContentRef: "ev-required-fence", ContentDigest: "digest-required-fence", ByteCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	validClaim := agentcoord.MailboxClaimRequest{
		SessionID: "session-required-fence", RunID: "run-required-fence", MessageID: message.ID, Owner: "worker-required-fence",
		AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration, Limit: 1,
	}
	claimCases := []struct {
		name   string
		mutate func(*agentcoord.MailboxClaimRequest)
	}{
		{name: "owner", mutate: func(request *agentcoord.MailboxClaimRequest) { request.Owner = "" }},
		{name: "attempt", mutate: func(request *agentcoord.MailboxClaimRequest) { request.AttemptID = "" }},
		{name: "generation", mutate: func(request *agentcoord.MailboxClaimRequest) { request.LeaseGeneration = 0 }},
	}
	for _, test := range claimCases {
		t.Run("claim "+test.name, func(t *testing.T) {
			request := validClaim
			test.mutate(&request)
			if _, err := store.Claim(ctx, request); !errors.Is(err, ErrMailboxInvalid) {
				t.Fatalf("Claim = %v, want ErrMailboxInvalid", err)
			}
		})
	}
	claimed, err := store.Claim(ctx, validClaim)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("valid Claim = %+v, %v", claimed, err)
	}
	validAck := agentcoord.MailboxAckRequest{
		SessionID: validClaim.SessionID, RunID: validClaim.RunID, MessageID: message.ID, Owner: validClaim.Owner,
		AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
	}
	ackCases := []struct {
		name   string
		mutate func(*agentcoord.MailboxAckRequest)
	}{
		{name: "owner", mutate: func(request *agentcoord.MailboxAckRequest) { request.Owner = "" }},
		{name: "attempt", mutate: func(request *agentcoord.MailboxAckRequest) { request.AttemptID = "" }},
		{name: "generation", mutate: func(request *agentcoord.MailboxAckRequest) { request.LeaseGeneration = 0 }},
	}
	for _, test := range ackCases {
		t.Run("ack "+test.name, func(t *testing.T) {
			request := validAck
			test.mutate(&request)
			if err := store.Ack(ctx, request); !errors.Is(err, ErrMailboxInvalid) {
				t.Fatalf("Ack = %v, want ErrMailboxInvalid", err)
			}
			if err := store.Nack(ctx, agentcoord.MailboxNackRequest{MailboxAckRequest: request}); !errors.Is(err, ErrMailboxInvalid) {
				t.Fatalf("Nack = %v, want ErrMailboxInvalid", err)
			}
		})
	}
	if err := store.Ack(ctx, validAck); err != nil {
		t.Fatalf("valid Ack = %v", err)
	}
}

func TestOperationalLeaseTimestamps_NormalizeLegacyFractionsBeforeComparison(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-legacy-time", "run-legacy-time")
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-legacy-time", RunID: "run-legacy-time", AttemptID: "attempt-legacy-time", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.EnqueueOperatorSteer(ctx, agentcoord.Message{
		SessionID: "session-legacy-time", RunID: "run-legacy-time", To: "run-legacy-time", From: "operator", Kind: "steer",
		IdempotencyKey: "legacy-time", ContentRef: "ev-legacy-time", ContentDigest: "digest-legacy-time", ByteCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: "session-legacy-time", RunID: "run-legacy-time", MessageID: message.ID, Owner: "worker-legacy-time",
		AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration, Limit: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_run_attempts SET lease_expires_at = '2099-01-01T00:00:00Z' WHERE attempt_id = ?`, lease.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_mailbox SET lease_expires_at = '2099-01-01T00:00:00.1Z' WHERE message_id = ?`, message.ID); err != nil {
		t.Fatal(err)
	}
	if err := normalizeOperationalLeaseTimestamps(store.db); err != nil {
		t.Fatal(err)
	}
	var attachmentRaw, mailboxRaw string
	if err := store.db.QueryRow(`SELECT CAST(lease_expires_at AS TEXT) FROM agent_run_attempts WHERE attempt_id = ?`, lease.AttemptID).Scan(&attachmentRaw); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT CAST(lease_expires_at AS TEXT) FROM agent_mailbox WHERE message_id = ?`, message.ID).Scan(&mailboxRaw); err != nil {
		t.Fatal(err)
	}
	if attachmentRaw != "2099-01-01T00:00:00.000000000Z" || mailboxRaw != "2099-01-01T00:00:00.100000000Z" {
		t.Fatalf("normalized leases attachment=%q mailbox=%q", attachmentRaw, mailboxRaw)
	}
	if current, err := store.Current(ctx, "session-legacy-time", "run-legacy-time"); err != nil || current.AttemptID != lease.AttemptID {
		t.Fatalf("materialized current lease = %+v, %v", current, err)
	}
	listed, err := store.List(ctx, agentcoord.MailboxQuery{SessionID: "session-legacy-time", RunID: "run-legacy-time"})
	if err != nil || len(listed) != 1 || listed[0].LeasedUntil.Nanosecond() != 100_000_000 {
		t.Fatalf("materialized mailbox lease = %+v, %v", listed, err)
	}
	if _, err := store.db.Exec(`UPDATE agent_run_attempts SET lease_expires_at = '2000-01-01T00:00:00Z' WHERE attempt_id = ?`, lease.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_mailbox SET lease_expires_at = '2000-01-01T00:00:00.1Z' WHERE message_id = ?`, message.ID); err != nil {
		t.Fatal(err)
	}
	if err := normalizeOperationalLeaseTimestamps(store.db); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Current(ctx, "session-legacy-time", "run-legacy-time"); !errors.Is(err, ErrAttachmentExpired) {
		t.Fatalf("legacy attachment expiry = %v, want ErrAttachmentExpired", err)
	}
	if expired, err := store.Expire(ctx, "session-legacy-time", "run-legacy-time", time.Now().UTC()); err != nil || expired != 1 {
		t.Fatalf("legacy mailbox expiry = %d, %v", expired, err)
	}
}

func newMailboxTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "runledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedMailboxRun(t *testing.T, store *SQLiteStore, sessionID, runID string) {
	t.Helper()
	if _, err := store.StartRun(context.Background(), AgentRun{
		RunID: runID, SessionID: sessionID, Status: "running",
	}); err != nil {
		t.Fatalf("seed run %s: %v", runID, err)
	}
}
