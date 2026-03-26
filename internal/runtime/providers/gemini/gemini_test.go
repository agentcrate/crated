package gemini

import (
	"context"
	"testing"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/runtime"
)

func TestProvider_Name(t *testing.T) {
	p := &provider{}
	if got := p.Name(); got != "gemini" {
		t.Errorf("expected 'gemini', got %q", got)
	}
}

func TestAlias_Name(t *testing.T) {
	a := &alias{provider: &provider{}, name: "google"}
	if got := a.Name(); got != "google" {
		t.Errorf("expected 'google', got %q", got)
	}
}

func TestProvider_CreateModel_NoAPIKey(t *testing.T) {
	// Ensure none of the key env vars are set.
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	p := &provider{}
	_, err := p.CreateModel(context.Background(), "gemini-2.5-pro", runtime.ConnectConfig{}, agentfile.ModelConfig{})
	if err == nil {
		t.Fatal("expected error when no API key env is set")
	}
}

func TestProvider_CreateModel_CustomAuthEnvVar_Missing(t *testing.T) {
	t.Setenv("MY_CUSTOM_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	p := &provider{}
	_, err := p.CreateModel(context.Background(), "gemini-2.5-pro", runtime.ConnectConfig{
		AuthEnvVar: "MY_CUSTOM_KEY",
	}, agentfile.ModelConfig{})
	if err == nil {
		t.Fatal("expected error when custom auth env var is empty")
	}
}

func TestAlias_CreateModel_DelegatesToProvider(t *testing.T) {
	// An alias with no API key should produce the same error as the provider.
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	a := &alias{provider: &provider{}, name: "google"}
	_, err := a.CreateModel(context.Background(), "gemini-2.5-pro", runtime.ConnectConfig{}, agentfile.ModelConfig{})
	if err == nil {
		t.Fatal("expected error from alias delegation when no API key")
	}
}

func TestInit_RegistersBothProviders(t *testing.T) {
	// The init() function registers "gemini" and "google".
	gemini, ok := runtime.GetProvider("gemini")
	if !ok {
		t.Fatal("expected 'gemini' provider to be registered via init()")
	}
	if gemini.Name() != "gemini" {
		t.Errorf("expected name 'gemini', got %q", gemini.Name())
	}

	google, ok := runtime.GetProvider("google")
	if !ok {
		t.Fatal("expected 'google' provider alias to be registered via init()")
	}
	if google.Name() != "google" {
		t.Errorf("expected name 'google', got %q", google.Name())
	}
}
