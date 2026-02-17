package runtime //nolint:revive // internal package; name clash with stdlib is acceptable

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"google.golang.org/adk/model"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/ratelimit"
	"github.com/agentcrate/crated/internal/runtime/middleware"
	"github.com/agentcrate/crated/internal/runtimecfg"
)

// ConnectConfig holds the resolved connection details for a model.
// Populated from .crate/runtime.json at startup.
type ConnectConfig struct {
	APIBase    string // Base URL for the API endpoint.
	AuthEnvVar string // Env var name for the API key (empty = no auth).
	HostEnvVar string // Env var that overrides APIBase at runtime (e.g., OLLAMA_HOST).
}

// ModelProvider creates LLM instances for a given provider.
// Implementations are registered via RegisterProvider().
type ModelProvider interface {
	// Name returns the provider identifier (e.g., "openai", "gemini", "anthropic").
	Name() string
	// CreateModel creates an LLM instance for the given model ID.
	// The modelID is the part after the "/" in "provider/model-id".
	// ConnectConfig holds API base and auth config from runtime.json.
	// ModelConfig holds tuning parameters from the Agentfile.
	CreateModel(ctx context.Context, modelID string, cc ConnectConfig, mc agentfile.ModelConfig) (model.LLM, error)
}

// providerRegistry is the global registry of model providers.
var (
	providersMu sync.RWMutex
	providers   = make(map[string]ModelProvider)
)

// RegisterProvider registers a model provider. Providers are typically
// registered via init() functions in provider packages.
// Panics if a provider with the same name is already registered.
func RegisterProvider(p ModelProvider) {
	providersMu.Lock()
	defer providersMu.Unlock()

	name := p.Name()
	if _, exists := providers[name]; exists {
		panic(fmt.Sprintf("model provider %q already registered", name))
	}
	providers[name] = p
}

// GetProvider returns a registered provider by name.
func GetProvider(name string) (ModelProvider, bool) {
	providersMu.RLock()
	defer providersMu.RUnlock()
	p, ok := providers[name]
	return p, ok
}

// RegisteredProviders returns the names of all registered providers.
func RegisteredProviders() []string {
	providersMu.RLock()
	defer providersMu.RUnlock()
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	return names
}

// ModelRegistry maps model names from the Agentfile to LLM instances.
type ModelRegistry struct {
	models   map[string]model.LLM
	defaultN string
}

// NewModelRegistry creates LLM instances for each entry in brain.models
// by resolving the provider from the global provider registry.
// The runtimecfg provides connection details (API base, auth) resolved at build time.
//
// Graceful degradation: if a non-default model fails to initialize, the error
// is captured as a warning and the model is skipped. The default model is
// always required — if it fails, the entire registry creation fails.
func NewModelRegistry(ctx context.Context, brain agentfile.Brain, rc *runtimecfg.Config) (*ModelRegistry, []string, error) {
	reg := &ModelRegistry{
		models:   make(map[string]model.LLM, len(brain.Models)),
		defaultN: brain.Default,
	}
	var warnings []string

	for _, mc := range brain.Models {
		m, err := createModel(ctx, mc, rc)
		if err != nil {
			if mc.Name == brain.Default {
				return nil, nil, fmt.Errorf("creating default model %q: %w", mc.Name, err)
			}
			warnings = append(warnings, fmt.Sprintf("model %q skipped: %v", mc.Name, err))
			continue
		}
		reg.models[mc.Name] = m
	}

	return reg, warnings, nil
}

// Default returns the default model.
func (r *ModelRegistry) Default() (model.LLM, error) {
	m, ok := r.models[r.defaultN]
	if !ok {
		return nil, fmt.Errorf("default model %q not found in registry", r.defaultN)
	}
	return m, nil
}

// Get returns a model by name.
func (r *ModelRegistry) Get(name string) (model.LLM, bool) {
	m, ok := r.models[name]
	return m, ok
}

// createModel resolves the provider and delegates to the registered ModelProvider.
// It wraps the resulting model with logging and rate limiting middleware.
// Provider resolution: runtime config APIType > model prefix.
func createModel(ctx context.Context, mc agentfile.ModelConfig, rc *runtimecfg.Config) (model.LLM, error) {
	_, modelID := splitProviderModel(mc.Model)

	// Build ConnectConfig from the runtime config (written by crate build).
	var cc ConnectConfig
	var providerName string
	var maxConcurrency int

	if conn := rc.Lookup(mc.Model); conn != nil {
		// Runtime config found — use build-time resolved connection details.
		cc.APIBase = conn.APIBase
		cc.AuthEnvVar = conn.AuthEnvVar
		cc.HostEnvVar = conn.HostEnvVar
		providerName = conn.APIType
		maxConcurrency = conn.MaxConcurrency
	}

	// Fall back to the prefix in the model string if no runtime config.
	if providerName == "" {
		providerName, _ = splitProviderModel(mc.Model)
	}

	if providerName == "" {
		return nil, fmt.Errorf(
			"model %q: cannot determine provider; ensure .crate/runtime.json exists or use provider/model-id format; "+
				"registered providers: %v",
			mc.Model, RegisteredProviders(),
		)
	}

	provider, ok := GetProvider(providerName)
	if !ok {
		return nil, fmt.Errorf(
			"unknown model provider %q for model %q; registered providers: %v",
			providerName, mc.Model, RegisteredProviders(),
		)
	}

	m, err := provider.CreateModel(ctx, modelID, cc, mc)
	if err != nil {
		return nil, err
	}

	// Apply middleware: logging → rate limiting (outermost).
	logger := slog.Default().With("provider", providerName, "model", m.Name())
	m = middleware.WithLogging(m, logger)
	m = middleware.WithRateLimit(m, ratelimit.New(maxConcurrency))

	return m, nil
}

// splitProviderModel splits "provider/model-id" into its components.
// Returns ("", fullString) if no "/" separator is found.
func splitProviderModel(qualified string) (provider, modelID string) {
	parts := strings.SplitN(qualified, "/", 2)
	if len(parts) < 2 {
		return "", qualified
	}
	return parts[0], parts[1]
}
