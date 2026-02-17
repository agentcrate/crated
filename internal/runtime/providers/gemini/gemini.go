// Package gemini registers the "gemini" and "google" model providers.
//
// Import this package for its side effects to make Gemini models available:
//
//	import _ "github.com/agentcrate/crated/internal/runtime/providers/gemini"
package gemini

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/genai"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/runtime"
)

func init() {
	p := &provider{}
	runtime.RegisterProvider(p)
	runtime.RegisterProvider(&alias{provider: p, name: "google"})
}

type provider struct{}

// Name implements runtime.ModelProvider.
func (p *provider) Name() string { return "gemini" }

// CreateModel implements runtime.ModelProvider.
func (p *provider) CreateModel(ctx context.Context, modelID string, cc runtime.ConnectConfig, _ agentfile.ModelConfig) (model.LLM, error) {
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

	m, err := gemini.NewModel(ctx, modelID, &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("creating gemini model %q: %w", modelID, err)
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
