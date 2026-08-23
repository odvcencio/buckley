package commands

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"m31labs.dev/buckley/pkg/reviewpolicy"
)

type prCIAdmissionCapture struct {
	Receipt  reviewpolicy.CIAdmissionReceipt
	FetchErr error
}

func capturePRCIAdmission(run prCommandRunner, target prReference, pr *PRInfo, files []string) (prCIAdmissionCapture, error) {
	expectation := prCIAdmissionExpectation(pr, files)
	contexts, fetchErr := getPRRequiredContexts(run, target)
	receipt, err := reviewpolicy.NewCIAdmissionReceipt(reviewpolicy.CIAdmissionInput{
		Expectation:               expectation,
		RequiredContextsAvailable: fetchErr == nil,
		RequiredContexts:          contexts,
	})
	if err != nil {
		return prCIAdmissionCapture{}, fmt.Errorf("create CI admission receipt: %w", err)
	}
	return prCIAdmissionCapture{Receipt: receipt, FetchErr: fetchErr}, nil
}

func getPRRequiredContexts(run prCommandRunner, target prReference) ([]reviewpolicy.CIRequiredContext, error) {
	args := withPRTarget([]string{
		"pr", "checks", strconv.Itoa(target.Number),
		"--json", "name,state", "--required",
	}, target)
	output, err := run("gh", args...)
	if err != nil {
		return nil, err
	}
	var data []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, err
	}
	contexts := make([]reviewpolicy.CIRequiredContext, 0, len(data))
	for _, item := range data {
		contexts = append(contexts, reviewpolicy.CIRequiredContext{Name: item.Name, State: item.State})
	}
	return contexts, nil
}

func prCIAdmissionExpectation(pr *PRInfo, files []string) reviewpolicy.CIAdmissionExpectation {
	identity := reviewpolicy.CIAdmissionIdentity{}
	if pr != nil {
		identity = reviewpolicy.CIAdmissionIdentity{
			Host:       pr.Host,
			Repository: pr.Repository,
			PRNumber:   pr.Number,
			BaseBranch: pr.BaseBranch,
			BaseSHA:    pr.BaseSHA,
			HeadBranch: pr.HeadBranch,
			HeadSHA:    pr.HeadSHA,
		}
	}
	testFiles := recognizedChangedTestFiles(files)
	return reviewpolicy.CIAdmissionExpectation{
		Identity: identity,
		TestReachability: reviewpolicy.CIReachabilityRequest{
			Requested:                  len(testFiles) > 0,
			RecognizedChangedTestFiles: testFiles,
		},
	}
}

func prCIAdmissionExpectationForMetadata(metadata prMetadataSnapshot, request reviewpolicy.CIReachabilityRequest) reviewpolicy.CIAdmissionExpectation {
	return reviewpolicy.CIAdmissionExpectation{
		Identity: reviewpolicy.CIAdmissionIdentity{
			Host:       metadata.Host,
			Repository: metadata.Repository,
			PRNumber:   metadata.Number,
			BaseBranch: metadata.BaseBranch,
			BaseSHA:    metadata.BaseSHA,
			HeadBranch: metadata.HeadBranch,
			HeadSHA:    metadata.HeadSHA,
		},
		TestReachability: request,
	}
}

// CIAdmissionExpectation recomputes the identity and reachability request that
// every authority boundary must compare with the captured receipt.
func (ctx *PRContext) CIAdmissionExpectation() reviewpolicy.CIAdmissionExpectation {
	if ctx == nil {
		return reviewpolicy.CIAdmissionExpectation{}
	}
	return prCIAdmissionExpectation(ctx.PR, ctx.Files)
}

func ciAdmissionExpectationForChangedFiles(expectation reviewpolicy.CIAdmissionExpectation, files []string) reviewpolicy.CIAdmissionExpectation {
	recognized := recognizedChangedTestFiles(files)
	if len(recognized) == 0 {
		return expectation
	}
	expectation.TestReachability.Requested = true
	expectation.TestReachability.RecognizedChangedTestFiles = append(
		expectation.TestReachability.RecognizedChangedTestFiles,
		recognized...,
	)
	return expectation
}

func recognizedChangedTestFiles(files []string) []string {
	seen := make(map[string]struct{})
	for _, raw := range files {
		file := normalizeReviewEvidencePath(raw)
		if file == "" || !recognizedChangedTestFile(file) {
			continue
		}
		seen[file] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for file := range seen {
		result = append(result, file)
	}
	sort.Strings(result)
	return result
}

func recognizedChangedTestFile(file string) bool {
	lower := strings.ToLower(filepath.ToSlash(file))
	base := strings.ToLower(filepath.Base(lower))
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".go":
		return strings.HasSuffix(base, "_test.go")
	case ".py":
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || pathHasTestDirectory(lower)
	case ".rs":
		return strings.HasSuffix(base, "_test.rs") || pathHasTestDirectory(lower)
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		stem := strings.TrimSuffix(base, ext)
		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") || strings.Contains(lower, "/__tests__/")
	default:
		return false
	}
}

func pathHasTestDirectory(path string) bool {
	return strings.HasPrefix(path, "tests/") || strings.Contains(path, "/tests/") ||
		strings.HasPrefix(path, "test/") || strings.Contains(path, "/test/")
}

func addPRCIAdmissionStatus(ctx *PRContext, capture prCIAdmissionCapture) {
	if ctx == nil {
		return
	}
	receipt := capture.Receipt
	detail := fmt.Sprintf("%s (%d required contexts; receipt %s)", receipt.Reason, len(receipt.RequiredContexts), displayPRAdmissionDigest(receipt.Digest))
	if capture.FetchErr != nil {
		detail += "; " + compactPRContextErrorText(capture.FetchErr.Error())
	}
	switch receipt.Decision {
	case reviewpolicy.CIAdmissionAllow:
		ctx.addStatus("CI admission", "complete", detail, false)
	case reviewpolicy.CIAdmissionDeny:
		ctx.addStatus("CI admission", "blocked", detail, false)
	default:
		ctx.addStatus("CI admission", "unavailable", detail, false)
	}
}

func displayPRAdmissionDigest(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
