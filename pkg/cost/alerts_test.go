package cost

import "testing"

func TestBudgetNotifier_FiresThresholds(t *testing.T) {
	notifier := NewBudgetNotifier()
	var alerts []BudgetAlert
	notifier.OnAlert(func(alert BudgetAlert) {
		alerts = append(alerts, alert)
	})

	status := &BudgetStatus{
		SessionBudget:  10,
		SessionPercent: 50,
		DailyBudget:    20,
		DailyPercent:   76,
		MonthlyBudget:  30,
		MonthlyPercent: 95,
	}

	notifier.Check(status)
	if len(alerts) != 3 {
		t.Fatalf("expected 3 alerts, got %d", len(alerts))
	}
	levels := map[string]BudgetAlertLevel{}
	for _, alert := range alerts {
		levels[alert.BudgetType] = alert.Level
	}
	if levels[BudgetTypeSession] != BudgetAlertInfo {
		t.Fatalf("expected session info alert, got %q", levels[BudgetTypeSession])
	}
	if levels[BudgetTypeDaily] != BudgetAlertWarning {
		t.Fatalf("expected daily warning alert, got %q", levels[BudgetTypeDaily])
	}
	if levels[BudgetTypeMonthly] != BudgetAlertCritical {
		t.Fatalf("expected monthly critical alert, got %q", levels[BudgetTypeMonthly])
	}
}

func TestBudgetNotifier_DedupesAlerts(t *testing.T) {
	notifier := NewBudgetNotifier()
	count := 0
	notifier.OnAlert(func(alert BudgetAlert) {
		count++
	})

	status := &BudgetStatus{
		SessionBudget:  10,
		SessionPercent: 80,
	}

	notifier.Check(status)
	if count != 1 {
		t.Fatalf("expected 1 alert, got %d", count)
	}
	notifier.Check(status)
	if count != 1 {
		t.Fatalf("expected no duplicate alerts, got %d", count)
	}

	status.SessionPercent = 95
	notifier.Check(status)
	if count != 2 {
		t.Fatalf("expected critical alert, got %d", count)
	}

	status.SessionPercent = 120
	notifier.Check(status)
	if count != 3 {
		t.Fatalf("expected exceeded alert, got %d", count)
	}
}
