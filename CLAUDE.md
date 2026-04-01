# Crated — Agent Runtime

Go agent runtime that executes Agentfile-defined agents with multi-provider LLM support, MCP skills, and pluggable frontends. Runs inside Docker containers.

## Build & Dev

All commands go through `make`:

| Target | Purpose |
|--------|---------|
| `make build` | Build to `bin/crated` (with version ldflags) |
| `make install` | Install to `$GOPATH/bin` |
| `make test` | Tests with `-race` and verbose |
| `make test-short` | Tests with `-race` (no verbose) |
| `make lint` | golangci-lint |
| `make coverage` | Coverage report |

Never run `go test` directly — always use `make`.

## Architecture

```text
cmd/crated/main.go           Entrypoint, signal handling, provider imports

internal/
  runtime/
    runtime.go               Core engine (Init, Reload, Close)
    models.go                Provider registry + model creation
    middleware/              Logging + rate limiting decorators
    providers/
      openai/                OpenAI + compatible APIs (Ollama, vLLM)
      anthropic/             Claude models
      gemini/                Google Gemini (ADK SDK)
  runtimecfg/                .crate/runtime.json loader
  httpclient/                HTTP client with retries, timeouts, body limits
  ratelimit/                 Semaphore-based concurrent request limiter
  sse/                       Server-Sent Events parser
  health/                    HTTP health probes (/healthz, /readyz, /metrics)
  frontend/
    frontend.go              Frontend interface + AgentBridge
    repl/                    Console REPL frontend
    playground/              Web UI frontend
```

## Startup Pipeline

```text
Health server → Parse Agentfile → Resolve profile → Load runtime.json
  → Init providers → Connect skills (MCP) → Build ADK agent
  → MarkReady() → Start frontend
```

## Provider Registration

Providers register via side-effect imports in `main.go`:

```go
import (
    _ "github.com/agentcrate/crated/internal/runtime/providers/anthropic"
    _ "github.com/agentcrate/crated/internal/runtime/providers/openai"
    _ "github.com/agentcrate/crated/internal/runtime/providers/gemini"
)

// Each provider's init() calls:
func init() { runtime.RegisterProvider(&provider{}) }
```

## API Key Resolution (priority order)

1. Runtime env var (`host_env_var`, e.g., `OLLAMA_HOST`)
2. Build-time config (`api_base` from runtime.json)
3. Provider default (e.g., `https://api.openai.com/v1`)

## Signal Handling

- **SIGINT/SIGTERM (1st)** → Graceful shutdown (cancel context, drain)
- **SIGINT/SIGTERM (2nd)** → Force exit
- **SIGHUP** → Hot-reload persona/brain config only (skills require restart)

## Error Categories

- **Fatal** (blocks Init): default model missing, runtime config parse error
- **Warnings** (logged, continues): non-default model init failure
- **Early validation**: missing env vars checked before connecting
- Fail fast at startup, not on first message

## Logging

- **`log/slog`** (stdlib) — auto-selects text (REPL) or JSON (service)
- Per-component loggers: `slog.Default().With("component", "runtime")`
- Middleware logs: streaming mode, duration, token counts, tool calls

## Testing

- `saveAndRestoreProviders(t)` isolates the global provider registry per test
- `stubProvider` / `stubModel` for unit tests
- Mock HTTP servers via `httptest.NewServer` for provider testing
- White-box tests access internals, black-box tests use public API

## Security

- **Zero secrets in artifacts** — API keys come from env vars only, never in Agentfile
- **Non-root** container user (`agent`)
- **tini** as PID 1 for signal forwarding
- **Response body limit** 10 MB (prevents OOM)
- **Request timeout** 120s per provider call
- **Semaphore rate limiting** per model (default 10, 1 for local Ollama)

## Anti-Patterns

- **Never** put API keys in Agentfile or runtime.json — env vars only
- **Never** add a new provider without registering via `init()` + side-effect import
- **Never** skip early env var validation — fail at startup, not on first request
- **Never** add a new frontend without implementing the `Frontend` interface
- **Never** modify skill connections during hot-reload — they require a full restart
- **Never** use `panic()` in runtime code

## Design-First

For new providers, new frontends, or changes to the agent lifecycle: outline the integration points, config surface, and failure modes before implementing.

## Key References

- Architecture: `ARCHITECTURE.md` in this repo
- Security policy: `SECURITY.md`
- Agentfile spec: `github.com/agentcrate/agentfile`
- ADK framework: `google.golang.org/adk`
- MCP SDK: `github.com/modelcontextprotocol/go-sdk`
