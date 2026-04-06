package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/browser"
	"github.com/odvcencio/buckley/pkg/browser/adapters/servo"
	"github.com/odvcencio/buckley/pkg/session"
)

var (
	defaultBrowserManager     *browser.Manager
	defaultBrowserManagerErr  error
	defaultBrowserManagerOnce sync.Once
)

// BrowserStartTool starts a browser session backed by browserd.
type BrowserStartTool struct {
	Manager *browser.Manager
}

func (t *BrowserStartTool) Name() string {
	return "browser_start"
}

func (t *BrowserStartTool) Description() string {
	return "Start a browser session via the browser runtime."
}

func (t *BrowserStartTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"session_id": {
				Type:        "string",
				Description: "Optional session identifier; generated if omitted",
			},
			"url": {
				Type:        "string",
				Description: "Initial URL to open",
			},
			"viewport": {
				Type:        "object",
				Description: "Viewport config (width, height, device_scale_factor)",
			},
			"frame_rate": {
				Type:        "integer",
				Description: "Target frame rate (default 12)",
				Default:     12,
			},
			"user_agent": {
				Type:        "string",
				Description: "Optional user agent override",
			},
			"locale": {
				Type:        "string",
				Description: "Optional locale override (e.g. en-US)",
			},
			"timezone": {
				Type:        "string",
				Description: "Optional timezone override (e.g. UTC)",
			},
			"network_allowlist": {
				Type:        "array",
				Description: "Optional network allowlist for browserd",
			},
		},
	}
}

func (t *BrowserStartTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BrowserStartTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	manager, err := resolveBrowserManager(t.Manager)
	if err != nil {
		return toolError(err), nil
	}
	cfg := browser.DefaultSessionConfig()
	cfg.SessionID = strings.TrimSpace(parseBrowserString(params, "session_id"))
	if cfg.SessionID == "" {
		cfg.SessionID = session.GenerateSessionID("browser")
	}
	cfg.InitialURL = strings.TrimSpace(parseBrowserString(params, "url"))
	cfg.UserAgent = strings.TrimSpace(parseBrowserString(params, "user_agent"))
	cfg.Locale = strings.TrimSpace(parseBrowserString(params, "locale"))
	cfg.Timezone = strings.TrimSpace(parseBrowserString(params, "timezone"))
	cfg.NetworkAllowlist = parseBrowserStringSlice(params, "network_allowlist")

	if viewportVal, ok := params["viewport"]; ok {
		cfg.Viewport = parseBrowserViewport(viewportVal, cfg.Viewport)
	}
	cfg.FrameRate = parseBrowserInt(params, "frame_rate", cfg.FrameRate)

	sess, err := manager.CreateSession(ctx, cfg)
	if err != nil {
		return toolError(err), nil
	}
	obs, err := sess.Observe(ctx, browser.ObserveOptions{
		IncludeDOMSnapshot:   true,
		IncludeAccessibility: true,
	})
	if err != nil {
		return toolError(err), nil
	}
	obsData, err := marshalToMap(obs)
	if err != nil {
		return toolError(err), nil
	}
	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id":    cfg.SessionID,
			"state_version": obs.StateVersion,
			"url":           obs.URL,
			"title":         obs.Title,
			"observation":   obsData,
		},
	}, nil
}

// BrowserNavigateTool navigates an existing browser session.
type BrowserNavigateTool struct {
	Manager *browser.Manager
}

func (t *BrowserNavigateTool) Name() string {
	return "browser_navigate"
}

func (t *BrowserNavigateTool) Description() string {
	return "Navigate an existing browser session to a URL."
}

func (t *BrowserNavigateTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"session_id": {
				Type:        "string",
				Description: "Browser session identifier",
			},
			"url": {
				Type:        "string",
				Description: "URL to navigate to",
			},
		},
		Required: []string{"session_id", "url"},
	}
}

func (t *BrowserNavigateTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BrowserNavigateTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	manager, err := resolveBrowserManager(t.Manager)
	if err != nil {
		return toolError(err), nil
	}
	sessionID := strings.TrimSpace(parseBrowserString(params, "session_id"))
	if sessionID == "" {
		return toolError(fmt.Errorf("session_id is required")), nil
	}
	url := strings.TrimSpace(parseBrowserString(params, "url"))
	if url == "" {
		return toolError(fmt.Errorf("url is required")), nil
	}
	sess, ok := manager.GetSession(sessionID)
	if !ok {
		return toolError(fmt.Errorf("session not found: %s", sessionID)), nil
	}
	obs, err := sess.Navigate(ctx, url)
	if err != nil {
		return toolError(err), nil
	}
	obsData, err := marshalToMap(obs)
	if err != nil {
		return toolError(err), nil
	}
	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id":    sessionID,
			"state_version": obs.StateVersion,
			"url":           obs.URL,
			"title":         obs.Title,
			"observation":   obsData,
		},
	}, nil
}

// BrowserObserveTool retrieves the current observation for a session.
type BrowserObserveTool struct {
	Manager *browser.Manager
}

func (t *BrowserObserveTool) Name() string {
	return "browser_observe"
}

func (t *BrowserObserveTool) Description() string {
	return "Observe the current browser state for a session."
}

func (t *BrowserObserveTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"session_id": {
				Type:        "string",
				Description: "Browser session identifier",
			},
			"include_frame": {
				Type:        "boolean",
				Description: "Include latest frame data",
			},
			"include_dom_snapshot": {
				Type:        "boolean",
				Description: "Include DOM snapshot",
			},
			"include_accessibility": {
				Type:        "boolean",
				Description: "Include accessibility tree",
			},
			"include_hit_test": {
				Type:        "boolean",
				Description: "Include hit-test map",
			},
		},
		Required: []string{"session_id"},
	}
}

func (t *BrowserObserveTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BrowserObserveTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	manager, err := resolveBrowserManager(t.Manager)
	if err != nil {
		return toolError(err), nil
	}
	sessionID := strings.TrimSpace(parseBrowserString(params, "session_id"))
	if sessionID == "" {
		return toolError(fmt.Errorf("session_id is required")), nil
	}
	sess, ok := manager.GetSession(sessionID)
	if !ok {
		return toolError(fmt.Errorf("session not found: %s", sessionID)), nil
	}
	opts := browser.ObserveOptions{
		IncludeFrame:         parseBrowserBool(params, "include_frame", false),
		IncludeDOMSnapshot:   parseBrowserBool(params, "include_dom_snapshot", true),
		IncludeAccessibility: parseBrowserBool(params, "include_accessibility", true),
		IncludeHitTest:       parseBrowserBool(params, "include_hit_test", false),
	}
	obs, err := sess.Observe(ctx, opts)
	if err != nil {
		return toolError(err), nil
	}
	obsData, err := marshalToMap(obs)
	if err != nil {
		return toolError(err), nil
	}
	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id":    sessionID,
			"state_version": obs.StateVersion,
			"url":           obs.URL,
			"title":         obs.Title,
			"observation":   obsData,
		},
	}, nil
}

// BrowserStreamTool streams browser events for a short window.
type BrowserStreamTool struct {
	Manager *browser.Manager
}

func (t *BrowserStreamTool) Name() string {
	return "browser_stream"
}

func (t *BrowserStreamTool) Description() string {
	return "Stream browser events (frames, diffs, hit-test) for a short window."
}

func (t *BrowserStreamTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"session_id": {
				Type:        "string",
				Description: "Browser session identifier",
			},
			"duration_ms": {
				Type:        "integer",
				Description: "How long to stream events (milliseconds)",
				Default:     1000,
			},
			"max_events": {
				Type:        "integer",
				Description: "Maximum number of events to collect",
				Default:     25,
			},
			"include_frames": {
				Type:        "boolean",
				Description: "Include frame events",
			},
			"include_dom_diffs": {
				Type:        "boolean",
				Description: "Include DOM diff events",
			},
			"include_accessibility_diffs": {
				Type:        "boolean",
				Description: "Include accessibility diff events",
			},
			"include_hit_test": {
				Type:        "boolean",
				Description: "Include hit-test map events",
			},
			"target_fps": {
				Type:        "integer",
				Description: "Target frames per second",
			},
		},
		Required: []string{"session_id"},
	}
}

func (t *BrowserStreamTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BrowserStreamTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	manager, err := resolveBrowserManager(t.Manager)
	if err != nil {
		return toolError(err), nil
	}
	sessionID := strings.TrimSpace(parseBrowserString(params, "session_id"))
	if sessionID == "" {
		return toolError(fmt.Errorf("session_id is required")), nil
	}
	sess, ok := manager.GetSession(sessionID)
	if !ok {
		return toolError(fmt.Errorf("session not found: %s", sessionID)), nil
	}
	durationMs := parseBrowserInt(params, "duration_ms", 1000)
	if durationMs <= 0 {
		durationMs = 1000
	}
	maxEvents := parseBrowserInt(params, "max_events", 25)
	if maxEvents <= 0 {
		maxEvents = 25
	}

	includeFrames := parseBrowserBool(params, "include_frames", false)
	includeDOMDiffs := parseBrowserBool(params, "include_dom_diffs", false)
	includeA11yDiffs := parseBrowserBool(params, "include_accessibility_diffs", false)
	includeHitTest := parseBrowserBool(params, "include_hit_test", false)
	if !includeFrames && !includeDOMDiffs && !includeA11yDiffs && !includeHitTest {
		includeFrames = true
	}

	opts := browser.StreamOptions{
		IncludeFrames:             includeFrames,
		IncludeDOMDiffs:           includeDOMDiffs,
		IncludeAccessibilityDiffs: includeA11yDiffs,
		IncludeHitTest:            includeHitTest,
		TargetFPS:                 parseBrowserInt(params, "target_fps", 0),
	}

	streamCtx, cancel := context.WithTimeout(ctx, time.Duration(durationMs)*time.Millisecond)
	defer cancel()

	eventsCh, err := sess.Stream(streamCtx, opts)
	if err != nil {
		return toolError(err), nil
	}

	events := make([]map[string]any, 0, maxEvents)
	for {
		select {
		case event, ok := <-eventsCh:
			if !ok {
				return &Result{
					Success: true,
					Data: map[string]any{
						"session_id":  sessionID,
						"event_count": len(events),
						"events":      events,
					},
				}, nil
			}
			eventData, err := marshalToMap(event)
			if err != nil {
				return toolError(err), nil
			}
			events = append(events, eventData)
			if len(events) >= maxEvents {
				cancel()
			}
		case <-streamCtx.Done():
			return &Result{
				Success: true,
				Data: map[string]any{
					"session_id":  sessionID,
					"event_count": len(events),
					"events":      events,
				},
			}, nil
		}
	}
}

// BrowserActTool sends an action to the browser session.
type BrowserActTool struct {
	Manager *browser.Manager
}

func (t *BrowserActTool) Name() string {
	return "browser_act"
}

func (t *BrowserActTool) Description() string {
	return "Send an action (click, type, scroll, hover, key, focus, clipboard_read, clipboard_write) to the browser session."
}

func (t *BrowserActTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"session_id": {
				Type:        "string",
				Description: "Browser session identifier",
			},
			"action": {
				Type:        "object",
				Description: "Action payload (type, target, text, key, scroll, modifiers, expected_state_version)",
			},
		},
		Required: []string{"session_id", "action"},
	}
}

func (t *BrowserActTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BrowserActTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	manager, err := resolveBrowserManager(t.Manager)
	if err != nil {
		return toolError(err), nil
	}
	sessionID := strings.TrimSpace(parseBrowserString(params, "session_id"))
	if sessionID == "" {
		return toolError(fmt.Errorf("session_id is required")), nil
	}
	action, err := parseBrowserAction(params["action"])
	if err != nil {
		return toolError(err), nil
	}
	sess, ok := manager.GetSession(sessionID)
	if !ok {
		return toolError(fmt.Errorf("session not found: %s", sessionID)), nil
	}
	result, err := sess.Act(ctx, action)
	if err != nil {
		return toolError(err), nil
	}
	resultData, err := marshalToMap(result)
	if err != nil {
		return toolError(err), nil
	}
	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id":    sessionID,
			"state_version": result.StateVersion,
			"result":        resultData,
		},
	}, nil
}

// BrowserClipboardReadTool reads from the browser session clipboard.
type BrowserClipboardReadTool struct {
	Manager *browser.Manager
}

func (t *BrowserClipboardReadTool) Name() string {
	return "browser_clipboard_read"
}

func (t *BrowserClipboardReadTool) Description() string {
	return "Read the virtual browser clipboard for a session (requires approval)."
}

func (t *BrowserClipboardReadTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"session_id": {
				Type:        "string",
				Description: "Browser session identifier",
			},
			"expected_state_version": {
				Type:        "integer",
				Description: "Optional expected state version to guard against stale reads",
			},
		},
		Required: []string{"session_id"},
	}
}

func (t *BrowserClipboardReadTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BrowserClipboardReadTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	manager, err := resolveBrowserManager(t.Manager)
	if err != nil {
		return toolError(err), nil
	}
	sessionID := strings.TrimSpace(parseBrowserString(params, "session_id"))
	if sessionID == "" {
		return toolError(fmt.Errorf("session_id is required")), nil
	}
	action := browser.Action{
		Type:                 browser.ActionClipboardRead,
		ExpectedStateVersion: browser.StateVersion(parseBrowserInt(params, "expected_state_version", 0)),
	}
	sess, ok := manager.GetSession(sessionID)
	if !ok {
		return toolError(fmt.Errorf("session not found: %s", sessionID)), nil
	}
	result, err := sess.Act(ctx, action)
	if err != nil {
		return toolError(err), nil
	}
	resultData, err := marshalToMap(result)
	if err != nil {
		return toolError(err), nil
	}
	text, bytes, source := extractClipboardMeta(result, "clipboard_read")
	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id":       sessionID,
			"state_version":    result.StateVersion,
			"clipboard_text":   text,
			"clipboard_bytes":  bytes,
			"clipboard_source": source,
			"result":           resultData,
		},
	}, nil
}

// BrowserClipboardWriteTool writes to the browser session clipboard.
type BrowserClipboardWriteTool struct {
	Manager *browser.Manager
}

func (t *BrowserClipboardWriteTool) Name() string {
	return "browser_clipboard_write"
}

func (t *BrowserClipboardWriteTool) Description() string {
	return "Write text to the virtual browser clipboard for a session."
}

func (t *BrowserClipboardWriteTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"session_id": {
				Type:        "string",
				Description: "Browser session identifier",
			},
			"text": {
				Type:        "string",
				Description: "Clipboard text to store",
			},
			"expected_state_version": {
				Type:        "integer",
				Description: "Optional expected state version to guard against stale writes",
			},
		},
		Required: []string{"session_id", "text"},
	}
}

func (t *BrowserClipboardWriteTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BrowserClipboardWriteTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	manager, err := resolveBrowserManager(t.Manager)
	if err != nil {
		return toolError(err), nil
	}
	sessionID := strings.TrimSpace(parseBrowserString(params, "session_id"))
	if sessionID == "" {
		return toolError(fmt.Errorf("session_id is required")), nil
	}
	text := parseBrowserString(params, "text")
	action := browser.Action{
		Type:                 browser.ActionClipboardWrite,
		ExpectedStateVersion: browser.StateVersion(parseBrowserInt(params, "expected_state_version", 0)),
		Text:                 text,
	}
	sess, ok := manager.GetSession(sessionID)
	if !ok {
		return toolError(fmt.Errorf("session not found: %s", sessionID)), nil
	}
	result, err := sess.Act(ctx, action)
	if err != nil {
		return toolError(err), nil
	}
	resultData, err := marshalToMap(result)
	if err != nil {
		return toolError(err), nil
	}
	_, bytes, source := extractClipboardMeta(result, "clipboard_write")
	if bytes == 0 {
		bytes = len([]byte(text))
	}
	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id":       sessionID,
			"state_version":    result.StateVersion,
			"clipboard_bytes":  bytes,
			"clipboard_source": source,
			"result":           resultData,
		},
	}, nil
}

// BrowserCloseTool closes a browser session.
type BrowserCloseTool struct {
	Manager *browser.Manager
}

func (t *BrowserCloseTool) Name() string {
	return "browser_close"
}

func (t *BrowserCloseTool) Description() string {
	return "Close and clean up a browser session."
}

func (t *BrowserCloseTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"session_id": {
				Type:        "string",
				Description: "Browser session identifier",
			},
		},
		Required: []string{"session_id"},
	}
}

func (t *BrowserCloseTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BrowserCloseTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	manager, err := resolveBrowserManager(t.Manager)
	if err != nil {
		return toolError(err), nil
	}
	sessionID := strings.TrimSpace(parseBrowserString(params, "session_id"))
	if sessionID == "" {
		return toolError(fmt.Errorf("session_id is required")), nil
	}
	if err := manager.CloseSession(sessionID); err != nil {
		return toolError(err), nil
	}
	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id": sessionID,
			"closed":     true,
		},
	}, nil
}

func resolveBrowserManager(provided *browser.Manager) (*browser.Manager, error) {
	if provided != nil {
		return provided, nil
	}
	return defaultBrowserManagerForTools()
}

func defaultBrowserManagerForTools() (*browser.Manager, error) {
	defaultBrowserManagerOnce.Do(func() {
		cfg := servo.DefaultConfig()
		if val := strings.TrimSpace(os.Getenv("BROWSERD_PATH")); val != "" {
			cfg.BrowserdPath = val
		}
		if val := strings.TrimSpace(os.Getenv("BROWSERD_SOCKET_DIR")); val != "" {
			cfg.SocketDir = val
		}
		if val := strings.TrimSpace(os.Getenv("BROWSERD_FRAME_RATE")); val != "" {
			cfg.FrameRate = parseBrowserIntValue(val, cfg.FrameRate)
		}
		if val := strings.TrimSpace(os.Getenv("BROWSERD_CONNECT_TIMEOUT")); val != "" {
			cfg.ConnectTimeout = parseBrowserDurationValue(val, cfg.ConnectTimeout)
		}
		runtime, err := servo.NewRuntime(cfg)
		if err != nil {
			defaultBrowserManagerErr = err
			return
		}
		defaultBrowserManager = browser.NewManager(runtime)
	})
	return defaultBrowserManager, defaultBrowserManagerErr
}

func toolError(err error) *Result {
	return &Result{
		Success: false,
		Error:   err.Error(),
	}
}

func marshalToMap(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseBrowserString(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	if val, ok := params[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

func parseBrowserInt(params map[string]any, key string, defaultVal int) int {
	if params == nil {
		return defaultVal
	}
	return parseBrowserIntValue(params[key], defaultVal)
}

func parseBrowserIntValue(value any, defaultVal int) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		if strings.TrimSpace(v) == "" {
			return defaultVal
		}
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return defaultVal
		}
		return i
	default:
		return defaultVal
	}
}

func parseBrowserBool(params map[string]any, key string, defaultVal bool) bool {
	if params == nil {
		return defaultVal
	}
	switch v := params[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return defaultVal
}

func parseBrowserFloat(value any, defaultVal float64) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		if strings.TrimSpace(v) == "" {
			return defaultVal
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return defaultVal
		}
		return f
	default:
		return defaultVal
	}
}

func parseBrowserViewport(value any, fallback browser.Viewport) browser.Viewport {
	viewport := fallback
	data, ok := value.(map[string]any)
	if !ok {
		return viewport
	}
	if width, ok := data["width"]; ok {
		viewport.Width = parseBrowserIntValue(width, viewport.Width)
	}
	if height, ok := data["height"]; ok {
		viewport.Height = parseBrowserIntValue(height, viewport.Height)
	}
	if scale, ok := data["device_scale_factor"]; ok {
		viewport.DeviceScaleFactor = parseBrowserFloat(scale, viewport.DeviceScaleFactor)
	}
	return viewport
}

func parseBrowserStringSlice(params map[string]any, key string) []string {
	if params == nil {
		return nil
	}
	value, ok := params[key]
	if !ok {
		return nil
	}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{strings.TrimSpace(v)}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func parseBrowserDurationValue(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(value); err == nil {
		return parsed
	}
	if millis, err := strconv.Atoi(value); err == nil {
		return time.Duration(millis) * time.Millisecond
	}
	return fallback
}

func parseBrowserAction(value any) (browser.Action, error) {
	payload, ok := value.(map[string]any)
	if !ok {
		return browser.Action{}, fmt.Errorf("action must be an object")
	}
	actionType := strings.ToLower(strings.TrimSpace(parseBrowserString(payload, "type")))
	if actionType == "" {
		return browser.Action{}, fmt.Errorf("action.type is required")
	}
	action := browser.Action{
		Type:                 parseBrowserActionType(actionType),
		ExpectedStateVersion: browser.StateVersion(parseBrowserIntValue(payload["expected_state_version"], 0)),
		Text:                 strings.TrimSpace(parseBrowserString(payload, "text")),
		Key:                  strings.TrimSpace(parseBrowserString(payload, "key")),
	}
	if action.Type == "" {
		return browser.Action{}, fmt.Errorf("unknown action type: %s", actionType)
	}
	if targetVal, ok := payload["target"]; ok {
		action.Target = parseBrowserActionTarget(targetVal)
	}
	if scrollVal, ok := payload["scroll"]; ok {
		action.Scroll = parseBrowserScroll(scrollVal)
	}
	if modsVal, ok := payload["modifiers"]; ok {
		action.Modifiers = parseBrowserModifiers(modsVal)
	}
	return action, nil
}

func parseBrowserActionType(value string) browser.ActionType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "click":
		return browser.ActionClick
	case "type":
		return browser.ActionTypeText
	case "scroll":
		return browser.ActionScroll
	case "hover":
		return browser.ActionHover
	case "key":
		return browser.ActionKey
	case "focus":
		return browser.ActionFocus
	case "clipboard_read":
		return browser.ActionClipboardRead
	case "clipboard_write":
		return browser.ActionClipboardWrite
	default:
		return ""
	}
}

func extractClipboardMeta(result *browser.ActionResult, kind string) (string, int, string) {
	if result == nil {
		return "", 0, ""
	}
	for _, effect := range result.Effects {
		if effect.Kind != kind {
			continue
		}
		text, _ := effect.Metadata["text"].(string)
		bytes := parseBrowserIntValue(effect.Metadata["bytes"], 0)
		source, _ := effect.Metadata["source"].(string)
		if bytes == 0 && text != "" {
			bytes = len([]byte(text))
		}
		return text, bytes, source
	}
	return "", 0, ""
}

func parseBrowserActionTarget(value any) *browser.ActionTarget {
	target := &browser.ActionTarget{}
	data, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if node, ok := data["node_id"]; ok {
		target.NodeID = uint64(parseBrowserIntValue(node, 0))
	}
	if pointVal, ok := data["point"]; ok {
		if point := parseBrowserPoint(pointVal); point != nil {
			target.Point = point
		}
	}
	if target.NodeID == 0 && target.Point == nil {
		return nil
	}
	return target
}

func parseBrowserPoint(value any) *browser.Point {
	data, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return &browser.Point{
		X: parseBrowserIntValue(data["x"], 0),
		Y: parseBrowserIntValue(data["y"], 0),
	}
}

func parseBrowserScroll(value any) *browser.ScrollDelta {
	data, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	unit := strings.ToLower(strings.TrimSpace(parseBrowserString(data, "unit")))
	return &browser.ScrollDelta{
		X:    parseBrowserIntValue(data["x"], 0),
		Y:    parseBrowserIntValue(data["y"], 0),
		Unit: parseBrowserScrollUnit(unit),
	}
}

func parseBrowserScrollUnit(value string) browser.ScrollUnit {
	switch value {
	case "pixels":
		return browser.ScrollUnitPixels
	case "lines":
		return browser.ScrollUnitLines
	default:
		return ""
	}
}

func parseBrowserModifiers(value any) []browser.KeyModifier {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]browser.KeyModifier, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "shift":
			out = append(out, browser.KeyModifierShift)
		case "alt":
			out = append(out, browser.KeyModifierAlt)
		case "ctrl", "control":
			out = append(out, browser.KeyModifierCtrl)
		case "meta", "cmd", "command":
			out = append(out, browser.KeyModifierMeta)
		}
	}
	return out
}
