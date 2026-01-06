package security

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AuthPatternsAnalyzer detects authentication and authorization vulnerabilities
type AuthPatternsAnalyzer struct {
	config   Config
	patterns []authPattern
}

// authPattern represents authentication/auth patterns to detect
type authPattern struct {
	pattern     string
	category    Category
	severity    Severity
	title       string
	description string
	remediation string
}

// NewAuthPatternsAnalyzer creates a new auth patterns analyzer
func NewAuthPatternsAnalyzer(config Config) *AuthPatternsAnalyzer {
	analyzer := &AuthPatternsAnalyzer{
		config: config,
		patterns: []authPattern{
			// Missing Authentication checks
			{
				pattern:     `(?i)func\s+\w+\(.*http\.Request\)\s*\{[^}]*\bdb\.\w+\(`,
				category:    CategoryBrokenAuth,
				severity:    SeverityHigh,
				title:       "Missing authentication check",
				description: "HTTP handler function directly accesses database without authentication",
				remediation: "Add authentication middleware or check before accessing sensitive operations",
			},
			{
				pattern:     `(?i)(r\.Method\s*==\s*"POST"|r\.Method\s*==\s*"PUT"|r\.Method\s*==\s*"DELETE")[^}]*[\s\S]*?\{[\s\S]*?\bdb\.Save\(|\bdb\.Update\(|\bdb\.Delete\(`,
				category:    CategoryBrokenAuth,
				severity:    SeverityHigh,
				title:       "Missing authentication on state-changing operations",
				description: "State-changing operations (POST/PUT/DELETE) without authentication",
				remediation: "Implement authentication and authorization checks",
			},

			// Missing Authorization checks
			{
				pattern:     `(?i)func\s+.*admin|admin.*func\s+\w+\([^)]*\)\s*\{[\s\S]*?\bdb\.`,
				category:    CategoryBrokenAccess,
				severity:    SeverityHigh,
				title:       "Missing authorization check",
				description: "Admin or privileged function without proper authorization",
				remediation: "Implement role-based access control (RBAC) or permission checks",
			},
			{
				pattern:     `(?i)if\s+.*user\.ID\s*==\s*.*userID\s*.*\{[\s\S]*?\bdb\.\w+\(.*userID.*\)`,
				category:    CategoryBrokenAccess,
				severity:    SeverityHigh,
				title:       "Insecure Direct Object Reference (IDOR)",
				description: "Direct use of user ID from request without authorization check",
				remediation: "Verify user has permission to access the requested resource",
			},

			// Weak password handling
			{
				pattern:     `(?i)password\s*=\s*"[^"]*password[^"]*"`,
				category:    CategorySensitiveData,
				severity:    SeverityHigh,
				title:       "Hardcoded password",
				description: "Hardcoded password found in source code",
				remediation: "Remove hardcoded passwords, use environment variables or secure vaults",
			},
			{
				pattern:     `(?i)bcrypt\.Cost\s*=\s*\d+`,
				category:    CategorySensitiveData,
				severity:    SeverityMedium,
				title:       "Weak bcrypt cost factor",
				description: "Bcrypt cost factor may be too low for security",
				remediation: "Use bcrypt.DefaultCost or higher (minimum cost: 10)",
			},

			// Session Management issues
			{
				pattern:     `(?i)session\.Save\(\s*\)`,
				category:    CategoryBrokenAuth,
				severity:    SeverityMedium,
				title:       "Unconditional session save",
				description: "Session saved without checking authentication state",
				remediation: "Only save session after successful authentication",
			},
			{
				pattern:     `(?i)http\.Only\s*=\s*false`,
				category:    CategorySensitiveData,
				severity:    SeverityMedium,
				title:       "Session cookie without HttpOnly flag",
				description: "Session cookie can be accessed by JavaScript",
				remediation: "Set HttpOnly: true on session cookies",
			},
			{
				pattern:     `(?i)Secure\s*=\s*false`,
				category:    CategorySensitiveData,
				severity:    SeverityHigh,
				title:       "Session cookie without Secure flag",
				description: "Session cookie transmitted over HTTP",
				remediation: "Set Secure: true on session cookies",
			},

			// JWT issues
			{
				pattern:     `(?i)HS256|HS384|HS512.*secret\s*[:=]\s*"[^"]*"`,
				category:    CategorySensitiveData,
				severity:    SeverityCritical,
				title:       "Hardcoded JWT secret",
				description: "JWT signing secret hardcoded in source",
				remediation: "Store JWT secrets in environment variables or secure vaults",
			},
			{
				pattern:     `(?i)jwt\.Parse\(.*\)\s*[^)]*$`,
				category:    CategoryBrokenAuth,
				severity:    SeverityHigh,
				title:       "JWT without signature verification",
				description: "JWT parsed without verifying signature",
				remediation: "Always verify JWT signatures before trusting claims",
			},
			{
				pattern:     `(?i)Expiration\s*:\s*\d+\s*\*\s*time\.\w+`,
				category:    CategoryBrokenAuth,
				severity:    SeverityLow,
				title:       "JWT expiration may be too long",
				description: "JWT expiration time may be excessive",
				remediation: "Use shorter expiration times (e.g., 15 minutes for access tokens)",
			},

			// CORS misconfiguration
			{
				pattern:     `(?i)cors\.\w+\(\s*.*Origins\s*[:=]\s*\[\]string\{\s*"\*"\s*\}`,
				category:    CategorySecurityMisconfig,
				severity:    SeverityMedium,
				title:       "Permissive CORS configuration",
				description: "CORS allows wildcard origin (*)",
				remediation: "Specify specific allowed origins instead of wildcard",
			},
			{
				pattern:     `(?i)Access-Control-Allow-Origin:\s*\*`,
				category:    CategorySecurityMisconfig,
				severity:    SeverityMedium,
				title:       "Permissive CORS header",
				description: "CORS header allows all origins",
				remediation: "Use specific origins and implement proper origin validation",
			},

			// API key handling
			{
				pattern:     `(?i)(api.?key|apiKey|x-api-key).*[:=]\s*"[^"]*"`,
				category:    CategorySensitiveData,
				severity:    SeverityHigh,
				title:       "Hardcoded API key",
				description: "API key hardcoded in source code",
				remediation: "Store API keys in environment variables or secure vaults",
			},

			// OAuth issues
			{
				pattern:     `(?i)OAuth\w*\s+.*secret\s*=\s*"[^"]*"`,
				category:    CategorySensitiveData,
				severity:    SeverityCritical,
				title:       "Hardcoded OAuth secret",
				description: "OAuth secret hardcoded in source code",
				remediation: "Store OAuth secrets securely, never hardcode",
			},

			// Privilege escalation patterns
			{
				pattern:     `(?i)(isAdmin|isSuperuser|role\s*==\s*"admin").*\|\|.*true`,
				category:    CategoryBrokenAccess,
				severity:    SeverityCritical,
				title:       "Privilege escalation backdoor",
				description: "Authentication check can be bypassed",
				remediation: "Remove backdoor authentication logic",
			},
			{
				pattern:     `(?i)role\s*:=\s*"admin"`,
				category:    CategoryBrokenAccess,
				severity:    SeverityHigh,
				title:       "Unconditional admin role assignment",
				description: "User role unconditionally set to admin",
				remediation: "Implement proper role assignment based on authentication",
			},

			// CSRF protection
			{
				pattern:     `(?i)POST[^}]*\{[\s\S]*?(c\.Next\(\)|next\(\))[^}]*\}`,
				category:    CategoryBrokenAuth,
				severity:    SeverityMedium,
				title:       "POST endpoint without CSRF protection",
				description: "State-changing operation may lack CSRF protection",
				remediation: "Implement CSRF tokens or SameSite cookies",
			},

			// Missing rate limiting
			{
				pattern:     `(?i)func.*login|login.*func[^}]*\{[\s\S]*?\}`,
				category:    CategoryBrokenAuth,
				severity:    SeverityMedium,
				title:       "Login endpoint without rate limiting",
				description: "Login function lacks rate limiting protection",
				remediation: "Implement rate limiting to prevent brute force attacks",
			},

			// Weak crypto for auth
			{
				pattern:     `(?i)md5\(|sha1\(|base64\.StdEncoding\.EncodeToString.*password`,
				category:    CategorySensitiveData,
				severity:    SeverityHigh,
				title:       "Weak hashing algorithm for passwords",
				description: "Passwords hashed with weak algorithm",
				remediation: "Use bcrypt, Argon2, or scrypt for password hashing",
			},
		},
	}

	return analyzer
}

// Name returns the analyzer name
func (a *AuthPatternsAnalyzer) Name() string {
	return "auth-patterns"
}

// Description returns the analyzer description
func (a *AuthPatternsAnalyzer) Description() string {
	return "Detects authentication and authorization vulnerabilities including missing auth checks, insecure session management, and privilege escalation"
}

// Analyze performs auth pattern analysis
func (a *AuthPatternsAnalyzer) Analyze(path string) (*Result, error) {
	result := NewResult()
	startTime := time.Now()

	// Process all source files
	err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !a.shouldAnalyzeFile(filePath) {
			return nil
		}

		fileFindings, err := a.analyzeFile(filePath)
		if err != nil {
			fmt.Printf("Warning: Failed to analyze %s: %v\n", filePath, err)
			return nil
		}

		result.AddFindings(fileFindings)
		return nil
	})

	if err != nil {
		return nil, err
	}

	result.ScanTime = time.Since(startTime).Milliseconds()

	return result, nil
}

// shouldAnalyzeFile determines if a file should be analyzed
func (a *AuthPatternsAnalyzer) shouldAnalyzeFile(path string) bool {
	for _, excludeDir := range a.config.ExcludeDirs {
		excludeDir = strings.TrimSpace(excludeDir)
		if excludeDir == "" {
			continue
		}
		if strings.Contains(path, string(filepath.Separator)+excludeDir+string(filepath.Separator)) {
			return false
		}
	}

	// Skip test files unless configured to include them
	if !a.config.IncludeTests {
		if strings.Contains(path, "_test.go") || strings.Contains(path, ".test.") {
			return false
		}
	}

	// Only analyze relevant file types
	ext := strings.ToLower(filepath.Ext(path))
	supportedExts := map[string]bool{
		".go":   true,
		".py":   true,
		".js":   true,
		".ts":   true,
		".php":  true,
		".java": true,
		".rb":   true,
		".cs":   true,
	}

	return supportedExts[ext]
}

// analyzeFile analyzes a single file for auth patterns
func (a *AuthPatternsAnalyzer) analyzeFile(path string) ([]SecurityFinding, error) {
	var findings []SecurityFinding

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(content), "\n")

	// Process line by line patterns
	for i, line := range lines {
		for _, pattern := range a.patterns {
			if pattern.category == CategoryBrokenAuth || pattern.category == CategoryBrokenAccess {
				// Line-based patterns
				matched, matches := a.matchPattern(line, pattern.pattern)
				if matched {
					finding := a.createFinding(pattern, path, i+1, line, matches)
					if finding != nil {
						findings = append(findings, *finding)
					}
				}
			}
		}
	}

	// Multi-line patterns: Check for function-level auth issues
	findings = append(findings, a.analyzeFunctionAuth(path, lines)...)

	return findings, nil
}

// analyzeFunctionAuth analyzes authentication patterns at function level
func (a *AuthPatternsAnalyzer) analyzeFunctionAuth(path string, lines []string) []SecurityFinding {
	var findings []SecurityFinding

	// Look for HTTP handlers without auth checks
	inHandler := false
	handlerStart := 0
	bracketCount := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect handler function
		if !inHandler && a.isHTTPHandler(line) {
			inHandler = true
			handlerStart = i
			bracketCount = strings.Count(line, "{") - strings.Count(line, "}")
			continue
		}

		if inHandler {
			bracketCount += strings.Count(line, "{") - strings.Count(line, "}")

			// End of function
			if bracketCount == 0 && trimmed != "" {
				// Check if handler has auth
				if !a.hasAuthCheck(lines[handlerStart:i+1]) && a.hasSensitiveOperation(lines[handlerStart:i+1]) {
					finding := &SecurityFinding{
						Category:    CategoryBrokenAuth,
						Severity:    SeverityHigh,
						Title:       "HTTP handler without authentication",
						Description: fmt.Sprintf("Handler function from line %d to %d performs sensitive operations without authentication", handlerStart+1, i+1),
						FilePath:    path,
						LineNumber:  handlerStart + 1,
						LineContent: strings.TrimSpace(lines[handlerStart]),
						Remediation: "Add authentication middleware or check before sensitive operations",
						Confidence:  0.7,
					}
					findings = append(findings, *finding)
				}

				inHandler = false
				handlerStart = 0
			}
		}
	}

	return findings
}

// isHTTPHandler checks if a line starts an HTTP handler function
func (a *AuthPatternsAnalyzer) isHTTPHandler(line string) bool {
	patterns := []string{
		"func.*http.ResponseWriter", "func.*\\*http.Request",
		"app\\.get\\(", "app\\.post\\(", "app\\.put\\(", "app\\.delete\\(",
		"router\\.get\\(", "router\\.post\\(", "router\\.put\\(", "router\\.delete\\(",
		"r\\.GET\\(", "r\\.POST\\(", "r\\.PUT\\(", "r\\.DELETE\\(",
	}

	for _, pattern := range patterns {
		if matched, _ := regexp.MatchString(pattern, line); matched {
			return true
		}
	}

	return false
}

// hasAuthCheck checks if a function has authentication checks
func (a *AuthPatternsAnalyzer) hasAuthCheck(lines []string) bool {
	authPatterns := []string{
		"auth", "Auth", "token", "Token", "session", "Session",
		"middleware", "Middleware", "validate", "Validate", "check", "Check",
		"IsAuthenticated", "isAuthenticated", "requireAuth", "require_auth",
		"@authenticated", "@login_required", "authorize", "Authorize",
	}

	text := strings.Join(lines, " ")
	for _, pattern := range authPatterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}

	return false
}

// hasSensitiveOperation checks if a function performs sensitive operations
func (a *AuthPatternsAnalyzer) hasSensitiveOperation(lines []string) bool {
	sensitivePatterns := []string{
		"db\\.", "database\\.", "insert", "Insert", "update", "Update",
		"delete", "Delete", "remove", "Remove", "save", "Save",
		"create", "Create", "modify", "Modify", "password", "Password",
		"email", "Email", "credit", "Credit", "payment", "Payment",
		"transaction", "Transaction", "user", "User", "account", "Account",
		"role", "Role", "permission", "Permission",
	}

	text := strings.Join(lines, " ")
	for _, pattern := range sensitivePatterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}

	return false
}

// matchPattern checks if a line matches a pattern
func (a *AuthPatternsAnalyzer) matchPattern(line, pattern string) (bool, []string) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, nil
	}

	matches := re.FindStringSubmatch(line)
	return matches != nil, matches
}

// createFinding creates a security finding from a pattern match
func (a *AuthPatternsAnalyzer) createFinding(pattern authPattern, filePath string, lineNum int, lineContent string, matches []string) *SecurityFinding {
	description := pattern.description
	remediation := pattern.remediation
	confidence := 0.8

	// Enhance description with match details if available
	if len(matches) > 0 {
		description = fmt.Sprintf("%s: Pattern matched '%s'", pattern.description, matches[0])
	}

	// Adjust confidence for context
	if a.isSensitiveContext(lineContent) {
		confidence = 0.9
	} else if a.isExternalFacing(lineContent) {
		confidence = 0.85
	} else if a.isLikelyTestCode(filePath, lineContent) {
		confidence = 0.3
		pattern.severity = pattern.severity * 0.5 // Reduce severity for test code
	}

	finding := &SecurityFinding{
		Category:    pattern.category,
		Severity:    pattern.severity,
		Title:       pattern.title,
		Description: description,
		FilePath:    filePath,
		LineNumber:  lineNum,
		LineContent: strings.TrimSpace(lineContent),
		Remediation: remediation,
		Confidence:  confidence,
	}

	return finding
}

// isSensitiveContext checks if line is in a sensitive context
func (a *AuthPatternsAnalyzer) isSensitiveContext(line string) bool {
	sensitive := []string{
		"login", "Login", "auth", "Auth", "session", "Session",
		"password", "Password", "credential", "Credential",
		"token", "Token", "jwt", "JWT", "cookie", "Cookie",
	}

	for _, s := range sensitive {
		if strings.Contains(line, s) {
			return true
		}
	}

	return false
}

// isExternalFacing checks if this appears to handle external requests
func (a *AuthPatternsAnalyzer) isExternalFacing(line string) bool {
	external := []string{
		"http", "HTTP", "request", "Request", "r.Method", "w.Write",
		"ResponseWriter", "http.Request", "gin.Context", "echo.Context",
		"@app.route", "@router.", "@app.",
	}

	for _, s := range external {
		if strings.Contains(line, s) {
			return true
		}
	}

	return false
}

// isLikelyTestCode checks if this is likely test code
func (a *AuthPatternsAnalyzer) isLikelyTestCode(filePath, line string) bool {
	// Check file path
	if strings.Contains(filePath, "_test.") || strings.Contains(filePath, ".test.") ||
		strings.Contains(filePath, "test_") || strings.Contains(filePath, "/test/") {
		return true
	}

	// Check content
	if strings.Contains(line, "testing.") || strings.Contains(line, "assert.") ||
		strings.Contains(line, "mock.") || strings.Contains(line, "stub.") {
		return true
	}

	return false
}
