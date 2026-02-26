package cost

import "sync"

// BudgetAlertLevel indicates the severity of a budget alert.
type BudgetAlertLevel string

const (
	BudgetAlertInfo     BudgetAlertLevel = "info"
	BudgetAlertWarning  BudgetAlertLevel = "warning"
	BudgetAlertCritical BudgetAlertLevel = "critical"
	BudgetAlertExceeded BudgetAlertLevel = "exceeded"
)

const (
	BudgetTypeSession = "session"
	BudgetTypeDaily   = "daily"
	BudgetTypeMonthly = "monthly"
)

// BudgetAlert describes a budget threshold alert.
type BudgetAlert struct {
	Level      BudgetAlertLevel
	BudgetType string
	Status     BudgetStatus
	Percent    float64
}

// BudgetAlertCallback receives budget alerts.
type BudgetAlertCallback func(alert BudgetAlert)

// BudgetNotifier evaluates budget thresholds and dispatches alerts.
type BudgetNotifier struct {
	mu         sync.Mutex
	thresholds map[BudgetAlertLevel]float64
	callbacks  []BudgetAlertCallback
	fired      map[string]bool
}

// NewBudgetNotifier creates a notifier with default thresholds.
func NewBudgetNotifier() *BudgetNotifier {
	return &BudgetNotifier{
		thresholds: defaultBudgetThresholds(),
		fired:      make(map[string]bool),
	}
}

// OnAlert registers a callback for budget alerts.
func (bn *BudgetNotifier) OnAlert(cb BudgetAlertCallback) {
	if bn == nil || cb == nil {
		return
	}
	bn.mu.Lock()
	bn.ensureDefaultsLocked()
	bn.callbacks = append(bn.callbacks, cb)
	bn.mu.Unlock()
}

// Check evaluates budget status and fires any threshold alerts.
func (bn *BudgetNotifier) Check(status *BudgetStatus) {
	if bn == nil || status == nil {
		return
	}
	bn.mu.Lock()
	bn.ensureDefaultsLocked()
	alerts := bn.collectAlertsLocked(status)
	callbacks := append([]BudgetAlertCallback{}, bn.callbacks...)
	bn.mu.Unlock()

	if len(alerts) == 0 || len(callbacks) == 0 {
		return
	}
	for _, alert := range alerts {
		for _, cb := range callbacks {
			cb(alert)
		}
	}
}

func (bn *BudgetNotifier) collectAlertsLocked(status *BudgetStatus) []BudgetAlert {
	var alerts []BudgetAlert
	if alert := bn.alertForBudgetLocked(BudgetTypeSession, status.SessionPercent, status.SessionBudget, status); alert != nil {
		alerts = append(alerts, *alert)
	}
	if alert := bn.alertForBudgetLocked(BudgetTypeDaily, status.DailyPercent, status.DailyBudget, status); alert != nil {
		alerts = append(alerts, *alert)
	}
	if alert := bn.alertForBudgetLocked(BudgetTypeMonthly, status.MonthlyPercent, status.MonthlyBudget, status); alert != nil {
		alerts = append(alerts, *alert)
	}
	return alerts
}

func (bn *BudgetNotifier) alertForBudgetLocked(budgetType string, percent float64, budget float64, status *BudgetStatus) *BudgetAlert {
	if budget <= 0 {
		return nil
	}
	level := bn.levelForPercentLocked(percent)
	if level == "" {
		return nil
	}
	key := budgetType + ":" + string(level)
	if bn.fired[key] {
		return nil
	}
	bn.fired[key] = true
	return &BudgetAlert{
		Level:      level,
		BudgetType: budgetType,
		Status:     *status,
		Percent:    percent,
	}
}

func (bn *BudgetNotifier) levelForPercentLocked(percent float64) BudgetAlertLevel {
	if percent <= 0 {
		return ""
	}
	if percent >= bn.thresholdForLocked(BudgetAlertExceeded) {
		return BudgetAlertExceeded
	}
	if percent >= bn.thresholdForLocked(BudgetAlertCritical) {
		return BudgetAlertCritical
	}
	if percent >= bn.thresholdForLocked(BudgetAlertWarning) {
		return BudgetAlertWarning
	}
	if percent >= bn.thresholdForLocked(BudgetAlertInfo) {
		return BudgetAlertInfo
	}
	return ""
}

func (bn *BudgetNotifier) thresholdForLocked(level BudgetAlertLevel) float64 {
	threshold, ok := bn.thresholds[level]
	if !ok {
		return 0
	}
	return threshold
}

func (bn *BudgetNotifier) ensureDefaultsLocked() {
	if bn.thresholds == nil {
		bn.thresholds = defaultBudgetThresholds()
	}
	if bn.fired == nil {
		bn.fired = make(map[string]bool)
	}
}

func defaultBudgetThresholds() map[BudgetAlertLevel]float64 {
	return map[BudgetAlertLevel]float64{
		BudgetAlertInfo:     50,
		BudgetAlertWarning:  75,
		BudgetAlertCritical: 90,
		BudgetAlertExceeded: 100,
	}
}
