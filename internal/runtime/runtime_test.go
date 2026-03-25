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
