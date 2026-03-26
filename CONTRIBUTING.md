# Contributing to crated

Thank you for your interest in contributing to crated! This document provides guidelines and instructions for contributing.

## Code of Conduct

This project adheres to the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code.

## How to Contribute

### Reporting Bugs

Before creating a bug report, please check existing issues to avoid duplicates.

When filing a bug report, include:

- **crated version** (binary version or Docker image tag)
- **OS and architecture** (e.g., macOS arm64, Linux amd64)
- **Docker version** (`docker --version`), if relevant
- **Steps to reproduce** the issue
- **Expected behavior** vs. **actual behavior**
- **Agentfile content** (sanitized of secrets), if relevant

### Suggesting Features

Feature requests are welcome! Please open an issue with:

- A clear description of the feature
- The use case it addresses
- Any proposed implementation approach

### Pull Requests

1. **Fork** the repository
2. **Create a branch** from `main`: `git checkout -b feat/my-feature`
3. **Make your changes** following the coding standards below
4. **Add tests** for any new functionality
5. **Run the test suite**: `make test`
6. **Run the linter**: `make lint`
7. **Commit** using conventional commit messages
8. **Push** and open a Pull Request

## Development Setup

### Prerequisites

- Go 1.25+
- Docker Engine 24+ (for runtime tests)
- [golangci-lint](https://golangci-lint.run/) (for linting)

### Getting Started

```bash
git clone https://github.com/agentcrate/crated.git
cd crated
make build
make test
```

## Coding Standards

### Go Code

- Follow [Effective Go](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- Use `gofmt` / `goimports` for formatting
- All exported functions must have doc comments
- Error variables use `Err` prefix: `ErrProviderNotFound`
- Interfaces use noun or `-er` pattern: `ModelProvider`
- Tests are co-located: `foo.go` -> `foo_test.go`

### Commit Messages

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```text
type(scope): description

feat(runtime): add OpenAI provider
fix(models): handle missing API key gracefully
docs(readme): add Docker build instructions
test(gemini): add provider registration tests
chore(deps): bump ADK to v0.5.0
```

Types: `feat`, `fix`, `docs`, `test`, `chore`, `refactor`, `perf`, `ci`

### Branch Naming

- `feat/description` for features
- `fix/description` for bug fixes
- `docs/description` for documentation
- `refactor/description` for refactoring

## Testing

- All new code must have accompanying tests
- Tests must pass with race detection: `go test -race ./...`
- Aim for meaningful coverage, not 100% line coverage
- Use table-driven tests where appropriate
- Mock external dependencies (model APIs, MCP servers)

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
