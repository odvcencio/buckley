package policy

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Engine evaluates tool calls against the active policy
type Engine struct {
	mu     sync.RWMutex
	policy *Policy
	store  Store
}

// Store interface for policy persistence
type Store interface {
	GetActivePolicy() (*Policy, error)
	SavePolicy(policy *Policy) error
	CreatePendingApproval(approval *PendingApproval) error
	GetPendingApproval(id string) (*PendingApproval, error)
	UpdatePendingApproval(approval *PendingApproval) error
	ListPendingApprovals(sessionID string) ([]*PendingApproval, error)
	ExpirePendingApprovals() (int, error)
	LogToolExecution(entry *AuditEntry) error
	GetAuditLog(sessionID string, limit int) ([]*AuditEntry, error)
}

// NewEngine creates a new policy engine
func NewEngine(store Store) *Engine {
	return &Engine{
		store: store,
	}
}

// LoadPolicy loads the active policy from the store
func (e *Engine) LoadPolicy() error {
	if e.store == nil {
		e.policy = DefaultPolicy()
		return nil
	}

	policy, err := e.store.GetActivePolicy()
	if err != nil {
		return err
	}

	if policy == nil {
		e.policy = DefaultPolicy()
		return nil
	}

	e.mu.Lock()
	e.policy = policy
	e.mu.Unlock()
	return nil
}

// Evaluate evaluates a tool call against the policy
func (e *Engine) Evaluate(call ToolCall) EvaluationResult {
	e.mu.RLock()
	policy := e.policy
	e.mu.RUnlock()

	if policy == nil {
		policy = DefaultPolicy()
	}

	result := EvaluationResult{
		RiskReasons: []string{},
		ExpiresAt:   time.Now().Add(5 * time.Minute), // Default 5 min expiry
	}

	// Determine tool category if not set
	if call.Category == "" {
		call.Category = categorizeToolCall(call)
	}

	categoryDecision := ActionContext // Default to context-based evaluation
	categoryMatched := false

	// Step 1: Check category rules
	if catRule, ok := policy.Config.Categories[string(call.Category)]; ok {
		categoryMatched = true
		// Check exceptions first
		if matchesException(call, catRule.Exceptions) {
			// Exception matched - invert the action
			if catRule.Action == ActionApprove {
				categoryDecision = ActionAuto
				result.MatchedRule = "category_exception:" + string(call.Category)
			}
		} else {
			// No exception matched - use category action
			categoryDecision = catRule.Action
			if result.MatchedRule == "" {
				result.MatchedRule = "category:" + string(call.Category)
			}
		}

		// Reject immediately
		if categoryDecision == ActionReject {
			result.Decision = ActionReject
			return result
		}
	}

	// Step 2: Calculate risk score (ALWAYS, even if category matched)
	riskScore := 0
	for _, rule := range policy.Config.RiskRules {
		if matchesCondition(call, rule.Condition) {
			riskScore += rule.Score
			result.RiskReasons = append(result.RiskReasons, rule.Condition)

			// If rule has explicit action "approve", it overrides category auto
			if rule.Action == ActionApprove {
				result.RequiresApproval = true
				result.MatchedRule = "risk_rule:" + rule.Condition
			}
		}
	}
	result.RiskScore = riskScore

	// Step 3: Apply time window threshold
	threshold := getThresholdForTime(policy.Config.TimeWindows)

	// Decision logic:
	// 1. If any risk rule explicitly required approval, require it
	// 2. If risk score >= threshold, require approval
	// 3. If category says approve (and no exception matched), require approval
	// 4. Otherwise, auto-approve

	if result.RequiresApproval {
		// Risk rule explicitly required approval
		result.Decision = ActionApprove
	} else if threshold > 0 && riskScore >= threshold {
		// Risk score exceeds threshold
		result.RequiresApproval = true
		result.Decision = ActionApprove
		if result.MatchedRule == "" || strings.HasPrefix(result.MatchedRule, "category") {
			result.MatchedRule = "risk_threshold"
		}
	} else if categoryMatched && categoryDecision == ActionApprove {
		// Category requires approval
		result.RequiresApproval = true
		result.Decision = ActionApprove
	} else {
		// Auto-approve
		result.Decision = ActionAuto
		if result.MatchedRule == "" {
			result.MatchedRule = "under_threshold"
		}
	}

	// Set expiry from policy defaults
	if policy.Config.Defaults.ApprovalExpiry > 0 {
		result.ExpiresAt = time.Now().Add(policy.Config.Defaults.ApprovalExpiry)
	}

	return result
}

// categorizeToolCall determines the category of a tool call
func categorizeToolCall(call ToolCall) ToolCategory {
	name := strings.ToLower(call.Name)

	// File operations
	if strings.Contains(name, "read") || strings.Contains(name, "cat") || strings.Contains(name, "view") {
		return CategoryFileRead
	}
	if strings.Contains(name, "write") || strings.Contains(name, "edit") || strings.Contains(name, "create") {
		return CategoryFileWrite
	}

	// Shell commands
	if strings.Contains(name, "shell") || strings.Contains(name, "bash") || strings.Contains(name, "exec") || name == "run_shell" {
		return CategoryShell
	}

	// Search operations
	if strings.Contains(name, "search") || strings.Contains(name, "grep") || strings.Contains(name, "find") || strings.Contains(name, "glob") {
		return CategorySearch
	}

	// Git operations
	if strings.Contains(name, "git") {
		return CategoryGit
	}

	// Network operations
	if strings.Contains(name, "fetch") || strings.Contains(name, "http") || strings.Contains(name, "curl") || strings.Contains(name, "request") {
		return CategoryNetwork
	}

	return CategoryUnknown
}

// matchesException checks if a tool call matches any exception
func matchesException(call ToolCall, exceptions []Exception) bool {
	for _, exc := range exceptions {
		if exc.Pattern != "" {
			// File path pattern matching
			if path, ok := call.Input["path"].(string); ok {
				if matchPathPattern(exc.Pattern, path) {
					return true
				}
			}
			if filePath, ok := call.Input["file_path"].(string); ok {
				if matchPathPattern(exc.Pattern, filePath) {
					return true
				}
			}
		}
		if exc.Command != "" {
			// Command pattern matching
			if cmd, ok := call.Input["command"].(string); ok {
				if matchGlob(exc.Command, cmd) {
					return true
				}
			}
		}
	}
	return false
}

// matchPathPattern matches a path against a pattern
// Supports patterns like "*.log", "/tmp/*", etc.
func matchPathPattern(pattern, path string) bool {
	// Check basename match first (for patterns like "*.log")
	if matched, _ := filepath.Match(pattern, filepath.Base(path)); matched {
		return true
	}

	// Check if path starts with directory pattern (for patterns like "/tmp/*")
	if strings.HasSuffix(pattern, "/*") {
		dir := strings.TrimSuffix(pattern, "/*")
		if strings.HasPrefix(path, dir+"/") {
			return true
		}
	}

	// Full path match
	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}

	return false
}

// matchesCondition checks if a tool call matches a risk condition
func matchesCondition(call ToolCall, condition string) bool {
	inputJSON, _ := json.Marshal(call.Input)
	inputStr := string(inputJSON)

	switch RiskCondition(condition) {
	case RiskTouchesSecrets:
		secretPatterns := []string{
			"\\.env", "secret", "credential", "password", "api.?key",
			"token", "private.?key", "auth", "\\.pem", "\\.key",
		}
		for _, pattern := range secretPatterns {
			if matched, _ := regexp.MatchString("(?i)"+pattern, inputStr); matched {
				return true
			}
		}

	case RiskDestructive:
		destructivePatterns := []string{
			"rm\\s+-rf", "rm\\s+-r", "rmdir", "unlink",
			"DROP\\s+TABLE", "DROP\\s+DATABASE", "DELETE\\s+FROM",
			"truncate", "reset\\s+--hard", "force",
			"--force", "-f\\s+", "\\s+-f$",
		}
		for _, pattern := range destructivePatterns {
			if matched, _ := regexp.MatchString("(?i)"+pattern, inputStr); matched {
				return true
			}
		}

	case RiskExternalNetwork:
		// Check for external URLs or network commands
		if cmd, ok := call.Input["command"].(string); ok {
			if strings.Contains(cmd, "curl") || strings.Contains(cmd, "wget") ||
				strings.Contains(cmd, "http://") || strings.Contains(cmd, "https://") {
				// Exclude localhost
				if !strings.Contains(cmd, "localhost") && !strings.Contains(cmd, "127.0.0.1") {
					return true
				}
			}
		}
		if url, ok := call.Input["url"].(string); ok {
			if !strings.Contains(url, "localhost") && !strings.Contains(url, "127.0.0.1") {
				return true
			}
		}

	case RiskModifiesGit:
		gitMutatingCmds := []string{
			"git\\s+commit", "git\\s+push", "git\\s+rebase",
			"git\\s+reset", "git\\s+merge", "git\\s+checkout\\s+-b",
			"git\\s+branch\\s+-[dD]", "git\\s+tag",
		}
		for _, pattern := range gitMutatingCmds {
			if matched, _ := regexp.MatchString("(?i)"+pattern, inputStr); matched {
				return true
			}
		}

	case RiskWritesConfig:
		configPatterns := []string{
			"\\.yaml$", "\\.yml$", "\\.json$", "\\.toml$",
			"config\\.", "\\.config", "settings\\.",
		}
		for _, pattern := range configPatterns {
			if matched, _ := regexp.MatchString("(?i)"+pattern, inputStr); matched {
				// Only if it's a write operation
				if call.Category == CategoryFileWrite || call.Category == CategoryShell {
					return true
				}
			}
		}

	case RiskInstallsPackage:
		installPatterns := []string{
			"npm\\s+install", "npm\\s+i\\s", "yarn\\s+add",
			"go\\s+get", "go\\s+install", "pip\\s+install",
			"apt\\s+install", "apt-get\\s+install", "brew\\s+install",
		}
		for _, pattern := range installPatterns {
			if matched, _ := regexp.MatchString("(?i)"+pattern, inputStr); matched {
				return true
			}
		}
	}

	return false
}

// matchGlob matches a glob pattern against a string
func matchGlob(pattern, s string) bool {
	// Convert glob to regex
	regexPattern := "^" + regexp.QuoteMeta(pattern) + "$"
	regexPattern = strings.ReplaceAll(regexPattern, "\\*", ".*")
	regexPattern = strings.ReplaceAll(regexPattern, "\\?", ".")

	matched, _ := regexp.MatchString(regexPattern, s)
	return matched
}

// getThresholdForTime returns the risk threshold for the current time
func getThresholdForTime(windows map[string]TimeWindow) int {
	now := time.Now()
	if len(windows) == 0 {
		return 50
	}

	names := make([]string, 0, len(windows))
	for name := range windows {
		names = append(names, name)
	}
	sort.Strings(names)

	dayName := strings.ToLower(now.Weekday().String())
	for _, name := range names {
		window := windows[name]
		if len(window.Days) == 0 {
			continue
		}
		for _, d := range window.Days {
			if strings.ToLower(d) == dayName {
				return window.Threshold
			}
		}
	}

	for _, name := range names {
		window := windows[name]
		if window.Hours == "" {
			continue
		}

		loc := time.Local
		if window.Timezone != "" {
			if l, err := time.LoadLocation(window.Timezone); err == nil {
				loc = l
			}
		}

		localNow := now.In(loc)
		if inTimeRange(localNow, window.Hours) {
			return window.Threshold
		}
	}

	if window, ok := windows["default"]; ok && len(window.Days) == 0 && window.Hours == "" {
		return window.Threshold
	}

	// Default threshold if no window matches
	return 50
}

// inTimeRange checks if the current time is within an hour range like "09:00-18:00"
func inTimeRange(t time.Time, rangeStr string) bool {
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return false
	}

	startParts := strings.Split(strings.TrimSpace(parts[0]), ":")
	endParts := strings.Split(strings.TrimSpace(parts[1]), ":")

	if len(startParts) != 2 || len(endParts) != 2 {
		return false
	}

	var startHour, startMin, endHour, endMin int
	if _, err := parseTime(startParts, &startHour, &startMin); err != nil {
		return false
	}
	if _, err := parseTime(endParts, &endHour, &endMin); err != nil {
		return false
	}

	currentMinutes := t.Hour()*60 + t.Minute()
	startMinutes := startHour*60 + startMin
	endMinutes := endHour*60 + endMin

	// Handle overnight ranges (e.g., "22:00-06:00")
	if startMinutes > endMinutes {
		return currentMinutes >= startMinutes || currentMinutes < endMinutes
	}

	return currentMinutes >= startMinutes && currentMinutes < endMinutes
}

func parseTime(parts []string, hour, min *int) (bool, error) {
	if len(parts) != 2 {
		return false, nil
	}
	var h, m int
	// Parse hour
	for _, c := range parts[0] {
		if c >= '0' && c <= '9' {
			h = h*10 + int(c-'0')
		}
	}
	// Parse minute
	for _, c := range parts[1] {
		if c >= '0' && c <= '9' {
			m = m*10 + int(c-'0')
		}
	}
	*hour = h
	*min = m
	return true, nil
}

// DefaultPolicy returns the default policy configuration
func DefaultPolicy() *Policy {
	return &Policy{
		Name:     "default",
		IsActive: true,
		Config: Config{
			Categories: map[string]CategoryRule{
				string(CategoryFileRead): {Action: ActionAuto},
				string(CategorySearch):   {Action: ActionAuto},
				string(CategoryFileWrite): {
					Action: ActionApprove,
					Exceptions: []Exception{
						{Pattern: "*.log"},
						{Pattern: "/tmp/*"},
					},
				},
				string(CategoryShell): {
					Action: ActionApprove,
					Exceptions: []Exception{
						{Command: "go test *"},
						{Command: "go build *"},
						{Command: "npm run *"},
						{Command: "npm test *"},
						{Command: "git status"},
						{Command: "git diff *"},
						{Command: "git log *"},
						{Command: "ls *"},
						{Command: "cat *"},
					},
				},
				string(CategoryGit): {Action: ActionContext},
			},
			RiskRules: []RiskRule{
				{Condition: string(RiskTouchesSecrets), Score: 100, Action: ActionApprove},
				{Condition: string(RiskDestructive), Score: 100, Action: ActionApprove},
				{Condition: string(RiskExternalNetwork), Score: 50},
				{Condition: string(RiskModifiesGit), Score: 30},
				{Condition: string(RiskWritesConfig), Score: 20},
				{Condition: string(RiskInstallsPackage), Score: 20},
			},
			TimeWindows: map[string]TimeWindow{
				"active": {
					Hours:     "09:00-18:00",
					Threshold: 50,
				},
				"afk": {
					Hours:     "18:00-09:00",
					Threshold: 10,
				},
				"weekend": {
					Days:      []string{"saturday", "sunday"},
					Threshold: 0, // Approve everything on weekends
				},
			},
			Defaults: Defaults{
				Action:         ActionContext,
				ApprovalExpiry: 5 * time.Minute,
				MaxPending:     10,
			},
		},
	}
}

// GetPolicy returns the current policy
func (e *Engine) GetPolicy() *Policy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.policy == nil {
		return DefaultPolicy()
	}
	return e.policy
}

// SetPolicy updates the active policy
func (e *Engine) SetPolicy(policy *Policy) error {
	e.mu.Lock()
	e.policy = policy
	e.mu.Unlock()

	if e.store != nil {
		return e.store.SavePolicy(policy)
	}
	return nil
}
