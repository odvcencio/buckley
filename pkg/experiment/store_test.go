package experiment

import (
	"database/sql"
	"testing"
	"time"

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
		tool_calls INTEGER,
		tool_successes INTEGER,
		tool_failures INTEGER,
		files_modified INTEGER,
		lines_changed INTEGER,
		started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		completed_at TIMESTAMP
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
