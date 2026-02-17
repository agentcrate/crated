package runtimecfg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agentcrate/crated/internal/runtimecfg"
)

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")

	content := `{
		"models": {
			"openai/gpt-4o": {
				"api_type": "openai",
				"api_base": "https://api.openai.com/v1",
				"auth_env_var": "OPENAI_API_KEY"
			},
			"ollama/mistral": {
				"api_type": "openai",
				"api_base": "http://localhost:11434/v1",
				"host_env_var": "OLLAMA_HOST"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := runtimecfg.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(cfg.Models))
	}

	oai := cfg.Lookup("openai/gpt-4o")
	if oai == nil {
		t.Fatal("expected openai/gpt-4o to exist")
	}
	if oai.APIType != "openai" {
		t.Errorf("expected api_type=openai, got %q", oai.APIType)
	}
	if oai.APIBase != "https://api.openai.com/v1" {
		t.Errorf("unexpected api_base: %q", oai.APIBase)
	}
	if oai.AuthEnvVar != "OPENAI_API_KEY" {
		t.Errorf("unexpected auth_env_var: %q", oai.AuthEnvVar)
	}

	ollama := cfg.Lookup("ollama/mistral")
	if ollama == nil {
		t.Fatal("expected ollama/mistral to exist")
	}
	if ollama.HostEnvVar != "OLLAMA_HOST" {
		t.Errorf("expected host_env_var=OLLAMA_HOST, got %q", ollama.HostEnvVar)
	}
}

func TestLoad_MissingFile_ReturnsEmpty(t *testing.T) {
	cfg, err := runtimecfg.Load("/nonexistent/runtime.json")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Models) != 0 {
		t.Errorf("expected empty models map, got %d entries", len(cfg.Models))
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runtimecfg.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSave_And_Reload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".crate", "runtime.json")

	original := &runtimecfg.Config{
		Models: map[string]runtimecfg.ModelConnection{
			"anthropic/claude-sonnet-4-20250514": {
				APIType:    "anthropic",
				APIBase:    "https://api.anthropic.com/v1",
				AuthEnvVar: "ANTHROPIC_API_KEY",
			},
		},
	}

	if err := original.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := runtimecfg.Load(path)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	conn := loaded.Lookup("anthropic/claude-sonnet-4-20250514")
	if conn == nil {
		t.Fatal("expected model to exist after reload")
	}
	if conn.APIType != "anthropic" {
		t.Errorf("expected api_type=anthropic, got %q", conn.APIType)
	}
}

func TestLookup_NotFound(t *testing.T) {
	cfg := &runtimecfg.Config{
		Models: map[string]runtimecfg.ModelConnection{},
	}
	if conn := cfg.Lookup("nonexistent/model"); conn != nil {
		t.Errorf("expected nil for nonexistent model, got %+v", conn)
	}
}

func TestLoad_NullModelsField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")
	// JSON with no "models" key — should still get an initialized map.
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := runtimecfg.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Models == nil {
		t.Fatal("expected non-nil Models map")
	}
	if len(cfg.Models) != 0 {
		t.Errorf("expected 0 models, got %d", len(cfg.Models))
	}
}

func TestLookup_Found(t *testing.T) {
	cfg := &runtimecfg.Config{
		Models: map[string]runtimecfg.ModelConnection{
			"openai/gpt-4o": {
				APIType:    "openai",
				APIBase:    "https://api.openai.com/v1",
				AuthEnvVar: "OPENAI_API_KEY",
			},
		},
	}
	conn := cfg.Lookup("openai/gpt-4o")
	if conn == nil {
		t.Fatal("expected connection to be found")
	}
	if conn.APIType != "openai" {
		t.Errorf("expected api_type=openai, got %q", conn.APIType)
	}
}

func TestSave_ErrorOnInvalidPath(t *testing.T) {
	cfg := &runtimecfg.Config{
		Models: map[string]runtimecfg.ModelConnection{},
	}
	// Try to save to a path under /proc which is read-only.
	err := cfg.Save("/proc/nonexistent/path/runtime.json")
	if err == nil {
		t.Fatal("expected error saving to read-only path")
	}
}

func TestLoad_WithMaxConcurrency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")

	content := `{
		"models": {
			"ollama/mistral": {
				"api_type": "openai",
				"api_base": "http://localhost:11434/v1",
				"host_env_var": "OLLAMA_HOST",
				"max_concurrency": 1
			}
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := runtimecfg.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn := cfg.Lookup("ollama/mistral")
	if conn == nil {
		t.Fatal("expected model to exist")
	}
	if conn.MaxConcurrency != 1 {
		t.Errorf("expected max_concurrency=1, got %d", conn.MaxConcurrency)
	}
}

func TestSave_ToRelativePath(t *testing.T) {
	// Change to a temp dir so we can save with a relative path (no "/" prefix).
	// This exercises dirOf returning "." when no "/" is found in the path.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := &runtimecfg.Config{
		Models: map[string]runtimecfg.ModelConnection{
			"test/model": {APIType: "test"},
		},
	}
	err := cfg.Save(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded, err := runtimecfg.Load(path)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if len(loaded.Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(loaded.Models))
	}
}

func TestSave_WithMultipleModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "runtime.json")

	cfg := &runtimecfg.Config{
		Models: map[string]runtimecfg.ModelConnection{
			"openai/gpt-4o": {
				APIType:    "openai",
				APIBase:    "https://api.openai.com/v1",
				AuthEnvVar: "OPENAI_API_KEY",
			},
			"anthropic/claude-sonnet-4-20250514": {
				APIType:        "anthropic",
				APIBase:        "https://api.anthropic.com/v1",
				AuthEnvVar:     "ANTHROPIC_API_KEY",
				MaxConcurrency: 5,
			},
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := runtimecfg.Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if len(loaded.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(loaded.Models))
	}

	conn := loaded.Lookup("anthropic/claude-sonnet-4-20250514")
	if conn == nil {
		t.Fatal("expected anthropic model")
	}
	if conn.MaxConcurrency != 5 {
		t.Errorf("expected max_concurrency=5, got %d", conn.MaxConcurrency)
	}
}
