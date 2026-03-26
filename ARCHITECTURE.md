# Architecture

This document describes the internal architecture of `crated`, the agent runtime daemon.

## System Overview

```text
┌──────────────────────────────────────────────────────┐
│                     crated                           │
│                                                      │
│  ┌──────────┐    ┌──────────┐    ┌───────────────┐   │
│  │ Frontend │◄──►│  Agent   │◄──►│ Model         │   │
│  │ (REPL)   │    │  Bridge  │    │ Registry      │   │
│  └──────────┘    └────┬─────┘    └───┬───────────┘   │
│                       │              │               │
│                  ┌────▼─────┐   ┌────▼────────────┐  │
│                  │ ADK      │   │ Providers       │  │
│                  │ Runner   │   │ ┌─────────────┐ │  │
│                  └────┬─────┘   │ │ OpenAI      │ │  │
│                       │         │ │ Anthropic   │ │  │
│                  ┌────▼─────┐   │ │ Gemini      │ │  │
│                  │ MCP      │   │ └─────────────┘ │  │
│                  │ Toolsets  │   └─────────────────┘  │
│                  └──────────┘                        │
│                                                      │
│  ┌──────────┐    ┌──────────┐    ┌───────────────┐   │
│  │ Health   │    │ Runtime  │    │ HTTP Client   │   │
│  │ Server   │    │ Config   │    │ + SSE Reader  │   │
│  └──────────┘    └──────────┘    └───────────────┘   │
└──────────────────────────────────────────────────────┘
```

## Package Map

| Package | Lines | Purpose |
|---|---|---|
| `cmd/crated` | 280 | CLI entrypoint, signal handling, SIGHUP hot-reload |
| `internal/runtime` | 482 | Core engine: model registry, provider interface, skill connections |
| `internal/runtime/middleware` | 142 | Model decorators: logging, rate limiting |
| `internal/runtime/providers/openai` | 556 | OpenAI + compatible APIs (Ollama, Azure, vLLM) |
| `internal/runtime/providers/anthropic` | 562 | Anthropic Claude models |
| `internal/runtime/providers/gemini` | 102 | Google Gemini (delegates to ADK SDK) |
| `internal/frontend` | 220 | Pluggable UI interface, AgentBridge, registry |
| `internal/frontend/repl` | 115 | Interactive console frontend |
| `internal/health` | 170 | HTTP health probes (`/healthz`, `/readyz`, `/metrics`) |
| `internal/httpclient` | 200 | HTTP client with retries, backoff, body limits |
| `internal/ratelimit` | 81 | Semaphore-based concurrency limiter |
| `internal/runtimecfg` | 98 | Build-time config loader (`.crate/runtime.json`) |
| `internal/sse` | 86 | Server-Sent Events stream parser |

## Key Design Decisions

### Provider Registration Pattern

Providers self-register via `init()` side-effect imports. The main package imports them:

```go
import (
    _ "github.com/agentcrate/crated/internal/runtime/providers/openai"
    _ "github.com/agentcrate/crated/internal/runtime/providers/anthropic"
    _ "github.com/agentcrate/crated/internal/runtime/providers/gemini"
)
```

Each provider calls `runtime.RegisterProvider()` in its `init()`. This keeps the runtime package decoupled from specific providers and allows new providers to be added without modifying the core.

### Two-Layer Configuration

| Layer | File | Set by | Contains |
|---|---|---|---|
| **Agentfile** | `Agentfile` | Developer | Models, persona, skills, tuning params |
| **Runtime config** | `.crate/runtime.json` | `crate build` | API endpoints, auth env vars, host overrides |

The Agentfile defines *what* the agent is. The runtime config defines *how* to connect. This separation allows the same Agentfile to run against different backends (cloud, local, staging) without modification.

### API Base URL Resolution

For each model, the base URL resolves in priority order:

1. **Runtime env var** (`host_env_var`, e.g., `OLLAMA_HOST`) — deploy-time override
2. **Build-time config** (`api_base` from `runtime.json`)
3. **Provider default** (e.g., `https://api.openai.com/v1`)

### Model Middleware Chain

Every model is wrapped in a middleware chain:

```text
Rate Limiter → Logger → Provider Model
```

- **Rate Limiter**: Semaphore-based per-model concurrency cap (default: 10)
- **Logger**: Structured `slog` output with token counts, tool calls, and duration

### Graceful Degradation

When initializing models, the **default** model failure is fatal. Non-default model failures produce warnings but don't block startup. This allows agents to start even if an optional model (e.g., a specialized summarizer) is temporarily unavailable.

### Frontend Architecture

Frontends implement a simple interface:

```go
type Frontend interface {
    Name() string
    Run(ctx context.Context, bridge *AgentBridge) error
}
```

The `AgentBridge` wraps the ADK runner and session management, so frontends never deal with ADK internals. Current implementations: `repl` (console), `playground` (web UI).

### Signal Handling

| Signal | Behavior |
|---|---|
| `SIGINT` / `SIGTERM` (first) | Graceful shutdown: close skills, drain connections |
| `SIGINT` / `SIGTERM` (second) | Force exit |
| `SIGHUP` | Hot-reload: re-parse Agentfile, rebuild agent with new persona/brain, keep skill connections alive |

### Health Probes

| Endpoint | Type | Returns 200 |
|---|---|---|
| `GET /healthz` | Liveness | Always (process is alive) |
| `GET /readyz` | Readiness | After models + skills initialized |
| `GET /metrics` | Diagnostics | Always (uptime, heap, goroutines, GC) |

### Logging

`slog` throughout with auto-format detection:

- **Text format**: when stdout is a TTY (REPL mode)
- **JSON format**: when stdout is not a TTY (container/service mode)

Per-component loggers via `slog.With("component", "...")` for filtering.

## Container Architecture

The Dockerfile produces a multi-stage, multi-arch base image:

```text
Stage 1 (builder): golang:1.25-alpine → compile crated binary
Stage 2 (runtime): alpine:3.21 → crated + Node.js (npx) + Python (uvx)
```

Key decisions:
- **tini** as PID 1 for proper signal forwarding
- **Non-root `agent` user** for security
- **Node.js + Python** pre-installed for stdio MCP tools (npx/uvx)
- **HEALTHCHECK** directive for Docker Compose compatibility

## Adding a New Provider

1. Create `internal/runtime/providers/yourprovider/yourprovider.go`
2. Implement `runtime.ModelProvider` interface
3. Call `runtime.RegisterProvider()` in `init()`
4. Add side-effect import to `cmd/crated/main.go`
5. Add scope `yourprovider` to `.github/workflows/pr-title.yml`

## Adding a New Frontend

1. Create `internal/frontend/yourfrontend/yourfrontend.go`
2. Implement `frontend.Frontend` interface
3. Call `frontend.RegisterFrontend()` in `init()`
4. Add side-effect import to `cmd/crated/main.go`
