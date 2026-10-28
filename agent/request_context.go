package main

import (
	"os"
	"strings"
)

func sessionTodoFile() string {
	return strings.TrimSpace(os.Getenv("AGENT_SESSION_FILE"))
}

// injectRequestContext prepends Fairy's active plan snapshot to the LLM-bound
// user message. Plans are the only persisted execution state.
func injectRequestContext(userMessage string, cfg *Config) string {
	if userMessage == "" {
		return userMessage
	}
	prefix := buildPlanPrefix(cfg, userMessage)
	return prefix + userMessage
}

// requestContextWrappers lists the wrappers injectRequestContext may prepend.
var requestContextWrappers = []struct{ open, close string }{
	{planTagOpen, planTagClose},
}

// stripRequestContextPrefix removes the leading plan wrapper before the
// message is persisted, so stored history matches what the user actually sent.
func stripRequestContextPrefix(content string) string {
	for {
		stripped := false
		for _, wrapper := range requestContextWrappers {
			if !strings.HasPrefix(content, wrapper.open) {
				continue
			}
			end := strings.Index(content, wrapper.close)
			if end < 0 {
				return content
			}
			content = content[end+len(wrapper.close):]
			stripped = true
			break
		}
		if !stripped {
			return content
		}
	}
}
