package repl

import (
	"context"
	"io"
	"iter"
	"os"
	"strings"
	"testing"

	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/agentcrate/crated/internal/frontend"
)

// --- Helpers ---

// stubLLM is a minimal model.LLM that returns a fixed response.
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

func createBridge(t *testing.T) *frontend.AgentBridge {
	t.Helper()
	a, err := llmagent.New(llmagent.Config{
		Name:  "test-agent",
		Model: &stubLLM{responseText: "reply"},
	})
	if err != nil {
		t.Fatalf("creating agent: %v", err)
	}
	bridge, err := frontend.NewAgentBridge(a)
	if err != nil {
		t.Fatalf("creating bridge: %v", err)
	}
	return bridge
}

// withStdin temporarily replaces os.Stdin with a reader containing input,
// and restores it after the test.
func withStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
		r.Close()
	})

	go func() {
		defer w.Close()
		_, _ = io.WriteString(w, input)
	}()
}

// --- Tests ---

func TestReplFrontend_Name(t *testing.T) {
	f := &replFrontend{}
	if got := f.Name(); got != "repl" {
		t.Errorf("expected name 'repl', got %q", got)
	}
}

func TestReplFrontend_ExitCommand(t *testing.T) {
	bridge := createBridge(t)
	withStdin(t, "exit\n")

	f := &replFrontend{}
	err := f.Run(context.Background(), bridge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReplFrontend_QuitCommand(t *testing.T) {
	bridge := createBridge(t)
	withStdin(t, "quit\n")

	f := &replFrontend{}
	err := f.Run(context.Background(), bridge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReplFrontend_EOF(t *testing.T) {
	bridge := createBridge(t)
	withStdin(t, "") // EOF immediately

	f := &replFrontend{}
	err := f.Run(context.Background(), bridge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReplFrontend_MessageThenExit(t *testing.T) {
	bridge := createBridge(t)
	withStdin(t, "hello\nexit\n")

	f := &replFrontend{}
	err := f.Run(context.Background(), bridge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReplFrontend_EmptyLineThenExit(t *testing.T) {
	bridge := createBridge(t)
	withStdin(t, "\n\nexit\n")

	f := &replFrontend{}
	err := f.Run(context.Background(), bridge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReplFrontend_CanceledContext(t *testing.T) {
	bridge := createBridge(t)
	// Don't write anything to stdin — context cancel should terminate.
	withStdin(t, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	f := &replFrontend{}
	err := f.Run(ctx, bridge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExitCommands(t *testing.T) {
	if !exitCommands["exit"] {
		t.Error("expected 'exit' to be an exit command")
	}
	if !exitCommands["quit"] {
		t.Error("expected 'quit' to be an exit command")
	}
	if exitCommands["hello"] {
		t.Error("expected 'hello' to not be an exit command")
	}
}

func TestReplFrontend_CaseInsensitiveExit(t *testing.T) {
	bridge := createBridge(t)
	withStdin(t, "EXIT\n")

	f := &replFrontend{}
	err := f.Run(context.Background(), bridge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReplFrontend_MultipleMessagesThenQuit(t *testing.T) {
	bridge := createBridge(t)
	withStdin(t, strings.Join([]string{
		"hello",
		"how are you",
		"quit",
	}, "\n")+"\n")

	f := &replFrontend{}
	err := f.Run(context.Background(), bridge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
