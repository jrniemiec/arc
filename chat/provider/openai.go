package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/jrniemiec/arc/chat"
)

type OpenAIProvider struct {
	client openai.Client
	model  openai.ChatModel
}

func NewOpenAIProvider(model string) (*OpenAIProvider, error) {
	key := strings.TrimSpace(os.Getenv("ARC_OPENAI_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if key == "" {
		return nil, errors.New("OPENAI_API_KEY not set")
	}
	return &OpenAIProvider{
		client: openai.NewClient(option.WithAPIKey(key)),
		model:  openai.ChatModel(model),
	}, nil
}

func (p *OpenAIProvider) Name() string { return "openai:" + string(p.model) }

func (p *OpenAIProvider) buildMessages(systemPrompt string, messages []chat.Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, 1+len(messages))
	if sp := strings.TrimSpace(systemPrompt); sp != "" {
		out = append(out, openai.SystemMessage(sp))
	}
	for _, m := range messages {
		switch strings.ToLower(strings.TrimSpace(m.Role)) {
		case "system":
			out = append(out, openai.SystemMessage(m.Content))
		case "assistant":
			out = append(out, openai.AssistantMessage(m.Content))
		default:
			out = append(out, openai.UserMessage(m.Content))
		}
	}
	return out
}

func (p *OpenAIProvider) Chat(ctx context.Context, systemPrompt string, messages []chat.Message) (string, chat.Usage, error) {
	msgs := p.buildMessages(systemPrompt, messages)
	resp, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: msgs,
	})
	if err != nil {
		return "", chat.Usage{}, err
	}
	if len(resp.Choices) == 0 {
		return "", chat.Usage{}, errors.New("openai: empty response choices")
	}
	u := chat.Usage{
		InputTokens:  int(resp.Usage.PromptTokens),
		OutputTokens: int(resp.Usage.CompletionTokens),
	}
	return resp.Choices[0].Message.Content, u, nil
}

func (p *OpenAIProvider) ChatStream(
	ctx context.Context,
	systemPrompt string,
	messages []chat.Message,
	onDelta func(string) error,
) (string, chat.Usage, error) {
	msgs := p.buildMessages(systemPrompt, messages)
	stream := p.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:         p.model,
		Messages:      msgs,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	})

	var sb strings.Builder
	var u chat.Usage
	for stream.Next() {
		chunk := stream.Current()
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			u.InputTokens = int(chunk.Usage.PromptTokens)
			u.OutputTokens = int(chunk.Usage.CompletionTokens)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta.Content
		if d == "" {
			continue
		}
		sb.WriteString(d)
		if onDelta != nil {
			if err := onDelta(d); err != nil {
				return sb.String(), u, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return sb.String(), u, err
	}
	return sb.String(), u, nil
}

// buildOpenAIToolMessages translates internal Message types to the OpenAI wire
// format. tool-result messages become role:"tool". Assistant messages with
// ToolCalls become role:"assistant" with a tool_calls array.
func buildOpenAIToolMessages(systemPrompt string, messages []chat.Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, 1+len(messages))
	if sp := strings.TrimSpace(systemPrompt); sp != "" {
		out = append(out, openai.SystemMessage(sp))
	}
	for _, m := range messages {
		switch {
		case m.Role == chat.RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))

		case m.Role == chat.RoleToolResult:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))

		case m.Role == chat.RoleAssistant && len(m.ToolCalls) > 0:
			calls := make([]openai.ChatCompletionMessageToolCallUnionParam, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				calls[i] = openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: string(tc.Input),
						},
					},
				}
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Content: openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: openai.String(m.Content),
					},
					ToolCalls: calls,
				},
			})

		case m.Role == chat.RoleAssistant:
			out = append(out, openai.AssistantMessage(m.Content))

		default:
			out = append(out, openai.UserMessage(m.Content))
		}
	}
	return out
}

// toOpenAITools translates arc ToolDef slice to the OpenAI tools param format.
// OpenAI wraps each tool in a "function" object and uses "parameters" instead
// of "input_schema". FunctionParameters is map[string]any so we unmarshal the
// raw JSON schema.
func toOpenAITools(tools []chat.ToolDef) ([]openai.ChatCompletionToolUnionParam, error) {
	out := make([]openai.ChatCompletionToolUnionParam, len(tools))
	for i, t := range tools {
		var params openai.FunctionParameters
		if err := json.Unmarshal(t.InputSchema, &params); err != nil {
			return nil, fmt.Errorf("tool %q: unmarshal schema: %w", t.Name, err)
		}
		out[i] = openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  params,
		})
	}
	return out, nil
}

func (p *OpenAIProvider) ChatStreamWithTools(
	ctx context.Context,
	systemPrompt string,
	messages []chat.Message,
	tools []chat.ToolDef,
	onTextDelta func(string) error,
	onToolStart func(toolName string) error,
) (chat.StreamResponse, error) {
	msgs := buildOpenAIToolMessages(systemPrompt, messages)
	apiTools, err := toOpenAITools(tools)
	if err != nil {
		return chat.StreamResponse{}, err
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:         p.model,
		Messages:      msgs,
		Tools:         apiTools,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	})

	// Per-index accumulator for streamed tool call arguments.
	type toolAcc struct {
		id      string
		name    string
		argsBuf strings.Builder
		started bool // onToolStart already fired
	}
	accumulators := map[int64]*toolAcc{}

	var textBuf strings.Builder
	var u chat.Usage
	var stopReason string

	for stream.Next() {
		chunk := stream.Current()

		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			u.InputTokens = int(chunk.Usage.PromptTokens)
			u.OutputTokens = int(chunk.Usage.CompletionTokens)
		}

		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]

		if string(choice.FinishReason) != "" {
			stopReason = string(choice.FinishReason)
		}

		// Text delta.
		if d := choice.Delta.Content; d != "" {
			textBuf.WriteString(d)
			if onTextDelta != nil {
				if err := onTextDelta(d); err != nil {
					return chat.StreamResponse{}, err
				}
			}
		}

		// Tool call deltas (accumulated by index).
		for _, tc := range choice.Delta.ToolCalls {
			idx := tc.Index
			acc, ok := accumulators[idx]
			if !ok {
				acc = &toolAcc{}
				accumulators[idx] = acc
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				if !acc.started && acc.name != "" {
					acc.started = true
					if onToolStart != nil {
						if err := onToolStart(acc.name); err != nil {
							return chat.StreamResponse{}, err
						}
					}
				}
				acc.argsBuf.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return chat.StreamResponse{}, err
	}

	// Assemble ContentBlocks from accumulated data.
	var blocks []chat.ContentBlock
	if t := textBuf.String(); t != "" {
		blocks = append(blocks, chat.ContentBlock{Type: "text", Text: t})
	}
	// Emit tool_use blocks in index order.
	for i := int64(0); i < int64(len(accumulators)); i++ {
		acc, ok := accumulators[i]
		if !ok {
			continue
		}
		// Fire onToolStart for any tool whose name arrived before argument deltas
		// (edge case: name-only chunk with no argument deltas).
		if !acc.started && acc.name != "" && onToolStart != nil {
			_ = onToolStart(acc.name)
		}
		blocks = append(blocks, chat.ContentBlock{
			Type:         "tool_use",
			ToolUseID:    acc.id,
			ToolUseName:  acc.name,
			ToolUseInput: json.RawMessage(acc.argsBuf.String()),
		})
	}

	// Map OpenAI finish reasons to internal stop reasons.
	internalStop := stopReason
	switch stopReason {
	case "tool_calls":
		internalStop = "tool_use"
	case "stop":
		internalStop = "end_turn"
	}

	return chat.StreamResponse{
		Content:    blocks,
		StopReason: internalStop,
		Usage:      u,
	}, nil
}
