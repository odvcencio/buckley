package security

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SecretsAnalyzer detects exposed secrets in code
type SecretsAnalyzer struct {
	config           Config
	secretPatterns   []secretPattern
	entropyThreshold float64
}

// secretPattern represents patterns to detect secrets
type secretPattern struct {
	pattern     string
	category    Category
	severity    Severity
	title       string
	description string
	remediation string
	validator   func(match string) bool // Optional validator function
}

// NewSecretsAnalyzer creates a new secrets analyzer
func NewSecretsAnalyzer(config Config) *SecretsAnalyzer {
	analyzer := &SecretsAnalyzer{
		config:           config,
		entropyThreshold: 3.5, // Adjust based on false positive rate
		secretPatterns: []secretPattern{
			// API Keys
			{
				pattern:     `(?i)(api[_-]?key|apikey)\s*[:=]\s*["'][a-z0-9]{20,}["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Hardcoded API key",
				description: "Potential API key found in code",
				remediation: "Store API keys in environment variables or secret management service",
			},
			{
				pattern:     `(?i)(x-api-key|x_api_key)\s*[:=]\s*["'][a-z0-9_-]{20,}["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Hardcoded X-API-Key",
				description: "Potential API key found in code",
				remediation: "Store API keys in environment variables",
			},

			// AWS credentials
			{
				pattern:     `(?i)AKIA[0-9A-Z]{16}`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "AWS Access Key ID",
				description: "AWS Access Key ID found",
				remediation: "Store AWS credentials in environment variables or IAM roles",
			},
			{
				pattern:     `(?i)(aws[_-]?secret[_-]?access[_-]?key|aws_secret_access_key)\s*[:=]\s*["'][a-z0-9/+]{40}["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "AWS Secret Access Key",
				description: "AWS Secret Access Key found",
				remediation: "Store AWS credentials securely, never hardcode",
			},

			// Azure credentials
			{
				pattern:     `(?i)(AccountKey|account[_-]?key)\s*[:=]\s*["'][a-z0-9/+]{40,}["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Azure Account Key",
				description: "Azure Storage Account Key found",
				remediation: "Store Azure credentials in environment variables",
			},

			// GCP credentials
			{
				pattern:     `(?i)(gcp|google|gcloud)[_-]?(api[_-]?key|key)\s*[:=]\s*["'][a-z0-9_-]{20,}["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "GCP API Key",
				description: "Google Cloud API Key found",
				remediation: "Store GCP credentials in environment variables",
			},

			// Database credentials
			{
				pattern:     `(?i)(db[_-]?|database[_-]?|postgres[_-]?|mysql[_-]?)?pass(word)?\s*[:=]\s*["'][^"'\s]{6,}["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Database password",
				description: "Database password found in code",
				remediation: "Store database credentials in environment variables",
			},
			{
				pattern:     `(?i)(connection[_-]?string|conn[_-]?string)\s*[:=]\s*["'][^"']*(password|pwd)=[^;& ]+[^"']*["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Database connection string with password",
				description: "Database connection string contains password",
				remediation: "Use environment variables or secret management for connection strings",
			},

			// Private keys
			{
				pattern:     `(?i)-----BEGIN\s+(RSA\s+)?PRIVATE\s+KEY-----`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Private key",
				description: "Private cryptographic key found",
				remediation: "Remove private keys from source code, store securely",
			},
			{
				pattern:     `(?i)(ssh[_-]?private[_-]?key|private[_-]?key)\s*[:=]\s*["'][a-z0-9/+\n]{100,}["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "SSH Private Key",
				description: "SSH private key found in code",
				remediation: "Remove SSH keys from source code immediately",
			},

			// Generic secrets
			{
				pattern:     `(?i)(secret|token|private[_-]?key|auth[_-]?token)\s*[:=]\s*["'][a-z0-9_-]{20,}["']`,
				category:    CategorySecrets,
				severity:    SeverityHigh,
				title:       "Generic secret",
				description: "Potential secret token found",
				remediation: "Store secrets in environment variables or secret management",
			},

			// OAuth tokens
			{
				pattern:     `(?i)(oauth[_-]?token|oauthtoken)\s*[:=]\s*["'][a-z0-9_-]{20,}["']`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "OAuth Token",
				description: "OAuth token found in code",
				remediation: "Store OAuth tokens securely, never hardcode",
			},

			// JWT tokens (though these are usually short-lived)
			{
				pattern:     `(?i)eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+`,
				category:    CategorySecrets,
				severity:    SeverityHigh,
				title:       "JWT Token",
				description: "JWT token found in code",
				remediation: "JWTs should not be hardcoded; use secure token storage",
			},

			// Slack tokens
			{
				pattern:     `xox[baprs]-[0-9]{12}-[0-9]{12}-[0-9]{12}-[a-z0-9]{32}`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Slack Token",
				description: "Slack bot or user token found",
				remediation: "Store Slack tokens in environment variables",
			},

			// GitHub tokens
			{
				pattern:     `(?i)(github[_-]?token|ghp_|gho_|ghu_|ghs_|ghr_)[a-z0-9]{20,}`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "GitHub Token",
				description: "GitHub personal access token found",
				remediation: "Store GitHub tokens in environment variables",
			},

			// Stripe keys
			{
				pattern:     `(?i)sk_live_[0-9a-zA-Z]{24}`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Stripe Live Secret Key",
				description: "Stripe live secret key found",
				remediation: "Store Stripe keys in environment variables",
			},
			{
				pattern:     `(?i)pk_live_[0-9a-zA-Z]{24}`,
				category:    CategorySecrets,
				severity:    SeverityMedium,
				title:       "Stripe Live Publishable Key",
				description: "Stripe live publishable key found",
				remediation: "Publishable keys are less sensitive but should still be in config",
			},

			// Twilio credentials
			{
				pattern:     `(?i)AC[a-z0-9]{32}`,
				category:    CategorySecrets,
				severity:    SeverityHigh,
				title:       "Twilio Account SID",
				description: "Twilio Account SID found",
				remediation: "Store Twilio credentials in environment variables",
			},

			// Mailgun credentials
			{
				pattern:     `(?i)key-[0-9a-z]{32}`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Mailgun API Key",
				description: "Mailgun API key found",
				remediation: "Store Mailgun API keys in environment variables",
			},

			// SendGrid API Key
			{
				pattern:     `SG\.[a-z0-9_-]{22}\.[a-z0-9_-]{43}`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "SendGrid API Key",
				description: "SendGrid API key found",
				remediation: "Store SendGrid API keys in environment variables",
			},

			// Mailchimp API Key
			{
				pattern:     `(?i)[0-9a-f]{32}-us\d{1,2}`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Mailchimp API Key",
				description: "Mailchimp API key found",
				remediation: "Store Mailchimp API keys in environment variables",
			},

			// Heroku API Key
			{
				pattern:     `(?i)heroku[a-z0-9_-]{36}`,
				category:    CategorySecrets,
				severity:    SeverityCritical,
				title:       "Heroku API Key",
				description: "Heroku API key found",
				remediation: "Store Heroku API keys in environment variables",
			},

			// Cryptographic keys in URLs
			{
				pattern:     `(?i)[?&](key|api[_-]?key|token|auth)=['"]?[a-z0-9_-]{20,}['"]?`,
				category:    CategorySecrets,
				severity:    SeverityHigh,
				title:       "Secret in URL",
				description: "Secret passed in URL parameter (will be logged)",
				remediation: "Use headers or POST body for secrets, not URL parameters",
			},

			// Base64-encoded secrets (high entropy)
			{
				pattern:     `(?i)(secret|token|key)\s*[:=]\s*["']?[a-z0-9+/]{40,}={0,2}["']?`,
				category:    CategorySecrets,
				severity:    SeverityMedium,
				title:       "Base64-encoded secret",
				description: "Possibly base64-encoded secret",
				remediation: "Check if this is a real secret and store securely",
			},
		},
	}

	return analyzer
}

// Name returns the analyzer name
func (a *SecretsAnalyzer) Name() string {
	return "secrets"
}

// Description returns the analyzer description
func (a *SecretsAnalyzer) Description() string {
	return "Detects exposed secrets including API keys, passwords, private keys, and tokens"
}

// Analyze performs secrets analysis
func (a *SecretsAnalyzer) Analyze(path string) (*Result, error) {
	result := NewResult()
	startTime := time.Now()

	// Track visited files to avoid duplicates (like in git history)
	visited := make(map[string]bool)

	// Process all files
	err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !a.shouldAnalyzeFile(filePath) {
			return nil
		}

		// Skip if already visited
		if visited[filePath] {
			return nil
		}
		visited[filePath] = true

		fileFindings, err := a.analyzeFile(filePath)
		if err != nil {
			fmt.Printf("Warning: Failed to analyze %s: %v\n", filePath, err)
			return nil
		}

		result.AddFindings(fileFindings)
		result.FileCount++

		return nil
	})

	if err != nil {
		return nil, err
	}

	// Also scan for high-entropy strings
	highEntropyFindings, err := a.findHighEntropyStrings(path)
	if err != nil {
		fmt.Printf("Warning: Failed to scan for high-entropy strings: %v\n", err)
	} else {
		result.AddFindings(highEntropyFindings)
	}

	result.ScanTime = time.Since(startTime).Milliseconds()

	return result, nil
}

// shouldAnalyzeFile determines if a file should be analyzed
func (a *SecretsAnalyzer) shouldAnalyzeFile(path string) bool {
	for _, excludeDir := range a.config.ExcludeDirs {
		excludeDir = strings.TrimSpace(excludeDir)
		if excludeDir == "" {
			continue
		}
		if strings.Contains(path, string(filepath.Separator)+excludeDir+string(filepath.Separator)) {
			return false
		}
	}

	// Skip obvious non-source files
	ext := strings.ToLower(filepath.Ext(path))
	skipExts := map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".bmp": true, ".ico": true, ".svg": true, ".woff": true,
		".woff2": true, ".ttf": true, ".eot": true,
		".mp3": true, ".mp4": true, ".avi": true, ".mov": true,
		".zip": true, ".tar": true, ".gz": true, ".rar": true,
		".pdf": true, ".doc": true, ".docx": true, ".xls": true,
	}

	if skipExts[ext] {
		return false
	}

	// Skip large files
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.Size() > a.config.MaxFileSize {
		return false
	}

	// Skip test files if configured
	if !a.config.IncludeTests {
		if strings.Contains(path, "_test.") || strings.Contains(path, ".test.") {
			return false
		}
	}

	return true
}

// analyzeFile analyzes a single file for secrets
func (a *SecretsAnalyzer) analyzeFile(path string) ([]SecurityFinding, error) {
	var findings []SecurityFinding

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 1

	for scanner.Scan() {
		line := scanner.Text()

		// Check against known secret patterns
		for _, pattern := range a.secretPatterns {
			matched, matches := a.matchPattern(line, pattern.pattern)
			if matched {
				if pattern.validator == nil || pattern.validator(matches[0]) {
					finding := a.createFinding(path, lineNum, line, pattern, matches)
					if finding != nil {
						findings = append(findings, *finding)
					}
				}
			}
		}

		lineNum++
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return findings, nil
}

// findHighEntropyStrings finds strings with high entropy (potential secrets)
func (a *SecretsAnalyzer) findHighEntropyStrings(rootPath string) ([]SecurityFinding, error) {
	var findings []SecurityFinding

	// Common variable names that might contain secrets
	secretVarPatterns := []string{
		`(?i)(secret|token|key|password|passwd|pwd|credential|auth)`,
		`(?i)(api[_-]?|private[_-]?|private|access[_-]?|private)`,
		`(?i)(jwt[_-]?|oauth[_-]?|bearer[_-]?)`,
	}

	quotePatterns := []string{
		`"([^"]{20,})"`, // Double-quoted strings
		`'([^']{20,})'`, // Single-quoted strings
		"`([^`]{20,})`", // Backtick strings
	}

	visited := make(map[string]bool)

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !a.shouldAnalyzeFile(path) {
			return nil
		}

		if visited[path] {
			return nil
		}
		visited[path] = true

		content, err := os.ReadFile(path)
		if err != nil {
			return nil // Skip files we can't read
		}

		lines := strings.Split(string(content), "\n")

		for lineNum, line := range lines {
			// Check if line contains suspicious variable names
			hasSecretVar := false
			for _, varPattern := range secretVarPatterns {
				if matched, _ := regexp.MatchString(varPattern, line); matched {
					hasSecretVar = true
					break
				}
			}

			if !hasSecretVar {
				continue
			}

			// Check for quoted strings
			for _, quotePattern := range quotePatterns {
				re := regexp.MustCompile(quotePattern)
				matches := re.FindAllStringSubmatch(line, -1)

				for _, match := range matches {
					if len(match) > 1 {
						str := match[1]

						// Skip common false positives
						if a.isCommonString(str) || a.isPlaceholder(str) {
							continue
						}

						// Calculate entropy
						entropy := a.calculateEntropy(str)

						if entropy > a.entropyThreshold && len(str) >= 16 {
							// Additional checks to reduce false positives
							if a.looksLikeSecret(str) {
								finding := &SecurityFinding{
									Category:    CategorySecrets,
									Severity:    SeverityHigh,
									Title:       "High-entropy string (potential secret)",
									Description: fmt.Sprintf("High-entropy string found (entropy: %.2f)", entropy),
									FilePath:    path,
									LineNumber:  lineNum + 1,
									LineContent: strings.TrimSpace(line),
									Remediation: "Verify if this is a secret and store securely if it is",
									Confidence:  0.6, // Lower confidence for entropy-based detection
								}
								findings = append(findings, *finding)
							}
						}
					}
				}
			}
		}

		return nil
	})

	return findings, err
}

// calculateEntropy calculates Shannon entropy of a string
func (a *SecretsAnalyzer) calculateEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}

	// Count character frequencies
	freq := make(map[rune]int)
	for _, char := range s {
		freq[char]++
	}

	// Calculate entropy
	entropy := 0.0
	lenFloat := float64(len(s))

	for _, count := range freq {
		p := float64(count) / lenFloat
		entropy -= p * math.Log2(p)
	}

	return entropy
}

// looksLikeSecret performs additional checks on high-entropy strings
func (a *SecretsAnalyzer) looksLikeSecret(s string) bool {
	// Skip if it's just hex encoding of common data
	if a.isHexEncodedData(s) {
		return false
	}

	// Skip if it's a base64 of common data
	if a.isBase64OfCommonData(s) {
		return false
	}

	// Check character distribution
	if a.hasUniformCharDistribution(s) {
		return true
	}

	// Check for mixed alphanumeric with symbols
	if a.hasMixedAlphanumericSymbols(s) {
		return true
	}

	return false
}

// isCommonString checks if string is a common false positive
func (a *SecretsAnalyzer) isCommonString(s string) bool {
	commonPatterns := []string{
		"password", "secret", "key", "token",
		"example", "localhost", "127.0.0.1",
		"test", "mock", "stub", "dummy",
		"true", "false", "null", "undefined",
		"application/json", "text/plain", "text/html",
		"function", "class", "interface", "extends",
		"import ", "export ", "require(", "from ",
		" SELECT ", " INSERT ", " UPDATE ", " DELETE ",
	}

	sLower := strings.ToLower(s)
	for _, pattern := range commonPatterns {
		if strings.Contains(sLower, pattern) {
			return true
		}
	}

	// Skip file paths
	if strings.Contains(s, "/") || strings.Contains(s, "\\") || strings.Contains(s, ".") {
		return true
	}

	return false
}

// isPlaceholder checks if string is a placeholder
func (a *SecretsAnalyzer) isPlaceholder(s string) bool {
	placeholders := []string{
		"YOUR_API_KEY", "YOUR_SECRET", "YOUR_TOKEN",
		"PLACEHOLDER", "EXAMPLE", "CHANGE_ME",
		"INSERT_", "REPLACE_", "CONFIGURE_",
		"abc123", "xxx", "123456", "password123",
	}

	sUpper := strings.ToUpper(s)
	for _, ph := range placeholders {
		if strings.Contains(sUpper, ph) {
			return true
		}
	}

	return false
}

// isHexEncodedData checks if string might be hex-encoded common data
func (a *SecretsAnalyzer) isHexEncodedData(s string) bool {
	return regexp.MustCompile(`^[0-9a-fA-F]+$`).MatchString(s)
}

// isBase64OfCommonData checks if base64 decodes to common data
func (a *SecretsAnalyzer) isBase64OfCommonData(s string) bool {
	// Check if it looks like base64
	if !regexp.MustCompile(`^[a-zA-Z0-9/+]+={0,2}$`).MatchString(s) {
		return false
	}

	// Try to decode
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return false
	}

	decodedStr := string(decoded)

	// Check if decoded data is common
	commonDataPatterns := []string{
		`{"`, `"}`, `[]`, `{}`, `:`, `;`, ` `, `\n`, `\t`,
		`class`, `function`, `var`, `let`, `const`, `import`,
		`SELECT`, `INSERT`, `UPDATE`, `DELETE`, `FROM`, `WHERE`,
	}

	for _, pattern := range commonDataPatterns {
		if strings.Contains(decodedStr, pattern) {
			return true
		}
	}

	return false
}

// hasUniformCharDistribution checks if string has uniform character distribution
func (a *SecretsAnalyzer) hasUniformCharDistribution(s string) bool {
	if len(s) < 20 {
		return false
	}

	// Count unique characters
	unique := make(map[rune]bool)
	for _, char := range s {
		unique[char] = true
	}

	uniqueRatio := float64(len(unique)) / float64(len(s))

	// Secret keys typically have high unique character ratio
	return uniqueRatio > 0.7
}

// hasMixedAlphanumericSymbols checks for mix of character types
func (a *SecretsAnalyzer) hasMixedAlphanumericSymbols(s string) bool {
	hasLower := false
	hasUpper := false
	hasDigit := false
	hasSymbol := false

	for _, char := range s {
		switch {
		case char >= 'a' && char <= 'z':
			hasLower = true
		case char >= 'A' && char <= 'Z':
			hasUpper = true
		case char >= '0' && char <= '9':
			hasDigit = true
		case char == '-' || char == '_' || char == '+' || char == '/' || char == '=':
			hasSymbol = true
		default:
			// Other symbols
			if char < ' ' || char > '~' {
				return false // Control characters or non-ASCII
			}
			hasSymbol = true
		}
	}

	// Secrets typically have mix of at least 3 types
	types := 0
	if hasLower {
		types++
	}
	if hasUpper {
		types++
	}
	if hasDigit {
		types++
	}
	if hasSymbol {
		types++
	}

	return types >= 3
}

// matchPattern checks if a line matches a pattern
func (a *SecretsAnalyzer) matchPattern(line, pattern string) (bool, []string) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, nil
	}

	matches := re.FindStringSubmatch(line)
	return matches != nil, matches
}

// createFinding creates a security finding from a pattern match
func (a *SecretsAnalyzer) createFinding(path string, lineNum int, lineContent string, pattern secretPattern, matches []string) *SecurityFinding {
	description := pattern.description
	remediation := pattern.remediation

	// Enhance description with match details if available
	if len(matches) > 0 {
		description = fmt.Sprintf("%s: '%s...'", pattern.description, matches[0][:min(len(matches[0]), 20)])
	}

	finding := &SecurityFinding{
		Category:    pattern.category,
		Severity:    pattern.severity,
		Title:       pattern.title,
		Description: description,
		FilePath:    path,
		LineNumber:  lineNum,
		LineContent: strings.TrimSpace(lineContent),
		Remediation: remediation,
		Confidence:  0.9, // High confidence for pattern matches
	}

	return finding
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
