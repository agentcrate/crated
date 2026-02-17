// crated is the agent execution daemon and container entrypoint.
//
// It reads an Agentfile, applies the active environment profile (if any),
// initializes model providers and MCP skill connections, then runs the
// ADK tool-calling loop through a pluggable frontend interface.
//
// This binary is the ENTRYPOINT for agent container images built by
// `crate build`.
//
// Frontend selection (--frontend flag, default: repl):
//
//	crated --agentfile /agent/Agentfile --frontend repl
//
// Profile selection (precedence: flag > env var > base config):
//
//	crated --agentfile /agent/Agentfile --profile prod
//	CRATE_PROFILE=staging crated --agentfile /agent/Agentfile
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/frontend"
	"github.com/agentcrate/crated/internal/health"
	"github.com/agentcrate/crated/internal/runtime"
	"github.com/agentcrate/crated/internal/runtimecfg"

	// Register model providers — each init() adds to the provider registry.
	_ "github.com/agentcrate/crated/internal/runtime/providers/anthropic"
	_ "github.com/agentcrate/crated/internal/runtime/providers/gemini"
	_ "github.com/agentcrate/crated/internal/runtime/providers/openai"

	// Register frontends — each init() adds to the frontend registry.
	_ "github.com/agentcrate/crated/internal/frontend/repl"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	agentfilePath := flag.String("agentfile", "Agentfile", "Path to the Agentfile")
	profileFlag := flag.String("profile", "", "Environment profile to activate (overrides CRATE_PROFILE)")
	runtimeCfgPath := flag.String("runtime-config", "", "Path to runtime.json (default: .crate/runtime.json relative to Agentfile)")
	healthPort := flag.Int("health-port", 8080, "Port for health check endpoints (0 to disable)")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	logFormat := flag.String("log-format", "auto", "Log format: auto, json, text (auto=text for repl, json otherwise)")
	frontendName := flag.String("frontend", "repl", "Frontend to use: "+fmt.Sprintf("%v", frontend.RegisteredFrontends()))
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("crated", version)
		return
	}

	// Initialize structured logging.
	initLogging(*logLevel, *logFormat, *frontendName)

	if err := run(*agentfilePath, *profileFlag, *runtimeCfgPath, *frontendName, *healthPort); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// initLogging sets up the global logger.
//
// Format options:
//   - "auto": text for interactive frontends (repl), JSON for services (http, etc.)
//   - "json": always JSON (machine-readable, for production/containers)
//   - "text": always text (human-readable, for development)
func initLogging(level, format, frontendName string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	useText := format == "text" || (format == "auto" && frontendName == "repl")

	var handler slog.Handler
	if useText {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}

func run(agentfilePath, profileFlag, runtimeCfgFlag, frontendName string, healthPort int) error {
	logger := slog.Default().With("component", "main")
	logger.Info("starting crated", "version", version, "pid", os.Getpid())

	// Resolve the frontend before doing any heavy lifting.
	fe, ok := frontend.GetFrontend(frontendName)
	if !ok {
		return fmt.Errorf("unknown frontend %q; registered: %v", frontendName, frontend.RegisteredFrontends())
	}
	logger.Info("using frontend", "frontend", fe.Name())

	// Create a cancellable context. We wire both signal-based (Ctrl+C) and
	// frontend-initiated cancellation into this single context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handler: first Ctrl+C = graceful shutdown, second = force exit.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("received shutdown signal, press Ctrl+C again to force exit")
		cancel()
		<-sigCh
		logger.Warn("forced exit")
		os.Exit(1)
	}()

	// 1. Start health check server (liveness responds immediately).
	var hs *health.Server
	if healthPort > 0 {
		hs = health.NewServer(fmt.Sprintf(":%d", healthPort))
		go hs.ListenAndServe(ctx)
		defer func() {
			if hs != nil {
				hs.MarkNotReady()
			}
		}()
	}

	// 2. Listen for SIGHUP to reload config.
	go handleSIGHUP(logger)

	// 3. Parse and validate the Agentfile.
	data, err := os.ReadFile(agentfilePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", agentfilePath, err)
	}

	result, err := agentfile.Parse(data)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", agentfilePath, err)
	}
	if !result.IsValid() {
		for _, e := range result.Errors {
			logger.Error("validation error", "field", e.Field, "message", e.Message)
		}
		return fmt.Errorf("agentfile validation failed with %d errors", len(result.Errors))
	}

	// 4. Resolve the active profile (flag > env > base).
	af := result.Agentfile
	profileName := resolveProfile(profileFlag)
	if profileName != "" {
		resolved, err := agentfile.ResolveProfile(af, profileName)
		if err != nil {
			return fmt.Errorf("resolving profile %q: %w", profileName, err)
		}
		af = resolved
		logger.Info("using profile", "profile", profileName)
	}

	// 5. Load runtime config (written by crate build).
	rcPath := runtimeCfgFlag
	if rcPath == "" {
		// Default: .crate/runtime.json relative to the Agentfile.
		rcPath = filepath.Join(filepath.Dir(agentfilePath), runtimecfg.ConfigPath)
	}
	rc, err := runtimecfg.Load(rcPath)
	if err != nil {
		return fmt.Errorf("loading runtime config: %w", err)
	}

	// 6. Initialize the runtime (models + skills + ADK agent).
	rt := runtime.New(af, rc)
	defer rt.Close()

	if err := rt.Init(ctx); err != nil {
		return fmt.Errorf("initializing runtime: %w", err)
	}

	// 7. Mark ready — readiness probe now returns 200.
	if hs != nil {
		hs.MarkReady()
	}

	// 8. Create the agent bridge and run the frontend.
	bridge, err := frontend.NewAgentBridge(rt.Agent())
	if err != nil {
		return fmt.Errorf("creating agent bridge: %w", err)
	}

	if err := fe.Run(ctx, bridge); err != nil {
		if ctx.Err() != nil {
			logger.Info("agent stopped")
			return nil
		}
		return fmt.Errorf("frontend %q failed: %w", fe.Name(), err)
	}

	return nil
}

// resolveProfile determines the active profile name.
// Precedence: --profile flag > CRATE_PROFILE env var > "" (no profile).
func resolveProfile(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("CRATE_PROFILE")
}

// handleSIGHUP logs when SIGHUP is received. This is a placeholder for
// future config hot-reload functionality.
func handleSIGHUP(logger *slog.Logger) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	for range ch {
		logger.Info("received SIGHUP, config reload not yet implemented")
	}
}
