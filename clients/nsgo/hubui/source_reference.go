package hubui

import (
	"strings"

	"github.com/monkeyWie/reignhub/pkg/filelink"
	"github.com/nsgo-dev/nsgo/base/textview"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

type sourceReference struct{ label, hint string }

func (ref sourceReference) Render(cx *core.Scope) core.Node {
	hint := label(cx, ref.hint, 12, false)
	// The overlay can be measured at the window width before its own cap
	// applies. Give the text the same content bound when measuring its height.
	hint.StyleFunc = func() core.Style {
		return core.Style{Color: palette(cx).foreground, MaxWidth: core.Pt(tooltipMaxWidth(cx) - 2*tooltipInset), WhiteSpace: core.WhiteSpaceNormal}
	}
	return core.View{Label: ref.hint, Tooltip: tooltipCard(cx, hint),
		Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 3},
		Children: []core.Node{
			core.Icon{SVG: lucide.CodeXml(), Style: core.Style{Width: core.Pt(13), Height: core.Pt(13)}, StyleFunc: func() core.Style { return core.Style{Color: palette(cx).brand} }},
			core.Text{Content: ref.label, StyleFunc: func() core.Style {
				color := palette(cx).brand
				return core.Style{FontSize: 15, Color: color, TextDecoration: core.TextDecorationUnderline, TextDecorationColor: color.Faded(.5)}
			}},
		}}
}

func sourceReferenceSpan(node textview.Custom) core.Span {
	if ref, ok := node.Data.(sourceReference); ok {
		return core.Span{Content: ref.label, Attachment: ref}
	}
	return core.Span{Content: node.Text}
}

func messageDocument(source, cwd string, streaming bool) textview.Document {
	doc := textview.ParseWith(source, textview.Options{Streaming: streaming})
	if !strings.ContainsAny(source, ":#") {
		return doc
	}
	sourceReferenceBlocks(doc.Blocks, cwd)
	for _, note := range doc.Footnotes {
		sourceReferenceBlocks(note.Children, cwd)
	}
	return doc
}

// Visit parsed links so fenced code, literal examples and images keep their
// native Markdown semantics. Child slices belong to this newly parsed document.
func sourceReferenceBlocks(blocks []textview.Block, cwd string) {
	for _, block := range blocks {
		switch b := block.(type) {
		case textview.Paragraph:
			sourceReferenceInlines(b.Content, cwd)
		case textview.Heading:
			sourceReferenceInlines(b.Content, cwd)
		case textview.Blockquote:
			sourceReferenceBlocks(b.Children, cwd)
		case textview.List:
			for _, item := range b.Items {
				sourceReferenceBlocks(item.Children, cwd)
			}
		case textview.Details:
			sourceReferenceInlines(b.Summary, cwd)
			sourceReferenceBlocks(b.Children, cwd)
		case textview.Table:
			for _, cell := range b.Head {
				sourceReferenceInlines(cell.Content, cwd)
			}
			for _, row := range b.Rows {
				for _, cell := range row {
					sourceReferenceInlines(cell.Content, cwd)
				}
			}
		}
	}
}

func sourceReferenceInlines(items []textview.Inline, cwd string) {
	for i, item := range items {
		switch n := item.(type) {
		case textview.Link:
			if label, hint := filelink.SourceLocation(n.URL, cwd); label != "" {
				items[i] = textview.Custom{Name: "source-reference", Text: label, Data: sourceReference{label, hint}}
			}
		case textview.Strong:
			sourceReferenceInlines(n.Content, cwd)
		case textview.Emphasis:
			sourceReferenceInlines(n.Content, cwd)
		case textview.Strikethrough:
			sourceReferenceInlines(n.Content, cwd)
		case textview.Underline:
			sourceReferenceInlines(n.Content, cwd)
		case textview.Mark:
			sourceReferenceInlines(n.Content, cwd)
		case textview.Superscript:
			sourceReferenceInlines(n.Content, cwd)
		case textview.Subscript:
			sourceReferenceInlines(n.Content, cwd)
		case textview.Small:
			sourceReferenceInlines(n.Content, cwd)
		}
	}
}
