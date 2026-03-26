<div align="center">

# crated

**The runtime daemon for AI agents.**

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue?style=flat-square)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/agentcrate/crated/ci.yml?branch=main&style=flat-square&label=CI)](https://github.com/agentcrate/crated/actions)
[![codecov](https://codecov.io/gh/agentcrate/crated/graph/badge.svg)](https://codecov.io/gh/agentcrate/crated)
[![Go Report Card](https://goreportcard.com/badge/github.com/agentcrate/crated?style=flat-square)](https://goreportcard.com/report/github.com/agentcrate/crated)

The container entrypoint that powers AI agents built with [AgentCrate](https://agentcrate.ai).

[Getting Started](#getting-started) · [Documentation](https://agentcrate.ai) · [Contributing](CONTRIBUTING.md)

</div>

---

## What is crated?

`crated` is the runtime daemon that powers agent containers built by `crate build`. It reads an Agentfile, initializes model providers and MCP skill connections, and runs the Google ADK tool-calling loop.

When you build an agent with `crate build`, the resulting container image uses `crated` as its entrypoint. You can also run it directly for local development.

## Features

- **Multi-provider LLM support** -- Built-in providers for OpenAI, Anthropic (Claude), Google Gemini, and OpenAI-compatible APIs (Ollama, Azure OpenAI, vLLM)
- **Build-time config injection** -- API endpoints and auth are resolved from the registry at `crate build` time and injected via `.crate/runtime.json`, keeping the Agentfile clean
- **Runtime overrides** -- Deploy-time env vars (e.g., `OLLAMA_HOST`) override API endpoints without rebuilding
- **Environment profiles** -- Switch configurations with `--profile` flag or `CRATE_PROFILE` env var
- **Health check endpoints** -- HTTP liveness (`/healthz`) and readiness (`/readyz`) probes for Kubernetes
- **MCP skill connections** -- Connects to stdio, HTTP, and SSE MCP servers
- **Google ADK integration** -- Full tool-calling loop with the ADK agent framework
- **Signal handling** -- Graceful shutdown with tini as PID 1

## Getting Started

### Install

```bash
# From source
go install github.com/agentcrate/crated/cmd/crated@latest

# Docker (base image for agent containers)
docker pull ghcr.io/agentcrate/crated:latest
```

### Quick Start

```bash
# Run with default Agentfile in current directory
crated

# Specify Agentfile path and profile
crated --agentfile /agent/Agentfile --profile prod

# Via environment variable
CRATE_PROFILE=staging crated --agentfile /agent/Agentfile

# Point to a remote Ollama instance
OLLAMA_HOST=http://gpu-box:11434 crated --agentfile /agent/Agentfile
```

### CLI Flags

| Flag               | Default                | Description                                                 |
| ------------------ | ---------------------- | ----------------------------------------------------------- |
| `--agentfile`      | `Agentfile`            | Path to the Agentfile                                       |
| `--profile`        | *(none)*               | Environment profile to activate (overrides `CRATE_PROFILE`) |
| `--runtime-config` | `.crate/runtime.json`  | Path to the build-time runtime config                       |
| `--health-port`    | `8080`                 | Port for health check endpoints (`0` to disable)            |

### Health Check Endpoints

`crated` exposes HTTP health probes for container orchestrators:

| Endpoint       | Purpose   | When it returns 200                       |
| -------------- | --------- | ----------------------------------------- |
| `GET /healthz` | Liveness  | Immediately on startup                    |
| `GET /readyz`  | Readiness | After models and skills are initialized   |

**Kubernetes example:**

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
  initialDelaySeconds: 2
  periodSeconds: 10
readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 5
```

### Building the Base Image

```bash
docker build -t agentcrate/base:dev .
```

## Runtime Configuration

`crated` uses a two-layer configuration model:

| Layer              | File                   | Contains                                       | Set by        |
| ------------------ | ---------------------- | ---------------------------------------------- | ------------- |
| **Agentfile**      | `Agentfile`            | Models, persona, skills, tuning                | Developer     |
| **Runtime config** | `.crate/runtime.json`  | API endpoints, auth env vars, host overrides   | `crate build` |

The Agentfile defines *what* the agent is. The runtime config defines *how* to connect.

**Example `.crate/runtime.json`:**

```json
{
  "models": {
    "openai/gpt-4o": {
      "api_type": "openai",
      "api_base": "https://api.openai.com/v1",
      "auth_env_var": "OPENAI_API_KEY"
    },
    "ollama/mistral": {
      "api_type": "openai",
      "api_base": "http://localhost:11434/v1",
      "host_env_var": "OLLAMA_HOST"
    }
  }
}
```

### API Base URL Resolution

For each model, the API base URL is resolved in priority order:

1. **Runtime env var** (`host_env_var`, e.g., `OLLAMA_HOST`) -- deploy-time override
2. **Build-time config** (`api_base` from `runtime.json`)
3. **Provider default** (e.g., `https://api.openai.com/v1`)

## Building from Source

```bash
git clone https://github.com/agentcrate/crated.git
cd crated
make build

# Run tests
make test

# Run linter
make lint
```

### Requirements

- Go 1.25+
- Docker Engine 24+ (for building the base image)

## Architecture

`crated` is part of the AgentCrate ecosystem:

| Component | Description | License |
| --------- | ----------- | ------- |
| [`agentfile`](https://github.com/agentcrate/agentfile) | Agentfile v1 spec, types, parsing, and validation | Apache 2.0 |
| [`api`](https://github.com/agentcrate/api) | Protocol Buffer definitions for all AgentCrate services | Apache 2.0 |
| [`crate`](https://github.com/agentcrate/crate) | CLI for building, validating, and publishing agent images | Apache 2.0 |
| **`crated`** (this repo) | Agent runtime daemon (container entrypoint) | Apache 2.0 |

### Project Structure

```text
crated/
├── cmd/crated/              # CLI entrypoint
└── internal/
    ├── health/              # HTTP health check server (/healthz, /readyz)
    ├── runtimecfg/          # Build-time runtime config (runtime.json)
    └── runtime/
        ├── runtime.go       # Core execution engine
        ├── models.go        # Model provider registry + ConnectConfig
        └── providers/
            ├── openai/      # OpenAI & compatible APIs (Ollama, Azure, vLLM)
            ├── anthropic/   # Anthropic Claude models
            └── gemini/      # Google Gemini models
```

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Development Workflow

```bash
# Run tests with race detection
make test

# Run linter
make lint

# Build binary
make build
```

## Security

If you discover a security vulnerability, please report it responsibly. See [SECURITY.md](SECURITY.md) for details.

## License

Licensed under the [Apache License, Version 2.0](LICENSE).

Copyright 2026 AgentCrate Contributors.
