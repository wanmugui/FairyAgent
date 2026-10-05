package main

import (
	"regexp"
	"strings"
)

// maskInlineToolCalls removes complete inline pseudo tool calls
// (functions.name({...})) from content so they never stream as visible text.
// A trailing call that is still streaming (no closing paren yet) is held back
// (everything from "functions." onward), because the loop converts complete
// calls into native tool_calls after the stream ends.
func maskInlineToolCalls(content string) string {
	for {
		start := strings.Index(content, "functions.")
		if start < 0 {
			return content
		}
		rest := content[start+len("functions."):]
		endName := strings.IndexByte(rest, '(')
		if endName <= 0 {
			return content[:start]
		}
		if strings.TrimSpace(rest[:endName]) == "" {
			return content[:start]
		}
		closeIdx := findInlineCallClose(rest, endName+1)
		if closeIdx < 0 {
			return content[:start]
		}
		content = content[:start] + rest[closeIdx+1:]
	}
}

// partialTagRe matches a trailing "<" that could be the start of a protocol
// tag but has no closing ">" yet (e.g. "<msg" or "</mes" while streaming).
var partialTagRe = regexp.MustCompile(`<[A-Za-z/][^>]*$`)

// holdBackIncompleteTag trims a trailing incomplete tag so partial tags never
// stream as visible text. The held-back text is either completed and stripped
// on a later update (it becomes a tag) or emitted once the tag never forms.
func holdBackIncompleteTag(clean string) string {
	loc := partialTagRe.FindStringIndex(clean)
	if loc == nil {
		return clean
	}
	return clean[:loc[0]]
}
