package local

import "agentloop/agent/internal/dtypes"

type Tool = dtypes.Tool
type ToolDef = dtypes.ToolDef
type ToolInvocation = dtypes.ToolInvocation
type ToolResult = dtypes.ToolResult
type ToolAttachment = dtypes.ToolAttachment

type LocalExecutableConfig struct {
	Python  string
	Shell   string
	Node    string
	Browser string
}

type ToolRuntimeConfig struct {
	Executables LocalExecutableConfig
}

// PptToolsConfig contains the endpoint settings that PPT skill scripts read
// from their process environment.
type PptToolsConfig struct {
	BaseURL string
	APIPath string
	HostPin string
}

type DocumentParserConfig struct {
	OutputMaxTokens        int
	OutputTruncateStrategy string
}

// ImageGenerateConfig points image_generate at a text-to-image provider. The
// prompt itself always comes from the Agent; only transport/model settings live
// here.
type ImageGenerateConfig struct {
	ModelName     string
	BaseURL       string
	APIKey        string
	AspectRatio   string
	TimeoutSec    int
	MaxRetries    int
	RetryBaseMs   int
	MaxImages     int
	MaxImageBytes int64
}

type ImageVQAConfig struct {
	ModelName string
	BaseURL   string
	APIKey    string
	// Mode selects the answering pipeline: "auto" tries local OCR/YOLO first and
	// only calls the multimodal API when the question needs real visual
	// understanding, "local" never calls the API, "remote" always does.
	Mode          string
	TimeoutSec    int
	MaxTokens     int
	Temperature   float64
	MaxRetries    int
	RetryBaseMs   int
	MaxImageBytes int64
}

// Config contains only the process-local settings required by environment
// tools. Prompt rendering remains owned by the Agent package.
type Config struct {
	RepoRoot           string
	ConfigPath         string
	SelectedModelID    string
	SkillsRoot         string
	UseMock            bool
	BashPolicy         BashPolicy
	ToolRuntime        *ToolRuntimeConfig
	PptTools           PptToolsConfig
	DocumentParser     DocumentParserConfig
	ImageVQA           ImageVQAConfig
	ImageGenerate      ImageGenerateConfig
	BuildSubtaskPrompt func(task string) (string, error)
}
