package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/modelprofile"
)

func TestBehaviorProfileStore_PersistsImmutableVersions(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "profiles.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	profiles := NewBehaviorProfileStore(store)
	first := storageTestBehaviorProfile("v1", time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC))
	if err := profiles.Put(context.Background(), first); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := profiles.Put(context.Background(), first); err != nil {
		t.Fatalf("idempotent Put: %v", err)
	}
	conflict := first
	conflict.Metrics.EditFidelity = 0.5
	if err := profiles.Put(context.Background(), conflict); err == nil {
		t.Fatal("expected conflicting same-version profile to fail")
	}
	second := storageTestBehaviorProfile("v2", first.MeasuredAt.Add(time.Hour))
	if err := profiles.Put(context.Background(), second); err != nil {
		t.Fatalf("Put second: %v", err)
	}
	loaded, ok, err := profiles.Get(context.Background(), first.ModelID, "v1")
	if err != nil || !ok || loaded.ModelID != first.ModelID || loaded.Version != "v1" {
		t.Fatalf("Get = %+v, %v, %v", loaded, ok, err)
	}
	latest, ok, err := profiles.Latest(context.Background(), first.ModelID)
	if err != nil || !ok || latest.Version != "v2" {
		t.Fatalf("Latest = %+v, %v, %v", latest, ok, err)
	}
}

func TestBehaviorProfileStore_PromotionPointerIsExplicitAndRollbackable(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "profiles.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	profiles := NewBehaviorProfileStore(store)
	first := storageTestBehaviorProfile("v1", time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC))
	second := storageTestBehaviorProfile("v2", first.MeasuredAt.Add(time.Hour))
	if err := profiles.Put(context.Background(), first); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := profiles.Put(context.Background(), second); err != nil {
		t.Fatalf("Put second: %v", err)
	}

	if promoted, ok, err := profiles.Promoted(context.Background(), first.ModelID); err != nil || ok {
		t.Fatalf("Promoted before explicit promotion = %+v, %v, %v", promoted, ok, err)
	}
	latest, ok, err := profiles.Latest(context.Background(), first.ModelID)
	if err != nil || !ok || latest.Version != "v2" {
		t.Fatalf("Latest = %+v, %v, %v", latest, ok, err)
	}

	if err := profiles.Promote(context.Background(), first.ModelID, "v1"); err != nil {
		t.Fatalf("Promote v1: %v", err)
	}
	promoted, ok, err := profiles.Promoted(context.Background(), first.ModelID)
	if err != nil || !ok || promoted.Version != "v1" {
		t.Fatalf("Promoted v1 = %+v, %v, %v", promoted, ok, err)
	}
	if err := profiles.Promote(context.Background(), first.ModelID, "v1"); err != nil {
		t.Fatalf("idempotent Promote v1: %v", err)
	}
	promoted, ok, err = profiles.Promoted(context.Background(), first.ModelID)
	if err != nil || !ok || promoted.Version != "v1" {
		t.Fatalf("idempotent Promoted v1 = %+v, %v, %v", promoted, ok, err)
	}

	if err := profiles.Promote(context.Background(), first.ModelID, "v2"); err != nil {
		t.Fatalf("Promote v2: %v", err)
	}
	promoted, ok, err = profiles.Promoted(context.Background(), first.ModelID)
	if err != nil || !ok || promoted.Version != "v2" {
		t.Fatalf("Promoted v2 = %+v, %v, %v", promoted, ok, err)
	}

	if err := profiles.Promote(context.Background(), first.ModelID, "v1"); err != nil {
		t.Fatalf("rollback promote v1: %v", err)
	}
	promoted, ok, err = profiles.Promoted(context.Background(), first.ModelID)
	if err != nil || !ok || promoted.Version != "v1" {
		t.Fatalf("rollback Promoted v1 = %+v, %v, %v", promoted, ok, err)
	}
}

func TestBehaviorProfileStore_PromoteMissingCandidatePreservesExistingPointer(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "profiles.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	profiles := NewBehaviorProfileStore(store)
	first := storageTestBehaviorProfile("v1", time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC))
	if err := profiles.Put(context.Background(), first); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := profiles.Promote(context.Background(), first.ModelID, "v1"); err != nil {
		t.Fatalf("Promote v1: %v", err)
	}
	if err := profiles.Promote(context.Background(), first.ModelID, "missing"); err == nil {
		t.Fatal("expected missing candidate promotion to fail")
	}
	promoted, ok, err := profiles.Promoted(context.Background(), first.ModelID)
	if err != nil || !ok || promoted.Version != "v1" {
		t.Fatalf("Promoted after failed promote = %+v, %v, %v", promoted, ok, err)
	}
}

func TestBehaviorProfileStore_PromotedDetectsDanglingPointer(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "profiles.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO model_behavior_profile_promotions (model_id, profile_version) VALUES (?, ?)`, "example/model", "missing"); err == nil {
		t.Fatal("expected foreign key to reject dangling promoted profile pointer")
	}

	ctx := context.Background()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		_ = conn.Close()
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO model_behavior_profile_promotions (model_id, profile_version) VALUES (?, ?)`, "example/model", "missing"); err != nil {
		_ = conn.Close()
		t.Fatalf("insert dangling pointer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = conn.Close()
		t.Fatalf("restore foreign keys: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close fixture connection: %v", err)
	}

	if _, ok, err := NewBehaviorProfileStore(store).Promoted(ctx, "example/model"); err == nil || ok {
		t.Fatalf("Promoted dangling pointer ok=%v err=%v, want defensive error", ok, err)
	}
}

func storageTestBehaviorProfile(version string, measuredAt time.Time) modelprofile.Profile {
	return modelprofile.Profile{
		SchemaVersion: modelprofile.SchemaVersion,
		ModelID:       "example/model",
		Version:       version,
		Class:         modelprofile.ClassBalanced,
		SampleSize:    20,
		Confidence:    0.9,
		MeasuredAt:    measuredAt,
		Capabilities:  modelprofile.Capabilities{ToolCalls: true},
		Metrics: modelprofile.Metrics{
			ToolReliability:             0.9,
			ArgumentRepairReliability:   0.9,
			StructuredOutputReliability: 0.9,
			ParallelCallReliability:     0.9,
			EditFidelity:                0.9,
			VerificationPassRate:        0.9,
			ContinuationReliability:     0.9,
		},
	}
}
