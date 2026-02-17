package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/httpclient"
	"github.com/agentcrate/crated/internal/runtime"
)

// ConnectConfig is a type alias for convenience.
type ConnectConfig = runtime.ConnectConfig

// newTestModel creates an anthropicModel pointed at the given test server.
func newTestModel(ts *httptest.Server, mc agentfile.ModelConfig) *anthropicModel {
	return &anthropicModel{
		modelID: "claude-sonnet-test",
		apiKey:  "test-key",
		baseURL: ts.URL,
		config:  mc,
		client:  httpclient.New(httpclient.Options{MaxRetries: 0}),
	}
}

// --- Provider Tests ---

func TestProvider_Name(t *testing.T) {
	p := &provider{}
	if got := p.Name(); got != "anthropic" {
		t.Errorf("expected 'anthropic', got %q", got)
	}
}

func TestProvider_CreateModel(t *testing.T) {
	p := &provider{}
	m, err := p.CreateModel(context.Background(), "claude-sonnet-4-20250514", ConnectConfig{
		APIBase: "https://api.example.com/v1",
	}, agentfile.ModelConfig{Name: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name() != "anthropic/claude-sonnet-4-20250514" {
		t.Errorf("expected name 'anthropic/claude-sonnet-4-20250514', got %q", m.Name())
	}
}

// --- Model Name ---

func TestModel_Name(t *testing.T) {
	m := &anthropicModel{modelID: "claude-haiku-test"}
	if got := m.Name(); got != "anthropic/claude-haiku-test" {
		t.Errorf("expected 'anthropic/claude-haiku-test', got %q", got)
	}
}

// --- Sync Generation Tests ---

func TestGenerateSync_TextResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Anthropic-specific headers.
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key header")
		}
		if r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("expected anthropic-version header")
		}

		resp := messagesResponse{
			Content: []contentBlock{
				{Type: "text", Text: "Hello from Claude!"},
			},
			StopReason: "end_turn",
			Usage: &anthropicUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("hello", "user"),
		},
	}

	var responses []*model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[0].Content == nil || len(responses[0].Content.Parts) == 0 {
		t.Fatal("expected non-nil content with parts")
	}
	if responses[0].Content.Parts[0].Text != "Hello from Claude!" {
		t.Errorf("expected text 'Hello from Claude!', got %q", responses[0].Content.Parts[0].Text)
	}
	if !responses[0].TurnComplete {
		t.Error("expected TurnComplete=true")
	}
	if responses[0].UsageMetadata == nil {
		t.Fatal("expected usage metadata")
	}
}

func TestGenerateSync_ToolCallResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := messagesResponse{
			Content: []contentBlock{
				{
					Type:  "tool_use",
					ID:    "tool_1",
					Name:  "search",
					Input: map[string]any{"query": "weather"},
				},
			},
			StopReason: "tool_use",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("search for weather", "user"),
		},
	}

	var responses []*model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[0].Content == nil || len(responses[0].Content.Parts) == 0 {
		t.Fatal("expected content with parts")
	}
	if responses[0].Content.Parts[0].FunctionCall == nil {
		t.Fatal("expected function call in response")
	}
	if responses[0].Content.Parts[0].FunctionCall.Name != "search" {
		t.Errorf("expected function name 'search', got %q", responses[0].Content.Parts[0].FunctionCall.Name)
	}
}

func TestGenerateSync_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hello", "user")},
	}

	for _, err := range m.GenerateContent(context.Background(), req, false) {
		if err == nil {
			t.Fatal("expected error for API error response")
		}
		return
	}
	t.Fatal("expected at least one yield")
}

func TestGenerateSync_InvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hello", "user")},
	}

	for _, err := range m.GenerateContent(context.Background(), req, false) {
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
		return
	}
	t.Fatal("expected at least one yield")
}

// --- Streaming Generation Tests ---

func TestGenerateStreaming_TextResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []struct {
			typ  string
			data any
		}{
			{"content_block_start", contentBlockStartEvent{
				Index:        0,
				ContentBlock: contentBlock{Type: "text"},
			}},
			{"content_block_delta", contentBlockDeltaEvent{
				Index: 0,
				Delta: contentBlockDelta{Type: "text_delta", Text: "Hello "},
			}},
			{"content_block_delta", contentBlockDeltaEvent{
				Index: 0,
				Delta: contentBlockDelta{Type: "text_delta", Text: "world!"},
			}},
			{"content_block_stop", contentBlockStopEvent{Index: 0}},
			{"message_delta", messageDeltaEvent{
				Delta: messageDelta{StopReason: "end_turn"},
				Usage: &anthropicUsage{OutputTokens: 5},
			}},
			{"message_stop", struct{}{}},
		}

		for _, ev := range events {
			data, _ := json.Marshal(ev.data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.typ, data)
		}
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hello", "user")},
	}

	var responses []*model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) < 3 {
		t.Fatalf("expected at least 3 responses, got %d", len(responses))
	}

	// First text delta.
	if responses[0].Content.Parts[0].Text != "Hello " {
		t.Errorf("expected first chunk 'Hello ', got %q", responses[0].Content.Parts[0].Text)
	}

	// Last should be TurnComplete.
	last := responses[len(responses)-1]
	if !last.TurnComplete {
		t.Error("expected last response TurnComplete=true")
	}
	if last.UsageMetadata == nil {
		t.Error("expected usage metadata on final response")
	}
}

func TestGenerateStreaming_ToolUseResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []struct {
			typ  string
			data any
		}{
			{"content_block_start", contentBlockStartEvent{
				Index: 0,
				ContentBlock: contentBlock{
					Type: "tool_use",
					ID:   "tool_1",
					Name: "search",
				},
			}},
			{"content_block_delta", contentBlockDeltaEvent{
				Index: 0,
				Delta: contentBlockDelta{Type: "input_json_delta", PartialJSON: `{"q":"we`},
			}},
			{"content_block_delta", contentBlockDeltaEvent{
				Index: 0,
				Delta: contentBlockDelta{Type: "input_json_delta", PartialJSON: `ather"}`},
			}},
			{"content_block_stop", contentBlockStopEvent{Index: 0}},
			{"message_delta", messageDeltaEvent{
				Delta: messageDelta{StopReason: "tool_use"},
			}},
			{"message_stop", struct{}{}},
		}

		for _, ev := range events {
			data, _ := json.Marshal(ev.data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.typ, data)
		}
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("search for weather", "user")},
	}

	var responses []*model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) < 2 {
		t.Fatalf("expected at least 2 responses, got %d", len(responses))
	}

	// Find the tool call response (should be before the final).
	var foundToolCall bool
	for _, r := range responses {
		if r.Content != nil && len(r.Content.Parts) > 0 && r.Content.Parts[0].FunctionCall != nil {
			foundToolCall = true
			if r.Content.Parts[0].FunctionCall.Name != "search" {
				t.Errorf("expected function name 'search', got %q", r.Content.Parts[0].FunctionCall.Name)
			}
		}
	}
	if !foundToolCall {
		t.Error("expected a function call in stream responses")
	}

	last := responses[len(responses)-1]
	if !last.TurnComplete {
		t.Error("expected TurnComplete=true")
	}
}

func TestGenerateStreaming_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hi", "user")},
	}

	for _, err := range m.GenerateContent(context.Background(), req, true) {
		if err == nil {
			t.Fatal("expected error for server error")
		}
		return
	}
	t.Fatal("expected at least one yield")
}

// --- buildRequest Tests ---

func TestBuildRequest_WithConfig(t *testing.T) {
	temp := 0.7
	maxTok := 200
	topP := 0.9
	m := &anthropicModel{
		modelID: "claude-sonnet-4-20250514",
		config: agentfile.ModelConfig{
			Temperature: &temp,
			MaxTokens:   &maxTok,
			TopP:        &topP,
		},
	}

	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hello", "user")},
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "search",
					Description: "Search for info",
				}},
			}},
		},
	}

	anthropicReq := m.buildRequest(llmReq, true)

	if anthropicReq.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected model, got %q", anthropicReq.Model)
	}
	if anthropicReq.MaxTokens != 200 {
		t.Errorf("expected max_tokens=200, got %d", anthropicReq.MaxTokens)
	}
	if anthropicReq.Temperature == nil || *anthropicReq.Temperature != 0.7 {
		t.Error("expected temperature=0.7")
	}
	if anthropicReq.TopP == nil || *anthropicReq.TopP != 0.9 {
		t.Error("expected top_p=0.9")
	}
	if !anthropicReq.Stream {
		t.Error("expected stream=true")
	}
	if len(anthropicReq.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(anthropicReq.Tools))
	}
}

func TestBuildRequest_DefaultMaxTokens(t *testing.T) {
	m := &anthropicModel{
		modelID: "claude-sonnet-4-20250514",
		config:  agentfile.ModelConfig{},
	}

	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hello", "user")},
	}

	anthropicReq := m.buildRequest(llmReq, false)
	if anthropicReq.MaxTokens != 4096 {
		t.Errorf("expected default max_tokens=4096, got %d", anthropicReq.MaxTokens)
	}
}

func TestBuildRequest_SystemPrompt(t *testing.T) {
	m := &anthropicModel{
		modelID: "claude-sonnet-4-20250514",
		config:  agentfile.ModelConfig{},
	}

	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("You are a helpful assistant.", "system"),
			genai.NewContentFromText("hello", "user"),
		},
	}

	anthropicReq := m.buildRequest(llmReq, false)
	if anthropicReq.System != "You are a helpful assistant." {
		t.Errorf("expected system prompt, got %q", anthropicReq.System)
	}
}

// --- convertContents Tests ---

func TestConvertContents_BasicMessages(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("hello", "user"),
		genai.NewContentFromText("hi", "model"),
	}
	system, msgs := convertContents(contents)
	if system != "" {
		t.Errorf("expected no system, got %q", system)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msgs[1].Role)
	}
}

func TestConvertContents_ToolUseAndToolResult(t *testing.T) {
	contents := []*genai.Content{
		{
			Role: "model",
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					Name: "search",
					ID:   "tool_1",
					Args: map[string]any{"q": "hello"},
				},
			}},
		},
		{
			Role: "tool",
			Parts: []*genai.Part{{
				FunctionResponse: &genai.FunctionResponse{
					Name:     "search",
					ID:       "tool_1",
					Response: map[string]any{"result": "world"},
				},
			}},
		},
	}
	_, msgs := convertContents(contents)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msgs[0].Role)
	}
	if msgs[0].Content[0].Type != "tool_use" {
		t.Errorf("expected type 'tool_use', got %q", msgs[0].Content[0].Type)
	}
	// Tool result should be role "user" (Anthropic format).
	if msgs[1].Role != "user" {
		t.Errorf("expected role 'user' for tool result, got %q", msgs[1].Role)
	}
}

// --- mapStopReason Tests ---

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		input string
		want  genai.FinishReason
	}{
		{"end_turn", genai.FinishReasonStop},
		{"tool_use", genai.FinishReasonStop},
		{"max_tokens", genai.FinishReasonMaxTokens},
		{"unknown", genai.FinishReasonStop},
	}
	for _, tt := range tests {
		got := mapStopReason(tt.input)
		if got != tt.want {
			t.Errorf("mapStopReason(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// --- convertInput Tests ---

func TestConvertInput_MapDirect(t *testing.T) {
	input := map[string]any{"key": "value"}
	result := convertInput(input)
	if result["key"] != "value" {
		t.Errorf("expected key=value, got %v", result)
	}
}

func TestConvertInput_NonMap(t *testing.T) {
	input := struct {
		Key string `json:"key"`
	}{Key: "value"}
	result := convertInput(input)
	if result["key"] != "value" {
		t.Errorf("expected key=value, got %v", result)
	}
}

// --- mapRole Tests ---

func TestMapRole(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"user", "user"},
		{"model", "assistant"},
		{"system", "system"},
	}
	for _, tt := range tests {
		got := mapRole(tt.input)
		if got != tt.want {
			t.Errorf("mapRole(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
