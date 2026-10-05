package main

import (
	"agentloop/agent/internal/biz/mcp"
	toolruntime "agentloop/agent/internal/biz/tool"
	"agentloop/agent/internal/biz/tool/httptool"
	"context"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type LocalToolBuilder func(ToolDef, *Config) (Tool, error)

type ToolFactory struct {
	LocalBuilders map[string]LocalToolBuilder
}

func NewToolFactory() *ToolFactory {
	return &ToolFactory{LocalBuilders: builtinLocalToolBuilders()}
}

func (f *ToolFactory) BuildRegistry(cfg *Config) (*ToolRegistry, error) {
	if cfg == nil {
		return nil, fmt.Errorf("build tool registry: config is nil")
	}
	if err := ensureToolRuntimeDefaults(cfg); err != nil {
		return nil, err
	}
	schemas, err := toolruntime.LoadSchemas(cfg.SchemasPath())
	if err != nil {
		return nil, fmt.Errorf("build tool registry: %w", err)
	}

	registry := NewToolRegistry()
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, schemaKey := range names {
		schema := schemas[schemaKey]
		name := strings.TrimSpace(schema.Name)
		if name == "" {
			name = schemaKey
		}
		if !strings.EqualFold(name, schemaKey) {
			return nil, fmt.Errorf("tool schema key %q does not match schema name %q", schemaKey, schema.Name)
		}
		if !isConfiguredToolEnabled(cfg.HTTPTools, name) {
			continue
		}
		if !isToolAvailableOnPlatform(name) {
			continue
		}

		override, hasOverride := configuredBackendOverride(cfg.ToolRuntime.Tools, name)
		if !hasOverride {
			return nil, fmt.Errorf("tool %q has no backend configuration", name)
		}
		backend := override.Backend
		definition := schema.ToolDef()

		var tool Tool
		switch backend {
		case BackendLocal:
			builder, ok := localBuilder(f.LocalBuilders, name)
			if !ok {
				return nil, fmt.Errorf("tool %q uses local backend but no LocalToolBuilder is registered", name)
			}
			tool, err = builder(definition, cfg)
			if err != nil {
				return nil, fmt.Errorf("build local tool %q: %w", name, err)
			}
		case BackendHTTP:
			if cfg.Gateway.Endpoint == "" {
				return nil, fmt.Errorf("tool %q uses http backend but unified tool service endpoint is missing", name)
			}
			tool = httptool.NewHTTPTool(name, cfg.Gateway.Endpoint, definition, cfg.Gateway.Timeout, cfg.Gateway.BearerToken, cfg.Gateway.Headers, cfg.ToolRuntime.RetryCount, cfg.Gateway.HostPin)
		default:
			return nil, fmt.Errorf("tool %q uses unsupported backend %q", name, backend)
		}

		if tool == nil {
			return nil, fmt.Errorf("tool %q backend %q returned nil implementation", name, backend)
		}
		if err := registry.Register(backend, tool); err != nil {
			return nil, fmt.Errorf("register tool %q: %w", name, err)
		}
	}
	if err := f.registerMCPTools(cfg, registry); err != nil {
		_ = registry.Close()
		return nil, err
	}
	return registry, nil
}

func (f *ToolFactory) registerMCPTools(cfg *Config, registry *ToolRegistry) error {
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return nil
	}
	serverNames := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		serverNames = append(serverNames, name)
	}
	sort.Strings(serverNames)
	for _, serverName := range serverNames {
		serverCfg := cfg.MCPServers[serverName]
		if !serverCfg.IsEnabled() {
			continue
		}
		timeoutSec := serverCfg.StartupTimeoutSec
		if timeoutSec <= 0 {
			timeoutSec = 30
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
		cwd := strings.TrimSpace(serverCfg.Cwd)
		if cwd != "" && !filepath.IsAbs(cwd) {
			cwd = cfg.ResolvePath(cwd)
		}
		client, err := mcp.Connect(ctx, mcp.Options{
			Name:              serverName,
			Transport:         serverCfg.Transport,
			Command:           serverCfg.Command,
			Args:              serverCfg.Args,
			Env:               serverCfg.Env,
			Cwd:               cwd,
			URL:               serverCfg.URL,
			Headers:           serverCfg.Headers,
			ProtocolVersion:   serverCfg.ProtocolVersion,
			StartupTimeoutSec: timeoutSec,
			ToolPrefix:        serverCfg.ToolPrefix,
		})
		if err != nil {
			cancel()
			if serverCfg.Required {
				return fmt.Errorf("connect required MCP server %q: %w", serverName, err)
			}
			log.Printf("[mcp:%s] unavailable: %v", serverName, err)
			continue
		}
		descriptors, err := client.ListTools(ctx)
		cancel()
		if err != nil {
			_ = client.Close()
			if serverCfg.Required {
				return fmt.Errorf("list required MCP server %q tools: %w", serverName, err)
			}
			log.Printf("[mcp:%s] tools/list failed: %v", serverName, err)
			continue
		}
		registered := 0
		for _, descriptor := range descriptors {
			if !mcpToolAllowed(descriptor.Name, serverCfg.AllowedTools, serverCfg.DeniedTools) {
				continue
			}
			localName := mcp.NamespacedToolName(serverName, descriptor.Name, serverCfg.ToolPrefix)
			tool := mcp.NewTool(client, serverName, localName, descriptor)
			if err := registry.Register(BackendMCP, tool); err != nil {
				_ = client.Close()
				return fmt.Errorf("register MCP tool %q from server %q: %w", descriptor.Name, serverName, err)
			}
			registered++
		}
		if registered == 0 {
			_ = client.Close()
		}
	}
	return nil
}

func mcpToolAllowed(name string, allowed, denied []string) bool {
	for _, item := range denied {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
			return false
		}
	}
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func isConfiguredToolEnabled(entries map[string]ToolEntry, name string) bool {
	for configuredName, entry := range entries {
		if strings.EqualFold(configuredName, name) {
			return entry.Enabled
		}
	}
	return true
}

func isToolAvailableOnPlatform(name string) bool {
	if strings.EqualFold(strings.TrimSpace(name), "powershell") {
		return runtime.GOOS == "windows"
	}
	return true
}

func configuredBackendOverride(overrides map[string]ToolBackendOverride, name string) (ToolBackendOverride, bool) {
	for configuredName, override := range overrides {
		if strings.EqualFold(configuredName, name) {
			return override, true
		}
	}
	return ToolBackendOverride{}, false
}

func localBuilder(builders map[string]LocalToolBuilder, name string) (LocalToolBuilder, bool) {
	for configuredName, builder := range builders {
		if strings.EqualFold(configuredName, name) {
			return builder, true
		}
	}
	return nil, false
}
