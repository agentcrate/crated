package runtime //nolint:revive // test file for internal package

import (
	"context"
	"testing"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/runtimecfg"
)

func TestNew_WithNilConfig(t *testing.T) {
	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "test-agent"},
	}
	rt := New(af, nil)
	if rt == nil { //nolint:staticcheck // guarding against nil
		t.Fatal("expected non-nil Runtime")
	}
	if rt.Agentfile() != af {
		t.Error("expected Agentfile to match")
	}
	if rt.rc == nil { //nolint:staticcheck // rt confirmed non-nil above
		t.Error("expected rc to be initialized when nil is passed")
	}
}

func TestNew_WithConfig(t *testing.T) {
	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "test-agent"},
	}
	rc := &runtimecfg.Config{
		Models: map[string]runtimecfg.ModelConnection{
			"openai/gpt-4o": {APIType: "openai"},
		},
	}
	rt := New(af, rc)
	if rt.Agentfile().Metadata.Name != "test-agent" {
		t.Errorf("expected name 'test-agent', got %q", rt.Agentfile().Metadata.Name)
	}
}

func TestRuntime_AgentAndModels_BeforeInit(t *testing.T) {
	af := &agentfile.Agentfile{}
	rt := New(af, nil)

	// Before Init, Agent and Models are nil.
	if rt.Agent() != nil {
		t.Error("expected nil agent before Init")
	}
	if rt.Models() != nil {
		t.Error("expected nil models before Init")
	}
}

func TestRuntime_Close_NoClosers(t *testing.T) {
	af := &agentfile.Agentfile{}
	rt := New(af, nil)
	// Should not panic with no closers.
	rt.Close()
}

func TestRuntime_Close_CallsClosers(t *testing.T) {
	af := &agentfile.Agentfile{}
	rt := New(af, nil)

	called := 0
	rt.closers = append(rt.closers, func() { called++ })
	rt.closers = append(rt.closers, func() { called++ })

	rt.Close()
	if called != 2 {
		t.Errorf("expected 2 closers called, got %d", called)
	}
	if rt.closers != nil {
		t.Error("expected closers to be nil after Close")
	}

	// Calling Close again should be a no-op.
	rt.Close()
}

func TestConnectSkill_UnknownType(t *testing.T) {
	skill := agentfile.Skill{
		Name:   "bad-skill",
		Type:   "unknown",
		Source: "something",
	}
	_, _, err := connectSkill(context.Background(), &skill)
	if err == nil {
		t.Fatal("expected error for unknown skill type")
	}
}

func TestConnectSkill_MCPType(t *testing.T) {
	skill := agentfile.Skill{
		Name:   "registry-skill",
		Type:   "mcp",
		Source: "cratehub.ai/tools/web-search",
	}
	_, _, err := connectSkill(context.Background(), &skill)
	if err == nil {
		t.Fatal("expected error for mcp type at runtime")
	}
}

func TestInit_NoProviderForModel(t *testing.T) {
	saveAndRestoreProviders(t)

	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "test-agent"},
		Brain: agentfile.Brain{
			Default: "main",
			Models: []agentfile.ModelConfig{
				{Name: "main", Model: "nonexistent-provider/model"},
			},
		},
	}
	rt := New(af, nil)
	err := rt.Init(context.Background())
	if err == nil {
		t.Fatal("expected error for unresolvable model provider")
	}
}

func TestInit_SuccessWithStubProvider(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test", model: &stubModel{name: "test/chat"}}
	RegisterProvider(sp)

	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "test-agent", Description: "A test agent"},
		Brain: agentfile.Brain{
			Default: "main",
			Models: []agentfile.ModelConfig{
				{Name: "main", Model: "test/chat"},
			},
		},
		Persona: agentfile.Persona{SystemPrompt: "You are a test agent."},
	}
	rt := New(af, nil)
	err := rt.Init(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer rt.Close()

	if rt.Agent() == nil {
		t.Error("expected non-nil agent after Init")
	}
	if rt.Models() == nil {
		t.Error("expected non-nil models after Init")
	}
}

func TestInit_SkillConnectionFailure(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test", model: &stubModel{name: "test/chat"}}
	RegisterProvider(sp)

	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "test-agent"},
		Brain: agentfile.Brain{
			Default: "main",
			Models: []agentfile.ModelConfig{
				{Name: "main", Model: "test/chat"},
			},
		},
		Skills: []agentfile.Skill{
			{Name: "bad-skill", Type: "unknown", Source: "something"},
		},
	}
	rt := New(af, nil)
	err := rt.Init(context.Background())
	if err == nil {
		t.Fatal("expected error for skill connection failure")
	}
}

func TestInit_WithModelWarnings(t *testing.T) {
	saveAndRestoreProviders(t)

	RegisterProvider(&failSecondProvider{name: "test"})

	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "test-agent", Description: "A test agent"},
		Brain: agentfile.Brain{
			Default: "default-model",
			Models: []agentfile.ModelConfig{
				{Name: "default-model", Model: "test/default"},
				{Name: "optional-model", Model: "test/optional"},
			},
		},
		Persona: agentfile.Persona{SystemPrompt: "test"},
	}
	rt := New(af, nil)
	err := rt.Init(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer rt.Close()

	if rt.Agent() == nil {
		t.Error("expected non-nil agent")
	}
}

// --- checkSkillEnv Tests ---

func TestCheckSkillEnv_NoEnvVars(t *testing.T) {
	skill := &agentfile.Skill{Name: "no-env", Type: "http"}
	if err := checkSkillEnv(skill); err != nil {
		t.Fatalf("expected nil error for skill with no env requirements: %v", err)
	}
}

func TestCheckSkillEnv_AllSet(t *testing.T) {
	t.Setenv("TEST_SKILL_KEY", "present")
	t.Setenv("TEST_SKILL_SECRET", "also-present")

	skill := &agentfile.Skill{
		Name: "env-skill",
		Type: "http",
		Env:  []string{"TEST_SKILL_KEY", "TEST_SKILL_SECRET"},
	}
	if err := checkSkillEnv(skill); err != nil {
		t.Fatalf("expected nil error when all envs are set: %v", err)
	}
}

func TestCheckSkillEnv_Missing(t *testing.T) {
	t.Setenv("TEST_SKILL_KEY", "present")
	t.Setenv("TEST_SKILL_MISSING", "")

	skill := &agentfile.Skill{
		Name: "env-skill",
		Type: "http",
		Env:  []string{"TEST_SKILL_KEY", "TEST_SKILL_MISSING"},
	}
	err := checkSkillEnv(skill)
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

// --- Reload Tests ---

func TestReload_Success(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test", model: &stubModel{name: "test/chat"}}
	RegisterProvider(sp)

	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "orig-agent", Description: "Original"},
		Brain: agentfile.Brain{
			Default: "main",
			Models:  []agentfile.ModelConfig{{Name: "main", Model: "test/chat"}},
		},
		Persona: agentfile.Persona{SystemPrompt: "Original prompt"},
	}
	rt := New(af, nil)
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer rt.Close()

	// Reload with changed persona.
	newAF := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "updated-agent", Description: "Updated"},
		Brain: agentfile.Brain{
			Default: "main",
			Models:  []agentfile.ModelConfig{{Name: "main", Model: "test/chat"}},
		},
		Persona: agentfile.Persona{SystemPrompt: "Updated prompt"},
	}
	if err := rt.Reload(context.Background(), newAF); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if rt.Agentfile().Metadata.Name != "updated-agent" {
		t.Errorf("expected name 'updated-agent', got %q", rt.Agentfile().Metadata.Name)
	}
	if rt.Agent() == nil {
		t.Error("expected non-nil agent after Reload")
	}
}

func TestReload_NoDefaultModel(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test", model: &stubModel{name: "test/chat"}}
	RegisterProvider(sp)

	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "test-agent"},
		Brain: agentfile.Brain{
			Default: "main",
			Models:  []agentfile.ModelConfig{{Name: "main", Model: "test/chat"}},
		},
		Persona: agentfile.Persona{SystemPrompt: "Test prompt"},
	}
	rt := New(af, nil)
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer rt.Close()

	// Break the model registry so Default() fails.
	rt.models.defaultN = "nonexistent"

	err := rt.Reload(context.Background(), af)
	if err == nil {
		t.Fatal("expected error when Reload can't resolve default model")
	}
}

// --- preloadContext Tests ---

func TestPreloadContext_Stubs(t *testing.T) {
	pc := preloadContext{context.Background()}

	if pc.UserContent() != nil {
		t.Error("expected nil UserContent")
	}
	if pc.InvocationID() != "" {
		t.Errorf("expected empty InvocationID, got %q", pc.InvocationID())
	}
	if pc.AgentName() != "" {
		t.Errorf("expected empty AgentName, got %q", pc.AgentName())
	}
	if pc.ReadonlyState() != nil {
		t.Error("expected nil ReadonlyState")
	}
	if pc.UserID() != "" {
		t.Errorf("expected empty UserID, got %q", pc.UserID())
	}
	if pc.AppName() != "" {
		t.Errorf("expected empty AppName, got %q", pc.AppName())
	}
	if pc.SessionID() != "" {
		t.Errorf("expected empty SessionID, got %q", pc.SessionID())
	}
	if pc.Branch() != "" {
		t.Errorf("expected empty Branch, got %q", pc.Branch())
	}
}

// --- connectSkill transport type Tests ---

func TestConnectSkill_HTTPType(t *testing.T) {
	skill := agentfile.Skill{
		Name:   "http-skill",
		Type:   "http",
		Source: "http://127.0.0.1:1/invalid-endpoint",
	}
	// connectSkill for http creates the transport and toolset without error;
	// the actual connection failure happens when Tools() is called.
	ts, closer, err := connectSkill(context.Background(), &skill)
	if err != nil {
		t.Fatalf("unexpected error for http skill setup: %v", err)
	}
	if ts == nil {
		t.Fatal("expected non-nil toolset for http skill")
	}
	if closer != nil {
		t.Error("expected nil closer for http skill (no subprocess)")
	}
}

func TestConnectSkill_SSEType(t *testing.T) {
	skill := agentfile.Skill{
		Name:   "sse-skill",
		Type:   "sse",
		Source: "http://127.0.0.1:1/invalid-endpoint",
	}
	ts, closer, err := connectSkill(context.Background(), &skill)
	if err != nil {
		t.Fatalf("unexpected error for sse skill setup: %v", err)
	}
	if ts == nil {
		t.Fatal("expected non-nil toolset for sse skill")
	}
	if closer != nil {
		t.Error("expected nil closer for sse skill (no subprocess)")
	}
}

func TestInit_MissingSkillEnvFails(t *testing.T) {
	saveAndRestoreProviders(t)

	sp := &stubProvider{name: "test", model: &stubModel{name: "test/chat"}}
	RegisterProvider(sp)

	t.Setenv("MISSING_VAR", "")

	af := &agentfile.Agentfile{
		Metadata: agentfile.Metadata{Name: "test-agent"},
		Brain: agentfile.Brain{
			Default: "main",
			Models:  []agentfile.ModelConfig{{Name: "main", Model: "test/chat"}},
		},
		Skills: []agentfile.Skill{
			{
				Name:   "needs-env",
				Type:   "http",
				Source: "http://example.com",
				Env:    []string{"MISSING_VAR"},
			},
		},
	}
	rt := New(af, nil)
	err := rt.Init(context.Background())
	if err == nil {
		t.Fatal("expected error for skill with missing env var")
	}
}
