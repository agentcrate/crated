package openai

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

// newTestModel creates an openaiModel pointed at the given test server.
func newTestModel(ts *httptest.Server, mc agentfile.ModelConfig) *openaiModel {
	return &openaiModel{
		modelID: "gpt-4o-test",
		apiKey:  "test-key",
		baseURL: ts.URL,
		config:  mc,
		client:  httpclient.New(httpclient.Options{MaxRetries: 0}),
	}
}

// --- Provider Tests ---

func TestProvider_Name(t *testing.T) {
	p := &provider{}
	if got := p.Name(); got != "openai" {
		t.Errorf("expected 'openai', got %q", got)
	}
}

func TestProvider_CreateModel(t *testing.T) {
	p := &provider{}
	m, err := p.CreateModel(context.Background(), "gpt-4o", ConnectConfig{
		APIBase: "https://api.example.com/v1",
	}, agentfile.ModelConfig{Name: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name() != "openai/gpt-4o" {
		t.Errorf("expected name 'openai/gpt-4o', got %q", m.Name())
	}
}

// --- Model Name ---

func TestModel_Name(t *testing.T) {
	m := &openaiModel{modelID: "gpt-4o-mini"}
	if got := m.Name(); got != "openai/gpt-4o-mini" {
		t.Errorf("expected 'openai/gpt-4o-mini', got %q", got)
	}
}

// --- Sync Generation Tests ---

func TestGenerateSync_TextResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers.
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer auth header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json")
		}

		resp := chatResponse{
			Choices: []choice{{
				Message:      message{Role: "assistant", Content: "Hello from GPT!"},
				FinishReason: "stop",
			}},
			Usage: &usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
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
	if responses[0].Content.Parts[0].Text != "Hello from GPT!" {
		t.Errorf("expected text 'Hello from GPT!', got %q", responses[0].Content.Parts[0].Text)
	}
	if !responses[0].TurnComplete {
		t.Error("expected TurnComplete=true")
	}
	if responses[0].UsageMetadata == nil {
		t.Fatal("expected usage metadata")
	}
	if responses[0].UsageMetadata.TotalTokenCount != 15 {
		t.Errorf("expected total tokens 15, got %d", responses[0].UsageMetadata.TotalTokenCount)
	}
}

func TestGenerateSync_ToolCallResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := chatResponse{
			Choices: []choice{{
				Message: message{
					Role: "assistant",
					ToolCalls: []toolCall{{
						ID:   "call_123",
						Type: "function",
						Function: functionCall{
							Name:      "search",
							Arguments: `{"query":"weather"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
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
		t.Fatal("expected content with function call parts")
	}
	if responses[0].Content.Parts[0].FunctionCall == nil {
		t.Fatal("expected function call in response")
	}
	if responses[0].Content.Parts[0].FunctionCall.Name != "search" {
		t.Errorf("expected function name 'search', got %q", responses[0].Content.Parts[0].FunctionCall.Name)
	}
}

func TestGenerateSync_EmptyChoices(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := chatResponse{Choices: []choice{}}
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
	if !responses[0].TurnComplete {
		t.Error("expected TurnComplete=true for empty choices")
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
		Contents: []*genai.Content{
			genai.NewContentFromText("hello", "user"),
		},
	}

	for _, err := range m.GenerateContent(context.Background(), req, false) {
		if err == nil {
			t.Fatal("expected error for API error response")
		}
		return // got the expected error
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
		Contents: []*genai.Content{
			genai.NewContentFromText("hello", "user"),
		},
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
	stop := "stop"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		chunks := []streamResponse{
			{Choices: []streamChoice{{Delta: streamDelta{Content: "Hello "}, FinishReason: nil}}},
			{Choices: []streamChoice{{Delta: streamDelta{Content: "world!"}, FinishReason: &stop}},
				Usage: &usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
		}

		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("hello", "user"),
		},
	}

	var responses []*model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	// Should get: text chunk "Hello ", then final response (with finish reason).
	if len(responses) < 2 {
		t.Fatalf("expected at least 2 responses, got %d", len(responses))
	}

	// First chunk should be a text delta.
	if responses[0].Content.Parts[0].Text != "Hello " {
		t.Errorf("expected first chunk 'Hello ', got %q", responses[0].Content.Parts[0].Text)
	}

	// Last response should be TurnComplete.
	last := responses[len(responses)-1]
	if !last.TurnComplete {
		t.Error("expected last response TurnComplete=true")
	}
}

func TestGenerateStreaming_ToolCallResponse(t *testing.T) {
	stop := "tool_calls"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		chunks := []streamResponse{
			{Choices: []streamChoice{{Delta: streamDelta{
				ToolCalls: []streamToolCall{{
					Index: 0, ID: "call_1", Function: streamFunctionCall{Name: "search"},
				}},
			}}}},
			{Choices: []streamChoice{{Delta: streamDelta{
				ToolCalls: []streamToolCall{{
					Index: 0, Function: streamFunctionCall{Arguments: `{"q":"weather`},
				}},
			}}}},
			{Choices: []streamChoice{{Delta: streamDelta{
				ToolCalls: []streamToolCall{{
					Index: 0, Function: streamFunctionCall{Arguments: `"}`},
				}},
			}}}},
			{Choices: []streamChoice{{Delta: streamDelta{}, FinishReason: &stop}}},
		}

		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("search for weather", "user"),
		},
	}

	var responses []*model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) == 0 {
		t.Fatal("expected at least 1 response")
	}

	last := responses[len(responses)-1]
	if !last.TurnComplete {
		t.Error("expected TurnComplete=true")
	}
	if last.Content == nil || len(last.Content.Parts) == 0 {
		t.Fatal("expected content with tool call parts")
	}
	if last.Content.Parts[0].FunctionCall == nil {
		t.Fatal("expected function call in final response")
	}
	if last.Content.Parts[0].FunctionCall.Name != "search" {
		t.Errorf("expected function name 'search', got %q", last.Content.Parts[0].FunctionCall.Name)
	}
}

func TestGenerateStreaming_EmptyChoices(t *testing.T) {
	stop := "stop"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Chunk with no choices (should be skipped), then a normal final.
		chunks := []streamResponse{
			{Choices: []streamChoice{}},
			{Usage: &usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}},
			{Choices: []streamChoice{{Delta: streamDelta{Content: "hi"}, FinishReason: &stop}}},
		}
		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	m := newTestModel(ts, agentfile.ModelConfig{})
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hi", "user")},
	}

	var count int
	for _, err := range m.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		count++
	}
	if count == 0 {
		t.Fatal("expected at least 1 response")
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
	maxTok := 100
	topP := 0.9
	m := &openaiModel{
		modelID: "gpt-4o",
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

	oaiReq := m.buildRequest(llmReq, true)

	if oaiReq.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", oaiReq.Model)
	}
	if oaiReq.Temperature == nil || *oaiReq.Temperature != 0.7 {
		t.Error("expected temperature 0.7")
	}
	if oaiReq.MaxTokens == nil || *oaiReq.MaxTokens != 100 {
		t.Error("expected max_tokens 100")
	}
	if oaiReq.TopP == nil || *oaiReq.TopP != 0.9 {
		t.Error("expected top_p 0.9")
	}
	if !oaiReq.Stream {
		t.Error("expected stream=true")
	}
	if oaiReq.StreamOptions == nil || !oaiReq.StreamOptions.IncludeUsage {
		t.Error("expected stream_options.include_usage=true")
	}
	if len(oaiReq.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(oaiReq.Tools))
	}
}

// --- convertContents Tests ---

func TestConvertContents_BasicMessages(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("hello", "user"),
		genai.NewContentFromText("hi", "model"),
	}
	msgs := convertContents(contents)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi" {
		t.Errorf("msg[1] = %+v", msgs[1])
	}
}

func TestConvertContents_ToolCallsAndResponses(t *testing.T) {
	contents := []*genai.Content{
		{
			Role: "model",
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					Name: "search",
					ID:   "call_1",
					Args: map[string]any{"q": "hello"},
				},
			}},
		},
		{
			Role: "tool",
			Parts: []*genai.Part{{
				FunctionResponse: &genai.FunctionResponse{
					Name:     "search",
					ID:       "call_1",
					Response: map[string]any{"result": "world"},
				},
			}},
		},
	}
	msgs := convertContents(contents)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Assistant with tool call.
	if msgs[0].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msgs[0].Role)
	}
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msgs[0].ToolCalls))
	}
	if msgs[0].ToolCalls[0].Function.Name != "search" {
		t.Errorf("expected tool name 'search', got %q", msgs[0].ToolCalls[0].Function.Name)
	}

	// Tool response.
	if msgs[1].Role != "tool" {
		t.Errorf("expected role 'tool', got %q", msgs[1].Role)
	}
	if msgs[1].ToolCallID != "call_1" {
		t.Errorf("expected tool call ID 'call_1', got %q", msgs[1].ToolCallID)
	}
}

// --- mapFinishReason Tests ---

func TestMapFinishReason(t *testing.T) {
	tests := []struct {
		input string
		want  genai.FinishReason
	}{
		{"stop", genai.FinishReasonStop},
		{"tool_calls", genai.FinishReasonStop},
		{"length", genai.FinishReasonMaxTokens},
		{"content_filter", genai.FinishReasonSafety},
		{"unknown", genai.FinishReasonStop},
	}
	for _, tt := range tests {
		got := mapFinishReason(tt.input)
		if got != tt.want {
			t.Errorf("mapFinishReason(%q) = %v, want %v", tt.input, got, tt.want)
		}
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
		{"tool", "tool"},
	}
	for _, tt := range tests {
		got := mapRole(tt.input)
		if got != tt.want {
			t.Errorf("mapRole(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- ConnectConfig type alias for import simplicity ---
type ConnectConfig = runtime.ConnectConfig
