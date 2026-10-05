package builtin

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestLocalTimeToolReturnsExpectedFields(t *testing.T) {
	clock := func() time.Time {
		return time.Date(2026, time.August, 11, 15, 4, 5, 0, time.FixedZone("CST", 8*60*60))
	}
	tool := NewLocalTimeToolWithClock(localFileTestSchema("get_current_time"), clock)
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Name: "get_current_time",
		Args: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	for key, want := range map[string]any{
		"current_time": "2026-08-11 15:04:05",
		"date":         "2026-08-11",
		"time":         "15:04:05",
		"day_of_week":  "Tuesday",
		"timezone":     "CST",
		"tomorrow":     "2026-08-12",
		"yesterday":    "2026-08-10",
		"unix_ts":      int64(1786431845),
	} {
		if got := result.Value[key]; got != want {
			t.Errorf("unexpected %s: got %#v want %#v", key, got, want)
		}
	}
}
