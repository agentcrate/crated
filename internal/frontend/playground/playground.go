// Package playground implements a web-based chat frontend for crated.
//
// It serves an embedded HTML/CSS/JS chat interface on port 3000 and
// communicates with the agent via WebSocket. This is the primary frontend
// for `crate dev` and `crate run --playground`.
//
// # Architecture
//
//	Browser  ──WebSocket──>  playground.go  ──>  AgentBridge.Chat()
//	         <──JSON events──               <──  iter.Seq2[Event, error]
//
// TODO: Add WebSocket integration tests using httptest.NewServer and gorilla/websocket test client.
package playground

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/agentcrate/crated/internal/frontend"
)

//go:embed static/*
var staticFS embed.FS

func init() {
	if err := frontend.RegisterFrontend(&Playground{}); err != nil {
		panic(fmt.Sprintf("registering playground frontend: %v", err))
	}
}

// Playground is the web-based chat frontend.
type Playground struct{}

// Name returns the frontend identifier.
func (p *Playground) Name() string { return "playground" }

// Run starts the HTTP/WebSocket server and blocks until ctx is canceled.
func (p *Playground) Run(ctx context.Context, bridge *frontend.AgentBridge) error {
	logger := slog.Default().With("component", "playground")

	mux := http.NewServeMux()

	// Serve embedded static files.
	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("loading static files: %w", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticContent))))

	// Serve index.html at root.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	// Health endpoint.
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Resolve and validate port before setting up routes.
	port := os.Getenv("PLAYGROUND_PORT")
	if port == "" {
		port = "3000"
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return fmt.Errorf("invalid PLAYGROUND_PORT %q: must be an integer between 1 and 65535", port)
	}

	// WebSocket endpoint.
	upgrader := newUpgrader(port)
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r, bridge, logger, &upgrader)
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return fmt.Errorf("listening on :%s: %w", port, err)
	}

	logger.Info("playground running", "url", "http://localhost:"+port)

	// Shutdown on context cancellation.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}

	return nil
}

// ── WebSocket handler ──────────────────────────────────────────────────

// newUpgrader creates a WebSocket upgrader with origin checking scoped to the
// playground's own listen port. In production (ALLOWED_ORIGINS set), only
// explicitly listed origins are accepted. In dev mode (default), the upgrader
// accepts only the exact playground origin to mitigate DNS rebinding attacks.
func newUpgrader(playgroundPort string) websocket.Upgrader {
	// Build the allowed origin set.
	allowed := map[string]bool{}
	if envOrigins := os.Getenv("ALLOWED_ORIGINS"); envOrigins != "" {
		for _, o := range splitOrigins(envOrigins) {
			allowed[o] = true
		}
	} else {
		// Dev mode: allow only the exact playground origin.
		allowed["http://localhost:"+playgroundPort] = true
		allowed["http://127.0.0.1:"+playgroundPort] = true
	}

	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // Same-origin requests don't send Origin header.
			}
			return allowed[origin]
		},
	}
}

// splitOrigins parses a comma-separated list of allowed origins.
func splitOrigins(s string) []string {
	var origins []string
	for _, part := range splitComma(s) {
		trimmed := trimSpace(part)
		if trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	return origins
}

// trimSpace trims leading/trailing whitespace from s (avoids importing strings).
func trimSpace(s string) string {
	for s != "" && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for s != "" && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// splitComma splits s on commas.
func splitComma(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// wsIncoming is a message from the browser.
type wsIncoming struct {
	Type string `json:"type"` // "message"
	Text string `json:"text"`
}

// wsOutgoing is an event sent to the browser.
type wsOutgoing struct {
	Type             string   `json:"type"`                       // "text", "tool_call", "done", "error", "reload"
	Text             string   `json:"text,omitempty"`             // text content
	Tools            []string `json:"tools,omitempty"`            // tool names (for tool_call events)
	PromptTokens     int32    `json:"promptTokens,omitempty"`     // token usage (on done events)
	CompletionTokens int32    `json:"completionTokens,omitempty"` // token usage (on done events)
	TotalTokens      int32    `json:"totalTokens,omitempty"`      // token usage (on done events)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request, bridge *frontend.AgentBridge, logger *slog.Logger, upgrader *websocket.Upgrader) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("websocket upgrade failed", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	ctx := r.Context()

	// Create a session for this WebSocket connection.
	userID := r.URL.Query().Get("user")
	if userID == "" {
		userID = "playground-user"
		logger.Warn("no user ID provided in WebSocket connection, using default")
	}
	sessionID, err := bridge.CreateSession(ctx, userID)
	if err != nil {
		logger.Error("creating session", "error", err)
		_ = writeJSON(conn, wsOutgoing{Type: "error", Text: "Failed to create session"})
		return
	}

	logger.Info("websocket connected", "session", sessionID)

	// Read loop — receive user messages and stream agent responses.
	for {
		var msg wsIncoming
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.Error("websocket read", "error", err)
			}
			return
		}

		if msg.Type != "message" || msg.Text == "" {
			continue
		}

		logger.Debug("user message", "text_len", len(msg.Text))

		// Stream agent response with per-message timeout.
		chatCtx, chatCancel := context.WithTimeout(ctx, 5*time.Minute)
		streamChat(chatCtx, conn, bridge, userID, sessionID, msg.Text, logger)
		chatCancel()
	}
}

// streamChat streams a single chat response to the WebSocket connection.
func streamChat(ctx context.Context, conn *websocket.Conn, bridge *frontend.AgentBridge, userID, sessionID, text string, logger *slog.Logger) {
	for event, err := range bridge.Chat(ctx, userID, sessionID, text) {
		if err != nil {
			_ = writeJSON(conn, wsOutgoing{Type: "error", Text: err.Error()})
			return
		}

		// Send tool calls.
		if len(event.ToolCalls) > 0 {
			if err := writeJSON(conn, wsOutgoing{Type: "tool_call", Tools: event.ToolCalls}); err != nil {
				logger.Warn("failed to write tool_call to websocket", "error", err)
				return
			}
		}

		// Send text content.
		if event.Text != "" {
			if err := writeJSON(conn, wsOutgoing{Type: "text", Text: event.Text}); err != nil {
				logger.Warn("failed to write text to websocket", "error", err)
				return
			}
		}

		// Send done marker with usage data.
		if event.IsFinal {
			doneMsg := wsOutgoing{Type: "done"}
			if event.Usage != nil {
				doneMsg.PromptTokens = event.Usage.PromptTokens
				doneMsg.CompletionTokens = event.Usage.CompletionTokens
				doneMsg.TotalTokens = event.Usage.TotalTokens
			}
			if err := writeJSON(conn, doneMsg); err != nil {
				logger.Warn("failed to write done to websocket", "error", err)
			}
		}
	}
}

func writeJSON(conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}
