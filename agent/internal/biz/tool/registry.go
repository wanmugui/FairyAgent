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
	// deferred 记录已注册但默认不向模型暴露 schema 的工具（通常是 MCP 工具）。
	// 它们仍然可以被 Get 取到并执行，只是需要先经 tool_search 解锁。
	deferred map[string]string
	// exposed 记录已被解锁、schema 应当进入后续请求的延迟工具名。
	exposed map[string]bool
}

func NewRegistry() *Registry {
	return &Registry{
		tools:          make(map[string]RegisteredTool),
		normalizedName: make(map[string]string),
		deferred:       make(map[string]string),
		exposed:        make(map[string]bool),
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

// RegisterDeferred registers a tool that stays callable but whose schema is not
// advertised to the model until it is exposed. group identifies the owning
// provider (an MCP server name) so tool_search can report sibling tools.
func (r *Registry) RegisterDeferred(backend dtypes.ToolBackend, group string, tool dtypes.Tool) error {
	if err := r.Register(backend, tool); err != nil {
		return err
	}
	if r.deferred == nil {
		r.deferred = make(map[string]string)
	}
	name := strings.TrimSpace(tool.Name())
	r.deferred[name] = strings.TrimSpace(group)
	return nil
}

// ListDeferred returns the searchable summary of every deferred tool. Deferred
// tools that have already been exposed are excluded: they are no longer hidden.
func (r *Registry) ListDeferred() []dtypes.DeferredToolInfo {
	infos := make([]dtypes.DeferredToolInfo, 0, len(r.deferred))
	for name, group := range r.deferred {
		if r.exposed[name] {
			continue
		}
		info := dtypes.DeferredToolInfo{Name: name, Group: group}
		if registered, ok := r.tools[name]; ok && registered.Tool != nil {
			info.Description = registered.Tool.Schema().ToolDescription()
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// Expose promotes a deferred tool so its schema enters subsequent requests.
// It reports whether the tool exists and was deferred.
func (r *Registry) Expose(name string) bool {
	canonical, ok := r.normalizedName[normalizeToolName(name)]
	if !ok {
		return false
	}
	if _, deferred := r.deferred[canonical]; !deferred {
		return false
	}
	if r.exposed == nil {
		r.exposed = make(map[string]bool)
	}
	r.exposed[canonical] = true
	return true
}

// ListExposedSchemas returns the schemas the model should see: every
// non-deferred tool plus every deferred tool that has been exposed. The result
// stays sorted by tool name, matching ListSchemas ordering.
func (r *Registry) ListExposedSchemas() []dtypes.ToolDef {
	names := r.ListNames()
	schemas := make([]dtypes.ToolDef, 0, len(names))
	for _, name := range names {
		registered, ok := r.tools[name]
		if !ok || registered.Tool == nil {
			continue
		}
		if _, deferred := r.deferred[name]; deferred && !r.exposed[name] {
			continue
		}
		schemas = append(schemas, registered.Tool.Schema())
	}
	return schemas
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
