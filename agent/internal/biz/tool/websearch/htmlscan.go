package websearch

import (
	"strings"
)

// anchorMatch describes one <a> tag found in an HTML document along with the
// textual content nested inside it. Cheap to extract because we only care
// about class + href + inner text.
type anchorMatch struct {
	Class string
	Href  string
	Text  string
}

// findAnchors walks an HTML body once and returns every <a ...> tag with its
// class= and href= attributes plus its inner text. We deliberately do NOT
// invoke a full HTML parser — both DDG and Bing layouts we target put each
// result's title and link on a single <a>, so a regex-free byte scan is
// enough and avoids the x/net/html dependency.
func findAnchors(body string) []anchorMatch {
	out := make([]anchorMatch, 0, 16)
	i := 0
	for i < len(body) {
		idx := strings.Index(body[i:], "<a")
		if idx < 0 {
			break
		}
		i += idx
		// Confirm we are looking at <a followed by space or >.
		if i+2 >= len(body) {
			break
		}
		next := body[i+2]
		if next != ' ' && next != '>' && next != '\t' && next != '\n' && next != '\r' {
			i += 2
			continue
		}
		// Find end of opening tag.
		tagEnd := indexOfUnquoted(body, i, '>')
		if tagEnd < 0 {
			break
		}
		tag := body[i:tagEnd]
		class := attrValue(tag, "class")
		href := attrValue(tag, "href")
		// Find matching </a>.
		closeIdx := strings.Index(strings.ToLower(body[tagEnd:]), "</a>")
		var inner string
		if closeIdx >= 0 {
			inner = body[tagEnd : tagEnd+closeIdx]
		} else {
			inner = body[tagEnd:]
		}
		out = append(out, anchorMatch{
			Class: class,
			Href:  href,
			Text:  innerText(inner),
		})
		i = tagEnd + 1
	}
	return out
}

// indexOfUnquoted returns the position of `ch` after `from`, ignoring any
// occurrences inside a quoted attribute. We use byte literals because
// attribute values cannot contain raw '<'.
func indexOfUnquoted(s string, from int, ch byte) int {
	inSingle := false
	inDouble := false
	for j := from; j < len(s); j++ {
		c := s[j]
		switch c {
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case ch:
			if !inSingle && !inDouble {
				return j
			}
		}
	}
	return -1
}

// attrValue pulls the value of `name="..."` (or single-quoted) out of a tag.
func attrValue(tag, name string) string {
	lower := strings.ToLower(tag)
	idx := strings.Index(lower, name+"=")
	if idx < 0 {
		return ""
	}
	idx += len(name) + 1
	if idx >= len(tag) {
		return ""
	}
	quote := tag[idx]
	if quote != '"' && quote != '\'' {
		// Bare value (rare). Take up to whitespace or end.
		end := idx
		for end < len(tag) {
			c := tag[end]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '>' {
				break
			}
			end++
		}
		return strings.TrimSpace(tag[idx:end])
	}
	idx++
	end := strings.IndexByte(tag[idx:], quote)
	if end < 0 {
		return ""
	}
	return tag[idx : idx+end]
}

// innerText strips every tag from a fragment and decodes a handful of HTML
// entities. Just enough to render DDG/Bing result titles.
func innerText(s string) string {
	var b strings.Builder
	inTag := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '<':
			inTag = true
		case c == '>':
			inTag = false
		case !inTag:
			if c == '&' {
				// Decode a few common entities.
				if rest, ok := decodeEntity(s[i:]); ok {
					b.WriteString(rest)
					// Advance past the entity.
					advance := strings.IndexAny(s[i:], ";")
					if advance > 0 {
						i += advance
					}
					continue
				}
			}
			b.WriteByte(c)
		}
	}
	// Collapse whitespace.
	return collapseWS(b.String())
}

func decodeEntity(s string) (string, bool) {
	if len(s) < 4 || s[0] != '&' {
		return "", false
	}
	end := strings.IndexByte(s, ';')
	if end < 0 || end > 8 {
		return "", false
	}
	switch s[:end+1] {
	case "&amp;":
		return "&", true
	case "&lt;":
		return "<", true
	case "&gt;":
		return ">", true
	case "&quot;":
		return `"`, true
	case "&apos;":
		return "'", true
	case "&nbsp;":
		return " ", true
	}
	return "", false
}

func collapseWS(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// hasClassToken reports whether `classes` (a space-separated class attribute)
// contains the exact token `want`.
func hasClassToken(classes, want string) bool {
	for _, c := range strings.Fields(classes) {
		if c == want {
			return true
		}
	}
	return false
}
