package cost

import (
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/odvcencio/buckley/pkg/storage"
)

func newTrackerWithMocks(t *testing.T, session *storage.Session, daily, monthly float64) (*Tracker, *MockcostStore, *MockCostCalculator) {
	t.Helper()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	store := NewMockcostStore(ctrl)
	calc := NewMockCostCalculator(ctrl)

	if session == nil {
		session = &storage.Session{ID: "s"}
	}

	store.EXPECT().GetSession("s").Return(session, nil)
	store.EXPECT().GetDailyCost().Return(daily, nil)
	store.EXPECT().GetMonthlyCost().Return(monthly, nil)

	tracker, err := New("s", store, calc)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return tracker, store, calc
}

func TestCheckBudgetZeroLimits(t *testing.T) {
	tracker, _, _ := newTrackerWithMocks(t, &storage.Session{ID: "s", TotalCost: 10}, 5, 20)
	tracker.SetBudgets(0, 0, 0, 0)

	status := tracker.CheckBudget()
	if status.SessionPercent != 0 || status.DailyPercent != 0 || status.MonthlyPercent != 0 {
		t.Fatalf("expected zero percentages when limits disabled, got %+v", status)
	}
	if status.ShouldWarn || status.ShouldStop {
		t.Fatalf("expected no warnings when budgets disabled, got %+v", status)
	}
}

func TestSetBudgetsNormalizesValues(t *testing.T) {
	tracker, _, _ := newTrackerWithMocks(t, &storage.Session{ID: "s"}, 0, 0)

	tracker.SetBudgets(-1, 0, 5, -3)
	if tracker.sessionBudget != 0 || tracker.dailyBudget != 0 || tracker.autoStopAt != 0 {
		t.Fatalf("expected non-positive budgets to be normalized to zero, got session=%v daily=%v auto=%v",
			tracker.sessionBudget, tracker.dailyBudget, tracker.autoStopAt)
	}
	if tracker.monthlyBudget != 5 {
		t.Fatalf("expected positive budget to remain unchanged, got %v", tracker.monthlyBudget)
	}
}

func TestUnlimitedBudgetsNeverWarn(t *testing.T) {
	tracker, _, _ := newTrackerWithMocks(t, &storage.Session{ID: "s"}, 0, 0)

	tracker.SetBudgets(0, 0, 0, 0)
	tracker.sessionCost = 999
	tracker.dailyCost = 500
	tracker.monthlyCost = 1000

	status := tracker.CheckBudget()
	if status.ShouldWarn || status.ShouldStop {
		t.Fatalf("unlimited budgets should never warn/stop, got %+v", status)
	}
}

func TestEstimateStreamingCostNilCalculator(t *testing.T) {
	tracker, _, _ := newTrackerWithMocks(t, &storage.Session{ID: "s"}, 0, 0)
	// Simulate calculator becoming unavailable
	tracker.costCalc = nil

	if got := tracker.EstimateStreamingCost("model", 1024); got != 0 {
		t.Fatalf("EstimateStreamingCost() = %v, want 0 when calculator missing", got)
	}
}

func TestRecordAPICallPropagatesCalculatorError(t *testing.T) {
	tracker, store, calc := newTrackerWithMocks(t, &storage.Session{ID: "s"}, 0, 0)
	store.EXPECT().SaveAPICall(gomock.Any()).Times(0)
	calc.EXPECT().CalculateCostFromTokens("model", 10, 5).Return(0.0, errors.New("boom"))
	if _, err := tracker.RecordAPICall("model", 10, 5); err == nil {
		t.Fatal("RecordAPICall() expected error when calculator fails")
	}
}

func TestRecordAPICallPersistsData(t *testing.T) {
	tracker, store, calc := newTrackerWithMocks(t, &storage.Session{ID: "s"}, 0, 0)

	calc.EXPECT().CalculateCostFromTokens("model", 1000, 500).Return(0.5, nil)

	var savedCall *storage.APICall
	store.EXPECT().SaveAPICall(gomock.Any()).DoAndReturn(func(call *storage.APICall) error {
		savedCall = call
		return nil
	})

	cost, err := tracker.RecordAPICall("model", 1000, 500)
	if err != nil {
		t.Fatalf("RecordAPICall() error = %v", err)
	}
	if cost != 0.5 {
		t.Fatalf("RecordAPICall() cost = %v, want 0.5", cost)
	}
	if savedCall == nil {
		t.Fatal("expected SaveAPICall to be invoked")
	}
	if savedCall.Timestamp.After(time.Now().Add(time.Second)) {
		t.Fatalf("timestamp should be near now, got %v", savedCall.Timestamp)
	}
}

// TestGetSessionCost tests session cost retrieval
func TestGetSessionCost(t *testing.T) {
	session := &storage.Session{
		ID:        "test-session",
		TotalCost: 15.75,
	}
	tracker, _, _ := newTrackerWithMocks(t, session, 0, 0)

	cost := tracker.GetSessionCost()
	if cost != 15.75 {
		t.Errorf("GetSessionCost() = %v, want 15.75", cost)
	}
}

// TestGetDailyCost tests daily cost retrieval
func TestGetDailyCost(t *testing.T) {
	tracker, _, _ := newTrackerWithMocks(t, nil, 25.50, 0)

	cost := tracker.GetDailyCost()
	if cost != 25.50 {
		t.Errorf("GetDailyCost() = %v, want 25.50", cost)
	}
}

// TestGetMonthlyCost tests monthly cost retrieval
func TestGetMonthlyCost(t *testing.T) {
	tracker, _, _ := newTrackerWithMocks(t, nil, 0, 125.75)

	cost := tracker.GetMonthlyCost()
	if cost != 125.75 {
		t.Errorf("GetMonthlyCost() = %v, want 125.75", cost)
	}
}

// TestGetWarningMessage tests warning message generation
func TestGetWarningMessage(t *testing.T) {
	tests := []struct {
		name          string
		sessionCost   float64
		dailyCost     float64
		monthlyCost   float64
		sessionLimit  float64
		dailyLimit    float64
		monthlyLimit  float64
		expectWarning bool
		checkContent  func(string) bool
	}{
		{
			name:          "session_limit_warning",
			sessionCost:   18.0,
			dailyCost:     0,
			monthlyCost:   0,
			sessionLimit:  20.0,
			dailyLimit:    0,
			monthlyLimit:  0,
			expectWarning: true,
			checkContent: func(msg string) bool {
				return len(msg) > 0
			},
		},
		{
			name:          "daily_limit_warning",
			sessionCost:   0,
			dailyCost:     45.0,
			monthlyCost:   0,
			sessionLimit:  0,
			dailyLimit:    50.0,
			monthlyLimit:  0,
			expectWarning: true,
			checkContent: func(msg string) bool {
				return len(msg) > 0
			},
		},
		{
			name:          "monthly_limit_warning",
			sessionCost:   0,
			dailyCost:     0,
			monthlyCost:   180.0,
			sessionLimit:  0,
			dailyLimit:    0,
			monthlyLimit:  200.0,
			expectWarning: true,
			checkContent: func(msg string) bool {
				return len(msg) > 0
			},
		},
		{
			name:          "no_warning_under_limits",
			sessionCost:   5.0,
			dailyCost:     10.0,
			monthlyCost:   50.0,
			sessionLimit:  20.0,
			dailyLimit:    50.0,
			monthlyLimit:  200.0,
			expectWarning: false,
			checkContent: func(msg string) bool {
				return msg == ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &storage.Session{
				ID:        "test",
				TotalCost: tt.sessionCost,
			}
			tracker, _, _ := newTrackerWithMocks(t, session, tt.dailyCost, tt.monthlyCost)
			tracker.SetBudgets(tt.sessionLimit, tt.dailyLimit, tt.monthlyLimit, 0)

			status := tracker.CheckBudget()
			msg := status.GetWarningMessage()

			if tt.expectWarning && msg == "" {
				t.Error("expected warning message, got empty string")
			}

			if !tt.expectWarning && msg != "" {
				t.Errorf("expected no warning, got: %s", msg)
			}

			if !tt.checkContent(msg) {
				t.Errorf("message content check failed: %s", msg)
			}
		})
	}
}

// TestEstimateStreamingCost tests streaming cost estimation
func TestEstimateStreamingCost(t *testing.T) {
	tracker, _, calc := newTrackerWithMocks(t, nil, 0, 0)

	calc.EXPECT().CalculateCostFromTokens("test-model", 0, 100).Return(0.75, nil)
	if cost := tracker.EstimateStreamingCost("test-model", 100); cost != 0.75 {
		t.Errorf("EstimateStreamingCost() = %v, want 0.75", cost)
	}

	calc.EXPECT().CalculateCostFromTokens("bad-model", 0, 50).Return(0.0, errors.New("model not found"))
	if cost := tracker.EstimateStreamingCost("bad-model", 50); cost != 0 {
		t.Errorf("EstimateStreamingCost() error path = %v, want 0", cost)
	}

	if cost := tracker.EstimateStreamingCost("test-model", 0); cost != 0 {
		t.Errorf("EstimateStreamingCost() zero tokens = %v, want 0", cost)
	}
}
