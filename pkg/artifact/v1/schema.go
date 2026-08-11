package artifactv1

import (
	"encoding/json"
	"fmt"
	"sort"
)

// FieldSpec is a compatibility-relevant top-level wire field. Nested
// structures evolve only in a new schema version; this descriptor catches the
// breaking changes that would otherwise make old persisted artifacts unreadable.
type FieldSpec struct {
	Type     string
	Required bool
}

// SchemaDescriptor describes the compatibility surface of an artifact schema.
type SchemaDescriptor struct {
	Version string
	Fields  map[string]FieldSpec
}

// CurrentSchemaDescriptor returns a detached description of Artifact v1.
func CurrentSchemaDescriptor() SchemaDescriptor {
	return SchemaDescriptor{
		Version: SchemaVersion,
		Fields: map[string]FieldSpec{
			"schema_version":     {Type: "string", Required: true},
			"artifact_id":        {Type: "string", Required: true},
			"kind":               {Type: "string", Required: true},
			"status":             {Type: "string", Required: true},
			"title":              {Type: "string", Required: true},
			"summary":            {Type: "string", Required: true},
			"blocks":             {Type: "array", Required: false},
			"findings":           {Type: "array", Required: false},
			"diagnostics":        {Type: "array", Required: false},
			"evidence_refs":      {Type: "array", Required: false},
			"next_actions":       {Type: "array", Required: false},
			"incomplete_reasons": {Type: "array", Required: false},
			"metadata":           {Type: "object", Required: false},
		},
	}
}

// CompatibilityError groups every breaking field change in a proposed schema.
type CompatibilityError struct {
	Reasons []string
}

// Error implements error.
func (e *CompatibilityError) Error() string {
	if e == nil || len(e.Reasons) == 0 {
		return "artifact schema is incompatible"
	}
	if len(e.Reasons) == 1 {
		return "artifact schema is incompatible: " + e.Reasons[0]
	}
	return fmt.Sprintf("artifact schema is incompatible: %d breaking changes", len(e.Reasons))
}

// CheckBackwardCompatibility rejects changes that would prevent a v1 reader
// from interpreting data accepted by the previous descriptor. Adding optional
// fields is safe; removing a field, changing its type, or making an optional
// field newly required is not.
func CheckBackwardCompatibility(previous, candidate SchemaDescriptor) error {
	reasons := make([]string, 0)
	if previous.Version == "" {
		reasons = append(reasons, "previous version is required")
	}
	if candidate.Version == "" {
		reasons = append(reasons, "candidate version is required")
	}
	for field, oldSpec := range previous.Fields {
		newSpec, ok := candidate.Fields[field]
		if !ok {
			reasons = append(reasons, "removes field "+field)
			continue
		}
		if oldSpec.Type != newSpec.Type {
			reasons = append(reasons, "changes type of field "+field)
		}
		if oldSpec.Required != newSpec.Required {
			if oldSpec.Required {
				reasons = append(reasons, "makes required field optional: "+field)
			} else {
				reasons = append(reasons, "makes optional field required: "+field)
			}
		}
	}
	for field, spec := range candidate.Fields {
		if _, existed := previous.Fields[field]; !existed && spec.Required {
			reasons = append(reasons, "adds required field "+field)
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	sort.Strings(reasons)
	return &CompatibilityError{Reasons: reasons}
}

// JSONSchema returns a detached Draft 2020-12 schema for native structured
// output providers. The schema mirrors the strict Go validator; the validator
// remains the authority for safety limits and semantic checks after decoding.
func JSONSchema() map[string]any {
	definitions := map[string]any{
		"evidence_ref": evidenceRefSchema(),
		"location":     locationSchema(),
		"finding":      findingSchema(),
		"diagnostic":   diagnosticSchema(),
		"next_action":  nextActionSchema(),
		"block":        blockSchema(),
	}
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  "https://buckley.dev/schemas/artifact/v1.json",
		"title":                "Buckley Artifact v1",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schema_version", "artifact_id", "kind", "status", "title", "summary"},
		"properties": map[string]any{
			"schema_version": map[string]any{"const": SchemaVersion},
			"artifact_id":    stringSchema(),
			"kind":           stringSchema(),
			"status": map[string]any{"enum": []string{
				string(StatusDraft), string(StatusInProgress), string(StatusCompleted), string(StatusFailed), string(StatusBlocked), string(StatusIncomplete),
			}},
			"title":              stringSchema(),
			"summary":            stringSchema(),
			"blocks":             map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/block"}},
			"findings":           map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/finding"}},
			"diagnostics":        map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/diagnostic"}},
			"evidence_refs":      map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/evidence_ref"}},
			"next_actions":       map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/next_action"}},
			"incomplete_reasons": map[string]any{"type": "array", "items": stringSchema()},
			"metadata":           map[string]any{"type": "object", "additionalProperties": stringSchema()},
		},
		"$defs": definitions,
	}
}

// JSONSchemaBytes returns the canonical JSON encoding of JSONSchema. Go's
// encoder sorts map keys, making the result suitable for cache keys and tests.
func JSONSchemaBytes() ([]byte, error) {
	return json.Marshal(JSONSchema())
}

func stringSchema() map[string]any {
	return map[string]any{"type": "string"}
}

func evidenceRefSchema() map[string]any {
	return objectSchema(
		[]string{"id"},
		map[string]any{
			"id":    stringSchema(),
			"label": stringSchema(),
			"kind":  stringSchema(),
			"uri":   stringSchema(),
		},
	)
}

func locationSchema() map[string]any {
	return objectSchema(nil, map[string]any{
		"path":       stringSchema(),
		"start_line": map[string]any{"type": "integer", "minimum": 0},
		"end_line":   map[string]any{"type": "integer", "minimum": 0},
		"symbol":     stringSchema(),
	})
}

func findingSchema() map[string]any {
	return objectSchema(
		[]string{"id", "severity", "confidence", "title", "summary"},
		map[string]any{
			"id":             stringSchema(),
			"severity":       map[string]any{"enum": []string{"critical", "high", "medium", "low", "info"}},
			"confidence":     map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			"title":          stringSchema(),
			"summary":        stringSchema(),
			"location":       map[string]any{"$ref": "#/$defs/location"},
			"recommendation": stringSchema(),
			"evidence_refs":  evidenceRefArraySchema(),
		},
	)
}

func diagnosticSchema() map[string]any {
	return objectSchema(
		[]string{"level", "message"},
		map[string]any{
			"level":    map[string]any{"enum": []string{"error", "warning", "info"}},
			"code":     stringSchema(),
			"message":  stringSchema(),
			"location": map[string]any{"$ref": "#/$defs/location"},
		},
	)
}

func nextActionSchema() map[string]any {
	return objectSchema(
		[]string{"description"},
		map[string]any{
			"id":            stringSchema(),
			"description":   stringSchema(),
			"priority":      stringSchema(),
			"evidence_refs": evidenceRefArraySchema(),
		},
	)
}

func blockSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			objectSchema([]string{"kind", "text"}, map[string]any{"kind": map[string]any{"const": string(BlockProse)}, "text": stringSchema()}),
			objectSchema([]string{"kind", "text", "level"}, map[string]any{"kind": map[string]any{"const": string(BlockHeading)}, "text": stringSchema(), "level": map[string]any{"type": "integer", "minimum": 1, "maximum": 6}}),
			objectSchema([]string{"kind", "facts"}, map[string]any{"kind": map[string]any{"const": string(BlockFacts)}, "facts": factArraySchema()}),
			objectSchema([]string{"kind", "table"}, map[string]any{"kind": map[string]any{"const": string(BlockTable)}, "table": tableSchema()}),
			objectSchema([]string{"kind", "code"}, map[string]any{"kind": map[string]any{"const": string(BlockCode)}, "code": codeSchema()}),
			objectSchema([]string{"kind", "diff"}, map[string]any{"kind": map[string]any{"const": string(BlockDiff)}, "diff": diffSchema()}),
			objectSchema([]string{"kind", "checklist"}, map[string]any{"kind": map[string]any{"const": string(BlockChecklist)}, "checklist": checklistSchema()}),
			objectSchema([]string{"kind", "finding"}, map[string]any{"kind": map[string]any{"const": string(BlockFinding)}, "finding": map[string]any{"$ref": "#/$defs/finding"}}),
			objectSchema([]string{"kind", "operation"}, map[string]any{"kind": map[string]any{"const": string(BlockOperationSummary)}, "operation": operationSchema()}),
			objectSchema([]string{"kind", "evidence"}, map[string]any{"kind": map[string]any{"const": string(BlockEvidenceLink)}, "evidence": map[string]any{"$ref": "#/$defs/evidence_ref"}}),
		},
	}
}

func factArraySchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": objectSchema([]string{"label", "value"}, map[string]any{
			"label": stringSchema(),
			"value": stringSchema(),
		}),
	}
}

func tableSchema() map[string]any {
	return objectSchema([]string{"headers"}, map[string]any{
		"headers": map[string]any{"type": "array", "items": stringSchema()},
		"rows":    map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": stringSchema()}},
	})
}

func codeSchema() map[string]any {
	return objectSchema([]string{"content"}, map[string]any{
		"language": stringSchema(),
		"content":  stringSchema(),
	})
}

func diffSchema() map[string]any {
	return objectSchema([]string{"content"}, map[string]any{
		"path":    stringSchema(),
		"content": stringSchema(),
	})
}

func checklistSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": objectSchema([]string{"text"}, map[string]any{
			"text":   stringSchema(),
			"state":  stringSchema(),
			"detail": stringSchema(),
		}),
	}
}

func operationSchema() map[string]any {
	return objectSchema([]string{"operation", "status"}, map[string]any{
		"operation":     stringSchema(),
		"status":        stringSchema(),
		"duration_ms":   map[string]any{"type": "integer", "minimum": 0},
		"detail":        stringSchema(),
		"metrics":       factArraySchema(),
		"evidence_refs": evidenceRefArraySchema(),
	})
}

func evidenceRefArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/evidence_ref"}}
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
