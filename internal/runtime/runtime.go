// Package runtime implements the agent execution engine.
//
// The runtime reads a parsed Agentfile, configures model backends and
// MCP skill connections, then runs the ADK tool-calling loop to serve
// user interactions. It is the binary that powers `crate run` and the
// container entrypoint for built agent images.
package runtime //nolint:revive // internal package; name clash with stdlib is acceptable

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/runtimecfg"
)

// Runtime is the agent execution engine. It owns the model connection,
// MCP skill connections, and the ADK agent that orchestrates them.
type Runtime struct {
	af       *agentfile.Agentfile
	rc       *runtimecfg.Config
	agent    agent.Agent
	models   *ModelRegistry
	toolsets []tool.Toolset
	closers  []func() // cleanup functions for stdio processes, etc.
	logger   *slog.Logger
	mu       sync.RWMutex // protects agent for hot-reload
}

// New creates a Runtime from a parsed Agentfile and optional runtime config.
// The runtime config provides build-time resolved connection details.
// If rc is nil, an empty config is used (providers fall back to defaults).
// It does NOT start the agent — call Init() then Run() for that.
func New(af *agentfile.Agentfile, rc *runtimecfg.Config) *Runtime {
	if rc == nil {
		rc = &runtimecfg.Config{Models: make(map[string]runtimecfg.ModelConnection)}
	}
	return &Runtime{
		af:     af,
		rc:     rc,
		logger: slog.Default().With("component", "runtime"),
	}
}

// Init configures the model backend and connects to all declared skills.
// Must be called before Run().
func (r *Runtime) Init(ctx context.Context) error {
	// 1. Create models (with graceful degradation for non-default models).
	models, warnings, err := NewModelRegistry(ctx, r.af.Brain, r.rc)
	if err != nil {
		return fmt.Errorf("initializing models: %w", err)
	}
	for _, w := range warnings {
		r.logger.Warn("model init warning", "message", w)
	}
	r.models = models

	// 2. Connect to each skill based on its type.
	for i := range r.af.Skills {
		skill := &r.af.Skills[i]
		// Validate required environment variables before connecting.
		if err := checkSkillEnv(skill); err != nil {
			return fmt.Errorf("skill %q: %w", skill.Name, err)
		}
		r.logger.Info("connecting skill", "name", skill.Name, "type", skill.Type)
		ts, closer, err := connectSkill(ctx, skill)
		if err != nil {
			r.Close() // best-effort cleanup of already-opened connections
			return fmt.Errorf("connecting skill %q: %w", skill.Name, err)
		}
		r.toolsets = append(r.toolsets, ts)
		if closer != nil {
			r.closers = append(r.closers, closer)
		}

		// Eagerly initialize the MCP session. The ADK defers this to the
		// first LLM request, but we want startup errors to surface early
		// and the first user message to be fast.
		if _, err := ts.Tools(preloadContext{ctx}); err != nil {
			r.Close()
			return fmt.Errorf("preloading skill %q tools: %w", skill.Name, err)
		}
		r.logger.Info("skill ready", "name", skill.Name)
	}

	// 3. Get the default model for the agent.
	defaultModel, err := r.models.Default()
	if err != nil {
		r.Close()
		return fmt.Errorf("resolving default model: %w", err)
	}

	// 4. Build the ADK agent.
	instruction := r.af.Persona.SystemPrompt
	a, err := llmagent.New(llmagent.Config{
		Name:        r.af.Metadata.Name,
		Description: r.af.Metadata.Description,
		Model:       defaultModel,
		Instruction: instruction,
		Toolsets:    r.toolsets,
	})
	if err != nil {
		r.Close()
		return fmt.Errorf("creating agent: %w", err)
	}
	r.agent = a

	r.logger.Info("runtime initialized",
		"agent", r.af.Metadata.Name,
		"models", len(r.models.models),
		"skills", len(r.toolsets),
	)

	return nil
}

// Agent returns the configured ADK agent. Must be called after Init().
// Safe for concurrent use during hot-reload.
func (r *Runtime) Agent() agent.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agent
}

// Agentfile returns the runtime's Agentfile.
func (r *Runtime) Agentfile() *agentfile.Agentfile {
	return r.af
}

// Models returns the model registry. Must be called after Init().
func (r *Runtime) Models() *ModelRegistry {
	return r.models
}

// Reload updates the runtime with a new Agentfile. It recreates the ADK
// agent with the new persona/brain config while keeping existing skill
// connections alive. This is called on SIGHUP for dev hot-reload.
//
// Only persona and brain changes are safe to reload — skill or build
// changes require a full container restart.
func (r *Runtime) Reload(ctx context.Context, newAF *agentfile.Agentfile) error {
	r.logger.Info("reloading agent config",
		"old_prompt_len", len(r.af.Persona.SystemPrompt),
		"new_prompt_len", len(newAF.Persona.SystemPrompt),
	)

	// Resolve the default model. If brain config changed, try the new default.
	defaultModel, err := r.models.Default()
	if err != nil {
		return fmt.Errorf("resolving default model: %w", err)
	}

	// Recreate the agent with the new instruction but existing toolsets.
	a, err := llmagent.New(llmagent.Config{
		Name:        newAF.Metadata.Name,
		Description: newAF.Metadata.Description,
		Model:       defaultModel,
		Instruction: newAF.Persona.SystemPrompt,
		Toolsets:    r.toolsets,
	})
	if err != nil {
		return fmt.Errorf("creating reloaded agent: %w", err)
	}

	// Swap the agent atomically.
	r.mu.Lock()
	r.agent = a
	r.af = newAF
	r.mu.Unlock()

	r.logger.Info("agent config reloaded",
		"agent", newAF.Metadata.Name,
	)

	return nil
}

// Close shuts down all skill connections and subprocess handles.
func (r *Runtime) Close() {
	for _, fn := range r.closers {
		fn()
	}
	r.closers = nil
}

// connectSkill creates an MCP toolset for a single skill based on its type.
// Returns the toolset, an optional closer function, and any error.
func connectSkill(ctx context.Context, skill *agentfile.Skill) (tool.Toolset, func(), error) {
	var transport mcp.Transport

	switch skill.Type {
	case "stdio":
		// Launch the tool as a subprocess and communicate over stdin/stdout.
		cmd := exec.CommandContext(ctx, skill.Command, skill.Args...)
		transport = &mcp.CommandTransport{Command: cmd}

	case "http":
		// Streamable HTTP transport (MCP over HTTP with optional streaming).
		transport = &mcp.StreamableClientTransport{
			Endpoint: skill.Source,
		}

	case "sse":
		// Server-Sent Events transport (legacy MCP transport).
		transport = &mcp.SSEClientTransport{
			Endpoint: skill.Source,
		}

	case "mcp":
		return nil, nil, fmt.Errorf("mcp registry skills must be resolved at build time, not runtime (skill %q source: %s)", skill.Name, skill.Source)

	default:
		return nil, nil, fmt.Errorf("unknown skill type %q", skill.Type)
	}

	ts, err := mcptoolset.New(mcptoolset.Config{
		Transport: transport,
	})
	if err != nil {
		return nil, nil, err
	}

	return ts, nil, nil
}

// preloadContext is a minimal agent.ReadonlyContext used to eagerly
// initialize MCP sessions at startup. The ADK's Tools() method requires
// this interface, but only uses the embedded context.Context.
type preloadContext struct {
	context.Context
}

//nolint:revive // interface stub — only context.Context is used at runtime.
func (preloadContext) UserContent() *genai.Content { return nil }

//nolint:revive // interface stub.
func (preloadContext) InvocationID() string { return "" }

//nolint:revive // interface stub.
func (preloadContext) AgentName() string { return "" }

//nolint:revive // interface stub.
func (preloadContext) ReadonlyState() session.ReadonlyState { return nil }

//nolint:revive // interface stub.
func (preloadContext) UserID() string { return "" }

//nolint:revive // interface stub.
func (preloadContext) AppName() string { return "" }

//nolint:revive // interface stub.
func (preloadContext) SessionID() string { return "" }

//nolint:revive // interface stub.
func (preloadContext) Branch() string { return "" }

// checkSkillEnv validates that all environment variables declared in
// skill.Env are set. Returns a clear error listing any missing vars.
func checkSkillEnv(skill *agentfile.Skill) error {
	var missing []string
	for _, env := range skill.Env {
		if os.Getenv(env) == "" {
			missing = append(missing, env)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}
