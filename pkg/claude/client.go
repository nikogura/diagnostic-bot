package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	anthropic "github.com/liushuangls/go-anthropic/v2"
	"github.com/nikogura/diagnostic-bot/pkg/metrics"
)

const (
	// ModelSonnet45 is the Claude Sonnet 4.5 model ID.
	ModelSonnet45 = "claude-sonnet-4-5-20250929"

	// MaxTokens is the maximum tokens for Claude responses.
	MaxTokens = 4096
)

// Client wraps the Anthropic API client with tool use support.
type Client struct {
	client *anthropic.Client
	logger *slog.Logger
	model  string
}

// NewClient creates a new Claude API client.
func NewClient(apiKey string, model string, logger *slog.Logger) (result *Client) {
	// Use default model if not specified
	if model == "" {
		model = ModelSonnet45
	}

	result = &Client{
		client: anthropic.NewClient(apiKey),
		logger: logger,
		model:  model,
	}

	return result
}

// MessageRequest represents a request to Claude with tool support.
type MessageRequest struct {
	SystemPrompt string
	Messages     []anthropic.Message
	Tools        []anthropic.ToolDefinition
	MaxTokens    int
}

// MessageResponse represents Claude's response including tool uses.
type MessageResponse struct {
	Content       []anthropic.MessageContent
	StopReason    string
	Usage         anthropic.MessagesUsage
	ToolUses      []ToolUse
	TextResponses []string
}

// ToolUse represents a tool that Claude wants to invoke.
type ToolUse struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// SendMessage sends a message to Claude and handles tool use responses.
func (c *Client) SendMessage(ctx context.Context, req MessageRequest) (result MessageResponse, err error) {
	var resp anthropic.MessagesResponse

	if req.MaxTokens == 0 {
		req.MaxTokens = MaxTokens
	}

	request := anthropic.MessagesRequest{
		Model:     anthropic.Model(c.model),
		Messages:  req.Messages,
		MaxTokens: req.MaxTokens,
		System:    req.SystemPrompt,
	}

	if len(req.Tools) > 0 {
		request.Tools = req.Tools
	}

	c.logger.InfoContext(ctx, "sending message to Claude",
		slog.Int("message_count", len(req.Messages)),
		slog.Int("tool_count", len(req.Tools)),
		slog.Int("max_tokens", req.MaxTokens),
		slog.Int("system_prompt_bytes", len(req.SystemPrompt)))

	resp, err = c.client.CreateMessages(ctx, request)
	if err != nil {
		metrics.RecordClaudeAPICall(ctx, "error")
		err = fmt.Errorf("calling Claude API: %w", err)
		return result, err
	}

	// Record successful API call and token usage
	metrics.RecordClaudeAPICall(ctx, "success")
	metrics.AddClaudeAPITokens(ctx, "input", int64(resp.Usage.InputTokens))
	metrics.AddClaudeAPITokens(ctx, "output", int64(resp.Usage.OutputTokens))

	c.logger.InfoContext(ctx, "received response from Claude",
		slog.String("stop_reason", string(resp.StopReason)),
		slog.Int("input_tokens", resp.Usage.InputTokens),
		slog.Int("output_tokens", resp.Usage.OutputTokens))

	// Parse response content
	var toolUses []ToolUse
	var textResponses []string

	for _, content := range resp.Content {
		//nolint:exhaustive // Only handling text and tool_use, other types are ignored
		switch content.Type {
		case "text":
			if content.Text != nil {
				textResponses = append(textResponses, *content.Text)
			}

		case "tool_use":
			if content.MessageContentToolUse != nil {
				toolUses = append(toolUses, ToolUse{
					ID:    content.MessageContentToolUse.ID,
					Name:  content.MessageContentToolUse.Name,
					Input: content.MessageContentToolUse.Input,
				})
			}

		default:
			// Ignore other content types (tool_result, image, etc.)
		}
	}

	result = MessageResponse{
		Content:       resp.Content,
		StopReason:    string(resp.StopReason),
		Usage:         resp.Usage,
		ToolUses:      toolUses,
		TextResponses: textResponses,
	}

	return result, err
}

// AppendUserMessage appends a user message to the message history.
func AppendUserMessage(messages []anthropic.Message, text string) (result []anthropic.Message) {
	result = append(messages, anthropic.Message{
		Role: anthropic.RoleUser,
		Content: []anthropic.MessageContent{
			{
				Type: "text",
				Text: &text,
			},
		},
	})

	return result
}

// AppendAssistantMessage appends an assistant message to the message history.
func AppendAssistantMessage(messages []anthropic.Message, content []anthropic.MessageContent) (result []anthropic.Message) {
	result = append(messages, anthropic.Message{
		Role:    anthropic.RoleAssistant,
		Content: content,
	})

	return result
}

// AppendToolResult appends a tool result to the message history.
func AppendToolResult(messages []anthropic.Message, toolUseID string, content string, isError bool) (result []anthropic.Message) {
	toolResult := anthropic.NewToolResultMessageContent(toolUseID, content, isError)

	result = append(messages, anthropic.Message{
		Role:    anthropic.RoleUser,
		Content: []anthropic.MessageContent{toolResult},
	})

	return result
}
