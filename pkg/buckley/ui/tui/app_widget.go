// Package tui provides the integrated terminal user interface for Buckley.
//
// This package has been decomposed into multiple files for better maintainability:
//
//   - app_types.go: Shared types (RenderMetrics, layoutSpec) and layout utilities
//   - app_core.go: WidgetApp struct, NewWidgetApp constructor, Run loop, lifecycle
//   - app_render.go: Render loop, animation updates, metrics tracking
//   - app_input.go: Keyboard/mouse handling, keybindings, input dispatch
//   - app_commands.go: Command palette, handlers, slash commands
//   - app_layout.go: Layout calculations, sidebar management
//   - app_agent.go: Agent server initialization
//   - app_helpers.go: Utility functions, web URLs, status, public API
//
// This file (app_widget.go) serves as the package entry point documentation.
package tui
