package context

import (
	"fmt"
	"strings"
)

type agentsParser struct {
	ctx            *ProjectContext
	currentSection string
	currentSub     string
}

const (
	sectionProjectSummary   = "Project Summary"
	sectionDevelopmentRules = "Development Rules"
	sectionAgentGuidelines  = "Agent Guidelines"
	sectionSubAgents        = "Sub-Agents"
)

func newAgentsParser(ctx *ProjectContext) *agentsParser {
	return &agentsParser{
		ctx: ctx,
	}
}

func (p *agentsParser) processLine(line string) {
	if line == "" {
		return
	}

	switch {
	case strings.HasPrefix(line, "## "):
		p.currentSection = strings.TrimSpace(strings.TrimPrefix(line, "## "))
		p.currentSub = ""
	case strings.HasPrefix(line, "### ") && p.currentSection == sectionSubAgents:
		p.currentSub = strings.TrimSpace(strings.TrimPrefix(line, "### "))
		p.ctx.SubAgents[p.currentSub] = &SubAgentSpec{
			Name:  p.currentSub,
			Tools: []string{},
		}
	default:
		p.handleContent(line)
	}
}

func (p *agentsParser) handleContent(line string) {
	switch p.currentSection {
	case sectionProjectSummary:
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "[") {
			p.ctx.Summary += line + " "
		}
	case sectionDevelopmentRules:
		if strings.HasPrefix(line, "- ") {
			p.ctx.Rules = append(p.ctx.Rules, strings.TrimPrefix(line, "- "))
		}
	case sectionAgentGuidelines:
		if strings.HasPrefix(line, "- ") {
			p.ctx.Guidelines = append(p.ctx.Guidelines, strings.TrimPrefix(line, "- "))
		}
	case sectionSubAgents:
		if p.currentSub != "" && strings.HasPrefix(line, "- **") {
			p.parseSubAgentField(line)
		}
	}
}

func (p *agentsParser) parseSubAgentField(line string) {
	spec := p.ctx.SubAgents[p.currentSub]
	if spec == nil {
		return
	}

	switch {
	case strings.Contains(line, "**Description:**"):
		spec.Description = extractValue(line, "**Description:**")
	case strings.Contains(line, "**Model:**"):
		spec.Model = extractValue(line, "**Model:**")
	case strings.Contains(line, "**Tools:**"):
		toolsStr := extractValue(line, "**Tools:**")
		toolsStr = strings.Trim(toolsStr, "[]")
		if toolsStr != "" {
			for _, tool := range strings.Split(toolsStr, ",") {
				spec.Tools = append(spec.Tools, strings.TrimSpace(tool))
			}
		}
	case strings.Contains(line, "**Max Cost:**"):
		costStr := strings.TrimPrefix(extractValue(line, "**Max Cost:**"), "$")
		fmt.Sscanf(costStr, "%f", &spec.MaxCost)
	case strings.Contains(line, "**Instructions:**"):
		spec.Instructions = extractValue(line, "**Instructions:**")
	}
}
