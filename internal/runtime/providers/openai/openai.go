// Package openai registers the "openai" model provider.
//
// Import this package for its side effects to make OpenAI models available:
//
//	import _ "github.com/agentcrate/crated/internal/runtime/providers/openai"
//
// Required environment variable: OPENAI_API_KEY
//
// Supported models include gpt-4o, gpt-4o-mini, gpt-4-turbo, o1, o3, etc.
// This provider also handles OpenAI-compatible APIs (Ollama, Azure, vLLM).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/httpclient"
	"github.com/agentcrate/crated/internal/runtime"
	"github.com/agentcrate/crated/internal/sse"
)

const defaultBaseURL = "https://api.openai.com/v1"
const defaultAPIKeyEnv = "OPENAI_API_KEY"

func init() {
	if err := runtime.RegisterProvider(&provider{}); err != nil {
		slog.Error("registering openai provider", "error", err)
	}
}

type provider struct{}

// Name implements runtime.ModelProvider.
func (p *provider) Name() string { return "openai" }

// CreateModel implements runtime.ModelProvider.
func (p *provider) CreateModel(_ context.Context, modelID string, cc runtime.ConnectConfig, mc agentfile.ModelConfig) (model.LLM, error) {
	keyEnv := cc.AuthEnvVar
	if keyEnv == "" {
		keyEnv = defaultAPIKeyEnv
	}
	apiKey := os.Getenv(keyEnv)

	// Resolve base URL: host env override > runtime config > default.
	base := ""
	if cc.HostEnvVar != "" {
		base = os.Getenv(cc.HostEnvVar)
		if base != "" {
			base = strings.TrimRight(base, "/") + "/v1"
		}
	}
	if base == "" {
		base = cc.APIBase
	}
	if base == "" {
		base = defaultBaseURL
	}

	logger := slog.Default().With("provider", "openai", "model", modelID)

	// Fail early if the API key is missing for cloud endpoints.
	// Local providers (Ollama, vLLM) don't need one.
	if apiKey == "" && base == defaultBaseURL {
		return nil, fmt.Errorf("%s is not set — required for model %q (set it in your environment or use --env-file)", keyEnv, modelID)
	}

	return &openaiModel{
		modelID: modelID,
		apiKey:  apiKey,
		baseURL: base,
		config:  mc,
		client: httpclient.New(httpclient.Options{
			Timeout: 120 * time.Second,
			Logger:  logger,
		}),
	}, nil
}

type openaiModel struct {
	modelID string
	apiKey  string
	baseURL string
	config  agentfile.ModelConfig
	client  *httpclient.Client
}

// Name implements model.LLM.
func (m *openaiModel) Name() string { return "openai/" + m.modelID }

// GenerateContent dispatches to streaming or non-streaming based on the flag.
func (m *openaiModel) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	if streaming {
		return m.generateStreaming(ctx, req)
	}
	return m.generateSync(ctx, req)
}

// --- Non-streaming (synchronous) ---

func (m *openaiModel) generateSync(ctx context.Context, req *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		oaiReq := m.buildRequest(req, false)

		body, err := json.Marshal(oaiReq)
		if err != nil {
			yield(nil, fmt.Errorf("marshaling openai request: %w", err))
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			yield(nil, fmt.Errorf("creating openai request: %w", err))
			return
		}
		m.setHeaders(httpReq)

		resp, err := m.client.Do(httpReq)
		if err != nil {
			yield(nil, fmt.Errorf("openai API call failed: %w", err))
			return
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, err := m.client.ReadBody(resp)
		if err != nil {
			yield(nil, fmt.Errorf("reading openai response: %w", err))
			return
		}

		if resp.StatusCode != http.StatusOK {
			yield(nil, fmt.Errorf("openai API error (HTTP %d): %s", resp.StatusCode, string(respBody)))
			return
		}

		var chatResp chatResponse
		if err := json.Unmarshal(respBody, &chatResp); err != nil {
			yield(nil, fmt.Errorf("unmarshaling openai response: %w", err))
			return
		}

		yield(convertSyncResponse(&chatResp), nil)
	}
}

// --- Streaming ---

func (m *openaiModel) generateStreaming(ctx context.Context, req *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		oaiReq := m.buildRequest(req, true)

		body, err := json.Marshal(oaiReq)
		if err != nil {
			yield(nil, fmt.Errorf("marshaling openai request: %w", err))
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			yield(nil, fmt.Errorf("creating openai request: %w", err))
			return
		}
		m.setHeaders(httpReq)

		resp, err := m.client.DoStream(httpReq)
		if err != nil {
			yield(nil, fmt.Errorf("openai stream failed: %w", err))
			return
		}
		defer func() { _ = resp.Body.Close() }()

		// Accumulate tool calls across stream chunks.
		var toolCalls []toolCallAccumulator
		var finalUsage *usage

		for event, err := range sse.Read(resp.Body) {
			if err != nil {
				yield(nil, fmt.Errorf("reading openai stream: %w", err))
				return
			}

			if event.Data == "[DONE]" {
				break
			}

			var chunk streamResponse
			if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
				yield(nil, fmt.Errorf("parsing openai stream chunk: %w", err))
				return
			}

			// Capture usage from the final chunk (requires stream_options.include_usage).
			if chunk.Usage != nil {
				finalUsage = chunk.Usage
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			delta := chunk.Choices[0].Delta

			// Handle text deltas — yield each chunk immediately.
			if delta.Content != "" {
				llmResp := &model.LLMResponse{
					Content: &genai.Content{
						Role:  "model",
						Parts: []*genai.Part{genai.NewPartFromText(delta.Content)},
					},
					TurnComplete: false,
				}
				if !yield(llmResp, nil) {
					return
				}
			}

			// Handle tool call deltas — accumulate arguments.
			for _, tc := range delta.ToolCalls {
				for len(toolCalls) <= tc.Index {
					toolCalls = append(toolCalls, toolCallAccumulator{})
				}
				if tc.ID != "" {
					toolCalls[tc.Index].ID = tc.ID
				}
				if tc.Function.Name != "" {
					toolCalls[tc.Index].Name = tc.Function.Name
				}
				toolCalls[tc.Index].Arguments += tc.Function.Arguments
			}

			// Check for stream end.
			if chunk.Choices[0].FinishReason != nil {
				// Build final response with accumulated tool calls and usage.
				finalResp := &model.LLMResponse{
					TurnComplete: true,
					FinishReason: mapFinishReason(*chunk.Choices[0].FinishReason),
				}

				if len(toolCalls) > 0 {
					content := &genai.Content{Role: "model"}
					for _, tc := range toolCalls {
						var args map[string]any
						if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
							slog.Default().Warn("malformed tool call arguments", "provider", "openai", "model", m.modelID, "tool", tc.Name, "error", err)
						}
						content.Parts = append(content.Parts, genai.NewPartFromFunctionCall(tc.Name, args))
					}
					finalResp.Content = content
				}

				if finalUsage != nil {
					finalResp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
						PromptTokenCount:     int32(finalUsage.PromptTokens),
						CandidatesTokenCount: int32(finalUsage.CompletionTokens),
						TotalTokenCount:      int32(finalUsage.TotalTokens),
					}
				}

				yield(finalResp, nil)
				return
			}
		}
	}
}

// --- Helpers ---

func (m *openaiModel) buildRequest(req *model.LLMRequest, stream bool) chatRequest {
	messages := convertContents(req.Contents)
	oaiReq := chatRequest{
		Model:    m.modelID,
		Messages: messages,
		Stream:   stream,
	}
	if stream {
		oaiReq.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if m.config.Temperature != nil {
		oaiReq.Temperature = m.config.Temperature
	}
	if m.config.MaxTokens != nil {
		oaiReq.MaxTokens = m.config.MaxTokens
	}
	if m.config.TopP != nil {
		oaiReq.TopP = m.config.TopP
	}
	if req.Config != nil && len(req.Config.Tools) > 0 {
		oaiReq.Tools = convertTools(req.Config.Tools)
	}
	return oaiReq
}

func (m *openaiModel) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
}

// --- OpenAI API types ---

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []message      `json:"messages"`
	Temperature   *float64       `json:"temperature,omitempty"`
	MaxTokens     *int           `json:"max_tokens,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	Tools         []oaiTool      `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string          `json:"type"`
	Function oaiToolFunction `json:"function"`
}

type oaiToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// Sync response types.
type chatResponse struct {
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage,omitempty"`
}

type choice struct {
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Streaming response types.
type streamResponse struct {
	Choices []streamChoice `json:"choices"`
	Usage   *usage         `json:"usage,omitempty"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []streamToolCall `json:"tool_calls,omitempty"`
}

type streamToolCall struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function streamFunctionCall `json:"function,omitempty"`
}

type streamFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// toolCallAccumulator buffers tool call data across stream chunks.
type toolCallAccumulator struct {
	ID        string
	Name      string
	Arguments string
}

// --- Conversion helpers ---

func convertContents(contents []*genai.Content) []message {
	var msgs []message
	for _, c := range contents {
		role := mapRole(c.Role)
		text := extractText(c)

		msg := message{Role: role, Content: text}

		if role == "tool" && len(c.Parts) > 0 {
			if c.Parts[0].FunctionResponse != nil {
				fr := c.Parts[0].FunctionResponse
				msg.ToolCallID = fr.ID
				if msg.ToolCallID == "" {
					msg.ToolCallID = fr.Name
				}
				respBytes, _ := json.Marshal(fr.Response)
				msg.Content = string(respBytes)
			}
		}

		if role == "assistant" {
			for _, part := range c.Parts {
				if part.FunctionCall == nil {
					continue
				}
				fc := part.FunctionCall
				argsBytes, _ := json.Marshal(fc.Args)
				callID := fc.ID
				if callID == "" {
					callID = fc.Name
				}
				msg.ToolCalls = append(msg.ToolCalls, toolCall{
					ID:   callID,
					Type: "function",
					Function: functionCall{
						Name:      fc.Name,
						Arguments: string(argsBytes),
					},
				})
			}
			if len(msg.ToolCalls) > 0 {
				msg.Content = ""
			}
		}

		msgs = append(msgs, msg)
	}
	return msgs
}

func mapRole(role string) string {
	switch role {
	case "user":
		return "user"
	case "model":
		return "assistant"
	default:
		return role
	}
}

func extractText(c *genai.Content) string {
	var sb strings.Builder
	for _, part := range c.Parts {
		if part.Text != "" {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

func convertTools(genaiTools []*genai.Tool) []oaiTool {
	var tools []oaiTool
	for _, t := range genaiTools {
		for _, fd := range t.FunctionDeclarations {
			tools = append(tools, oaiTool{
				Type: "function",
				Function: oaiToolFunction{
					Name:        fd.Name,
					Description: fd.Description,
					Parameters:  fd.Parameters,
				},
			})
		}
	}
	return tools
}

func convertSyncResponse(resp *chatResponse) *model.LLMResponse {
	if len(resp.Choices) == 0 {
		return &model.LLMResponse{
			TurnComplete: true,
			FinishReason: genai.FinishReasonStop,
		}
	}

	ch := resp.Choices[0]
	content := &genai.Content{Role: "model"}

	if len(ch.Message.ToolCalls) > 0 {
		for _, tc := range ch.Message.ToolCalls {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				slog.Warn("malformed tool call arguments", "tool", tc.Function.Name, "error", err)
			}
			content.Parts = append(content.Parts, genai.NewPartFromFunctionCall(tc.Function.Name, args))
		}
	} else if ch.Message.Content != "" {
		content.Parts = append(content.Parts, genai.NewPartFromText(ch.Message.Content))
	}

	llmResp := &model.LLMResponse{
		Content:      content,
		TurnComplete: true,
		FinishReason: mapFinishReason(ch.FinishReason),
	}

	if resp.Usage != nil {
		llmResp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(resp.Usage.PromptTokens),
			CandidatesTokenCount: int32(resp.Usage.CompletionTokens),
			TotalTokenCount:      int32(resp.Usage.TotalTokens),
		}
	}

	return llmResp
}

func mapFinishReason(reason string) genai.FinishReason {
	switch reason {
	case "stop":
		return genai.FinishReasonStop
	case "tool_calls":
		return genai.FinishReasonStop
	case "length":
		return genai.FinishReasonMaxTokens
	case "content_filter":
		return genai.FinishReasonSafety
	default:
		return genai.FinishReasonStop
	}
}
