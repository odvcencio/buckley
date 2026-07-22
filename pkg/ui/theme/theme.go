// Package theme provides a unified visual design system for Buckley's TUI.
// The default palette is intentionally restrained: graphite surfaces, cool
// blue focus, and neutral message text keep long agent sessions readable.
package theme

import (
	"m31labs.dev/buckley/pkg/ui/compositor"
)

// Theme defines the complete visual language for the TUI.
type Theme struct {
	// Core palette
	Background    compositor.Style // Primary canvas
	Surface       compositor.Style // Elevated surfaces (cards, panels)
	SurfaceRaised compositor.Style // Higher elevation
	SurfaceDim    compositor.Style // Recessed areas

	// Text hierarchy
	TextPrimary   compositor.Style // Main content
	TextSecondary compositor.Style // Supporting text
	TextMuted     compositor.Style // Hints, placeholders
	TextInverse   compositor.Style // Text on accent backgrounds

	// Accent colors
	Accent     compositor.Style // Primary action, highlights
	AccentDim  compositor.Style // Subtle accent usage
	AccentGlow compositor.Style // Emphasis, active states

	// Semantic colors
	Success compositor.Style
	Warning compositor.Style
	Error   compositor.Style
	Info    compositor.Style

	// Message sources
	User      compositor.Style
	Assistant compositor.Style
	System    compositor.Style
	Tool      compositor.Style
	Thinking  compositor.Style

	// UI elements
	Border      compositor.Style
	BorderFocus compositor.Style
	Selection   compositor.Style
	SearchMatch compositor.Style
	Scrollbar   compositor.Style
	ScrollThumb compositor.Style

	// Mode indicators
	ModeNormal compositor.Style
	ModeShell  compositor.Style
	ModeEnv    compositor.Style
	ModeSearch compositor.Style

	// Special
	Logo    compositor.Style
	Spinner compositor.Style
}

// DefaultTheme returns Buckley's graphite theme.
func DefaultTheme() *Theme {
	return &Theme{
		Background:    compositor.DefaultStyle().WithBG(compositor.RGB(10, 12, 15)),
		Surface:       compositor.DefaultStyle().WithBG(compositor.RGB(16, 19, 24)),
		SurfaceRaised: compositor.DefaultStyle().WithBG(compositor.RGB(23, 27, 34)),
		SurfaceDim:    compositor.DefaultStyle().WithBG(compositor.RGB(7, 9, 12)),

		TextPrimary:   compositor.DefaultStyle().WithFG(compositor.RGB(220, 226, 234)),
		TextSecondary: compositor.DefaultStyle().WithFG(compositor.RGB(151, 161, 175)),
		TextMuted:     compositor.DefaultStyle().WithFG(compositor.RGB(94, 104, 118)),
		TextInverse:   compositor.DefaultStyle().WithFG(compositor.RGB(10, 12, 15)),

		Accent:     compositor.DefaultStyle().WithFG(compositor.RGB(122, 162, 247)),
		AccentDim:  compositor.DefaultStyle().WithFG(compositor.RGB(82, 112, 173)),
		AccentGlow: compositor.DefaultStyle().WithFG(compositor.RGB(160, 190, 255)).WithBold(true),

		// Semantic colors
		Success: compositor.DefaultStyle().WithFG(compositor.RGB(158, 206, 106)),
		Warning: compositor.DefaultStyle().WithFG(compositor.RGB(224, 175, 104)),
		Error:   compositor.DefaultStyle().WithFG(compositor.RGB(247, 118, 142)),
		Info:    compositor.DefaultStyle().WithFG(compositor.RGB(125, 207, 255)),

		User:      compositor.DefaultStyle().WithFG(compositor.RGB(125, 207, 255)),
		Assistant: compositor.DefaultStyle().WithFG(compositor.RGB(220, 226, 234)),
		System:    compositor.DefaultStyle().WithFG(compositor.RGB(151, 161, 175)).WithItalic(true),
		Tool:      compositor.DefaultStyle().WithFG(compositor.RGB(187, 154, 247)),
		Thinking:  compositor.DefaultStyle().WithFG(compositor.RGB(94, 104, 118)).WithItalic(true),

		// UI elements
		Border:      compositor.DefaultStyle().WithFG(compositor.RGB(43, 49, 59)),
		BorderFocus: compositor.DefaultStyle().WithFG(compositor.RGB(122, 162, 247)),
		Selection:   compositor.DefaultStyle().WithBG(compositor.RGB(40, 52, 76)),
		SearchMatch: compositor.DefaultStyle().WithBG(compositor.RGB(89, 67, 28)).WithFG(compositor.RGB(238, 242, 247)),
		Scrollbar:   compositor.DefaultStyle().WithFG(compositor.RGB(43, 49, 59)),
		ScrollThumb: compositor.DefaultStyle().WithFG(compositor.RGB(94, 104, 118)),

		// Mode indicators
		ModeNormal: compositor.DefaultStyle().WithFG(compositor.RGB(151, 161, 175)),
		ModeShell:  compositor.DefaultStyle().WithFG(compositor.RGB(158, 206, 106)).WithBold(true),
		ModeEnv:    compositor.DefaultStyle().WithFG(compositor.RGB(125, 207, 255)).WithBold(true),
		ModeSearch: compositor.DefaultStyle().WithFG(compositor.RGB(224, 175, 104)).WithBold(true),

		// Special
		Logo:    compositor.DefaultStyle().WithFG(compositor.RGB(122, 162, 247)).WithBold(true),
		Spinner: compositor.DefaultStyle().WithFG(compositor.RGB(122, 162, 247)),
	}
}

// Symbols provides consistent iconography.
var Symbols = struct {
	// Bullets and markers
	Bullet      string
	BulletEmpty string
	Arrow       string
	ArrowRight  string
	ArrowDown   string
	Check       string
	Cross       string
	Dot         string
	Ring        string

	// Borders (rounded)
	BorderTopLeft     string
	BorderTopRight    string
	BorderBottomLeft  string
	BorderBottomRight string
	BorderHorizontal  string
	BorderVertical    string

	// UI elements
	Spinner      []string
	Progress     string
	ProgressFill string
	Scrollbar    string
	ScrollThumb  string

	// Message prefixes
	User      string
	Assistant string
	System    string
	Tool      string
	Thinking  string

	// Mode indicators
	ModeNormal string
	ModeShell  string
	ModeEnv    string
	ModeSearch string

	// File types
	FileDefault  string
	FileFolder   string
	FileGo       string
	FileJS       string
	FileTS       string
	FilePython   string
	FileMarkdown string
	FileYAML     string
	FileJSON     string
}{
	// Bullets and markers
	Bullet:      "●",
	BulletEmpty: "○",
	Arrow:       "›",
	ArrowRight:  "→",
	ArrowDown:   "↓",
	Check:       "✓",
	Cross:       "✗",
	Dot:         "·",
	Ring:        "◌",

	// Borders (rounded for softer feel)
	BorderTopLeft:     "╭",
	BorderTopRight:    "╮",
	BorderBottomLeft:  "╰",
	BorderBottomRight: "╯",
	BorderHorizontal:  "─",
	BorderVertical:    "│",

	// UI elements
	Spinner:      []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	Progress:     "░",
	ProgressFill: "█",
	Scrollbar:    "░",
	ScrollThumb:  "█",

	// Message prefixes
	User:      "❯",
	Assistant: "●",
	System:    "◆",
	Tool:      "◇",
	Thinking:  "…",

	// Mode indicators
	ModeNormal: "λ",
	ModeShell:  "!",
	ModeEnv:    "$",
	ModeSearch: "/",

	// File types
	FileDefault:  "◇",
	FileFolder:   "▸",
	FileGo:       "◈",
	FileJS:       "◆",
	FileTS:       "◆",
	FilePython:   "◈",
	FileMarkdown: "◇",
	FileYAML:     "◇",
	FileJSON:     "◇",
}

// Layout defines standard spacing and dimensions.
var Layout = struct {
	// Padding
	PaddingXS int
	PaddingSM int
	PaddingMD int
	PaddingLG int
	PaddingXL int

	// Margins
	MarginXS int
	MarginSM int
	MarginMD int
	MarginLG int
	MarginXL int

	// Component dimensions
	HeaderHeight    int
	StatusHeight    int
	InputMinHeight  int
	PickerMaxHeight int
	ScrollbarWidth  int
}{
	// Padding
	PaddingXS: 1,
	PaddingSM: 2,
	PaddingMD: 3,
	PaddingLG: 4,
	PaddingXL: 6,

	// Margins
	MarginXS: 1,
	MarginSM: 2,
	MarginMD: 3,
	MarginLG: 4,
	MarginXL: 6,

	// Component dimensions
	HeaderHeight:    1,
	StatusHeight:    1,
	InputMinHeight:  3,
	PickerMaxHeight: 15,
	ScrollbarWidth:  1,
}
