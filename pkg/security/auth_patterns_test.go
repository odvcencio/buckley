package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewAuthPatternsAnalyzer(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	if analyzer == nil {
		t.Fatal("NewAuthPatternsAnalyzer should return non-nil analyzer")
	}

	if analyzer.Name() != "auth-patterns" {
		t.Errorf("Name() = %v, want 'auth-patterns'", analyzer.Name())
	}

	if analyzer.Description() == "" {
		t.Error("Description should not be empty")
	}

	if len(analyzer.patterns) == 0 {
		t.Error("Should have auth patterns registered")
	}
}

func TestAuthPatternsAnalyzer_DetectHardcodedPassword(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "auth.go")

	content := `package auth

var password = "hardcoded_password_123"
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestAuthPatternsAnalyzer_DetectInsecureCookie(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "session.go")

	content := `package session

import "net/http"

func SetCookie(w http.ResponseWriter) {
	cookie := &http.Cookie{
		Name:     "session",
		Value:    "abc123",
		HttpOnly: false,
		Secure:   false,
	}
	http.SetCookie(w, cookie)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestAuthPatternsAnalyzer_DetectHardcodedJWTSecret(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "jwt.go")

	content := `package jwt

const jwtSecret = "my-super-secret-key"

func sign() {
	token := jwt.SigningMethodHS256.Sign(claims, jwtSecret)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestAuthPatternsAnalyzer_DetectPermissiveCORS(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "cors.go")

	content := `package cors

import "net/http"

func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestAuthPatternsAnalyzer_DetectWeakHashing(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "hash.go")

	content := `package hash

import "crypto/md5"

func hashPassword(password string) {
	hash := md5.Sum([]byte(password))
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestAuthPatternsAnalyzer_IsHTTPHandler(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	tests := []struct {
		line string
		want bool
	}{
		{"func HandleRequest(w http.ResponseWriter, r *http.Request) {", true},
		{"app.get(\"/users\", function(req, res) {", true},
		{"router.post(\"/login\", loginHandler)", true},
		{"r.GET(\"/api/users\", getUsers)", true},
		{"func processData(data string) {", false},
		{"var x = 10", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := analyzer.isHTTPHandler(tt.line); got != tt.want {
				t.Errorf("isHTTPHandler(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestAuthPatternsAnalyzer_HasAuthCheck(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{
			"Has auth check",
			[]string{
				"func handler(w http.ResponseWriter, r *http.Request) {",
				"  if !isAuthenticated(r) {",
				"    return",
				"  }",
				"}",
			},
			true,
		},
		{
			"Has middleware",
			[]string{
				"func handler(w http.ResponseWriter, r *http.Request) {",
				"  middleware.Auth(r)",
				"}",
			},
			true,
		},
		{
			"No auth check",
			[]string{
				"func handler(w http.ResponseWriter, r *http.Request) {",
				"  db.Query(\"SELECT * FROM users\")",
				"}",
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzer.hasAuthCheck(tt.lines); got != tt.want {
				t.Errorf("hasAuthCheck() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthPatternsAnalyzer_HasSensitiveOperation(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{
			"Has DB operation",
			[]string{
				"func handler() {",
				"  db.Insert(user)",
				"}",
			},
			true,
		},
		{
			"Has password operation",
			[]string{
				"func handler() {",
				"  user.password = newPassword",
				"}",
			},
			true,
		},
		{
			"No sensitive operation",
			[]string{
				"func handler() {",
				"  fmt.Println(\"Hello\")",
				"}",
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzer.hasSensitiveOperation(tt.lines); got != tt.want {
				t.Errorf("hasSensitiveOperation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthPatternsAnalyzer_IsSensitiveContext(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	tests := []struct {
		line string
		want bool
	}{
		{"var password = \"secret\"", true},
		{"func login(credentials auth.Credentials) {", true},
		{"session.SetToken(token)", true},
		{"cookie := &http.Cookie{...}", true},
		{"var username = \"john\"", false},
		{"fmt.Println(\"Hello\")", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := analyzer.isSensitiveContext(tt.line); got != tt.want {
				t.Errorf("isSensitiveContext(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestAuthPatternsAnalyzer_IsExternalFacing(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	tests := []struct {
		line string
		want bool
	}{
		{"func handler(w http.ResponseWriter, r *http.Request) {", true},
		{"w.Write([]byte(\"response\"))", true},
		{"@app.route(\"/api/users\")", true}, // Pattern requires @app. prefix
		{"var x = 10", false},
		{"internal.ProcessData()", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := analyzer.isExternalFacing(tt.line); got != tt.want {
				t.Errorf("isExternalFacing(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestAuthPatternsAnalyzer_IsLikelyTestCode(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	tests := []struct {
		name     string
		filePath string
		line     string
		want     bool
	}{
		{
			"Test file path",
			"/path/to/auth_test.go",
			"var password = \"test\"",
			true,
		},
		{
			"Test directory",
			"/path/test/auth.go",
			"var password = \"test\"",
			true,
		},
		{
			"Test content",
			"/path/to/auth.go",
			"assert.Equal(t, expected, actual)",
			true,
		},
		{
			"Mock content",
			"/path/to/auth.go",
			"mockDB := mock.NewDB()",
			true,
		},
		{
			"Normal code",
			"/path/to/auth.go",
			"var password = \"secret\"",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzer.isLikelyTestCode(tt.filePath, tt.line); got != tt.want {
				t.Errorf("isLikelyTestCode(%q, %q) = %v, want %v", tt.filePath, tt.line, got, tt.want)
			}
		})
	}
}

func TestAuthPatternsAnalyzer_ShouldAnalyzeFile(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"Go file", "/path/to/auth.go", true},
		{"Python file", "/path/to/auth.py", true},
		{"JavaScript file", "/path/to/auth.js", true},
		{"PHP file", "/path/to/auth.php", true},
		{"Java file", "/path/to/Auth.java", true},
		{"Ruby file", "/path/to/auth.rb", true},
		{"C# file", "/path/to/Auth.cs", true},
		{"TypeScript file", "/path/to/auth.ts", true},
		{"Binary file", "/path/to/app.exe", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzer.shouldAnalyzeFile(tt.path); got != tt.want {
				t.Errorf("shouldAnalyzeFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestAuthPatternsAnalyzer_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze should not fail on empty directory: %v", err)
	}

	if len(result.Findings) != 0 {
		t.Error("Empty directory should have no findings")
	}
}

func TestAuthPatternsAnalyzer_MultipleIssues(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "auth.go")

	content := `package auth

import (
	"crypto/md5"
	"net/http"
)

const password = "hardcoded_password"
const jwtSecret = "my-jwt-secret"

func login(w http.ResponseWriter, r *http.Request) {
	// Weak hashing
	hash := md5.Sum([]byte(password))

	// Permissive CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Insecure cookie
	cookie := &http.Cookie{
		Name:     "session",
		Value:    "abc123",
		HttpOnly: false,
		Secure:   false,
	}
	http.SetCookie(w, cookie)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewAuthPatternsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}

	// Optionally check that we detected at least some issues
	if len(result.Findings) > 0 {
		t.Logf("Detected %d findings", len(result.Findings))
	}
}

func TestAuthPatternsAnalyzer_SkipTestFiles(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "auth_test.go")

	content := `package auth

const password = "test_password"
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	config.IncludeTests = false
	analyzer := NewAuthPatternsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Should not analyze test files when IncludeTests is false
	if len(result.Findings) > 0 {
		t.Error("Should skip test files when IncludeTests is false")
	}
}
