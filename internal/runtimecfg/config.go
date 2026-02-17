// Package runtimecfg defines the build-time runtime configuration that
// crate build injects into container images alongside the Agentfile.
//
// The Agentfile defines WHAT the agent is (models, persona, skills).
// The RuntimeConfig defines HOW to connect (API endpoints, auth env vars).
//
// This package lives in crated but the types are also used by crate's builder
// to write the config file during `crate build`.
package runtimecfg

import (
	"encoding/json"
	"fmt"
	"os"
)

// ConfigPath is the conventional path for the runtime config inside a
// crate container image. Written by `crate build`, read by `crated`.
const ConfigPath = ".crate/runtime.json"

// Config holds the connection metadata for each model, resolved from
// the registry at build time. This is NOT part of the Agentfile spec.
type Config struct {
	// Models maps the provider-qualified model ID (e.g., "openai/gpt-4o")
	// to its connection details.
	Models map[string]ModelConnection `json:"models"`
}

// ModelConnection holds the resolved connection details for a single model.
type ModelConnection struct {
	// APIType is the API protocol family: "openai", "gemini", "anthropic".
	// Determines which provider implementation the runtime uses.
	// For example, Ollama uses "openai" because it speaks the OpenAI API.
	APIType string `json:"api_type"`

	// APIBase is the base URL for the model API endpoint.
	// e.g., "https://api.openai.com/v1", "http://localhost:11434/v1"
	APIBase string `json:"api_base"`

	// AuthEnvVar is the name of the environment variable containing the API key.
	// Empty means no authentication is required (e.g., local Ollama).
	AuthEnvVar string `json:"auth_env_var,omitempty"`

	// HostEnvVar is the name of an environment variable that overrides APIBase
	// at runtime. For example, OLLAMA_HOST lets users point to a remote Ollama
	// instance without rebuilding. Empty means APIBase is used as-is.
	HostEnvVar string `json:"host_env_var,omitempty"`

	// MaxConcurrency is the maximum number of concurrent requests to this model.
	// Zero means use the default (10). Set to 1 for local models like Ollama
	// that typically handle one request at a time.
	MaxConcurrency int `json:"max_concurrency,omitempty"`
}

// Load reads a runtime config from the given path.
// Returns an empty config (not an error) if the file doesn't exist,
// so that crated can fall back to provider defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Models: make(map[string]ModelConnection)}, nil
		}
		return nil, fmt.Errorf("reading runtime config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing runtime config: %w", err)
	}
	if cfg.Models == nil {
		cfg.Models = make(map[string]ModelConnection)
	}
	return &cfg, nil
}

// Save writes the runtime config to the given path as JSON.
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling runtime config: %w", err)
	}
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// Lookup returns the connection details for a model, or nil if not found.
func (c *Config) Lookup(model string) *ModelConnection {
	mc, ok := c.Models[model]
	if !ok {
		return nil
	}
	return &mc
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
