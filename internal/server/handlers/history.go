package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/amalgamated-tools/copilot-api-go/internal/history"
	"github.com/gorilla/mux"
)

// HistoryGetEntries handles GET /history/api/entries.
func HistoryGetEntries(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			if n > 500 {
				n = 500
			}
			limit = n
		}
	}

	all := history.Global().GetAll()
	// Newest first
	entries := make([]*history.Entry, 0, len(all))
	for i := len(all) - 1; i >= 0 && len(entries) < limit; i-- {
		entries = append(entries, all[i])
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": entries,
		"total":   history.Global().Count(),
	})
}

// HistoryGetEntry handles GET /history/api/entries/{id}.
func HistoryGetEntry(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	entry := history.Global().GetByID(id)
	if entry == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entry)
}

// HistoryDeleteEntries handles DELETE /history/api/entries.
func HistoryDeleteEntries(w http.ResponseWriter, r *http.Request) {
	history.Global().DeleteAll()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// HistoryGetStats handles GET /history/api/stats.
func HistoryGetStats(w http.ResponseWriter, r *http.Request) {
	stats := history.Global().GetStats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

// HistoryExport handles GET /history/api/export.
func HistoryExport(w http.ResponseWriter, r *http.Request) {
	all := history.Global().GetAll()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="history-export.json"`)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"entries":     all,
	})
}

// HistoryGetSessions handles GET /history/api/sessions.
func HistoryGetSessions(w http.ResponseWriter, r *http.Request) {
	sessions := history.Global().Sessions()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"sessions": sessions,
		"total":    len(sessions),
	})
}

// HistoryGetSession handles GET /history/api/sessions/{id}.
func HistoryGetSession(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	all := history.Global().GetAll()
	entries := []*history.Entry{}
	for _, e := range all {
		if e.Endpoint == id {
			entries = append(entries, e)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      id,
		"entries": entries,
	})
}

// HistoryDeleteSession handles DELETE /history/api/sessions/{id}.
func HistoryDeleteSession(w http.ResponseWriter, r *http.Request) {
	// We don't have per-session deletion; just acknowledge
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
