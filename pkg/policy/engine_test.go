package policy

import (
	"testing"
	"time"
)

func TestCategorizeToolCall(t *testing.T) {
	tests := []struct {
		name     string
		call     ToolCall
		expected ToolCategory
	}{
		{
			name:     "read file",
			call:     ToolCall{Name: "read_file"},
			expected: CategoryFileRead,
		},
		{
			name:     "write file",
			call:     ToolCall{Name: "write_file"},
			expected: CategoryFileWrite,
		},
		{
			name:     "edit file",
			call:     ToolCall{Name: "edit_file"},
			expected: CategoryFileWrite,
		},
		{
			name:     "run shell",
			call:     ToolCall{Name: "run_shell"},
			expected: CategoryShell,
		},
		{
			name:     "bash command",
			call:     ToolCall{Name: "bash"},
			expected: CategoryShell,
		},
		{
			name:     "git status",
			call:     ToolCall{Name: "git_status"},
			expected: CategoryGit,
		},
		{
			name:     "search files",
			call:     ToolCall{Name: "search_files"},
			expected: CategorySearch,
		},
		{
			name:     "grep content",
			call:     ToolCall{Name: "grep"},
			expected: CategorySearch,
		},
		{
			name:     "http fetch",
			call:     ToolCall{Name: "http_fetch"},
			expected: CategoryNetwork,
		},
		{
			name:     "unknown tool",
			call:     ToolCall{Name: "custom_tool"},
			expected: CategoryUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := categorizeToolCall(tt.call)
			if got != tt.expected {
				t.Errorf("categorizeToolCall() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestMatchesCondition(t *testing.T) {
	tests := []struct {
		name      string
		call      ToolCall
		condition string
		expected  bool
	}{
		{
			name: "touches secrets - env file",
			call: ToolCall{
				Name:  "write_file",
				Input: map[string]any{"path": "/app/.env"},
			},
			condition: string(RiskTouchesSecrets),
			expected:  true,
		},
		{
			name: "touches secrets - credentials",
			call: ToolCall{
				Name:  "read_file",
				Input: map[string]any{"path": "/home/user/credentials.json"},
			},
			condition: string(RiskTouchesSecrets),
			expected:  true,
		},
		{
			name: "destructive - rm rf",
			call: ToolCall{
				Name:     "run_shell",
				Input:    map[string]any{"command": "rm -rf /tmp/build"},
				Category: CategoryShell,
			},
			condition: string(RiskDestructive),
			expected:  true,
		},
		{
			name: "destructive - DROP TABLE",
			call: ToolCall{
				Name:     "run_shell",
				Input:    map[string]any{"command": "sqlite3 db.sqlite 'DROP TABLE users'"},
				Category: CategoryShell,
			},
			condition: string(RiskDestructive),
			expected:  true,
		},
		{
			name: "external network - curl",
			call: ToolCall{
				Name:  "run_shell",
				Input: map[string]any{"command": "curl https://api.example.com/data"},
			},
			condition: string(RiskExternalNetwork),
			expected:  true,
		},
		{
			name: "external network - localhost allowed",
			call: ToolCall{
				Name:  "run_shell",
				Input: map[string]any{"command": "curl http://localhost:8080/health"},
			},
			condition: string(RiskExternalNetwork),
			expected:  false,
		},
		{
			name: "modifies git - commit",
			call: ToolCall{
				Name:  "run_shell",
				Input: map[string]any{"command": "git commit -m 'test'"},
			},
			condition: string(RiskModifiesGit),
			expected:  true,
		},
		{
			name: "modifies git - push",
			call: ToolCall{
				Name:  "run_shell",
				Input: map[string]any{"command": "git push origin main"},
			},
			condition: string(RiskModifiesGit),
			expected:  true,
		},
		{
			name: "installs package - npm",
			call: ToolCall{
				Name:  "run_shell",
				Input: map[string]any{"command": "npm install lodash"},
			},
			condition: string(RiskInstallsPackage),
			expected:  true,
		},
		{
			name: "installs package - go get",
			call: ToolCall{
				Name:  "run_shell",
				Input: map[string]any{"command": "go get github.com/pkg/errors"},
			},
			condition: string(RiskInstallsPackage),
			expected:  true,
		},
		{
			name: "safe command",
			call: ToolCall{
				Name:  "run_shell",
				Input: map[string]any{"command": "ls -la"},
			},
			condition: string(RiskDestructive),
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesCondition(tt.call, tt.condition)
			if got != tt.expected {
				t.Errorf("matchesCondition() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestMatchesException(t *testing.T) {
	tests := []struct {
		name       string
		call       ToolCall
		exceptions []Exception
		expected   bool
	}{
		{
			name: "matches log file pattern",
			call: ToolCall{
				Name:  "write_file",
				Input: map[string]any{"path": "/var/log/app.log"},
			},
			exceptions: []Exception{{Pattern: "*.log"}},
			expected:   true,
		},
		{
			name: "matches tmp directory",
			call: ToolCall{
				Name:  "write_file",
				Input: map[string]any{"path": "/tmp/build/output.txt"},
			},
			exceptions: []Exception{{Pattern: "/tmp/*"}},
			expected:   true,
		},
		{
			name: "matches command exception",
			call: ToolCall{
				Name:  "run_shell",
				Input: map[string]any{"command": "go test ./..."},
			},
			exceptions: []Exception{{Command: "go test *"}},
			expected:   true,
		},
		{
			name: "no match",
			call: ToolCall{
				Name:  "write_file",
				Input: map[string]any{"path": "/app/config.yaml"},
			},
			exceptions: []Exception{{Pattern: "*.log"}},
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesException(tt.call, tt.exceptions)
			if got != tt.expected {
				t.Errorf("matchesException() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEngineEvaluate(t *testing.T) {
	engine := NewEngine(nil)
	engine.policy = DefaultPolicy()

	tests := []struct {
		name           string
		call           ToolCall
		wantApproval   bool
		wantRiskScore  int
		minRiskReasons int
	}{
		{
			name: "file read - auto approve",
			call: ToolCall{
				Name:     "read_file",
				Input:    map[string]any{"path": "/app/main.go"},
				Category: CategoryFileRead,
			},
			wantApproval:  false,
			wantRiskScore: 0,
		},
		{
			name: "file write - requires approval",
			call: ToolCall{
				Name:     "write_file",
				Input:    map[string]any{"path": "/app/config.yaml"},
				Category: CategoryFileWrite,
			},
			wantApproval:   true,
			wantRiskScore:  20, // writes_config
			minRiskReasons: 1,
		},
		{
			name: "write log file - exception",
			call: ToolCall{
				Name:     "write_file",
				Input:    map[string]any{"path": "/var/log/app.log"},
				Category: CategoryFileWrite,
			},
			wantApproval:  false,
			wantRiskScore: 0,
		},
		{
			name: "shell rm -rf - high risk",
			call: ToolCall{
				Name:     "run_shell",
				Input:    map[string]any{"command": "rm -rf ./build"},
				Category: CategoryShell,
			},
			wantApproval:   true,
			wantRiskScore:  100, // destructive
			minRiskReasons: 1,
		},
		{
			name: "shell go test - exception",
			call: ToolCall{
				Name:     "run_shell",
				Input:    map[string]any{"command": "go test ./pkg/..."},
				Category: CategoryShell,
			},
			wantApproval:  false,
			wantRiskScore: 0,
		},
		{
			name: "secrets access - always approve",
			call: ToolCall{
				Name:     "read_file",
				Input:    map[string]any{"path": "/app/.env"},
				Category: CategoryFileRead,
			},
			wantApproval:   true,
			wantRiskScore:  100, // touches_secrets
			minRiskReasons: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := engine.Evaluate(tt.call)

			if result.RequiresApproval != tt.wantApproval {
				t.Errorf("RequiresApproval = %v, want %v", result.RequiresApproval, tt.wantApproval)
			}

			if result.RiskScore < tt.wantRiskScore {
				t.Errorf("RiskScore = %v, want >= %v", result.RiskScore, tt.wantRiskScore)
			}

			if len(result.RiskReasons) < tt.minRiskReasons {
				t.Errorf("RiskReasons count = %v, want >= %v", len(result.RiskReasons), tt.minRiskReasons)
			}

			if result.ExpiresAt.Before(time.Now()) {
				t.Error("ExpiresAt should be in the future")
			}
		})
	}
}

func TestEngineEvaluate_TimeWindowOverrides(t *testing.T) {
	engine := NewEngine(nil)
	policy := DefaultPolicy()
	policy.Config.TimeWindows = map[string]TimeWindow{
		"always_day": {
			Days: []string{
				"monday", "tuesday", "wednesday", "thursday",
				"friday", "saturday", "sunday",
			},
			Threshold: 0,
		},
		"always_hours": {
			Hours:     "00:00-23:59",
			Threshold: 50,
		},
	}
	engine.policy = policy

	call := ToolCall{
		Name:     "http_fetch",
		Input:    map[string]any{"url": "https://example.com"},
		Category: CategoryUnknown,
	}

	result := engine.Evaluate(call)
	if result.RequiresApproval {
		t.Fatalf("expected auto-approval when day window disables threshold, got approval (score=%d)", result.RiskScore)
	}
}

func TestInTimeRange(t *testing.T) {
	tests := []struct {
		name     string
		time     time.Time
		rangeStr string
		expected bool
	}{
		{
			name:     "within range",
			time:     time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			rangeStr: "09:00-18:00",
			expected: true,
		},
		{
			name:     "before range",
			time:     time.Date(2024, 1, 1, 8, 0, 0, 0, time.UTC),
			rangeStr: "09:00-18:00",
			expected: false,
		},
		{
			name:     "after range",
			time:     time.Date(2024, 1, 1, 19, 0, 0, 0, time.UTC),
			rangeStr: "09:00-18:00",
			expected: false,
		},
		{
			name:     "overnight range - before midnight",
			time:     time.Date(2024, 1, 1, 23, 0, 0, 0, time.UTC),
			rangeStr: "22:00-06:00",
			expected: true,
		},
		{
			name:     "overnight range - after midnight",
			time:     time.Date(2024, 1, 1, 3, 0, 0, 0, time.UTC),
			rangeStr: "22:00-06:00",
			expected: true,
		},
		{
			name:     "overnight range - outside",
			time:     time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			rangeStr: "22:00-06:00",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inTimeRange(tt.time, tt.rangeStr)
			if got != tt.expected {
				t.Errorf("inTimeRange() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDefaultPolicy(t *testing.T) {
	policy := DefaultPolicy()

	if policy.Name != "default" {
		t.Errorf("Name = %v, want default", policy.Name)
	}

	if !policy.IsActive {
		t.Error("IsActive should be true")
	}

	// Check categories exist
	expectedCategories := []string{"file_read", "file_write", "shell_command", "search", "git"}
	for _, cat := range expectedCategories {
		if _, ok := policy.Config.Categories[cat]; !ok {
			t.Errorf("Missing category: %s", cat)
		}
	}

	// Check risk rules exist
	if len(policy.Config.RiskRules) == 0 {
		t.Error("Should have risk rules")
	}

	// Check time windows exist
	if len(policy.Config.TimeWindows) == 0 {
		t.Error("Should have time windows")
	}
}
