package runledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/launchadmission"
	"m31labs.dev/buckley/pkg/launchcontract"
)

func TestLaunchAdmissionStore_ExactReplayConflictReopenAndActiveRead(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "launch.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ensureLaunchTestRun(t, store, "session-launch", "run-launch")
	envelope := launchTestEnvelope(t, "session-launch", "run-launch", "gsxmail", launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), 5*time.Minute), "")

	created, inserted, err := ensureLaunchTestEnvelope(context.Background(), store, envelope)
	if err != nil || !inserted || created.CreatedAt().IsZero() || !launchcontract.CanonicalTime(created.CreatedAt()) {
		t.Fatalf("first admission = %+v, %v, %v", created.Snapshot(), inserted, err)
	}
	replayed, inserted, err := ensureLaunchTestEnvelope(context.Background(), store, envelope)
	if err != nil || inserted || !reflect.DeepEqual(replayed.Snapshot(), created.Snapshot()) || replayed.CreatedAt() != created.CreatedAt() {
		t.Fatalf("replay = %+v, %v, %v", replayed.Snapshot(), inserted, err)
	}
	loaded, err := store.GetLaunchEnvelope(context.Background(), envelope.SessionID, envelope.RunID)
	if err != nil || loaded.Digest() != created.Digest() {
		t.Fatalf("active read = %+v, %v", loaded.Snapshot(), err)
	}

	conflicting := launchTestEnvelope(t, envelope.SessionID, envelope.RunID, envelope.Profile.ID, envelope.PriceEvidence, strings.Repeat("0", 64))
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, conflicting); !errors.Is(err, ErrLaunchEnvelopeConflict) {
		t.Fatalf("conflicting admission error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err = reopened.GetLaunchEnvelope(context.Background(), envelope.SessionID, envelope.RunID)
	if err != nil || loaded.Digest() != created.Digest() {
		t.Fatalf("reopened read = %+v, %v", loaded.Snapshot(), err)
	}
}

func TestLaunchAdmissionStore_RequiresOpaqueSealContractOwnershipAndDBTime(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "launch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	envelope := launchTestEnvelope(t, "session-launch", "run-launch", "gosx", launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), 5*time.Minute), "")
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uncontracted admission error = %v", err)
	}
	if _, _, err := store.EnsureLaunchAdmission(context.Background(), launchadmission.SealedEnvelope{}); !errors.Is(err, launchadmission.ErrInvalid) {
		t.Fatalf("zero opaque seal error = %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM launch_envelopes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid admission rows=%d err=%v", count, err)
	}
	ensureLaunchTestRun(t, store, envelope.SessionID, envelope.RunID)
	record, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if record.CreatedAt().Before(record.Snapshot().PriceEvidence.ObservedAt) {
		t.Fatalf("database time predates price observation: %v", record.CreatedAt())
	}
	if _, err := store.db.Exec(`UPDATE launch_envelopes SET profile_id='tqwebp' WHERE run_id=?`, envelope.RunID); err == nil {
		t.Fatal("immutable envelope update succeeded")
	}
	if _, err := store.db.Exec(`DELETE FROM launch_envelopes WHERE run_id=?`, envelope.RunID); err == nil {
		t.Fatal("immutable envelope delete succeeded")
	}
	if _, err := store.db.Exec(`DELETE FROM agent_run_contracts WHERE run_id=? AND session_id=?`, envelope.RunID, envelope.SessionID); err == nil {
		t.Fatal("immutable run contract delete succeeded")
	}
	if _, err := store.db.Exec(`DELETE FROM agent_runs WHERE run_id=? AND session_id=?`, envelope.RunID, envelope.SessionID); err != nil {
		t.Fatalf("intentional run cascade failed: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM launch_envelopes WHERE run_id=?`, envelope.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade rows=%d err=%v", count, err)
	}
}

func TestLaunchAdmissionStore_ExpiredPriceIsHistoricalOnly(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "launch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ensureLaunchTestRun(t, store, "session-expired", "run-expired")
	observed := time.Now().UTC().Round(0).Add(-2 * time.Minute)
	envelope := launchTestEnvelope(t, "session-expired", "run-expired", "gsxmail", launchTestPrice(t, observed, time.Minute), "")
	record, err := launchRecordAt(envelope, observed)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := record.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO launch_envelopes (
			run_id, session_id, schema_version, profile_id, profile_version,
			profile_digest, envelope_digest, envelope_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, envelope.RunID, envelope.SessionID, envelope.Schema, envelope.Profile.ID, envelope.Profile.Schema,
		envelope.ProfileDigest, envelope.EnvelopeDigest, string(encoded), sqliteTimestamp(record.CreatedAt())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetLaunchEnvelope(context.Background(), envelope.SessionID, envelope.RunID); !errors.Is(err, ErrLaunchEnvelopeInvalid) {
		t.Fatalf("expired active read error = %v", err)
	}
	historical, err := store.GetHistoricalLaunchEnvelope(context.Background(), envelope.SessionID, envelope.RunID)
	if err != nil || historical.Digest() != record.Digest() {
		t.Fatalf("historical read = %+v, %v", historical.Snapshot(), err)
	}
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); !errors.Is(err, ErrLaunchEnvelopeInvalid) {
		t.Fatalf("expired replay error = %v", err)
	}
}

func TestLaunchAdmissionStore_ConcurrentExactAndConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "conflict"}[conflict], func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "launch.db")
			seed, err := New(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			ensureLaunchTestRun(t, seed, "session-launch", "run-launch")
			_ = seed.Close()
			left, _ := New(dbPath)
			right, _ := New(dbPath)
			defer left.Close()
			defer right.Close()
			price := launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), 5*time.Minute)
			leftEnvelope := launchTestEnvelope(t, "session-launch", "run-launch", "gsxmail", price, "")
			contextDigest := ""
			if conflict {
				contextDigest = strings.Repeat("0", 64)
			}
			rightEnvelope := launchTestEnvelope(t, "session-launch", "run-launch", "gsxmail", price, contextDigest)
			type outcome struct {
				inserted bool
				err      error
			}
			outcomes := make(chan outcome, 2)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for index, item := range []struct {
				store    *SQLiteStore
				envelope launchadmission.Envelope
			}{{left, leftEnvelope}, {right, rightEnvelope}} {
				_ = index
				wg.Add(1)
				go func(item struct {
					store    *SQLiteStore
					envelope launchadmission.Envelope
				}) {
					defer wg.Done()
					<-start
					_, inserted, err := ensureLaunchTestEnvelope(context.Background(), item.store, item.envelope)
					outcomes <- outcome{inserted: inserted, err: err}
				}(item)
			}
			close(start)
			wg.Wait()
			close(outcomes)
			created, succeeded, conflicted := 0, 0, 0
			for result := range outcomes {
				if result.inserted {
					created++
				}
				if result.err == nil {
					succeeded++
				} else if errors.Is(result.err, ErrLaunchEnvelopeConflict) {
					conflicted++
				} else {
					t.Fatal(result.err)
				}
			}
			if created != 1 || !conflict && succeeded != 2 || conflict && (succeeded != 1 || conflicted != 1) {
				t.Fatalf("created=%d succeeded=%d conflicted=%d", created, succeeded, conflicted)
			}
		})
	}
}

func TestLaunchAdmissionStore_RejectsForeignAndCorruptProjection(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "launch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ensureLaunchTestRun(t, store, "session-owner", "run-launch")
	foreign := launchTestEnvelope(t, "session-other", "run-launch", "gsxmail", launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), 5*time.Minute), "")
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, foreign); !errors.Is(err, ErrLaunchEnvelopeConflict) {
		t.Fatalf("foreign contract error = %v", err)
	}
	envelope := launchTestEnvelope(t, "session-owner", "run-launch", "gsxmail", foreign.PriceEvidence, "")
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER trg_launch_envelopes_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE launch_envelopes SET profile_id='gosx' WHERE run_id=?`, envelope.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetHistoricalLaunchEnvelope(context.Background(), envelope.SessionID, envelope.RunID); !errors.Is(err, ErrLaunchEnvelopeInvalid) {
		t.Fatalf("corrupt projection error = %v", err)
	}
}

func ensureLaunchTestEnvelope(ctx context.Context, store *SQLiteStore, envelope launchadmission.Envelope) (launchadmission.Record, bool, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return launchadmission.Record{}, false, err
	}
	var record launchadmission.Record
	inserted := false
	err = retryMailboxBusy(ctx, func() error {
		var writeErr error
		record, inserted, writeErr = store.ensureLaunchEnvelopeProjectionOnce(ctx, envelope, encoded)
		return writeErr
	})
	return record, inserted, err
}

func launchTestEnvelope(t *testing.T, sessionID, runID, profileID string, price launchcontract.FreePriceEvidence, contextDigest string) launchadmission.Envelope {
	t.Helper()
	profile, err := launchcontract.ResolveProfile(profileID)
	if err != nil {
		t.Fatal(err)
	}
	profileDigest, err := profile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root := "/work/" + profileID
	if contextDigest == "" {
		contextDigest = strings.Repeat("a", 64)
	}
	envelope := launchadmission.Envelope{
		Schema: launchadmission.EnvelopeSchema, RunID: runID, SessionID: sessionID,
		Profile: profile, ProfileDigest: profileDigest,
		Workspace: launchadmission.WorkspaceObservation{
			Schema: launchcontract.WorkspaceEvidenceSchema, CanonicalRoot: root,
			RootSHA256: strings.Repeat("1", 64), HEAD: strings.Repeat("2", 40),
			ManifestSHA256: strings.Repeat("3", 64), PreflightSHA256: strings.Repeat("4", 64),
			LicenseID: "MIT", LicensePath: "LICENSE", LicenseSHA256: strings.Repeat("5", 64), LicenseManifestSHA256: strings.Repeat("6", 64),
		},
		PriceEvidence: price,
		Route: launchadmission.RouteObservation{
			ProviderID: profile.Provider, ModelID: profile.Model,
			CatalogSourceID: profile.PriceGuard.CatalogSourceID, CatalogObservationDigest: price.Receipt.Digest,
			ProviderPostAttempts: profile.ProviderPostAttempts, ManagerAffordabilityAttempts: profile.ManagerAffordabilityAttempts,
			DurableRetryOwner: profile.RetryOwner,
		},
		Image: launchadmission.ImageObservation{
			Schema: launchadmission.WorkerEvidenceSchema, Contract: launchadmission.WorkerContract,
			Reference: "127.0.0.1:5000/buckley/worker@sha256:" + strings.Repeat("7", 64),
			ImageID:   "sha256:" + strings.Repeat("8", 64), ManifestDigest: "sha256:" + strings.Repeat("7", 64), ConfigDigest: "sha256:" + strings.Repeat("8", 64),
			SBOMSHA256: strings.Repeat("9", 64), ProvenanceSHA256: strings.Repeat("e", 64), OS: launchadmission.WorkerOS, Architecture: launchadmission.WorkerArchitecture,
			ContextSHA256: contextDigest, ModuleLockSHA256: strings.Repeat("b", 64), ToolchainLockSHA256: strings.Repeat("c", 64), GoVersion: launchadmission.WorkerGoVersion, TinyGoVersion: launchadmission.WorkerTinyGoVersion,
		},
	}
	envelope.EnvelopeDigest = launchTestDigest(t, envelope)
	return envelope
}

func ensureLaunchTestRun(t *testing.T, store *SQLiteStore, sessionID, runID string) {
	t.Helper()
	run := AgentRun{RunID: runID, SessionID: sessionID, Status: "queued", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if _, _, err := store.EnsureRunContract(context.Background(), run, strings.Repeat("f", 64), "evidence-"+runID); err != nil {
		t.Fatal(err)
	}
}

func launchTestPrice(t *testing.T, observedAt time.Time, ttl time.Duration) launchcontract.FreePriceEvidence {
	t.Helper()
	prices, err := launchcontract.NormalizeZeroPrices(map[string]string{
		"prompt": "0", "completion": "0", "request": "0", "image": "0", "web_search": "0", "internal_reasoning": "0", "input_cache_read": "0", "input_cache_write": "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := launchcontract.CatalogReceipt{Schema: launchcontract.CatalogReceiptSchema, SourceID: launchcontract.OpenRouterCatalogSourceID, SourceURL: launchcontract.OpenRouterCatalogSourceURL, ObservedAt: observedAt, ResponseDigest: strings.Repeat("d", 64), ModelObjectDigest: strings.Repeat("e", 64)}
	receipt.Digest = launchTestDigest(t, receipt)
	evidence := launchcontract.FreePriceEvidence{Schema: launchcontract.FreePriceEvidenceSchema, ProviderID: launchcontract.ProviderOpenRouter, SourceID: launchcontract.OpenRouterCatalogSourceID, SourceURL: launchcontract.OpenRouterCatalogSourceURL, CanonicalSlug: launchcontract.ModelOxAlpha, ObservedAt: observedAt, ExpiresAt: observedAt.Add(ttl), Prices: prices, Receipt: receipt}
	evidence.Digest = launchTestDigest(t, evidence)
	if err := evidence.ValidateAt(observedAt); err != nil {
		t.Fatal(err)
	}
	return evidence
}

func launchTestDigest(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
