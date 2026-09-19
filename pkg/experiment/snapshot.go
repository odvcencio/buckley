package experiment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	ExperimentSnapshotVersion   = "buckley-experiment-snapshot-v1"
	ExperimentSnapshotKind      = "buckley.experiment.snapshot"
	ExperimentSnapshotMaxBytes  = 16 * 1024 * 1024
	experimentSnapshotMaxDepth  = 128
	experimentSnapshotExporter  = "buckley experiment snapshot exporter v1"
	experimentSnapshotDigestAlg = "sha256"
)

const ExperimentSnapshotProvenance = "retained supplied evaluation evidence; not re-executed/attested; export is not an atomic database snapshot; checksum covers snapshot JSON self-consistency only and is not execution attestation; exporter identity is not historical harness identity"

// ExperimentSnapshot is a portable retained-result boundary for one
// experiment. It preserves stored public run outputs and evaluation rows for
// later offline comparison; it does not embed worktree contents, transcripts,
// private reasoning, or tool-result history.
type ExperimentSnapshot struct {
	Kind        string                           `json:"kind"`
	Version     string                           `json:"version"`
	Exporter    string                           `json:"exporter"`
	ExportedAt  time.Time                        `json:"exported_at"`
	Provenance  string                           `json:"provenance"`
	Experiment  Experiment                       `json:"experiment"`
	Runs        []Run                            `json:"runs"`
	Evaluations map[string][]CriterionEvaluation `json:"evaluations"`
	Digest      SnapshotDigest                   `json:"digest"`
}

type SnapshotDigest struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

func NewSnapshot(exp *Experiment, runs []Run, evaluations map[string][]CriterionEvaluation) (*ExperimentSnapshot, error) {
	if exp == nil {
		return nil, errors.New("experiment is nil")
	}
	snapshot := &ExperimentSnapshot{
		Kind:        ExperimentSnapshotKind,
		Version:     ExperimentSnapshotVersion,
		Exporter:    experimentSnapshotExporter,
		ExportedAt:  time.Now().UTC(),
		Provenance:  ExperimentSnapshotProvenance,
		Evaluations: map[string][]CriterionEvaluation{},
	}
	if err := cloneJSONUseNumber(exp, &snapshot.Experiment); err != nil {
		return nil, fmt.Errorf("clone experiment: %w", err)
	}
	if err := cloneJSONUseNumber(runs, &snapshot.Runs); err != nil {
		return nil, fmt.Errorf("clone runs: %w", err)
	}
	if evaluations != nil {
		if err := cloneJSONUseNumber(evaluations, &snapshot.Evaluations); err != nil {
			return nil, fmt.Errorf("clone evaluations: %w", err)
		}
	}
	if err := snapshot.validate(); err != nil {
		return nil, err
	}
	if err := snapshot.seal(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func EncodeSnapshot(w io.Writer, snapshot *ExperimentSnapshot) error {
	if w == nil {
		return errors.New("snapshot writer is nil")
	}
	if snapshot == nil {
		return errors.New("snapshot is nil")
	}
	if err := snapshot.validate(); err != nil {
		return err
	}
	if err := snapshot.verifyDigest(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > ExperimentSnapshotMaxBytes {
		return fmt.Errorf("snapshot exceeds %d byte limit", ExperimentSnapshotMaxBytes)
	}
	if err := rejectDuplicateJSONKeysAndTrailingData(data); err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

func DecodeSnapshot(r io.Reader) (*ExperimentSnapshot, error) {
	if r == nil {
		return nil, errors.New("snapshot reader is nil")
	}
	data, err := io.ReadAll(io.LimitReader(r, ExperimentSnapshotMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > ExperimentSnapshotMaxBytes {
		return nil, fmt.Errorf("snapshot exceeds %d byte limit", ExperimentSnapshotMaxBytes)
	}
	if err := rejectDuplicateJSONKeysAndTrailingData(data); err != nil {
		return nil, err
	}
	var snapshot ExperimentSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, err
	}
	if snapshot.Version != ExperimentSnapshotVersion {
		return nil, fmt.Errorf("unsupported experiment snapshot version %q", snapshot.Version)
	}
	if err := snapshot.validate(); err != nil {
		return nil, err
	}
	if err := snapshot.verifyDigest(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *ExperimentSnapshot) Compare() (*ComparisonReport, error) {
	if s == nil {
		return nil, errors.New("snapshot is nil")
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	if err := s.verifyDigest(); err != nil {
		return nil, err
	}
	return CompareRuns(&s.Experiment, s.Runs, s.Evaluations)
}

func (s *ExperimentSnapshot) seal() error {
	sum, err := s.computeDigest()
	if err != nil {
		return err
	}
	s.Digest = SnapshotDigest{Algorithm: experimentSnapshotDigestAlg, Value: sum}
	return nil
}

func (s *ExperimentSnapshot) verifyDigest() error {
	if s.Digest.Algorithm != experimentSnapshotDigestAlg {
		return fmt.Errorf("unsupported experiment snapshot digest algorithm %q", s.Digest.Algorithm)
	}
	if !isHexSHA256(s.Digest.Value) {
		return errors.New("experiment snapshot digest is malformed")
	}
	want, err := s.computeDigest()
	if err != nil {
		return err
	}
	if s.Digest.Value != want {
		return errors.New("experiment snapshot digest mismatch")
	}
	return nil
}

func (s *ExperimentSnapshot) computeDigest() (string, error) {
	copy := *s
	copy.Digest = SnapshotDigest{}
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s *ExperimentSnapshot) validate() error {
	if s == nil {
		return errors.New("snapshot is nil")
	}
	if s.Kind != ExperimentSnapshotKind {
		return fmt.Errorf("unsupported experiment snapshot kind %q", s.Kind)
	}
	if s.Version != ExperimentSnapshotVersion {
		return fmt.Errorf("unsupported experiment snapshot version %q", s.Version)
	}
	if strings.TrimSpace(s.Experiment.ID) == "" {
		return errors.New("snapshot experiment id is required")
	}
	if s.Evaluations == nil {
		return errors.New("snapshot evaluations are required")
	}
	if s.Provenance != ExperimentSnapshotProvenance {
		return errors.New("experiment snapshot provenance statement is unsupported")
	}
	return validateSnapshotReferences(&s.Experiment, s.Runs, s.Evaluations)
}

func validateSnapshotReferences(exp *Experiment, runs []Run, evaluations map[string][]CriterionEvaluation) error {
	if err := validateCompareRunsInputs(exp, runs, evaluations); err != nil {
		return err
	}
	seenRuns := make(map[string]struct{}, len(runs))
	criteriaByRunID := make(map[string]map[int64]struct{}, len(runs))
	for _, run := range runs {
		seenRuns[run.ID] = struct{}{}
		if run.ExperimentID == "" {
			return fmt.Errorf("snapshot run %s is missing experiment id", run.ID)
		}
		criteria := exp.Criteria
		if run.InputManifest != nil {
			criteria = run.InputManifest.criteriaDefinitions()
		}
		if err := validateComparisonCriteria(run.ID, criteria); err != nil {
			return err
		}
		ids := make(map[int64]struct{}, len(criteria))
		for _, criterion := range criteria {
			ids[criterion.ID] = struct{}{}
		}
		criteriaByRunID[run.ID] = ids
	}
	for runID, evals := range evaluations {
		if _, ok := seenRuns[runID]; !ok {
			return fmt.Errorf("snapshot evaluations reference missing run id %s", runID)
		}
		criteria := criteriaByRunID[runID]
		for _, eval := range evals {
			if _, ok := criteria[eval.CriterionID]; !ok {
				return fmt.Errorf("snapshot evaluation for run %s references missing criterion id %d", runID, eval.CriterionID)
			}
		}
	}
	return nil
}

func cloneJSONUseNumber(src any, dst any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(dst)
}

func isHexSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func rejectDuplicateJSONKeysAndTrailingData(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("snapshot JSON has trailing data")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > experimentSnapshotMaxDepth {
		return fmt.Errorf("snapshot JSON exceeds maximum depth %d", experimentSnapshotMaxDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("snapshot JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("snapshot JSON contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("snapshot JSON object is malformed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("snapshot JSON array is malformed")
		}
	default:
		return errors.New("snapshot JSON delimiter is malformed")
	}
	return nil
}
