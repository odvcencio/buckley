package browser

import (
	"encoding/json"
	"time"
)

// StateVersion tracks the browser state revision for staleness checks.
type StateVersion uint64

// Viewport defines the browser viewport size.
type Viewport struct {
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty"`
}

// FrameFormat identifies the image format for a frame payload.
type FrameFormat string

const (
	FrameFormatPNG  FrameFormat = "png"
	FrameFormatJPEG FrameFormat = "jpeg"
	FrameFormatWebP FrameFormat = "webp"
)

// Frame is a single visual frame from the browser.
type Frame struct {
	StateVersion StateVersion `json:"state_version"`
	Width        int          `json:"width"`
	Height       int          `json:"height"`
	Format       FrameFormat  `json:"format"`
	Data         []byte       `json:"data,omitempty"`
	Timestamp    time.Time    `json:"timestamp"`
}

// HitTestMap maps screen regions to node identifiers.
type HitTestMap struct {
	Width   int         `json:"width"`
	Height  int         `json:"height"`
	Regions []HitRegion `json:"regions,omitempty"`
}

// HitRegion identifies a node region for hit-testing.
type HitRegion struct {
	NodeID uint64 `json:"node_id"`
	Bounds Rect   `json:"bounds"`
}

// Rect describes a rectangle in viewport coordinates.
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Point describes a coordinate in viewport space.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Observation bundles the browser state returned to the agent.
type Observation struct {
	StateVersion      StateVersion    `json:"state_version"`
	URL               string          `json:"url,omitempty"`
	Title             string          `json:"title,omitempty"`
	Frame             *Frame          `json:"frame,omitempty"`
	DOMSnapshot       json.RawMessage `json:"dom_snapshot,omitempty"`
	AccessibilityTree json.RawMessage `json:"accessibility_tree,omitempty"`
	HitTest           *HitTestMap     `json:"hit_test,omitempty"`
	Timestamp         time.Time       `json:"timestamp"`
}

// StreamEventType indicates the type of streaming browser event.
type StreamEventType string

const (
	StreamEventFrame             StreamEventType = "frame"
	StreamEventDOMDiff           StreamEventType = "dom_diff"
	StreamEventAccessibilityDiff StreamEventType = "accessibility_diff"
	StreamEventHitTest           StreamEventType = "hit_test"
)

// StreamEvent is a streamed update from the browser runtime.
type StreamEvent struct {
	Type              StreamEventType `json:"type"`
	StateVersion      StateVersion    `json:"state_version"`
	Frame             *Frame          `json:"frame,omitempty"`
	DOMDiff           json.RawMessage `json:"dom_diff,omitempty"`
	AccessibilityDiff json.RawMessage `json:"accessibility_diff,omitempty"`
	HitTest           *HitTestMap     `json:"hit_test,omitempty"`
	Timestamp         time.Time       `json:"timestamp"`
}

// ObserveOptions tunes the observation payload.
type ObserveOptions struct {
	IncludeFrame         bool `json:"include_frame"`
	IncludeDOMSnapshot   bool `json:"include_dom_snapshot"`
	IncludeAccessibility bool `json:"include_accessibility"`
	IncludeHitTest       bool `json:"include_hit_test"`
}

// StreamOptions selects which events are streamed.
type StreamOptions struct {
	IncludeFrames             bool `json:"include_frames"`
	IncludeDOMDiffs           bool `json:"include_dom_diffs"`
	IncludeAccessibilityDiffs bool `json:"include_accessibility_diffs"`
	IncludeHitTest            bool `json:"include_hit_test"`
	TargetFPS                 int  `json:"target_fps,omitempty"`
}

// ActionType represents the supported browser actions.
type ActionType string

const (
	ActionClick          ActionType = "click"
	ActionTypeText       ActionType = "type"
	ActionScroll         ActionType = "scroll"
	ActionHover          ActionType = "hover"
	ActionKey            ActionType = "key"
	ActionFocus          ActionType = "focus"
	ActionClipboardRead  ActionType = "clipboard_read"
	ActionClipboardWrite ActionType = "clipboard_write"
)

// KeyModifier describes a keyboard modifier.
type KeyModifier string

const (
	KeyModifierShift KeyModifier = "shift"
	KeyModifierAlt   KeyModifier = "alt"
	KeyModifierCtrl  KeyModifier = "ctrl"
	KeyModifierMeta  KeyModifier = "meta"
)

// ScrollUnit describes how scroll deltas are interpreted.
type ScrollUnit string

const (
	ScrollUnitPixels ScrollUnit = "pixels"
	ScrollUnitLines  ScrollUnit = "lines"
)

// ScrollDelta captures a scroll action intent.
type ScrollDelta struct {
	X    int        `json:"x,omitempty"`
	Y    int        `json:"y,omitempty"`
	Unit ScrollUnit `json:"unit,omitempty"`
}

// ActionTarget identifies where an action should be applied.
type ActionTarget struct {
	NodeID uint64 `json:"node_id,omitempty"`
	Point  *Point `json:"point,omitempty"`
}

// Action is a user or agent request for a browser action.
type Action struct {
	Type                 ActionType    `json:"type"`
	ExpectedStateVersion StateVersion  `json:"expected_state_version,omitempty"`
	Target               *ActionTarget `json:"target,omitempty"`
	Text                 string        `json:"text,omitempty"`
	Key                  string        `json:"key,omitempty"`
	Scroll               *ScrollDelta  `json:"scroll,omitempty"`
	Modifiers            []KeyModifier `json:"modifiers,omitempty"`
}

// Effect summarizes a notable outcome from an action.
type Effect struct {
	Kind     string         `json:"kind"`
	Summary  string         `json:"summary,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ActionResult returns the updated state and effects after an action.
type ActionResult struct {
	StateVersion StateVersion `json:"state_version"`
	Observation  *Observation `json:"observation,omitempty"`
	Effects      []Effect     `json:"effects,omitempty"`
}

// ClipboardMode controls clipboard bridging policy.
type ClipboardMode string

const (
	ClipboardModeVirtual ClipboardMode = "virtual"
	ClipboardModeHost    ClipboardMode = "host"
)

// ClipboardPolicy defines clipboard access rules for a session.
type ClipboardPolicy struct {
	Mode          ClipboardMode `json:"mode"`
	AllowRead     bool          `json:"allow_read"`
	AllowWrite    bool          `json:"allow_write"`
	MaxBytes      int           `json:"max_bytes"`
	ReadAllowlist []string      `json:"read_allowlist,omitempty"`
}

// SessionConfig configures a browser session.
type SessionConfig struct {
	SessionID        string          `json:"session_id"`
	InitialURL       string          `json:"initial_url,omitempty"`
	Viewport         Viewport        `json:"viewport"`
	UserAgent        string          `json:"user_agent,omitempty"`
	Locale           string          `json:"locale,omitempty"`
	Timezone         string          `json:"timezone,omitempty"`
	FrameRate        int             `json:"frame_rate,omitempty"`
	NetworkAllowlist []string        `json:"network_allowlist,omitempty"`
	Clipboard        ClipboardPolicy `json:"clipboard"`
}

// DefaultSessionConfig returns the recommended session defaults.
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		Viewport: Viewport{
			Width:             1280,
			Height:            720,
			DeviceScaleFactor: 1.0,
		},
		FrameRate: 12,
		Clipboard: ClipboardPolicy{
			Mode:       ClipboardModeVirtual,
			AllowRead:  false,
			AllowWrite: true,
			MaxBytes:   64 * 1024,
		},
	}
}
