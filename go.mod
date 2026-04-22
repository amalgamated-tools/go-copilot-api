module github.com/amalgamated-tools/copilot-api-go

go 1.26.2

require (
	github.com/gorilla/mux v1.8.1
	github.com/gorilla/websocket v1.5.3
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/go-cmp v0.7.0 // indirect
	golang.org/x/mod v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/telemetry v0.0.0-20260409153401-be6f6cb8b1fa // indirect
	golang.org/x/tools v0.44.0 // indirect
	golang.org/x/vuln v1.2.0 // indirect
	mvdan.cc/gofumpt v0.9.2 // indirect
)

tool (
	golang.org/x/vuln/cmd/govulncheck
	mvdan.cc/gofumpt
)
