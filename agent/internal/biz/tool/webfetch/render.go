package webfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// renderBridgeRelPath is the repo-relative location of the Playwright bridge
// that the `browser` tool drives. We shell out to it as a one-shot process
// rather than importing its package: webfetch must not take a hard dependency
// on a browser toolchain, and this way a missing browser simply degrades to
// "no rendering" instead of breaking the whole package.
const renderBridgeRelPath = "agent/internal/biz/tool/local/browser_bridge.mjs"

// renderTriggerLen is the rendered-text length below which we suspect the page
// needed JavaScript. Measured against a real case: 房天下's 商圈 pages render
// their community price table via XHR, so the raw HTML body yields a stub of
// roughly this size while the hydrated page is an order of magnitude larger.
const renderTriggerLen = 1200

// renderMaxText is capped by the browser bridge's own MAX_TEXT (16000), so
// asking for more is pointless — it would be silently truncated anyway.
const renderMaxText = 15000

// tryRender re-fetches url through a real browser and returns the hydrated
// document text. It is a best-effort fallback: any failure returns ("", err)
// and the caller keeps the plain-HTTP result.
func tryRender(ctx context.Context, url string) (string, error) {
	repo := os.Getenv("FAIRY_REPO_ROOT")
	if repo == "" {
		return "", errors.New("FAIRY_REPO_ROOT unset; cannot locate browser bridge")
	}
	bridge := filepath.Join(repo, filepath.FromSlash(renderBridgeRelPath))
	if _, err := os.Stat(bridge); err != nil {
		return "", fmt.Errorf("browser bridge not found at %s: %w", bridge, err)
	}
	node := "node"
	if v := os.Getenv("FAIRY_NODE_BIN"); v != "" {
		node = v
	}

	// The bridge keeps a persistent session keyed to this process tree; a
	// The bridge handles exactly one action per process and tears its browser
	// down afterwards, so goto+eval cannot be chained across invocations.
	// "open" is the only action that returns the post-load page state in a
	// single round trip, so it is the one we use.
	out, err := runBridge(ctx, node, bridge, map[string]any{
		"action":     "open",
		"url":        url,
		"wait_until": "domcontentloaded",
	})
	if err != nil {
		return "", fmt.Errorf("render open: %w", err)
	}

	// The bridge truncates innerText at its own MAX_TEXT, so this is a prefix
	// of the hydrated document. That is still far more than the plain-HTTP
	// shell we are trying to replace.
	page, ok := out["page"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("render open returned no page snapshot (%T)", out["page"])
	}
	text, ok := page["text"].(string)
	if !ok {
		return "", fmt.Errorf("render page snapshot has no text (%v)", keysOf(page))
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("render open returned empty text")
	}
	return text, nil
}

// keysOf lists the snapshot keys, so a contract mismatch is diagnosable from
// the error instead of just failing.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// runBridge feeds one JSON request to the bridge on stdin and decodes the
// JSON response it writes to stdout.
func runBridge(ctx context.Context, node, bridge string, req map[string]any) (map[string]any, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// A browser round-trip is slow; give it room but never outlive ctx.
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, node, bridge)
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Env = append(os.Environ(), "FAIRY_REPO_ROOT="+os.Getenv("FAIRY_REPO_ROOT"))

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("bridge exit %v: %s", err, truncateForErr(stderr.String()))
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		return nil, fmt.Errorf("bridge response is not JSON: %s", truncateForErr(stdout.String()))
	}
	return out, nil
}

func truncateForErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// shouldTryRender decides whether a plain-HTTP result looks like an unhydrated
// shell worth re-fetching in a browser.
//
// Two conditions must hold: the payload is HTML (JSON/PDF/plain text never
// benefit), and the extracted text is short enough that it is implausibly thin
// for a real document. Long-but-poor pages are left alone because re-rendering
// them costs a browser launch without improving the result.
func shouldTryRender(contentType, bodyText string) bool {
	if !strings.Contains(contentType, "html") {
		return false
	}
	// Cap pages are always tiny and never worth a browser.
	if len([]rune(bodyText)) >= renderTriggerLen {
		return false
	}
	// Skip obvious error/interstitial pages: a browser round-trip will not
	// turn a 404 or a login wall into content.
	low := strings.ToLower(bodyText)
	for _, marker := range []string{"404", "not found", "access denied", "人机验证", "安全验证", "captcha"} {
		if strings.Contains(low, marker) {
			return false
		}
	}
	return true
}
