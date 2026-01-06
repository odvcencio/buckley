# Feature Plan: {{.FeatureName}}

**Created:** {{.CreatedAt.Format "2006-01-02 15:04:05"}}
**Project Type:** {{.Context.ProjectType}}
**Branch:** {{.Context.GitBranch}}
**Plan ID:** {{.ID}}

## Description

{{.Description}}

## Architecture Overview

{{if .Context.Architecture}}{{.Context.Architecture}}{{else}}To be determined during implementation.{{end}}

{{if .Context.ResearchSummary}}
## Research Highlights

{{.Context.ResearchSummary}}

{{if .Context.ResearchRisks}}
**Top Risks**
{{range .Context.ResearchRisks}}- {{.}}
{{end}}
{{end}}
{{end}}

## Tasks

{{range $i, $task := .Tasks}}
### Task {{$task.ID}}: {{$task.Title}}

**Status:** {{if eq $task.Status 0}}Pending{{else if eq $task.Status 1}}In Progress{{else if eq $task.Status 2}}Completed{{else if eq $task.Status 3}}Failed{{else}}Skipped{{end}}

**Description:** {{$task.Description}}

**Files to modify:**
{{range $task.Files}}- `{{.}}`
{{end}}

{{if $task.Dependencies}}**Dependencies:**
{{range $task.Dependencies}}- Task {{.}}
{{end}}
{{end}}

**Estimated time:** {{$task.EstimatedTime}}

**Verification:**
{{range $task.Verification}}- [ ] {{.}}
{{end}}

---
{{end}}

## Success Criteria

- All tasks completed
- All tests passing
- Code review approved
- Documentation updated

## Progress

- Total Tasks: {{len .Tasks}}
- Completed: {{.CompletedCount}}
- Remaining: {{.RemainingCount}}
