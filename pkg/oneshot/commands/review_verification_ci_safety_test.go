package commands

import "testing"

func TestEvaluateVerification_RejectsUnmappedCI(t *testing.T) {
	for _, checks := range [][]CICheck{{{"Pages", CICheckPass}}, {{"Build", CICheckPass}, {"Test", CICheckPass}}} {
		rep := EvaluateVerification(VerificationInput{Mode: VerifyReceiptOrCI, Files: []string{"pkg/engine/loop.go"}, ChecksOnHead: true, Checks: checks})
		code, _ := rep.Gate()
		if code != VerificationGateMissing || rep.Qualifier() != "unverified" || len(rep.Covered) != 0 {
			t.Fatalf("unmapped checks authorize source coverage: %+v gate=%d", rep, code)
		}
	}
}

func TestEvaluateVerification_DuplicateNamedChecksFailClosed(t *testing.T) {
	for _, state := range []CICheckState{CICheckPending, CICheckSkipped, CICheckFail} {
		for _, checks := range [][]CICheck{{{"tests", CICheckPass}, {"tests", state}}, {{"tests", state}, {"tests", CICheckPass}}} {
			rep := EvaluateVerification(VerificationInput{Mode: VerifyReceiptOrCI, Files: []string{"pkg/engine/loop.go"}, ChecksOnHead: true, Checks: checks, Declared: []DeclaredVerification{{Name: "engine", Paths: []string{"pkg/engine/**"}, CIChecks: []string{"tests"}}}})
			code, _ := rep.Gate()
			if code == VerificationGateOK || len(rep.Covered) != 0 {
				t.Fatalf("duplicate named checks accepted: %+v report=%+v", checks, rep)
			}
		}
	}
}
