package reviewpolicy

import (
	"encoding/json"
	"strings"
	"testing"
)

func matrixAdmissionInput() CIAdmissionInput {
	expectation := testCIAdmissionExpectation()
	expectation.TestReachability.RecognizedChangedTestFiles = []string{"root_test.go", "examples/deck/deck_test.go"}
	return CIAdmissionInput{
		Expectation: expectation, RequiredContextsAvailable: true,
		RequiredContexts: []CIRequiredContext{{Name: "Go (.)", State: "SUCCESS"}, {Name: "Go (examples/deck)", State: "SUCCESS"}},
		TestReachabilityEvidence: &CIReachabilityEvidence{Source: "github_ci_go_matrix_v1", HeadSHA: expectation.Identity.HeadSHA, MergeSHA: "merge", RunID: 123,
			Modules: []CIReachabilityModule{
				{Directory: ".", Module: "example.dev/root", JobID: 100, Check: "Go (.)", TestFiles: []string{"root_test.go"}, Packages: []string{"example.dev/root"}},
				{Directory: "examples/deck", Module: "deck", JobID: 101, Check: "Go (examples/deck)", TestFiles: []string{"examples/deck/deck_test.go"}, Packages: []string{"deck"}},
			},
		},
	}
}

func TestNewCIAdmissionReceipt_MatrixCoversAllModules(t *testing.T) {
	input := matrixAdmissionInput()
	receipt, err := NewCIAdmissionReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.Authorize(input.Expectation); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip CIAdmissionReceipt
	if err := json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if err := roundtrip.Authorize(input.Expectation); err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"job_id":101`, `"job_id":102`, 1)
	if err := json.Unmarshal([]byte(tampered), &roundtrip); err != nil {
		t.Fatal(err)
	}
	if err := roundtrip.Authorize(input.Expectation); err == nil {
		t.Fatal("tampered module job authorized")
	}
	input.TestReachabilityEvidence.Modules[0].TestFiles[0] = "changed_test.go"
	if err := receipt.Authorize(receiptExpectationForTest(receipt)); err != nil {
		t.Fatalf("caller mutation changed canonical evidence: %v", err)
	}
}

func receiptExpectationForTest(receipt CIAdmissionReceipt) CIAdmissionExpectation {
	return CIAdmissionExpectation{Identity: receipt.Identity, TestReachability: receipt.TestReachability}
}

func TestNewCIAdmissionReceipt_MatrixRejectsIncompleteAndMisbinding(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*CIAdmissionInput)
	}{
		{"omitted module", func(i *CIAdmissionInput) { i.TestReachabilityEvidence.Modules = i.TestReachabilityEvidence.Modules[:1] }},
		{"unrequired module", func(i *CIAdmissionInput) { i.RequiredContexts = i.RequiredContexts[:1] }},
		{"duplicate job", func(i *CIAdmissionInput) { i.TestReachabilityEvidence.Modules[1].JobID = 100 }},
		{"wrong check", func(i *CIAdmissionInput) { i.TestReachabilityEvidence.Modules[1].Check = "Go (.)" }},
		{"file outside module", func(i *CIAdmissionInput) {
			i.TestReachabilityEvidence.Modules[1].Directory = "other"
			i.TestReachabilityEvidence.Modules[1].Check = "Go (other)"
			i.RequiredContexts[1].Name = "Go (other)"
		}},
		{"wrong package", func(i *CIAdmissionInput) { i.TestReachabilityEvidence.Modules[1].Packages = []string{"other"} }},
		{"missing package", func(i *CIAdmissionInput) { i.TestReachabilityEvidence.Modules[1].Packages = nil }},
		{"stale head", func(i *CIAdmissionInput) { i.TestReachabilityEvidence.HeadSHA = "old" }},
		{"duplicate file", func(i *CIAdmissionInput) {
			i.TestReachabilityEvidence.Modules[0].TestFiles = append(i.TestReachabilityEvidence.Modules[0].TestFiles, "root_test.go")
		}},
		{"empty required checks", func(i *CIAdmissionInput) { i.RequiredContexts = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := matrixAdmissionInput()
			test.edit(&input)
			if _, err := NewCIAdmissionReceipt(input); err == nil {
				t.Fatal("misbound matrix evidence accepted")
			}
		})
	}
}
