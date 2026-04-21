// Package models fetches and caches the Copilot model catalog.
package models

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/amalgamated-tools/copilot-api-go/internal/copilot"
	"github.com/amalgamated-tools/copilot-api-go/internal/state"
)

// rawModel mirrors the JSON shape returned by Copilot's /models endpoint.
type rawModel struct {
	ID                  string                 `json:"id"`
	Name                string                 `json:"name"`
	Vendor              string                 `json:"vendor"`
	Version             string                 `json:"version"`
	Object              string                 `json:"object"`
	Preview             bool                   `json:"preview"`
	IsChatDefault       bool                   `json:"is_chat_default"`
	IsChatFallback      bool                   `json:"is_chat_fallback"`
	ModelPickerEnabled  bool                   `json:"model_picker_enabled"`
	ModelPickerCategory string                 `json:"model_picker_category,omitempty"`
	SupportedEndpoints  []string               `json:"supported_endpoints,omitempty"`
	RequestHeaders      map[string]string      `json:"request_headers,omitempty"`
	Capabilities        *rawModelCapabilities  `json:"capabilities,omitempty"`
}

type rawModelCapabilities struct {
	Family    string            `json:"family,omitempty"`
	Object    string            `json:"object,omitempty"`
	Tokenizer string            `json:"tokenizer,omitempty"`
	Type      string            `json:"type,omitempty"`
	Limits    *rawModelLimits   `json:"limits,omitempty"`
	Supports  map[string]interface{} `json:"supports,omitempty"`
}

type rawModelLimits struct {
	MaxContextWindowTokens      *int `json:"max_context_window_tokens,omitempty"`
	MaxOutputTokens             *int `json:"max_output_tokens,omitempty"`
	MaxPromptTokens             *int `json:"max_prompt_tokens,omitempty"`
	MaxNonStreamingOutputTokens *int `json:"max_non_streaming_output_tokens,omitempty"`
}

type rawModelsResponse struct {
	Object string     `json:"object"`
	Data   []rawModel `json:"data"`
}

// Fetch retrieves the model list from Copilot API and caches it in state.
func Fetch() error {
	req, err := http.NewRequest("GET", copilot.BaseURL()+"/models", nil)
	if err != nil {
		return err
	}
	copilot.ApplyHeaders(req, copilot.CopilotHeaders())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("models request failed (%d): %s", resp.StatusCode, string(body))
	}

	var raw rawModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return err
	}

	models := convertModels(raw)
	state.SetModels(&models)
	return nil
}

func convertModels(raw rawModelsResponse) state.ModelsResponse {
	data := make([]state.Model, 0, len(raw.Data))
	for _, r := range raw.Data {
		m := state.Model{
			ID:                  r.ID,
			Name:                r.Name,
			Vendor:              r.Vendor,
			Version:             r.Version,
			Object:              r.Object,
			Preview:             r.Preview,
			IsChatDefault:       r.IsChatDefault,
			IsChatFallback:      r.IsChatFallback,
			ModelPickerEnabled:  r.ModelPickerEnabled,
			ModelPickerCategory: r.ModelPickerCategory,
			SupportedEndpoints:  r.SupportedEndpoints,
			RequestHeaders:      r.RequestHeaders,
		}
		if r.Capabilities != nil {
			caps := &state.ModelCapabilities{
				Family:    r.Capabilities.Family,
				Tokenizer: r.Capabilities.Tokenizer,
				Type:      r.Capabilities.Type,
				Supports:  state.ModelSupports(r.Capabilities.Supports),
			}
			if r.Capabilities.Limits != nil {
				caps.Limits = &state.ModelLimits{
					MaxContextWindowTokens:      r.Capabilities.Limits.MaxContextWindowTokens,
					MaxOutputTokens:             r.Capabilities.Limits.MaxOutputTokens,
					MaxPromptTokens:             r.Capabilities.Limits.MaxPromptTokens,
					MaxNonStreamingOutputTokens: r.Capabilities.Limits.MaxNonStreamingOutputTokens,
				}
			}
			m.Capabilities = caps
		}
		data = append(data, m)
	}
	return state.ModelsResponse{Object: raw.Object, Data: data}
}

// StartRefreshLoop runs a background goroutine refreshing models every interval seconds.
// It returns a cancel function that stops the loop.
func StartRefreshLoop(intervalSec int) (stop func()) {
	if intervalSec <= 0 {
		return func() {}
	}
	stopCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if err := Fetch(); err != nil {
					slog.Error("model refresh failed", "error", err)
				}
			}
		}
	}()
	return func() { close(stopCh) }
}

// ============================================================================
// Model name resolution
// ============================================================================

// modelPreference lists the preferred models per family (highest priority first).
var modelPreference = map[string][]string{
	"opus":   {"claude-opus-4.6", "claude-opus-4.5", "claude-opus-41"},
	"sonnet": {"claude-sonnet-4.6", "claude-sonnet-4.5", "claude-sonnet-4"},
	"haiku":  {"claude-haiku-4.5"},
}

// knownModifiers are suffixes that may appear after the version number.
var knownModifiers = []string{"-fast", "-1m"}

// ResolveModelName applies alias expansion, hyphen→dot normalization, date-suffix stripping,
// and model override mapping to produce the model ID that should be sent to Copilot.
func ResolveModelName(model string) string {
	// 0. Bracket notation: "opus[1m]" → "opus-1m"
	model = normalizeBracketNotation(model)

	modelIDs := state.GetModelIDs()
	overrides := state.GetModelOverrides()

	// 1. Raw override check
	if target, ok := overrides[model]; ok {
		return resolveOverrideTarget(model, target, modelIDs, overrides, nil)
	}

	// 2. Core resolution
	resolved := resolveCore(model, modelIDs)

	// 3. Check resolved name against overrides
	if resolved != model {
		if target, ok := overrides[resolved]; ok {
			return resolveOverrideTarget(resolved, target, modelIDs, overrides, nil)
		}
	}

	// 4. Family-level override
	family := getFamily(resolved)
	if family != "" {
		if target, ok := overrides[family]; ok {
			defaultTargets := map[string]string{
				"opus": "claude-opus-4.6", "sonnet": "claude-sonnet-4.6", "haiku": "claude-haiku-4.5",
			}
			if target != defaultTargets[family] {
				familyResolved := resolveOverrideTarget(family, target, modelIDs, overrides, nil)
				if familyResolved != resolved {
					return familyResolved
				}
			}
		}
	}

	return resolved
}

func normalizeBracketNotation(model string) string {
	idx := strings.Index(model, "[")
	if idx < 0 || !strings.HasSuffix(model, "]") {
		return model
	}
	return model[:idx] + "-" + strings.ToLower(model[idx+1:len(model)-1])
}

func extractModifierSuffix(model string) (base, suffix string) {
	lower := strings.ToLower(model)
	for _, mod := range knownModifiers {
		if strings.HasSuffix(lower, mod) {
			return model[:len(model)-len(mod)], mod
		}
	}
	return model, ""
}

func findPreferred(family string, modelIDs map[string]bool) string {
	pref, ok := modelPreference[family]
	if !ok {
		return family
	}
	if len(modelIDs) == 0 {
		return pref[0]
	}
	for _, candidate := range pref {
		if modelIDs[candidate] {
			return candidate
		}
	}
	return pref[0]
}

func getFamily(model string) string {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "opus") {
		return "opus"
	}
	if strings.Contains(lower, "sonnet") {
		return "sonnet"
	}
	if strings.Contains(lower, "haiku") {
		return "haiku"
	}
	return ""
}

// versioned matches "claude-{family}-{major}-{minor}[-YYYYMMDD]"
func resolveCore(model string, modelIDs map[string]bool) string {
	base, suffix := extractModifierSuffix(model)

	resolvedBase := resolveBase(base, modelIDs)

	if suffix != "" {
		withSuffix := resolvedBase + suffix
		if len(modelIDs) == 0 || modelIDs[withSuffix] {
			return withSuffix
		}
		return resolvedBase
	}
	return resolvedBase
}

func resolveBase(model string, modelIDs map[string]bool) string {
	// Short alias
	if _, ok := modelPreference[model]; ok {
		return findPreferred(model, modelIDs)
	}

	// Hyphenated: claude-opus-4-6 → claude-opus-4.6
	if converted := hyphenToDot(model); converted != model {
		if len(modelIDs) == 0 || modelIDs[converted] {
			return converted
		}
	}

	// Date suffix: claude-opus-4-20250514 → find best opus
	if base, family := stripDateSuffix(model); family != "" {
		if modelIDs[base] {
			return base
		}
		return findPreferred(family, modelIDs)
	}

	return model
}

// hyphenToDot converts "claude-opus-4-6" to "claude-opus-4.6".
// Pattern: claude-{word}-{major}-{minor}
func hyphenToDot(model string) string {
	parts := strings.Split(model, "-")
	if len(parts) < 4 {
		return model
	}
	// Must start with "claude"
	if parts[0] != "claude" {
		return model
	}
	// parts[len-1] should be 1-2 digit minor, parts[len-2] should be 1-2 digit major
	last := parts[len(parts)-1]
	prev := parts[len(parts)-2]
	// Check if last part is 1-2 digits (version minor, not date)
	if len(last) < 1 || len(last) > 2 || !isDigits(last) {
		return model
	}
	if len(prev) < 1 || len(prev) > 2 || !isDigits(prev) {
		return model
	}
	base := strings.Join(parts[:len(parts)-2], "-")
	return base + "-" + prev + "." + last
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// stripDateSuffix detects "claude-{family}-{major}-YYYYMMDD" patterns.
func stripDateSuffix(model string) (base, family string) {
	parts := strings.Split(model, "-")
	if len(parts) < 4 {
		return "", ""
	}
	last := parts[len(parts)-1]
	if len(last) < 8 || !isDigits(last) {
		return "", ""
	}
	// The last part looks like a date (8+ digits)
	base = strings.Join(parts[:len(parts)-1], "-")
	fam := getFamily(base)
	return base, fam
}

func resolveOverrideTarget(source, target string, modelIDs map[string]bool, overrides map[string]string, seen map[string]bool) string {
	if len(modelIDs) == 0 || modelIDs[target] {
		return target
	}
	if seen == nil {
		seen = map[string]bool{source: true}
	}
	// Chain: check if target itself has an override
	if nextTarget, ok := overrides[target]; ok && !seen[target] {
		seen[target] = true
		return resolveOverrideTarget(target, nextTarget, modelIDs, overrides, seen)
	}
	// Alias resolution
	resolved := resolveCore(target, modelIDs)
	if resolved != target {
		return resolved
	}
	// Family fallback
	if fam := getFamily(target); fam != "" {
		return findPreferred(fam, modelIDs)
	}
	return target
}
