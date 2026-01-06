package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Condition is an interface for checking if a condition is satisfied
type Condition interface {
	// Check returns true if the condition is satisfied
	Check(ctx context.Context) (bool, error)
	// String returns a human-readable description
	String() string
	// Timeout returns the timeout duration for this condition
	Timeout() time.Duration
}

// BaseCondition provides common fields for all conditions
type BaseCondition struct {
	timeout time.Duration
}

// Timeout returns the timeout duration
func (b *BaseCondition) Timeout() time.Duration {
	if b.timeout == 0 {
		return 30 * time.Second // Default timeout
	}
	return b.timeout
}

// AndCondition combines multiple conditions with AND logic
type AndCondition struct {
	BaseCondition
	conditions []Condition
}

// NewAndCondition creates a new AND condition
func NewAndCondition(conditions []Condition) *AndCondition {
	// Set timeout to max of all conditions
	var maxTimeout time.Duration
	for _, c := range conditions {
		if c.Timeout() > maxTimeout {
			maxTimeout = c.Timeout()
		}
	}

	return &AndCondition{
		BaseCondition: BaseCondition{timeout: maxTimeout},
		conditions:    conditions,
	}
}

// Check implements Condition interface
func (a *AndCondition) Check(ctx context.Context) (bool, error) {
	for _, condition := range a.conditions {
		satisfied, err := condition.Check(ctx)
		if err != nil {
			return false, fmt.Errorf("condition %s failed: %w", condition.String(), err)
		}
		if !satisfied {
			return false, nil
		}
	}
	return true, nil
}

// String implements Condition interface
func (a *AndCondition) String() string {
	var parts []string
	for _, c := range a.conditions {
		parts = append(parts, c.String())
	}
	return fmt.Sprintf("AND(%s)", strings.Join(parts, " && "))
}

// OrCondition combines multiple conditions with OR logic
type OrCondition struct {
	BaseCondition
	conditions []Condition
}

// NewOrCondition creates a new OR condition
func NewOrCondition(conditions []Condition) *OrCondition {
	// Set timeout to max of all conditions
	var maxTimeout time.Duration
	for _, c := range conditions {
		if c.Timeout() > maxTimeout {
			maxTimeout = c.Timeout()
		}
	}

	return &OrCondition{
		BaseCondition: BaseCondition{timeout: maxTimeout},
		conditions:    conditions,
	}
}

// Check implements Condition interface
func (o *OrCondition) Check(ctx context.Context) (bool, error) {
	for _, condition := range o.conditions {
		satisfied, err := condition.Check(ctx)
		if err != nil {
			continue // Continue checking other conditions on error
		}
		if satisfied {
			return true, nil
		}
	}
	return false, nil
}

// String implements Condition interface
func (o *OrCondition) String() string {
	var parts []string
	for _, c := range o.conditions {
		parts = append(parts, c.String())
	}
	return fmt.Sprintf("OR(%s)", strings.Join(parts, " || "))
}

// ConditionBuilder helps build complex conditions
type ConditionBuilder struct {
	conditions []Condition
}

// NewConditionBuilder creates a new condition builder
func NewConditionBuilder() *ConditionBuilder {
	return &ConditionBuilder{
		conditions: []Condition{},
	}
}

// And adds an AND condition
func (cb *ConditionBuilder) And(condition Condition) *ConditionBuilder {
	cb.conditions = append(cb.conditions, condition)
	return cb
}

// Build builds the final condition (ANDs all added conditions)
func (cb *ConditionBuilder) Build() Condition {
	if len(cb.conditions) == 0 {
		// Return a condition that's always true
		return &AlwaysTrueCondition{}
	}
	if len(cb.conditions) == 1 {
		return cb.conditions[0]
	}
	return NewAndCondition(cb.conditions)
}

// BuildOr creates an OR condition from all added conditions
func (cb *ConditionBuilder) BuildOr() Condition {
	if len(cb.conditions) == 0 {
		return &AlwaysFalseCondition{}
	}
	if len(cb.conditions) == 1 {
		return cb.conditions[0]
	}
	return NewOrCondition(cb.conditions)
}

// AlwaysTrueCondition always returns true
type AlwaysTrueCondition struct {
	BaseCondition
}

// Check implements Condition interface
func (a *AlwaysTrueCondition) Check(ctx context.Context) (bool, error) {
	return true, nil
}

// String implements Condition interface
func (a *AlwaysTrueCondition) String() string {
	return "AlwaysTrue"
}

// AlwaysFalseCondition always returns false
type AlwaysFalseCondition struct {
	BaseCondition
}

// Check implements Condition interface
func (a *AlwaysFalseCondition) Check(ctx context.Context) (bool, error) {
	return false, nil
}

// String implements Condition interface
func (a *AlwaysFalseCondition) String() string {
	return "AlwaysFalse"
}

// NotCondition negates another condition
type NotCondition struct {
	BaseCondition
	condition Condition
}

// NewNotCondition creates a new NOT condition
func NewNotCondition(condition Condition) *NotCondition {
	return &NotCondition{
		BaseCondition: BaseCondition{timeout: condition.Timeout()},
		condition:     condition,
	}
}

// Check implements Condition interface
func (n *NotCondition) Check(ctx context.Context) (bool, error) {
	satisfied, err := n.condition.Check(ctx)
	if err != nil {
		return false, err
	}
	return !satisfied, nil
}

// String implements Condition interface
func (n *NotCondition) String() string {
	return fmt.Sprintf("NOT(%s)", n.condition.String())
}

// WaitFor waits for a condition to be satisfied with timeout
func WaitFor(ctx context.Context, condition Condition) error {
	ctx, cancel := context.WithTimeout(ctx, condition.Timeout())
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for condition: %s", condition.String())
		case <-ticker.C:
			satisfied, err := condition.Check(ctx)
			if err != nil {
				return fmt.Errorf("condition check failed: %w", err)
			}
			if satisfied {
				return nil
			}
		}
	}
}

// WaitForWithRetry waits for a condition with configurable retry logic
func WaitForWithRetry(ctx context.Context, condition Condition, maxRetries int, retryDelay time.Duration) error {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelay)
		}

		ctxWithTimeout, cancel := context.WithTimeout(ctx, condition.Timeout())
		err := WaitFor(ctxWithTimeout, condition)
		cancel()

		if err == nil {
			return nil
		}

		lastErr = err

		// Check if context was cancelled (don't retry)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	return fmt.Errorf("max retries (%d) exceeded: %w", maxRetries, lastErr)
}

// WaitResult contains the result of a wait operation
type WaitResult struct {
	Success    bool
	Duration   time.Duration
	Error      error
	RetryCount int
}

// WaitForWithResult waits for a condition and returns detailed results
func WaitForWithResult(ctx context.Context, condition Condition, maxRetries int, retryDelay time.Duration) WaitResult {
	start := time.Now()

	err := WaitForWithRetry(ctx, condition, maxRetries, retryDelay)

	return WaitResult{
		Success:    err == nil,
		Duration:   time.Since(start),
		Error:      err,
		RetryCount: maxRetries, // This would need to be tracked more precisely
	}
}
