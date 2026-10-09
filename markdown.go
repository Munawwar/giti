package main

import (
	"fmt"
	"html"
	"strings"

	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

func markdownInline(node ast.Node, source []byte) string {
	var children strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		children.WriteString(markdownInline(child, source))
	}
	content := children.String()
	switch node := node.(type) {
	case *ast.Text:
		value := node.Segment.Value(source)
		if !node.IsRaw() {
			value = util.UnescapePunctuations(value)
		}
		content = html.EscapeString(html.UnescapeString(string(value)))
		if node.HardLineBreak() {
			content += "\n"
		} else if node.SoftLineBreak() {
			content += " "
		}
	case *ast.String:
		content = html.EscapeString(string(node.Value))
	case *ast.Emphasis:
		if node.Level == 2 {
			content = "<b>" + content + "</b>"
		} else {
			content = "<i>" + content + "</i>"
		}
	case *ast.CodeSpan:
		var code strings.Builder
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			code.Write(child.Text(source))
		}
		value := strings.ReplaceAll(code.String(), "\n", " ")
		if strings.HasPrefix(value, " ") && strings.HasSuffix(value, " ") && strings.TrimSpace(value) != "" {
			value = value[1 : len(value)-1]
		}
		content = `<span font_family="monospace" background="#f3f4f5">` + html.EscapeString(value) + `</span>`
	case *ast.Link:
		content = `<a href="` + html.EscapeString(string(node.Destination)) + `">` + content + `</a>`
	case *ast.AutoLink:
		content = `<a href="` + html.EscapeString(string(node.URL(source))) + `">` + html.EscapeString(string(node.Label(source))) + `</a>`
	case *ast.Image:
		content = `<i>` + content + `</i>`
	case *ast.RawHTML:
		content = html.EscapeString(string(node.Text(source)))
	case *extast.Strikethrough:
		content = "<s>" + content + "</s>"
	case *extast.TaskCheckBox:
		content = "☐ "
		if node.IsChecked {
			content = "☑ "
		}
	}
	return content
}

func markdownLabel(markup string) *gtk.Label {
	label := must(gtk.LabelNew(""))
	label.SetMarkup(markup)
	label.SetSelectable(true)
	label.SetXAlign(0)
	label.SetYAlign(0)
	label.SetLineWrap(true)
	label.SetLineWrapMode(pango.WRAP_WORD_CHAR)
	label.SetMaxWidthChars(90)
	return label
}

// Render Markdown into native GTK widgets, keeping preview and export on the
// same document without introducing a browser runtime or remote image loads.
func renderMarkdown(document string) *gtk.Box {
	source := []byte(document)
	root := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	page := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 8))
	context, _ := page.GetStyleContext()
	context.AddClass("giti-review-document")
	var render func(ast.Node, *gtk.Box)
	render = func(parent ast.Node, target *gtk.Box) {
		for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
			switch node := node.(type) {
			case *ast.Heading:
				size, top := "medium", 10
				if node.Level == 1 {
					size, top = "xx-large", 0
				} else if node.Level == 2 {
					size, top = "large", 18
				}
				label := markdownLabel(`<span weight="bold" size="` + size + `">` + markdownInline(node, source) + `</span>`)
				label.SetMarginTop(top)
				label.SetMarginBottom(4)
				target.PackStart(label, false, false, 0)
			case *ast.Paragraph, *ast.TextBlock:
				target.PackStart(markdownLabel(markdownInline(node, source)), false, false, 0)
			case *ast.CodeBlock, *ast.FencedCodeBlock:
				var code strings.Builder
				for index := 0; index < node.Lines().Len(); index++ {
					line := node.Lines().At(index)
					value := string(line.Value(source))
					markup := html.EscapeString(value)
					if fenced, ok := node.(*ast.FencedCodeBlock); ok && string(fenced.Language(source)) == "diff" {
						if strings.HasPrefix(value, "+") {
							markup = `<span foreground="#174d22">` + markup + `</span>`
						} else if strings.HasPrefix(value, "-") {
							markup = `<span foreground="#682126">` + markup + `</span>`
						}
					}
					code.WriteString(markup)
				}
				box := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
				context, _ := box.GetStyleContext()
				context.AddClass("giti-review-code")
				box.PackStart(markdownLabel(`<span font_family="monospace">`+strings.TrimSuffix(code.String(), "\n")+`</span>`), false, false, 0)
				target.PackStart(box, false, false, 0)
			case *ast.List:
				index := node.Start
				for item := node.FirstChild(); item != nil; item = item.NextSibling() {
					row := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 10))
					marker := "•"
					if node.IsOrdered() {
						marker = fmt.Sprintf("%d.", index)
						index++
					}
					row.PackStart(markdownLabel(marker), false, false, 0)
					body := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
					render(item, body)
					row.PackStart(body, true, true, 0)
					target.PackStart(row, false, false, 0)
				}
			case *ast.Blockquote:
				quote := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 8))
				context, _ := quote.GetStyleContext()
				context.AddClass("giti-review-quote")
				render(node, quote)
				target.PackStart(quote, false, false, 0)
			case *ast.ThematicBreak:
				target.PackStart(must(gtk.SeparatorNew(gtk.ORIENTATION_HORIZONTAL)), false, false, 6)
			case *extast.Table:
				grid := must(gtk.GridNew())
				grid.SetColumnSpacing(16)
				grid.SetRowSpacing(8)
				rowIndex := 0
				for row := node.FirstChild(); row != nil; row = row.NextSibling() {
					column := 0
					for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
						markup := markdownInline(cell, source)
						if row.Kind() == extast.KindTableHeader {
							markup = "<b>" + markup + "</b>"
						}
						label := markdownLabel(markup)
						label.SetHExpand(true)
						grid.Attach(label, column, rowIndex, 1, 1)
						column++
					}
					rowIndex++
				}
				target.PackStart(grid, false, false, 4)
			case *ast.HTMLBlock:
				target.PackStart(markdownLabel(html.EscapeString(string(node.Text(source)))), false, false, 0)
			default:
				render(node, target)
			}
		}
	}
	render(root, page)
	return page
}
