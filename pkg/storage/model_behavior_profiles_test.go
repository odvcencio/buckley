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
