package prompts

// plainLanguageCore condenses decision 0012 for generated prose. Output schemas,
// header repair, character limits, and body bullet rules still apply.
const plainLanguageCore = `Plain, specific, and sourced (decision 0012; writing-plainly):
- Lead with the point. Put the answer, result, or request first.
- Say it plainly. Use common words, active voice, and concrete verbs. Keep reasoning; cut filler.
- Keep terms stable. Use one name per thing; define unfamiliar abbreviations.
- Show receipts. Give checkable evidence; say what you did not verify. No superlatives or hype.
- Shape it for scanning. Use short paragraphs, prose for reasoning, and lists or tables where useful. Keep the required output format.`

// CommitProseBlock guides commit prose inside the existing message contract.
func CommitProseBlock() string {
	return plainLanguageCore + `

Commit register: A conventional header of 72 characters or fewer. A body that says why and names any risk. No file lists.`
}

// PRProseBlock guides pull request titles and bodies.
func PRProseBlock() string {
	return plainLanguageCore + `

PR register: What and why, then evidence (tests, numbers, screenshots), then risk and rollout, then where to look first.`
}

// ReviewProseBlock guides findings and limits prose flags to reader harm.
func ReviewProseBlock() string {
	return plainLanguageCore + `

Review register: One finding at a time: what is wrong, why it matters, a suggested fix, and the severity. Direct and kind.
Flag prose only when it hurts the reader: unclear, misleading, unsourced, inconsistent in its terms, or using an undefined abbreviation. No style nitpicks. For each prose finding, use **Category**: prose, cite the text and reader impact, and give a suggested rewrite in **Rewrite**.`
}

// MergeNoteProseBlock supplements the commit register for conflict resolutions.
func MergeNoteProseBlock() string {
	return `Merge note register: State what conflicted, which behavior the resolution keeps, and why. Cite the supplied resolution facts and verification; say what was not verified.`
}

// DeclineCommentTemplate is the fixed decline register: decision, measured
// evidence and reason, then concrete next steps and the maintainer override.
// It is rendered directly, without a model prompt.
const DeclineCommentTemplate = `## Automated review declined

The reviewable diff is %d bytes across %d files, above the %d-byte limit for automated posting. This limit bounds the automated review budget for one pull request.

Please split the change by directory or package so each pull request fits the limit. Keep generated or bundled artifacts separate so reviewers can focus on the source changes.

A core maintainer can run the full review locally with ` + "`buckley review-pr`" + `, with no size limit. A core maintainer can also re-trigger this review if the current size is intentional.
`
