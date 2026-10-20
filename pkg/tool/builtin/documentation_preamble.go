package builtin

import (
	"strings"
	"unicode/utf8"

	"github.com/odvcencio/gotreesitter"
)

const largeDocumentationPreambleBytes = 64 << 10
const documentationPreambleBodyBytes = 64 << 10

// A large comment-only preamble need not inflate the syntax tree for a no-op.
// The complete remaining program still goes through the normal strict parser.
func existingPreambleDocumentation(p *gotreesitter.Parser, language *gotreesitter.Language, content, name string) (documentationEdit, bool) {
	if len(content) < largeDocumentationPreambleBytes || strings.Contains(name, ".") {
		return documentationEdit{}, false
	}
	end, docStart := leadingDocumentationPreamble(content)
	if end < largeDocumentationPreambleBytes || docStart < 0 || len(content)-end > documentationPreambleBodyBytes {
		return documentationEdit{}, false
	}
	prefix := content[:end]
	if !utf8.ValidString(prefix) || strings.ContainsAny(prefix, "\x00\u2028\u2029") {
		return documentationEdit{}, false
	}
	code := content[end:]
	declaration := strings.TrimLeft(code, " \t\r\n")
	// This is an admission filter; syntax and the unique target are checked below.
	admitted := false
	for _, start := range []string{"function ", "async function ", "export function ", "export async function ", "export default function ", "export default async function "} {
		after, ok := strings.CutPrefix(declaration, start+name)
		if ok && len(after) > 0 && strings.ContainsRune("( <\t\r\n", rune(after[0])) {
			admitted = true
			break
		}
	}
	if !admitted {
		return documentationEdit{}, false
	}
	tree, err := parseFunctionTree(p, code)
	if err != nil {
		return documentationEdit{}, false
	}
	defer tree.Release()
	fn, err := findNamedTreeFunction(tree.RootNode(), language, code, name)
	if err != nil {
		return documentationEdit{}, false
	}
	anchor, _, err := jsDocumentationAnchor(fn, language, code)
	if err != nil || strings.TrimSpace(code[:anchor.StartByte()]) != "" {
		return documentationEdit{}, false
	}
	return documentationEdit{existing: true, line: strings.Count(content[:docStart], "\n") + 1}, true
}

func leadingDocumentationPreamble(content string) (int, int) {
	offset, lastEnd, docStart := 0, 0, -1
	for offset < len(content) {
		for offset < len(content) && strings.ContainsRune(" \t\r\n", rune(content[offset])) {
			offset++
		}
		remaining := content[offset:]
		switch {
		case strings.HasPrefix(remaining, "/*"):
			end := strings.Index(remaining[2:], "*/")
			if end < 0 {
				return 0, -1
			}
			if strings.HasPrefix(remaining, "/**") {
				docStart = offset
			}
			offset += end + 4
			lastEnd = offset
		case strings.HasPrefix(remaining, "//"):
			end := strings.IndexAny(remaining, "\r\n")
			if end < 0 {
				return 0, -1
			}
			offset += end + 1
			lastEnd = offset
		default:
			return lastEnd, docStart
		}
	}
	return lastEnd, docStart
}
