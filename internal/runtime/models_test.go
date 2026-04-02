package runtime //nolint:revive // test file for internal package

import (
	"context"
	"fmt"
	"iter"
	"testing"

	"google.golang.org/adk/model"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/runtimecfg"
)

// --- helpers ---

// saveAndRestoreProviders snapshots the global provider registry and restores
// it at the end of the test.
func saveAndRestoreProviders(t *testing.T) {
	t.Helper()
	providersMu.Lock()
	saved := providers
	providers = make(map[string]ModelProvider)
	providersMu.Unlock()
	t.Cleanup(func() {
		providersMu.Lock()
		providers = saved
		providersMu.Unlock()
	})
}

// stubProvider implements ModelProvider for testing.
type stubProvider struct {
	name    string
	model   model.LLM
	err     error
	created int // count of CreateModel calls
}

func (p *stubProvider) Name() string { return p.name }
func (p *stubProvider) CreateModel(_ context.Context, _ string, _ ConnectConfig, _ agentfile.ModelConfig) (model.LLM, error) {
	p.created++
	return p.model, p.err
}

// stubModel implements model.LLM for testing.
type stubModel struct {
	name string
}

func (m *stubModel) Name() string { return m.name }
func (m *stubModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{}, nil)
	}
}

// --- Provider Registry Tests ---

func TestRegisterAndGetProvider(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test-prov"}
	if err := RegisterProvider(sp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := GetProvider("test-prov")
	if !ok {
		t.Fatal("expected provider to be registered")
	}
	if got.Name() != "test-prov" {
		t.Fatalf("expected name 'test-prov', got %q", got.Name())
	}
}

func TestGetProviderNotFound(t *testing.T) {
	saveAndRestoreProviders(t)

	_, ok := GetProvider("nonexistent")
	if ok {
		t.Fatal("expected provider not found")
	}
}

func TestRegisterProviderDuplicateReturnsError(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "dupe"}
	if err := RegisterProvider(sp); err != nil {
		t.Fatalf("first registration should succeed: %v", err)
	}

	if err := RegisterProvider(sp); err == nil {
		t.Fatal("expected error on duplicate registration")
	}
}

func TestRegisteredProviders(t *testing.T) {
	saveAndRestoreProviders(t)

	if err := RegisterProvider(&stubProvider{name: "alpha"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := RegisterProvider(&stubProvider{name: "beta"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := RegisteredProviders()
	if len(names) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(names))
	}
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["alpha"] || !nameSet["beta"] {
		t.Fatalf("expected alpha and beta, got %v", names)
	}
}

// --- splitProviderModel Tests ---

func TestSplitProviderModel(t *testing.T) {
	tests := []struct {
		input    string
		wantProv string
		wantID   string
	}{
		{"openai/gpt-4o", "openai", "gpt-4o"},
		{"anthropic/claude-sonnet-4-20250514", "anthropic", "claude-sonnet-4-20250514"},
		{"gpt-4o", "", "gpt-4o"},
		{"", "", ""},
		{"a/b/c", "a", "b/c"},
	}

	for _, tt := range tests {
		prov, id := splitProviderModel(tt.input)
		if prov != tt.wantProv || id != tt.wantID {
			t.Errorf("splitProviderModel(%q) = (%q, %q), want (%q, %q)",
				tt.input, prov, id, tt.wantProv, tt.wantID)
		}
	}
}

// --- ModelRegistry Tests ---

func TestModelRegistry_DefaultAndGet(t *testing.T) {
	reg := &ModelRegistry{
		models:   map[string]model.LLM{"fast": &stubModel{name: "fast"}, "smart": &stubModel{name: "smart"}},
		defaultN: "fast",
	}

	def, err := reg.Default()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.Name() != "fast" {
		t.Errorf("expected default 'fast', got %q", def.Name())
	}

	m, ok := reg.Get("smart")
	if !ok {
		t.Fatal("expected 'smart' model to exist")
	}
	if m.Name() != "smart" {
		t.Errorf("expected name 'smart', got %q", m.Name())
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Fatal("expected 'nonexistent' model to not exist")
	}
}

func TestModelRegistry_DefaultNotFound(t *testing.T) {
	reg := &ModelRegistry{
		models:   map[string]model.LLM{},
		defaultN: "missing",
	}

	_, err := reg.Default()
	if err == nil {
		t.Fatal("expected error for missing default model")
	}
}

func TestNewModelRegistry_Success(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test", model: &stubModel{name: "test/fast"}}
	if err := RegisterProvider(sp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	brain := agentfile.Brain{
		Default: "fast",
		Models: []agentfile.ModelConfig{
			{Name: "fast", Model: "test/fast"},
		},
	}
	rc := &runtimecfg.Config{Models: make(map[string]runtimecfg.ModelConnection)}

	reg, warnings, err := NewModelRegistry(context.Background(), brain, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings, got %v", warnings)
	}
	if _, ok := reg.Get("fast"); !ok {
		t.Fatal("expected 'fast' model to be registered")
	}
}

func TestNewModelRegistry_DefaultModelFails(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test", err: fmt.Errorf("auth failed")}
	if err := RegisterProvider(sp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	brain := agentfile.Brain{
		Default: "main",
		Models: []agentfile.ModelConfig{
			{Name: "main", Model: "test/main"},
		},
	}
	rc := &runtimecfg.Config{Models: make(map[string]runtimecfg.ModelConnection)}

	_, _, err := NewModelRegistry(context.Background(), brain, rc)
	if err == nil {
		t.Fatal("expected error when default model fails")
	}
}

func TestNewModelRegistry_NonDefaultModelFails_GracefulDegradation(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test"}
	// Override CreateModel to fail on the second model.
	_ = sp // original stub unused; failSecondProvider handles the behavior

	// We need a custom provider that fails for non-default.
	if err := RegisterProvider(&failSecondProvider{name: "test"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	brain := agentfile.Brain{
		Default: "default-model",
		Models: []agentfile.ModelConfig{
			{Name: "default-model", Model: "test/default"},
			{Name: "optional-model", Model: "test/optional"},
		},
	}
	rc := &runtimecfg.Config{Models: make(map[string]runtimecfg.ModelConnection)}

	reg, warnings, err := NewModelRegistry(context.Background(), brain, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning for skipped model, got %d: %v", len(warnings), warnings)
	}
	if _, ok := reg.Get("default-model"); !ok {
		t.Fatal("expected default model to be present")
	}
	if _, ok := reg.Get("optional-model"); ok {
		t.Fatal("expected optional model to be absent")
	}

}

type failSecondProvider struct {
	name  string
	calls int
}

func (p *failSecondProvider) Name() string { return p.name }
func (p *failSecondProvider) CreateModel(_ context.Context, _ string, _ ConnectConfig, mc agentfile.ModelConfig) (model.LLM, error) {
	p.calls++
	if mc.Name == "optional-model" {
		return nil, fmt.Errorf("simulated failure")
	}
	return &stubModel{name: mc.Name}, nil
}

// --- createModel Tests ---

func TestCreateModel_NoProvider(t *testing.T) {
	saveAndRestoreProviders(t)

	mc := agentfile.ModelConfig{Name: "x", Model: "unknown-provider/model"}
	rc := &runtimecfg.Config{Models: make(map[string]runtimecfg.ModelConnection)}

	_, err := createModel(context.Background(), mc, rc)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestCreateModel_NoProviderPrefix(t *testing.T) {
	saveAndRestoreProviders(t)

	mc := agentfile.ModelConfig{Name: "x", Model: "bare-model-name"}
	rc := &runtimecfg.Config{Models: make(map[string]runtimecfg.ModelConnection)}

	_, err := createModel(context.Background(), mc, rc)
	if err == nil {
		t.Fatal("expected error when model has no provider prefix and no runtime config")
	}
}

func TestCreateModel_UsesRuntimeConfig(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "openai", model: &stubModel{name: "openai/gpt-4o"}}
	if err := RegisterProvider(sp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mc := agentfile.ModelConfig{Name: "fast", Model: "openai/gpt-4o"}
	rc := &runtimecfg.Config{
		Models: map[string]runtimecfg.ModelConnection{
			"openai/gpt-4o": {
				APIType:        "openai",
				APIBase:        "https://custom.api.com/v1",
				AuthEnvVar:     "MY_KEY",
				MaxConcurrency: 5,
			},
		},
	}

	m, err := createModel(context.Background(), mc, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The model is wrapped in middleware, so Name() still works.
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}
