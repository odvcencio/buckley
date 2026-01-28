// Package main provides a self-healing E2E test driver for Buckley's TUI.
//
// The driver connects to Buckley via the fluffy-ui agent socket and executes
// test scenarios using semantic matching rather than brittle selectors.
//
// Usage:
//
//	go run driver.go --socket unix:/tmp/buckley.sock --scenario scenario.json
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

// AgentRequest represents a request to the fluffy-ui agent socket.
type AgentRequest struct {
	Type        string `json:"type"`
	ID          int    `json:"id,omitempty"`
	Text        string `json:"text,omitempty"`
	Key         string `json:"key,omitempty"`
	X           int    `json:"x,omitempty"`
	Y           int    `json:"y,omitempty"`
	Button      string `json:"button,omitempty"`
	Action      string `json:"action,omitempty"`
	IncludeText bool   `json:"include_text,omitempty"`
}

// AgentResponse represents a response from the fluffy-ui agent socket.
type AgentResponse struct {
	ID           int             `json:"id,omitempty"`
	OK           bool            `json:"ok"`
	Error        string          `json:"error,omitempty"`
	Message      string          `json:"message,omitempty"`
	Snapshot     json.RawMessage `json:"snapshot,omitempty"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

// WidgetInfo describes a widget in the UI snapshot.
type WidgetInfo struct {
	ID          string       `json:"id"`
	Role        string       `json:"type"`
	Label       string       `json:"label,omitempty"`
	Description string       `json:"description,omitempty"`
	Value       string       `json:"value,omitempty"`
	Focused     bool         `json:"focused,omitempty"`
	Focusable   bool         `json:"focusable,omitempty"`
	Bounds      Bounds       `json:"bounds"`
	Children    []WidgetInfo `json:"children,omitempty"`
}

// Bounds represents widget boundaries.
type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Snapshot represents the full UI state.
type Snapshot struct {
	Timestamp  time.Time    `json:"timestamp"`
	Width      int          `json:"width"`
	Height     int          `json:"height"`
	LayerCount int          `json:"layer_count"`
	Text       string       `json:"text,omitempty"`
	Widgets    []WidgetInfo `json:"widgets"`
	FocusedID  string       `json:"focused_id,omitempty"`
}

// Driver connects to Buckley's agent socket and executes test scenarios.
type Driver struct {
	conn   net.Conn
	reader *bufio.Reader
	enc    *json.Encoder
	nextID int
}

// NewDriver creates a new agent driver connected to the given socket.
func NewDriver(socketAddr string) (*Driver, error) {
	conn, err := dialAgent(socketAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to agent: %w", err)
	}

	d := &Driver{
		conn:   conn,
		reader: bufio.NewReader(conn),
		enc:    json.NewEncoder(conn),
		nextID: 1,
	}

	// Send hello
	if err := d.send(AgentRequest{Type: "hello"}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}

	resp, err := d.receive()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("hello response: %w", err)
	}
	if !resp.OK {
		conn.Close()
		return nil, fmt.Errorf("hello failed: %s", resp.Error)
	}

	return d, nil
}

// Close closes the driver connection.
func (d *Driver) Close() error {
	return d.conn.Close()
}

// Snapshot captures the current UI state.
func (d *Driver) Snapshot(includeText bool) (*Snapshot, error) {
	id := d.nextID
	d.nextID++

	if err := d.send(AgentRequest{Type: "snapshot", ID: id, IncludeText: includeText}); err != nil {
		return nil, err
	}

	resp, err := d.receive()
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("snapshot failed: %s", resp.Error)
	}

	var snap Snapshot
	if err := json.Unmarshal(resp.Snapshot, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}

	return &snap, nil
}

// Type sends text input to the focused widget.
func (d *Driver) Type(text string) error {
	id := d.nextID
	d.nextID++

	if err := d.send(AgentRequest{Type: "text", ID: id, Text: text}); err != nil {
		return err
	}

	resp, err := d.receive()
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("type failed: %s", resp.Error)
	}
	return nil
}

// Key sends a key press.
func (d *Driver) Key(key string) error {
	id := d.nextID
	d.nextID++

	if err := d.send(AgentRequest{Type: "key", ID: id, Key: key}); err != nil {
		return err
	}

	resp, err := d.receive()
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("key failed: %s", resp.Error)
	}
	return nil
}

// KeyCombo sends a key combination (e.g., "ctrl+n").
func (d *Driver) KeyCombo(keys ...string) error {
	for _, key := range keys {
		// Send modifier down if needed
		if isModifier(key) {
			if err := d.keyAction(key, "press"); err != nil {
				return err
			}
		}
	}

	// Send non-modifier keys
	for _, key := range keys {
		if !isModifier(key) {
			if err := d.Key(key); err != nil {
				return err
			}
		}
	}

	// Release modifiers in reverse order
	for i := len(keys) - 1; i >= 0; i-- {
		if isModifier(keys[i]) {
			if err := d.keyAction(keys[i], "release"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (d *Driver) keyAction(key, action string) error {
	id := d.nextID
	d.nextID++

	req := AgentRequest{Type: "key", ID: id, Key: key}
	if action == "release" {
		// Some agent protocols use special key names for release
		req.Key = key + "_up"
	}

	if err := d.send(req); err != nil {
		return err
	}

	resp, err := d.receive()
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("key %s failed: %s", action, resp.Error)
	}
	return nil
}

func isModifier(key string) bool {
	switch strings.ToLower(key) {
	case "ctrl", "alt", "shift", "meta", "super":
		return true
	}
	return false
}

// Click clicks at the given coordinates.
func (d *Driver) Click(x, y int) error {
	id := d.nextID
	d.nextID++

	if err := d.send(AgentRequest{Type: "mouse", ID: id, X: x, Y: y, Button: "left", Action: "click"}); err != nil {
		return err
	}

	resp, err := d.receive()
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("click failed: %s", resp.Error)
	}
	return nil
}

func (d *Driver) send(req AgentRequest) error {
	return d.enc.Encode(req)
}

func (d *Driver) receive() (*AgentResponse, error) {
	line, err := d.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var resp AgentResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func dialAgent(addr string) (net.Conn, error) {
	if strings.HasPrefix(addr, "unix:") {
		return net.Dial("unix", strings.TrimPrefix(addr, "unix:"))
	}
	if strings.HasPrefix(addr, "tcp:") {
		return net.Dial("tcp", strings.TrimPrefix(addr, "tcp:"))
	}
	// Try as unix socket path
	return net.Dial("unix", addr)
}

// FindWidget finds a widget matching the given criteria.
// Uses semantic matching for labels (case-insensitive substring match).
func FindWidget(snap *Snapshot, role string, labelMatch string) *WidgetInfo {
	return findWidgetRecursive(snap.Widgets, role, strings.ToLower(labelMatch))
}

func findWidgetRecursive(widgets []WidgetInfo, role string, labelMatch string) *WidgetInfo {
	for i := range widgets {
		w := &widgets[i]
		if (role == "" || strings.EqualFold(w.Role, role)) &&
			(labelMatch == "" || strings.Contains(strings.ToLower(w.Label), labelMatch)) {
			return w
		}
		if found := findWidgetRecursive(w.Children, role, labelMatch); found != nil {
			return found
		}
	}
	return nil
}

// FindWidgetByValue finds a widget by its value.
func FindWidgetByValue(snap *Snapshot, value string) *WidgetInfo {
	return findWidgetByValueRecursive(snap.Widgets, strings.ToLower(value))
}

func findWidgetByValueRecursive(widgets []WidgetInfo, value string) *WidgetInfo {
	for i := range widgets {
		w := &widgets[i]
		if strings.Contains(strings.ToLower(w.Value), value) {
			return &widgets[i]
		}
		if found := findWidgetByValueRecursive(w.Children, value); found != nil {
			return found
		}
	}
	return nil
}

// WaitForCondition waits for a condition to be met, polling the snapshot.
func WaitForCondition(d *Driver, timeout time.Duration, pollInterval time.Duration, condition func(*Snapshot) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap, err := d.Snapshot(false)
		if err != nil {
			return err
		}
		if condition(snap) {
			return nil
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("timeout waiting for condition")
}

func main() {
	var (
		socketAddr = flag.String("socket", "unix:/tmp/buckley.sock", "Agent socket address")
		scenario   = flag.String("scenario", "", "Scenario file to run")
		verbose    = flag.Bool("verbose", false, "Verbose output")
	)
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// Connect to agent
	driver, err := NewDriver(*socketAddr)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer driver.Close()

	log.Printf("Connected to %s", *socketAddr)

	// Get initial snapshot
	snap, err := driver.Snapshot(true)
	if err != nil {
		log.Fatalf("Failed to get snapshot: %v", err)
	}

	if *verbose {
		log.Printf("Screen: %dx%d, %d widgets", snap.Width, snap.Height, len(snap.Widgets))
		if snap.Text != "" {
			log.Printf("Screen text:\n%s", snap.Text)
		}
	}

	// Run scenario if provided
	if *scenario != "" {
		if err := runScenario(driver, *scenario); err != nil {
			log.Fatalf("Scenario failed: %v", err)
		}
		log.Println("Scenario completed successfully")
		return
	}

	// Interactive mode - demonstrate capabilities
	demoMode(driver)
}

func demoMode(d *Driver) {
	log.Println("Running demo mode...")

	// Find and click on input area
	snap, err := d.Snapshot(false)
	if err != nil {
		log.Fatalf("Failed to get snapshot: %v", err)
	}

	// Try to find input field
	input := FindWidget(snap, "textbox", "")
	if input != nil {
		log.Printf("Found input at (%d, %d)", input.Bounds.X, input.Bounds.Y)
		centerX := input.Bounds.X + input.Bounds.Width/2
		centerY := input.Bounds.Y + input.Bounds.Height/2
		
		log.Printf("Clicking input at (%d, %d)", centerX, centerY)
		if err := d.Click(centerX, centerY); err != nil {
			log.Printf("Click failed: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Type a message
	log.Println("Typing message...")
	if err := d.Type("Hello from the agent driver!"); err != nil {
		log.Printf("Type failed: %v", err)
	}

	log.Println("Demo complete")
}

func runScenario(d *Driver, scenarioFile string) error {
	data, err := os.ReadFile(scenarioFile)
	if err != nil {
		return fmt.Errorf("read scenario: %w", err)
	}

	var scenario Scenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		return fmt.Errorf("parse scenario: %w", err)
	}

	log.Printf("Running scenario: %s", scenario.Name)

	for i, step := range scenario.Steps {
		log.Printf("Step %d: %s", i+1, step.Goal)

		switch step.Action {
		case "snapshot":
			snap, err := d.Snapshot(true)
			if err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			log.Printf("Screen: %dx%d, focused: %s", snap.Width, snap.Height, snap.FocusedID)

		case "focus":
			if err := focusWidget(d, step.Target); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}

		case "type":
			if err := d.Type(step.Text); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}

		case "key":
			if err := d.Key(step.Key); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}

		case "key_combo":
			if err := d.KeyCombo(step.Keys...); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}

		case "click":
			if err := clickWidget(d, step.Target); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}

		case "wait_for":
			if err := waitForCondition(d, step); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}

		case "sleep":
			time.Sleep(time.Duration(step.DurationMs) * time.Millisecond)

		default:
			return fmt.Errorf("step %d: unknown action %q", i+1, step.Action)
		}
	}

	return nil
}

// Scenario represents a test scenario.
type Scenario struct {
	Name  string       `json:"name"`
	Steps []ScenarioStep `json:"steps"`
}

// ScenarioStep represents a single step in a scenario.
type ScenarioStep struct {
	Goal        string   `json:"goal"`
	Action      string   `json:"action"`
	Target      string   `json:"target,omitempty"`
	Text        string   `json:"text,omitempty"`
	Key         string   `json:"key,omitempty"`
	Keys        []string `json:"keys,omitempty"`
	Condition   string   `json:"condition,omitempty"`
	Value       string   `json:"value,omitempty"`
	DurationMs  int      `json:"duration_ms,omitempty"`
	TimeoutSec  int      `json:"timeout_sec,omitempty"`
}

func focusWidget(d *Driver, target string) error {
	snap, err := d.Snapshot(false)
	if err != nil {
		return err
	}

	// Try semantic matching first (self-healing)
	criteria := MatchCriteria{
		Label:         target,
		Keywords:      []string{target},
		MinConfidence: 0.5,
	}
	
	if result := FindWidgetSemantic(snap, criteria); result != nil {
		centerX := result.Widget.Bounds.X + result.Widget.Bounds.Width/2
		centerY := result.Widget.Bounds.Y + result.Widget.Bounds.Height/2
		return d.Click(centerX, centerY)
	}

	// Fallback to simple matching
	widget := FindWidget(snap, "", target)
	if widget == nil {
		// Try finding by role
		widget = FindWidget(snap, target, "")
	}
	if widget == nil {
		// Last resort: try semantic matching with goal-based criteria
		if result, _ := SelfHealingFind(snap, target); result != nil {
			centerX := result.Widget.Bounds.X + result.Widget.Bounds.Width/2
			centerY := result.Widget.Bounds.Y + result.Widget.Bounds.Height/2
			return d.Click(centerX, centerY)
		}
		return fmt.Errorf("widget not found: %s", target)
	}

	centerX := widget.Bounds.X + widget.Bounds.Width/2
	centerY := widget.Bounds.Y + widget.Bounds.Height/2
	return d.Click(centerX, centerY)
}

func clickWidget(d *Driver, target string) error {
	return focusWidget(d, target)
}

func waitForCondition(d *Driver, step ScenarioStep) error {
	timeout := 10 * time.Second
	if step.TimeoutSec > 0 {
		timeout = time.Duration(step.TimeoutSec) * time.Second
	}

	switch step.Condition {
	case "text_contains":
		return WaitForCondition(d, timeout, 100*time.Millisecond, func(snap *Snapshot) bool {
			return strings.Contains(strings.ToLower(snap.Text), strings.ToLower(step.Value))
		})

	case "widget_exists":
		return WaitForCondition(d, timeout, 100*time.Millisecond, func(snap *Snapshot) bool {
			return FindWidget(snap, "", step.Value) != nil
		})

	default:
		return fmt.Errorf("unknown condition: %s", step.Condition)
	}
}
