package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLocalWorkspacePathKeepsRelativePathInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()

	got, err := resolveLocalWorkspacePath(workspace, "notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workspace, "notes", "a.md")
	if got != want {
		t.Fatalf("unexpected path: got %q want %q", got, want)
	}
}

func TestResolveLocalWorkspacePathAllowsParentTraversal(t *testing.T) {
	// Post-loosen: the agent can read/write paths outside the workspace.
	// "../outside.txt" now resolves to the parent of the temp dir (allowed).
	workspace := t.TempDir()
	parent := filepath.Dir(workspace)

	for _, path := range []string{"../outside.txt", "local://../outside.txt"} {
		got, err := resolveLocalWorkspacePath(workspace, path)
		if err != nil {
			t.Fatalf("expected %q to resolve, got error: %v", path, err)
		}
		want := filepath.Join(parent, "outside.txt")
		if got != want {
			t.Fatalf("unexpected resolution: got %q want %q", got, want)
		}
	}
}

func TestResolveLocalWorkspacePathAcceptsLocalPrefix(t *testing.T) {
	workspace := t.TempDir()

	got, err := resolveLocalWorkspacePath(workspace, "local://nested/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(workspace, "nested", "file.txt"); got != want {
		t.Fatalf("unexpected local path: got %q want %q", got, want)
	}
}

func TestResolveLocalWorkspacePathMapsProductionMntDataRoot(t *testing.T) {
	workspace := t.TempDir()

	got, err := resolveLocalWorkspacePath(workspace, "/mnt/data/result/pptid_demo")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(workspace, "result", "pptid_demo"); got != want {
		t.Fatalf("unexpected production path: got %q want %q", got, want)
	}
}

func TestResolveLocalReadablePathMapsProductionSkillPathToConfiguredRoot(t *testing.T) {
	workspace := t.TempDir()
	skillsRoot := t.TempDir()

	got, root, err := resolveLocalReadablePath(workspace, skillsRoot, "local:///skills/ppt-maker/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if root != "skills" {
		t.Fatalf("unexpected path root: %q", root)
	}
	if want := filepath.Join(skillsRoot, "ppt-maker", "SKILL.md"); got != want {
		t.Fatalf("unexpected skill path: got %q want %q", got, want)
	}
}

func TestResolveLocalReadablePathAllowsHostPathsAndRejectsMemory(t *testing.T) {
	workspace := t.TempDir()
	// Post-loosen: arbitrary absolute host paths via local:// are accepted;
	// memory:// and knowledge:// remain unsupported by this local backend.
	for _, requested := range []string{"memory://today.md", "knowledge://project/file.md"} {
		if _, _, err := resolveLocalReadablePath(workspace, t.TempDir(), requested); err == nil {
			t.Fatalf("expected %q to be rejected", requested)
		}
	}
	// And a host-style path now succeeds.
	got, root, err := resolveLocalReadablePath(workspace, t.TempDir(), "local:///etc/hosts")
	if err != nil {
		t.Fatalf("expected host path to resolve, got: %v", err)
	}
	if root != "workspace" {
		t.Fatalf("unexpected root kind: %q", root)
	}
	if !strings.HasSuffix(got, string(filepath.Separator)+"etc"+string(filepath.Separator)+"hosts") {
		t.Fatalf("unexpected resolution: %q", got)
	}
}

func TestResolveLocalReadablePathKeepsBareSkillsPathInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	skillsRoot := t.TempDir()

	got, root, err := resolveLocalReadablePath(workspace, skillsRoot, "skills/notes.md")
	if err != nil {
		t.Fatal(err)
	}
	if root != "workspace" {
		t.Fatalf("unexpected path root: %q", root)
	}
	if want := filepath.Join(workspace, "skills", "notes.md"); got != want {
		t.Fatalf("unexpected workspace path: got %q want %q", got, want)
	}
}

func TestDecodeLocalToolArgsRejectsInvalidJSON(t *testing.T) {
	_, err := decodeLocalToolArgs(ToolInvocation{Args: json.RawMessage(`{"broken":`)})
	if err == nil || !strings.Contains(err.Error(), "parse tool arguments") {
		t.Fatalf("expected invalid JSON error, got %v", err)
	}
}

func TestWriteJSONFileAtomicallyCreatesReadableJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "result.json")
	payload := map[string]any{"ok": true, "count": 2}

	if err := writeJSONFileAtomically(path, payload); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("written file is not JSON: %v", err)
	}
	if got["ok"] != true || got["count"] != float64(2) {
		t.Fatalf("unexpected payload: %#v", got)
	}
}

func TestResolveLocalReadablePathAcceptsWindowsDrivePath(t *testing.T) {
	workspace := t.TempDir()
	for _, requested := range []string{`C:\project\frontend`, `C:/project/frontend`, `C:\project`} {
		got, root, err := resolveLocalReadablePath(workspace, t.TempDir(), requested)
		if err != nil {
			t.Fatalf("expected Windows drive path %q to resolve, got: %v", requested, err)
		}
		if root != "workspace" {
			t.Fatalf("unexpected root kind for %q: %q", requested, root)
		}
		gotSlash := strings.ToLower(filepath.ToSlash(got))
		if !strings.HasSuffix(gotSlash, "project/frontend") && !strings.HasSuffix(gotSlash, "project") {
			t.Fatalf("unexpected resolution for %q: %q", requested, got)
		}
	}
}
