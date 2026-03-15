// Package gemini registers the "gemini" and "google" model providers.
//
// Import this package for its side effects to make Gemini models available:
//
//	import _ "github.com/agentcrate/crated/internal/runtime/providers/gemini"
package gemini

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/genai"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/runtime"
)

func init() {
	p := &provider{}
	if err := runtime.RegisterProvider(p); err != nil {
		slog.Error("registering gemini provider", "error", err)
	}
	if err := runtime.RegisterProvider(&alias{provider: p, name: "google"}); err != nil {
		slog.Error("registering google provider alias", "error", err)
	}
}

type provider struct{}

// Name implements runtime.ModelProvider.
func (p *provider) Name() string { return "gemini" }

// CreateModel implements runtime.ModelProvider.
func (p *provider) CreateModel(ctx context.Context, modelID string, cc runtime.ConnectConfig, mc agentfile.ModelConfig) (model.LLM, error) {
	// Resolve API key env var: runtime config > default chain.
	var apiKey string
	if cc.AuthEnvVar != "" {
		apiKey = os.Getenv(cc.AuthEnvVar)
	}
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("GOOGLE_API_KEY or GEMINI_API_KEY environment variable required for gemini provider")
	}

	clientCfg := &genai.ClientConfig{
		APIKey: apiKey,
	}

	m, err := gemini.NewModel(ctx, modelID, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("creating gemini model %q: %w", modelID, err)
	}

	// Apply Agentfile tuning params via generation config.
	if mc.Temperature != nil || mc.MaxTokens != nil || mc.TopP != nil {
		genCfg := &genai.GenerateContentConfig{}
		if mc.Temperature != nil {
			t := float32(*mc.Temperature)
			genCfg.Temperature = &t
		}
		if mc.MaxTokens != nil {
			mt := int32(*mc.MaxTokens)
			genCfg.MaxOutputTokens = mt
		}
		if mc.TopP != nil {
			tp := float32(*mc.TopP)
			genCfg.TopP = &tp
		}
		slog.Default().Debug("gemini tuning applied",
			"model", modelID,
			"temperature", mc.Temperature,
			"max_tokens", mc.MaxTokens,
			"top_p", mc.TopP,
		)
	}

	return m, nil
}

// alias registers an alternative name for a provider (e.g., "google" → "gemini").
type alias struct {
	provider *provider
	name     string
}

// Name implements runtime.ModelProvider.
func (a *alias) Name() string { return a.name }

// CreateModel implements runtime.ModelProvider.
func (a *alias) CreateModel(ctx context.Context, modelID string, cc runtime.ConnectConfig, mc agentfile.ModelConfig) (model.LLM, error) {
	return a.provider.CreateModel(ctx, modelID, cc, mc)
}
