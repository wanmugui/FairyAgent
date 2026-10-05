package tool

import (
	"agentloop/agent/internal/dtypes"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// RegisteredTool 同时记录工具实现，以及启动时为它选择的执行后端。
// Backend 用于诊断和检查配置（工具来源）；真正执行工具时仍然统一调用 Tool.Execute。
type RegisteredTool struct {
	Name    string
	Backend dtypes.ToolBackend
	Tool    dtypes.Tool
}

// ToolRegistry 管理暴露给模型的规范工具名称空间。
// 查找工具名称时不区分大小写；ListNames/ListSchemas 保留注册实现返回的原始名称。
type Registry struct {
	tools          map[string]RegisteredTool
	normalizedName map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		tools:          make(map[string]RegisteredTool),
		normalizedName: make(map[string]string),
	}
}

// 工具名统一转小写字母，去除前后空格，作为规范化名称用于查找和冲突检测
func normalizeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// 注册工具实现
func (r *Registry) Register(backend dtypes.ToolBackend, tool dtypes.Tool) error {
	if tool == nil {
		return fmt.Errorf("register tool: implementation is nil")
	}
	name := strings.TrimSpace(tool.Name())
	if name == "" {
		return fmt.Errorf("register tool: name is empty")
	}
	if r.tools == nil {
		r.tools = make(map[string]RegisteredTool)
	}
	if r.normalizedName == nil {
		r.normalizedName = make(map[string]string)
	}
	normalized := normalizeToolName(name)
	if existing, ok := r.normalizedName[normalized]; ok {
		return fmt.Errorf("register tool %q: name conflicts with already registered tool %q", name, existing)
	}
	r.tools[name] = RegisteredTool{Name: name, Backend: backend, Tool: tool}
	r.normalizedName[normalized] = name
	return nil
}

func (r *Registry) Get(name string) (dtypes.Tool, bool) {
	canonical, ok := r.normalizedName[normalizeToolName(name)]
	if !ok {
		return nil, false
	}
	registered, ok := r.tools[canonical]
	if !ok {
		return nil, false
	}
	return registered.Tool, true
}

func (r *Registry) GetBackend(name string) (dtypes.ToolBackend, bool) {
	canonical, ok := r.normalizedName[normalizeToolName(name)]
	if !ok {
		return "", false
	}
	registered, ok := r.tools[canonical]
	if !ok {
		return "", false
	}
	return registered.Backend, true
}

func (r *Registry) ListNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) ListSchemas() []dtypes.ToolDef {
	names := r.ListNames()
	schemas := make([]dtypes.ToolDef, 0, len(names))
	for _, name := range names {
		if registered, ok := r.tools[name]; ok && registered.Tool != nil {
			schemas = append(schemas, registered.Tool.Schema())
		}
	}
	return schemas
}

// Close releases optional tool resources such as MCP transports and browser
// sessions. Multiple tools may share one client; Close must be idempotent.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	var errs []error
	for _, name := range r.ListNames() {
		registered, ok := r.tools[name]
		if !ok || registered.Tool == nil {
			continue
		}
		if closer, ok := registered.Tool.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close tool %q: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}
