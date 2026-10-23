package channels

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	xhtml "golang.org/x/net/html"
)

// Preserve Markdown blocks rather than cutting through links, tables or inline
// delimiters. Oversized fenced code can be reopened in subsequent messages.
// Indivisible blocks are submitted intact: only the platform knows their
// rendered length and whether its structural limits allow them.
func splitMarkdown(source string, limit int) ([]string, error) {
	var blocks []string
	var block strings.Builder
	lines := strings.SplitAfter(source, "\n")
	flush := func() {
		if block.Len() > 0 {
			blocks = append(blocks, block.String())
			block.Reset()
		}
	}
	for i := 0; i < len(lines); i++ {
		marker, width, _, fence := markdownFence(lines[i])
		if !fence {
			block.WriteString(lines[i])
			if strings.TrimSpace(lines[i]) == "" {
				flush()
			}
			continue
		}
		flush()
		header := strings.TrimRight(lines[i], "\r\n") + "\n"
		var content strings.Builder
		for i++; i < len(lines); i++ {
			endMarker, endWidth, rest, closing := markdownFence(lines[i])
			if closing && endMarker == marker && endWidth >= width && strings.TrimSpace(rest) == "" {
				break
			}
			content.WriteString(lines[i])
		}
		footer := strings.Repeat(string(marker), width)
		budget := limit - utf16Length(header+footer) - 2
		if budget < 2 {
			blocks = append(blocks, header+content.String()+footer+"\n")
			continue
		}
		pieces := splitUnits(content.String(), budget)
		if len(pieces) == 0 {
			pieces = []string{""}
		}
		for _, piece := range pieces {
			if piece != "" && !strings.HasSuffix(piece, "\n") {
				piece += "\n"
			}
			blocks = append(blocks, header+piece+footer+"\n")
		}
	}
	flush()
	return packMarkupBlocks(blocks, limit)
}

func markdownFence(line string) (byte, int, string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || trimmed[0] != '`' && trimmed[0] != '~' {
		return 0, 0, "", false
	}
	width := 0
	for width < len(trimmed) && trimmed[width] == trimmed[0] {
		width++
	}
	return trimmed[0], width, trimmed[width:], width >= 3
}

func splitUnits(source string, limit int) []string {
	var parts []string
	start, units, lastBreak := 0, 0, 0
	for index, r := range source {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if units+width > limit {
			end := index
			if lastBreak > start+(index-start)/2 {
				end = lastBreak
			}
			parts = append(parts, source[start:end])
			start = end
			units = utf16Length(source[start:index])
			lastBreak = start
		}
		units += width
		if r == '\n' {
			lastBreak = index + 1
		}
	}
	if start < len(source) {
		parts = append(parts, source[start:])
	}
	return parts
}

func packMarkupBlocks(blocks []string, limit int) ([]string, error) {
	var parts []string
	var current strings.Builder
	length := 0
	for _, block := range blocks {
		size := utf16Length(block)
		if length > 0 && length+size > limit {
			parts = append(parts, current.String())
			current.Reset()
			length = 0
		}
		current.WriteString(block)
		length += size
	}
	if strings.TrimSpace(current.String()) != "" {
		parts = append(parts, current.String())
	}
	return parts, nil
}

// HTML is split only between complete top-level elements, never inside a tag,
// attribute or entity. Large indivisible elements retain their source so the
// platform can validate the rendered content instead of a source-size guess.
func splitHTML(source string, limit int) ([]string, error) {
	z := xhtml.NewTokenizer(strings.NewReader(source))
	var blocks []string
	var current strings.Builder
	depth := 0
	for {
		kind := z.Next()
		if kind == xhtml.ErrorToken {
			if z.Err() != io.EOF {
				return nil, z.Err()
			}
			break
		}
		current.Write(z.Raw())
		switch kind {
		case xhtml.StartTagToken:
			tag, _ := z.TagName()
			if !strings.Contains(" area base br col embed hr img input link meta param source track wbr ", " "+string(tag)+" ") {
				depth++
			}
		case xhtml.EndTagToken:
			depth--
		}
		if depth == 0 {
			blocks = append(blocks, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		blocks = append(blocks, current.String())
	}
	return packMarkupBlocks(blocks, limit)
}

// Weixin's published plugin preserves only part of Markdown. Parse before
// adapting so literal asterisks, filenames and fenced code remain intact.
// Images remain readable links; uploading media still requires attachments.
func weixinMarkdown(source string) string {
	var rendered bytes.Buffer
	md := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithRendererOptions(goldhtml.WithUnsafe()))
	_ = md.Convert([]byte(source), &rendered)
	root, _ := xhtml.Parse(&rendered)
	var render func(*xhtml.Node) string
	children := func(n *xhtml.Node) string {
		var out strings.Builder
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			out.WriteString(render(child))
		}
		return out.String()
	}
	attr := func(n *xhtml.Node, name string) string {
		for _, a := range n.Attr {
			if a.Key == name {
				return a.Val
			}
		}
		return ""
	}
	render = func(n *xhtml.Node) string {
		if n.Type == xhtml.TextNode {
			return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`").Replace(n.Data)
		}
		if n.Type == xhtml.CommentNode {
			return ""
		}
		switch n.Data {
		case "head":
			return ""
		case "pre":
			code := htmlNodeText(n)
			language := ""
			if n.FirstChild != nil {
				language = strings.TrimPrefix(attr(n.FirstChild, "class"), "language-")
			}
			fence := strings.Repeat("`", max(3, maxBackticks(code)+1))
			return fence + language + "\n" + strings.TrimSuffix(code, "\n") + "\n" + fence + "\n\n"
		case "code":
			code := htmlNodeText(n)
			fence := strings.Repeat("`", maxBackticks(code)+1)
			return fence + " " + code + " " + fence
		case "img":
			return attr(n, "alt") + " (" + attr(n, "src") + ")"
		case "a":
			return "[" + children(n) + "](" + strings.ReplaceAll(attr(n, "href"), ")", "%29") + ")"
		case "strong":
			return "**" + children(n) + "**"
		case "em":
			content := children(n)
			if strings.ContainsFunc(content, func(r rune) bool {
				return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
			}) {
				return content
			}
			return "*" + content + "*"
		case "del":
			return "~~" + children(n) + "~~"
		case "h1", "h2", "h3", "h4":
			return strings.Repeat("#", int(n.Data[1]-'0')) + " " + children(n) + "\n\n"
		case "h5", "h6", "p":
			return children(n) + "\n\n"
		case "br":
			return "\n"
		case "hr":
			return "---\n\n"
		case "blockquote":
			return "> " + strings.ReplaceAll(strings.TrimSpace(children(n)), "\n", "\n> ") + "\n\n"
		case "ul", "ol":
			var out strings.Builder
			index := 1
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Data != "li" {
					continue
				}
				prefix := "- "
				if n.Data == "ol" {
					prefix = strconv.Itoa(index) + ". "
				}
				out.WriteString(prefix + strings.ReplaceAll(strings.TrimSpace(children(child)), "\n", "\n  ") + "\n")
				index++
			}
			return out.String() + "\n"
		case "table":
			var rows []string
			var visit func(*xhtml.Node)
			visit = func(row *xhtml.Node) {
				if row.Data == "tr" {
					var cells []string
					for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
						if cell.Data == "td" || cell.Data == "th" {
							cells = append(cells, strings.ReplaceAll(strings.TrimSpace(children(cell)), "|", "\\|"))
						}
					}
					rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
					if len(rows) == 1 {
						rows = append(rows, "|"+strings.Repeat(" --- |", len(cells)))
					}
					return
				}
				for child := row.FirstChild; child != nil; child = child.NextSibling {
					visit(child)
				}
			}
			visit(n)
			return strings.Join(rows, "\n") + "\n\n"
		case "input":
			for _, a := range n.Attr {
				if a.Key == "checked" {
					return "[x] "
				}
			}
			return "[ ] "
		default:
			return children(n)
		}
	}
	return strings.TrimSpace(render(root))
}

func htmlNodeText(n *xhtml.Node) string {
	if n.Type == xhtml.TextNode {
		return n.Data
	}
	var out strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		out.WriteString(htmlNodeText(child))
	}
	return out.String()
}

func maxBackticks(text string) int {
	longest, current := 0, 0
	for _, r := range text {
		if r == '`' {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return longest
}
