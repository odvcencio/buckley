// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/widgets"
)

func (a *WidgetApp) contextMenuActive() bool {
	if a == nil || a.screen == nil || a.contextMenuOverlay == nil {
		return false
	}
	top := a.screen.TopLayer()
	if top == nil {
		return false
	}
	return top.Root == a.contextMenuOverlay
}

func (a *WidgetApp) dismissContextMenu() {
	if !a.contextMenuActive() {
		return
	}
	_ = a.screen.PopLayer()
	a.contextMenuOverlay = nil
	a.contextMenuPanel = nil
	a.contextMenu = nil
	a.dirty = true
}

func (a *WidgetApp) showContextMenu(x, y int) bool {
	if a == nil || a.screen == nil {
		return false
	}
	if a.contextMenuActive() {
		a.dismissContextMenu()
	}
	items := a.buildContextMenuItems(x, y)
	if len(items) == 0 {
		return false
	}
	menu := widgets.NewMenu(items...)
	menu.SetStyle(a.style(a.theme.SurfaceRaised))
	menu.SetSelectedStyle(a.style(a.theme.Selection))
	menu.Focus()

	panel := widgets.NewPanel(menu)
	panel.SetTitle("Actions")
	panel.SetStyle(a.style(a.theme.SurfaceRaised))
	panel.WithBorder(a.style(a.theme.Border))

	overlay := buckleywidgets.NewPositionedOverlay(panel, x, y)
	a.contextMenu = menu
	a.contextMenuPanel = panel
	a.contextMenuOverlay = overlay
	a.screen.PushLayer(overlay, true)
	a.dirty = true
	return true
}

func (a *WidgetApp) buildContextMenuItems(x, y int) []*widgets.MenuItem {
	items := make([]*widgets.MenuItem, 0, 6)

	hasSelection := a.chatView != nil && a.chatView.HasSelection()
	items = append(items, &widgets.MenuItem{
		ID:       "copy-selection",
		Title:    "Copy selection",
		Shortcut: "Ctrl+Shift+C",
		Disabled: !hasSelection,
		OnSelect: func() {
			if !hasSelection || a.chatView == nil {
				return
			}
			text := strings.TrimSpace(a.chatView.SelectionText())
			if text == "" {
				a.setStatusOverride("No selection to copy", 2*time.Second)
				return
			}
			if err := a.writeClipboard(text); err != nil {
				a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
				return
			}
			a.setStatusOverride("Selection copied", 2*time.Second)
			a.dismissContextMenu()
		},
	})
	items = append(items, &widgets.MenuItem{
		ID:       "clear-selection",
		Title:    "Clear selection",
		Disabled: !hasSelection,
		OnSelect: func() {
			if a.chatView != nil {
				a.chatView.ClearSelection()
			}
			a.selectionActive = false
			a.selectionLastValid = false
			a.dismissContextMenu()
		},
	})

	var codeLang string
	var code string
	hasCode := false
	if a.chatView != nil {
		if action, language, value, ok := a.chatView.CodeHeaderActionAtPoint(x, y); ok && action == "copy" {
			codeLang = language
			code = value
			hasCode = true
		} else if language, value, ok := a.chatView.LatestCodeBlock(); ok {
			codeLang = language
			code = value
			hasCode = true
		}
	}
	if hasCode {
		title := "Copy code block"
		if strings.TrimSpace(codeLang) != "" {
			title = "Copy " + codeLang + " code"
		}
		items = append(items, &widgets.MenuItem{
			ID:       "copy-code",
			Title:    title,
			Shortcut: "Alt+C",
			OnSelect: func() {
				if err := a.writeClipboard(code); err != nil {
					a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
				} else if strings.TrimSpace(codeLang) == "" {
					a.setStatusOverride("Copied code block", 2*time.Second)
				} else {
					a.setStatusOverride("Copied "+codeLang+" code", 2*time.Second)
				}
				a.dismissContextMenu()
			},
		})
	}

	if a.chatView != nil {
		if action, _, _, ok := a.chatView.CodeHeaderActionAtPoint(x, y); ok && action == "open" {
			items = append(items, &widgets.MenuItem{
				ID:    "open-code",
				Title: "Open code in web",
				OnSelect: func() {
					a.openWebTarget("code", false)
					a.dismissContextMenu()
				},
			})
		}
	}

	items = append(items, &widgets.MenuItem{
		ID:    "scroll-top",
		Title: "Scroll to top",
		OnSelect: func() {
			if a.chatView != nil {
				a.chatView.ScrollToTop()
				a.updateScrollStatus()
			}
			a.dismissContextMenu()
		},
	})
	items = append(items, &widgets.MenuItem{
		ID:    "scroll-bottom",
		Title: "Jump to latest",
		OnSelect: func() {
			a.jumpToLatest()
			a.dismissContextMenu()
		},
	})
	items = append(items, &widgets.MenuItem{
		ID:    "toggle-sidebar",
		Title: "Toggle sidebar",
		OnSelect: func() {
			a.toggleSidebar()
			a.dismissContextMenu()
		},
	})
	items = append(items, &widgets.MenuItem{
		ID:       "ui-settings",
		Title:    "UI settings",
		Shortcut: "Ctrl+,",
		OnSelect: func() {
			a.showSettingsDialog()
			a.dismissContextMenu()
		},
	})

	return items
}
