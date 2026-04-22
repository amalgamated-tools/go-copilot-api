// Package state holds the global mutable server state, protected by a RWMutex.
package state

import (
	"sync"
)

// AccountType represents the Copilot account tier.
type AccountType string

const (
	AccountIndividual AccountType = "individual"
	AccountBusiness   AccountType = "business"
	AccountEnterprise AccountType = "enterprise"
)

// TokenInfo holds GitHub token metadata.
type TokenInfo struct {
	Token       string
	Source      string // "cli" | "env" | "file" | "device-auth"
	ExpiresAt   *int64 // Unix timestamp, nil if unknown
	Refreshable bool
}

// CopilotTokenInfo holds Copilot token metadata.
type CopilotTokenInfo struct {
	Token     string
	ExpiresAt int64 // Unix timestamp
	RefreshIn int   // seconds
}

// ModelLimits holds per-model token limits.
type ModelLimits struct {
	MaxContextWindowTokens      *int
	MaxOutputTokens             *int
	MaxPromptTokens             *int
	MaxNonStreamingOutputTokens *int
}

// ModelSupports holds boolean capability flags.
type ModelSupports map[string]interface{}

// ModelCapabilities describes what a model can do.
type ModelCapabilities struct {
	Family    string
	Limits    *ModelLimits
	Supports  ModelSupports
	Tokenizer string
	Type      string
}

// Model is one entry from the Copilot /models response.
type Model struct {
	ID                  string
	Name                string
	Vendor              string
	Version             string
	Object              string
	Preview             bool
	IsChatDefault       bool
	IsChatFallback      bool
	ModelPickerEnabled  bool
	ModelPickerCategory string
	Capabilities        *ModelCapabilities
	SupportedEndpoints  []string
	RequestHeaders      map[string]string
}

// ModelsResponse is the full /models API response.
type ModelsResponse struct {
	Object string
	Data   []Model
}

// State is the shared runtime state for the server.
type State struct {
	mu sync.RWMutex

	// Auth tokens
	GitHubToken      string
	CopilotToken     string
	TokenInfo        *TokenInfo
	CopilotTokenInfo *CopilotTokenInfo

	// Account settings
	AccountType AccountType

	// VSCode version (fetched on startup)
	VSCodeVersion string

	// Model catalog (fetched from Copilot API)
	Models     *ModelsResponse
	ModelIndex map[string]*Model // id → Model
	ModelIDs   map[string]bool   // set of available model IDs

	// Runtime flags
	Verbose         bool
	ShowGitHubToken bool
	AutoTruncate    bool
	RateLimit       bool

	// Config-managed values
	ModelOverrides       map[string]string
	StreamIdleTimeout    int // seconds
	FetchTimeout         int // seconds
	ShutdownGracefulWait int
	ShutdownAbortWait    int
	HistoryLimit         int
	HistoryMinEntries    int
	StaleRequestMaxAge   int
	ModelRefreshInterval int

	// Server start time (Unix ms)
	ServerStartTime int64
}

var global = &State{
	AccountType:          AccountIndividual,
	ModelIndex:           make(map[string]*Model),
	ModelIDs:             make(map[string]bool),
	ModelOverrides:       defaultModelOverrides(),
	StreamIdleTimeout:    300,
	FetchTimeout:         300,
	ShutdownGracefulWait: 60,
	ShutdownAbortWait:    120,
	HistoryLimit:         200,
	HistoryMinEntries:    50,
	StaleRequestMaxAge:   600,
	ModelRefreshInterval: 600,
	AutoTruncate:         true,
	RateLimit:            true,
}

func defaultModelOverrides() map[string]string {
	return map[string]string{
		"opus":   "claude-opus-4.6",
		"sonnet": "claude-sonnet-4.6",
		"haiku":  "claude-haiku-4.5",
	}
}

// Get returns the global state (read lock held by caller for reads).
func Get() *State {
	return global
}

// WithRead executes fn with a read lock.
func WithRead(fn func(*State)) {
	global.mu.RLock()
	defer global.mu.RUnlock()
	fn(global)
}

// WithWrite executes fn with a write lock.
func WithWrite(fn func(*State)) {
	global.mu.Lock()
	defer global.mu.Unlock()
	fn(global)
}

// SetGitHubToken atomically sets the GitHub token.
func SetGitHubToken(token string) {
	WithWrite(func(s *State) { s.GitHubToken = token })
}

// SetCopilotToken atomically sets the Copilot bearer token.
func SetCopilotToken(token string) {
	WithWrite(func(s *State) { s.CopilotToken = token })
}

// SetTokenInfo atomically sets the GitHub token metadata.
func SetTokenInfo(info *TokenInfo) {
	WithWrite(func(s *State) { s.TokenInfo = info })
}

// SetCopilotTokenInfo atomically sets the Copilot token metadata.
func SetCopilotTokenInfo(info *CopilotTokenInfo) {
	WithWrite(func(s *State) { s.CopilotTokenInfo = info })
}

// SetVSCodeVersion atomically sets the cached VSCode version.
func SetVSCodeVersion(v string) {
	WithWrite(func(s *State) { s.VSCodeVersion = v })
}

// SetModels atomically updates the model catalog and rebuilds indexes.
func SetModels(resp *ModelsResponse) {
	WithWrite(func(s *State) {
		s.Models = resp
		s.ModelIndex = make(map[string]*Model, len(resp.Data))
		s.ModelIDs = make(map[string]bool, len(resp.Data))
		for i := range resp.Data {
			m := &resp.Data[i]
			s.ModelIndex[m.ID] = m
			s.ModelIDs[m.ID] = true
		}
	})
}

// GetCopilotToken returns the current Copilot token (safe for concurrent reads).
func GetCopilotToken() string {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.CopilotToken
}

// GetGitHubToken returns the current GitHub token.
func GetGitHubToken() string {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.GitHubToken
}

// GetVSCodeVersion returns the cached VSCode version.
func GetVSCodeVersion() string {
	global.mu.RLock()
	defer global.mu.RUnlock()
	if global.VSCodeVersion != "" {
		return global.VSCodeVersion
	}
	return "1.104.3"
}

// GetAccountType returns the configured account type.
func GetAccountType() AccountType {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.AccountType
}

// IsHealthy returns true when both GitHub and Copilot tokens are set.
func IsHealthy() bool {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.GitHubToken != "" && global.CopilotToken != ""
}

// GetModelIndex returns a snapshot of the model index map.
func GetModelIndex() map[string]*Model {
	global.mu.RLock()
	defer global.mu.RUnlock()
	// Return a shallow copy to avoid callers holding the lock
	copy := make(map[string]*Model, len(global.ModelIndex))
	for k, v := range global.ModelIndex {
		copy[k] = v
	}
	return copy
}

// GetModelIDs returns a snapshot of available model IDs.
func GetModelIDs() map[string]bool {
	global.mu.RLock()
	defer global.mu.RUnlock()
	copy := make(map[string]bool, len(global.ModelIDs))
	for k, v := range global.ModelIDs {
		copy[k] = v
	}
	return copy
}

// GetModels returns the full models response.
func GetModels() *ModelsResponse {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.Models
}

// GetModelOverrides returns a copy of the model overrides map.
func GetModelOverrides() map[string]string {
	global.mu.RLock()
	defer global.mu.RUnlock()
	copy := make(map[string]string, len(global.ModelOverrides))
	for k, v := range global.ModelOverrides {
		copy[k] = v
	}
	return copy
}

// SetCLIFlags sets the CLI-provided flags.
func SetCLIFlags(accountType AccountType, verbose, showGitHubToken, autoTruncate, rateLimit bool) {
	WithWrite(func(s *State) {
		s.AccountType = accountType
		s.Verbose = verbose
		s.ShowGitHubToken = showGitHubToken
		s.AutoTruncate = autoTruncate
		s.RateLimit = rateLimit
	})
}

// SetServerStartTime records when the server started (Unix ms).
func SetServerStartTime(ms int64) {
	WithWrite(func(s *State) { s.ServerStartTime = ms })
}

// ApplyConfigDefaults writes config-file values into state.
func ApplyConfigDefaults(streamIdleTimeout, fetchTimeout, shutdownGraceful, shutdownAbort, historyLimit, historyMin, staleMaxAge, modelRefresh int, overrides map[string]string) {
	WithWrite(func(s *State) {
		if streamIdleTimeout > 0 {
			s.StreamIdleTimeout = streamIdleTimeout
		}
		if fetchTimeout > 0 {
			s.FetchTimeout = fetchTimeout
		}
		if shutdownGraceful > 0 {
			s.ShutdownGracefulWait = shutdownGraceful
		}
		if shutdownAbort > 0 {
			s.ShutdownAbortWait = shutdownAbort
		}
		if historyLimit > 0 {
			s.HistoryLimit = historyLimit
		}
		if historyMin > 0 {
			s.HistoryMinEntries = historyMin
		}
		if staleMaxAge > 0 {
			s.StaleRequestMaxAge = staleMaxAge
		}
		if modelRefresh > 0 {
			s.ModelRefreshInterval = modelRefresh
		}
		if overrides != nil {
			s.ModelOverrides = overrides
		}
	})
}
