# copilot-api-go

A Go port of the Copilot API proxy, exposing GitHub Copilot's API as standard OpenAI and Anthropic compatible endpoints.

## Build

```bash
go build -o copilot-api ./cmd/copilot-api/
```

## Usage

```bash
# Start the server (authenticates if needed)
./copilot-api start --port 4141

# Run GitHub authentication flow only
./copilot-api auth

# Remove stored GitHub token
./copilot-api logout

# Show Copilot quota
./copilot-api check-usage
```

### `start` Options

| Option | Default | Description |
|--------|---------|-------------|
| `--port`, `-p` | 4141 | Port to listen on |
| `--host`, `-H` | (all interfaces) | Host to bind to |
| `--account-type`, `-a` | individual | `individual`, `business`, or `enterprise` |
| `--github-token`, `-g` | | Provide GitHub token directly |
| `--no-rate-limit` | | Disable adaptive rate limiting |
| `--no-auto-truncate` | | Disable auto-truncation |
| `--verbose` | | Enable verbose logging |

## API Endpoints

All routes from the TypeScript version are implemented:

### OpenAI Compatible

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | Chat completions |
| `/v1/responses` | POST | Responses API |
| `/v1/models` | GET | List available models |
| `/v1/models/:model` | GET | Get specific model |
| `/v1/embeddings` | POST | Text embeddings |

All endpoints also work without the `/v1` prefix.

### Anthropic Compatible

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/messages` | POST | Messages API |
| `/v1/messages/count_tokens` | POST | Token counting |

### Utility

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/api/status` | GET | Server status |
| `/api/tokens` | GET | Token info |
| `/api/config` | GET | Current config |
| `/api/logs` | GET | Recent log entries |
| `/api/event_logging/batch` | POST | Event logging (no-op) |
| `/history/api/entries` | GET | History entries |
| `/history/api/entries/:id` | GET | Single entry |
| `/history/api/entries` | DELETE | Delete all entries |
| `/history/api/stats` | GET | Usage statistics |
| `/history/api/export` | GET | Export history |
| `/history/api/sessions` | GET | List sessions |
| `/history/api/sessions/:id` | GET | Session entries |
| `/history/api/sessions/:id` | DELETE | Delete session |
| `/ws` | WebSocket | Real-time updates |

## Configuration

Create `~/.local/share/copilot-api/config.yaml`. See the parent project's `config.example.yaml` for available options.

Key config fields recognized by the Go port:

```yaml
stream_idle_timeout: 300
fetch_timeout: 300
shutdown_graceful_wait: 60
history_limit: 200

rate_limiter:
  retry_interval: 10
  request_interval: 10
  recovery_timeout: 10
  consecutive_successes: 5

model:
  model_overrides:
    opus: claude-opus-4.6
    sonnet: claude-sonnet-4.6
```

## Project Structure

```
go/
├── cmd/copilot-api/main.go         # Entry point + CLI commands
├── internal/
│   ├── auth/device.go              # GitHub OAuth device flow + file storage
│   ├── config/config.go            # YAML config loading
│   ├── copilot/client.go           # Copilot API headers and helpers
│   ├── history/history.go          # In-memory request history
│   ├── models/models.go            # Model catalog + name resolver
│   ├── ratelimit/limiter.go        # Adaptive rate limiter
│   ├── server/
│   │   ├── server.go               # HTTP server + router
│   │   └── handlers/
│   │       ├── handlers.go         # All route handlers
│   │       ├── history.go          # History API handlers
│   │       └── websocket.go        # WebSocket handler
│   ├── state/state.go              # Thread-safe global state
│   └── token/token.go              # Token management (GitHub + Copilot)
├── go.mod
└── go.sum
```
