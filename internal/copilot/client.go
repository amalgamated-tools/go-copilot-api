// Package copilot provides helpers for building requests to the GitHub Copilot API.
package copilot

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/amalgamated-tools/copilot-api-go/internal/state"
)

const (
	copilotVersion       = "0.38.0"
	editorPluginVersion  = "copilot-chat/" + copilotVersion
	userAgent            = "GitHubCopilotChat/" + copilotVersion
	copilotAPIVersion    = "2025-05-01"
	internalAPIVersion   = "2025-04-01"
	githubAPIVersion     = "2022-11-28"

	GitHubAPIBase = "https://api.github.com"
	GitHubBase    = "https://github.com"
	ClientID      = "Iv1.b507a08c87ecfe98"
)

// interactionID is set once per server lifetime.
var interactionID = newUUID()

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// BaseURL returns the Copilot API base URL for the current account type.
func BaseURL() string {
	at := state.GetAccountType()
	if at == state.AccountIndividual {
		return "https://api.githubcopilot.com"
	}
	return fmt.Sprintf("https://api.%s.githubcopilot.com", at)
}

// CopilotHeaders builds the standard headers required by the Copilot chat API.
func CopilotHeaders() map[string]string {
	token := state.GetCopilotToken()
	vsCode := state.GetVSCodeVersion()
	requestID := hex.EncodeToString(func() []byte { b := make([]byte, 16); rand.Read(b); return b }())

	return map[string]string{
		"Authorization":                        "Bearer " + token,
		"Content-Type":                         "application/json",
		"Accept":                               "application/json",
		"copilot-integration-id":               "vscode-chat",
		"editor-version":                       "vscode/" + vsCode,
		"editor-plugin-version":                editorPluginVersion,
		"user-agent":                           userAgent,
		"openai-intent":                        "conversation-panel",
		"x-github-api-version":                 copilotAPIVersion,
		"x-request-id":                         requestID,
		"X-Interaction-Id":                     interactionID,
		"X-Interaction-Type":                   "conversation-panel",
		"X-Agent-Task-Id":                      requestID,
		"x-vscode-user-agent-library-version":  "electron-fetch",
	}
}

// GitHubHeaders builds the headers required for GitHub public API calls.
func GitHubHeaders() map[string]string {
	token := state.GetGitHubToken()
	vsCode := state.GetVSCodeVersion()
	return map[string]string{
		"Content-Type":                        "application/json",
		"Accept":                              "application/json",
		"Authorization":                       "token " + token,
		"editor-version":                      "vscode/" + vsCode,
		"editor-plugin-version":               editorPluginVersion,
		"user-agent":                          userAgent,
		"x-github-api-version":                githubAPIVersion,
		"x-vscode-user-agent-library-version": "electron-fetch",
	}
}

// GitHubInternalHeaders returns headers for GitHub's internal Copilot endpoints.
func GitHubInternalHeaders() map[string]string {
	h := GitHubHeaders()
	h["x-github-api-version"] = internalAPIVersion
	return h
}

// ApplyHeaders sets a map of headers on an HTTP request.
func ApplyHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
}

// Do makes an HTTP request and returns the response.
// The caller is responsible for closing resp.Body.
func Do(method, url string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	ApplyHeaders(req, headers)
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}

// DecodeJSON reads an HTTP response body into v and closes the body.
func DecodeJSON(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// ReadBody reads and closes the response body, returning it as a string.
func ReadBody(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return string(data), err
}

// VSCodeVersionFallback is used when the GitHub API is unavailable.
const VSCodeVersionFallback = "1.104.3"

// FetchVSCodeVersion fetches the latest VSCode release tag from GitHub.
func FetchVSCodeVersion() (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/microsoft/vscode/releases/latest", nil)
	if err != nil {
		return VSCodeVersionFallback, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "copilot-api")

	resp, err := client.Do(req)
	if err != nil {
		return VSCodeVersionFallback, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return VSCodeVersionFallback, nil
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return VSCodeVersionFallback, nil
	}

	if release.TagName != "" {
		return release.TagName, nil
	}
	return VSCodeVersionFallback, nil
}
