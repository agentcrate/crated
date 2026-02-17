// Package middleware provides model decorators for cross-cutting concerns.
//
// These wrappers implement model.LLM and delegate to an inner model,
// adding logging, rate limiting, or other behavior transparently.
package middleware

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"time"

	"google.golang.org/adk/model"

	"github.com/agentcrate/crated/internal/ratelimit"
)

// --- Logging Middleware ---

// WithLogging wraps a model with structured request/response logging.
// Logs: model name, streaming mode, message count, tool count, duration,
// token usage, and any tool calls made.
func WithLogging(m model.LLM, logger *slog.Logger) model.LLM {
	return &loggingModel{inner: m, logger: logger}
}

type loggingModel struct {
	inner  model.LLM
	logger *slog.Logger
}

// Name implements model.LLM.
func (m *loggingModel) Name() string { return m.inner.Name() }

// GenerateContent implements model.LLM.
func (m *loggingModel) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		start := time.Now()

		// Count tools being sent.
		toolCount := 0
		if req.Config != nil {
			for _, t := range req.Config.Tools {
				toolCount += len(t.FunctionDeclarations)
			}
		}

		m.logger.Debug("LLM request",
			"streaming", streaming,
			"messages", len(req.Contents),
			"tools_available", toolCount,
		)

		var lastResp *model.LLMResponse
		chunks := 0
		hadError := false

		for resp, err := range m.inner.GenerateContent(ctx, req, streaming) {
			chunks++
			if err != nil {
				hadError = true
				m.logger.Error("LLM error",
					"duration_ms", time.Since(start).Milliseconds(),
					"error", err,
				)
			}
			if resp != nil {
				lastResp = resp
			}
			if !yield(resp, err) {
				return
			}
		}

		if hadError {
			return
		}

		// Log final response summary.
		attrs := []any{
			"duration_ms", time.Since(start).Milliseconds(),
			"chunks", chunks,
		}

		if lastResp != nil && lastResp.UsageMetadata != nil {
			attrs = append(attrs,
				"prompt_tokens", lastResp.UsageMetadata.PromptTokenCount,
				"completion_tokens", lastResp.UsageMetadata.CandidatesTokenCount,
				"total_tokens", lastResp.UsageMetadata.TotalTokenCount,
			)
		}

		if lastResp != nil && lastResp.Content != nil {
			var toolCalls []string
			for _, part := range lastResp.Content.Parts {
				if part.FunctionCall != nil {
					toolCalls = append(toolCalls, part.FunctionCall.Name)
				}
			}
			if len(toolCalls) > 0 {
				attrs = append(attrs, "tool_calls", toolCalls)
			}
		}

		m.logger.Debug("LLM response", attrs...)
	}
}

// --- Rate Limiting Middleware ---

// WithRateLimit wraps a model with a concurrent request limiter.
// Blocks until a slot is available or the context is canceled.
func WithRateLimit(m model.LLM, limiter *ratelimit.Limiter) model.LLM {
	return &rateLimitedModel{inner: m, limiter: limiter}
}

type rateLimitedModel struct {
	inner   model.LLM
	limiter *ratelimit.Limiter
}

// Name implements model.LLM.
func (m *rateLimitedModel) Name() string { return m.inner.Name() }

// GenerateContent implements model.LLM.
func (m *rateLimitedModel) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if err := m.limiter.Acquire(ctx); err != nil {
			yield(nil, fmt.Errorf("rate limit exceeded for model %s: %w", m.inner.Name(), err))
			return
		}
		defer m.limiter.Release()

		for resp, err := range m.inner.GenerateContent(ctx, req, streaming) {
			if !yield(resp, err) {
				return
			}
		}
	}
}
