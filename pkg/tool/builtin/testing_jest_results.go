package builtin

import "encoding/json"

func parseJestReport(raw []byte) testReport {
	var root struct {
		Success         *bool `json:"success"`
		WasInterrupted  *bool `json:"wasInterrupted"`
		NumPassedTests  *int  `json:"numPassedTests"`
		NumFailedTests  *int  `json:"numFailedTests"`
		NumPendingTests *int  `json:"numPendingTests"`
		NumTodoTests    *int  `json:"numTodoTests"`
		NumTotalTests   *int  `json:"numTotalTests"`
		TestResults     *[]struct {
			AssertionResults *[]struct {
				Status   string          `json:"status"`
				WouldRun json.RawMessage `json:"wouldRun"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return testReport{}
	}
	if root.Success == nil || root.WasInterrupted == nil ||
		root.NumPassedTests == nil || root.NumFailedTests == nil ||
		root.NumPendingTests == nil || root.NumTodoTests == nil ||
		root.NumTotalTests == nil || root.TestResults == nil {
		return testReport{}
	}
	if *root.WasInterrupted || *root.NumPassedTests < 0 || *root.NumFailedTests < 0 ||
		*root.NumPendingTests < 0 || *root.NumTodoTests < 0 || *root.NumTotalTests < 0 {
		return testReport{}
	}
	var passed, failed, skipped int
	for _, tr := range *root.TestResults {
		if tr.AssertionResults == nil {
			return testReport{}
		}
		for _, a := range *tr.AssertionResults {
			if len(a.WouldRun) != 0 {
				return testReport{}
			}
			switch a.Status {
			case "passed":
				passed++
			case "failed":
				failed++
			case "pending", "skipped", "disabled", "todo":
				skipped++
			default:
				return testReport{}
			}
		}
	}
	if passed != *root.NumPassedTests || failed != *root.NumFailedTests ||
		skipped != *root.NumPendingTests+*root.NumTodoTests ||
		passed+failed+skipped != *root.NumTotalTests {
		return testReport{}
	}
	r := testReport{passed: passed, failed: failed, skipped: skipped, complete: true}
	if !*root.Success {
		r.verificationError = "jest reported an unsuccessful test run"
	}
	return r
}
