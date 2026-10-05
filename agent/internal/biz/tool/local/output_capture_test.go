package local

import (
	"os"
	"strings"
	"testing"
)

func TestBoundedOutputCaptureKeepsTailAndSpillFile(t *testing.T) {
	capture := newBoundedOutputCapture(8)
	if _, err := capture.Write([]byte("abcdefghij")); err != nil {
		t.Fatal(err)
	}
	result := capture.Result()
	if !result.Truncated || result.Bytes != 10 || !strings.Contains(result.Text, "full output:") || !strings.HasSuffix(result.Text, "cdefghij") {
		t.Fatalf("unexpected capture result: %#v", result)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcdefghij" {
		t.Fatalf("unexpected spill contents: %q", data)
	}
	_ = os.Remove(result.Path)
}

func TestBoundedOutputCaptureRemovesSpillWhenNotTruncated(t *testing.T) {
	capture := newBoundedOutputCapture(64)
	if _, err := capture.Write([]byte("short")); err != nil {
		t.Fatal(err)
	}
	result := capture.Result()
	if result.Truncated || result.Path != "" || result.Text != "short" || result.Bytes != 5 {
		t.Fatalf("unexpected capture result: %#v", result)
	}
}
