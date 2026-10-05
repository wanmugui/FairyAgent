package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Minimal Jinja2-subset renderer used to expand the modular system-prompt
// template (config/system/zh.md) at runtime. Supports the constructs
// used by the locale parts:
//
//	{% if EXPR %} ... {% elif EXPR %} ... {% else %} ... {% endif %}
//	{{ VAR }}  /  {{ VAR|safe }}
//	whitespace control: {%- and -%}
//
// EXPR supports: VAR, !VAR, not VAR, A or B, A and B, A and not B, VAR > N.

type jinjaKind int

const (
	jinjaText jinjaKind = iota
	jinjaOut
	jinjaStmt
)

type jinjaTok struct {
	kind  jinjaKind
	text  string
	trimL bool
	trimR bool
}

func tokenizeJinja(src string) ([]jinjaTok, error) {
	var toks []jinjaTok
	pos := 0
	pendingTrimL := false
	ws := " \t\r\n"
	for pos < len(src) {
		d := strings.Index(src[pos:], "{{")
		s := strings.Index(src[pos:], "{%")
		open, kind, close := -1, jinjaText, ""
		switch {
		case d == -1 && s == -1:
			open = -1
		case d == -1:
			open, kind, close = s, jinjaStmt, "%}"
		case s == -1:
			open, kind, close = d, jinjaOut, "}}"
		case d < s:
			open, kind, close = d, jinjaOut, "}}"
		default:
			open, kind, close = s, jinjaStmt, "%}"
		}
		if open == -1 {
			seg := src[pos:]
			if pendingTrimL {
				seg = strings.TrimLeft(seg, ws)
			}
			if seg != "" {
				toks = append(toks, jinjaTok{kind: jinjaText, text: seg})
			}
			break
		}
		seg := src[pos : pos+open]
		if pendingTrimL {
			seg = strings.TrimLeft(seg, ws)
			pendingTrimL = false
		}
		if seg != "" {
			toks = append(toks, jinjaTok{kind: jinjaText, text: seg})
		}
		c := strings.Index(src[pos+open:], close)
		if c == -1 {
			return nil, fmt.Errorf("unclosed %s tag", close)
		}
		closeAt := pos + open + c
		body := src[pos+open+2 : closeAt]
		trimL := strings.HasPrefix(body, "-")
		trimR := strings.HasSuffix(body, "-")
		body = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(body, "-"), "-"))
		if trimL && len(toks) > 0 && toks[len(toks)-1].kind == jinjaText {
			toks[len(toks)-1].text = strings.TrimRight(toks[len(toks)-1].text, ws)
		}
		toks = append(toks, jinjaTok{kind: kind, text: body, trimL: trimL, trimR: trimR})
		if trimR {
			pendingTrimL = true
		}
		pos = closeAt + len(close)
	}
	return toks, nil
}

type jinjaNode interface {
	render(vars map[string]any) string
}

type jinjaTextNode struct{ text string }

func (n jinjaTextNode) render(vars map[string]any) string { return n.text }

type jinjaOutNode struct{ expr string }

func (n jinjaOutNode) render(vars map[string]any) string {
	expr := strings.TrimSuffix(strings.TrimSpace(n.expr), "|safe")
	expr = strings.TrimSpace(expr)
	if v, ok := vars[expr]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

type jinjaIfBranch struct {
	cond  string
	nodes []jinjaNode
}

type jinjaIfNode struct {
	branches []jinjaIfBranch
}

func (n jinjaIfNode) render(vars map[string]any) string {
	for _, b := range n.branches {
		if b.cond == "" || evalJinjaCond(b.cond, vars) {
			var sb strings.Builder
			for _, nd := range b.nodes {
				sb.WriteString(nd.render(vars))
			}
			return sb.String()
		}
	}
	return ""
}

func parseJinja(toks []jinjaTok, i int) ([]jinjaNode, int, error) {
	var nodes []jinjaNode
	for i < len(toks) {
		t := toks[i]
		switch t.kind {
		case jinjaText:
			nodes = append(nodes, jinjaTextNode{t.text})
			i++
		case jinjaOut:
			nodes = append(nodes, jinjaOutNode{t.text})
			i++
		case jinjaStmt:
			switch {
			case t.text == "endif" || t.text == "else" || strings.HasPrefix(t.text, "elif "):
				return nodes, i, nil
			case strings.HasPrefix(t.text, "if "):
				node, ni, err := parseJinjaIf(toks, i, strings.TrimSpace(strings.TrimPrefix(t.text, "if ")))
				if err != nil {
					return nil, i, err
				}
				nodes = append(nodes, node)
				i = ni
			default:
				return nil, i, fmt.Errorf("unsupported statement %q", t.text)
			}
		}
	}
	return nodes, i, nil
}

func parseJinjaIf(toks []jinjaTok, i int, cond string) (jinjaNode, int, error) {
	br := jinjaIfNode{branches: []jinjaIfBranch{{cond: cond}}}
	i++
	for {
		inner, ni, err := parseJinja(toks, i)
		if err != nil {
			return br, i, err
		}
		br.branches[len(br.branches)-1].nodes = inner
		i = ni
		if i >= len(toks) {
			return br, i, fmt.Errorf("unterminated if")
		}
		st := toks[i]
		if st.kind != jinjaStmt {
			return br, i, fmt.Errorf("expected endif/elif/else after if body")
		}
		switch {
		case strings.HasPrefix(st.text, "elif "):
			br.branches = append(br.branches, jinjaIfBranch{cond: strings.TrimSpace(strings.TrimPrefix(st.text, "elif "))})
			i++
		case st.text == "else":
			br.branches = append(br.branches, jinjaIfBranch{cond: ""})
			i++
		case st.text == "endif":
			i++
			return br, i, nil
		default:
			return br, i, fmt.Errorf("unexpected tag %q inside if", st.text)
		}
	}
}

var jinjaGtRe = regexp.MustCompile(`^(\w+)\s*>\s*(\d+)$`)

func evalJinjaCond(expr string, vars map[string]any) bool {
	for _, part := range strings.Split(expr, " or ") {
		if evalJinjaAnd(part, vars) {
			return true
		}
	}
	return false
}

func evalJinjaAnd(expr string, vars map[string]any) bool {
	for _, part := range strings.Split(expr, " and ") {
		if !evalJinjaTerm(strings.TrimSpace(part), vars) {
			return false
		}
	}
	return true
}

func evalJinjaTerm(expr string, vars map[string]any) bool {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "not ") {
		return !evalJinjaTerm(strings.TrimSpace(strings.TrimPrefix(expr, "not ")), vars)
	}
	if m := jinjaGtRe.FindStringSubmatch(expr); m != nil {
		n, _ := strconv.Atoi(m[2])
		return jinjaNum(vars[m[1]]) > n
	}
	return jinjaTruthy(vars[expr])
}

func jinjaNum(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func jinjaTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t != ""
	default:
		return true
	}
}

// renderJinja renders a Jinja2-subset template with the given variables.
func renderJinja(src string, vars map[string]any) (string, error) {
	toks, err := tokenizeJinja(src)
	if err != nil {
		return "", err
	}
	nodes, i, err := parseJinja(toks, 0)
	if err != nil {
		return "", err
	}
	if i != len(toks) {
		return "", fmt.Errorf("leftover tokens at %d", i)
	}
	var sb strings.Builder
	for _, n := range nodes {
		sb.WriteString(n.render(vars))
	}
	return sb.String(), nil
}

// stripTemplateTags removes any remaining template tags (fallback on render error).
func stripTemplateTags(src string) string {
	re := regexp.MustCompile(`\{\{[\s\S]*?\}\}|\{%[\s\S]*?%\}`)
	return re.ReplaceAllString(src, "")
}
