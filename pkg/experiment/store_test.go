package experiment

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/transparency"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}

	// Create tables
	schema := `
	CREATE TABLE IF NOT EXISTS experiments (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		hypothesis TEXT,
		task_prompt TEXT NOT NULL,
		task_context TEXT,
		task_working_dir TEXT,
		task_timeout_ms INTEGER,
		status TEXT NOT NULL DEFAULT 'pending',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		completed_at TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS experiment_variants (
		id TEXT PRIMARY KEY,
		experiment_id TEXT NOT NULL REFERENCES experiments(id),
		name TEXT NOT NULL,
		model_id TEXT NOT NULL,
		provider_id TEXT,
		system_prompt TEXT,
		temperature REAL,
		max_tokens INTEGER,
		tools_allowed TEXT,
		custom_config TEXT,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS experiment_criteria (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		experiment_id TEXT NOT NULL REFERENCES experiments(id),
		name TEXT NOT NULL,
		criterion_type TEXT NOT NULL,
		target TEXT NOT NULL,
		weight REAL NOT NULL DEFAULT 1
	);

	CREATE TABLE IF NOT EXISTS experiment_runs (
		id TEXT PRIMARY KEY,
		experiment_id TEXT NOT NULL REFERENCES experiments(id),
		variant_id TEXT NOT NULL REFERENCES experiment_variants(id),
		session_id TEXT,
		branch TEXT,
		status TEXT NOT NULL DEFAULT 'pending',
		output TEXT,
		files_changed TEXT,
		error TEXT,
		duration_ms INTEGER,
		prompt_tokens INTEGER,
		completion_tokens INTEGER,
		total_cost REAL,
		usage_json TEXT,
		cost_unknown INTEGER NOT NULL DEFAULT 0,
		tool_calls INTEGER,
		tool_successes INTEGER,
		tool_failures INTEGER,
		files_modified INTEGER,
		lines_changed INTEGER,
		started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		completed_at TIMESTAMP,
		input_manifest_json TEXT,
		model_executions_json TEXT
	);

	CREATE TABLE IF NOT EXISTS experiment_evaluations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id TEXT NOT NULL REFERENCES experiment_runs(id),
		criterion_id INTEGER NOT NULL REFERENCES experiment_criteria(id),
		passed INTEGER NOT NULL DEFAULT 0,
		score REAL NOT NULL DEFAULT 0,
		details TEXT,
		evaluated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	return db
}

func TestNewStore(t *testing.T) {
	tests := []struct {
		name string
		db   *sql.DB
		want bool
	}{
		{"nil db returns nil", nil, false},
		{"valid db returns store", setupTestDB(t), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore(tt.db)
			if (store != nil) != tt.want {
				t.Errorf("NewStore() = %v, want non-nil: %v", store, tt.want)
			}
		})
	}
}

func TestStore_CreateExperiment(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	tests := []struct {
		name    string
		exp     *Experiment
		wantErr bool
	}{
		{
			name:    "nil experiment returns error",
			exp:     nil,
			wantErr: true,
		},
		{
			name: "valid experiment with no ID generates ID",
			exp: &Experiment{
				Name: "test-experiment",
				Task: Task{Prompt: "test prompt"},
			},
			wantErr: false,
		},
		{
			name: "experiment with variants",
			exp: &Experiment{
				Name: "test-with-variants",
				Task: Task{Prompt: "test prompt"},
				Variants: []Variant{
					{Name: "variant-1", ModelID: "gpt-4"},
					{Name: "variant-2", ModelID: "claude-3"},
				},
			},
			wantErr: false,
		},
		{
			name: "experiment with criteria",
			exp: &Experiment{
				Name: "test-with-criteria",
				Task: Task{Prompt: "test prompt"},
				Criteria: []SuccessCriterion{
					{Name: "test passes", Type: CriterionTestPass, Target: "go test ./..."},
					{Name: "file exists", Type: CriterionFileExists, Target: "output.txt"},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.CreateExperiment(tt.exp)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateExperiment() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.exp != nil {
				if tt.exp.ID == "" {
					t.Error("CreateExperiment() did not generate ID")
				}
				if tt.exp.Status == "" {
					t.Error("CreateExperiment() did not set status")
				}
			}
		})
	}
}

func TestStore_CreateExperimentRollsBackAfterVariantMarshalError(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	invalid := &Experiment{
		ID:   "exp-create-rollback",
		Name: "rollback",
		Task: Task{Prompt: "prompt"},
		Variants: []Variant{{
			ID:           "variant-bad",
			Name:         "bad",
			ModelID:      "model-a",
			CustomConfig: map[string]any{"bad": func() {}},
		}},
	}
	if err := store.CreateExperiment(invalid); err == nil {
		t.Fatalf("CreateExperiment invalid custom config error = nil, want error")
	}
	var experimentRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM experiments WHERE id = ?`, invalid.ID).Scan(&experimentRows); err != nil {
		t.Fatalf("count experiments: %v", err)
	}
	if experimentRows != 0 {
		t.Fatalf("experiment rows after rollback = %d, want 0", experimentRows)
	}

	valid := &Experiment{
		ID:       "exp-create-valid-after-rollback",
		Name:     "valid",
		Task:     Task{Prompt: "prompt"},
		Variants: []Variant{{ID: "variant-ok", Name: "ok", ModelID: "model-a"}},
	}
	if err := store.CreateExperiment(valid); err != nil {
		t.Fatalf("valid CreateExperiment after rollback: %v", err)
	}
	loaded, err := store.GetExperiment(valid.ID)
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	if loaded == nil || loaded.ID != valid.ID || len(loaded.Variants) != 1 {
		t.Fatalf("loaded valid experiment = %#v", loaded)
	}
}

func TestStore_GetExperiment(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Create test experiment
	exp := &Experiment{
		ID:          "test-exp-1",
		Name:        "test-experiment",
		Description: "test description",
		Task:        Task{Prompt: "test prompt"},
		Variants: []Variant{
			{ID: "var-1", Name: "variant-1", ModelID: "gpt-4"},
		},
		Criteria: []SuccessCriterion{
			{Name: "test", Type: CriterionTestPass, Target: "go test"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	tests := []struct {
		name    string
		id      string
		wantNil bool
		wantErr bool
	}{
		{"empty id returns error", "", true, true},
		{"non-existent id returns nil", "non-existent", true, false},
		{"existing id returns experiment", "test-exp-1", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.GetExperiment(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetExperiment() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if (got == nil) != tt.wantNil {
				t.Errorf("GetExperiment() = %v, wantNil %v", got, tt.wantNil)
			}
			if got != nil {
				if got.ID != tt.id {
					t.Errorf("GetExperiment() ID = %v, want %v", got.ID, tt.id)
				}
				if len(got.Variants) == 0 {
					t.Error("GetExperiment() did not load variants")
				}
				if len(got.Criteria) == 0 {
					t.Error("GetExperiment() did not load criteria")
				}
			}
		})
	}
}

func TestStore_UpdateExperimentStatus(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Create test experiment
	exp := &Experiment{
		ID:   "test-exp-status",
		Name: "test",
		Task: Task{Prompt: "test"},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	tests := []struct {
		name      string
		id        string
		status    ExperimentStatus
		completed *time.Time
		wantErr   bool
	}{
		{"empty id returns error", "", ExperimentRunning, nil, true},
		{"empty status returns error", "test-exp-status", "", nil, true},
		{"valid status update", "test-exp-status", ExperimentRunning, nil, false},
		{"completed status sets time", "test-exp-status", ExperimentCompleted, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.UpdateExperimentStatus(tt.id, tt.status, tt.completed)
			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateExperimentStatus() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStore_ListExperiments(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Create test experiments
	for i := 0; i < 5; i++ {
		exp := &Experiment{
			Name: "test-exp",
			Task: Task{Prompt: "test"},
		}
		if i%2 == 0 {
			exp.Status = ExperimentCompleted
		}
		if err := store.CreateExperiment(exp); err != nil {
			t.Fatalf("failed to create experiment: %v", err)
		}
	}

	tests := []struct {
		name      string
		limit     int
		status    ExperimentStatus
		wantCount int
	}{
		{"no filter returns all", 0, "", 5},
		{"limit 3", 3, "", 3},
		{"filter completed", 0, ExperimentCompleted, 3},
		{"filter pending", 0, ExperimentPending, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.ListExperiments(tt.limit, tt.status)
			if err != nil {
				t.Errorf("ListExperiments() error = %v", err)
				return
			}
			if len(got) != tt.wantCount {
				t.Errorf("ListExperiments() count = %v, want %v", len(got), tt.wantCount)
			}
		})
	}
}

func TestStore_SaveRun(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Create experiment first
	exp := &Experiment{
		ID:   "exp-for-run",
		Name: "test",
		Task: Task{Prompt: "test"},
		Variants: []Variant{
			{ID: "var-1", Name: "v1", ModelID: "gpt-4"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	tests := []struct {
		name    string
		run     *Run
		wantErr bool
	}{
		{"nil run returns error", nil, true},
		{
			"valid run",
			&Run{
				ExperimentID: "exp-for-run",
				VariantID:    "var-1",
				Branch:       "test-branch",
				Status:       RunRunning,
			},
			false,
		},
		{
			"run with metrics",
			&Run{
				ExperimentID: "exp-for-run",
				VariantID:    "var-1",
				Status:       RunCompleted,
				Output:       "test output",
				Metrics: RunMetrics{
					DurationMs:       1000,
					PromptTokens:     100,
					CompletionTokens: 200,
					TotalCost:        0.01,
				},
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.SaveRun(tt.run)
			if (err != nil) != tt.wantErr {
				t.Errorf("SaveRun() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.run != nil && tt.run.ID == "" {
				t.Error("SaveRun() did not generate ID")
			}
		})
	}
}

func TestStore_SaveRunInputManifestRoundTripAndCompletionPreserves(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:   "exp-manifest",
		Name: "manifest",
		Task: Task{Prompt: "test prompt", Context: map[string]string{"b": "2", "a": "1"}},
		Variants: []Variant{
			{ID: "var-1", Name: "v1", ModelID: "gpt-4", ProviderID: "openrouter"},
		},
		Criteria: []SuccessCriterion{{Name: "tests", Type: CriterionTestPass, Target: "go test ./...", Weight: 1}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	manifest, err := buildRunInputManifest(exp, exp.Variants[0], time.Minute)
	if err != nil {
		t.Fatalf("buildRunInputManifest: %v", err)
	}
	start := time.Now()
	if err := store.SaveRun(&Run{
		ID:            "run-manifest",
		ExperimentID:  exp.ID,
		VariantID:     "var-1",
		Branch:        "experiment/exp-manifest/var-1",
		Status:        RunRunning,
		StartedAt:     start,
		InputManifest: manifest,
	}); err != nil {
		t.Fatalf("SaveRun initial: %v", err)
	}
	completed := time.Now()
	if err := store.SaveRun(&Run{
		ID:           "run-manifest",
		ExperimentID: exp.ID,
		VariantID:    "var-1",
		Branch:       "experiment/exp-manifest/var-1",
		Status:       RunCompleted,
		Output:       "done",
		StartedAt:    start,
		CompletedAt:  &completed,
	}); err != nil {
		t.Fatalf("SaveRun completion: %v", err)
	}
	got, err := store.GetRun("run-manifest")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.InputManifest == nil {
		t.Fatalf("stored manifest = %#v, want preserved manifest", got)
	}
	if got.InputManifest.InputDigest != manifest.InputDigest || got.InputManifest.WorkloadDigest != manifest.WorkloadDigest {
		t.Fatalf("manifest digest changed: got %#v want %#v", got.InputManifest, manifest)
	}
	if got.Status != RunCompleted || got.Output != "done" {
		t.Fatalf("completion fields not saved: status=%q output=%q", got.Status, got.Output)
	}
}

func TestStore_SaveRunInputManifestConflictDoesNotUpdateResultOrReparent(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:   "exp-conflict-a",
		Name: "manifest",
		Task: Task{Prompt: "test prompt"},
		Variants: []Variant{
			{ID: "var-1", Name: "v1", ModelID: "gpt-4"},
			{ID: "var-2", Name: "v2", ModelID: "gpt-5"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	manifest, err := buildRunInputManifest(exp, exp.Variants[0], time.Minute)
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-conflict", ExperimentID: exp.ID, VariantID: "var-1", Status: RunRunning, Output: "initial", InputManifest: manifest}); err != nil {
		t.Fatalf("SaveRun initial: %v", err)
	}
	changed, err := buildRunInputManifest(exp, exp.Variants[1], time.Minute)
	if err != nil {
		t.Fatalf("build changed manifest: %v", err)
	}
	err = store.SaveRun(&Run{ID: "run-conflict", ExperimentID: exp.ID, VariantID: "var-2", Status: RunCompleted, Output: "rewritten", InputManifest: changed})
	if !errors.Is(err, ErrRunManifestConflict) {
		t.Fatalf("SaveRun changed manifest/reparent error = %v, want ErrRunManifestConflict", err)
	}
	got, err := store.GetRun("run-conflict")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.VariantID != "var-1" || got.Status != RunRunning || got.Output != "initial" {
		t.Fatalf("conflict updated row: variant=%q status=%q output=%q", got.VariantID, got.Status, got.Output)
	}
}

func TestStore_SaveRunDoesNotRetroactivelyAttachManifestToLegacyRun(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-legacy-manifest",
		Name:     "legacy",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "gpt-4"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-legacy", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted, Output: "legacy"}); err != nil {
		t.Fatalf("SaveRun legacy: %v", err)
	}
	manifest, err := buildRunInputManifest(exp, exp.Variants[0], time.Minute)
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	err = store.SaveRun(&Run{ID: "run-legacy", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted, Output: "claimed", InputManifest: manifest})
	if !errors.Is(err, ErrRunManifestConflict) {
		t.Fatalf("SaveRun retroactive manifest error = %v, want ErrRunManifestConflict", err)
	}
	got, err := store.GetRun("run-legacy")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.InputManifest != nil || got.Output != "legacy" {
		t.Fatalf("legacy row changed: manifest=%#v output=%q", got.InputManifest, got.Output)
	}
}

func TestStore_GetRunRejectsMalformedInputManifest(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-malformed-manifest",
		Name:     "malformed",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "gpt-4"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-malformed", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if _, err := db.Exec(`UPDATE experiment_runs SET input_manifest_json = ? WHERE id = ?`, `{"version":"wrong"}`, "run-malformed"); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}
	if _, err := store.GetRun("run-malformed"); err == nil {
		t.Fatalf("GetRun malformed manifest error = nil, want error")
	}
	if _, err := store.ListRuns(exp.ID); err == nil {
		t.Fatalf("ListRuns malformed manifest error = nil, want error")
	}
}

func TestStore_SaveRunConflictingManifestRaceAcrossConnections(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "manifest-race.db")
	base, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New base: %v", err)
	}
	storeA := NewStoreFromStorage(base)
	exp := &Experiment{
		ID:       "exp-race",
		Name:     "race",
		Task:     Task{Prompt: "race prompt"},
		Variants: []Variant{{ID: "var-race", Name: "variant", ModelID: "model-a"}},
	}
	if err := storeA.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := base.Close(); err != nil {
		t.Fatalf("close base: %v", err)
	}

	storageA, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New A: %v", err)
	}
	defer storageA.Close()
	storageB, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New B: %v", err)
	}
	defer storageB.Close()
	storeA = NewStoreFromStorage(storageA)
	storeB := NewStoreFromStorage(storageB)

	manifestA, err := buildRunInputManifest(exp, exp.Variants[0], time.Minute)
	if err != nil {
		t.Fatalf("build manifest A: %v", err)
	}
	expB := *exp
	expB.Task.Prompt = "different race prompt"
	manifestB, err := buildRunInputManifest(&expB, exp.Variants[0], time.Minute)
	if err != nil {
		t.Fatalf("build manifest B: %v", err)
	}
	if manifestA.InputDigest == manifestB.InputDigest {
		t.Fatalf("test setup produced identical manifests")
	}

	type saveResult struct {
		label    string
		manifest *RunInputManifest
		output   string
		err      error
	}
	start := make(chan struct{})
	results := make(chan saveResult, 2)
	var wg sync.WaitGroup
	save := func(label string, store *Store, manifest *RunInputManifest, output string) {
		defer wg.Done()
		<-start
		err := store.SaveRun(&Run{
			ID:            "run-race",
			ExperimentID:  exp.ID,
			VariantID:     "var-race",
			Branch:        "experiment/exp-race/var-race",
			Status:        RunCompleted,
			Output:        output,
			StartedAt:     time.Now(),
			InputManifest: manifest,
		})
		results <- saveResult{label: label, manifest: manifest, output: output, err: err}
	}
	wg.Add(2)
	go save("A", storeA, manifestA, "winner-a")
	go save("B", storeB, manifestB, "winner-b")
	close(start)
	wg.Wait()
	close(results)

	accepted := make([]saveResult, 0, 1)
	conflicts := 0
	for result := range results {
		if result.err == nil {
			accepted = append(accepted, result)
			continue
		}
		if errors.Is(result.err, ErrRunManifestConflict) {
			conflicts++
			continue
		}
		t.Fatalf("save %s unexpected error: %v", result.label, result.err)
	}
	if len(accepted) != 1 || conflicts != 1 {
		t.Fatalf("race results accepted=%d conflicts=%d, want 1/1", len(accepted), conflicts)
	}
	got, err := storeA.GetRun("run-race")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.InputManifest == nil {
		t.Fatalf("GetRun = %#v, want manifest-backed winning run", got)
	}
	winner := accepted[0]
	if got.Output != winner.output || got.InputManifest.InputDigest != winner.manifest.InputDigest {
		t.Fatalf("mixed state: got output=%q digest=%s, winner output=%q digest=%s",
			got.Output, got.InputManifest.InputDigest, winner.output, winner.manifest.InputDigest)
	}
}

func TestStore_SaveRunModelExecutionsImmutable(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-model-exec",
		Name:     "identity",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model", ProviderID: "provider-a"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunRunning, Output: "running"}); err != nil {
		t.Fatalf("SaveRun initial: %v", err)
	}
	identity := []model.ExecutionIdentity{{
		RequestedModel: "requested/model",
		SelectedModel:  "selected/model",
		ProviderID:     "provider-a",
		ResponseModel:  "reported/model",
		ResponseID:     "resp-1",
	}}
	if err := store.SaveRun(&Run{ID: "run-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted, Output: "completed", ModelExecutions: identity}); err != nil {
		t.Fatalf("SaveRun terminal identity: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted, Output: "nil preserve"}); err != nil {
		t.Fatalf("SaveRun nil preserve: %v", err)
	}
	got, err := store.GetRun("run-identity")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Output != "nil preserve" || len(got.ModelExecutions) != 1 || got.ModelExecutions[0] != identity[0] {
		t.Fatalf("run after nil preserve = %+v, want output update with identity preserved", got)
	}
	changed := []model.ExecutionIdentity{identity[0]}
	changed[0].ResponseID = "resp-2"
	err = store.SaveRun(&Run{ID: "run-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted, Output: "rewritten", ModelExecutions: changed})
	if !errors.Is(err, ErrRunManifestConflict) {
		t.Fatalf("SaveRun changed identity error = %v, want ErrRunManifestConflict", err)
	}
	got, err = store.GetRun("run-identity")
	if err != nil {
		t.Fatalf("GetRun after conflict: %v", err)
	}
	if got.Output != "nil preserve" || got.ModelExecutions[0].ResponseID != "resp-1" {
		t.Fatalf("identity conflict updated row: %+v", got)
	}
}

func TestStore_SaveRunRejectsModelExecutionsForNonterminalRun(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-nonterminal-identity",
		Name:     "identity",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	identity := []model.ExecutionIdentity{{
		RequestedModel: "requested/model",
		SelectedModel:  "requested/model",
		ProviderID:     "provider-a",
		ResponseModel:  "requested/model",
		ResponseID:     "resp-1",
	}}
	err := store.SaveRun(&Run{ID: "run-nonterminal-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunRunning, Output: "running", ModelExecutions: identity})
	if !errors.Is(err, ErrRunManifestConflict) {
		t.Fatalf("SaveRun nonterminal identity error = %v, want ErrRunManifestConflict", err)
	}
	var inserted int
	if err := db.QueryRow(`SELECT COUNT(*) FROM experiment_runs WHERE id = ?`, "run-nonterminal-identity").Scan(&inserted); err != nil {
		t.Fatalf("count nonterminal identity row: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("nonterminal identity save inserted %d rows, want 0", inserted)
	}

	if err := store.SaveRun(&Run{ID: "run-nonterminal-update", ExperimentID: exp.ID, VariantID: "var-1", Status: RunRunning, Output: "running"}); err != nil {
		t.Fatalf("SaveRun running without identity: %v", err)
	}
	err = store.SaveRun(&Run{ID: "run-nonterminal-update", ExperimentID: exp.ID, VariantID: "var-1", Status: RunRunning, Output: "claimed", ModelExecutions: identity})
	if !errors.Is(err, ErrRunManifestConflict) {
		t.Fatalf("SaveRun running update identity error = %v, want ErrRunManifestConflict", err)
	}
	got, err := store.GetRun("run-nonterminal-update")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Output != "running" || got.ModelExecutions != nil {
		t.Fatalf("nonterminal identity update changed row: output=%q identities=%#v", got.Output, got.ModelExecutions)
	}
}

func TestStore_SaveRunConflictingModelExecutionsRaceAcrossConnections(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "identity-race.db")
	base, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New base: %v", err)
	}
	store := NewStoreFromStorage(base)
	exp := &Experiment{
		ID:       "exp-identity-race",
		Name:     "race",
		Task:     Task{Prompt: "race prompt"},
		Variants: []Variant{{ID: "var-race", Name: "variant", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := base.Close(); err != nil {
		t.Fatalf("close base: %v", err)
	}

	storageA, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New A: %v", err)
	}
	defer storageA.Close()
	storageB, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New B: %v", err)
	}
	defer storageB.Close()
	storeA := NewStoreFromStorage(storageA)
	storeB := NewStoreFromStorage(storageB)

	type saveResult struct {
		label    string
		identity []model.ExecutionIdentity
		output   string
		err      error
	}
	identityA := []model.ExecutionIdentity{{SelectedModel: "model-a", ProviderID: "provider-a", ResponseModel: "model-a", ResponseID: "resp-a"}}
	identityB := []model.ExecutionIdentity{{SelectedModel: "model-b", ProviderID: "provider-b", ResponseModel: "model-b", ResponseID: "resp-b"}}
	start := make(chan struct{})
	results := make(chan saveResult, 2)
	var wg sync.WaitGroup
	save := func(label string, store *Store, identity []model.ExecutionIdentity, output string) {
		defer wg.Done()
		<-start
		err := store.SaveRun(&Run{
			ID:              "run-identity-race",
			ExperimentID:    exp.ID,
			VariantID:       "var-race",
			Status:          RunCompleted,
			Output:          output,
			StartedAt:       time.Now(),
			ModelExecutions: identity,
		})
		results <- saveResult{label: label, identity: identity, output: output, err: err}
	}
	wg.Add(2)
	go save("A", storeA, identityA, "winner-a")
	go save("B", storeB, identityB, "winner-b")
	close(start)
	wg.Wait()
	close(results)

	accepted := make([]saveResult, 0, 1)
	conflicts := 0
	for result := range results {
		if result.err == nil {
			accepted = append(accepted, result)
			continue
		}
		if errors.Is(result.err, ErrRunManifestConflict) {
			conflicts++
			continue
		}
		t.Fatalf("save %s unexpected error: %v", result.label, result.err)
	}
	if len(accepted) != 1 || conflicts != 1 {
		t.Fatalf("race results accepted=%d conflicts=%d, want 1/1", len(accepted), conflicts)
	}
	got, err := storeA.GetRun("run-identity-race")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	winner := accepted[0]
	if got.Output != winner.output || len(got.ModelExecutions) != 1 || got.ModelExecutions[0] != winner.identity[0] {
		t.Fatalf("mixed state: got output=%q identities=%+v, winner output=%q identities=%+v",
			got.Output, got.ModelExecutions, winner.output, winner.identity)
	}
}

func TestStore_SaveRunDoesNotRetroactivelyAttachModelExecutionsToLegacyTerminalRun(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-legacy-identity",
		Name:     "legacy",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-legacy-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted, Output: "legacy"}); err != nil {
		t.Fatalf("SaveRun legacy: %v", err)
	}
	err := store.SaveRun(&Run{ID: "run-legacy-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted, Output: "claimed", ModelExecutions: []model.ExecutionIdentity{{SelectedModel: "selected/model"}}})
	if !errors.Is(err, ErrRunManifestConflict) {
		t.Fatalf("SaveRun retroactive identity error = %v, want ErrRunManifestConflict", err)
	}
	got, err := store.GetRun("run-legacy-identity")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(got.ModelExecutions) != 0 || got.Output != "legacy" {
		t.Fatalf("legacy row changed: identities=%+v output=%q", got.ModelExecutions, got.Output)
	}
}

func TestStore_SaveRunPersistsCapturedEmptyModelExecutions(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-empty-identity",
		Name:     "empty",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-empty-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunRunning}); err != nil {
		t.Fatalf("SaveRun initial: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-empty-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunFailed, ModelExecutions: []model.ExecutionIdentity{}}); err != nil {
		t.Fatalf("SaveRun captured empty identity: %v", err)
	}
	got, err := store.GetRun("run-empty-identity")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ModelExecutions == nil || len(got.ModelExecutions) != 0 {
		t.Fatalf("model executions = %#v, want non-nil empty captured marker", got.ModelExecutions)
	}
	var raw sql.NullString
	if err := db.QueryRow(`SELECT model_executions_json FROM experiment_runs WHERE id = ?`, "run-empty-identity").Scan(&raw); err != nil {
		t.Fatalf("query raw model executions: %v", err)
	}
	if !raw.Valid || raw.String != "[]" {
		t.Fatalf("raw model_executions_json = %#v, want []", raw)
	}
}

func TestStore_SaveRunPreservesUsageEvidenceAndDoesNotAlias(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-usage-evidence",
		Name:     "usage",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	reasoning := 3
	usage := &transparency.TokenUsage{
		Input:                10,
		Output:               5,
		ReportedTotal:        15,
		ReportedReasoning:    &reasoning,
		UsageEvidencePresent: true,
	}
	run := &Run{
		ID:           "run-usage-evidence",
		ExperimentID: exp.ID,
		VariantID:    "var-1",
		Status:       RunCompleted,
		Metrics:      RunMetrics{TotalCost: 0.01, Usage: usage, CostUnknown: true},
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	*usage.ReportedReasoning = 99
	run.Metrics.Usage.Input = 999

	got, err := store.GetRun("run-usage-evidence")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Metrics.Usage == nil || got.Metrics.Usage.Input != 10 || got.Metrics.Usage.ReportedReasoning == nil || *got.Metrics.Usage.ReportedReasoning != 3 || !got.Metrics.CostUnknown {
		t.Fatalf("usage evidence = %+v costUnknown=%v, want retained original", got.Metrics.Usage, got.Metrics.CostUnknown)
	}
	*got.Metrics.Usage.ReportedReasoning = 42
	again, err := store.GetRun("run-usage-evidence")
	if err != nil {
		t.Fatalf("GetRun again: %v", err)
	}
	if again.Metrics.Usage == nil || again.Metrics.Usage.ReportedReasoning == nil || *again.Metrics.Usage.ReportedReasoning != 3 {
		t.Fatalf("stored usage aliases caller result: %+v", again.Metrics.Usage)
	}
	runs, err := store.ListRuns(exp.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Metrics.Usage == nil || runs[0].Metrics.Usage.Input != 10 || !runs[0].Metrics.CostUnknown {
		t.Fatalf("ListRuns usage = %+v", runs)
	}
}

func TestStore_GetRunRejectsMalformedUsageEvidence(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-malformed-usage",
		Name:     "malformed",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-malformed-usage", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if _, err := db.Exec(`UPDATE experiment_runs SET usage_json = ? WHERE id = ?`, `{"input":1} {}`, "run-malformed-usage"); err != nil {
		t.Fatalf("corrupt usage evidence: %v", err)
	}
	if _, err := store.GetRun("run-malformed-usage"); err == nil {
		t.Fatalf("GetRun malformed usage evidence error = nil, want error")
	}
	if _, err := store.ListRuns(exp.ID); err == nil {
		t.Fatalf("ListRuns malformed usage evidence error = nil, want error")
	}
}

func TestStore_GetRunRejectsMalformedModelExecutions(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-malformed-identity",
		Name:     "malformed",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-malformed-identity", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if _, err := db.Exec(`UPDATE experiment_runs SET model_executions_json = ? WHERE id = ?`, "[{\"response_id\":\"bad\nid\"}]", "run-malformed-identity"); err != nil {
		t.Fatalf("corrupt model executions: %v", err)
	}
	if _, err := store.GetRun("run-malformed-identity"); err == nil {
		t.Fatalf("GetRun malformed model executions error = nil, want error")
	}
	if _, err := store.ListRuns(exp.ID); err == nil {
		t.Fatalf("ListRuns malformed model executions error = nil, want error")
	}
}

func TestStore_GetRunRejectsUnknownModelExecutionField(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-unknown-identity-field",
		Name:     "malformed",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&Run{ID: "run-unknown-identity-field", ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if _, err := db.Exec(`UPDATE experiment_runs SET model_executions_json = ? WHERE id = ?`, `[{"selected_modle":"typo"}]`, "run-unknown-identity-field"); err != nil {
		t.Fatalf("corrupt model executions: %v", err)
	}
	if _, err := store.GetRun("run-unknown-identity-field"); err == nil {
		t.Fatalf("GetRun unknown model execution field error = nil, want error")
	}
}

func TestStore_GetRunRejectsNullOrTrailingModelExecutionsJSON(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:       "exp-bad-identity-json",
		Name:     "malformed",
		Task:     Task{Prompt: "test prompt"},
		Variants: []Variant{{ID: "var-1", Name: "v1", ModelID: "requested/model"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{name: "non-null empty", raw: ``},
		{name: "non-null whitespace", raw: " \n\t"},
		{name: "literal null", raw: `null`},
		{name: "trailing object", raw: `[] {}`},
		{name: "trailing garbage", raw: `[] nope`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runID := "run-" + strings.ReplaceAll(tt.name, " ", "-")
			if err := store.SaveRun(&Run{ID: runID, ExperimentID: exp.ID, VariantID: "var-1", Status: RunCompleted}); err != nil {
				t.Fatalf("SaveRun: %v", err)
			}
			if _, err := db.Exec(`UPDATE experiment_runs SET model_executions_json = ? WHERE id = ?`, tt.raw, runID); err != nil {
				t.Fatalf("corrupt model executions: %v", err)
			}
			if _, err := store.GetRun(runID); err == nil {
				t.Fatalf("GetRun %s error = nil, want malformed model executions error", tt.name)
			}
		})
	}
}

func TestStore_ListRuns(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Create experiment and runs
	exp := &Experiment{
		ID:   "exp-list-runs",
		Name: "test",
		Task: Task{Prompt: "test"},
		Variants: []Variant{
			{ID: "var-1", Name: "v1", ModelID: "gpt-4"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	for i := 0; i < 3; i++ {
		run := &Run{
			ExperimentID: "exp-list-runs",
			VariantID:    "var-1",
			Status:       RunCompleted,
		}
		if err := store.SaveRun(run); err != nil {
			t.Fatalf("failed to save run: %v", err)
		}
	}

	tests := []struct {
		name         string
		experimentID string
		wantCount    int
		wantErr      bool
	}{
		{"empty id returns error", "", 0, true},
		{"non-existent returns empty", "non-existent", 0, false},
		{"existing returns runs", "exp-list-runs", 3, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.ListRuns(tt.experimentID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ListRuns() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(got) != tt.wantCount {
				t.Errorf("ListRuns() count = %v, want %v", len(got), tt.wantCount)
			}
		})
	}
}

func TestStore_ReplaceEvaluations(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Create experiment, variant, criterion, and run
	exp := &Experiment{
		ID:   "exp-eval",
		Name: "test",
		Task: Task{Prompt: "test"},
		Variants: []Variant{
			{ID: "var-1", Name: "v1", ModelID: "gpt-4"},
		},
		Criteria: []SuccessCriterion{
			{Name: "test", Type: CriterionTestPass, Target: "go test"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	run := &Run{
		ID:           "run-1",
		ExperimentID: "exp-eval",
		VariantID:    "var-1",
		Status:       RunCompleted,
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("failed to save run: %v", err)
	}

	tests := []struct {
		name    string
		runID   string
		evals   []CriterionEvaluation
		wantErr bool
	}{
		{"empty run id returns error", "", nil, true},
		{"empty evals succeeds", "run-1", nil, false},
		{
			"valid evaluations",
			"run-1",
			[]CriterionEvaluation{
				{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1.0},
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.ReplaceEvaluations(tt.runID, tt.evals)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReplaceEvaluations() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStore_FindExperimentByName(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Create experiments with same name
	for i := 0; i < 3; i++ {
		exp := &Experiment{
			Name: "same-name",
			Task: Task{Prompt: "test"},
		}
		if err := store.CreateExperiment(exp); err != nil {
			t.Fatalf("failed to create experiment: %v", err)
		}
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	tests := []struct {
		name    string
		expName string
		wantNil bool
		wantErr bool
	}{
		{"empty name returns error", "", true, true},
		{"non-existent returns nil", "non-existent", true, false},
		{"existing returns most recent", "same-name", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.FindExperimentByName(tt.expName)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindExperimentByName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if (got == nil) != tt.wantNil {
				t.Errorf("FindExperimentByName() = %v, wantNil %v", got, tt.wantNil)
			}
		})
	}
}

func TestStore_NilStore(t *testing.T) {
	var store *Store

	// All operations should return ErrStoreUnavailable
	if err := store.CreateExperiment(&Experiment{}); err != ErrStoreUnavailable {
		t.Errorf("CreateExperiment on nil store should return ErrStoreUnavailable, got %v", err)
	}

	if _, err := store.GetExperiment("id"); err != ErrStoreUnavailable {
		t.Errorf("GetExperiment on nil store should return ErrStoreUnavailable, got %v", err)
	}

	if _, err := store.ListExperiments(10, ""); err != ErrStoreUnavailable {
		t.Errorf("ListExperiments on nil store should return ErrStoreUnavailable, got %v", err)
	}

	if err := store.UpdateExperimentStatus("id", ExperimentRunning, nil); err != ErrStoreUnavailable {
		t.Errorf("UpdateExperimentStatus on nil store should return ErrStoreUnavailable, got %v", err)
	}

	if err := store.SaveRun(&Run{}); err != ErrStoreUnavailable {
		t.Errorf("SaveRun on nil store should return ErrStoreUnavailable, got %v", err)
	}

	if _, err := store.ListRuns("id"); err != ErrStoreUnavailable {
		t.Errorf("ListRuns on nil store should return ErrStoreUnavailable, got %v", err)
	}

	if err := store.ReplaceEvaluations("id", nil); err != ErrStoreUnavailable {
		t.Errorf("ReplaceEvaluations on nil store should return ErrStoreUnavailable, got %v", err)
	}

	if _, err := store.GetRun("id"); err != ErrStoreUnavailable {
		t.Errorf("GetRun on nil store should return ErrStoreUnavailable, got %v", err)
	}
}

func TestNewStoreFromStorage(t *testing.T) {
	tests := []struct {
		name    string
		store   interface{}
		wantNil bool
	}{
		{
			name:    "nil storage returns nil",
			store:   nil,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *Store
			switch tt.store {
			case nil:
				got = NewStoreFromStorage(nil)
			}

			if (got == nil) != tt.wantNil {
				t.Errorf("NewStoreFromStorage() = %v, wantNil %v", got, tt.wantNil)
			}
		})
	}
}

func TestTimeoutMillis(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    int64
	}{
		{
			name:    "zero returns 0",
			timeout: 0,
			want:    0,
		},
		{
			name:    "negative returns 0",
			timeout: -1 * time.Second,
			want:    0,
		},
		{
			name:    "positive duration",
			timeout: 5 * time.Second,
			want:    5000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := timeoutMillis(tt.timeout)
			if got != tt.want {
				t.Errorf("timeoutMillis() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDurationFromMillis(t *testing.T) {
	tests := []struct {
		name string
		raw  sql.NullInt64
		want time.Duration
	}{
		{
			name: "null returns 0",
			raw:  sql.NullInt64{Valid: false},
			want: 0,
		},
		{
			name: "zero returns 0",
			raw:  sql.NullInt64{Valid: true, Int64: 0},
			want: 0,
		},
		{
			name: "negative returns 0",
			raw:  sql.NullInt64{Valid: true, Int64: -100},
			want: 0,
		},
		{
			name: "positive value",
			raw:  sql.NullInt64{Valid: true, Int64: 5000},
			want: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := durationFromMillis(tt.raw)
			if got != tt.want {
				t.Errorf("durationFromMillis() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNullIntPtr(t *testing.T) {
	five := 5
	zero := 0

	tests := []struct {
		name  string
		value *int
		want  any
	}{
		{
			name:  "nil returns nil",
			value: nil,
			want:  nil,
		},
		{
			name:  "zero returns nil",
			value: &zero,
			want:  nil,
		},
		{
			name:  "non-zero returns value",
			value: &five,
			want:  5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nullIntPtr(tt.value)
			if got != tt.want {
				t.Errorf("nullIntPtr() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNullFloatPtr(t *testing.T) {
	value := 0.5
	zero := 0.0

	tests := []struct {
		name  string
		value *float64
		want  any
	}{
		{
			name:  "nil returns nil",
			value: nil,
			want:  nil,
		},
		{
			name:  "zero returns nil",
			value: &zero,
			want:  nil,
		},
		{
			name:  "non-zero returns value",
			value: &value,
			want:  0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nullFloatPtr(tt.value)
			if got != tt.want {
				t.Errorf("nullFloatPtr() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMarshalUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]string
		wantErr bool
	}{
		{
			name:    "nil map",
			input:   nil,
			wantErr: false,
		},
		{
			name:    "empty map",
			input:   map[string]string{},
			wantErr: false,
		},
		{
			name: "map with values",
			input: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonStr, err := marshalJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("marshalJSON() expected error")
				}
				return
			}
			if err != nil {
				t.Errorf("marshalJSON() error = %v", err)
				return
			}

			var result map[string]string
			if jsonStr != "" {
				err = unmarshalJSON(jsonStr, &result)
				if err != nil {
					t.Errorf("unmarshalJSON() error = %v", err)
					return
				}

				if len(result) != len(tt.input) {
					t.Errorf("round-trip length = %d, want %d", len(result), len(tt.input))
				}
				for k, v := range tt.input {
					if result[k] != v {
						t.Errorf("round-trip[%q] = %q, want %q", k, result[k], v)
					}
				}
			}
		})
	}
}

func TestGetRun(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Create experiment first
	exp := &Experiment{
		ID:   "exp-getrun",
		Name: "getrun-test",
		Task: Task{Prompt: "test"},
		Variants: []Variant{
			{ID: "var-1", Name: "variant-1", ModelID: "gpt-4"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	// Create run
	run := &Run{
		ID:           "run-1",
		ExperimentID: "exp-getrun",
		VariantID:    "var-1",
		Status:       RunCompleted,
		Output:       "test output",
		Metrics: RunMetrics{
			DurationMs: 1000,
			TotalCost:  0.01,
		},
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("failed to save run: %v", err)
	}

	tests := []struct {
		name    string
		runID   string
		wantNil bool
		wantErr bool
	}{
		{
			name:    "existing run",
			runID:   "run-1",
			wantNil: false,
			wantErr: false,
		},
		{
			name:    "nonexistent run",
			runID:   "run-nonexistent",
			wantNil: true,
			wantErr: false,
		},
		{
			name:    "empty id",
			runID:   "",
			wantNil: true,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.GetRun(tt.runID)

			if tt.wantErr && err == nil {
				t.Error("GetRun() error = nil, want error")
				return
			}
			if !tt.wantErr && err != nil {
				t.Errorf("GetRun() error = %v", err)
				return
			}

			if (got == nil) != tt.wantNil {
				t.Errorf("GetRun() = %v, wantNil %v", got, tt.wantNil)
			}

			if got != nil && got.ID != tt.runID {
				t.Errorf("GetRun() ID = %v, want %v", got.ID, tt.runID)
			}
		})
	}
}
