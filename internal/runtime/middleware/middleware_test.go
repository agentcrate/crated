package middleware_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/agentcrate/crated/internal/ratelimit"
	"github.com/agentcrate/crated/internal/runtime/middleware"
)

// --- Mock Model ---

type mockLLM struct {
	name      string
	responses []*model.LLMResponse
	err       error
}

func (m *mockLLM) Name() string { return m.name }

func (m *mockLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.err != nil {
			yield(nil, m.err)
			return
		}
		for _, r := range m.responses {
			if !yield(r, nil) {
				return
			}
		}
	}
}

// --- WithLogging Tests ---

func TestWithLogging_Name(t *testing.T) {
	inner := &mockLLM{name: "test-model"}
	logged := middleware.WithLogging(inner, slog.Default())
	if got := logged.Name(); got != "test-model" {
		t.Errorf("expected name 'test-model', got %q", got)
	}
}

func TestWithLogging_Success(t *testing.T) {
	inner := &mockLLM{
		name: "test-model",
		responses: []*model.LLMResponse{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{{Text: "hello"}},
				},
				UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
					PromptTokenCount:     10,
					CandidatesTokenCount: 5,
					TotalTokenCount:      15,
				},
			},
		},
	}

	logged := middleware.WithLogging(inner, slog.Default())
	req := &model.LLMRequest{Contents: []*genai.Content{}}

	var responses []*model.LLMResponse
	for resp, err := range logged.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[0].Content.Parts[0].Text != "hello" {
		t.Errorf("expected text 'hello', got %q", responses[0].Content.Parts[0].Text)
	}
}

func TestWithLogging_Error(t *testing.T) {
	inner := &mockLLM{
		name: "test-model",
		err:  fmt.Errorf("LLM failed"),
	}

	logged := middleware.WithLogging(inner, slog.Default())
	req := &model.LLMRequest{Contents: []*genai.Content{}}

	var gotErr error
	for _, err := range logged.GenerateContent(context.Background(), req, true) {
		if err != nil {
			gotErr = err
		}
	}

	if gotErr == nil {
		t.Fatal("expected error from logging middleware")
	}
}

func TestWithLogging_WithTools(t *testing.T) {
	inner := &mockLLM{
		name: "test-model",
		responses: []*model.LLMResponse{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{{
						FunctionCall: &genai.FunctionCall{Name: "search"},
					}},
				},
			},
		},
	}

	logged := middleware.WithLogging(inner, slog.Default())
	req := &model.LLMRequest{
		Contents: []*genai.Content{},
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{
				{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "search"}}},
			},
		},
	}

	var responses []*model.LLMResponse
	for resp, err := range logged.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
}

func TestWithLogging_NilContent(t *testing.T) {
	inner := &mockLLM{
		name:      "test-model",
		responses: []*model.LLMResponse{{}}, // nil Content
	}

	logged := middleware.WithLogging(inner, slog.Default())
	req := &model.LLMRequest{Contents: []*genai.Content{}}

	count := 0
	for _, err := range logged.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 response, got %d", count)
	}
}

func TestWithLogging_Streaming(t *testing.T) {
	inner := &mockLLM{
		name: "test-model",
		responses: []*model.LLMResponse{
			{Content: &genai.Content{Parts: []*genai.Part{{Text: "chunk1"}}}},
			{Content: &genai.Content{Parts: []*genai.Part{{Text: "chunk2"}}}},
		},
	}

	logged := middleware.WithLogging(inner, slog.Default())
	req := &model.LLMRequest{Contents: []*genai.Content{}}

	count := 0
	for _, err := range logged.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 chunks, got %d", count)
	}
}

// --- WithRateLimit Tests ---

func TestWithRateLimit_Name(t *testing.T) {
	inner := &mockLLM{name: "limited-model"}
	limited := middleware.WithRateLimit(inner, ratelimit.New(5))
	if got := limited.Name(); got != "limited-model" {
		t.Errorf("expected name 'limited-model', got %q", got)
	}
}

func TestWithRateLimit_PassesThrough(t *testing.T) {
	inner := &mockLLM{
		name: "test-model",
		responses: []*model.LLMResponse{
			{Content: &genai.Content{Parts: []*genai.Part{{Text: "ok"}}}},
		},
	}

	limited := middleware.WithRateLimit(inner, ratelimit.New(10))
	req := &model.LLMRequest{Contents: []*genai.Content{}}

	var responses []*model.LLMResponse
	for resp, err := range limited.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
}

func TestWithRateLimit_ContextCanceled(t *testing.T) {
	inner := &mockLLM{
		name:      "test-model",
		responses: []*model.LLMResponse{{}},
	}

	// Create a limiter with capacity 1.
	lim := ratelimit.New(1)
	// Fill it.
	if err := lim.Acquire(context.Background()); err != nil {
		t.Fatalf("pre-acquire failed: %v", err)
	}

	limited := middleware.WithRateLimit(inner, lim)
	req := &model.LLMRequest{Contents: []*genai.Content{}}

	// Use a canceled context so Acquire fails.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var gotErr error
	for _, err := range limited.GenerateContent(ctx, req, false) {
		if err != nil {
			gotErr = err
		}
	}

	if gotErr == nil {
		t.Fatal("expected error from rate-limited model with canceled context")
	}

	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("expected context.Canceled in error chain, got: %v", gotErr)
	}

	lim.Release()
}

func TestWithRateLimit_ReleasesAfterCompletion(t *testing.T) {
	inner := &mockLLM{
		name:      "test-model",
		responses: []*model.LLMResponse{{}},
	}

	lim := ratelimit.New(1)
	limited := middleware.WithRateLimit(inner, lim)
	req := &model.LLMRequest{Contents: []*genai.Content{}}

	// First call — should succeed.
	for _, err := range limited.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// If the slot wasn't released, this would block.
	stats := lim.Stats()
	if stats.Active != 0 {
		t.Errorf("expected 0 active after generate, got %d", stats.Active)
	}

	// Second call — should also succeed (slot was released).
	for _, err := range limited.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatalf("unexpected error on second call: %v", err)
		}
	}
}
