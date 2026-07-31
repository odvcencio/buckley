package widgets

import (
	"fmt"
	"strconv"
	"strings"

	"m31labs.dev/mdpp"
)

// markdownPPForTerminal parses chat content with Markdown++ and lowers its
// richer document nodes to portable markdown understood by FluffyUI.
func markdownPPForTerminal(content string) string {
	doc, err := mdpp.Parse([]byte(content))
	if err != nil || doc == nil || doc.AST() == nil {
		return content
	}
	for _, diagnostic := range doc.Diagnostics() {
		if diagnostic.Severity == mdpp.SeverityError {
			return content
		}
	}
	return strings.TrimRight(renderMDPPBlocks(doc.AST().Children), "\n")
}

func renderMDPPBlocks(nodes []*mdpp.Node) string {
	parts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if rendered := strings.TrimRight(renderMDPPBlock(node), "\n"); rendered != "" {
			parts = append(parts, rendered)
		}
	}
	return strings.Join(parts, "\n\n")
}

func renderMDPPBlock(node *mdpp.Node) string {
	if node == nil {
		return ""
	}
	switch node.Type {
	case mdpp.NodeDocument:
		return renderMDPPBlocks(node.Children)
	case mdpp.NodeFrontmatter:
		return ""
	case mdpp.NodeHeading:
		level := node.Level()
		if level < 1 || level > 6 {
			level = 1
		}
		return strings.Repeat("#", level) + " " + renderMDPPInlineChildren(node)
	case mdpp.NodeParagraph:
		return renderMDPPInlineChildren(node)
	case mdpp.NodeCodeBlock, mdpp.NodeDiagram:
		language := node.Attr("language")
		if language == "" {
			language = node.Attr("syntax")
		}
		fence := "```"
		if strings.Contains(node.Literal, fence) {
			fence = "````"
		}
		return fence + language + "\n" + strings.TrimRight(node.Literal, "\n") + "\n" + fence
	case mdpp.NodeBlockquote:
		return prefixMDPPLines(renderMDPPBlocks(node.Children), "> ")
	case mdpp.NodeAdmonition:
		title := strings.ToUpper(node.Attr("type"))
		if title == "" {
			title = "NOTE"
		}
		if custom := node.Attr("title"); custom != "" {
			title += " — " + custom
		}
		body := renderMDPPBlocks(node.Children)
		if body == "" {
			return "> **" + title + "**"
		}
		return "> **" + title + "**\n>\n" + prefixMDPPLines(body, "> ")
	case mdpp.NodeContainerDirective:
		title := node.Attr("title")
		if title == "" {
			title = node.Attr("type")
		}
		if title == "" {
			title = "DETAILS"
		}
		body := renderMDPPBlocks(node.Children)
		return "> **" + title + "**\n>\n" + prefixMDPPLines(body, "> ")
	case mdpp.NodeList:
		return renderMDPPList(node, 0)
	case mdpp.NodeTable:
		return renderMDPPTable(node)
	case mdpp.NodeThematicBreak:
		return "---"
	case mdpp.NodeMathBlock:
		return "$$\n" + node.Literal + "\n$$"
	case mdpp.NodeDefinitionList:
		return renderMDPPDefinitions(node)
	case mdpp.NodeTableOfContents:
		return renderMDPPBlocks(node.Children)
	case mdpp.NodeAutoEmbed:
		src := node.Attr("src")
		return "[" + src + "](" + src + ")"
	default:
		if len(node.Children) > 0 {
			return renderMDPPBlocks(node.Children)
		}
		return node.Literal
	}
}

func renderMDPPInlineChildren(node *mdpp.Node) string {
	var out strings.Builder
	for _, child := range node.Children {
		out.WriteString(renderMDPPInline(child))
	}
	return out.String()
}

func renderMDPPInline(node *mdpp.Node) string {
	if node == nil {
		return ""
	}
	children := renderMDPPInlineChildren(node)
	switch node.Type {
	case mdpp.NodeText:
		return node.Literal
	case mdpp.NodeSoftBreak:
		return "\n"
	case mdpp.NodeHardBreak:
		return "  \n"
	case mdpp.NodeEmphasis:
		return "*" + children + "*"
	case mdpp.NodeStrong:
		return "**" + children + "**"
	case mdpp.NodeStrikethrough:
		return "~~" + children + "~~"
	case mdpp.NodeCodeSpan:
		marker := "`"
		if strings.Contains(node.Literal, marker) {
			marker = "``"
		}
		return marker + node.Literal + marker
	case mdpp.NodeLink:
		if href := node.Attr("href"); href != "" {
			return "[" + children + "](" + href + ")"
		}
		if raw := node.Attr("raw"); raw != "" {
			return raw
		}
		return children
	case mdpp.NodeImage:
		return "![" + node.Attr("alt") + "](" + node.Attr("src") + ")"
	case mdpp.NodeFootnoteRef:
		return "[^" + node.Attr("id") + "]"
	case mdpp.NodeMathInline:
		return "$" + node.Literal + "$"
	case mdpp.NodeSuperscript:
		return "^" + node.Literal + "^"
	case mdpp.NodeSubscript:
		return "~" + node.Literal + "~"
	case mdpp.NodeEmoji:
		return node.Literal
	case mdpp.NodeHTMLInline, mdpp.NodeComponent, mdpp.NodeExpression:
		if node.Literal != "" {
			return node.Literal
		}
		return children
	default:
		if children != "" {
			return children
		}
		return node.Literal
	}
}

func renderMDPPList(list *mdpp.Node, depth int) string {
	ordered := list.Attr("ordered") == "true"
	start, _ := strconv.Atoi(list.Attr("start"))
	if start < 1 {
		start = 1
	}
	var lines []string
	index := start
	for _, item := range list.Children {
		if item == nil || (item.Type != mdpp.NodeListItem && item.Type != mdpp.NodeTaskListItem) {
			continue
		}
		marker := "-"
		if ordered {
			marker = fmt.Sprintf("%d.", index)
			index++
		}
		if item.Type == mdpp.NodeTaskListItem {
			check := " "
			if item.Attr("checked") == "true" {
				check = "x"
			}
			marker += " [" + check + "]"
		}
		var bodyParts []string
		for _, child := range item.Children {
			if child.Type == mdpp.NodeList {
				bodyParts = append(bodyParts, renderMDPPList(child, depth+1))
			} else {
				bodyParts = append(bodyParts, renderMDPPBlock(child))
			}
		}
		body := strings.TrimSpace(strings.Join(bodyParts, "\n"))
		bodyLines := strings.Split(body, "\n")
		indent := strings.Repeat("  ", depth)
		lines = append(lines, indent+marker+" "+bodyLines[0])
		for _, line := range bodyLines[1:] {
			lines = append(lines, indent+"  "+line)
		}
	}
	return strings.Join(lines, "\n")
}

func renderMDPPTable(table *mdpp.Node) string {
	if table == nil || len(table.Children) == 0 {
		return ""
	}
	rows := make([][]string, 0, len(table.Children))
	for _, row := range table.Children {
		if row == nil || row.Type != mdpp.NodeTableRow {
			continue
		}
		cells := make([]string, 0, len(row.Children))
		for _, cell := range row.Children {
			cells = append(cells, strings.ReplaceAll(strings.TrimSpace(renderMDPPInlineChildren(cell)), "|", "\\|"))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return ""
	}
	columns := len(rows[0])
	alignments := strings.Split(table.Attr("align"), ",")
	separator := make([]string, columns)
	for column := range separator {
		separator[column] = "---"
		if column < len(alignments) {
			switch alignments[column] {
			case "left":
				separator[column] = ":---"
			case "right":
				separator[column] = "---:"
			case "center":
				separator[column] = ":---:"
			}
		}
	}
	lines := []string{mdppTableRow(rows[0]), mdppTableRow(separator)}
	for _, row := range rows[1:] {
		lines = append(lines, mdppTableRow(row))
	}
	return strings.Join(lines, "\n")
}

func mdppTableRow(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
}

func renderMDPPDefinitions(list *mdpp.Node) string {
	var lines []string
	for _, child := range list.Children {
		switch child.Type {
		case mdpp.NodeDefinitionTerm:
			lines = append(lines, "**"+renderMDPPInlineChildren(child)+"**")
		case mdpp.NodeDefinitionDesc:
			lines = append(lines, ": "+renderMDPPInlineChildren(child))
		}
	}
	return strings.Join(lines, "\n")
}

func prefixMDPPLines(content, prefix string) string {
	if content == "" {
		return strings.TrimSpace(prefix)
	}
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
