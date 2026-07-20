package prompts

// ste100ProseBlock is the ASD-STE100 (Simplified Technical English) style
// rule set injected into prompts that generate commit and PR prose. It
// governs the prose inside the existing structural contracts (header
// format, character limits, bullet rules); it does not replace them. The
// full rules profile lives in the org decision record:
// hypha://m31labs/hyphae decisions/0011-ste100-prose-standard.md
const ste100ProseBlock = `ASD-STE100 profile:
- Use active voice. Use the imperative mood for instructions.
- Write one topic per sentence. Keep each sentence at or below 25 words.
- Give each word one meaning; use it the same way throughout.
- Do not write noun clusters of more than three nouns. Keep articles.
- Do not use idioms, slang, or Latin abbreviations. Write "for example",
  "that is", "and so on".
- Define an abbreviation at first use unless it is standard in this repo.
- Use concrete verbs. Avoid vague verbs such as "handle", "leverage",
  "deal with".
- This rule governs prose only; the structural rules above still apply.`

// ste100ReviewTenet is the ASD-STE100 prose tenet injected into review
// prompts. It directs the reviewer to flag prose that violates the
// profile and to propose a plain-language rewrite for each flag.
const ste100ReviewTenet = `ASD-STE100 profile:
- Flag prose in commit messages, PR titles/descriptions, and added doc or
  comment text that violates ASD-STE100:
  - Passive voice where active voice reads clearly.
  - Sentences over 20 words (procedural) or 25 words (descriptive).
  - Noun clusters of more than three nouns.
  - Inconsistent terminology for the same concept.
  - Abbreviations left undefined at first use.
- For every violation, quote the exact text and give a suggested rewrite
  in active, plain prose.
- Report violations as a MINOR finding unless the violation obscures a
  Critical or Major finding.
- Do not flag code identifiers, quoted tool output, verbatim logs, or
  third-party text.`
