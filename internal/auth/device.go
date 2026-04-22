// Package auth implements the GitHub OAuth device authorization flow and token file storage.
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/amalgamated-tools/copilot-api-go/internal/config"
	"github.com/amalgamated-tools/copilot-api-go/internal/copilot"
)

// DeviceCodeResponse is the initial response from GitHub's device code endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// accessTokenResponse is returned when polling for an access token.
type accessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

// GetDeviceCode requests a device code from GitHub.
func GetDeviceCode() (*DeviceCodeResponse, error) {
	payload := map[string]string{
		"client_id": copilot.ClientID,
		"scope":     "read:user",
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", copilot.GitHubBase+"/login/device/code", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("device code request failed (%d): %s", resp.StatusCode, string(body))
	}

	var result DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// PollAccessToken polls GitHub's OAuth endpoint until a token is granted or the device code expires.
func PollAccessToken(deviceCode *DeviceCodeResponse) (string, error) {
	sleepDuration := time.Duration(deviceCode.Interval+1) * time.Second
	expiresAt := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)

	payload := map[string]string{
		"client_id":   copilot.ClientID,
		"device_code": deviceCode.DeviceCode,
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
	}

	client := &http.Client{Timeout: 15 * time.Second}

	for time.Now().Before(expiresAt) {
		time.Sleep(sleepDuration)

		body, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST", copilot.GitHubBase+"/login/oauth/access_token", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		var result accessTokenResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if result.AccessToken != "" {
			return result.AccessToken, nil
		}
		// "authorization_pending" is normal; other errors are fatal
		if result.Error != "" && result.Error != "authorization_pending" && result.Error != "slow_down" {
			return "", fmt.Errorf("OAuth error: %s", result.Error)
		}
	}

	return "", errors.New("device code expired; please run authentication again")
}

// SaveToken writes a GitHub token to the data directory token file.
func SaveToken(token string) error {
	path := config.TokenPath()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(token)), 0o600); err != nil {
		return err
	}
	return nil
}

// LoadToken reads the GitHub token from the environment or data directory token file.
// Checks GITHUB_TOKEN env var first, then falls back to stored token file.
// Returns an empty string (and nil error) if no token is found.
func LoadToken() (string, error) {
	// Check environment variable first
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return strings.TrimSpace(token), nil
	}

	// Fall back to token file
	data, err := os.ReadFile(config.TokenPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// ClearToken removes any stored GitHub token.
func ClearToken() error {
	return os.WriteFile(config.TokenPath(), []byte(""), 0o600)
}

// RunDeviceFlow performs the full interactive device authorization flow.
// It prints the user code and URL, polls for a token, and saves it.
func RunDeviceFlow() (string, error) {
	deviceCode, err := GetDeviceCode()
	if err != nil {
		return "", fmt.Errorf("failed to get device code: %w", err)
	}

	fmt.Printf("Please enter the code %q at %s\n", deviceCode.UserCode, deviceCode.VerificationURI)

	token, err := PollAccessToken(deviceCode)
	if err != nil {
		return "", fmt.Errorf("failed to poll access token: %w", err)
	}

	if err := SaveToken(token); err != nil {
		return "", fmt.Errorf("failed to save token: %w", err)
	}

	return token, nil
}
