package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type APIConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	// Format selects the upstream dialect: chat_completions (default),
	// responses, or anthropic_messages. Empty means Chat Completions, which
	// is what every provider spoke before dialects existed.
	Format string `json:"format,omitempty"`
	// NativeVision lets the main model consume image attachments directly.
	// Nil keeps the feature enabled for model profiles that do not declare it,
	// while native_vision:false remains available for text-only providers.
	NativeVision       *bool   `json:"native_vision,omitempty"`
	VisionMaxImages    int     `json:"vision_max_images,omitempty"`
	VisionMaxDimension int     `json:"vision_max_dimension,omitempty"`
	VisionJPEGQuality  int     `json:"vision_jpeg_quality,omitempty"`
	TimeoutSec         int     `json:"timeout_sec"`
	Temperature        float64 `json:"temperature"`
	// ReasoningEffort 空字符串表示不发送该字段；取值 low / medium / high。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MaxTokens       int    `json:"max_tokens"`
	MaxRetries      int    `json:"max_retries"`
	RetryBaseMs     int    `json:"retry_base_ms"`
	// ContextWindow is how many tokens the provider accepts in one request,
	// prompt and completion together. Zero means "not declared", and then the
	// summary threshold is the only budget the harness has to go on.
	ContextWindow int `json:"context_window"`
}

func (c APIConfig) NativeVisionEnabled() bool {
	return c.NativeVision == nil || *c.NativeVision
}

func (c APIConfig) VisionMaxImagesOrDefault() int {
	if c.VisionMaxImages > 0 {
		return c.VisionMaxImages
	}
	// Raised from 4. The limit guards how many images one turn may carry;
	// it used to be tight because every image was inlined as base64 in the
	// context. Images are now placeholders (a path plus the encode
	// parameters) and the payload is produced once, at the wire boundary,
	// so a larger batch costs a bounded amount of request body rather than
	// a growing transcript.
	return 10
}

func (c APIConfig) VisionMaxDimensionOrDefault() int {
	if c.VisionMaxDimension > 0 {
		return c.VisionMaxDimension
	}
	return 1600
}

func (c APIConfig) VisionJPEGQualityOrDefault() int {
	if c.VisionJPEGQuality > 0 && c.VisionJPEGQuality <= 100 {
		return c.VisionJPEGQuality
	}
	return 82
}

// ModelProfile is one selectable entry in the config's "models" map. Only the
// API block is per-model; every other setting is shared by all models.
//
// A model may either carry its own API block (the hand-written form) or
// name the provider that serves it (the derived form). The derived form is
// what the settings panel produces: the base url, key, dialect and output
// limit live on the provider, so they are not duplicated per model.
type ModelProfile struct {
	Display string    `json:"display"`
	API     APIConfig `json:"api"`
	// ProviderID names the provider that serves this model. When it is
	// set, the API block is filled in from that provider at selection time.
	ProviderID string `json:"provider_id,omitempty"`
	// Provider is the older spelling of ProviderID, kept so configurations
	// written before the rename still resolve.
	Provider string `json:"provider,omitempty"`
	// ModelID is the model name sent upstream. It defaults to the map key.
	ModelID string `json:"model_id,omitempty"`
}

// ProviderConfig is one upstream service: a base url, a key, a dialect and
// the models it serves.
type ProviderConfig struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Note             string   `json:"note"`
	Homepage         string   `json:"homepage"`
	BaseURL          string   `json:"base_url"`
	Format           string   `json:"format"`
	OutputLimitField string   `json:"output_limit_field"`
	OutputLimit      int      `json:"output_limit"`
	APIKeyRef        string   `json:"api_key_ref"`
	Models           []string `json:"models"`
}

type GatewayConfig struct {
	Endpoint    string            `json:"endpoint"`
	Timeout     int               `json:"timeout"`
	BearerToken string            `json:"bearerToken"`
	Headers     map[string]string `json:"headers"`
	HostPin     string            `json:"hostPin"` // "host=ip,host2=ip2"：HTTP 工具 DNS pin（本地解析不到公网域名时钉到可达 IP）
}

type ToolApisConfig struct {
	WebFetch       WebFetchToolConfig       `json:"webFetch"`
	WebBudget      WebBudgetToolConfig      `json:"webBudget"`
	ReadFile       ReadFileToolConfig       `json:"readFile"`
	ImageVQA       ImageVQAToolConfig       `json:"imageVQA"`
	ImageSearch    ImageSearchToolConfig    `json:"imageSearch"`
	ImageGenerate  ImageGenerateToolConfig  `json:"imageGenerate"`
	DocumentParser DocumentParserToolConfig `json:"documentParser"`
	Rerank         RerankToolConfig         `json:"rerank"`
	PptTools       PptToolsToolConfig       `json:"pptTools"`
}

// WebFetchToolConfig 保留生产 HTTP backend 的摘要模型兼容字段；本地
// web_fetch 直接抓取正文并支持 file_mode=stateless 的二进制下载，不依赖这些字段。
type WebFetchToolConfig struct {
	SummaryModelName    string `json:"summaryModelName"`
	SummaryModelBaseUrl string `json:"summaryModelBaseUrl"`
}

// WebBudgetToolConfig controls the Codex-style dynamic output budget for
// text web tools. The agent computes one budget from the current prompt size
// and remaining context, then splits it across parallel web_search/web_fetch
// calls in the same step. Individual tools use the allocation to cap snippets
// or fetched text; they do not call an extra summarization model.
type WebBudgetToolConfig struct {
	Enabled             *bool `json:"enabled,omitempty"`
	ContextWindowTokens int   `json:"contextWindowTokens"`
	OutputReserveTokens int   `json:"outputReserveTokens"`
	SafetyReserveTokens int   `json:"safetyReserveTokens"`
	MinTurnTokens       int   `json:"minTurnTokens"`
	MaxTurnTokens       int   `json:"maxTurnTokens"`
	TurnSharePercent    int   `json:"turnSharePercent"`
	SearchMaxResults    int   `json:"searchMaxResults"`
	SearchSnippetChars  int   `json:"searchSnippetChars"`
	FetchMaxChars       int   `json:"fetchMaxChars"`
	FetchSummaryChars   int   `json:"fetchSummaryChars"`
}

func (c WebBudgetToolConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c WebBudgetToolConfig) withDefaults() WebBudgetToolConfig {
	if c.ContextWindowTokens <= 0 {
		c.ContextWindowTokens = 128000
	}
	if c.OutputReserveTokens <= 0 {
		c.OutputReserveTokens = 12000
	}
	if c.SafetyReserveTokens <= 0 {
		c.SafetyReserveTokens = 8000
	}
	if c.MinTurnTokens <= 0 {
		c.MinTurnTokens = 2000
	}
	if c.MaxTurnTokens <= 0 {
		c.MaxTurnTokens = 32000
	}
	if c.TurnSharePercent <= 0 || c.TurnSharePercent > 100 {
		c.TurnSharePercent = 50
	}
	if c.SearchMaxResults <= 0 {
		c.SearchMaxResults = 8
	}
	if c.SearchSnippetChars <= 0 {
		c.SearchSnippetChars = 600
	}
	if c.FetchMaxChars <= 0 {
		c.FetchMaxChars = 24000
	}
	if c.FetchSummaryChars == 0 {
		c.FetchSummaryChars = 2400
	}
	return c
}

// ImageSearchToolConfig 控制本地 image_search 的多源搜索策略：
// SourcePriority 按顺序尝试，凑够 top_k 即停止，前面的源不足时自动回退下一个源；
// TimeoutSec 是单个搜索源的超时；MaxImageBytes 是单张图片下载体积上限。
type ImageSearchToolConfig struct {
	SourcePriority []string `json:"sourcePriority"`
	TimeoutSec     int      `json:"timeoutSec"`
	MaxImageBytes  int64    `json:"maxImageBytes"`
}

// ImageGenerateToolConfig 配置本地 image_generate（MiniMax 文生图）。
// prompt 由 Agent 生成后传入，这里只放模型与传输参数。
type ImageGenerateToolConfig struct {
	ModelName     string `json:"modelName"`
	BaseURL       string `json:"baseUrl"`
	APIKey        string `json:"apiKey"`
	AspectRatio   string `json:"aspectRatio"`
	TimeoutSec    int    `json:"timeoutSec"`
	MaxRetries    int    `json:"maxRetries"`
	RetryBaseMs   int    `json:"retryBaseMs"`
	MaxImages     int    `json:"maxImages"`
	MaxImageBytes int64  `json:"maxImageBytes"`
}

type ReadFileToolConfig struct {
	SegmentReadMaxTokens int   `json:"segmentReadMaxTokens"`
	SegmentReadMinTokens int   `json:"segmentReadMinTokens"`
	MaxReadFileSizeBytes int64 `json:"maxReadFileSizeBytes"`
}

type ImageVQAToolConfig struct {
	ModelName     string  `json:"modelName"`
	BaseURL       string  `json:"baseUrl"`
	APIKey        string  `json:"apiKey"`
	Mode          string  `json:"mode"`
	TimeoutSec    int     `json:"timeoutSec"`
	MaxTokens     int     `json:"maxTokens"`
	Temperature   float64 `json:"temperature"`
	MaxRetries    int     `json:"maxRetries"`
	RetryBaseMs   int     `json:"retryBaseMs"`
	MaxImageBytes int64   `json:"maxImageBytes"`
}

type DocumentParserToolConfig struct {
	ServiceUrl             string `json:"serviceUrl"`
	ParserEndpoint         string `json:"parserEndpoint"`
	UploadDir              string `json:"uploadDir"`
	OutputMaxTokens        int    `json:"outputMaxTokens"`
	OutputTruncateStrategy string `json:"outputTruncateStrategy"`
	TableMaxDisplayRows    int    `json:"tableMaxDisplayRows"`
}

type RerankToolConfig struct {
	ServiceUrl         string  `json:"serviceUrl"`
	ChunkMinTokens     int     `json:"chunkMinTokens"`
	ChunkOverlapTokens int     `json:"chunkOverlapTokens"`
	TopK               int     `json:"topK"`
	Threshold          float64 `json:"threshold"`
}

// 「思考窗口」的包级默认值。这些值与配置化之前的写死常量完全一致，
// 配置文件里缺字段时必须落回这里，否则默认行为会悄悄改变。
const (
	defaultReasoningKeepRecent = 5
	defaultMaxThinkingChars    = 3000
)

// ReasoningConfig 是「思考窗口」的可调参数。原先这两个值是包级常量
// （reasoningKeepRecent / maxThinkingChars），写死后用户无法在设置面板调节；
// 这里保持默认值不变（5 条 / 3000 字符），只把来源换成配置文件。
type ReasoningConfig struct {
	// KeepRecent 尾部保留 reasoning 与 <thinking> 的消息条数。
	KeepRecent int `json:"keep_recent"`
	// MaxThinkingChars 单条显式 <thinking>/<think> 块的上限字符数。
	MaxThinkingChars int `json:"max_thinking_chars"`
	// Enabled 关闭后不清理历史 reasoning（保留原始 reasoning_content）。
	// 注意：思考清理本身没有阈值，它按「距末尾多少条消息」执行，
	// 真正的旋钮是 KeepRecent。
	Enabled *bool `json:"enabled"`
}

func (c ReasoningConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c ReasoningConfig) withDefaults() ReasoningConfig {
	if c.KeepRecent <= 0 {
		c.KeepRecent = defaultReasoningKeepRecent
	}
	if c.MaxThinkingChars <= 0 {
		c.MaxThinkingChars = defaultMaxThinkingChars
	}
	return c
}

func (c ReasoningConfig) KeepRecentOrDefault() int {
	if c.KeepRecent > 0 {
		return c.KeepRecent
	}
	return defaultReasoningKeepRecent
}

func (c ReasoningConfig) MaxThinkingCharsOrDefault() int {
	if c.MaxThinkingChars > 0 {
		return c.MaxThinkingChars
	}
	return defaultMaxThinkingChars
}

// SegmentedMemoryConfig controls the append-only episodic memory store. It is
// intentionally separate from the legacy end-of-session memory summarizer:
// this path records every user request, streamed reply, and tool operation as
// its own searchable segment while the agent loop is still running.
type SegmentedMemoryConfig struct {
	Enabled              bool   `json:"enabled"`
	KeyExtraction        string `json:"key_extraction"`
	MaxSegmentChars      int    `json:"max_segment_chars"`
	MaxSegmentSteps      int    `json:"max_segment_steps"`
	MaxRootIndexEntries  int    `json:"max_root_index_entries"`
	PromptKeyLimit       int    `json:"prompt_key_limit"`
	KeyExtractionTimeout int    `json:"key_extraction_timeout_sec"`
	KeyWaitTimeout       int    `json:"key_wait_timeout_sec"`
	MaxConcurrentKeys    int    `json:"max_concurrent_keys"`
}

// PptToolsToolConfig 是新 PPT skill（ppt-maker 分发到 no-template/template/creative
// 三个模式）依赖的后端工具网关配置。creative_page_render / html_page_generate /
// html_page_review / html_to_png / image_filter 均经 POST {baseUrl}{apiPath}
//（/api/agent/tool_call）调用。兼容服务可通过自定义 Host 或 DNS 配置接入。
type PptToolsToolConfig struct {
	BaseUrl string `json:"baseUrl"`
	ApiPath string `json:"apiPath"`
	HostPin string `json:"hostPin"`
}

type SkillReg struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Location    string   `json:"location"`
	Tags        []string `json:"tags,omitempty"`
	Triggers    []string `json:"triggers,omitempty"`
	Always      bool     `json:"always,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`

	// Category is the first level of the two-level tree the settings UI shows.
	// Empty means "未分类"; the UI keeps those in an implicit bucket rather than
	// dropping them, so a newly added skill is never invisible.
	Category string `json:"category,omitempty"`

	// Core marks a skill the user reaches for constantly. *bool rather than bool
	// so "unset" stays distinguishable from "explicitly not core" — the PATCH
	// handler only writes the fields the client actually sent.
	Core *bool `json:"core,omitempty"`
}

// SkillCategory is a first-level bucket in the skill tree. Categories come from
// the skills referencing them, plus any explicitly configured ones, so a bucket
// can stay alive even after its last skill is moved away.
type SkillCategory struct {
	Name    string `json:"name"`
	Skills  int    `json:"skills"`
	Core    int    `json:"core"`
	Builtin bool   `json:"builtin,omitempty"`
}

// SkillRoutingConfig controls progressive skill disclosure. A cheap local
// recall narrows the registry first; local semantic reranking only reorders
// that small candidate set.
type SkillRoutingConfig struct {
	Enabled     bool     `json:"enabled"`
	Always      []string `json:"always,omitempty"`
	RecallLimit int      `json:"recall_limit"`
	InjectLimit int      `json:"inject_limit"`
	// FullBodyLimit caps how many injected skills carry their full SKILL.md
	// body instead of just the description.
	FullBodyLimit int `json:"full_body_limit"`
	CacheTTLSec   int `json:"cache_ttl_sec"`
	MaxBodyRunes  int `json:"max_body_runes"`
}

type ToolEntry struct {
	Enabled bool `json:"enabled"`
}

type ToolRuntimeConfig struct {
	Executables LocalExecutableConfig          `json:"executables"`
	Tools       map[string]ToolBackendOverride `json:"tools"`
	RetryCount  int                            `json:"retry_count"`
	// Timeouts maps a tool name to its per-tool timeout in seconds; tools not
	// listed use the global API timeout.
	Timeouts map[string]int `json:"timeouts"`
	// Approval maps a tool name to "ask" when that tool must be user-approved
	// before it runs. ApprovalMode is a global override:
	//   "ask"  - approved tools require approval (default when Approval is set)
	//   "allow"- never block (unattended runs)
	//   "deny" - always block approved tools
	Approval     map[string]string `json:"approval"`
	ApprovalMode string            `json:"approval_mode"`
}

// BashPolicyConfig is the on-disk representation of the bash policy. An
// empty struct means "use the hard-coded defaults from
// local.DefaultBashPolicy()"; users opt out by setting `enabled: false`.
type BashPolicyConfig struct {
	Enabled        bool     `json:"enabled"`
	AllowCommands  []string `json:"allow_commands"`
	DenyCommands   []string `json:"deny_commands"`
	DenyPaths      []string `json:"deny_paths"`
	StrictDenyOnly bool     `json:"strict_deny_only"`
}

type LocalExecutableConfig struct {
	Python  string `json:"python"`
	Shell   string `json:"shell"`
	Node    string `json:"node"`
	Browser string `json:"browser"`
}

type ToolBackendOverride struct {
	Backend ToolBackend `json:"backend"`
}

// MCPServerConfig describes one Model Context Protocol server. Servers are
// discovered at Agent startup and their tools are registered dynamically.
type MCPServerConfig struct {
	Enabled           *bool             `json:"enabled,omitempty"`
	Disabled          bool              `json:"disabled,omitempty"`
	Required          bool              `json:"required,omitempty"`
	Transport         string            `json:"transport,omitempty"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Cwd               string            `json:"cwd,omitempty"`
	URL               string            `json:"url,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	ProtocolVersion   string            `json:"protocol_version,omitempty"`
	StartupTimeoutSec int               `json:"startup_timeout_sec,omitempty"`
	ToolPrefix        string            `json:"tool_prefix,omitempty"`
	AllowedTools      []string          `json:"allowed_tools,omitempty"`
	DeniedTools       []string          `json:"denied_tools,omitempty"`
}

func (c MCPServerConfig) IsEnabled() bool {
	if c.Disabled {
		return false
	}
	return c.Enabled == nil || *c.Enabled
}

// UISettings is the subset of frontend settings the native agent needs for
// unattended runs. Other UI-only settings remain owned by server.cjs.
type UISettings struct {
	AutoFallback  *bool  `json:"auto_fallback,omitempty"`
	FallbackModel string `json:"fallback_model,omitempty"`
}

type Config struct {
	DefaultModel    string                  `json:"default_model"`
	Models          map[string]ModelProfile `json:"models"`
	// Providers are the upstream services the models are derived from.
	// A model that names a provider takes its base url, key, dialect and
	// output limit from that provider.
	Providers     map[string]ProviderConfig `json:"providers,omitempty"`
	API           APIConfig                 `json:"api"`
	FallbackModel string                    `json:"-"`
	Settings        UISettings              `json:"settings,omitempty"`
	WorkspaceDir    string                  `json:"workspace_dir"`
	MemoryDir       string                  `json:"memory_dir"`
	MemorySummarize *bool                   `json:"memory_summarize,omitempty"`
	GenerateTitle   *bool                   `json:"generate_title,omitempty"`
	UseMock         bool                    `json:"use_mock"`
	Prompts         struct {
		SystemPath        string `json:"system_path"`
		UserPath          string `json:"user_path"`
		ModulesDir        string `json:"modules_dir"`
		SubtaskSystemPath string `json:"subtask_system_path"`
		SubtaskUserPath   string `json:"subtask_user_path"`
	} `json:"prompts"`
	SystemPartsDir         string                     `json:"system_parts_dir"`
	SkillsDir              string                     `json:"skills_dir"`
	Skills                 []SkillReg                 `json:"skills"`
	Tools                  ToolApisConfig             `json:"tools"`
	HistoryDir             string                     `json:"history_dir"`
	MockFile               string                     `json:"mock_file"`
	Gateway                GatewayConfig              `json:"unifiedToolService"`
	HTTPTools              map[string]ToolEntry       `json:"httpTools"`
	MCPServers             map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	ToolRuntime            *ToolRuntimeConfig         `json:"tool_runtime,omitempty"`
	BashPolicy             BashPolicyConfig           `json:"bash_policy,omitempty"`
	ToolsSchemas           string                     `json:"tools_schemas"`
	MaxSteps               int                        `json:"max_steps"`
	SubtaskMaxSteps        int                        `json:"subtask_max_steps"`
	SubtaskForbiddenTools  []string                   `json:"subtask_forbidden_tools"`
	SummaryThresholdTokens int                        `json:"summary_threshold_tokens"`
	SummaryRetainTokens    int                        `json:"summary_retain_tokens"`
	ToolScoreCleanup       ToolScoreCleanupConfig     `json:"tool_score_cleanup,omitempty"`
	Reasoning              ReasoningConfig            `json:"reasoning,omitempty"`
	SkillRouting           SkillRoutingConfig         `json:"skill_routing,omitempty"`
	SegmentedMemory        SegmentedMemoryConfig      `json:"segmented_memory,omitempty"`
	MaxNetworkCalls        int                        `json:"max_network_calls"`
	ConfigPath             string                     `json:"-"`
	RepoRoot               string                     `json:"-"`
	SelectedModelID        string                     `json:"-"`
	mockLLMState           *mockLLMState              `json:"-"`
}

func LoadConfig(repoRoot, configPath string) (*Config, error) {
	absPath := configPath
	if !filepath.IsAbs(configPath) {
		absPath = filepath.Join(repoRoot, configPath)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// Strip BOM
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		raw = raw[3:]
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.ConfigPath = absPath
	cfg.RepoRoot = repoRoot
	if cfg.Settings.AutoFallback == nil || *cfg.Settings.AutoFallback {
		if fallback := strings.TrimSpace(cfg.Settings.FallbackModel); fallback != "" {
			cfg.FallbackModel = fallback
		}
	}
	resolveAPIKeyFromFile(&cfg)
	if err := ensureToolRuntimeDefaults(&cfg); err != nil {
		return nil, err
	}

	// Default values
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 60
	}
	if cfg.SubtaskMaxSteps <= 0 {
		cfg.SubtaskMaxSteps = 20
	}
	if len(cfg.SubtaskForbiddenTools) == 0 {
		cfg.SubtaskForbiddenTools = []string{
			"create_subtask", "ask_user",
		}
	}
	if cfg.SummaryThresholdTokens <= 0 {
		cfg.SummaryThresholdTokens = 60000
	}
	if cfg.SummaryRetainTokens <= 0 {
		cfg.SummaryRetainTokens = 12000
	}
	cfg.ToolScoreCleanup.ApplyDefaults(cfg.SummaryThresholdTokens)
	cfg.Reasoning = cfg.Reasoning.withDefaults()
	// 把思考窗口参数推给运行时读它们的包级变量（apiclient.go / tagrouter.go）。
	setReasoningKeepRecent(cfg.Reasoning.KeepRecentOrDefault())
	setMaxThinkingChars(cfg.Reasoning.MaxThinkingCharsOrDefault())
	setReasoningEnabled(cfg.Reasoning.IsEnabled())
	cfg.SkillRouting.ApplyDefaults()
	applySegmentedMemoryDefaults(&cfg)
	if cfg.MaxNetworkCalls < 0 {
		cfg.MaxNetworkCalls = 20
	} // 0 = unlimited; negative -> default 20
	if cfg.API.TimeoutSec <= 0 {
		cfg.API.TimeoutSec = 120
	}
	if cfg.API.MaxTokens <= 0 {
		cfg.API.MaxTokens = 16384
	}
	if cfg.API.MaxRetries <= 0 {
		cfg.API.MaxRetries = 3
	}
	if cfg.API.RetryBaseMs <= 0 {
		cfg.API.RetryBaseMs = 1000
	}
	applyImageVQADefaults(&cfg)
	resolveImageVQAAPIKeyFromFile(&cfg)
	applyImageSearchDefaults(&cfg)
	applyImageGenerateDefaults(&cfg)
	resolveImageGenerateAPIKeyFromFile(&cfg)
	if cfg.HistoryDir == "" {
		cfg.HistoryDir = "runs"
	}
	if cfg.MemoryDir == "" {
		cfg.MemoryDir = "memory"
	}
	return &cfg, nil
}

func isValidToolBackend(backend ToolBackend) bool {
	switch backend {
	case BackendLocal, BackendHTTP:
		return true
	default:
		return false
	}
}

// ensureToolRuntimeDefaults 为显式的工具路由应用默认值并校验 backend 名称。
// 每一个启用的 schema 都必须在 tools 中声明其 backend；不再回退到旧的外部进程工具。
func ensureToolRuntimeDefaults(cfg *Config) error {
	if cfg.ToolRuntime == nil {
		cfg.ToolRuntime = &ToolRuntimeConfig{
			Tools:      make(map[string]ToolBackendOverride),
			RetryCount: 1,
		}
	} else {
		if cfg.ToolRuntime.Tools == nil {
			cfg.ToolRuntime.Tools = make(map[string]ToolBackendOverride)
		}
		if cfg.ToolRuntime.RetryCount <= 0 {
			cfg.ToolRuntime.RetryCount = 1
		}
	}
	if cfg.ToolRuntime.Timeouts == nil {
		cfg.ToolRuntime.Timeouts = make(map[string]int)
	}
	if cfg.ToolRuntime.Approval == nil {
		cfg.ToolRuntime.Approval = make(map[string]string)
	}
	if cfg.ToolRuntime.ApprovalMode == "" {
		if len(cfg.ToolRuntime.Approval) > 0 {
			cfg.ToolRuntime.ApprovalMode = "ask"
		} else {
			cfg.ToolRuntime.ApprovalMode = "allow"
		}
	}

	for name, override := range cfg.ToolRuntime.Tools {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("tool_runtime.tools: tool name is empty")
		}
		if !isValidToolBackend(override.Backend) {
			return fmt.Errorf("tool_runtime.tools.%s.backend: unknown backend %q", name, override.Backend)
		}
	}
	return nil
}

// ToolTimeout resolves the per-tool timeout for name, falling back to the
// global API timeout when the tool has no explicit entry.
func (c *Config) ToolTimeout(name string, fallback time.Duration) time.Duration {
	if c != nil && c.ToolRuntime != nil {
		if secs, ok := c.ToolRuntime.Timeouts[name]; ok && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return fallback
}

// ToolNeedsApproval reports whether tool name must be user-approved before
// execution under the configured approval policy.
func (c *Config) ToolNeedsApproval(name string) bool {
	if c == nil || c.ToolRuntime == nil {
		return false
	}
	policy, listed := c.ToolRuntime.Approval[name]
	mode := c.ToolRuntime.ApprovalMode
	if mode == "allow" {
		return false
	}
	if mode == "deny" {
		return listed
	}
	// default "ask"
	return listed && strings.EqualFold(strings.TrimSpace(policy), "ask")
}

func (c *Config) ResolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.RepoRoot, p)
}

func (c *Config) SystemPath() string {
	if c.Prompts.SystemPath != "" {
		return c.ResolvePath(c.Prompts.SystemPath)
	}
	return filepath.Join(c.RepoRoot, "config", "system", "zh.md")
}

func (c *Config) UserPath() string {
	if c.Prompts.UserPath != "" {
		return c.ResolvePath(c.Prompts.UserPath)
	}
	return filepath.Join(c.RepoRoot, "config", "user.txt")
}

// ModulesPath returns the root for on-demand prompt modules such as summary,
// generate_title, memory summarization and subtask contracts.
func (c *Config) ModulesPath() string {
	if strings.TrimSpace(c.Prompts.ModulesDir) != "" {
		return c.ResolvePath(c.Prompts.ModulesDir)
	}
	return filepath.Join(c.RepoRoot, "config", "modules")
}

// SubtaskSystemPath is the worker-only contract used by delegated subtask runs.
func (c *Config) SubtaskSystemPath() string {
	if c.Prompts.SubtaskSystemPath != "" {
		return c.ResolvePath(c.Prompts.SubtaskSystemPath)
	}
	return filepath.Join(c.ModulesPath(), "subtask", "system", "zh.md")
}

// SubtaskUserPath carries only the delegated work package and its skill registry.
func (c *Config) SubtaskUserPath() string {
	if c.Prompts.SubtaskUserPath != "" {
		return c.ResolvePath(c.Prompts.SubtaskUserPath)
	}
	return filepath.Join(c.ModulesPath(), "subtask", "user", "zh.md")
}

func (c *Config) SchemasPath() string {
	if c.ToolsSchemas != "" {
		return c.ResolvePath(c.ToolsSchemas)
	}
	return filepath.Join(c.RepoRoot, "config", "tools", "schemas.json")
}

// SelectModel applies the named model profile from the config's "models" map.
// Legacy single-model configs (no models map) are left untouched so older
// config files keep working. An empty modelID selects default_model, then the
// first model in sorted order.
func (c *Config) SelectModel(modelID string) error {
	if c == nil || len(c.Models) == 0 {
		return nil
	}
	id := strings.TrimSpace(modelID)
	if id == "" {
		id = strings.TrimSpace(c.DefaultModel)
	}
	if id == "" {
		names := make([]string, 0, len(c.Models))
		for name := range c.Models {
			names = append(names, name)
		}
		sort.Strings(names)
		id = names[0]
	}
	profile, ok := c.Models[id]
	if !ok {
		return fmt.Errorf("unknown model %q (available: %s)", id, strings.Join(sortedModelIDs(c.Models), ", "))
	}
	// A derived model carries no api block of its own; the provider it
	// names supplies the base url, key, dialect and output limit.
	resolved := profile
	// Only provider_id triggers the provider lookup. The older "provider"
	// field is just an ownership tag ("this model belongs to that
	// provider") and carries no connection details -- treating it as a
	// reference would replace the model id with the config key and break
	// models whose upstream name differs from their config key.
	providerID := strings.TrimSpace(profile.ProviderID)
	if providerID != "" {
		provider, found := c.Providers[providerID]
		if !found {
			return fmt.Errorf("model %q names provider %q, which is not configured", id, providerID)
		}
		api := resolved.API
		// The provider wins for the connection details; the model entry
		// may still override the model id and the shared tuning knobs.
		if provider.BaseURL != "" {
			api.BaseURL = provider.BaseURL
		}
		if provider.Format != "" {
			api.Format = provider.Format
		}
		if provider.OutputLimit > 0 {
			api.MaxTokens = provider.OutputLimit
		}
		// The key reference points into local_secrets.json.
		if provider.APIKeyRef != "" {
			api.APIKey = provider.APIKeyRef
		}
		if modelID := strings.TrimSpace(profile.ModelID); modelID != "" {
			api.Model = modelID
		} else {
			api.Model = id
		}
		resolved.API = api
	}
	if strings.TrimSpace(resolved.API.Model) == "" {
		return fmt.Errorf("model %q has no api.model configured", id)
	}
	// 切模型时整体换成该模型的 api 段，但顶层声明的上下文窗口要保留。
	// 之前这里是 c.API = profile.API 一把覆盖：config.json 顶层 api.context_window
	// 被模型的 api 段整个盖掉，而模型段通常不写窗口，于是窗口恒为 0，
	// effectiveContextBudget 只能退化成 summary_threshold_tokens（50 万），
	// 压缩阈值形同虚设——实测一个 668 轮的真实会话里只触发了 1 次回落。
	window := c.API.ContextWindow
	c.API = resolved.API
	if c.API.ContextWindow <= 0 && window > 0 {
		c.API.ContextWindow = window
	}
	c.SelectedModelID = id
	// The per-model api_key may still be a READ_FROM_* placeholder.
	resolveAPIKeyFromFile(c)
	return nil
}

func sortedModelIDs(models map[string]ModelProfile) []string {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// resolveAPIKeyFromFile fills READ_FROM_* API key placeholders from key files
// at the repo root. MINIMAX_key.txt / DEEPSEEK_key.txt: first line key,
// optional second line model.
func resolveAPIKeyFromFile(cfg *Config) {
	key := cfg.API.APIKey
	if key == "" || !strings.HasPrefix(key, "READ_FROM_") {
		return
	}
	// Providers keep their key in config/local_secrets.json, referenced as
	// READ_FROM_LOCAL_SECRETS:<id>. That file is not a repo-root text file,
	// so it is read separately from the READ_FROM_*_KEY_TXT placeholders.
	if secret, ok := readLocalSecret(cfg.RepoRoot, key); ok {
		cfg.API.APIKey = secret
		return
	}
	if text, ok := readAPIKeyFile(cfg.RepoRoot, key); ok {
		applyAPIKeyFile(cfg, text)
	}
}

// readLocalSecret resolves a READ_FROM_LOCAL_SECRETS:<id> placeholder
// against config/local_secrets.json.
func readLocalSecret(repoRoot, placeholder string) (string, bool) {
	const prefix = "READ_FROM_LOCAL_SECRETS:"
	if !strings.HasPrefix(placeholder, prefix) {
		return "", false
	}
	id := strings.TrimSpace(strings.TrimPrefix(placeholder, prefix))
	if id == "" {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, "config", "local_secrets.json"))
	if err != nil {
		return "", false
	}
	var secrets map[string]any
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return "", false
	}
	value, ok := secrets[id]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

func applyImageVQADefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	imageVQA := &cfg.Tools.ImageVQA
	if strings.TrimSpace(imageVQA.ModelName) == "" {
		imageVQA.ModelName = "deepseek-flash"
	}
	if strings.TrimSpace(imageVQA.BaseURL) == "" {
		imageVQA.BaseURL = "https://api.deepseek.com"
	}
	if strings.TrimSpace(imageVQA.APIKey) == "" {
		imageVQA.APIKey = "READ_FROM_DEEPSEEK_KEY_TXT"
	}
	// auto: answer locally from OCR/YOLO when the question is about visible text
	// or a locatable control, otherwise fall back to the multimodal API.
	if strings.TrimSpace(imageVQA.Mode) == "" {
		imageVQA.Mode = "auto"
	}
	if imageVQA.TimeoutSec <= 0 {
		imageVQA.TimeoutSec = 120
	}
	if imageVQA.MaxTokens <= 0 {
		imageVQA.MaxTokens = 2048
	}
	if imageVQA.Temperature == 0 {
		imageVQA.Temperature = 0.1
	}
	if imageVQA.MaxRetries <= 0 {
		imageVQA.MaxRetries = 2
	}
	if imageVQA.RetryBaseMs <= 0 {
		imageVQA.RetryBaseMs = 1000
	}
	if imageVQA.MaxImageBytes <= 0 {
		imageVQA.MaxImageBytes = 16 << 20
	}
}

func applyImageSearchDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	imageSearch := &cfg.Tools.ImageSearch
	if len(imageSearch.SourcePriority) == 0 {
		imageSearch.SourcePriority = []string{"bing", "baidu", "wikimedia"}
	}
	if imageSearch.TimeoutSec <= 0 {
		imageSearch.TimeoutSec = 45
	}
	if imageSearch.MaxImageBytes <= 0 {
		imageSearch.MaxImageBytes = 20 << 20
	}
}

func applyImageGenerateDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	imageGenerate := &cfg.Tools.ImageGenerate
	if strings.TrimSpace(imageGenerate.ModelName) == "" {
		imageGenerate.ModelName = "image-01"
	}
	if strings.TrimSpace(imageGenerate.BaseURL) == "" {
		imageGenerate.BaseURL = "https://api.minimaxi.com/v1"
	}
	if strings.TrimSpace(imageGenerate.APIKey) == "" {
		imageGenerate.APIKey = "READ_FROM_MINIMAX_KEY_TXT"
	}
	if strings.TrimSpace(imageGenerate.AspectRatio) == "" {
		imageGenerate.AspectRatio = "16:9"
	}
	if imageGenerate.TimeoutSec <= 0 {
		imageGenerate.TimeoutSec = 180
	}
	if imageGenerate.MaxRetries <= 0 {
		imageGenerate.MaxRetries = 2
	}
	if imageGenerate.RetryBaseMs <= 0 {
		imageGenerate.RetryBaseMs = 1500
	}
	if imageGenerate.MaxImages <= 0 {
		imageGenerate.MaxImages = 4
	}
	if imageGenerate.MaxImageBytes <= 0 {
		imageGenerate.MaxImageBytes = 24 << 20
	}
}

// resolveImageGenerateAPIKeyFromFile reuses the repo-root key file lookup so
// image_generate and the MiniMax chat model share MINIMAX_key.txt.
func resolveImageGenerateAPIKeyFromFile(cfg *Config) {
	if cfg == nil {
		return
	}
	key := strings.TrimSpace(cfg.Tools.ImageGenerate.APIKey)
	if key == "" || !strings.HasPrefix(key, "READ_FROM_") {
		return
	}
	if text, ok := readAPIKeyFile(cfg.RepoRoot, key); ok {
		if secret, _ := parseAPIKeyFile(text); secret != "" {
			cfg.Tools.ImageGenerate.APIKey = secret
		}
	}
}

func resolveImageVQAAPIKeyFromFile(cfg *Config) {
	if cfg == nil {
		return
	}
	key := strings.TrimSpace(cfg.Tools.ImageVQA.APIKey)
	if key == "" || !strings.HasPrefix(key, "READ_FROM_") {
		return
	}
	if text, ok := readAPIKeyFile(cfg.RepoRoot, key); ok {
		if secret, _ := parseAPIKeyFile(text); secret != "" {
			cfg.Tools.ImageVQA.APIKey = secret
		}
	}
}

func readAPIKeyFile(repoRoot, placeholder string) (string, bool) {
	for _, fileName := range apiKeyFileCandidates(placeholder) {
		data, err := os.ReadFile(filepath.Join(repoRoot, fileName))
		if err != nil {
			continue
		}
		return string(data), true
	}
	return "", false
}

// apiKeyFileCandidates lists repo-root key files a READ_FROM_* placeholder may
// resolve to. READ_FROM_MINIMAX_KEY_TXT accepts both MINIMAX_KEY_TXT.txt and
// the shorter MINIMAX_key.txt spelling.
func apiKeyFileCandidates(key string) []string {
	if strings.Contains(key, "DEEPSEEK") {
		return []string{"DEEPSEEK_key.txt", "DEEPSEEK_KEY_TXT.txt"}
	}
	suffix := strings.TrimPrefix(key, "READ_FROM_")
	candidates := []string{suffix + ".txt"}
	if base, ok := strings.CutSuffix(suffix, "_KEY_TXT"); ok {
		candidates = append(candidates, base+"_key.txt", base+".txt")
	}
	return candidates
}

// applyAPIKeyFile reads the first non-comment line as the secret and the second
// as an optional model override.
func applyAPIKeyFile(cfg *Config, text string) {
	secret, model := parseAPIKeyFile(text)
	if secret != "" {
		cfg.API.APIKey = secret
	}
	if model != "" && (cfg.API.Model == "" || strings.HasPrefix(cfg.API.Model, "READ_FROM")) {
		cfg.API.Model = model
	}
}

func parseAPIKeyFile(text string) (string, string) {
	if len(text) >= 3 && text[0] == 0xEF && text[1] == 0xBB && text[2] == 0xBF {
		text = text[3:]
	}
	lines := strings.Split(text, "\n")
	var secret string
	var model string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if secret == "" {
			if idx := strings.Index(line, ":"); idx > 0 && !strings.Contains(line, "://") {
				secret = strings.TrimSpace(line[idx+1:])
			} else {
				secret = line
			}
		} else {
			model = line
			break
		}
	}
	return secret, model
}
