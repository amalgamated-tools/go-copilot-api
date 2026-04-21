package handlers

import (
	"log/slog"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// wsCheckOrigin allows same-host connections and localhost origins.
func wsCheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || u.Host == r.Host
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: wsCheckOrigin,
}

// WebSocket handles the /ws WebSocket endpoint for real-time history updates.
func WebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// Send a connected message
	err = conn.WriteJSON(map[string]interface{}{
		"type": "connected",
		"data": map[string]interface{}{},
	})
	if err != nil {
		return
	}

	// Keep connection alive and handle ping/pong
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}
