go install golang.org/x/tools/cmd/goimports@latest
brew install golangci-lint
brew install pre-commit

pre-commit install
pre-commit run --all-files