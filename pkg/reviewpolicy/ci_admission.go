package reviewpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

const (
	CIAdmissionSchema = "buckley.ci-admission.v1"

	CIAdmissionAllow       CIAdmissionDecision = "allow"
	CIAdmissionDeny        CIAdmissionDecision = "deny"
	CIAdmissionUnavailable CIAdmissionDecision = "unavailable"

	CIAdmissionReasonRequiredContextsPassed      CIAdmissionReason = "required_contexts_passed"
	CIAdmissionReasonNoRequiredContexts          CIAdmissionReason = "no_required_contexts"
	CIAdmissionReasonRequiredContextsUnavailable CIAdmissionReason = "required_contexts_unavailable"
	CIAdmissionReasonRequiredContextsNotPassing  CIAdmissionReason = "required_contexts_not_passing"
	CIAdmissionReasonTestReachabilityUnavailable CIAdmissionReason = "test_reachability_unavailable"

	CIReachabilityNotApplicable CIReachabilityStatus = "not_applicable"
	CIReachabilityUnavailable   CIReachabilityStatus = "unavailable"
)

const ciAdmissionPolicyV1 = "required contexts must be available, non-empty, and passing; requested test reachability must be deterministically available"

var (
	ErrCIAdmissionMissing     = errors.New("ci admission receipt is missing")
	ErrCIAdmissionInvalid     = errors.New("ci admission receipt is invalid")
	ErrCIAdmissionStale       = errors.New("ci admission receipt is stale")
	ErrCIAdmissionDenied      = errors.New("ci admission denied")
	ErrCIAdmissionUnavailable = errors.New("ci admission unavailable")
)

// CIAdmissionDecision is the fail-closed outcome of deterministic CI policy.
type CIAdmissionDecision string

// CIAdmissionReason is a stable machine-readable explanation for a decision.
type CIAdmissionReason string

// CIReachabilityStatus records whether a structural test-reachability finding
// was required for this receipt. Slice 1 deliberately has no reachability
// adapter, so any requested finding is unavailable rather than implicitly
// passing.
type CIReachabilityStatus string

// CIAdmissionIdentity binds a receipt to one exact pull-request snapshot.
type CIAdmissionIdentity struct {
	Host       string `json:"host"`
	Repository string `json:"repository"`
	PRNumber   int    `json:"pr_number"`
	BaseBranch string `json:"base_branch"`
	BaseSHA    string `json:"base_sha"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
}

// CIRequiredContext is one GitHub-required check observation.
type CIRequiredContext struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// CIReachabilityRequest is the immutable request against which a receipt is
// validated. Recognized test files require an adapter finding even when a
// caller accidentally leaves Requested false.
type CIReachabilityRequest struct {
	Requested                  bool     `json:"requested"`
	RecognizedChangedTestFiles []string `json:"recognized_changed_test_files,omitempty"`
}

// CIAdmissionExpectation is recomputed by each authority boundary instead of
// being accepted from model output or serialized tool parameters.
type CIAdmissionExpectation struct {
	Identity         CIAdmissionIdentity   `json:"identity"`
	TestReachability CIReachabilityRequest `json:"test_reachability"`
}

// CIAdmissionInput contains deterministic observations used to mint a receipt.
type CIAdmissionInput struct {
	Expectation               CIAdmissionExpectation
	RequiredContextsAvailable bool
	RequiredContexts          []CIRequiredContext
}

// CIAdmissionReceipt is a canonical, tamper-evident admission decision. Digest
// covers every preceding field in declaration order after all slices have been
// normalized and sorted.
type CIAdmissionReceipt struct {
	Schema                      string                `json:"schema"`
	Identity                    CIAdmissionIdentity   `json:"identity"`
	PolicySHA256                string                `json:"policy_sha256"`
	RequiredContextsAvailable   bool                  `json:"required_contexts_available"`
	RequiredContexts            []CIRequiredContext   `json:"required_contexts"`
	RequiredContextsFingerprint string                `json:"required_contexts_fingerprint"`
	TestReachability            CIReachabilityRequest `json:"test_reachability"`
	TestReachabilityStatus      CIReachabilityStatus  `json:"test_reachability_status"`
	Decision                    CIAdmissionDecision   `json:"decision"`
	Reason                      CIAdmissionReason     `json:"reason"`
	Digest                      string                `json:"digest"`
}

// NewCIAdmissionReceipt normalizes observations, derives the policy outcome,
// and seals the result with a canonical SHA-256 digest.
func NewCIAdmissionReceipt(input CIAdmissionInput) (CIAdmissionReceipt, error) {
	expectation, err := normalizeCIAdmissionExpectation(input.Expectation)
	if err != nil {
		return CIAdmissionReceipt{}, err
	}
	contexts, err := normalizeCIRequiredContexts(input.RequiredContexts)
	if err != nil {
		return CIAdmissionReceipt{}, err
	}
	if !input.RequiredContextsAvailable && len(contexts) > 0 {
		return CIAdmissionReceipt{}, fmt.Errorf("%w: unavailable required contexts cannot contain observations", ErrCIAdmissionInvalid)
	}

	receipt := CIAdmissionReceipt{
		Schema:                    CIAdmissionSchema,
		Identity:                  expectation.Identity,
		PolicySHA256:              ciAdmissionPolicyDigest(),
		RequiredContextsAvailable: input.RequiredContextsAvailable,
		RequiredContexts:          contexts,
		TestReachability:          expectation.TestReachability,
		TestReachabilityStatus:    expectedCIReachabilityStatus(expectation.TestReachability),
	}
	receipt.RequiredContextsFingerprint = requiredContextsFingerprint(receipt.RequiredContextsAvailable, receipt.RequiredContexts)
	receipt.Decision, receipt.Reason = deriveCIAdmissionDecision(receipt)
	receipt.Digest, err = receiptDigest(receipt)
	if err != nil {
		return CIAdmissionReceipt{}, err
	}
	return receipt, nil
}

// Validate verifies canonical form, policy identity, derived fields, and the
// receipt digest. It does not establish freshness; Authorize also compares the
// independently recomputed expectation.
func (r CIAdmissionReceipt) Validate() error {
	if r.Schema == "" && r.Digest == "" {
		return ErrCIAdmissionMissing
	}
	if r.Schema != CIAdmissionSchema || r.PolicySHA256 != ciAdmissionPolicyDigest() {
		return fmt.Errorf("%w: schema or policy digest mismatch", ErrCIAdmissionInvalid)
	}
	expectation, err := normalizeCIAdmissionExpectation(CIAdmissionExpectation{
		Identity:         r.Identity,
		TestReachability: r.TestReachability,
	})
	if err != nil || expectation.Identity != r.Identity || !equalCIReachabilityRequests(expectation.TestReachability, r.TestReachability) {
		return fmt.Errorf("%w: non-canonical identity or reachability request", ErrCIAdmissionInvalid)
	}
	contexts, err := normalizeCIRequiredContexts(r.RequiredContexts)
	if err != nil || r.RequiredContexts == nil || !equalCIRequiredContexts(contexts, r.RequiredContexts) {
		return fmt.Errorf("%w: non-canonical required contexts", ErrCIAdmissionInvalid)
	}
	if !r.RequiredContextsAvailable && len(r.RequiredContexts) > 0 {
		return fmt.Errorf("%w: unavailable required contexts contain observations", ErrCIAdmissionInvalid)
	}
	if want := requiredContextsFingerprint(r.RequiredContextsAvailable, r.RequiredContexts); r.RequiredContextsFingerprint != want {
		return fmt.Errorf("%w: required-context fingerprint mismatch", ErrCIAdmissionInvalid)
	}
	if want := expectedCIReachabilityStatus(r.TestReachability); r.TestReachabilityStatus != want {
		return fmt.Errorf("%w: test-reachability status mismatch", ErrCIAdmissionInvalid)
	}
	decision, reason := deriveCIAdmissionDecision(r)
	if r.Decision != decision || r.Reason != reason {
		return fmt.Errorf("%w: decision does not match evidence", ErrCIAdmissionInvalid)
	}
	digest, err := receiptDigest(r)
	if err != nil || digest != r.Digest {
		return fmt.Errorf("%w: receipt digest mismatch", ErrCIAdmissionInvalid)
	}
	return nil
}

// ValidateFor validates a receipt and compares it with an independently
// recomputed PR identity and reachability expectation. It deliberately does
// not require an allow decision: stable deny and unavailable receipts remain
// valid evidence for non-approval reviews and revalidation.
func (r CIAdmissionReceipt) ValidateFor(expected CIAdmissionExpectation) error {
	if err := r.Validate(); err != nil {
		return err
	}
	normalized, err := normalizeCIAdmissionExpectation(expected)
	if err != nil {
		return fmt.Errorf("%w: expected identity is invalid", ErrCIAdmissionStale)
	}
	if normalized.Identity != r.Identity || !equalCIReachabilityRequests(normalized.TestReachability, r.TestReachability) {
		return fmt.Errorf("%w: expectation does not match receipt", ErrCIAdmissionStale)
	}
	return nil
}

// Authorize validates and freshness-binds a receipt, then accepts only an
// allow decision. Approval and authoritative remote-CI shortcuts use this;
// ordinary receipt revalidation uses ValidateFor.
func (r CIAdmissionReceipt) Authorize(expected CIAdmissionExpectation) error {
	if err := r.ValidateFor(expected); err != nil {
		return err
	}
	switch r.Decision {
	case CIAdmissionAllow:
		return nil
	case CIAdmissionUnavailable:
		return fmt.Errorf("%w: %s", ErrCIAdmissionUnavailable, r.Reason)
	default:
		return fmt.Errorf("%w: %s", ErrCIAdmissionDenied, r.Reason)
	}
}

func deriveCIAdmissionDecision(r CIAdmissionReceipt) (CIAdmissionDecision, CIAdmissionReason) {
	switch {
	case !r.RequiredContextsAvailable:
		return CIAdmissionUnavailable, CIAdmissionReasonRequiredContextsUnavailable
	case len(r.RequiredContexts) == 0:
		return CIAdmissionDeny, CIAdmissionReasonNoRequiredContexts
	case !allCIRequiredContextsPass(r.RequiredContexts):
		return CIAdmissionDeny, CIAdmissionReasonRequiredContextsNotPassing
	case r.TestReachabilityStatus != CIReachabilityNotApplicable:
		return CIAdmissionUnavailable, CIAdmissionReasonTestReachabilityUnavailable
	default:
		return CIAdmissionAllow, CIAdmissionReasonRequiredContextsPassed
	}
}

func allCIRequiredContextsPass(contexts []CIRequiredContext) bool {
	for _, context := range contexts {
		switch context.State {
		case "PASS", "SUCCESS":
		default:
			return false
		}
	}
	return len(contexts) > 0
}

func expectedCIReachabilityStatus(request CIReachabilityRequest) CIReachabilityStatus {
	if request.Requested || len(request.RecognizedChangedTestFiles) > 0 {
		return CIReachabilityUnavailable
	}
	return CIReachabilityNotApplicable
}

func normalizeCIAdmissionExpectation(value CIAdmissionExpectation) (CIAdmissionExpectation, error) {
	value.Identity = normalizeCIAdmissionIdentity(value.Identity)
	if value.Identity.Host == "" || value.Identity.Repository == "" || value.Identity.PRNumber <= 0 ||
		value.Identity.BaseBranch == "" || value.Identity.BaseSHA == "" ||
		value.Identity.HeadBranch == "" || value.Identity.HeadSHA == "" {
		return CIAdmissionExpectation{}, fmt.Errorf("%w: exact host, repository, PR number, base branch/SHA, and head branch/SHA are required", ErrCIAdmissionInvalid)
	}
	files, err := normalizeCIAdmissionPaths(value.TestReachability.RecognizedChangedTestFiles)
	if err != nil {
		return CIAdmissionExpectation{}, err
	}
	value.TestReachability.RecognizedChangedTestFiles = files
	if len(files) > 0 {
		value.TestReachability.Requested = true
	}
	return value, nil
}

func normalizeCIAdmissionIdentity(value CIAdmissionIdentity) CIAdmissionIdentity {
	value.Host = strings.ToLower(strings.TrimSpace(value.Host))
	value.Repository = strings.Trim(strings.TrimSpace(value.Repository), "/")
	value.BaseBranch = strings.TrimSpace(value.BaseBranch)
	value.BaseSHA = strings.TrimSpace(value.BaseSHA)
	value.HeadBranch = strings.TrimSpace(value.HeadBranch)
	value.HeadSHA = strings.TrimSpace(value.HeadSHA)
	return value
}

func normalizeCIRequiredContexts(values []CIRequiredContext) ([]CIRequiredContext, error) {
	contexts := make([]CIRequiredContext, 0, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		value.State = strings.ToUpper(strings.TrimSpace(value.State))
		if value.Name == "" || value.State == "" {
			return nil, fmt.Errorf("%w: required context name and state are required", ErrCIAdmissionInvalid)
		}
		contexts = append(contexts, value)
	}
	sort.Slice(contexts, func(i, j int) bool {
		if contexts[i].Name != contexts[j].Name {
			return contexts[i].Name < contexts[j].Name
		}
		return contexts[i].State < contexts[j].State
	})
	result := contexts[:0]
	for _, value := range contexts {
		if len(result) > 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result, nil
}

func normalizeCIAdmissionPaths(values []string) ([]string, error) {
	paths := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
		if value == "" || path.IsAbs(value) {
			return nil, fmt.Errorf("%w: recognized test path must be repository-relative", ErrCIAdmissionInvalid)
		}
		value = path.Clean(value)
		if value == ".." || strings.HasPrefix(value, "../") {
			return nil, fmt.Errorf("%w: recognized test path escapes repository", ErrCIAdmissionInvalid)
		}
		paths = append(paths, value)
	}
	sort.Strings(paths)
	result := paths[:0]
	for _, value := range paths {
		if len(result) > 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result, nil
}

func requiredContextsFingerprint(available bool, contexts []CIRequiredContext) string {
	payload := struct {
		Available bool                `json:"available"`
		Contexts  []CIRequiredContext `json:"contexts"`
	}{Available: available, Contexts: contexts}
	encoded, _ := json.Marshal(payload)
	return sha256Hex(encoded)
}

func receiptDigest(receipt CIAdmissionReceipt) (string, error) {
	receipt.Digest = ""
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return "", fmt.Errorf("%w: canonical receipt encoding: %v", ErrCIAdmissionInvalid, err)
	}
	return sha256Hex(encoded), nil
}

func ciAdmissionPolicyDigest() string {
	return sha256Hex([]byte(CIAdmissionSchema + "\n" + ciAdmissionPolicyV1))
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func equalCIRequiredContexts(left, right []CIRequiredContext) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalCIReachabilityRequests(left, right CIReachabilityRequest) bool {
	if left.Requested != right.Requested || len(left.RecognizedChangedTestFiles) != len(right.RecognizedChangedTestFiles) {
		return false
	}
	for index := range left.RecognizedChangedTestFiles {
		if left.RecognizedChangedTestFiles[index] != right.RecognizedChangedTestFiles[index] {
			return false
		}
	}
	return true
}
