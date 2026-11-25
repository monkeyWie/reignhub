package messagecontent

import (
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

var userMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Footnote))

// UserMarkdown preserves typed line breaks when a user bubble renders Markdown.
// Only soft breaks need a display marker. Fences, tables and existing hard
// breaks keep their original source, as do the stored and sent messages.
func UserMarkdown(source string) string {
	if !strings.Contains(source, "\n") {
		return source
	}
	root := userMarkdown.Parser().Parse(text.NewReader([]byte(source)))
	var breaks []int
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := n.(*ast.Text); entering && ok && t.SoftLineBreak() {
			end := t.Segment.Stop
			for end < len(source) && (source[end] == ' ' || source[end] == '\t') {
				end++
			}
			breaks = append(breaks, end)
		}
		return ast.WalkContinue, nil
	})
	if len(breaks) == 0 {
		return source
	}
	// Footnote definitions can be visited after their references.
	slices.Sort(breaks)
	var out strings.Builder
	out.Grow(len(source) + 2*len(breaks))
	start := 0
	for _, end := range slices.Compact(breaks) {
		out.WriteString(source[start:end])
		out.WriteString("  ")
		start = end
	}
	out.WriteString(source[start:])
	return out.String()
}
