package sender

import (
	"html"
	"strings"
	"unicode"
)

// HTMLToPlainText converts an HTML document into formatted plain text suitable for email alternatives and notification channels.
func HTMLToPlainText(rawHTML string) string {
	if strings.TrimSpace(rawHTML) == "" {
		return ""
	}
	parsed := parseHTMLTags(rawHTML)
	unescaped := html.UnescapeString(parsed)
	return cleanLines(unescaped)
}

type htmlParser struct {
	skipTag          string
	anchorHref       string
	sb               strings.Builder
	anchorStartPos   int
	inPre            bool
	inTableCell      bool
	isFirstCellInRow bool
}

func newHTMLParser() *htmlParser {
	return &htmlParser{
		anchorStartPos:   -1,
		isFirstCellInRow: true,
	}
}

func (p *htmlParser) handleChar(ch byte) {
	if p.skipTag != "" {
		return
	}
	if (ch == '\r' || ch == '\n' || ch == '\t' || ch == ' ') && !p.inPre {
		s := p.sb.String()
		if s != "" && s[len(s)-1] != ' ' && s[len(s)-1] != '\n' {
			p.sb.WriteByte(' ')
		}
		return
	}
	p.sb.WriteByte(ch)
}

func parseHTMLTags(rawHTML string) string {
	p := newHTMLParser()
	n := len(rawHTML)
	i := 0

	for i < n {
		if rawHTML[i] != '<' {
			p.handleChar(rawHTML[i])
			i++
			continue
		}

		if strings.HasPrefix(rawHTML[i:], "<!--") {
			end := strings.Index(rawHTML[i+4:], "-->")
			if end == -1 {
				break
			}
			i += 4 + end + 3
			continue
		}

		tagEnd := strings.IndexByte(rawHTML[i:], '>')
		if tagEnd == -1 {
			break
		}
		fullTag := rawHTML[i : i+tagEnd+1]
		i += tagEnd + 1

		isClosing := strings.HasPrefix(fullTag, "</")
		tagName := extractTagName(fullTag)

		if p.skipTag != "" {
			if isClosing && strings.EqualFold(tagName, p.skipTag) {
				p.skipTag = ""
			}
			continue
		}

		if !isClosing && isSkippedTag(tagName) {
			p.skipTag = strings.ToLower(tagName)
			continue
		}

		if isClosing {
			p.handleClosingTag(tagName)
		} else {
			p.handleOpeningTag(fullTag, tagName)
		}
	}

	return p.sb.String()
}

func isSkippedTag(name string) bool {
	return strings.EqualFold(name, "script") ||
		strings.EqualFold(name, "style") ||
		strings.EqualFold(name, "head") ||
		strings.EqualFold(name, "noscript") ||
		strings.EqualFold(name, "svg")
}

func (p *htmlParser) handleClosingTag(tagName string) {
	switch strings.ToLower(tagName) {
	case "p", "h1", "h2", "h3", "h4", "h5", "h6":
		ensureNewlines(&p.sb, 2)
	case "div", "blockquote":
		if p.inTableCell {
			appendCellInlineBreak(&p.sb)
		} else {
			ensureNewlines(&p.sb, 1)
		}
	case "li", "ul", "ol":
		ensureNewlines(&p.sb, 1)
	case "table":
		ensureNewlines(&p.sb, 1)
		p.inTableCell = false
		p.isFirstCellInRow = true
	case "td", "th":
		p.inTableCell = false
	case "tr":
		p.inTableCell = false
		ensureNewlines(&p.sb, 1)
		p.isFirstCellInRow = true
	case "pre":
		p.inPre = false
		if !p.inTableCell {
			ensureNewlines(&p.sb, 1)
		}
	case "a":
		if p.anchorStartPos >= 0 {
			handleAnchorClose(&p.sb, p.anchorStartPos, p.anchorHref)
			p.anchorStartPos = -1
			p.anchorHref = ""
		}
	}
}

func (p *htmlParser) handleOpeningTag(fullTag, tagName string) {
	switch strings.ToLower(tagName) {
	case "a":
		p.anchorHref = extractAttr(fullTag, "href")
		p.anchorStartPos = p.sb.Len()
	case "img":
		if alt := extractAttr(fullTag, "alt"); alt != "" {
			p.sb.WriteByte('[')
			p.sb.WriteString(alt)
			p.sb.WriteByte(']')
		}
	default:
		p.handleOpeningBlockTag(tagName)
	}
}

func (p *htmlParser) handleOpeningBlockTag(tagName string) {
	switch strings.ToLower(tagName) {
	case "br":
		if p.inTableCell {
			appendCellInlineBreak(&p.sb)
		} else {
			p.sb.WriteByte('\n')
		}
	case "p", "h1", "h2", "h3", "h4", "h5", "h6":
		ensureNewlines(&p.sb, 2)
	case "div", "blockquote":
		if p.inTableCell {
			appendCellInlineBreak(&p.sb)
		} else {
			ensureNewlines(&p.sb, 1)
		}
	case "ul", "ol":
		ensureNewlines(&p.sb, 1)
	case "table":
		ensureNewlines(&p.sb, 1)
		p.inTableCell = false
		p.isFirstCellInRow = true
	case "tr":
		ensureNewlines(&p.sb, 1)
		p.isFirstCellInRow = true
	case "td", "th":
		p.inTableCell = true
		if !p.isFirstCellInRow {
			p.sb.WriteString(" | ")
		}
		p.isFirstCellInRow = false
	case "pre":
		p.inPre = true
		if p.inTableCell {
			appendCellInlineBreak(&p.sb)
		} else {
			ensureNewlines(&p.sb, 1)
		}
	case "li":
		ensureNewlines(&p.sb, 1)
		p.sb.WriteString("* ")
	case "hr":
		ensureNewlines(&p.sb, 1)
		p.sb.WriteString("---")
		ensureNewlines(&p.sb, 1)
	}
}

func appendCellInlineBreak(sb *strings.Builder) {
	s := sb.String()
	if s == "" {
		return
	}
	last := s[len(s)-1]
	if last == '\n' || last == '|' || last == ' ' {
		return
	}
	sb.WriteString(" - ")
}

func handleAnchorClose(sb *strings.Builder, startPos int, href string) {
	cleanHref := strings.TrimSpace(href)
	lowerHref := strings.ToLower(cleanHref)
	if cleanHref == "" || strings.HasPrefix(cleanHref, "#") ||
		strings.HasPrefix(lowerHref, "javascript:") ||
		strings.HasPrefix(lowerHref, "vbscript:") ||
		strings.HasPrefix(lowerHref, "data:") {
		return
	}
	currentText := sb.String()
	linkText := strings.TrimSpace(currentText[startPos:])
	if linkText == "" {
		return
	}
	cleanTrimmed := strings.TrimRight(cleanHref, "/")
	linkTrimmed := strings.TrimRight(linkText, "/")
	if strings.EqualFold(cleanTrimmed, linkTrimmed) {
		return
	}
	sb.WriteString(" (")
	sb.WriteString(cleanHref)
	sb.WriteByte(')')
}

func ensureNewlines(sb *strings.Builder, count int) {
	s := sb.String()
	if s == "" {
		return
	}
	existing := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\n'; i-- {
		existing++
	}
	for existing < count {
		sb.WriteByte('\n')
		existing++
	}
}

func extractTagName(tag string) string {
	s := strings.TrimPrefix(tag, "</")
	s = strings.TrimPrefix(s, "<")
	idx := strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == '/' || r == '>'
	})
	if idx != -1 {
		return s[:idx]
	}
	return s
}

func extractAttr(tag, attrName string) string {
	lowerTag := strings.ToLower(tag)
	target := strings.ToLower(attrName) + "="
	idx := strings.Index(lowerTag, target)
	if idx == -1 {
		return ""
	}
	valPart := tag[idx+len(target):]
	valPart = strings.TrimLeft(valPart, " \t\r\n")
	if valPart == "" {
		return ""
	}
	quote := valPart[0]
	if quote == '"' || quote == '\'' {
		endQuote := strings.IndexByte(valPart[1:], quote)
		if endQuote != -1 {
			return html.UnescapeString(valPart[1 : 1+endQuote])
		}
		return html.UnescapeString(valPart[1:])
	}
	endIdx := strings.IndexFunc(valPart, func(r rune) bool {
		return unicode.IsSpace(r) || r == '>'
	})
	if endIdx != -1 {
		return html.UnescapeString(strings.TrimSuffix(valPart[:endIdx], "/"))
	}
	return html.UnescapeString(strings.TrimSuffix(valPart, "/"))
}

func cleanLines(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	prevBlank := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !prevBlank && len(result) > 0 {
				result = append(result, "")
				prevBlank = true
			}
			continue
		}
		prevBlank = false

		collapsed := collapseInlineSpaces(trimmed)
		result = append(result, collapsed)
	}

	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}

	return strings.Join(result, "\n")
}

func collapseInlineSpaces(s string) string {
	var sb strings.Builder
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\u00a0' {
			if !inSpace {
				sb.WriteByte(' ')
				inSpace = true
			}
		} else {
			sb.WriteRune(r)
			inSpace = false
		}
	}
	return sb.String()
}
