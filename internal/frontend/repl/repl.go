// Package repl provides a console-based interactive frontend for the agent.
//
// Import this package for its side effects to register the "repl" frontend:
//
//	import _ "github.com/agentcrate/crated/internal/frontend/repl"
//
// The REPL reads lines from stdin under the "User -> " prompt and sends
// them to the agent. It supports:
//   - "exit" or "quit" commands for graceful shutdown
//   - Ctrl+D (EOF) for graceful shutdown
//   - Streaming output (partial responses printed as they arrive)
package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/agentcrate/crated/internal/frontend"
)

// exitCommands are the inputs that trigger a graceful shutdown.
var exitCommands = map[string]bool{
	"exit": true,
	"quit": true,
}

func init() {
	frontend.RegisterFrontend(&replFrontend{})
}

type replFrontend struct{}

// Name implements frontend.Frontend.
func (f *replFrontend) Name() string { return "repl" }

// Run implements frontend.Frontend.
func (f *replFrontend) Run(ctx context.Context, bridge *frontend.AgentBridge) error {
	logger := slog.Default().With("frontend", "repl")

	// Create a session for the console user.
	sessionID, err := bridge.CreateSession(ctx, "console_user")
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	reader := bufio.NewReader(os.Stdin)

	for {
		// Check context before blocking on stdin.
		if ctx.Err() != nil {
			logger.Info("agent stopped")
			return nil
		}

		fmt.Print("\nUser -> ")

		userInput, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println()
				logger.Info("stdin closed, shutting down")
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("reading stdin: %w", err)
		}

		trimmed := strings.TrimSpace(strings.ToLower(userInput))
		if exitCommands[trimmed] {
			logger.Info("exit command received, shutting down")
			return nil
		}

		if strings.TrimSpace(userInput) == "" {
			continue
		}

		prevText := ""
		prompted := false
		for event, err := range bridge.Chat(ctx, "console_user", sessionID, userInput) {
			if err != nil {
				fmt.Printf("\nERROR: %v\n", err)
				break
			}

			if event.Text != "" && !prompted {
				fmt.Print("\nAgent -> ")
				prompted = true
			}

			// Print partial (streaming) responses immediately.
			if !event.IsFinal {
				fmt.Print(event.Text)
				prevText += event.Text
				continue
			}

			// Only print the final response if it differs from the streamed text.
			if event.Text != prevText {
				fmt.Print(event.Text)
			}
			prevText = ""
		}
	}
}
