// Package server creates and configures the HTTP server with all routes.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"

	"github.com/gorilla/mux"

	"github.com/amalgamated-tools/copilot-api-go/internal/server/handlers"
	"github.com/amalgamated-tools/copilot-api-go/internal/token"
)

// Options configures the HTTP server.
type Options struct {
	Port int
	Host string
}

// New creates a fully configured HTTP server.
func New(opts Options) *http.Server {
	r := mux.NewRouter()

	// Middleware
	r.Use(corsMiddleware)
	r.Use(ensureTokenMiddleware)

	// Root
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Server running"))
	}).Methods("GET")

	// Health
	r.HandleFunc("/health", handlers.Health).Methods("GET")

	// Silence browser probes
	r.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	r.HandleFunc("/.well-known/appspecific/com.chrome.devtools.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// ── OpenAI Chat Completions ───────────────────────────────────────────────
	r.HandleFunc("/chat/completions", handlers.ChatCompletions).Methods("POST")
	r.HandleFunc("/v1/chat/completions", handlers.ChatCompletions).Methods("POST")

	// ── OpenAI Responses API ─────────────────────────────────────────────────
	r.HandleFunc("/responses", handlers.Responses).Methods("POST")
	r.HandleFunc("/v1/responses", handlers.Responses).Methods("POST")

	// ── OpenAI Models ─────────────────────────────────────────────────────────
	r.HandleFunc("/models", handlers.ListModels).Methods("GET")
	r.HandleFunc("/v1/models", handlers.ListModels).Methods("GET")
	// Note: must register the parameterized route AFTER the plain /models route
	r.HandleFunc("/models/{model}", handlers.GetModel).Methods("GET")
	r.HandleFunc("/v1/models/{model}", handlers.GetModel).Methods("GET")

	// ── OpenAI Embeddings ─────────────────────────────────────────────────────
	r.HandleFunc("/embeddings", handlers.Embeddings).Methods("POST")
	r.HandleFunc("/v1/embeddings", handlers.Embeddings).Methods("POST")

	// ── Anthropic Messages ───────────────────────────────────────────────────
	r.HandleFunc("/v1/messages", handlers.Messages).Methods("POST")
	r.HandleFunc("/v1/messages/count_tokens", handlers.CountTokens).Methods("POST")

	// ── Anthropic Event Logging ──────────────────────────────────────────────
	r.HandleFunc("/api/event_logging/batch", handlers.EventLoggingBatch).Methods("POST")

	// ── Management API ───────────────────────────────────────────────────────
	r.HandleFunc("/api/status", handlers.Status).Methods("GET")
	r.HandleFunc("/api/tokens", handlers.TokenInfo).Methods("GET")
	r.HandleFunc("/api/config", handlers.Config).Methods("GET")
	r.HandleFunc("/api/logs", handlers.Logs).Methods("GET")

	// ── History API ───────────────────────────────────────────────────────────
	r.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui", http.StatusMovedPermanently)
	}).Methods("GET")
	r.HandleFunc("/history/api/entries", handlers.HistoryGetEntries).Methods("GET")
	r.HandleFunc("/history/api/entries/{id}", handlers.HistoryGetEntry).Methods("GET")
	r.HandleFunc("/history/api/entries", handlers.HistoryDeleteEntries).Methods("DELETE")
	r.HandleFunc("/history/api/stats", handlers.HistoryGetStats).Methods("GET")
	r.HandleFunc("/history/api/export", handlers.HistoryExport).Methods("GET")
	r.HandleFunc("/history/api/sessions", handlers.HistoryGetSessions).Methods("GET")
	r.HandleFunc("/history/api/sessions/{id}", handlers.HistoryGetSession).Methods("GET")
	r.HandleFunc("/history/api/sessions/{id}", handlers.HistoryDeleteSession).Methods("DELETE")

	// ── WebSocket ─────────────────────────────────────────────────────────────
	r.HandleFunc("/ws", handlers.WebSocket)
	r.HandleFunc("/history/ws", handlers.WebSocket)

	// ── UI placeholder ────────────────────────────────────────────────────────
	r.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html><html><body><h1>Copilot API Go</h1><p>History UI not available in the Go build.</p></body></html>`))
	}).Methods("GET")

	// 404 handler
	r.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Not Found"})
	})

	addr := fmt.Sprintf("%s:%d", opts.Host, opts.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}
	return srv
}

// ListenAndServe starts the HTTP server and returns the listening address.
func ListenAndServe(srv *http.Server) (string, error) {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
		}
	}()
	return addr, nil
}

// Shutdown gracefully stops the server.
func Shutdown(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}

// isLocalOrigin returns true if the origin host is localhost or loopback.
func isLocalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// corsMiddleware adds CORS headers to responses from local origins only.
// Non-browser clients (no Origin header) pass through without CORS headers.
// Requests from non-local origins do not receive ACAO headers, causing browsers to block them.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && isLocalOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, anthropic-version, anthropic-beta")
			w.Header().Add("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ensureTokenMiddleware proactively refreshes the Copilot token before requests.
func ensureTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip token check for paths that don't need a valid Copilot token
		path := r.URL.Path
		if path == "/" || path == "/health" || path == "/favicon.ico" {
			next.ServeHTTP(w, r)
			return
		}
		// Best-effort token refresh; do not block the request on failure
		if err := token.EnsureValidCopilotToken(); err != nil {
			// Log but continue — the handler will return an appropriate error if token is missing
			slog.Warn("token refresh warning", "error", err)
		}
		next.ServeHTTP(w, r)
	})
}
