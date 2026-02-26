package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

type sidebarFiles struct {
	showRecentFiles bool
	showTouches     bool
	showLocks       bool

	recentFiles   []string
	activeTouches []TouchSummary
	fileLocks     []FileLockSummary
	projectPath   string

	breadcrumbPanel *breadcrumbPanel
	filesPanel      *filesPanel
	touchesPanel    *touchesPanel
	locksPanelInst  *locksPanel

	content *runtime.Flex
	scroll  *uiwidgets.ScrollView
}

func newSidebarFiles(border backend.Style) *sidebarFiles {
	files := &sidebarFiles{
		showRecentFiles: true,
		showTouches:     true,
		showLocks:       true,
	}
	files.breadcrumbPanel = newBreadcrumbPanel(border)
	files.filesPanel = newFilesPanel(border)
	files.touchesPanel = newTouchesPanel(border)
	files.locksPanelInst = newLocksPanel(border)
	files.content = files.buildContent()
	files.scroll = uiwidgets.NewScrollView(files.content)
	files.scroll.SetBehavior(scroll.ScrollBehavior{Vertical: scroll.ScrollAuto, Horizontal: scroll.ScrollNever, MouseWheel: 3, PageSize: 1})
	return files
}

func (f *sidebarFiles) ScrollView() *uiwidgets.ScrollView {
	if f == nil {
		return nil
	}
	return f.scroll
}

func (f *sidebarFiles) buildContent() *runtime.Flex {
	children := make([]runtime.FlexChild, 0, 4)
	if panel := f.breadcrumbPanel.Panel(); panel != nil {
		children = append(children, runtime.Fixed(panel))
	}
	if f.showRecentFiles {
		if panel := f.filesPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if f.showTouches {
		if panel := f.touchesPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if f.showLocks {
		if panel := f.locksPanelInst.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	return runtime.VBox(children...).WithGap(1)
}

func (f *sidebarFiles) rebuild() {
	if f == nil || f.scroll == nil {
		return
	}
	f.content = f.buildContent()
	f.scroll.SetContent(f.content)
}

func (f *sidebarFiles) ApplyVisibility(showRecentFiles, showTouches, showLocks bool) bool {
	if f == nil {
		return false
	}
	changed := false
	if f.showRecentFiles != showRecentFiles {
		f.showRecentFiles = showRecentFiles
		changed = true
	}
	if f.showTouches != showTouches {
		f.showTouches = showTouches
		changed = true
	}
	if f.showLocks != showLocks {
		f.showLocks = showLocks
		changed = true
	}
	if changed {
		f.rebuild()
	}
	return changed
}

func (f *sidebarFiles) ApplyProjectPath(path string) {
	if f == nil {
		return
	}
	path = strings.TrimSpace(path)
	if f.projectPath == path {
		return
	}
	f.projectPath = path
	f.updateBreadcrumb()
	f.updateFilesPanel()
}

func (f *sidebarFiles) SetStyles(border, background backend.Style) {
	if f == nil {
		return
	}
	if f.breadcrumbPanel != nil {
		f.breadcrumbPanel.SetStyles(border, background)
	}
	if f.filesPanel != nil {
		f.filesPanel.SetStyles(border, background)
	}
	if f.touchesPanel != nil {
		f.touchesPanel.SetStyles(border, background)
	}
	if f.locksPanelInst != nil {
		f.locksPanelInst.SetStyles(border, background)
	}
}

func (f *sidebarFiles) HasContent() bool {
	if f == nil {
		return false
	}
	if len(f.activeTouches) > 0 {
		return true
	}
	if len(f.recentFiles) > 0 {
		return true
	}
	if len(f.fileLocks) > 0 {
		return true
	}
	return false
}

func (f *sidebarFiles) applyActiveTouches(touches []TouchSummary) {
	if f == nil {
		return
	}
	f.activeTouches = touches
	f.updateTouchesPanel()
}

func (f *sidebarFiles) applyRecentFiles(files []string) {
	if f == nil {
		return
	}
	f.recentFiles = files
	f.updateFilesPanel()
}

func (f *sidebarFiles) applyFileLocks(locks []FileLockSummary) {
	if f == nil {
		return
	}
	f.fileLocks = locks
	f.updateLocksPanel()
}

func (f *sidebarFiles) updateAllPanels() {
	f.updateBreadcrumb()
	f.updateFilesPanel()
	f.updateTouchesPanel()
	f.updateLocksPanel()
}

func (f *sidebarFiles) updateBreadcrumb() {
	if f == nil || f.breadcrumbPanel == nil {
		return
	}
	f.breadcrumbPanel.Update(f.projectPath)
}

func (f *sidebarFiles) updateFilesPanel() {
	if f == nil || f.filesPanel == nil {
		return
	}
	f.filesPanel.Update(f.recentFiles, f.projectPath)
}

func (f *sidebarFiles) updateTouchesPanel() {
	if f == nil || f.touchesPanel == nil {
		return
	}
	f.touchesPanel.Update(f.activeTouches)
}

func (f *sidebarFiles) updateLocksPanel() {
	if f == nil || f.locksPanelInst == nil {
		return
	}
	f.locksPanelInst.Update(f.fileLocks)
}
