// Package config handles loading and parsing of the application's config.yaml file.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// RewriteRule describes a single find-and-replace rule applied to system prompts.
type RewriteRule struct {
	From   string `yaml:"from"`
	To     string `yaml:"to"`
	Method string `yaml:"method,omitempty"` // "line" or "regex" (default)
	Model  string `yaml:"model,omitempty"`
}

// RateLimiterConfig holds adaptive rate limiter settings.
type RateLimiterConfig struct {
	RetryInterval        float64 `yaml:"retry_interval"`
	ConsecutiveSuccesses int     `yaml:"consecutive_successes"`
}

// AnthropicConfig holds Anthropic-specific configuration.
type AnthropicConfig struct {
	RewriteTools              *bool               `yaml:"rewrite_tools"`
	StripServerTools          bool                `yaml:"strip_server_tools"`
	ImmutableThinkingMessages bool                `yaml:"immutable_thinking_messages"`
	DedupToolCalls            interface{}         `yaml:"dedup_tool_calls"` // false | "input" | "result"
	StripReadToolResultTags   bool                `yaml:"strip_read_tool_result_tags"`
	ContextEditing            *ContextEditingConf `yaml:"context_editing"`
	ToolSearchEnabled         *bool               `yaml:"tool_search_enabled"`
	CacheControlMode          string              `yaml:"cache_control_mode"`
	NonDeferredTools          []string            `yaml:"non_deferred_tools"`
	RewriteSystemReminders    interface{}         `yaml:"rewrite_system_reminders"` // false | []RewriteRule
	CompressToolResults       *bool               `yaml:"compress_tool_results_before_truncate"`
}

// ContextEditingConf holds context-editing options.
type ContextEditingConf struct {
	Mode          string `yaml:"mode"`
	TriggerTokens int    `yaml:"trigger_tokens"`
	KeepTools     int    `yaml:"keep_tools"`
	KeepThinking  int    `yaml:"keep_thinking"`
}

// OpenAIResponsesConfig holds OpenAI Responses API options.
type OpenAIResponsesConfig struct {
	NormalizeCallIds  *bool `yaml:"normalize_call_ids"`
	UpstreamWebSocket bool  `yaml:"upstream_websocket"`
}

// ModelConfig holds model override settings.
type ModelConfig struct {
	ModelOverrides map[string]string `yaml:"model_overrides"`
}

// Config is the top-level config.yaml structure.
type Config struct {
	// Networking
	Proxy string `yaml:"proxy"`

	// Timeouts (seconds)
	StreamIdleTimeout    int `yaml:"stream_idle_timeout"`
	FetchTimeout         int `yaml:"fetch_timeout"`
	StaleRequestMaxAge   int `yaml:"stale_request_max_age"`
	ModelRefreshInterval int `yaml:"model_refresh_interval"`

	// Shutdown
	ShutdownGracefulWait int `yaml:"shutdown_graceful_wait"`
	ShutdownAbortWait    int `yaml:"shutdown_abort_wait"`

	// History
	HistoryLimit      int `yaml:"history_limit"`
	HistoryMinEntries int `yaml:"history_min_entries"`

	// Subsection configs
	RateLimiter     *RateLimiterConfig     `yaml:"rate_limiter"`
	Anthropic       *AnthropicConfig       `yaml:"anthropic"`
	OpenAIResponses *OpenAIResponsesConfig `yaml:"openai-responses"`
	Model           *ModelConfig           `yaml:"model"`

	// System prompt overrides
	SystemPromptOverrides []RewriteRule `yaml:"system_prompt_overrides"`
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		StreamIdleTimeout:    300,
		FetchTimeout:         300,
		StaleRequestMaxAge:   600,
		ModelRefreshInterval: 600,
		ShutdownGracefulWait: 60,
		ShutdownAbortWait:    120,
		HistoryLimit:         200,
		HistoryMinEntries:    50,
	}
}

// DataDir returns the application data directory (~/.local/share/copilot-api).
func DataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".copilot-api"
	}
	return filepath.Join(home, ".local", "share", "copilot-api")
}

// TokenPath returns the path to the stored GitHub token file.
func TokenPath() string {
	return filepath.Join(DataDir(), "github_token")
}

// ConfigYAMLPath returns the path to config.yaml.
func ConfigYAMLPath() string {
	return filepath.Join(DataDir(), "config.yaml")
}

// Load reads and parses config.yaml, falling back to defaults on error.
func Load() (Config, error) {
	cfg := DefaultConfig()
	path := ConfigYAMLPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}

// EnsureDataDir creates the application data directory and token file if they don't exist.
func EnsureDataDir() error {
	dir := DataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tokenPath := TokenPath()
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		f, err := os.OpenFile(tokenPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		f.Close()
	}
	return nil
}
