package builtin

import (
	"context"
	"time"
)

type LocalTimeTool struct {
	name   string
	schema ToolDef
	now    func() time.Time
}

func NewLocalTimeTool(schema ToolDef) Tool {
	return NewLocalTimeToolWithClock(schema, time.Now)
}

func NewLocalTimeToolWithClock(schema ToolDef, clock func() time.Time) Tool {
	if clock == nil {
		clock = time.Now
	}
	return &LocalTimeTool{name: "get_current_time", schema: schema, now: clock}
}

func (t *LocalTimeTool) Name() string {
	return t.name
}

func (t *LocalTimeTool) Schema() ToolDef {
	return t.schema
}

func (t *LocalTimeTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	now := t.now()
	timezone, _ := now.Zone()
	return ToolResult{Value: map[string]any{
		"current_time": now.Format("2006-01-02 15:04:05"),
		"date":         now.Format("2006-01-02"),
		"time":         now.Format("15:04:05"),
		"day_of_week":  now.Weekday().String(),
		"timezone":     timezone,
		"tomorrow":     now.AddDate(0, 0, 1).Format("2006-01-02"),
		"yesterday":    now.AddDate(0, 0, -1).Format("2006-01-02"),
		"unix_ts":      now.Unix(),
	}}, nil
}
