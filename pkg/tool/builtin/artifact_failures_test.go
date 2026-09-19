package builtin

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestRecoveryToolFailuresRetainLatestAndDetach(t *testing.T) {
	var absent *ArtifactSubmission
	absent.RecordToolFailure("read_file", "ignored")
	sink := &ArtifactSubmission{}
	for i := 0; i < 10; i++ {
		sink.RecordToolFailure("read_file", fmt.Sprintf("failure-%d", i))
	}
	got := sink.RecoveryArtifact()
	before := recoveryJSON(t, got)
	if got.Status != artifactv1.StatusIncomplete || len(got.Diagnostics) != 8 || len(got.Blocks) != 0 || len(got.EvidenceRefs) != 0 {
		t.Fatalf("unexpected recovery: %+v", got)
	}
	for i, d := range got.Diagnostics {
		if d.Code != "tool_execution_failed" || d.Level != "warning" || !strings.Contains(d.Message, fmt.Sprintf("failure-%d", i+2)) {
			t.Fatalf("order/type lost: %+v", d)
		}
	}
	if !strings.Contains(strings.Join(got.IncompleteReasons, " "), "2 earlier tool failure diagnostics omitted") {
		t.Fatal("missing omission notice")
	}
	got.Diagnostics[0].Message = "mutated"
	if !bytes.Equal(before, recoveryJSON(t, sink.RecoveryArtifact())) {
		t.Fatal("recovery aliases or mutates journal")
	}
	bad := artifactv1.New(artifactv1.KindAnalysis, "invalid", "Invalid", "Should not clear diagnostics")
	if err := sink.Submit(bad); err == nil {
		t.Fatal("invalid submission accepted")
	}
	if len(sink.RecoveryArtifact().Diagnostics) != 8 {
		t.Fatal("rejected submission cleared diagnostics")
	}
	valid := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Done", "Requested verification passed")
	if err := sink.Submit(valid); err != nil {
		t.Fatal(err)
	}
	sink.RecordToolFailure("read_file", "late failure")
	final, _ := sink.Artifact()
	if final.Status != artifactv1.StatusCompleted || len(final.Diagnostics) != 0 || len(sink.RecoveryArtifact().Diagnostics) != 0 || len(sink.toolFailures) != 0 || sink.omittedToolFailures != 0 {
		t.Fatal("stale failures polluted successful artifact or retained memory")
	}
}

func TestRecoveryToolFailuresScreenAndBoundMessages(t *testing.T) {
	for _, tc := range []struct{ name, message, secret string }{
		{"assignment", "failure password=DO_NOT_RETAIN", "DO_NOT_RETAIN"},
		{"embedded JSON", `failure: {"api_key":"DO_NOT_RETAIN"}`, "DO_NOT_RETAIN"},
		{"nested JSON", `{"failure":{"access_token":"DO_NOT_RETAIN"}}`, "DO_NOT_RETAIN"},
		{"bearer", "request failed Bearer DO_NOT_RETAIN", "DO_NOT_RETAIN"},
		{"URL", "cannot fetch https://user:DO_NOT_RETAIN@example.invalid", "DO_NOT_RETAIN"},
		{"known token", "failed ghp_" + strings.Repeat("a", 36), strings.Repeat("a", 36)},
		{"PEM", "-----BEGIN PRIVATE KEY-----\nDO_NOT_RETAIN\n-----END PRIVATE KEY-----", "DO_NOT_RETAIN"},
		{"oversized", strings.Repeat("x", 64*1024) + "DO_NOT_RETAIN", "DO_NOT_RETAIN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &ArtifactSubmission{}
			sink.RecordToolFailure("read_file", tc.message)
			body := recoveryJSON(t, sink.RecoveryArtifact())
			if strings.Contains(string(body), tc.secret) {
				t.Fatal("credential-like detail escaped screening")
			}
			if len(sink.toolFailures) != 1 || len(sink.toolFailures[0].Message) > 1024 {
				t.Fatal("missing or unbounded diagnostic")
			}
		})
	}
	for _, name := range []string{"password=DO_NOT_RETAIN", strings.Repeat("x", 257) + "DO_NOT_RETAIN"} {
		sink := &ArtifactSubmission{}
		sink.RecordToolFailure(name, "ordinary error")
		body := recoveryJSON(t, sink.RecoveryArtifact())
		if strings.Contains(string(body), "DO_NOT_RETAIN") || !strings.Contains(string(body), "ordinary error") {
			t.Fatal("name screening lost safe error or leaked detail")
		}
	}
	for _, message := range []string{"", " \t", strings.Repeat("界\x01", 16000), string([]byte{0xff, 0xfe}) + " error"} {
		sink := &ArtifactSubmission{}
		sink.RecordToolFailure("read_file", message)
		got := sink.RecoveryArtifact()
		recoveryJSON(t, got)
		if len(got.Diagnostics) != 1 || len(got.Diagnostics[0].Message) > 1024 || !utf8.ValidString(got.Diagnostics[0].Message) {
			t.Fatal("invalid bounded message")
		}
		if strings.TrimSpace(message) == "" && !strings.Contains(got.Diagnostics[0].Message, "without an error message") {
			t.Fatal("empty failure silently lost")
		}
	}
}

func TestRecoveryToolFailuresShareSourceAndJSONBudget(t *testing.T) {
	sink := &ArtifactSubmission{}
	const count = 10
	content := strings.Repeat("\x01", 10000)
	for i := 0; i < count; i++ {
		if _, err := sink.CaptureReadSource(sourceReadResult(fmt.Sprintf("/captured-%d", i), content, 1, 1)); err != nil {
			t.Fatal(err)
		}
		sink.RecordToolFailure("read_file", strings.Repeat("\x01", 16000))
	}
	got := sink.RecoveryArtifactWithReserve(artifactv1.MaxProviderBytes / 2)
	body := recoveryJSON(t, got)
	if len(body) > artifactv1.MaxProviderBytes/2 || len(got.Diagnostics) != 8 || len(got.Blocks) != 1 || len(got.Blocks[0].Table.Rows) == 0 {
		t.Fatalf("lost budget or usable evidence: %d bytes, %+v", len(body), got)
	}
	for _, r := range got.Blocks[0].Table.Rows {
		if r[4] != content {
			t.Fatal("source bytes truncated")
		}
	}
	if len(sink.sources) != count || got.Status != artifactv1.StatusIncomplete {
		t.Fatal("recovery consumed evidence or upgraded status")
	}
}

func TestRecoveryToolFailuresConcurrentSnapshots(t *testing.T) {
	sink := &ArtifactSubmission{}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sink.RecordToolFailure("read_file", fmt.Sprintf("failure-%d", i))
			_ = sink.RecoveryArtifact()
		}(i)
	}
	wg.Wait()
	got := sink.RecoveryArtifact()
	recoveryJSON(t, got)
	if len(got.Diagnostics) != 8 || sink.omittedToolFailures != 56 {
		t.Fatalf("concurrent journal bounds lost: %d/%d", len(got.Diagnostics), sink.omittedToolFailures)
	}
}
