package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jrniemiec/arc/chat"
)

const (
	anthropicAPIBase = "https://api.anthropic.com/v1/messages"
	anthropicVersion = "2023-06-01"
	defaultMaxOutput = 4096
)

type AnthropicProvider struct {
	apiKey          string
	model           string
	maxOutputTokens int
	http            *http.Client
}

func NewAnthropicProvider(model string, maxOutputTokens int) (*AnthropicProvider, error) {
	key := strings.TrimSpace(os.Getenv("ARC_ANTHROPIC_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}
	if key == "" {
		return nil, errors.New("ANTHROPIC_API_KEY not set")
	}
	if maxOutputTokens <= 0 {
		maxOutputTokens = defaultMaxOutput
	}
	return &AnthropicProvider{
		apiKey:          key,
		model:           model,
		maxOutputTokens: maxOutputTokens,
		http: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     false,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}, nil
}

func (p *AnthropicProvider) Name() string { return "anthropic:" + p.model }

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicReq struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system,omitempty"`
	Messages  []anthropicMsg `json:"messages"`
	Stream    bool           `json:"stream,omitempty"`
}

// --- Tool-aware request types (Anthropic wire format) ---

// anthropicRichMsg uses json.RawMessage for content so it can hold
// either a plain string or an array of content blocks.
type anthropicRichMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicToolReq struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    json.RawMessage    `json:"system,omitempty"`
	Messages  []anthropicRichMsg `json:"messages"`
	Tools     []anthropicToolDef `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicToolDef struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	MaxUses     int             `json:"max_uses,omitempty"`
}

// anthropicContentBlock is used for building mixed-content messages.
type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	ServerToolUse *struct {
		WebSearchRequests int `json:"web_search_requests"`
	} `json:"server_tool_use,omitempty"`
}

type anthropicResp struct {
	Content []anthropicContent `json:"content"`
	Usage   anthropicUsage     `json:"usage"`
	Error   *anthropicError    `json:"error,omitempty"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicStreamEvent struct {
	Type         string `json:"type"`
	ContentBlock *struct {
		Type  string `json:"type"`
		ID    string `json:"id,omitempty"`
		Name  string `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	} `json:"content_block,omitempty"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
		StopReason  string `json:"stop_reason,omitempty"`
	} `json:"delta,omitempty"`
	Message *struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message,omitempty"`
	Usage *anthropicUsage `json:"usage,omitempty"`
	Error *anthropicError `json:"error,omitempty"`
}

func (p *AnthropicProvider) buildReq(systemPrompt string, messages []chat.Message, stream bool) ([]byte, error) {
	msgs := make([]anthropicMsg, 0, len(messages))
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "system" {
			continue
		}
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, anthropicMsg{Role: role, Content: m.Content})
	}
	r := anthropicReq{
		Model:     p.model,
		MaxTokens: p.maxOutputTokens,
		Messages:  msgs,
		Stream:    stream,
	}
	if sp := strings.TrimSpace(systemPrompt); sp != "" {
		r.System = sp
	}
	return json.Marshal(r)
}

func (p *AnthropicProvider) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIBase, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	return req, nil
}

func (p *AnthropicProvider) checkHTTPError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	var e struct {
		Error anthropicError `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, e.Error.Message)
	}
	return fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(b))
}

func (p *AnthropicProvider) Chat(ctx context.Context, systemPrompt string, messages []chat.Message) (string, chat.Usage, error) {
	body, err := p.buildReq(systemPrompt, messages, false)
	if err != nil {
		return "", chat.Usage{}, err
	}
	req, err := p.newRequest(ctx, body)
	if err != nil {
		return "", chat.Usage{}, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return "", chat.Usage{}, err
	}
	defer resp.Body.Close()
	if err := p.checkHTTPError(resp); err != nil {
		return "", chat.Usage{}, err
	}
	var out anthropicResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", chat.Usage{}, err
	}
	if out.Error != nil {
		return "", chat.Usage{}, errors.New(out.Error.Message)
	}
	if len(out.Content) == 0 {
		return "", chat.Usage{}, errors.New("anthropic: empty response content")
	}
	u := chat.Usage{
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}
	if out.Usage.ServerToolUse != nil {
		u.WebSearchRequests = out.Usage.ServerToolUse.WebSearchRequests
	}
	return out.Content[0].Text, u, nil
}

func (p *AnthropicProvider) ChatStream(
	ctx context.Context,
	systemPrompt string,
	messages []chat.Message,
	onDelta func(string) error,
) (string, chat.Usage, error) {
	body, err := p.buildReq(systemPrompt, messages, true)
	if err != nil {
		return "", chat.Usage{}, err
	}
	req, err := p.newRequest(ctx, body)
	if err != nil {
		return "", chat.Usage{}, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := p.http.Do(req)
	if err != nil {
		return "", chat.Usage{}, err
	}
	defer resp.Body.Close()
	if err := p.checkHTTPError(resp); err != nil {
		return "", chat.Usage{}, err
	}

	var sb strings.Builder
	var u chat.Usage
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if event.Error != nil {
			return sb.String(), u, errors.New(event.Error.Message)
		}
		if event.Type == "message_start" && event.Message != nil {
			u.InputTokens = event.Message.Usage.InputTokens
			if event.Message.Usage.ServerToolUse != nil {
				u.WebSearchRequests = event.Message.Usage.ServerToolUse.WebSearchRequests
			}
		}
		if event.Type == "message_delta" && event.Usage != nil {
			u.OutputTokens = event.Usage.OutputTokens
			if event.Usage.ServerToolUse != nil {
				u.WebSearchRequests = event.Usage.ServerToolUse.WebSearchRequests
			}
		}
		if event.Type == "content_block_delta" && event.Delta != nil && event.Delta.Text != "" {
			sb.WriteString(event.Delta.Text)
			if onDelta != nil {
				if err := onDelta(event.Delta.Text); err != nil {
					return sb.String(), u, err
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), u, err
	}
	return sb.String(), u, nil
}

// buildToolMessages translates internal Message types to the Anthropic wire
// format. tool-result messages become role:"user" with a tool_result content
// block. Assistant messages with ToolCalls become mixed content (text + tool_use).
// Assistant messages with RawContent (containing server tool blocks like web
// search results with encrypted_content) are passed through verbatim.
func buildToolMessages(messages []chat.Message) ([]anthropicRichMsg, error) {
	out := make([]anthropicRichMsg, 0, len(messages))
	for _, m := range messages {
		switch {
		case m.Role == "system":
			continue

		case m.Role == chat.RoleToolResult:
			block := anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			content, err := json.Marshal([]anthropicContentBlock{block})
			if err != nil {
				return nil, err
			}
			out = append(out, anthropicRichMsg{Role: "user", Content: content})

		case m.Role == "assistant" && len(m.RawContent) > 0:
			// RawContent preserves server tool blocks (web search results
			// with encrypted_content) that must be sent back verbatim.
			out = append(out, anthropicRichMsg{Role: "assistant", Content: m.RawContent})

		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			blocks := make([]anthropicContentBlock, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Input,
				})
			}
			content, err := json.Marshal(blocks)
			if err != nil {
				return nil, err
			}
			out = append(out, anthropicRichMsg{Role: "assistant", Content: content})

		default:
			role := strings.ToLower(strings.TrimSpace(m.Role))
			if role == "" {
				role = "user"
			}
			content, _ := json.Marshal(m.Content)
			out = append(out, anthropicRichMsg{Role: role, Content: content})
		}
	}
	return out, nil
}

func (p *AnthropicProvider) ChatStreamWithTools(
	ctx context.Context,
	systemPrompt string,
	messages []chat.Message,
	tools []chat.ToolDef,
	onTextDelta func(string) error,
	onToolStart func(toolName string) error,
) (chat.StreamResponse, error) {
	// Build messages in Anthropic wire format.
	msgs, err := buildToolMessages(messages)
	if err != nil {
		return chat.StreamResponse{}, err
	}

	// System prompt as structured block with cache_control.
	var systemBlock json.RawMessage
	if sp := strings.TrimSpace(systemPrompt); sp != "" {
		type sysBlock struct {
			Type         string      `json:"type"`
			Text         string      `json:"text"`
			CacheControl *cacheCtrl  `json:"cache_control,omitempty"`
		}
		sb := []sysBlock{{Type: "text", Text: sp, CacheControl: &cacheCtrl{Type: "ephemeral"}}}
		systemBlock, _ = json.Marshal(sb)
	}

	// Translate tool definitions. Server tools (Type != "") use a
	// different wire shape: type + name, no description/input_schema.
	var apiTools []anthropicToolDef
	for _, t := range tools {
		if t.Type != "" {
			apiTools = append(apiTools, anthropicToolDef{
				Type: t.Type,
				Name: t.Name,
			})
		} else {
			apiTools = append(apiTools, anthropicToolDef{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}

	r := anthropicToolReq{
		Model:     p.model,
		MaxTokens: p.maxOutputTokens,
		System:    systemBlock,
		Messages:  msgs,
		Tools:     apiTools,
		Stream:    true,
	}
	body, err := json.Marshal(r)
	if err != nil {
		return chat.StreamResponse{}, err
	}

	req, err := p.newRequest(ctx, body)
	if err != nil {
		return chat.StreamResponse{}, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := p.http.Do(req)
	if err != nil {
		return chat.StreamResponse{}, err
	}
	defer resp.Body.Close()
	if err := p.checkHTTPError(resp); err != nil {
		return chat.StreamResponse{}, err
	}

	// Parse SSE stream, handling text, tool_use, and server tool blocks.
	var (
		contentBlocks []chat.ContentBlock
		currentBlock  chat.ContentBlock
		inputBuf      strings.Builder    // accumulates partial JSON for tool_use/server_tool_use input
		rawCB         json.RawMessage    // raw content_block JSON for server tool blocks
		u             chat.Usage
		stopReason    string
	)

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if event.Error != nil {
			return chat.StreamResponse{}, errors.New(event.Error.Message)
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				u.InputTokens = event.Message.Usage.InputTokens
				if event.Message.Usage.ServerToolUse != nil {
					u.WebSearchRequests = event.Message.Usage.ServerToolUse.WebSearchRequests
				}
			}

		case "content_block_start":
			if event.ContentBlock == nil {
				currentBlock = chat.ContentBlock{Type: "text"}
				continue
			}
			switch event.ContentBlock.Type {
			case "tool_use":
				currentBlock = chat.ContentBlock{
					Type:        "tool_use",
					ToolUseID:   event.ContentBlock.ID,
					ToolUseName: event.ContentBlock.Name,
				}
				inputBuf.Reset()
				if onToolStart != nil {
					_ = onToolStart(event.ContentBlock.Name)
				}

			case "server_tool_use":
				currentBlock = chat.ContentBlock{
					Type:        "server_tool_use",
					ToolUseID:   event.ContentBlock.ID,
					ToolUseName: event.ContentBlock.Name,
				}
				inputBuf.Reset()
				if onToolStart != nil {
					_ = onToolStart(event.ContentBlock.Name)
				}

			case "web_search_tool_result":
				currentBlock = chat.ContentBlock{
					Type: "web_search_tool_result",
				}
				// Capture the raw content_block JSON — it contains
				// encrypted_content that must be passed back verbatim.
				var raw struct {
					CB json.RawMessage `json:"content_block"`
				}
				_ = json.Unmarshal([]byte(data), &raw)
				rawCB = raw.CB

			default:
				currentBlock = chat.ContentBlock{Type: "text"}
			}

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				currentBlock.Text += event.Delta.Text
				if onTextDelta != nil {
					if err := onTextDelta(event.Delta.Text); err != nil {
						return chat.StreamResponse{}, err
					}
				}
			case "input_json_delta":
				inputBuf.WriteString(event.Delta.PartialJSON)
			}

		case "content_block_stop":
			switch currentBlock.Type {
			case "tool_use":
				currentBlock.ToolUseInput = json.RawMessage(inputBuf.String())

			case "server_tool_use":
				currentBlock.ToolUseInput = json.RawMessage(inputBuf.String())
				// Reconstruct the raw block for multi-turn replay.
				currentBlock.Raw = buildServerToolUseRaw(
					currentBlock.ToolUseID,
					currentBlock.ToolUseName,
					currentBlock.ToolUseInput,
				)

			case "web_search_tool_result":
				currentBlock.Raw = rawCB
				rawCB = nil
			}
			contentBlocks = append(contentBlocks, currentBlock)
			currentBlock = chat.ContentBlock{}

		case "message_delta":
			if event.Usage != nil {
				u.OutputTokens = event.Usage.OutputTokens
				if event.Usage.ServerToolUse != nil {
					u.WebSearchRequests = event.Usage.ServerToolUse.WebSearchRequests
				}
			}
			if event.Delta != nil && event.Delta.StopReason != "" {
				stopReason = event.Delta.StopReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return chat.StreamResponse{}, err
	}

	return chat.StreamResponse{
		Content:    contentBlocks,
		StopReason: stopReason,
		Usage:      u,
	}, nil
}

// buildServerToolUseRaw reconstructs a server_tool_use content block as raw
// JSON for multi-turn replay.
func buildServerToolUseRaw(id, name string, input json.RawMessage) json.RawMessage {
	block := struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}{
		Type:  "server_tool_use",
		ID:    id,
		Name:  name,
		Input: input,
	}
	data, _ := json.Marshal(block)
	return data
}

type cacheCtrl struct {
	Type string `json:"type"`
}
