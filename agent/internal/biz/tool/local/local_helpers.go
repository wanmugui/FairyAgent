package local

import (
	"agentloop/agent/internal/biz/tool/shared"
	"path/filepath"
	"strings"
	"time"
)

func localToolTimeout(invocation ToolInvocation, requestedSeconds int, minimum, maximum time.Duration) time.Duration {
	if requestedSeconds <= 0 {
		requestedSeconds = int(minimum / time.Second)
	}
	timeout := time.Duration(requestedSeconds) * time.Second
	if timeout < minimum {
		timeout = minimum
	}
	if timeout > maximum {
		timeout = maximum
	}
	if invocation.Timeout > 0 && invocation.Timeout < timeout {
		timeout = invocation.Timeout
	}
	return timeout
}

func replaceMntDataReferences(value, workspace string) string {
	return replaceLocalPathReferences(value, workspace, "")
}

// replaceLocalPathReferences maps the two virtual filesystem roots described
// by the production tool contract before a local shell or Python process sees
// them. It intentionally accepts only those roots: other absolute paths stay
// untouched and are not treated as workspace paths.
func replaceLocalPathReferences(value, workspace, skillsRoot string) string {
	replacements := []struct {
		logicalRoot string
		replacement string
	}{
		{logicalRoot: "/mnt/data", replacement: filepath.ToSlash(workspace)},
	}
	// /skills is a single logical namespace, so it is anchored to the FIRST
	// configured root only. skillsRoot may now hold a list; rewriting to the
	// whole joined list would produce a path that exists nowhere.
	if roots := shared.SplitRoots(skillsRoot); len(roots) > 0 {
		replacements = append(replacements, struct {
			logicalRoot string
			replacement string
		}{logicalRoot: "/skills", replacement: filepath.ToSlash(roots[0])})
	}

	var result strings.Builder
	last := 0
	for searchFrom := 0; searchFrom < len(value); {
		index, logicalRoot, replacement := nextLocalPathReference(value, searchFrom, replacements)
		if index < 0 {
			break
		}
		end := index + len(logicalRoot)
		if isLocalPathBoundaryBefore(value, index) && isLocalPathBoundaryAfter(value, end) {
			result.WriteString(value[last:index])
			result.WriteString(replacement)
			last = end
		}
		searchFrom = end
	}
	if last == 0 {
		return value
	}
	result.WriteString(value[last:])
	return result.String()
}

func nextLocalPathReference(value string, searchFrom int, replacements []struct {
	logicalRoot string
	replacement string
}) (int, string, string) {
	bestIndex := -1
	var bestRoot, bestReplacement string
	for _, candidate := range replacements {
		relative := strings.Index(value[searchFrom:], candidate.logicalRoot)
		if relative < 0 {
			continue
		}
		index := searchFrom + relative
		if bestIndex < 0 || index < bestIndex {
			bestIndex, bestRoot, bestReplacement = index, candidate.logicalRoot, candidate.replacement
		}
	}
	return bestIndex, bestRoot, bestReplacement
}

func isLocalPathBoundaryBefore(value string, index int) bool {
	return index == 0 || strings.ContainsRune(" \t\r\n\"'=(:,;|&<>{[", rune(value[index-1]))
}

func isLocalPathBoundaryAfter(value string, index int) bool {
	return index == len(value) || strings.ContainsRune("/ \t\r\n\"'()[]{};,|&<>", rune(value[index]))
}
