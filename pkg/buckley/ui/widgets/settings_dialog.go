package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/forms"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// SettingsValues represents configurable UI settings.
type SettingsValues struct {
	Theme           string
	StylesheetPath  string
	MessageMetadata string
	HighContrast    bool
	ReduceMotion    bool
	EffectsEnabled  bool
}

// SettingsDialogConfig configures the settings dialog.
type SettingsDialogConfig struct {
	Values   SettingsValues
	OnSubmit func(values SettingsValues)
	OnCancel func()
	Width    int
}

type settingsFieldKind int

const (
	settingsFieldSelect settingsFieldKind = iota
	settingsFieldText
	settingsFieldBool
)

type settingsField struct {
	key     string
	label   string
	kind    settingsFieldKind
	options []string
}

// SettingsDialog presents editable UI settings with form validation.
type SettingsDialog struct {
	uiwidgets.FocusableBase

	form     *forms.Form
	fields   []settingsField
	selected int
	editing  bool
	editText string
	errors   []string

	width int

	onSubmit func(values SettingsValues)
	onCancel func()

	bgStyle       backend.Style
	borderStyle   backend.Style
	labelStyle    backend.Style
	textStyle     backend.Style
	selectedStyle backend.Style
	hintStyle     backend.Style
	errorStyle    backend.Style
}

// NewSettingsDialog creates a settings dialog.
func NewSettingsDialog(cfg SettingsDialogConfig) *SettingsDialog {
	values := cfg.Values
	if strings.TrimSpace(values.Theme) == "" {
		values.Theme = "dark"
	}
	if strings.TrimSpace(values.MessageMetadata) == "" {
		values.MessageMetadata = "always"
	}
	form := forms.NewForm(
		forms.NewField("theme", values.Theme, forms.OneOf("dark", "light")),
		forms.NewField("stylesheet", values.StylesheetPath),
		forms.NewField("metadata", values.MessageMetadata, forms.OneOf("always", "hover", "never")),
		forms.NewField("high_contrast", values.HighContrast),
		forms.NewField("reduce_motion", values.ReduceMotion),
		forms.NewField("effects", values.EffectsEnabled),
	)

	dialog := &SettingsDialog{
		form: form,
		fields: []settingsField{
			{key: "theme", label: "Theme", kind: settingsFieldSelect, options: []string{"dark", "light"}},
			{key: "metadata", label: "Metadata", kind: settingsFieldSelect, options: []string{"always", "hover", "never"}},
			{key: "stylesheet", label: "Stylesheet", kind: settingsFieldText},
			{key: "high_contrast", label: "High Contrast", kind: settingsFieldBool},
			{key: "reduce_motion", label: "Reduce Motion", kind: settingsFieldBool},
			{key: "effects", label: "Effects", kind: settingsFieldBool},
		},
		selected:      0,
		editing:       false,
		width:         cfg.Width,
		onSubmit:      cfg.OnSubmit,
		onCancel:      cfg.OnCancel,
		bgStyle:       backend.DefaultStyle(),
		borderStyle:   backend.DefaultStyle(),
		labelStyle:    backend.DefaultStyle().Bold(true),
		textStyle:     backend.DefaultStyle(),
		selectedStyle: backend.DefaultStyle().Reverse(true),
		hintStyle:     backend.DefaultStyle().Dim(true),
		errorStyle:    backend.DefaultStyle().Foreground(backend.ColorRed),
	}
	if dialog.width <= 0 {
		dialog.width = 64
	}
	dialog.Base.Role = accessibility.RoleDialog
	dialog.Base.State.Modal = true
	return dialog
}

// SetStyles configures dialog styles.
func (d *SettingsDialog) SetStyles(bg, border, label, text, selected, hint, err backend.Style) {
	if d == nil {
		return
	}
	d.bgStyle = bg
	d.borderStyle = border
	d.labelStyle = label
	d.textStyle = text
	d.selectedStyle = selected
	d.hintStyle = hint
	d.errorStyle = err
}

// Measure returns preferred size.
func (d *SettingsDialog) Measure(constraints runtime.Constraints) runtime.Size {
	height := len(d.fields) + 6
	if height < 8 {
		height = 8
	}
	width := d.width
	if width > constraints.MaxWidth {
		width = constraints.MaxWidth
	}
	if width < constraints.MinWidth {
		width = constraints.MinWidth
	}
	if height > constraints.MaxHeight {
		height = constraints.MaxHeight
	}
	if height < constraints.MinHeight {
		height = constraints.MinHeight
	}
	return runtime.Size{Width: width, Height: height}
}

// Layout centers the dialog within bounds.
func (d *SettingsDialog) Layout(bounds runtime.Rect) {
	size := d.Measure(runtime.Constraints{
		MaxWidth:  bounds.Width,
		MaxHeight: bounds.Height,
	})
	x := bounds.X + (bounds.Width-size.Width)/2
	y := bounds.Y + (bounds.Height-size.Height)/2
	d.FocusableBase.Layout(runtime.Rect{X: x, Y: y, Width: size.Width, Height: size.Height})
}

// Render draws the settings dialog.
func (d *SettingsDialog) Render(ctx runtime.RenderContext) {
	b := d.Bounds()
	if b.Width < 30 || b.Height < 8 {
		return
	}
	ctx.Buffer.Fill(b, ' ', d.bgStyle)
	d.drawBorder(ctx.Buffer, b)

	title := " Settings "
	titleX := b.X + (b.Width-textWidth(title))/2
	ctx.Buffer.SetString(titleX, b.Y, title, d.labelStyle)

	y := b.Y + 2
	maxY := b.Y + b.Height - 3
	for i, field := range d.fields {
		if y > maxY {
			break
		}
		rowStyle := d.textStyle
		if i == d.selected {
			rowStyle = d.selectedStyle
			ctx.Buffer.Fill(runtime.Rect{X: b.X + 1, Y: y, Width: b.Width - 2, Height: 1}, ' ', rowStyle)
		}
		label := field.label + ":"
		ctx.Buffer.SetString(b.X+2, y, label, d.labelStyle)

		value := d.fieldValue(field)
		if d.editing && i == d.selected && field.kind == settingsFieldText {
			value = d.editText + "|"
		}
		valueX := b.X + 2 + textWidth(label) + 1
		maxValue := b.Width - (valueX - b.X) - 2
		if maxValue < 0 {
			maxValue = 0
		}
		if len([]rune(value)) > maxValue {
			value = truncateString(value, maxValue)
		}
		ctx.Buffer.SetString(valueX, y, value, rowStyle)
		y++
	}

	hint := "Enter edit/toggle · ↑/↓ move · ←/→ cycle · Ctrl+S save · Esc close"
	footerY := b.Y + b.Height - 2
	footerStyle := d.hintStyle
	if len(d.errors) > 0 {
		hint = d.errors[0]
		footerStyle = d.errorStyle
	}
	if len(hint) > b.Width-4 {
		hint = truncateString(hint, b.Width-4)
	}
	ctx.Buffer.SetString(b.X+2, footerY, hint, footerStyle)
}

// HandleMessage processes input for settings editing.
func (d *SettingsDialog) HandleMessage(msg runtime.Message) runtime.HandleResult {
	switch ev := msg.(type) {
	case runtime.MouseMsg:
		return d.handleMouse(ev)
	case runtime.KeyMsg:
		return d.handleKey(ev)
	default:
		return runtime.Unhandled()
	}
}

func (d *SettingsDialog) handleMouse(ev runtime.MouseMsg) runtime.HandleResult {
	if ev.Action != runtime.MousePress || ev.Button != runtime.MouseLeft {
		return runtime.Unhandled()
	}
	b := d.Bounds()
	if !b.Contains(ev.X, ev.Y) {
		return runtime.Unhandled()
	}
	row := ev.Y - (b.Y + 2)
	if row >= 0 && row < len(d.fields) {
		d.selected = row
		field := d.fields[row]
		switch field.kind {
		case settingsFieldBool:
			d.toggleBool(field.key)
		case settingsFieldSelect:
			d.cycleSelect(field, 1)
		}
		d.Invalidate()
		return runtime.Handled()
	}
	return runtime.Unhandled()
}

func (d *SettingsDialog) handleKey(ev runtime.KeyMsg) runtime.HandleResult {
	if d == nil {
		return runtime.Unhandled()
	}
	if ev.Key == terminal.KeyEscape {
		if d.editing {
			d.editing = false
			d.editText = ""
			d.Invalidate()
			return runtime.Handled()
		}
		if d.onCancel != nil {
			d.onCancel()
		}
		return runtime.WithCommand(runtime.PopOverlay{})
	}
	if ev.Key == terminal.KeyRune && ev.Ctrl && (ev.Rune == 's' || ev.Rune == 'S') {
		return d.submit()
	}
	switch ev.Key {
	case terminal.KeyUp:
		if !d.editing && d.selected > 0 {
			d.selected--
			d.Invalidate()
		}
		return runtime.Handled()
	case terminal.KeyDown:
		if !d.editing && d.selected < len(d.fields)-1 {
			d.selected++
			d.Invalidate()
		}
		return runtime.Handled()
	case terminal.KeyLeft:
		if d.editing {
			return runtime.Handled()
		}
		d.cycleSelect(d.fields[d.selected], -1)
		d.Invalidate()
		return runtime.Handled()
	case terminal.KeyRight:
		if d.editing {
			return runtime.Handled()
		}
		d.cycleSelect(d.fields[d.selected], 1)
		d.Invalidate()
		return runtime.Handled()
	case terminal.KeyEnter:
		field := d.fields[d.selected]
		switch field.kind {
		case settingsFieldBool:
			d.toggleBool(field.key)
			d.Invalidate()
			return runtime.Handled()
		case settingsFieldSelect:
			d.cycleSelect(field, 1)
			d.Invalidate()
			return runtime.Handled()
		case settingsFieldText:
			if d.editing {
				d.commitEdit(field.key)
				d.Invalidate()
				return runtime.Handled()
			}
			d.beginEdit(field.key)
			d.Invalidate()
			return runtime.Handled()
		}
	case terminal.KeyBackspace:
		if d.editing {
			if len(d.editText) > 0 {
				runes := []rune(d.editText)
				d.editText = string(runes[:len(runes)-1])
				d.Invalidate()
			}
			return runtime.Handled()
		}
	case terminal.KeyRune:
		if d.editing {
			d.editText += string(ev.Rune)
			d.Invalidate()
			return runtime.Handled()
		}
	}
	return runtime.Unhandled()
}

func (d *SettingsDialog) beginEdit(key string) {
	d.editing = true
	if value, ok := d.form.Get(key).(string); ok {
		d.editText = value
	} else {
		d.editText = ""
	}
}

func (d *SettingsDialog) commitEdit(key string) {
	d.editing = false
	d.form.Set(key, d.editText)
	d.editText = ""
}

func (d *SettingsDialog) toggleBool(key string) {
	value, _ := d.form.Get(key).(bool)
	d.form.Set(key, !value)
}

func (d *SettingsDialog) cycleSelect(field settingsField, delta int) {
	if field.kind != settingsFieldSelect || len(field.options) == 0 {
		return
	}
	current, _ := d.form.Get(field.key).(string)
	idx := 0
	for i, opt := range field.options {
		if opt == current {
			idx = i
			break
		}
	}
	idx += delta
	if idx < 0 {
		idx = len(field.options) - 1
	}
	if idx >= len(field.options) {
		idx = 0
	}
	d.form.Set(field.key, field.options[idx])
}

func (d *SettingsDialog) fieldValue(field settingsField) string {
	switch field.kind {
	case settingsFieldBool:
		value, _ := d.form.Get(field.key).(bool)
		if value {
			return "on"
		}
		return "off"
	case settingsFieldSelect, settingsFieldText:
		value, _ := d.form.Get(field.key).(string)
		if strings.TrimSpace(value) == "" {
			return "(none)"
		}
		return value
	default:
		return ""
	}
}

func (d *SettingsDialog) submit() runtime.HandleResult {
	d.errors = nil
	errs := d.form.Validate()
	if len(errs) > 0 {
		for _, err := range errs {
			if strings.TrimSpace(err.Message) != "" {
				d.errors = append(d.errors, err.Message)
			}
		}
		d.Invalidate()
		return runtime.Handled()
	}
	values := SettingsValues{
		Theme:           stringOrDefault(d.form.Get("theme")),
		StylesheetPath:  stringOrDefault(d.form.Get("stylesheet")),
		MessageMetadata: stringOrDefault(d.form.Get("metadata")),
		HighContrast:    boolOrDefault(d.form.Get("high_contrast")),
		ReduceMotion:    boolOrDefault(d.form.Get("reduce_motion")),
		EffectsEnabled:  boolOrDefault(d.form.Get("effects")),
	}
	if d.onSubmit != nil {
		d.onSubmit(values)
	}
	return runtime.WithCommand(runtime.PopOverlay{})
}

func stringOrDefault(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func boolOrDefault(value any) bool {
	if value == nil {
		return false
	}
	if b, ok := value.(bool); ok {
		return b
	}
	return false
}

func (d *SettingsDialog) drawBorder(buf *runtime.Buffer, b runtime.Rect) {
	buf.Set(b.X, b.Y, '+', d.borderStyle)
	buf.Set(b.X+b.Width-1, b.Y, '+', d.borderStyle)
	buf.Set(b.X, b.Y+b.Height-1, '+', d.borderStyle)
	buf.Set(b.X+b.Width-1, b.Y+b.Height-1, '+', d.borderStyle)
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		buf.Set(x, b.Y, '-', d.borderStyle)
		buf.Set(x, b.Y+b.Height-1, '-', d.borderStyle)
	}
	for y := b.Y + 1; y < b.Y+b.Height-1; y++ {
		buf.Set(b.X, y, '|', d.borderStyle)
		buf.Set(b.X+b.Width-1, y, '|', d.borderStyle)
	}
}

func (d *SettingsDialog) AccessibleRole() accessibility.Role {
	return accessibility.RoleDialog
}

func (d *SettingsDialog) AccessibleLabel() string {
	return "Settings"
}

func (d *SettingsDialog) AccessibleDescription() string {
	return "Edit interface preferences"
}

func (d *SettingsDialog) AccessibleState() accessibility.StateSet {
	return accessibility.StateSet{}
}

func (d *SettingsDialog) AccessibleValue() *accessibility.ValueInfo {
	return nil
}

var _ runtime.Widget = (*SettingsDialog)(nil)
var _ runtime.Focusable = (*SettingsDialog)(nil)
var _ accessibility.Accessible = (*SettingsDialog)(nil)
