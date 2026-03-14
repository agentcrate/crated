// Package frontend defines the pluggable interface for agent user interfaces.
//
// The agent runtime is decoupled from how users interact with it. A Frontend
// is any component that can deliver user messages to the agent and present
// responses back. Implementations include the built-in REPL (console),
// and future adapters for HTTP/WebSocket APIs, Slack, Discord, Telegram, etc.
//
// # Architecture
//
// The flow is:
//
//	User <--[Frontend]--> AgentBridge <--[ADK Runner]--> LLM Provider
//
// The AgentBridge wraps the ADK runner and session management, exposing a
// simple Chat() method that frontends use to exchange messages. Frontends
// never need to know about ADK internals, sessions, or runner configuration.
//
// # Implementing a Frontend
//
// Implement the Frontend interface and register it via RegisterFrontend():
//
//	func init() {
//	    frontend.RegisterFrontend(&myFrontend{})
//	}
//
//	type myFrontend struct{}
//	func (f *myFrontend) Name() string { return "slack" }
//	func (f *myFrontend) Run(ctx context.Context, bridge *frontend.AgentBridge) error { ... }
package frontend

import (
	"context"
	"fmt"
	"iter"
	"sync"

	"google.golang.org/genai"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
)

// Frontend is the pluggable user interface for the agent.
// Each implementation handles user interaction for a specific channel.
type Frontend interface {
	// Name returns the frontend identifier (e.g., "repl", "http", "slack").
	Name() string

	// Run starts the frontend and blocks until shutdown.
	// The context is canceled on graceful shutdown (Ctrl+C, SIGTERM, etc.).
	// The AgentBridge provides message exchange with the running agent.
	Run(ctx context.Context, bridge *AgentBridge) error
}

// Usage tracks token counts for a response turn.
type Usage struct {
	PromptTokens     int32
	CompletionTokens int32
	TotalTokens      int32
}

// Event represents a response chunk from the agent.
type Event struct {
	// Text is the text content of this chunk (may be partial when streaming).
	Text string

	// IsFinal indicates this is the final response for the current turn.
	IsFinal bool

	// ToolCalls lists the names of tools called in this event (if any).
	ToolCalls []string

	// Usage contains token counts (only populated on IsFinal events).
	Usage *Usage

	// Author is the agent or sub-agent that produced this event.
	Author string
}

// AgentBridge wraps the ADK runner with session management.
// It provides the simple Chat() interface that frontends use.
type AgentBridge struct {
	runner   *runner.Runner
	sessions session.Service
	appName  string
}

// NewAgentBridge creates a bridge to the given agent.
func NewAgentBridge(rootAgent agent.Agent) (*AgentBridge, error) {
	appName := "crated"
	sessions := session.InMemoryService()

	r, err := runner.New(runner.Config{
		AppName:        appName,
		Agent:          rootAgent,
		SessionService: sessions,
	})
	if err != nil {
		return nil, fmt.Errorf("creating runner: %w", err)
	}

	return &AgentBridge{
		runner:   r,
		sessions: sessions,
		appName:  appName,
	}, nil
}

// CreateSession creates a new conversation session for a user.
// Returns a session ID that should be passed to Chat().
func (b *AgentBridge) CreateSession(ctx context.Context, userID string) (string, error) {
	resp, err := b.sessions.Create(ctx, &session.CreateRequest{
		AppName: b.appName,
		UserID:  userID,
	})
	if err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}
	return resp.Session.ID(), nil
}

// Chat sends a user message and returns an iterator of response events.
// The iterator yields partial (streaming) events followed by a final event.
func (b *AgentBridge) Chat(ctx context.Context, userID, sessionID, message string) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		userMsg := genai.NewContentFromText(message, genai.RoleUser)

		// Accumulate token usage across the response turn.
		var totalUsage Usage

		for adkEvent, err := range b.runner.Run(ctx, userID, sessionID, userMsg, agent.RunConfig{
			StreamingMode: agent.StreamingModeSSE,
		}) {
			if err != nil {
				yield(Event{}, err)
				return
			}

			if adkEvent.Content == nil {
				continue
			}

			text := ""
			var toolCalls []string
			for _, p := range adkEvent.Content.Parts {
				text += p.Text
				if p.FunctionCall != nil {
					toolCalls = append(toolCalls, p.FunctionCall.Name)
				}
			}

			// Extract token usage from the ADK event metadata.
			if adkEvent.UsageMetadata != nil {
				um := adkEvent.UsageMetadata
				totalUsage.PromptTokens += um.PromptTokenCount
				totalUsage.CompletionTokens += um.CandidatesTokenCount
				totalUsage.TotalTokens += um.TotalTokenCount
			}

			ev := Event{
				Text:      text,
				IsFinal:   adkEvent.IsFinalResponse(),
				ToolCalls: toolCalls,
				Author:    adkEvent.Author,
			}

			// Attach accumulated usage on the final event.
			if ev.IsFinal && totalUsage.TotalTokens > 0 {
				ev.Usage = &totalUsage
			}

			if !yield(ev, nil) {
				return
			}
		}
	}
}

// --- Frontend Registry ---

var (
	frontendsMu sync.RWMutex
	frontends   = make(map[string]Frontend)
)

// RegisterFrontend registers a frontend implementation.
// Typically called from init() in frontend packages.
// Panics if a frontend with the same name is already registered.
func RegisterFrontend(f Frontend) {
	frontendsMu.Lock()
	defer frontendsMu.Unlock()

	name := f.Name()
	if _, exists := frontends[name]; exists {
		panic(fmt.Sprintf("frontend %q already registered", name))
	}
	frontends[name] = f
}

// GetFrontend returns a registered frontend by name.
func GetFrontend(name string) (Frontend, bool) {
	frontendsMu.RLock()
	defer frontendsMu.RUnlock()
	f, ok := frontends[name]
	return f, ok
}

// RegisteredFrontends returns the names of all registered frontends.
func RegisteredFrontends() []string {
	frontendsMu.RLock()
	defer frontendsMu.RUnlock()
	names := make([]string, 0, len(frontends))
	for name := range frontends {
		names = append(names, name)
	}
	return names
}
