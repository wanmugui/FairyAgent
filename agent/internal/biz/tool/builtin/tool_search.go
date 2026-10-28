package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"agentloop/agent/internal/dtypes"
)

// toolSearchMaxDescription caps how much of a tool description is echoed back.
// Some MCP tools carry multi-kilobyte descriptions; the model only needs the
// leading summary to decide whether to unlock the tool, and the full schema
// arrives with the tool itself.
const toolSearchMaxDescription = 400

// toolSearchMaxSiblings caps how many sibling tools are listed per hit. The
// list exists so the model knows neighbouring capabilities exist, not so it
// can enumerate the whole catalogue.
const toolSearchMaxSiblings = 8

// toolSearchEntry is one deferred tool as reported by tool_search.
type toolSearchEntry struct {
	// Name is the model-visible tool name, including any server namespace.
	Name string `json:"name"`
	// Description is the short summary of what the tool does.
	Description string `json:"description"`
	// Group identifies the owning provider, e.g. an MCP server name.
	Group string `json:"group"`
	// Siblings lists other tools from the same group. Deferred tools are
	// hidden by default, so the model cannot infer that a neighbouring
	// capability exists; surfacing siblings keeps that context available.
	Siblings []string `json:"siblings,omitempty"`
	// Exposed reports whether this tool is already unlocked for use.
	Exposed bool `json:"exposed"`
	// Score is the ranking weight of this hit.
	Score int `json:"score"`
}

// NewLocalToolSearchTool returns a tool that searches the deferred tool
// catalogue and unlocks the tools it returns. Deferred tools stay callable
// and registered; only their schema is withheld from the model until they are
// exposed, so a large MCP catalogue does not occupy every request.
func NewLocalToolSearchTool(schema ToolDef, index dtypes.DeferredIndex) Tool {
	return newLocalStructuredTool("tool_search", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("tool_search", err), nil
		}
		query := strings.TrimSpace(localStringArg(args, "query"))
		limit := localIntArg(args, "limit", 10)
		if limit <= 0 {
			limit = 10
		}
		if index == nil {
			return localErrorResult("tool_search", fmt.Errorf("deferred tool index is not available")), nil
		}

		entries := index.ListDeferred()
		hits := make([]toolSearchEntry, 0, len(entries))
		for _, info := range entries {
			score := toolSearchScore(query, info.Name, info.Description, info.Group)
			if query != "" && score <= 0 {
				continue
			}
			hits = append(hits, toolSearchEntry{
				Name:        info.Name,
				Description: truncateToolSearchText(info.Description, toolSearchMaxDescription),
				Group:       info.Group,
				Siblings:    toolSearchSiblings(entries, info),
				Score:       score,
			})
		}
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].Score != hits[j].Score {
				return hits[i].Score > hits[j].Score
			}
			return hits[i].Name < hits[j].Name
		})
		if len(hits) > limit {
			hits = hits[:limit]
		}

		// Unlock every hit so the model can call it on the next turn. The
		// schema enters the request only after this call returns, which is
		// exactly the intended two-step flow: search, then call.
		unlocked := make([]string, 0, len(hits))
		for i := range hits {
			if index.Expose(hits[i].Name) {
				hits[i].Exposed = true
				unlocked = append(unlocked, hits[i].Name)
			}
		}

		return ToolResult{Value: map[string]any{
			"query":    query,
			"results":  hits,
			"count":    len(hits),
			"unlocked": unlocked,
			"hint":     "Unlocked tools appear in the next request's tool list. Call them directly, or search again for a closer match.",
		}}, nil
	})
}

// toolSearchSiblings lists the other tools sharing the same provider group.
func toolSearchSiblings(entries []dtypes.DeferredToolInfo, self dtypes.DeferredToolInfo) []string {
	if self.Group == "" {
		return nil
	}
	siblings := make([]string, 0, 8)
	for _, info := range entries {
		if info.Name == self.Name || info.Group != self.Group {
			continue
		}
		siblings = append(siblings, info.Name)
		if len(siblings) >= toolSearchMaxSiblings {
			break
		}
	}
	if len(siblings) == 0 {
		return nil
	}
	return siblings
}

// truncateToolSearchText shortens text to a maximum length, cutting on a rune
// boundary so the result stays valid UTF-8.
func truncateToolSearchText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

// toolSearchScore ranks a deferred tool against the query. Name matches
// weigh more than description matches, and every query term that appears in
// the description contributes, mirroring the skill_search ranking style.
func toolSearchScore(query, name, description, group string) int {
	if query == "" {
		return 1
	}
	lowerQuery := strings.ToLower(query)
	lowerName := strings.ToLower(name)
	lowerDesc := strings.ToLower(description)
	lowerGroup := strings.ToLower(group)

	score := 0
	if strings.Contains(lowerDesc, lowerQuery) {
		score += 6
	}
	if strings.Contains(lowerName, lowerQuery) {
		score += 5
	}
	if strings.Contains(lowerGroup, lowerQuery) {
		score += 2
	}
	for _, term := range strings.FieldsFunc(lowerQuery, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if term == "" {
			continue
		}
		if strings.Contains(lowerName, term) {
			score += 3
		}
		score += strings.Count(lowerDesc, term)
	}
	return score
}
