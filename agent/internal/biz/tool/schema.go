package tool

import (
	"agentloop/agent/internal/dtypes"
	"encoding/json"
	"fmt"
	"os"
)

// Schema is the on-disk representation used by config/tools/schemas.json.
type Schema struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

func LoadSchemas(path string) (map[string]Schema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schemas: %w", err)
	}
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		raw = raw[3:]
	}
	var schemas map[string]Schema
	if err := json.Unmarshal(raw, &schemas); err != nil {
		return nil, fmt.Errorf("parse schemas: %w", err)
	}
	return schemas, nil
}

func (s Schema) ToolDef() dtypes.ToolDef {
	return dtypes.ToolDef{Type: "function", Function: map[string]interface{}{
		"name":        s.Name,
		"description": s.Description,
		"parameters":  s.Parameters,
	}}
}
