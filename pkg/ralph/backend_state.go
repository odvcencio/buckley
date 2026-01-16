// pkg/ralph/backend_state.go
package ralph

import "time"

// BackendStatus represents the availability state of a backend.
type BackendStatus string

const (
	BackendActive   BackendStatus = "active"
	BackendParked   BackendStatus = "parked"
	BackendDisabled BackendStatus = "disabled"
)

type backendState struct {
	status        BackendStatus
	parkedUntil   time.Time
	lastError     string
	errorCount    int
	consecErrors  int
	totalCost     float64
	totalTokens   int
	windowStart   time.Time
	requestsCount int
	costInWindow  float64
	windowReset   time.Time
	lastModel     string
	lastUsed      time.Time
}

// ErrAllBackendsParked indicates every backend is parked and provides the next wake time.
type ErrAllBackendsParked struct {
	NextAvailable time.Time
}

func (e ErrAllBackendsParked) Error() string {
	if e.NextAvailable.IsZero() {
		return "all backends are parked"
	}
	return "all backends are parked until " + e.NextAvailable.Format(time.RFC3339)
}
