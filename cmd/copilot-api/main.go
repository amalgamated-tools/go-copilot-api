// Command copilot-api is a reverse proxy exposing GitHub Copilot's API
// as OpenAI and Anthropic compatible endpoints.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/amalgamated-tools/copilot-api-go/internal/auth"
	"github.com/amalgamated-tools/copilot-api-go/internal/config"
	"github.com/amalgamated-tools/copilot-api-go/internal/copilot"
	"github.com/amalgamated-tools/copilot-api-go/internal/models"
	"github.com/amalgamated-tools/copilot-api-go/internal/ratelimit"
	"github.com/amalgamated-tools/copilot-api-go/internal/server"
	"github.com/amalgamated-tools/copilot-api-go/internal/state"
	"github.com/amalgamated-tools/copilot-api-go/internal/token"
)

const version = "1.0.0"

const (
	keyError   = "error"
	keyVersion = "version"
	keyCount   = "count"
	keyID      = "id"
	keyVendor  = "vendor"
	keyURL     = "url"
	keyAddress = "address"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "start":
		runStart(args)
	case "auth":
		runAuth(args)
	case "logout":
		runLogout(args)
	case "check-usage":
		runCheckUsage(args)
	case "version", "--version", "-v":
		fmt.Printf("copilot-api-go v%s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`copilot-api-go - GitHub Copilot API proxy

Usage:
  copilot-api-go <command> [options]

Commands:
  start          Start the API proxy server
  auth           Run GitHub authentication flow
  logout         Remove stored GitHub token
  check-usage    Show Copilot quota information
  version        Show version

start options:
  --port, -p <n>          Port to listen on (default: 4141)
  --host, -H <addr>       Host to bind to (default: 127.0.0.1)
  --account-type, -a      Account type: individual|business|enterprise (default: individual)
  --github-token, -g <t>  Provide GitHub token directly
  --no-rate-limit         Disable adaptive rate limiting
  --no-auto-truncate      Disable auto-truncation
  --verbose               Enable verbose logging`)
}

// parseFlag finds --flag or -f value in args and returns it.
func parseFlag(args []string, long, short string, defaultVal string) string {
	longPrefix := "--" + long + "="
	shortPrefix := "-" + short + "="

	for i, a := range args {
		if (a == "--"+long || a == "-"+short) && i+1 < len(args) {
			return args[i+1]
		}
		if len(a) > len(longPrefix) && a[:len(longPrefix)] == longPrefix {
			return a[len(longPrefix):]
		}
		if len(a) > len(shortPrefix) && a[:len(shortPrefix)] == shortPrefix {
			return a[len(shortPrefix):]
		}
	}
	return defaultVal
}

// flagEnabled returns true unless --no-<name> is in args.
func flagEnabled(args []string, name string) bool {
	for _, a := range args {
		if a == "--no-"+name {
			return false
		}
	}
	return true
}

// flagPresent returns true if --<name> appears in args.
func flagPresent(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name {
			return true
		}
	}
	return false
}

// ============================================================================
// start
// ============================================================================

func runStart(args []string) {
	ctx := context.Background()
	portStr := parseFlag(args, "port", "p", "4141")
	host := parseFlag(args, "host", "H", "127.0.0.1")
	accountTypeStr := parseFlag(args, "account-type", "a", "individual")
	githubToken := parseFlag(args, "github-token", "g", "")
	verbose := flagPresent(args, "verbose")
	rateLimit := flagEnabled(args, "rate-limit")
	autoTruncate := flagEnabled(args, "auto-truncate")

	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 4141
	}

	var accountType state.AccountType
	switch accountTypeStr {
	case "business":
		accountType = state.AccountBusiness
	case "enterprise":
		accountType = state.AccountEnterprise
	default:
		accountType = state.AccountIndividual
	}

	// Configure log level based on verbose flag
	logLevel := slog.LevelInfo
	if verbose {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// Apply CLI flags to state
	state.SetCLIFlags(accountType, verbose, false, autoTruncate, rateLimit)

	// Load and apply config.yaml
	if err := config.EnsureDataDir(); err != nil {
		slog.WarnContext(ctx, "could not ensure data directory", slog.Any(keyError, err))
	}
	cfg, err := config.Load()
	if err != nil {
		slog.WarnContext(ctx, "could not load config.yaml", slog.Any(keyError, err))
	}

	// Apply config to state
	var overrides map[string]string
	if cfg.Model != nil && cfg.Model.ModelOverrides != nil {
		overrides = cfg.Model.ModelOverrides
	}
	state.ApplyConfigDefaults(
		cfg.StreamIdleTimeout,
		cfg.FetchTimeout,
		cfg.ShutdownGracefulWait,
		cfg.ShutdownAbortWait,
		cfg.HistoryLimit,
		cfg.HistoryMinEntries,
		cfg.StaleRequestMaxAge,
		cfg.ModelRefreshInterval,
		overrides,
	)

	// Initialize rate limiter
	if rateLimit {
		rlCfg := ratelimit.Config{
			BaseRetryIntervalSeconds:        10,
			ConsecutiveSuccessesForRecovery: 5,
		}
		if cfg.RateLimiter != nil {
			rlCfg.BaseRetryIntervalSeconds = cfg.RateLimiter.RetryInterval
			if cfg.RateLimiter.ConsecutiveSuccesses > 0 {
				rlCfg.ConsecutiveSuccessesForRecovery = cfg.RateLimiter.ConsecutiveSuccesses
			}
		}
		ratelimit.Init(rlCfg)
	}

	slog.InfoContext(ctx, "copilot-api-go", slog.String(keyVersion, version))

	// Fetch VSCode version
	vsCodeVersion, err := copilot.FetchVSCodeVersion()
	if err != nil {
		slog.WarnContext(ctx, "could not fetch VSCode version", slog.Any(keyError, err))
		vsCodeVersion = copilot.VSCodeVersionFallback
	}
	state.SetVSCodeVersion(vsCodeVersion)

	// Initialize token management
	if err := token.Init(token.InitOptions{CLIToken: githubToken}); err != nil {
		slog.ErrorContext(ctx, "Authentication failed", slog.Any(keyError, err))
		os.Exit(1)
	}

	// Fetch model catalog
	if err := models.Fetch(); err != nil {
		slog.ErrorContext(ctx, "Failed to fetch models", slog.Any(keyError, err))
		os.Exit(1)
	}

	resp := state.GetModels()
	if resp != nil {
		slog.InfoContext(ctx, "Available models", slog.Int(keyCount, len(resp.Data)))
		for _, m := range resp.Data {
			slog.DebugContext(ctx, "model",
				slog.String(keyID, m.ID),
				slog.String(keyVendor, m.Vendor),
			)
		}
	}

	// Start model refresh loop
	var modelRefreshInterval int
	state.WithRead(func(s *state.State) { modelRefreshInterval = s.ModelRefreshInterval })
	stopModelRefresh := models.StartRefreshLoop(modelRefreshInterval)
	defer stopModelRefresh()

	// Start server
	displayHost := host
	if displayHost == "" {
		displayHost = "localhost"
	}

	srv := server.New(server.Options{Port: port, Host: host})
	addr, err := server.ListenAndServe(srv)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to start server", slog.Any(keyError, err))
		os.Exit(1)
	}

	state.SetServerStartTime(time.Now().UnixMilli())
	slog.InfoContext(ctx, "Listening", slog.String(keyURL, fmt.Sprintf("http://%s:%d", displayHost, port)))
	slog.DebugContext(ctx, "bound to", slog.String(keyAddress, addr))

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.InfoContext(ctx, "Shutting down...")
	token.StopRefresh()

	var shutdownTimeout int
	state.WithRead(func(s *state.State) { shutdownTimeout = s.ShutdownGracefulWait })
	if shutdownTimeout <= 0 {
		shutdownTimeout = 60
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(shutdownTimeout)*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx, srv); err != nil {
		slog.ErrorContext(ctx, "Shutdown error", slog.Any(keyError, err))
	}
	slog.InfoContext(ctx, "Stopped.")
}

// ============================================================================
// auth
// ============================================================================

func runAuth(args []string) {
	ctx := context.Background()
	_ = args
	if err := config.EnsureDataDir(); err != nil {
		slog.ErrorContext(ctx, "Could not ensure data directory", slog.Any(keyError, err))
		os.Exit(1)
	}

	vsCodeVersion, _ := copilot.FetchVSCodeVersion()
	if vsCodeVersion == "" {
		vsCodeVersion = copilot.VSCodeVersionFallback
	}
	state.SetVSCodeVersion(vsCodeVersion)

	fmt.Println("Starting GitHub device authorization flow...")
	tok, err := auth.RunDeviceFlow()
	if err != nil {
		slog.ErrorContext(ctx, "Authentication failed", slog.Any(keyError, err))
		os.Exit(1)
	}

	state.SetGitHubToken(tok)

	// Validate and show user info
	user, err := token.GetGitHubUser()
	if err != nil {
		slog.WarnContext(ctx, "could not validate token", slog.Any(keyError, err))
	} else {
		fmt.Printf("Logged in as %s\n", user.Login)
	}

	fmt.Printf("GitHub token written to %s\n", config.TokenPath())
}

// ============================================================================
// logout
// ============================================================================

func runLogout(args []string) {
	ctx := context.Background()
	_ = args
	if err := auth.ClearToken(); err != nil {
		slog.ErrorContext(ctx, "Failed to clear token", slog.Any(keyError, err))
		os.Exit(1)
	}
	fmt.Println("Logged out. GitHub token removed.")
}

// ============================================================================
// check-usage
// ============================================================================

func runCheckUsage(args []string) {
	ctx := context.Background()
	_ = args
	if err := config.EnsureDataDir(); err != nil {
		slog.ErrorContext(ctx, "Could not ensure data directory", slog.Any(keyError, err))
		os.Exit(1)
	}

	vsCodeVersion, _ := copilot.FetchVSCodeVersion()
	if vsCodeVersion == "" {
		vsCodeVersion = copilot.VSCodeVersionFallback
	}
	state.SetVSCodeVersion(vsCodeVersion)

	// Load token from file/env
	tok, err := auth.LoadToken()
	if err != nil || tok == "" {
		fmt.Fprintln(os.Stderr, "No GitHub token found. Run `copilot-api-go auth` first.")
		os.Exit(1)
	}
	state.SetGitHubToken(tok)

	usage, err := token.GetCopilotUsage()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to get Copilot usage", slog.Any(keyError, err))
		os.Exit(1)
	}

	fmt.Printf("Plan: %s\n", usage.CopilotPlan)
	fmt.Printf("Quota reset: %s\n", usage.QuotaResetDate)
	fmt.Printf("\nChat:                %d remaining / %d total\n",
		usage.QuotaSnapshots.Chat.Remaining, usage.QuotaSnapshots.Chat.Entitlement)
	fmt.Printf("Completions:         %d remaining / %d total\n",
		usage.QuotaSnapshots.Completions.Remaining, usage.QuotaSnapshots.Completions.Entitlement)
	fmt.Printf("Premium interactions: %d remaining / %d total\n",
		usage.QuotaSnapshots.PremiumInteractions.Remaining, usage.QuotaSnapshots.PremiumInteractions.Entitlement)
}
