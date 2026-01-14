package widgets

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
	"github.com/odvcencio/buckley/pkg/ui/toast"
)

const (
	toastMaxWidth = 60
	toastPaddingX = 1
	toastSpacing  = 1
	toastMargin   = 1
	toastMinWidth = 20
)

type toastRect struct {
	id     string
	bounds runtime.Rect
}

// ToastStack renders toast notifications.
type ToastStack struct {
	Base
	toasts     []*toast.Toast
	onDismiss  func(id string)
	toastRects []toastRect

	bgStyle      backend.Style
	textStyle    backend.Style
	infoStyle    backend.Style
	successStyle backend.Style
	warnStyle    backend.Style
	errorStyle   backend.Style
}

// NewToastStack creates a new toast stack widget.
func NewToastStack() *ToastStack {
	return &ToastStack{
		bgStyle:      backend.DefaultStyle(),
		textStyle:    backend.DefaultStyle(),
		infoStyle:    backend.DefaultStyle(),
		successStyle: backend.DefaultStyle(),
		warnStyle:    backend.DefaultStyle(),
		errorStyle:   backend.DefaultStyle(),
	}
}

// SetToasts updates the toast list.
func (t *ToastStack) SetToasts(toasts []*toast.Toast) {
	t.toasts = toasts
}

// SetOnDismiss registers a handler for dismiss actions.
func (t *ToastStack) SetOnDismiss(fn func(id string)) {
	t.onDismiss = fn
}

// SetStyles configures the toast styles by level.
func (t *ToastStack) SetStyles(bg, text, info, success, warn, err backend.Style) {
	t.bgStyle = bg
	t.textStyle = text
	t.infoStyle = info
	t.successStyle = success
	t.warnStyle = warn
	t.errorStyle = err
}

// Measure fills the available space.
func (t *ToastStack) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight}
}

// Render draws the toast stack.
func (t *ToastStack) Render(ctx runtime.RenderContext) {
	bounds := t.bounds
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}

	t.toastRects = t.toastRects[:0]
	if len(t.toasts) == 0 {
		return
	}

	availableWidth := bounds.Width - 2*toastMargin
	if availableWidth <= 0 {
		return
	}
	maxWidth := minInt(toastMaxWidth, availableWidth)
	if maxWidth < toastMinWidth {
		maxWidth = availableWidth
	}
	if maxWidth <= 0 {
		return
	}

	y := bounds.Y + bounds.Height - 1 - toastMargin
	for i := len(t.toasts) - 1; i >= 0; i-- {
		toast := t.toasts[i]
		if toast == nil {
			continue
		}
		lines, prefix := t.toastLines(toast, maxWidth-2*toastPaddingX)
		if len(lines) == 0 {
			continue
		}
		width := maxLineLen(lines) + 2*toastPaddingX
		if width < toastMinWidth {
			width = toastMinWidth
		}
		if width > maxWidth {
			width = maxWidth
		}
		height := len(lines)

		yStart := y - height + 1
		if yStart < bounds.Y {
			break
		}
		x := bounds.X + bounds.Width - width - toastMargin
		rect := runtime.Rect{X: x, Y: yStart, Width: width, Height: height}
		t.toastRects = append(t.toastRects, toastRect{id: toast.ID, bounds: rect})

		for lineIdx, line := range lines {
			row := runtime.Rect{X: rect.X, Y: rect.Y + lineIdx, Width: rect.Width, Height: 1}
			ctx.Buffer.Fill(row, ' ', t.bgStyle)
			if line == "" {
				continue
			}
			startX := rect.X + toastPaddingX
			if lineIdx == 0 && prefix != "" {
				ctx.Buffer.SetString(startX, row.Y, prefix, t.levelStyle(toast.Level))
				ctx.Buffer.SetString(startX+len(prefix), row.Y, line[len(prefix):], t.textStyle)
			} else {
				ctx.Buffer.SetString(startX, row.Y, line, t.textStyle)
			}
		}

		y = yStart - toastSpacing
		if y < bounds.Y {
			break
		}
	}
}

// HandleMessage handles dismiss clicks.
func (t *ToastStack) HandleMessage(msg runtime.Message) runtime.HandleResult {
	mouse, ok := msg.(runtime.MouseMsg)
	if !ok {
		return runtime.Unhandled()
	}
	if mouse.Action != runtime.MouseRelease || mouse.Button != runtime.MouseLeft {
		return runtime.Unhandled()
	}
	for _, rect := range t.toastRects {
		if rect.bounds.Contains(mouse.X, mouse.Y) {
			if t.onDismiss != nil {
				t.onDismiss(rect.id)
			}
			return runtime.Handled()
		}
	}
	return runtime.Unhandled()
}

func (t *ToastStack) toastLines(toast *toast.Toast, maxWidth int) ([]string, string) {
	title := strings.TrimSpace(toast.Title)
	if title == "" {
		title = levelLabel(toast.Level)
	}
	prefix := levelIcon(toast.Level) + " "
	contentWidth := maxWidth - len(prefix)
	if contentWidth < 0 {
		contentWidth = 0
	}
	titleLine := prefix + truncateString(title, contentWidth)

	lines := []string{titleLine}

	message := strings.TrimSpace(toast.Message)
	if message != "" {
		if toast.Action != nil && strings.TrimSpace(toast.Action.Label) != "" {
			message = message + " [" + strings.TrimSpace(toast.Action.Label) + "]"
		}
		lines = append(lines, truncateString(message, maxWidth))
	}

	return lines, prefix
}

func (t *ToastStack) levelStyle(level toast.ToastLevel) backend.Style {
	switch level {
	case toast.ToastSuccess:
		return t.successStyle
	case toast.ToastWarning:
		return t.warnStyle
	case toast.ToastError:
		return t.errorStyle
	default:
		return t.infoStyle
	}
}

func levelLabel(level toast.ToastLevel) string {
	switch level {
	case toast.ToastSuccess:
		return "Success"
	case toast.ToastWarning:
		return "Warning"
	case toast.ToastError:
		return "Error"
	default:
		return "Info"
	}
}

func levelIcon(level toast.ToastLevel) string {
	switch level {
	case toast.ToastSuccess:
		return "+"
	case toast.ToastWarning:
		return "!"
	case toast.ToastError:
		return "x"
	default:
		return "i"
	}
}

func maxLineLen(lines []string) int {
	max := 0
	for _, line := range lines {
		if len(line) > max {
			max = len(line)
		}
	}
	return max
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
