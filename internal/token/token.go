// Package token implements GitHub and Copilot token management.
package token

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/amalgamated-tools/copilot-api-go/internal/auth"
	"github.com/amalgamated-tools/copilot-api-go/internal/copilot"
	"github.com/amalgamated-tools/copilot-api-go/internal/state"
)

const (
	keyError = "error"
	keyUser  = "user"
)

// GitHubUser represents a GitHub user from /user.
type GitHubUser struct {
	Login string `json:"login"`
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// GetGitHubUser fetches the authenticated user's info.
func GetGitHubUser() (*GitHubUser, error) {
	req, err := http.NewRequest("GET", copilot.GitHubAPIBase+"/user", nil)
	if err != nil {
		return nil, err
	}
	copilot.ApplyHeaders(req, copilot.GitHubHeaders())

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub /user failed (%d): %s", resp.StatusCode, string(body))
	}

	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

// copilotTokenResponse is the raw /copilot_internal/v2/token response.
type copilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	RefreshIn int    `json:"refresh_in"`
}

// fetchCopilotToken retrieves a new Copilot bearer token from GitHub.
func fetchCopilotToken() (*copilotTokenResponse, error) {
	req, err := http.NewRequest("GET", copilot.GitHubAPIBase+"/copilot_internal/v2/token", nil)
	if err != nil {
		return nil, err
	}
	copilot.ApplyHeaders(req, copilot.GitHubInternalHeaders())

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot token request failed (%d): %s", resp.StatusCode, string(body))
	}

	var result copilotTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Manager handles GitHub and Copilot token lifecycle.
type Manager struct {
	stopRefresh chan struct{}
	stopOnce    sync.Once
}

var globalManager = &Manager{
	stopRefresh: make(chan struct{}),
}

// GetManager returns the global token manager.
func GetManager() *Manager {
	return globalManager
}

// InitOptions controls how token initialization works.
type InitOptions struct {
	// CLIToken is an explicit token from --github-token flag.
	CLIToken string
}

// Init resolves the GitHub token (CLI > ENV > file > device flow), authenticates,
// fetches a Copilot token, and starts the background refresh loop.
func Init(opts InitOptions) error {
	// 1. Resolve GitHub token
	token, source, err := resolveGitHubToken(opts.CLIToken)
	if err != nil {
		return fmt.Errorf("failed to resolve GitHub token: %w", err)
	}

	// 2. Store in state
	state.SetGitHubToken(token)
	expiresAt := (*int64)(nil) // unknown
	state.SetTokenInfo(&state.TokenInfo{
		Token:       token,
		Source:      source,
		ExpiresAt:   expiresAt,
		Refreshable: source == "device-auth",
	})

	// 3. Validate token via GitHub /user
	user, err := GetGitHubUser()
	if err != nil {
		return fmt.Errorf("GitHub token validation failed: %w", err)
	}
	slog.InfoContext(context.Background(), "Logged in", slog.String(keyUser, user.Login))

	// 4. Fetch initial Copilot token
	if err := refreshCopilotToken(); err != nil {
		return fmt.Errorf("failed to obtain Copilot token: %w", err)
	}

	// 5. Start background refresh loop
	go globalManager.refreshLoop()

	return nil
}

// resolveGitHubToken returns the GitHub token and its source.
func resolveGitHubToken(cliToken string) (string, string, error) {
	// Priority 1: CLI --github-token
	if cliToken != "" {
		return cliToken, "cli", nil
	}

	// Priority 2: GITHUB_TOKEN environment variable
	if envToken := os.Getenv("GITHUB_TOKEN"); envToken != "" {
		return envToken, "env", nil
	}

	// Priority 3: File storage
	fileToken, err := auth.LoadToken()
	if err == nil && fileToken != "" {
		return fileToken, "file", nil
	}

	// Priority 4: Device authorization flow
	token, err := auth.RunDeviceFlow()
	if err != nil {
		return "", "", err
	}
	return token, "device-auth", nil
}

// refreshCopilotToken fetches a new Copilot token and updates state.
func refreshCopilotToken() error {
	resp, err := fetchCopilotToken()
	if err != nil {
		return err
	}
	state.SetCopilotToken(resp.Token)
	state.SetCopilotTokenInfo(&state.CopilotTokenInfo{
		Token:     resp.Token,
		ExpiresAt: resp.ExpiresAt,
		RefreshIn: resp.RefreshIn,
	})
	return nil
}

// EnsureValidCopilotToken refreshes the Copilot token if it's expired or near expiry.
func EnsureValidCopilotToken() error {
	var info *state.CopilotTokenInfo
	state.WithRead(func(s *state.State) { info = s.CopilotTokenInfo })
	if info == nil {
		return refreshCopilotToken()
	}
	// Refresh if within 60 seconds of expiry
	if time.Now().Unix()+60 >= info.ExpiresAt {
		return refreshCopilotToken()
	}
	return nil
}

// refreshLoop runs as a goroutine, refreshing the Copilot token before it expires.
func (m *Manager) refreshLoop() {
	for {
		var refreshIn int
		state.WithRead(func(s *state.State) {
			if s.CopilotTokenInfo != nil {
				refreshIn = s.CopilotTokenInfo.RefreshIn
			}
		})
		if refreshIn <= 0 {
			refreshIn = 1500 // default ~25 min
		}

		// Refresh slightly before expiry
		wait := time.Duration(refreshIn-60) * time.Second
		if wait < 30*time.Second {
			wait = 30 * time.Second
		}

		select {
		case <-m.stopRefresh:
			return
		case <-time.After(wait):
			if err := refreshCopilotToken(); err != nil {
				slog.ErrorContext(context.Background(), "Failed to refresh Copilot token", slog.Any(keyError, err))
			}
		}
	}
}

// StopRefresh signals the background refresh goroutine to stop by closing the channel.
func StopRefresh() {
	globalManager.stopOnce.Do(func() {
		close(globalManager.stopRefresh)
	})
}

// CopilotUsage represents quota information.
type CopilotUsage struct {
	Login          string    `json:"login"`
	CopilotPlan    string    `json:"copilot_plan"`
	QuotaResetDate string    `json:"quota_reset_date"`
	QuotaSnapshots QuotaSnap `json:"quota_snapshots"`
}

// QuotaSnap holds the quota breakdown.
type QuotaSnap struct {
	Chat                   QuotaDetail `json:"chat"`
	Completions            QuotaDetail `json:"completions"`
	PremiumInteractions    QuotaDetail `json:"premium_interactions"`
	ImmediateUsageInterval QuotaDetail `json:"immediate_usage_interval"`
}

// QuotaDetail is one quota category.
type QuotaDetail struct {
	Entitlement      int     `json:"entitlement"`
	HasQuota         bool    `json:"has_quota"`
	OverageCount     int     `json:"overage_count"`
	OveragePermitted bool    `json:"overage_permitted"`
	PercentRemaining float64 `json:"percent_remaining"`
	QuotaID          string  `json:"quota_id"`
	QuotaRemaining   float64 `json:"quota_remaining"`
	Remaining        int     `json:"remaining"`
	Unlimited        bool    `json:"unlimited"`
}

// GetCopilotUsage retrieves the authenticated user's Copilot quota.
func GetCopilotUsage() (*CopilotUsage, error) {
	req, err := http.NewRequest("GET", copilot.GitHubAPIBase+"/copilot_internal/user", nil)
	if err != nil {
		return nil, err
	}
	copilot.ApplyHeaders(req, copilot.GitHubInternalHeaders())

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot usage request failed (%d): %s", resp.StatusCode, string(body))
	}

	var result CopilotUsage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
