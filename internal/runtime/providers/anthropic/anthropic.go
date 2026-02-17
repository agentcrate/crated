// Package anthropic registers the "anthropic" model provider.
//
// Import this package for its side effects to make Anthropic models available:
//
//	import _ "github.com/agentcrate/crated/internal/runtime/providers/anthropic"
//
// Required environment variable: ANTHROPIC_API_KEY
//
// Supported models include claude-sonnet-4-20250514, claude-3.5-sonnet, claude-3-opus, etc.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"os"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/agentcrate/agentfile"
	"github.com/agentcrate/crated/internal/httpclient"
	"github.com/agentcrate/crated/internal/runtime"
	"github.com/agentcrate/crated/internal/sse"
)

const (
	defaultBaseURL   = "https://api.anthropic.com/v1"
	defaultAPIKeyEnv = "ANTHROPIC_API_KEY"
	apiVersion       = "2023-06-01"
)

func init() {
	runtime.RegisterProvider(&provider{})
}

type provider struct{}

// Name implements runtime.ModelProvider.
func (p *provider) Name() string { return "anthropic" }

// CreateModel implements runtime.ModelProvider.
func (p *provider) CreateModel(_ context.Context, modelID string, cc runtime.ConnectConfig, mc agentfile.ModelConfig) (model.LLM, error) {
	keyEnv := cc.AuthEnvVar
	if keyEnv == "" {
		keyEnv = defaultAPIKeyEnv
	}
	apiKey := os.Getenv(keyEnv)

	base := cc.APIBase
	if base == "" {
		base = defaultBaseURL
	}

	logger := slog.Default().With("provider", "anthropic", "model", modelID)

	return &anthropicModel{
		modelID: modelID,
		apiKey:  apiKey,
		baseURL: base,
		config:  mc,
		client: httpclient.New(httpclient.Options{
			Timeout: 120 * time.Second,
			Logger:  logger,
		}),
		logger: logger,
	}, nil
}

type anthropicModel struct {
	modelID string
	apiKey  string
	baseURL string
	config  agentfile.ModelConfig
	client  *httpclient.Client
	logger  *slog.Logger
}

// Name implements model.LLM.
func (m *anthropicModel) Name() string { return "anthropic/" + m.modelID }

// GenerateContent dispatches to streaming or non-streaming based on the flag.
func (m *anthropicModel) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	if streaming {
		return m.generateStreaming(ctx, req)
	}
	return m.generateSync(ctx, req)
}

// --- Non-streaming (synchronous) ---

func (m *anthropicModel) generateSync(ctx context.Context, req *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		anthropicReq := m.buildRequest(req, false)

		body, err := json.Marshal(anthropicReq)
		if err != nil {
			yield(nil, fmt.Errorf("marshaling anthropic request: %w", err))
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/messages", bytes.NewReader(body))
		if err != nil {
			yield(nil, fmt.Errorf("creating anthropic request: %w", err))
			return
		}
		m.setHeaders(httpReq)

		resp, err := m.client.Do(httpReq)
		if err != nil {
			yield(nil, fmt.Errorf("anthropic API call failed: %w", err))
			return
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, err := m.client.ReadBody(resp)
		if err != nil {
			yield(nil, fmt.Errorf("reading anthropic response: %w", err))
			return
		}

		if resp.StatusCode != http.StatusOK {
			yield(nil, fmt.Errorf("anthropic API error (HTTP %d): %s", resp.StatusCode, string(respBody)))
			return
		}

		var msgResp messagesResponse
		if err := json.Unmarshal(respBody, &msgResp); err != nil {
			yield(nil, fmt.Errorf("unmarshaling anthropic response: %w", err))
			return
		}

		yield(convertSyncResponse(&msgResp), nil)
	}
}

// --- Streaming ---

func (m *anthropicModel) generateStreaming(ctx context.Context, req *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		anthropicReq := m.buildRequest(req, true)

		body, err := json.Marshal(anthropicReq)
		if err != nil {
			yield(nil, fmt.Errorf("marshaling anthropic request: %w", err))
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/messages", bytes.NewReader(body))
		if err != nil {
			yield(nil, fmt.Errorf("creating anthropic request: %w", err))
			return
		}
		m.setHeaders(httpReq)

		resp, err := m.client.DoStream(httpReq)
		if err != nil {
			yield(nil, fmt.Errorf("anthropic stream failed: %w", err))
			return
		}
		defer func() { _ = resp.Body.Close() }()

		// Track active content blocks for tool_use accumulation.
		var activeBlocks []contentBlockAccumulator
		var finalUsage *anthropicUsage
		var stopReason string

		for event, err := range sse.Read(resp.Body) {
			if err != nil {
				yield(nil, fmt.Errorf("reading anthropic stream: %w", err))
				return
			}

			switch event.Type {
			case "content_block_start":
				var ev contentBlockStartEvent
				if err := json.Unmarshal([]byte(event.Data), &ev); err != nil {
					yield(nil, fmt.Errorf("parsing content_block_start: %w", err))
					return
				}
				// Extend activeBlocks to hold this index.
				for len(activeBlocks) <= ev.Index {
					activeBlocks = append(activeBlocks, contentBlockAccumulator{})
				}
				activeBlocks[ev.Index] = contentBlockAccumulator{
					Type: ev.ContentBlock.Type,
					ID:   ev.ContentBlock.ID,
					Name: ev.ContentBlock.Name,
				}

			case "content_block_delta":
				var ev contentBlockDeltaEvent
				if err := json.Unmarshal([]byte(event.Data), &ev); err != nil {
					yield(nil, fmt.Errorf("parsing content_block_delta: %w", err))
					return
				}

				switch ev.Delta.Type {
				case "text_delta":
					// Yield text chunks immediately.
					llmResp := &model.LLMResponse{
						Content: &genai.Content{
							Role:  "model",
							Parts: []*genai.Part{genai.NewPartFromText(ev.Delta.Text)},
						},
						TurnComplete: false,
					}
					if !yield(llmResp, nil) {
						return
					}

				case "input_json_delta":
					// Accumulate tool input JSON across chunks.
					if ev.Index < len(activeBlocks) {
						activeBlocks[ev.Index].InputJSON += ev.Delta.PartialJSON
					}
				}

			case "content_block_stop":
				var ev contentBlockStopEvent
				if err := json.Unmarshal([]byte(event.Data), &ev); err != nil {
					yield(nil, fmt.Errorf("parsing content_block_stop: %w", err))
					return
				}

				// If it was a tool_use block, yield the complete tool call now.
				if ev.Index < len(activeBlocks) && activeBlocks[ev.Index].Type == "tool_use" {
					block := activeBlocks[ev.Index]
					var args map[string]any
					_ = json.Unmarshal([]byte(block.InputJSON), &args)

					llmResp := &model.LLMResponse{
						Content: &genai.Content{
							Role:  "model",
							Parts: []*genai.Part{genai.NewPartFromFunctionCall(block.Name, args)},
						},
						TurnComplete: false,
					}
					if !yield(llmResp, nil) {
						return
					}
				}

			case "message_delta":
				var ev messageDeltaEvent
				if err := json.Unmarshal([]byte(event.Data), &ev); err != nil {
					yield(nil, fmt.Errorf("parsing message_delta: %w", err))
					return
				}
				stopReason = ev.Delta.StopReason
				if ev.Usage != nil {
					finalUsage = ev.Usage
				}

			case "message_stop":
				// Stream is complete. Yield the final response.
				finalResp := &model.LLMResponse{
					TurnComplete: true,
					FinishReason: mapStopReason(stopReason),
				}
				if finalUsage != nil {
					finalResp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
						CandidatesTokenCount: int32(finalUsage.OutputTokens),
					}
				}
				yield(finalResp, nil)
				return

				// message_start, ping — no action needed.
			}
		}
	}
}

// --- Helpers ---

func (m *anthropicModel) buildRequest(req *model.LLMRequest, stream bool) messagesRequest {
	system, messages := convertContents(req.Contents)

	anthropicReq := messagesRequest{
		Model:    m.modelID,
		Messages: messages,
		Stream:   stream,
	}

	if system != "" {
		anthropicReq.System = system
	}

	// Max tokens is required by the Anthropic API.
	if m.config.MaxTokens != nil {
		anthropicReq.MaxTokens = *m.config.MaxTokens
	} else {
		anthropicReq.MaxTokens = 4096
	}

	if m.config.Temperature != nil {
		anthropicReq.Temperature = m.config.Temperature
	}
	if m.config.TopP != nil {
		anthropicReq.TopP = m.config.TopP
	}

	if req.Config != nil && len(req.Config.Tools) > 0 {
		anthropicReq.Tools = convertTools(req.Config.Tools)
	}

	return anthropicReq
}

func (m *anthropicModel) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("x-api-key", m.apiKey)
	}
	req.Header.Set("anthropic-version", apiVersion)
}

// --- Anthropic API types ---

type messagesRequest struct {
	Model       string          `json:"model"`
	Messages    []message       `json:"messages"`
	System      string          `json:"system,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Tools       []anthropicTool `json:"tools,omitempty"`
	Stream      bool            `json:"stream"`
}

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

// Sync response types.
type messagesResponse struct {
	Content    []contentBlock  `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      *anthropicUsage `json:"usage,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Streaming event types.
type contentBlockStartEvent struct {
	Index        int          `json:"index"`
	ContentBlock contentBlock `json:"content_block"`
}

type contentBlockDeltaEvent struct {
	Index int               `json:"index"`
	Delta contentBlockDelta `json:"delta"`
}

type contentBlockDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type contentBlockStopEvent struct {
	Index int `json:"index"`
}

type messageDeltaEvent struct {
	Delta messageDelta    `json:"delta"`
	Usage *anthropicUsage `json:"usage,omitempty"`
}

type messageDelta struct {
	StopReason string `json:"stop_reason"`
}

// contentBlockAccumulator buffers tool_use input across stream chunks.
type contentBlockAccumulator struct {
	Type      string
	ID        string
	Name      string
	InputJSON string
}

// --- Conversion helpers ---

func convertContents(contents []*genai.Content) (system string, messages []message) {
	for _, c := range contents {
		role := mapRole(c.Role)

		if role == "system" {
			system = extractText(c)
			continue
		}

		var blocks []contentBlock

		for _, part := range c.Parts {
			switch {
			case part.Text != "":
				blocks = append(blocks, contentBlock{
					Type: "text",
					Text: part.Text,
				})
			case part.FunctionCall != nil:
				fc := part.FunctionCall
				blocks = append(blocks, contentBlock{
					Type:  "tool_use",
					ID:    fc.ID,
					Name:  fc.Name,
					Input: fc.Args,
				})
			case part.FunctionResponse != nil:
				fr := part.FunctionResponse
				respBytes, _ := json.Marshal(fr.Response)
				toolID := fr.ID
				if toolID == "" {
					toolID = fr.Name
				}
				blocks = append(blocks, contentBlock{
					Type:      "tool_result",
					ToolUseID: toolID,
					Content:   string(respBytes),
				})
			}
		}

		if len(blocks) > 0 {
			if blocks[0].Type == "tool_result" {
				role = "user"
			}
			messages = append(messages, message{
				Role:    role,
				Content: blocks,
			})
		}
	}
	return system, messages
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
	for _, part := range c.Parts {
		if part.Text != "" {
			return part.Text
		}
	}
	return ""
}

func convertTools(genaiTools []*genai.Tool) []anthropicTool {
	var tools []anthropicTool
	for _, t := range genaiTools {
		for _, fd := range t.FunctionDeclarations {
			tools = append(tools, anthropicTool{
				Name:        fd.Name,
				Description: fd.Description,
				InputSchema: fd.Parameters,
			})
		}
	}
	return tools
}

func convertSyncResponse(resp *messagesResponse) *model.LLMResponse {
	content := &genai.Content{Role: "model"}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			content.Parts = append(content.Parts, genai.NewPartFromText(block.Text))
		case "tool_use":
			args := convertInput(block.Input)
			content.Parts = append(content.Parts, genai.NewPartFromFunctionCall(block.Name, args))
		}
	}

	llmResp := &model.LLMResponse{
		Content:      content,
		TurnComplete: true,
		FinishReason: mapStopReason(resp.StopReason),
	}

	if resp.Usage != nil {
		llmResp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(resp.Usage.InputTokens),
			CandidatesTokenCount: int32(resp.Usage.OutputTokens),
			TotalTokenCount:      int32(resp.Usage.InputTokens + resp.Usage.OutputTokens),
		}
	}

	return llmResp
}

func convertInput(input any) map[string]any {
	if m, ok := input.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func mapStopReason(reason string) genai.FinishReason {
	switch reason {
	case "end_turn":
		return genai.FinishReasonStop
	case "tool_use":
		return genai.FinishReasonStop
	case "max_tokens":
		return genai.FinishReasonMaxTokens
	default:
		return genai.FinishReasonStop
	}
}
