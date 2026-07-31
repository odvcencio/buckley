package prompts

// Shared review-rule bullets, referenced verbatim by more than one review
// prompt or policy assembler (pkg/prompts, cmd/buckley's automated review
// policy plan). Each constant is the single source of truth for its exact
// wording; every other site references it instead of repeating the string.
const (
	// RuleFindingsRequireProvedFalsification limits Findings to a PROVED
	// falsification conclusion.
	RuleFindingsRequireProvedFalsification = "- Write Findings only when Falsification concludes PROVED."

	// RuleDisprovedOrUnresolvedGoesToRemarks moves a DISPROVED or
	// UNRESOLVED falsification conclusion to Remarks instead of Findings.
	RuleDisprovedOrUnresolvedGoesToRemarks = "- If Falsification concludes DISPROVED or UNRESOLVED, move concerns to Remarks or omit them."

	// RuleVerificationInFirstBatch requires required verification calls in
	// the first tool-call batch instead of deferring them to synthesis.
	RuleVerificationInFirstBatch = "- Put required verification in the first tool-call batch. Do not defer it until final synthesis."
)
