// Package handlers contains the HTTP route handler functions.
package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/amalgamated-tools/copilot-api-go/internal/copilot"
	"github.com/amalgamated-tools/copilot-api-go/internal/models"
	"github.com/amalgamated-tools/copilot-api-go/internal/ratelimit"
	"github.com/amalgamated-tools/copilot-api-go/internal/state"
	"github.com/amalgamated-tools/copilot-api-go/internal/token"
)

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"message": msg,
			"type":    "server_error",
		},
	})
}

// requireCopilotToken verifies the Copilot token is set, writing an error if not.
func requireCopilotToken(w http.ResponseWriter) bool {
	if state.GetCopilotToken() == "" {
		jsonError(w, "Copilot token not available", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// proxyConfig holds options for proxyUpstream.
type proxyConfig struct {
	// upstream is the full URL to forward to.
	upstream string
	// headers to send to upstream (replaces default Copilot headers).
	headers map[string]string
	// If true, add the Anthropic-version beta header for streaming.
	anthropicStream bool
}

// proxyUpstream forwards the incoming request body to an upstream URL,
// handling both streaming (SSE) and non-streaming JSON responses.
func proxyUpstream(w http.ResponseWriter, r *http.Request, cfg proxyConfig) {
	if !requireCopilotToken(w) {
		return
	}

	// Rate limit wait
	if lim := ratelimit.Get(); lim != nil {
		lim.WaitIfNeeded()
	}

	// Ensure Copilot token is valid
	if err := token.EnsureValidCopilotToken(); err != nil {
		slog.WarnContext(r.Context(), "token refresh failed", "error", err)
	}

	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Detect streaming
	var payload map[string]interface{}
	streaming := false
	if err := json.Unmarshal(bodyBytes, &payload); err == nil {
		if s, ok := payload["stream"].(bool); ok {
			streaming = s
		}
	}

	// Resolve model name if present
	if modelRaw, ok := payload["model"].(string); ok {
		resolved := models.ResolveModelName(modelRaw)
		if resolved != modelRaw {
			payload["model"] = resolved
			bodyBytes, _ = json.Marshal(payload)
		}
	}

	// Set max_tokens to model limit if not set (for OpenAI endpoints)
	if _, hasMax := payload["max_tokens"]; !hasMax {
		if modelID, ok := payload["model"].(string); ok {
			idx := state.GetModelIndex()
			if m, ok := idx[modelID]; ok && m.Capabilities != nil && m.Capabilities.Limits != nil && m.Capabilities.Limits.MaxOutputTokens != nil {
				payload["max_tokens"] = *m.Capabilities.Limits.MaxOutputTokens
				bodyBytes, _ = json.Marshal(payload)
			}
		}
	}

	headers := cfg.headers
	if headers == nil {
		headers = copilot.CopilotHeaders()
	}

	// Create upstream request
	upstreamReq, err := http.NewRequestWithContext(r.Context(), "POST", cfg.upstream, bytes.NewReader(bodyBytes))
	if err != nil {
		jsonError(w, "failed to create upstream request", http.StatusInternalServerError)
		return
	}
	copilot.ApplyHeaders(upstreamReq, headers)

	var fetchTimeout int
	state.WithRead(func(s *state.State) { fetchTimeout = s.FetchTimeout })
	if fetchTimeout <= 0 {
		fetchTimeout = 300
	}

	client := &http.Client{Timeout: time.Duration(fetchTimeout) * time.Second}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		if r.Context().Err() != nil {
			return // client disconnected
		}
		jsonError(w, fmt.Sprintf("upstream request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Record rate limit signal
	if resp.StatusCode == http.StatusTooManyRequests {
		if lim := ratelimit.Get(); lim != nil {
			lim.RecordRateLimit(0)
		}
	} else if lim := ratelimit.Get(); lim != nil {
		lim.RecordSuccess()
	}

	// Forward non-2xx errors as-is
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	if streaming {
		forwardSSE(w, r, resp)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// forwardSSE reads an SSE stream from upstream and forwards it to the client.
func forwardSSE(w http.ResponseWriter, r *http.Request, resp *http.Response) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)

		// SSE events are separated by blank lines; flush after each blank line
		if line == "" {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		slog.ErrorContext(r.Context(), "error forwarding SSE stream", "error", err)
	}

	// Ensure we flush at the end
	flusher.Flush()
}

// ============================================================================
// Health
// ============================================================================

// Health handles GET /health.
func Health(w http.ResponseWriter, r *http.Request) {
	healthy := state.IsHealthy()
	var copilotToken, githubToken, hasModels bool
	state.WithRead(func(s *state.State) {
		copilotToken = s.CopilotToken != ""
		githubToken = s.GitHubToken != ""
		hasModels = s.Models != nil
	})

	code := http.StatusOK
	status := "healthy"
	if !healthy {
		code = http.StatusServiceUnavailable
		status = "unhealthy"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": status,
		"checks": map[string]bool{
			"copilotToken": copilotToken,
			"githubToken":  githubToken,
			"models":       hasModels,
		},
	})
}

// ============================================================================
// Chat Completions
// ============================================================================

// ChatCompletions handles POST /chat/completions and POST /v1/chat/completions.
func ChatCompletions(w http.ResponseWriter, r *http.Request) {
	proxyUpstream(w, r, proxyConfig{
		upstream: copilot.BaseURL() + "/chat/completions",
	})
}

// ============================================================================
// Responses API
// ============================================================================

// Responses handles POST /responses and POST /v1/responses.
func Responses(w http.ResponseWriter, r *http.Request) {
	proxyUpstream(w, r, proxyConfig{
		upstream: copilot.BaseURL() + "/responses",
	})
}

// ============================================================================
// Anthropic Messages
// ============================================================================

// Messages handles POST /v1/messages.
func Messages(w http.ResponseWriter, r *http.Request) {
	proxyUpstream(w, r, proxyConfig{
		upstream: copilot.BaseURL() + "/v1/messages",
	})
}

// CountTokens handles POST /v1/messages/count_tokens.
func CountTokens(w http.ResponseWriter, r *http.Request) {
	if !requireCopilotToken(w) {
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Resolve model name
	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err == nil {
		if modelRaw, ok := payload["model"].(string); ok {
			resolved := models.ResolveModelName(modelRaw)
			if resolved != modelRaw {
				payload["model"] = resolved
				bodyBytes, _ = json.Marshal(payload)
			}
		}
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), "POST",
		copilot.BaseURL()+"/v1/messages/count_tokens", bytes.NewReader(bodyBytes))
	if err != nil {
		jsonError(w, "failed to create upstream request", http.StatusInternalServerError)
		return
	}
	copilot.ApplyHeaders(upstreamReq, copilot.CopilotHeaders())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		jsonError(w, fmt.Sprintf("upstream request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ============================================================================
// Embeddings
// ============================================================================

// Embeddings handles POST /embeddings and POST /v1/embeddings.
func Embeddings(w http.ResponseWriter, r *http.Request) {
	if !requireCopilotToken(w) {
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Normalize input: string → array
	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err == nil {
		if input, ok := payload["input"].(string); ok {
			payload["input"] = []string{input}
			bodyBytes, _ = json.Marshal(payload)
		}
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), "POST",
		copilot.BaseURL()+"/embeddings", bytes.NewReader(bodyBytes))
	if err != nil {
		jsonError(w, "failed to create upstream request", http.StatusInternalServerError)
		return
	}
	copilot.ApplyHeaders(upstreamReq, copilot.CopilotHeaders())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		jsonError(w, fmt.Sprintf("upstream request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ============================================================================
// Models
// ============================================================================

// stripInternalFields removes the request_headers field before sending to client.
func stripInternalFields(m *state.Model) map[string]interface{} {
	result := map[string]interface{}{
		"id":                    m.ID,
		"name":                  m.Name,
		"vendor":                m.Vendor,
		"version":               m.Version,
		"object":                m.Object,
		"preview":               m.Preview,
		"is_chat_default":       m.IsChatDefault,
		"is_chat_fallback":      m.IsChatFallback,
		"model_picker_enabled":  m.ModelPickerEnabled,
		"model_picker_category": m.ModelPickerCategory,
		"supported_endpoints":   m.SupportedEndpoints,
	}
	if m.Capabilities != nil {
		caps := map[string]interface{}{
			"family":    m.Capabilities.Family,
			"tokenizer": m.Capabilities.Tokenizer,
			"type":      m.Capabilities.Type,
			"supports":  m.Capabilities.Supports,
		}
		if m.Capabilities.Limits != nil {
			limits := map[string]interface{}{}
			if m.Capabilities.Limits.MaxContextWindowTokens != nil {
				limits["max_context_window_tokens"] = *m.Capabilities.Limits.MaxContextWindowTokens
			}
			if m.Capabilities.Limits.MaxOutputTokens != nil {
				limits["max_output_tokens"] = *m.Capabilities.Limits.MaxOutputTokens
			}
			if m.Capabilities.Limits.MaxPromptTokens != nil {
				limits["max_prompt_tokens"] = *m.Capabilities.Limits.MaxPromptTokens
			}
			if m.Capabilities.Limits.MaxNonStreamingOutputTokens != nil {
				limits["max_non_streaming_output_tokens"] = *m.Capabilities.Limits.MaxNonStreamingOutputTokens
			}
			caps["limits"] = limits
		}
		result["capabilities"] = caps
	}
	return result
}

// ListModels handles GET /models and GET /v1/models.
func ListModels(w http.ResponseWriter, r *http.Request) {
	resp := state.GetModels()
	if resp == nil {
		if err := models.Fetch(); err != nil {
			jsonError(w, "failed to fetch models", http.StatusInternalServerError)
			return
		}
		resp = state.GetModels()
	}

	data := make([]map[string]interface{}, 0, len(resp.Data))
	for i := range resp.Data {
		data = append(data, stripInternalFields(&resp.Data[i]))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": resp.Object,
		"data":   data,
	})
}

// GetModel handles GET /models/{model} and GET /v1/models/{model}.
func GetModel(w http.ResponseWriter, r *http.Request) {
	// Extract model ID from path — handles both /models/{id} and /v1/models/{id}
	path := r.URL.Path
	// Strip trailing slash
	path = strings.TrimSuffix(path, "/")
	// The model ID is the last path segment
	parts := strings.Split(path, "/")
	modelID := parts[len(parts)-1]

	resp := state.GetModels()
	if resp == nil {
		if err := models.Fetch(); err != nil {
			jsonError(w, "failed to fetch models", http.StatusInternalServerError)
			return
		}
	}

	idx := state.GetModelIndex()
	m, ok := idx[modelID]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": fmt.Sprintf("The model '%s' does not exist", modelID),
				"type":    "invalid_request_error",
				"param":   "model",
				"code":    "model_not_found",
			},
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stripInternalFields(m))
}

// ============================================================================
// Event Logging
// ============================================================================

// EventLoggingBatch handles POST /api/event_logging/batch.
// The Anthropic SDK sends telemetry here; we return 200 OK to avoid SDK errors.
func EventLoggingBatch(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// ============================================================================
// Status
// ============================================================================

var (
	quotaMu        sync.Mutex
	cachedQuota    interface{}
	quotaFetchedAt time.Time
	quotaCacheTTL  = 60 * time.Second
)

func getQuota() interface{} {
	quotaMu.Lock()
	defer quotaMu.Unlock()
	if cachedQuota != nil && time.Since(quotaFetchedAt) < quotaCacheTTL {
		return cachedQuota
	}
	usage, err := token.GetCopilotUsage()
	if err != nil {
		return cachedQuota // return stale value on error
	}
	cachedQuota = map[string]interface{}{
		"plan":                usage.CopilotPlan,
		"resetDate":           usage.QuotaResetDate,
		"chat":                usage.QuotaSnapshots.Chat,
		"completions":         usage.QuotaSnapshots.Completions,
		"premiumInteractions": usage.QuotaSnapshots.PremiumInteractions,
	}
	quotaFetchedAt = time.Now()
	return cachedQuota
}

// Status handles GET /api/status.
func Status(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UnixMilli()

	var serverStatus string
	var uptime int64
	var accountType state.AccountType
	var tokenSource, tokenExpiresAt interface{}
	var copilotExpiresAt interface{}
	var modelTotal, modelAvail int
	var serverStartTime int64

	state.WithRead(func(s *state.State) {
		if s.CopilotToken != "" && s.GitHubToken != "" {
			serverStatus = "healthy"
		} else {
			serverStatus = "unhealthy"
		}
		serverStartTime = s.ServerStartTime
		accountType = s.AccountType
		if s.TokenInfo != nil {
			tokenSource = s.TokenInfo.Source
			tokenExpiresAt = s.TokenInfo.ExpiresAt
		}
		if s.CopilotTokenInfo != nil {
			copilotExpiresAt = s.CopilotTokenInfo.ExpiresAt * 1000
		}
		if s.Models != nil {
			modelTotal = len(s.Models.Data)
		}
		modelAvail = len(s.ModelIDs)
	})

	if serverStartTime > 0 {
		uptime = (now - serverStartTime) / 1000
	}

	// Rate limiter status
	var rateLimiter interface{}
	if lim := ratelimit.Get(); lim != nil {
		st := lim.GetStatus()
		rateLimiter = map[string]interface{}{
			"enabled": true,
			"mode":    st.Mode,
			"config":  lim.GetConfig(),
		}
	} else {
		rateLimiter = map[string]interface{}{"enabled": false}
	}

	// Quota (cached with TTL to avoid live API calls on every request)
	quota := getQuota()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  serverStatus,
		"uptime":  uptime,
		"version": "go-1.0.0",
		"auth": map[string]interface{}{
			"accountType":           accountType,
			"tokenSource":           tokenSource,
			"tokenExpiresAt":        tokenExpiresAt,
			"copilotTokenExpiresAt": copilotExpiresAt,
		},
		"quota":       quota,
		"rateLimiter": rateLimiter,
		"models": map[string]int{
			"totalCount":     modelTotal,
			"availableCount": modelAvail,
		},
	})
}

// ============================================================================
// Token info
// ============================================================================

// TokenInfo handles GET /api/tokens.
func TokenInfo(w http.ResponseWriter, r *http.Request) {
	var githubInfo, copilotInfo interface{}

	state.WithRead(func(s *state.State) {
		if s.TokenInfo != nil {
			githubInfo = map[string]interface{}{
				"token":       s.TokenInfo.Token,
				"source":      s.TokenInfo.Source,
				"expiresAt":   s.TokenInfo.ExpiresAt,
				"refreshable": s.TokenInfo.Refreshable,
			}
		}
		if s.CopilotTokenInfo != nil {
			copilotInfo = map[string]interface{}{
				"token":     s.CopilotTokenInfo.Token,
				"expiresAt": s.CopilotTokenInfo.ExpiresAt,
				"refreshIn": s.CopilotTokenInfo.RefreshIn,
			}
		}
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"github":  githubInfo,
		"copilot": copilotInfo,
	})
}

// ============================================================================
// Config
// ============================================================================

// Config handles GET /api/config.
func Config(w http.ResponseWriter, r *http.Request) {
	var cfg map[string]interface{}
	state.WithRead(func(s *state.State) {
		cfg = map[string]interface{}{
			"verbose":              s.Verbose,
			"autoTruncate":         s.AutoTruncate,
			"modelOverrides":       s.ModelOverrides,
			"streamIdleTimeout":    s.StreamIdleTimeout,
			"fetchTimeout":         s.FetchTimeout,
			"shutdownGracefulWait": s.ShutdownGracefulWait,
			"shutdownAbortWait":    s.ShutdownAbortWait,
			"historyLimit":         s.HistoryLimit,
			"historyMinEntries":    s.HistoryMinEntries,
			"staleRequestMaxAge":   s.StaleRequestMaxAge,
			"modelRefreshInterval": s.ModelRefreshInterval,
		}
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

// ============================================================================
// Logs
// ============================================================================

// Logs handles GET /api/logs.
func Logs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": []interface{}{},
		"total":   0,
	})
}
