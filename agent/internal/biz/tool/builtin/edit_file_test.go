package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalEditFileReplacesFirstOrAllOccurrences(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "edit.txt")
	if err := os.WriteFile(path, []byte("a a a"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Read-before-edit guard: satisfy it the same way the model would.
	readResult := executeLocalFileTool(t, NewLocalReadFileTool(localFileTestSchema("read_file")), workspace, `{"file_path":"edit.txt"}`)
	if readResult.IsError {
		t.Fatalf("setup read failed: %#v", readResult.Value)
	}
	// Non-unique old_text without replace_all must surface the ambiguity so
	// the model can either retry with more context or pass replace_all=true.
	result := executeLocalFileTool(t, NewLocalEditFileTool(localFileTestSchema("edit_file")), workspace, `{"file_path":"edit.txt","old_text":"a","new_text":"b"}`)
	if !result.IsError {
		t.Fatalf("expected ambiguous old_text to error, got %#v", result.Value)
	}
	if !strings.Contains(stringifyEditResult(result.Value), "matched 3 locations") {
		t.Fatalf("expected match-count hint in error, got %#v", result.Value)
	}
	// Unique old_text replaces exactly once.
	result = executeLocalFileTool(t, NewLocalEditFileTool(localFileTestSchema("edit_file")), workspace, `{"file_path":"edit.txt","old_text":"a a","new_text":"b"}`)
	if result.IsError {
		t.Fatalf("unexpected unique-replacement error: %#v", result.Value)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "b a" || result.Value["replacements"] != 1 {
		t.Fatalf("unexpected first replacement: content=%q result=%#v", raw, result.Value)
	}
	// replace_all=true replaces every occurrence.
	result = executeLocalFileTool(t, NewLocalEditFileTool(localFileTestSchema("edit_file")), workspace, `{"file_path":"edit.txt","old_text":"a","new_text":"c","replace_all":true}`)
	if result.IsError {
		t.Fatalf("unexpected all replacement error: %#v", result.Value)
	}
	raw, _ = os.ReadFile(path)
	// After the unique replacement the file is "b a", so one "a" remains.
	if string(raw) != "b c" || result.Value["replacements"] != 1 {
		t.Fatalf("unexpected all replacement: content=%q result=%#v", raw, result.Value)
	}
}

func TestLocalEditFileMatchesAndPreservesLineEndings(t *testing.T) {
	tests := []struct {
		name         string
		initial      string
		args         string
		want         string
		replacements int
	}{
		{
			name:         "crlf unique match from normalized read view",
			initial:      "alpha\r\nbeta\r\ngamma\r\n",
			args:         `{"file_path":"sample.txt","old_text":"alpha\nbeta","new_text":"alpha\nBETA"}`,
			want:         "alpha\r\nBETA\r\ngamma\r\n",
			replacements: 1,
		},
		{
			name:         "crlf replace all",
			initial:      "alpha\r\nbeta\r\nalpha\r\nbeta\r\n",
			args:         `{"file_path":"sample.txt","old_text":"alpha\nbeta","new_text":"A\nB","replace_all":true}`,
			want:         "A\r\nB\r\nA\r\nB\r\n",
			replacements: 2,
		},
		{
			name:         "crlf deletion with empty replacement",
			initial:      "keep\r\nremove\r\nend\r\n",
			args:         `{"file_path":"sample.txt","old_text":"remove\n","new_text":""}`,
			want:         "keep\r\nend\r\n",
			replacements: 1,
		},
		{
			name:         "lf regression",
			initial:      "alpha\nbeta\ngamma\n",
			args:         `{"file_path":"sample.txt","old_text":"alpha\nbeta","new_text":"alpha\nBETA"}`,
			want:         "alpha\nBETA\ngamma\n",
			replacements: 1,
		},
		{
			name:         "mixed file follows dominant crlf",
			initial:      "one\r\ntwo\nthree\r\n",
			args:         `{"file_path":"sample.txt","old_text":"two\nthree","new_text":"TWO\nTHREE"}`,
			want:         "one\r\nTWO\r\nTHREE\r\n",
			replacements: 1,
		},
		{
			name:         "match starts on normalized newline",
			initial:      "alpha\r\nbeta\r\n",
			args:         `{"file_path":"sample.txt","old_text":"\nbeta","new_text":"\nBETA"}`,
			want:         "alpha\r\nBETA\r\n",
			replacements: 1,
		},
		{
			name:         "crlf old text against lf file",
			initial:      "alpha\nbeta\n",
			args:         `{"file_path":"sample.txt","old_text":"alpha\r\nbeta","new_text":"A\r\nB"}`,
			want:         "A\nB\n",
			replacements: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "sample.txt")
			if err := os.WriteFile(path, []byte(test.initial), 0o600); err != nil {
				t.Fatal(err)
			}
			readResult := executeLocalFileTool(t, NewLocalReadFileTool(localFileTestSchema("read_file")), workspace, `{"file_path":"sample.txt"}`)
			if readResult.IsError {
				t.Fatalf("setup read failed: %#v", readResult.Value)
			}
			result := executeLocalFileTool(t, NewLocalEditFileTool(localFileTestSchema("edit_file")), workspace, test.args)
			if result.IsError {
				t.Fatalf("unexpected edit error: %#v", result.Value)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(raw); got != test.want {
				t.Fatalf("unexpected content: got %q want %q", got, test.want)
			}
			if got := result.Value["replacements"]; got != test.replacements {
				t.Fatalf("unexpected replacement count: got %#v want %d", got, test.replacements)
			}
		})
	}
}

func stringifyEditResult(v map[string]any) string {
	if v == nil {
		return ""
	}
	if err, ok := v["error"].(string); ok {
		return err
	}
	return ""
}

// TestLocalEditFileRequiresReadFirst locks the anti-hallucination guard:
// editing an existing file without a prior read_file fails; after read_file
// (or write_file, which counts as seen) the same edit succeeds.
func TestLocalEditFileRequiresReadFirst(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "guard.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	editTool := NewLocalEditFileTool(localFileTestSchema("edit_file"))

	blocked := executeLocalFileTool(t, editTool, workspace, `{"file_path":"guard.txt","old_text":"hello","new_text":"hi"}`)
	if !blocked.IsError {
		t.Fatalf("edit without prior read should be blocked, got %#v", blocked.Value)
	}
	if !strings.Contains(stringifyEditResult(blocked.Value), "read-before-edit") {
		t.Fatalf("unexpected guard message: %#v", blocked.Value)
	}

	// read_file unblocks the path (same shared empty session key).
	read := executeLocalFileTool(t, NewLocalReadFileTool(localFileTestSchema("read_file")), workspace, `{"file_path":"guard.txt"}`)
	if read.IsError {
		t.Fatalf("read failed: %#v", read.Value)
	}
	ok := executeLocalFileTool(t, editTool, workspace, `{"file_path":"guard.txt","old_text":"hello","new_text":"hi"}`)
	if ok.IsError {
		t.Fatalf("edit after read should pass, got %#v", ok.Value)
	}

	// write_file also counts as seen (model authored the content).
	fresh := filepath.Join(workspace, "fresh.txt")
	if err := os.WriteFile(fresh, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = fresh
	written := executeLocalFileTool(t, NewLocalWriteFileTool(localFileTestSchema("write_file")), workspace, `{"file_path":"fresh2.txt","content":"abc"}`)
	if written.IsError {
		t.Fatalf("write failed: %#v", written.Value)
	}
	editFresh := executeLocalFileTool(t, editTool, workspace, `{"file_path":"fresh2.txt","old_text":"abc","new_text":"abd"}`)
	if editFresh.IsError {
		t.Fatalf("edit after write should pass, got %#v", editFresh.Value)
	}
}
