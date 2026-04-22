.PHONY: lint fmt hardfmt test testsum modernize

# Tooling commands
GOLANGCI_LINT_CMD = go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4

build:
	go build -ldflags "-X main.version=${VERSION}" -o copilot-api-go ./cmd/copilot-api

lint:
	$(GOLANGCI_LINT_CMD) run ./... --max-issues-per-linter 0 --max-same-issues 0

fmt:
	go fmt ./...

hardfmt:
	go tool gofumpt -w -l .

test:
	go test -v ./...

testsum:
	gotestsum -- -v ./...

modernize:
	go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest -fix  ./...
