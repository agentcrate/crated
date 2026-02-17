package frontend

import (
	"context"
	"iter"
	"testing"

	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// --- Registry Tests ---

func TestRegisterAndGetFrontend(t *testing.T) {
	// Reset for test isolation.
	frontendsMu.Lock()
	saved := frontends
	frontends = make(map[string]Frontend)
	frontendsMu.Unlock()
	defer func() {
		frontendsMu.Lock()
		frontends = saved
		frontendsMu.Unlock()
	}()

	mock := &mockFrontend{name: "test-fe"}
	RegisterFrontend(mock)

	got, ok := GetFrontend("test-fe")
	if !ok {
		t.Fatal("expected frontend to be registered")
	}
	if got.Name() != "test-fe" {
		t.Fatalf("expected name 'test-fe', got %q", got.Name())
	}
}

func TestGetFrontendNotFound(t *testing.T) {
	frontendsMu.Lock()
	saved := frontends
	frontends = make(map[string]Frontend)
	frontendsMu.Unlock()
	defer func() {
		frontendsMu.Lock()
		frontends = saved
		frontendsMu.Unlock()
	}()

	_, ok := GetFrontend("nonexistent")
	if ok {
		t.Fatal("expected frontend to not be found")
	}
}

func TestRegisterFrontendDuplicatePanics(t *testing.T) {
	frontendsMu.Lock()
	saved := frontends
	frontends = make(map[string]Frontend)
	frontendsMu.Unlock()
	defer func() {
		frontendsMu.Lock()
		frontends = saved
		frontendsMu.Unlock()
	}()

	mock := &mockFrontend{name: "dupe"}
	RegisterFrontend(mock)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	RegisterFrontend(mock)
}

func TestRegisteredFrontends(t *testing.T) {
	frontendsMu.Lock()
	saved := frontends
	frontends = make(map[string]Frontend)
	frontendsMu.Unlock()
	defer func() {
		frontendsMu.Lock()
		frontends = saved
		frontendsMu.Unlock()
	}()

	RegisterFrontend(&mockFrontend{name: "alpha"})
	RegisterFrontend(&mockFrontend{name: "beta"})

	names := RegisteredFrontends()
	if len(names) != 2 {
		t.Fatalf("expected 2 frontends, got %d", len(names))
	}

	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["alpha"] || !nameSet["beta"] {
		t.Fatalf("expected alpha and beta, got %v", names)
	}
}

// --- Event Tests ---

func TestEventFields(t *testing.T) {
	ev := Event{
		Text:      "hello",
		IsFinal:   true,
		ToolCalls: []string{"search"},
	}

	if ev.Text != "hello" {
		t.Errorf("expected text 'hello', got %q", ev.Text)
	}
	if !ev.IsFinal {
		t.Error("expected IsFinal to be true")
	}
	if len(ev.ToolCalls) != 1 || ev.ToolCalls[0] != "search" {
		t.Errorf("expected tool call 'search', got %v", ev.ToolCalls)
	}
}

// --- Mock Frontend ---

type mockFrontend struct {
	name string
}

func (f *mockFrontend) Name() string { return f.name }

func (f *mockFrontend) Run(_ context.Context, _ *AgentBridge) error {
	return nil
}

// --- AgentBridge Tests ---

// stubLLM is a minimal model.LLM for testing AgentBridge without real API calls.
type stubLLM struct {
	responseText string
}

func (m *stubLLM) Name() string { return "stub-model" }

func (m *stubLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText(m.responseText, genai.RoleModel),
		}, nil)
	}
}

func TestNewAgentBridge(t *testing.T) {
	a, err := llmagent.New(llmagent.Config{
		Name:  "test-agent",
		Model: &stubLLM{responseText: "hello"},
	})
	if err != nil {
		t.Fatalf("creating test agent: %v", err)
	}

	bridge, err := NewAgentBridge(a)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bridge == nil {
		t.Fatal("expected non-nil bridge")
	}
}

func TestCreateSession(t *testing.T) {
	a, err := llmagent.New(llmagent.Config{
		Name:  "test-agent",
		Model: &stubLLM{responseText: "hello"},
	})
	if err != nil {
		t.Fatalf("creating test agent: %v", err)
	}

	bridge, err := NewAgentBridge(a)
	if err != nil {
		t.Fatalf("creating bridge: %v", err)
	}

	sessionID, err := bridge.CreateSession(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}
}

func TestChat(t *testing.T) {
	a, err := llmagent.New(llmagent.Config{
		Name:  "test-agent",
		Model: &stubLLM{responseText: "world"},
	})
	if err != nil {
		t.Fatalf("creating test agent: %v", err)
	}

	bridge, err := NewAgentBridge(a)
	if err != nil {
		t.Fatalf("creating bridge: %v", err)
	}

	sessionID, err := bridge.CreateSession(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}

	var events []Event
	for ev, err := range bridge.Chat(context.Background(), "test-user", sessionID, "hello") {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, ev)
	}

	if len(events) == 0 {
		t.Fatal("expected at least one event from Chat")
	}

	// At least one event should have text.
	hasText := false
	for _, ev := range events {
		if ev.Text != "" {
			hasText = true
			break
		}
	}
	if !hasText {
		t.Error("expected at least one event with text content")
	}
}
