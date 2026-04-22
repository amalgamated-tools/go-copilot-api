.PHONY: lint fmt hardfmt test testsum modernize

# Tooling commands
GOLANGCI_LINT_CMD = go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@1c222b488bbc2c0ae2cad8423a24b8452f2fc3a9

lint:
	$(GOLANGCI_LINT_CMD) run ./... --max-issues-per-linter 0 --max-same-issues 0
	go run ./cmd/slogcheck ./...
	go run ./cmd/errorfcheck ./...

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
