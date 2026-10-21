package tool

import (
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"m31labs.dev/buckley/internal/commandoutput"
)

const (
	mcpOutputBudgetBytes   = 8 << 10
	mcpBlockSummaryLimit   = 16
	mcpMetadataBudgetBytes = 200
)

type mcpContentSummary struct {
	text            string
	blocks          []string
	outputBytes     int64
	outputTruncated bool
	blocksTruncated bool
}

// flattenMCPContent retains text boundaries and a fixed number of bounded
// block descriptions. Binary/resource bodies stay out of model results.
func flattenMCPContent(content []sdkmcp.Content) mcpContentSummary {
	var summary mcpContentSummary
	capture := commandoutput.NewCapture(mcpOutputBudgetBytes)
	hasText := false
	boundDetail := func(value string) string {
		if len(value) > mcpMetadataBudgetBytes {
			summary.blocksTruncated = true
		}
		return truncateBytes(value, mcpMetadataBudgetBytes)
	}
	for i, block := range content {
		if text, ok := block.(*sdkmcp.TextContent); ok {
			if hasText {
				_, _ = capture.Write([]byte("\n"))
			}
			_, _ = capture.Write([]byte(text.Text))
			hasText = true
		}
		if i >= mcpBlockSummaryLimit {
			summary.blocksTruncated = true
			continue
		}
		var label string
		switch b := block.(type) {
		case *sdkmcp.TextContent:
			label = fmt.Sprintf("[text: %d bytes]", len(b.Text))
		case *sdkmcp.ImageContent:
			label = fmt.Sprintf("[image: %s, %d bytes]", boundDetail(orUnknownMIME(b.MIMEType)), len(b.Data))
		case *sdkmcp.AudioContent:
			label = fmt.Sprintf("[audio: %s, %d bytes]", boundDetail(orUnknownMIME(b.MIMEType)), len(b.Data))
		case *sdkmcp.ResourceLink:
			label = fmt.Sprintf("[resource link: %s]", boundDetail(b.URI))
		case *sdkmcp.EmbeddedResource:
			uri := ""
			if b.Resource != nil {
				uri = b.Resource.URI
			}
			label = fmt.Sprintf("[embedded resource: %s]", boundDetail(uri))
		default:
			label = "[unrecognized content block]"
		}
		summary.blocks = append(summary.blocks, label)
	}
	summary.text, summary.outputTruncated = capture.Finish()
	summary.outputBytes = capture.TotalBytes()
	return summary
}

func orUnknownMIME(mimeType string) string {
	if strings.TrimSpace(mimeType) == "" {
		return "unknown mime type"
	}
	return mimeType
}
