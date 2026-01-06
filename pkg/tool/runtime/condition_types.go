package runtime

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// PortReadyCondition waits for a TCP port to become available
type PortReadyCondition struct {
	BaseCondition
	Host string
	Port int
}

// NewPortReadyCondition creates a new port ready condition
func NewPortReadyCondition(host string, port int) *PortReadyCondition {
	return &PortReadyCondition{
		BaseCondition: BaseCondition{timeout: 60 * time.Second}, // Default 60s timeout for ports
		Host:          host,
		Port:          port,
	}
}

// Check implements Condition interface
func (p *PortReadyCondition) Check(ctx context.Context) (bool, error) {
	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	conn, err := net.DialTimeout("tcp", address, 1*time.Second)
	if err != nil {
		return false, nil
	}
	defer conn.Close()
	return true, nil
}

// String implements Condition interface
func (p *PortReadyCondition) String() string {
	return fmt.Sprintf("PortReady(%s:%d)", p.Host, p.Port)
}

// FileExistsCondition waits for a file to exist
type FileExistsCondition struct {
	BaseCondition
	Path string
}

// NewFileExistsCondition creates a new file exists condition
func NewFileExistsCondition(path string) *FileExistsCondition {
	return &FileExistsCondition{
		BaseCondition: BaseCondition{timeout: 30 * time.Second},
		Path:          path,
	}
}

// Check implements Condition interface
func (f *FileExistsCondition) Check(ctx context.Context) (bool, error) {
	_, err := os.Stat(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("error checking file: %w", err)
	}
	return true, nil
}

// String implements Condition interface
func (f *FileExistsCondition) String() string {
	return fmt.Sprintf("FileExists(%s)", f.Path)
}

// ProcessExitCondition waits for a process to exit
type ProcessExitCondition struct {
	BaseCondition
	PID      int
	ExitCode int // Optional, if set waits for specific exit code
}

// NewProcessExitCondition creates a new process exit condition
func NewProcessExitCondition(pid int) *ProcessExitCondition {
	return &ProcessExitCondition{
		BaseCondition: BaseCondition{timeout: 300 * time.Second}, // 5 minute default
		PID:           pid,
		ExitCode:      -1, // Any exit code
	}
}

// Check implements Condition interface
func (p *ProcessExitCondition) Check(ctx context.Context) (bool, error) {
	// Use OS-level process check
	process, err := os.FindProcess(p.PID)
	if err != nil {
		// Process doesn't exist (already exited)
		return true, nil
	}

	// Try to send signal 0 to check if process exists
	err = process.Signal(os.Signal(nil))
	if err != nil {
		// Process doesn't exist
		return true, nil
	}

	// Process still running
	return false, nil
}

// String implements Condition interface
func (p *ProcessExitCondition) String() string {
	if p.ExitCode >= 0 {
		return fmt.Sprintf("ProcessExit(PID=%d, ExitCode=%d)", p.PID, p.ExitCode)
	}
	return fmt.Sprintf("ProcessExit(PID=%d)", p.PID)
}

// HealthCheckCondition waits for an HTTP health endpoint to return success
type HealthCheckCondition struct {
	BaseCondition
	URL    string
	Status int // Expected status code (default: 200)
	client *http.Client
}

// NewHealthCheckCondition creates a new health check condition
func NewHealthCheckCondition(url string) *HealthCheckCondition {
	return &HealthCheckCondition{
		BaseCondition: BaseCondition{timeout: 60 * time.Second},
		URL:           url,
		Status:        200,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Check implements Condition interface
func (h *HealthCheckCondition) Check(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", h.URL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return false, nil // Connection error means not ready
	}
	defer resp.Body.Close()

	return resp.StatusCode == h.Status, nil
}

// String implements Condition interface
func (h *HealthCheckCondition) String() string {
	return fmt.Sprintf("HealthCheck(%s, status=%d)", h.URL, h.Status)
}

// LogMatchCondition waits for a pattern to appear in a log file
type LogMatchCondition struct {
	BaseCondition
	LogFile string
	Pattern string
}

// NewLogMatchCondition creates a new log match condition
func NewLogMatchCondition(logFile, pattern string) *LogMatchCondition {
	return &LogMatchCondition{
		BaseCondition: BaseCondition{timeout: 60 * time.Second},
		LogFile:       logFile,
		Pattern:       pattern,
	}
}

// Check implements Condition interface
func (l *LogMatchCondition) Check(ctx context.Context) (bool, error) {
	file, err := os.Open(l.LogFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, l.Pattern) {
			return true, nil
		}
	}

	return false, nil
}

// String implements Condition interface
func (l *LogMatchCondition) String() string {
	return fmt.Sprintf("LogMatch(file=%s, pattern='%s')", l.LogFile, l.Pattern)
}

// DatabaseQueryCondition waits for a database query to succeed
type DatabaseQueryCondition struct {
	BaseCondition
	Dsn      string
	Query    string
	Database string // "postgres", "mysql", etc.
}

// NewDatabaseQueryCondition creates a new database query condition
func NewDatabaseQueryCondition(database, dsn, query string) *DatabaseQueryCondition {
	return &DatabaseQueryCondition{
		BaseCondition: BaseCondition{timeout: 60 * time.Second},
		Database:      database,
		Dsn:           dsn,
		Query:         query,
	}
}

// Check implements Condition interface
func (d *DatabaseQueryCondition) Check(ctx context.Context) (bool, error) {
	switch strings.ToLower(d.Database) {
	case "postgres", "postgresql":
		return d.checkPostgres(ctx)
	case "mysql":
		return d.checkMySQL(ctx)
	case "redis":
		return d.checkRedis(ctx)
	case "mongodb":
		return d.checkMongoDB(ctx)
	default:
		return false, fmt.Errorf("unsupported database: %s", d.Database)
	}
}

func (d *DatabaseQueryCondition) checkPostgres(ctx context.Context) (bool, error) {
	// Use psql command for now - in production, use proper driver
	cmd := exec.CommandContext(ctx, "psql", d.Dsn, "-c", d.Query)
	err := cmd.Run()
	if err != nil {
		return false, nil // Not ready yet
	}
	return true, nil
}

func (d *DatabaseQueryCondition) checkMySQL(ctx context.Context) (bool, error) {
	// Use mysql command
	cmd := exec.CommandContext(ctx, "mysql", "-e", d.Query)
	if d.Dsn != "" {
		cmd.Args = append(cmd.Args, "--defaults-file="+d.Dsn)
	}
	err := cmd.Run()
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (d *DatabaseQueryCondition) checkRedis(ctx context.Context) (bool, error) {
	// Use redis-cli command
	parts := strings.Split(d.Dsn, ":")
	host := "localhost"
	port := "6379"
	if len(parts) >= 1 {
		host = parts[0]
	}
	if len(parts) >= 2 {
		port = parts[1]
	}

	cmd := exec.CommandContext(ctx, "redis-cli", "-h", host, "-p", port, d.Query)
	err := cmd.Run()
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (d *DatabaseQueryCondition) checkMongoDB(ctx context.Context) (bool, error) {
	// Use mongo command
	cmd := exec.CommandContext(ctx, "mongo", "--eval", d.Query, d.Dsn)
	err := cmd.Run()
	if err != nil {
		return false, nil
	}
	return true, nil
}

// String implements Condition interface
func (d *DatabaseQueryCondition) String() string {
	return fmt.Sprintf("DatabaseQuery(%s, query='%s')", d.Database, d.Query)
}

// ParseConditionsFromYAML parses conditions from a YAML configuration
func ParseConditionsFromYAML(data map[string]any) ([]Condition, error) {
	var conditions []Condition

	conditionsList, ok := data["ready_conditions"].([]any)
	if !ok {
		return conditions, nil // No conditions specified
	}

	for _, condData := range conditionsList {
		condMap, ok := condData.(map[string]any)
		if !ok {
			continue
		}

		cond, err := parseSingleCondition(condMap)
		if err != nil {
			return nil, fmt.Errorf("failed to parse condition: %w", err)
		}

		if cond != nil {
			conditions = append(conditions, cond)
		}
	}

	return conditions, nil
}

func parseSingleCondition(data map[string]any) (Condition, error) {
	condType, ok := data["type"].(string)
	if !ok {
		return nil, nil // Skip conditions without type
	}

	timeout := 30 * time.Second
	if t, ok := data["timeout"].(string); ok {
		parsed, err := time.ParseDuration(t)
		if err == nil {
			timeout = parsed
		}
	}

	switch condType {
	case "port_ready":
		host, _ := data["host"].(string)
		if host == "" {
			host = "localhost"
		}
		port, _ := data["port"].(float64)
		cond := NewPortReadyCondition(host, int(port))
		cond.timeout = timeout
		return cond, nil

	case "file_exists":
		path, _ := data["path"].(string)
		if path == "" {
			return nil, fmt.Errorf("file_exists condition requires 'path'")
		}
		cond := NewFileExistsCondition(path)
		cond.timeout = timeout
		return cond, nil

	case "health_check":
		url, _ := data["url"].(string)
		if url == "" {
			return nil, fmt.Errorf("health_check condition requires 'url'")
		}
		cond := NewHealthCheckCondition(url)
		cond.timeout = timeout
		return cond, nil

	case "log_match":
		logFile, _ := data["log_file"].(string)
		pattern, _ := data["pattern"].(string)
		if logFile == "" || pattern == "" {
			return nil, fmt.Errorf("log_match requires 'log_file' and 'pattern'")
		}
		cond := NewLogMatchCondition(logFile, pattern)
		cond.timeout = timeout
		return cond, nil

	case "database_query":
		database, _ := data["database"].(string)
		dsn, _ := data["dsn"].(string)
		query, _ := data["query"].(string)
		if database == "" || dsn == "" || query == "" {
			return nil, fmt.Errorf("database_query requires 'database', 'dsn', and 'query'")
		}
		cond := NewDatabaseQueryCondition(database, dsn, query)
		cond.timeout = timeout
		return cond, nil

	default:
		return nil, fmt.Errorf("unknown condition type: %s", condType)
	}
}

// GetDefaultServiceConditions returns default ready conditions for common services
func GetDefaultServiceConditions(serviceType string) []Condition {
	switch strings.ToLower(serviceType) {
	case "postgresql", "postgres", "pg":
		return []Condition{
			NewPortReadyCondition("localhost", 5432),
			NewDatabaseQueryCondition("postgres", "host=localhost port=5432 user=postgres dbname=postgres sslmode=disable", "SELECT 1"),
		}

	case "mysql":
		return []Condition{
			NewPortReadyCondition("localhost", 3306),
			NewDatabaseQueryCondition("mysql", "root:@tcp(localhost:3306)/mysql", "SELECT 1"),
		}

	case "redis":
		return []Condition{
			NewPortReadyCondition("localhost", 6379),
			NewDatabaseQueryCondition("redis", "localhost:6379", "PING"),
		}

	case "mongodb":
		return []Condition{
			NewPortReadyCondition("localhost", 27017),
			NewDatabaseQueryCondition("mongodb", "mongodb://localhost:27017/test", "db.runCommand({ping: 1})"),
		}

	case "elasticsearch":
		return []Condition{
			NewPortReadyCondition("localhost", 9200),
			NewHealthCheckCondition("http://localhost:9200/_cluster/health"),
		}

	case "rabbitmq":
		return []Condition{
			NewPortReadyCondition("localhost", 5672),
			NewPortReadyCondition("localhost", 15672),
			NewHealthCheckCondition("http://localhost:15672/api/health"),
		}

	default:
		return []Condition{
			NewPortReadyCondition("localhost", 8080), // Generic web service
		}
	}
}
