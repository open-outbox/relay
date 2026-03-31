
go install golang.org/x/tools/cmd/goimports@latest
go install github.com/segmentio/golines@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

brew install golangci-lint
brew install pre-commit

pre-commit install
pre-commit run --all-files