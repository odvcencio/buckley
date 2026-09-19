package prompts

// ste100ProseBlock is the ASD-STE100 (Simplified Technical English) style
// rule set injected into prompts that generate commit and PR prose. It
// governs the prose inside the existing structural contracts (header
// format, character limits, bullet rules); it does not replace them. The
// full rules profile lives in the org decision record:
// hypha://m31labs/hyphae decisions/0011-ste100-prose-standard.md
const ste100ProseBlock = `ASD-STE100 profile:
- Use active voice. Use the imperative mood for instructions.
- Write one topic per sentence. Keep procedural sentences at or below 20
  words. Keep descriptive sentences at or below 25 words.
- Give each word one meaning; use it the same way throughout.
- Do not write noun clusters of more than three nouns. Keep articles.
- Do not use idioms, slang, or Latin abbreviations. Write "for example",
  "that is", "and so on".
- Define an abbreviation at first use unless it is standard in this repo.
- Use concrete verbs. Avoid vague verbs such as "handle", "leverage",
  "deal with".
- This rule governs prose only; the structural rules above still apply.`

// STE100ProseBlock returns the ASD-STE100 prose rule set for injection
// into prompts outside this package (for example, orchestrator system
// prompts that generate commit/PR prose but do not use the pkg/prompts
// templates directly).
func STE100ProseBlock() string {
	return ste100ProseBlock
}
