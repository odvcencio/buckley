package artifactv1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// OutputMode tells a model adapter how it must request a typed artifact.
type OutputMode string

const (
	OutputNativeJSONSchema OutputMode = "native_json_schema"
	OutputSubmitArtifact   OutputMode = "submit_artifact"
	OutputPromptJSON       OutputMode = "prompt_json"
)

// ProviderCapabilities are facts supplied by a model adapter rather than
// model-name conditionals. The protocol compiler can therefore give cheap and
// frontier providers the strongest contract each one actually supports.
type ProviderCapabilities struct {
	NativeJSONSchema bool
	ToolCalls        bool
}

// OutputContract is a fully inspectable request contract. JSONSchema and the
// submit_artifact schema are maps so provider adapters can convert them to
// their SDK's native request types without importing the artifact package's
// transport assumptions.
type OutputContract struct {
	Mode           OutputMode
	SchemaVersion  string
	JSONSchema     map[string]any
	SubmitArtifact *SubmitArtifactContract
	Prompt         string
}

// SubmitArtifactContract describes the forced tool-call fallback used when a
// provider cannot honor a native response schema.
type SubmitArtifactContract struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// NegotiatedOutput selects native JSON/schema first, then a forced
// submit_artifact tool call, and finally an explicit prompt JSON contract for
// providers that expose neither capability.
func NegotiatedOutput(capabilities ProviderCapabilities) OutputContract {
	contract := OutputContract{
		SchemaVersion: SchemaVersion,
		Prompt:        "Return exactly one JSON object conforming to buckley.artifact/v1. Do not wrap it in Markdown or add prose outside the object.",
	}
	switch {
	case capabilities.NativeJSONSchema:
		contract.Mode = OutputNativeJSONSchema
		contract.JSONSchema = JSONSchema()
	case capabilities.ToolCalls:
		contract.Mode = OutputSubmitArtifact
		contract.SubmitArtifact = submitArtifactContract(true)
	default:
		contract.Mode = OutputPromptJSON
	}
	return contract
}

// NegotiatedOutputDescriptor returns the stable, small receipt form of an
// output contract. The full schema remains available through NegotiatedOutput
// for provider requests, while planners and durable receipts avoid allocating
// and serializing a large schema tree on every task.
func NegotiatedOutputDescriptor(capabilities ProviderCapabilities) OutputContract {
	contract := OutputContract{
		SchemaVersion: SchemaVersion,
		Prompt:        "Return exactly one JSON object conforming to buckley.artifact/v1. Do not wrap it in Markdown or add prose outside the object.",
	}
	switch {
	case capabilities.NativeJSONSchema:
		contract.Mode = OutputNativeJSONSchema
	case capabilities.ToolCalls:
		contract.Mode = OutputSubmitArtifact
		contract.SubmitArtifact = submitArtifactContract(false)
	default:
		contract.Mode = OutputPromptJSON
	}
	return contract
}

func submitArtifactContract(includeParameters bool) *SubmitArtifactContract {
	contract := &SubmitArtifactContract{
		Name:        "submit_artifact",
		Description: "submit the final Buckley Artifact v1 result exactly once",
	}
	if includeParameters {
		contract.Parameters = map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"artifact"},
			"properties": map[string]any{
				"artifact": JSONSchema(),
			},
		}
	}
	return contract
}

// Repairer performs one bounded external repair attempt. An adapter can use a
// small, constrained model call; it must return only a replacement JSON body.
type Repairer interface {
	RepairArtifact(ctx context.Context, invalid []byte, diagnostics []Diagnostic) ([]byte, error)
}

// RepairFunc adapts a function to Repairer.
type RepairFunc func(context.Context, []byte, []Diagnostic) ([]byte, error)

// RepairArtifact implements Repairer.
func (f RepairFunc) RepairArtifact(ctx context.Context, invalid []byte, diagnostics []Diagnostic) ([]byte, error) {
	return f(ctx, invalid, diagnostics)
}

// DecodeOptions bounds provider-output parsing and optional repair work.
type DecodeOptions struct {
	MaxBytes          int
	MaxRepairAttempts int
	Repairer          Repairer
}

// DecodeReport records exactly how an artifact became valid. It can be stored
// in evidence or a run ledger without exposing the raw provider body.
type DecodeReport struct {
	Mode        OutputMode
	Attempts    int
	Repaired    bool
	RepairNotes []string
}

// DecodeProviderOutput accepts either a direct artifact object or a
// submit_artifact invocation. It validates strict JSON first, performs one
// local omission/fence repair, then invokes an optional repairer at most the
// caller's bounded attempt count.
func DecodeProviderOutput(ctx context.Context, raw []byte, mode OutputMode, options DecodeOptions) (Artifact, DecodeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	maxBytes := options.MaxBytes
	if maxBytes <= 0 || maxBytes > MaxProviderBytes {
		maxBytes = MaxProviderBytes
	}
	if len(raw) == 0 {
		return Artifact{}, DecodeReport{Mode: mode}, fmt.Errorf("decode artifact output: empty provider response")
	}
	if len(raw) > maxBytes {
		return Artifact{}, DecodeReport{Mode: mode}, fmt.Errorf("decode artifact output: response exceeds %d-byte limit", maxBytes)
	}

	report := DecodeReport{Mode: mode}
	artifact, diagnostics, err := decodeAndValidate(raw)
	if err == nil {
		return artifact, report, nil
	}

	lastRaw := raw
	lastDiagnostics := diagnostics
	maxAttempts := options.MaxRepairAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	if maxAttempts > 3 {
		maxAttempts = 3
	}

	if repaired, notes, repairedDiagnostics, repairErr := repairLocally(raw); repairErr == nil {
		report.Attempts++
		report.Repaired = true
		report.RepairNotes = append(report.RepairNotes, notes...)
		return repaired, report, nil
	} else if len(repairedDiagnostics) > 0 {
		lastDiagnostics = repairedDiagnostics
	}

	for report.Attempts < maxAttempts && options.Repairer != nil {
		report.Attempts++
		repairedRaw, repairErr := options.Repairer.RepairArtifact(ctx, lastRaw, StableDiagnostics(lastDiagnostics))
		if repairErr != nil {
			report.RepairNotes = append(report.RepairNotes, "repairer returned an error")
			continue
		}
		if len(repairedRaw) == 0 || len(repairedRaw) > maxBytes {
			report.RepairNotes = append(report.RepairNotes, "repairer returned an empty or oversized response")
			continue
		}
		artifact, diagnostics, err = decodeAndValidate(repairedRaw)
		if err == nil {
			report.Repaired = true
			report.RepairNotes = append(report.RepairNotes, "external repair accepted")
			return artifact, report, nil
		}
		lastRaw = repairedRaw
		lastDiagnostics = diagnostics
	}
	if len(lastDiagnostics) == 0 {
		return Artifact{}, report, fmt.Errorf("decode artifact output: %w", err)
	}
	return Artifact{}, report, &ValidationError{Diagnostics: StableDiagnostics(lastDiagnostics)}
}

// DecodeSubmitArtifact validates an explicit submit_artifact argument object.
func DecodeSubmitArtifact(raw []byte) (Artifact, error) {
	artifact, diagnostics, err := decodeAndValidateSubmission(raw)
	if err != nil {
		if len(diagnostics) > 0 {
			return Artifact{}, &ValidationError{Diagnostics: StableDiagnostics(diagnostics)}
		}
		return Artifact{}, err
	}
	return artifact, nil
}

func decodeAndValidate(raw []byte) (Artifact, []Diagnostic, error) {
	artifact, err := decodeArtifactPayload(raw)
	if err != nil {
		return Artifact{}, []Diagnostic{invalid("json", "artifact response is not a valid JSON object")}, err
	}
	if err := artifact.ValidateStrict(); err != nil {
		return Artifact{}, validationDiagnostics(err), err
	}
	return artifact, nil, nil
}

func decodeAndValidateSubmission(raw []byte) (Artifact, []Diagnostic, error) {
	var envelope struct {
		Artifact json.RawMessage `json:"artifact"`
	}
	if err := decodeStrict(raw, &envelope); err != nil {
		return Artifact{}, []Diagnostic{invalid("submit_artifact", "submit_artifact arguments are not a valid JSON object")}, err
	}
	if len(envelope.Artifact) == 0 {
		return Artifact{}, []Diagnostic{invalid("submit_artifact.artifact", "submit_artifact requires artifact")}, fmt.Errorf("submit_artifact artifact is required")
	}
	artifact, err := decodeStrictArtifact(envelope.Artifact)
	if err != nil {
		return Artifact{}, []Diagnostic{invalid("submit_artifact.artifact", "submitted artifact is not a valid JSON object")}, err
	}
	if err := artifact.ValidateStrict(); err != nil {
		return Artifact{}, validationDiagnostics(err), err
	}
	return artifact, nil, nil
}

func decodeArtifactPayload(raw []byte) (Artifact, error) {
	artifact, err := decodeStrictArtifact(raw)
	if err == nil {
		return artifact, nil
	}
	submitted, _, submitErr := decodeAndValidateSubmission(raw)
	if submitErr == nil {
		return submitted, nil
	}
	return Artifact{}, err
}

func decodeStrictArtifact(raw []byte) (Artifact, error) {
	var artifact Artifact
	if err := decodeStrict(raw, &artifact); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func repairLocally(raw []byte) (Artifact, []string, []Diagnostic, error) {
	candidate, notes := extractJSONObject(raw)
	artifact, err := decodeArtifactPayloadForRepair(candidate)
	if err != nil {
		return Artifact{}, notes, []Diagnostic{invalid("json", "artifact response is not repairable JSON")}, err
	}
	normalized := artifact.Normalized()
	if err := normalized.ValidateStrict(); err != nil {
		return Artifact{}, notes, validationDiagnostics(err), err
	}
	if normalized.ArtifactID != artifact.ArtifactID {
		notes = append(notes, "derived missing artifact_id")
	}
	if normalized.SchemaVersion != artifact.SchemaVersion {
		notes = append(notes, "filled missing schema_version")
	}
	if normalized.Kind != artifact.Kind {
		notes = append(notes, "filled missing kind")
	}
	if normalized.Status != artifact.Status {
		notes = append(notes, "filled missing status")
	}
	if normalized.Title != artifact.Title {
		notes = append(notes, "filled missing title")
	}
	if normalized.Summary != artifact.Summary {
		notes = append(notes, "filled missing summary")
	}
	if len(notes) == 0 {
		notes = append(notes, "normalized artifact presentation")
	}
	return normalized, notes, nil, nil
}

func decodeArtifactPayloadForRepair(raw []byte) (Artifact, error) {
	artifact, err := decodeStrictArtifact(raw)
	if err == nil {
		return artifact, nil
	}
	var envelope struct {
		Artifact json.RawMessage `json:"artifact"`
	}
	if envelopeErr := decodeStrict(raw, &envelope); envelopeErr != nil || len(envelope.Artifact) == 0 {
		return Artifact{}, err
	}
	return decodeStrictArtifact(envelope.Artifact)
}

func extractJSONObject(raw []byte) ([]byte, []string) {
	trimmed := bytes.TrimSpace(raw)
	notes := make([]string, 0, 1)
	if bytes.HasPrefix(trimmed, []byte("```")) {
		if newline := bytes.IndexByte(trimmed, '\n'); newline >= 0 {
			trimmed = trimmed[newline+1:]
			if end := bytes.LastIndex(trimmed, []byte("```")); end >= 0 {
				trimmed = trimmed[:end]
			}
			notes = append(notes, "removed Markdown code fence")
		}
	}
	start := bytes.IndexByte(trimmed, '{')
	end := bytes.LastIndexByte(trimmed, '}')
	if start > 0 || end >= 0 && end < len(trimmed)-1 {
		notes = append(notes, "extracted JSON object from surrounding text")
	}
	if start >= 0 && end >= start {
		return bytes.TrimSpace(trimmed[start : end+1]), notes
	}
	return trimmed, notes
}

func validationDiagnostics(err error) []Diagnostic {
	validation, ok := err.(*ValidationError)
	if !ok || validation == nil {
		return nil
	}
	return append([]Diagnostic(nil), validation.Diagnostics...)
}

// ArtifactPrompt appends the negotiated contract to an existing model prompt
// without making output requirements invisible in adapter-specific code.
func ArtifactPrompt(base string, contract OutputContract) string {
	base = strings.TrimSpace(base)
	prompt := strings.TrimSpace(contract.Prompt)
	if contract.Mode == OutputSubmitArtifact {
		prompt = "When the task is complete, call submit_artifact exactly once with a complete buckley.artifact/v1 object. Do not replace that submission with prose or a Markdown fence."
	}
	if prompt == "" {
		prompt = "Produce a complete buckley.artifact/v1 result."
	}
	if base == "" {
		return prompt
	}
	return base + "\n\n" + prompt
}
