// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"time"
)

// ============================================================================
// FILE: app_types.go
// PURPOSE: Shared types, constants, and layout utilities
// FUNCTIONS:
//   - layoutForWidth
//   - layoutForScreen
// ============================================================================

// RenderMetrics tracks rendering performance statistics.
type RenderMetrics struct {
	FrameCount      int64         // Total frames rendered
	DroppedFrames   int64         // Frames skipped due to being too slow
	TotalRenderTime time.Duration // Total time spent rendering
	LastFrameTime   time.Duration // Duration of last frame
	CellsUpdated    int64         // Cells updated in last frame
	FullRedraws     int64         // Number of full screen redraws
	PartialRedraws  int64         // Number of partial redraws
}

const (
	minInputHeight       = 2
	minChatHeight        = 4
	sidebarStandardWidth = 25
	sidebarWideWidth     = 35
	sidebarMinWidth      = 120
	sidebarWideMinWidth  = 160
)

type layoutSpec struct {
	sidebarVisible  bool
	presenceVisible bool
	sidebarWidth    int
	showHeader      bool
	showStatus      bool
}

func layoutForWidth(width int, hasSidebarContent bool) layoutSpec {
	if !hasSidebarContent {
		return layoutSpec{}
	}
	switch {
	case width >= sidebarWideMinWidth:
		return layoutSpec{sidebarVisible: true, sidebarWidth: sidebarWideWidth}
	case width >= sidebarMinWidth:
		return layoutSpec{sidebarVisible: true, sidebarWidth: sidebarStandardWidth}
	default:
		return layoutSpec{presenceVisible: true}
	}
}

func layoutForScreen(width, height int, hasSidebarContent, focusMode bool) layoutSpec {
	if focusMode {
		return layoutSpec{showHeader: false, showStatus: false}
	}
	spec := layoutForWidth(width, hasSidebarContent)
	spec.showHeader = height >= 20
	spec.showStatus = height >= 20
	return spec
}
