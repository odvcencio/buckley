package servo

import (
	"encoding/json"
	"time"

	"github.com/odvcencio/buckley/pkg/browser"
	browserdpb "github.com/odvcencio/buckley/pkg/browser/adapters/servo/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toProtoSessionConfig(cfg browser.SessionConfig) *browserdpb.SessionConfig {
	return &browserdpb.SessionConfig{
		SessionId:        cfg.SessionID,
		InitialUrl:       cfg.InitialURL,
		Viewport:         toProtoViewport(cfg.Viewport),
		UserAgent:        cfg.UserAgent,
		Locale:           cfg.Locale,
		Timezone:         cfg.Timezone,
		FrameRate:        uint32(cfg.FrameRate),
		NetworkAllowlist: cfg.NetworkAllowlist,
		Clipboard:        toProtoClipboard(cfg.Clipboard),
	}
}

func toProtoViewport(viewport browser.Viewport) *browserdpb.Viewport {
	return &browserdpb.Viewport{
		Width:             uint32(viewport.Width),
		Height:            uint32(viewport.Height),
		DeviceScaleFactor: viewport.DeviceScaleFactor,
	}
}

func toProtoClipboard(policy browser.ClipboardPolicy) *browserdpb.ClipboardPolicy {
	if !hasClipboardOverride(policy) {
		return nil
	}
	return &browserdpb.ClipboardPolicy{
		Mode:          toProtoClipboardMode(policy.Mode),
		AllowRead:     policy.AllowRead,
		AllowWrite:    policy.AllowWrite,
		MaxBytes:      uint32(policy.MaxBytes),
		ReadAllowlist: policy.ReadAllowlist,
	}
}

func toProtoClipboardMode(mode browser.ClipboardMode) browserdpb.ClipboardMode {
	switch mode {
	case browser.ClipboardModeVirtual:
		return browserdpb.ClipboardMode_CLIPBOARD_MODE_VIRTUAL
	case browser.ClipboardModeHost:
		return browserdpb.ClipboardMode_CLIPBOARD_MODE_HOST
	default:
		return browserdpb.ClipboardMode_CLIPBOARD_MODE_UNSPECIFIED
	}
}

func toProtoObserveOptions(opts browser.ObserveOptions) *browserdpb.ObserveOptions {
	return &browserdpb.ObserveOptions{
		IncludeFrame:         opts.IncludeFrame,
		IncludeDomSnapshot:   opts.IncludeDOMSnapshot,
		IncludeAccessibility: opts.IncludeAccessibility,
		IncludeHitTest:       opts.IncludeHitTest,
	}
}

func toProtoStreamOptions(opts browser.StreamOptions) *browserdpb.StreamOptions {
	return &browserdpb.StreamOptions{
		IncludeFrames:             opts.IncludeFrames,
		IncludeDomDiffs:           opts.IncludeDOMDiffs,
		IncludeAccessibilityDiffs: opts.IncludeAccessibilityDiffs,
		IncludeHitTest:            opts.IncludeHitTest,
		TargetFps:                 uint32(opts.TargetFPS),
	}
}

func toProtoAction(action browser.Action) *browserdpb.Action {
	return &browserdpb.Action{
		Type:                 toProtoActionType(action.Type),
		ExpectedStateVersion: uint64(action.ExpectedStateVersion),
		Target:               toProtoActionTarget(action.Target),
		Text:                 action.Text,
		Key:                  action.Key,
		Scroll:               toProtoScrollDelta(action.Scroll),
		Modifiers:            toProtoModifiers(action.Modifiers),
	}
}

func toProtoActionTarget(target *browser.ActionTarget) *browserdpb.ActionTarget {
	if target == nil {
		return nil
	}
	return &browserdpb.ActionTarget{
		NodeId: target.NodeID,
		Point:  toProtoPoint(target.Point),
	}
}

func toProtoPoint(point *browser.Point) *browserdpb.Point {
	if point == nil {
		return nil
	}
	return &browserdpb.Point{
		X: int32(point.X),
		Y: int32(point.Y),
	}
}

func toProtoScrollDelta(delta *browser.ScrollDelta) *browserdpb.ScrollDelta {
	if delta == nil {
		return nil
	}
	return &browserdpb.ScrollDelta{
		X:    int32(delta.X),
		Y:    int32(delta.Y),
		Unit: toProtoScrollUnit(delta.Unit),
	}
}

func toProtoScrollUnit(unit browser.ScrollUnit) browserdpb.ScrollUnit {
	switch unit {
	case browser.ScrollUnitPixels:
		return browserdpb.ScrollUnit_SCROLL_UNIT_PIXELS
	case browser.ScrollUnitLines:
		return browserdpb.ScrollUnit_SCROLL_UNIT_LINES
	default:
		return browserdpb.ScrollUnit_SCROLL_UNIT_UNSPECIFIED
	}
}

func toProtoActionType(actionType browser.ActionType) browserdpb.ActionType {
	switch actionType {
	case browser.ActionClick:
		return browserdpb.ActionType_ACTION_TYPE_CLICK
	case browser.ActionTypeText:
		return browserdpb.ActionType_ACTION_TYPE_TYPE
	case browser.ActionScroll:
		return browserdpb.ActionType_ACTION_TYPE_SCROLL
	case browser.ActionHover:
		return browserdpb.ActionType_ACTION_TYPE_HOVER
	case browser.ActionKey:
		return browserdpb.ActionType_ACTION_TYPE_KEY
	case browser.ActionFocus:
		return browserdpb.ActionType_ACTION_TYPE_FOCUS
	case browser.ActionClipboardRead:
		return browserdpb.ActionType_ACTION_TYPE_CLIPBOARD_READ
	case browser.ActionClipboardWrite:
		return browserdpb.ActionType_ACTION_TYPE_CLIPBOARD_WRITE
	default:
		return browserdpb.ActionType_ACTION_TYPE_UNSPECIFIED
	}
}

func toProtoModifiers(modifiers []browser.KeyModifier) []browserdpb.KeyModifier {
	if len(modifiers) == 0 {
		return nil
	}
	out := make([]browserdpb.KeyModifier, 0, len(modifiers))
	for _, modifier := range modifiers {
		out = append(out, toProtoModifier(modifier))
	}
	return out
}

func toProtoModifier(modifier browser.KeyModifier) browserdpb.KeyModifier {
	switch modifier {
	case browser.KeyModifierShift:
		return browserdpb.KeyModifier_KEY_MODIFIER_SHIFT
	case browser.KeyModifierAlt:
		return browserdpb.KeyModifier_KEY_MODIFIER_ALT
	case browser.KeyModifierCtrl:
		return browserdpb.KeyModifier_KEY_MODIFIER_CTRL
	case browser.KeyModifierMeta:
		return browserdpb.KeyModifier_KEY_MODIFIER_META
	default:
		return browserdpb.KeyModifier_KEY_MODIFIER_UNSPECIFIED
	}
}

func fromProtoObservation(obs *browserdpb.Observation) *browser.Observation {
	if obs == nil {
		return nil
	}
	return &browser.Observation{
		StateVersion:      browser.StateVersion(obs.StateVersion),
		URL:               obs.Url,
		Title:             obs.Title,
		Frame:             fromProtoFrame(obs.Frame),
		DOMSnapshot:       cloneRaw(obs.DomSnapshot),
		AccessibilityTree: cloneRaw(obs.AccessibilityTree),
		HitTest:           fromProtoHitTestMap(obs.HitTest),
		Timestamp:         timeFromProto(obs.Timestamp),
	}
}

func fromProtoStreamEvent(event *browserdpb.StreamEvent) *browser.StreamEvent {
	if event == nil {
		return nil
	}
	return &browser.StreamEvent{
		Type:              fromProtoStreamEventType(event.Type),
		StateVersion:      browser.StateVersion(event.StateVersion),
		Frame:             fromProtoFrame(event.Frame),
		DOMDiff:           cloneRaw(event.DomDiff),
		AccessibilityDiff: cloneRaw(event.AccessibilityDiff),
		HitTest:           fromProtoHitTestMap(event.HitTest),
		Timestamp:         timeFromProto(event.Timestamp),
	}
}

func fromProtoStreamEventType(eventType browserdpb.StreamEventType) browser.StreamEventType {
	switch eventType {
	case browserdpb.StreamEventType_STREAM_EVENT_TYPE_FRAME:
		return browser.StreamEventFrame
	case browserdpb.StreamEventType_STREAM_EVENT_TYPE_DOM_DIFF:
		return browser.StreamEventDOMDiff
	case browserdpb.StreamEventType_STREAM_EVENT_TYPE_ACCESSIBILITY_DIFF:
		return browser.StreamEventAccessibilityDiff
	case browserdpb.StreamEventType_STREAM_EVENT_TYPE_HIT_TEST:
		return browser.StreamEventHitTest
	default:
		return ""
	}
}

func fromProtoFrame(frame *browserdpb.Frame) *browser.Frame {
	if frame == nil {
		return nil
	}
	return &browser.Frame{
		StateVersion: browser.StateVersion(frame.StateVersion),
		Width:        int(frame.Width),
		Height:       int(frame.Height),
		Format:       fromProtoFrameFormat(frame.Format),
		Data:         frame.Data,
		Timestamp:    timeFromProto(frame.Timestamp),
	}
}

func fromProtoFrameFormat(format browserdpb.FrameFormat) browser.FrameFormat {
	switch format {
	case browserdpb.FrameFormat_FRAME_FORMAT_PNG:
		return browser.FrameFormatPNG
	case browserdpb.FrameFormat_FRAME_FORMAT_JPEG:
		return browser.FrameFormatJPEG
	case browserdpb.FrameFormat_FRAME_FORMAT_WEBP:
		return browser.FrameFormatWebP
	default:
		return ""
	}
}

func fromProtoHitTestMap(hit *browserdpb.HitTestMap) *browser.HitTestMap {
	if hit == nil {
		return nil
	}
	regions := make([]browser.HitRegion, 0, len(hit.Regions))
	for _, region := range hit.Regions {
		regions = append(regions, browser.HitRegion{
			NodeID: region.NodeId,
			Bounds: fromProtoRect(region.Bounds),
		})
	}
	return &browser.HitTestMap{
		Width:   int(hit.Width),
		Height:  int(hit.Height),
		Regions: regions,
	}
}

func fromProtoRect(rect *browserdpb.Rect) browser.Rect {
	if rect == nil {
		return browser.Rect{}
	}
	return browser.Rect{
		X:      int(rect.X),
		Y:      int(rect.Y),
		Width:  int(rect.Width),
		Height: int(rect.Height),
	}
}

func fromProtoActionResult(result *browserdpb.ActionResult) *browser.ActionResult {
	if result == nil {
		return nil
	}
	effects := make([]browser.Effect, 0, len(result.Effects))
	for _, effect := range result.Effects {
		effects = append(effects, fromProtoEffect(effect))
	}
	return &browser.ActionResult{
		StateVersion: browser.StateVersion(result.StateVersion),
		Observation:  fromProtoObservation(result.Observation),
		Effects:      effects,
	}
}

func fromProtoEffect(effect *browserdpb.Effect) browser.Effect {
	if effect == nil {
		return browser.Effect{}
	}
	metadata := map[string]any{}
	if effect.Metadata != nil {
		metadata = effect.Metadata.AsMap()
	}
	return browser.Effect{
		Kind:     effect.Kind,
		Summary:  effect.Summary,
		Metadata: metadata,
	}
}

func cloneRaw(data []byte) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func timeToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func timeFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func toProtoMetadata(meta map[string]any) *structpb.Struct {
	if len(meta) == 0 {
		return nil
	}
	structVal, err := structpb.NewStruct(meta)
	if err != nil {
		return nil
	}
	return structVal
}
