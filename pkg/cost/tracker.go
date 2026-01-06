package cost

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

// costStore defines the storage operations required by the tracker.
type costStore interface {
	GetSession(sessionID string) (*storage.Session, error)
	GetDailyCost() (float64, error)
	GetMonthlyCost() (float64, error)
	SaveAPICall(call *storage.APICall) error
}

// CostCalculator abstracts token-to-dollar conversions.
type CostCalculator interface {
	CalculateCostFromTokens(modelID string, promptTokens, completionTokens int) (float64, error)
}

// Tracker tracks API costs and enforces budgets
type Tracker struct {
	sessionID string
	store     costStore
	costCalc  CostCalculator

	// In-memory tracking
	mu              sync.RWMutex
	sessionCost     float64
	dailyCost       float64
	monthlyCost     float64
	lastDailyUpdate time.Time

	// Budget limits
	sessionBudget float64
	dailyBudget   float64
	monthlyBudget float64
	autoStopAt    float64
}

// New creates a new cost tracker
func New(sessionID string, store costStore, calculator CostCalculator) (*Tracker, error) {
	if store == nil {
		return nil, errors.New("cost tracker requires a storage backend")
	}
	if calculator == nil {
		return nil, errors.New("cost tracker requires a cost calculator")
	}

	ct := &Tracker{
		sessionID: sessionID,
		store:     store,
		costCalc:  calculator,

		// Default budgets (can be overridden by config)
		sessionBudget: 5.00,
		dailyBudget:   20.00,
		monthlyBudget: 100.00,
		autoStopAt:    0, // 0 = no auto-stop
	}

	// Load current costs from database
	if err := ct.loadCosts(); err != nil {
		return nil, err
	}

	return ct, nil
}

// SetBudgets sets the budget limits
func (ct *Tracker) SetBudgets(session, daily, monthly, autoStop float64) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.sessionBudget = normalizeBudget(session)
	ct.dailyBudget = normalizeBudget(daily)
	ct.monthlyBudget = normalizeBudget(monthly)
	ct.autoStopAt = normalizeBudget(autoStop)
}

// loadCosts loads current costs from the database
func (ct *Tracker) loadCosts() error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// Get session cost
	session, err := ct.store.GetSession(ct.sessionID)
	if err != nil {
		return err
	}
	if session != nil {
		ct.sessionCost = session.TotalCost
	}

	// Get daily cost
	daily, err := ct.store.GetDailyCost()
	if err != nil {
		return err
	}
	ct.dailyCost = daily
	ct.lastDailyUpdate = time.Now()

	// Get monthly cost
	monthly, err := ct.store.GetMonthlyCost()
	if err != nil {
		return err
	}
	ct.monthlyCost = monthly

	return nil
}

// RecordAPICall records an API call and updates costs
func (ct *Tracker) RecordAPICall(modelID string, promptTokens, completionTokens int) (float64, error) {
	if ct.costCalc == nil {
		return 0, errors.New("cost calculator unavailable")
	}

	// Calculate cost
	cost, err := ct.costCalc.CalculateCostFromTokens(modelID, promptTokens, completionTokens)
	if err != nil {
		return 0, fmt.Errorf("failed to calculate cost: %w", err)
	}

	// Update in-memory costs
	ct.mu.Lock()
	ct.sessionCost += cost
	ct.dailyCost += cost
	ct.monthlyCost += cost
	ct.mu.Unlock()

	// Save to database
	apiCall := &storage.APICall{
		SessionID:        ct.sessionID,
		Model:            modelID,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Cost:             cost,
		Timestamp:        time.Now(),
	}

	if err := ct.store.SaveAPICall(apiCall); err != nil {
		return cost, fmt.Errorf("failed to save API call: %w", err)
	}

	return cost, nil
}

// GetSessionCost returns the current session cost
func (ct *Tracker) GetSessionCost() float64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.sessionCost
}

// EstimateStreamingCost estimates the cost for tokens generated during streaming
// Treats streaming tokens as completion tokens for conservative estimation
func (ct *Tracker) EstimateStreamingCost(modelID string, streamingTokens int) float64 {
	if streamingTokens <= 0 || ct.costCalc == nil {
		return 0
	}

	// Estimate cost treating all streaming tokens as completion tokens
	cost, err := ct.costCalc.CalculateCostFromTokens(modelID, 0, streamingTokens)
	if err != nil {
		return 0 // Fail silently for display purposes
	}

	return cost
}

// GetDailyCost returns the current daily cost
func (ct *Tracker) GetDailyCost() float64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	// Refresh if it's a new day
	if time.Since(ct.lastDailyUpdate) > 24*time.Hour {
		ct.mu.RUnlock()
		ct.loadCosts()
		ct.mu.RLock()
	}

	return ct.dailyCost
}

// GetMonthlyCost returns the current monthly cost
func (ct *Tracker) GetMonthlyCost() float64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.monthlyCost
}

// CheckBudget checks if any budget limits have been exceeded
func (ct *Tracker) CheckBudget() *BudgetStatus {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	status := &BudgetStatus{
		SessionCost:    ct.sessionCost,
		DailyCost:      ct.dailyCost,
		MonthlyCost:    ct.monthlyCost,
		SessionBudget:  ct.sessionBudget,
		DailyBudget:    ct.dailyBudget,
		MonthlyBudget:  ct.monthlyBudget,
		SessionPercent: budgetPercent(ct.sessionCost, ct.sessionBudget),
		DailyPercent:   budgetPercent(ct.dailyCost, ct.dailyBudget),
		MonthlyPercent: budgetPercent(ct.monthlyCost, ct.monthlyBudget),
	}

	// Check for budget violations
	if ct.sessionBudget > 0 && ct.sessionCost >= ct.sessionBudget {
		status.SessionExceeded = true
		status.ShouldStop = true
	}

	if ct.dailyBudget > 0 && ct.dailyCost >= ct.dailyBudget {
		status.DailyExceeded = true
		status.ShouldWarn = true
	}

	if ct.monthlyBudget > 0 && ct.monthlyCost >= ct.monthlyBudget {
		status.MonthlyExceeded = true
		status.ShouldWarn = true
	}

	// Check auto-stop threshold
	if ct.autoStopAt > 0 && ct.sessionCost >= ct.autoStopAt {
		status.ShouldStop = true
	}

	// Warning at 80%
	if status.SessionPercent >= 80 || status.DailyPercent >= 80 || status.MonthlyPercent >= 80 {
		status.ShouldWarn = true
	}

	return status
}

// BudgetStatus represents the current budget status
type BudgetStatus struct {
	SessionCost    float64
	DailyCost      float64
	MonthlyCost    float64
	SessionBudget  float64
	DailyBudget    float64
	MonthlyBudget  float64
	SessionPercent float64
	DailyPercent   float64
	MonthlyPercent float64

	SessionExceeded bool
	DailyExceeded   bool
	MonthlyExceeded bool

	ShouldWarn bool
	ShouldStop bool
}

// GetWarningMessage returns a warning message if needed
func (bs *BudgetStatus) GetWarningMessage() string {
	if bs.ShouldStop {
		if bs.SessionExceeded {
			return fmt.Sprintf("⛔ Session budget exceeded! ($%.2f / $%.2f)", bs.SessionCost, bs.SessionBudget)
		}
		return fmt.Sprintf("⛔ Auto-stop threshold reached! ($%.2f)", bs.SessionCost)
	}

	if bs.ShouldWarn {
		msg := "⚠️  Budget warnings:\n"
		if bs.SessionPercent >= 80 {
			msg += fmt.Sprintf("  • Session: $%.2f / $%.2f (%.0f%%)\n", bs.SessionCost, bs.SessionBudget, bs.SessionPercent)
		}
		if bs.DailyExceeded || bs.DailyPercent >= 80 {
			msg += fmt.Sprintf("  • Daily: $%.2f / $%.2f (%.0f%%)\n", bs.DailyCost, bs.DailyBudget, bs.DailyPercent)
		}
		if bs.MonthlyExceeded || bs.MonthlyPercent >= 80 {
			msg += fmt.Sprintf("  • Monthly: $%.2f / $%.2f (%.0f%%)\n", bs.MonthlyCost, bs.MonthlyBudget, bs.MonthlyPercent)
		}
		return msg
	}

	return ""
}

func budgetPercent(current, limit float64) float64 {
	if limit <= 0 {
		return 0
	}
	return (current / limit) * 100
}

func normalizeBudget(limit float64) float64 {
	if limit <= 0 {
		return 0
	}
	return limit
}
