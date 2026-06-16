// Package api 提供模型客户端抽象、工具调用响应结构以及最小可用的后端实现。
// client_anthropic.go 为 Anthropic messages API 客户端实现：定义请求/响应类型、
// 消息转换、工具定义转换、HTTP 通信以及非流式/流式响应解析。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"mini-claude-code/internal/tools"
)

// anthropicClient 表示 Anthropic messages API 客户端。
type anthropicClient struct {
	// model 表示实际调用的模型名。
	model string
	// apiKey 表示认证使用的 API Key。
	apiKey string
	// baseURL 表示请求基础地址。
	baseURL string
	// thinkingMode 表示 thinking 模式，例如 adaptive 或 enabled。
	thinkingMode string
	// maxOutputTokens 表示单次调用的最大输出 token 预算。
	maxOutputTokens int
	// httpClient 表示底层 HTTP 客户端。
	httpClient *http.Client
}

// anthropicRequest 表示最小可用的 Anthropic messages 请求体。
type anthropicRequest struct {
	// Model 表示目标模型名。
	Model string `json:"model"`
	// MaxTokens 表示最大输出 token 数。
	MaxTokens int `json:"max_tokens"`
	// System 表示独立 system prompt 文本。
	System string `json:"system,omitempty"`
	// Messages 表示对话消息历史。
	Messages []anthropicMessage `json:"messages"`
	// Tools 表示公开给模型的工具定义。
	Tools []anthropicToolDef `json:"tools,omitempty"`
	// Stream 表示是否启用流式。
	Stream bool `json:"stream,omitempty"`
	// Thinking 表示 thinking 配置。
	Thinking *anthropicThinkingConfig `json:"thinking,omitempty"`
}

// anthropicThinkingConfig 表示 Anthropic thinking 配置。
type anthropicThinkingConfig struct {
	// Type 表示 thinking 类型，例如 enabled。
	Type string `json:"type"`
	// BudgetTokens 表示 thinking 预算 token 数。
	BudgetTokens int `json:"budget_tokens"`
}

// anthropicMessage 表示 Anthropic messages 消息。
type anthropicMessage struct {
	// Role 表示消息角色，例如 user 或 assistant。
	Role string `json:"role"`
	// Content 表示消息内容，可以是文本或 content blocks 列表。
	Content any `json:"content"`
}

// anthropicContentBlock 表示 Anthropic content block。
type anthropicContentBlock struct {
	// Type 表示内容块类型，例如 text、tool_use、tool_result、thinking。
	Type string `json:"type"`
	// Text 表示 text block 文本。
	Text string `json:"text,omitempty"`
	// ID 表示 tool_use block 的调用 id。
	ID string `json:"id,omitempty"`
	// Name 表示 tool_use 的工具名。
	Name string `json:"name,omitempty"`
	// Input 表示 tool_use 的输入参数。
	Input map[string]any `json:"input,omitempty"`
	// ToolUseID 表示 tool_result 绑定的 tool_use id。
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Content 表示 tool_result 的结果内容。
	Content string `json:"content,omitempty"`
	// Thinking 表示 thinking block 文本。
	Thinking string `json:"thinking,omitempty"`
}

// anthropicToolDef 表示 Anthropic 工具定义。
type anthropicToolDef struct {
	// Name 表示工具名。
	Name string `json:"name"`
	// Description 表示工具说明。
	Description string `json:"description,omitempty"`
	// InputSchema 表示工具参数 schema。
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// anthropicResponse 表示最小可用的 Anthropic messages 响应体。
type anthropicResponse struct {
	// Content 表示返回内容块。
	Content []anthropicContentBlock `json:"content"`
	// Usage 表示 token 使用统计。
	Usage struct {
		// InputTokens 表示输入 token。
		InputTokens int `json:"input_tokens"`
		// OutputTokens 表示输出 token。
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	// Error 表示结构化错误。
	Error *struct {
		// Type 表示错误类型。
		Type string `json:"type"`
		// Message 表示错误消息。
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// anthropicStreamEvent 表示 Anthropic SSE 流中的单个事件。
type anthropicStreamEvent struct {
	// Type 表示事件类型，例如 message_start、content_block_delta。
	Type string `json:"type"`
	// Message 表示 message_start 或 message_delta 事件里的消息对象。
	Message *struct {
		// Usage 表示消息级 token 统计。
		Usage struct {
			// InputTokens 表示输入 token。
			InputTokens int `json:"input_tokens"`
			// OutputTokens 表示输出 token。
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message,omitempty"`
	// Usage 表示某些实现可能直接把 usage 放在顶层。
	Usage *struct {
		// InputTokens 表示输入 token。
		InputTokens int `json:"input_tokens"`
		// OutputTokens 表示输出 token。
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
	// Index 表示当前 content block 索引。
	Index int `json:"index,omitempty"`
	// ContentBlock 表示 block 起始时的元数据。
	ContentBlock *struct {
		// Type 表示 block 类型。
		Type string `json:"type"`
		// ID 表示 tool_use 的调用 id。
		ID string `json:"id,omitempty"`
		// Name 表示 tool_use 的工具名。
		Name string `json:"name,omitempty"`
		// Text 表示 text block 初始文本。
		Text string `json:"text,omitempty"`
	} `json:"content_block,omitempty"`
	// Delta 表示 block 增量内容。
	Delta *struct {
		// Type 表示 delta 类型。
		Type string `json:"type"`
		// Text 表示文本增量。
		Text string `json:"text,omitempty"`
		// PartialJSON 表示 tool_use 输入 JSON 增量。
		PartialJSON string `json:"partial_json,omitempty"`
		// Thinking 表示 thinking 文本增量。
		Thinking string `json:"thinking,omitempty"`
	} `json:"delta,omitempty"`
	// Error 表示流式错误。
	Error *struct {
		// Type 表示错误类型。
		Type string `json:"type"`
		// Message 表示错误消息。
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Complete 对 Anthropic messages API 发起最小可执行请求。
func (c *anthropicClient) Complete(ctx context.Context, messages []Message, availableTools []tools.Tool) (Response, error) {
	return c.CompleteWithToolCallback(ctx, messages, availableTools, nil)
}

// CompleteWithToolCallback 对 Anthropic messages API 发起最小可执行请求，
// 并在流式阶段的 tool_use 完整结束时把工具回调给上层调度器。
func (c *anthropicClient) CompleteWithToolCallback(ctx context.Context, messages []Message, availableTools []tools.Tool, onToolComplete func(ToolCall)) (Response, error) {
	requestBody, err := buildAnthropicRequestWithOptions(
		c.model,
		messages,
		availableTools,
		c.thinkingMode,
		c.maxOutputTokens,
	)
	if err != nil {
		return Response{}, err
	}

	return withRetry(ctx, defaultRetryLimit, func() (Response, error) {
		requestBody.Stream = true
		streamResponse, streamErr := c.completeWithStream(ctx, requestBody, onToolComplete)
		if streamErr == nil {
			return streamResponse, nil
		}

		requestBody.Stream = false
		return c.completeWithoutStream(ctx, requestBody)
	})
}

// completeWithStream 使用 SSE 流式读取 Anthropic messages 响应。
// 如果上传了 onToolComplete，则在 content_block_stop 完成一个 tool_use 时立即回调，
// 让上层有机会边收边执行并发的安全工具。
func (c *anthropicClient) completeWithStream(ctx context.Context, requestBody anthropicRequest, onToolComplete func(ToolCall)) (Response, error) {
	request, err := c.newMessagesRequest(ctx, requestBody)
	if err != nil {
		return Response{}, err
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return Response{}, newRetryableHTTPError("anthropic messages stream request", response.StatusCode, response.Status, body)
	}

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/event-stream") {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return Response{}, err
		}
		return parseAnthropicNonStreamBytes(body)
	}

	return parseAnthropicStreamResponse(response.Body, onToolComplete)
}

// completeWithoutStream 使用普通 JSON 响应读取 Anthropic messages。
func (c *anthropicClient) completeWithoutStream(ctx context.Context, requestBody anthropicRequest) (Response, error) {
	request, err := c.newMessagesRequest(ctx, requestBody)
	if err != nil {
		return Response{}, err
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return Response{}, newRetryableHTTPError("anthropic messages request", response.StatusCode, response.Status, body)
	}

	var decoded anthropicResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return Response{}, err
	}
	if decoded.Error != nil {
		return Response{}, fmt.Errorf(decoded.Error.Message)
	}

	return parseAnthropicResponse(decoded), nil
}

// newMessagesRequest 统一构造 Anthropic messages 请求。
func (c *anthropicClient) newMessagesRequest(ctx context.Context, requestBody anthropicRequest) (*http.Request, error) {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/messages",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if requestBody.Stream {
		request.Header.Set("Accept", "application/json, text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Set("x-api-key", c.apiKey)
	request.Header.Set("anthropic-version", "2023-06-01")
	return request, nil
}

// buildAnthropicRequest 把统一消息历史转换成 Anthropic messages 请求体。
func buildAnthropicRequest(model string, messages []Message, availableTools []tools.Tool) (anthropicRequest, error) {
	systemText, anthropicMessages := convertToAnthropicMessages(messages)
	return anthropicRequest{
		Model:     model,
		MaxTokens: 16384,
		System:    systemText,
		Messages:  anthropicMessages,
		Tools:     convertAnthropicToolDefinitions(availableTools),
		Stream:    false,
	}, nil
}

// buildAnthropicRequestWithOptions 把统一消息历史转换成带 thinking 和输出预算的 Anthropic 请求体。
func buildAnthropicRequestWithOptions(model string, messages []Message, availableTools []tools.Tool, thinkingMode string, maxOutputTokens int) (anthropicRequest, error) {
	request, err := buildAnthropicRequest(model, messages, availableTools)
	if err != nil {
		return anthropicRequest{}, err
	}

	if maxOutputTokens > 0 {
		request.MaxTokens = maxOutputTokens
	}
	if thinkingMode == "adaptive" || thinkingMode == "enabled" {
		budgetTokens := request.MaxTokens - 1
		if budgetTokens < 1024 {
			budgetTokens = request.MaxTokens
		}
		request.Thinking = &anthropicThinkingConfig{
			Type:         "enabled",
			BudgetTokens: budgetTokens,
		}
	}
	return request, nil
}

// convertAnthropicToolDefinitions 把工具定义转换成 Anthropic tools 结构。
func convertAnthropicToolDefinitions(definitions []tools.Tool) []anthropicToolDef {
	if len(definitions) == 0 {
		return nil
	}

	converted := make([]anthropicToolDef, 0, len(definitions))
	for _, definition := range definitions {
		converted = append(converted, anthropicToolDef{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: definition.InputSchema,
		})
	}
	return converted
}

// convertToAnthropicMessages 把统一消息历史拆成 Anthropic 的 system 和 messages。
func convertToAnthropicMessages(messages []Message) (string, []anthropicMessage) {
	systemParts := make([]string, 0, 2)
	converted := make([]anthropicMessage, 0, len(messages))
	pendingToolResults := make([]anthropicContentBlock, 0, 2)

	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		converted = append(converted, anthropicMessage{
			Role:    "user",
			Content: append([]anthropicContentBlock{}, pendingToolResults...),
		})
		pendingToolResults = pendingToolResults[:0]
	}

	for _, message := range messages {
		switch message.Role {
		case "system":
			if strings.TrimSpace(message.Content) != "" {
				systemParts = append(systemParts, strings.TrimSpace(message.Content))
			}
		case "tool":
			pendingToolResults = append(pendingToolResults, anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: message.ToolCallID,
				Content:   message.Content,
			})
		case "user":
			if strings.TrimSpace(message.Content) == "" {
				// 如果当前 user 文本为空，就不要急着冲刷 tool_result，
				// 让它继续等待后续真正的 user 内容，尽量贴近源仓库同一层 user message 混排 block 的语义。
				continue
			}

			// Anthropic 允许同一层 user message 同时承载 tool_result 和 text block。
			// 这里优先把挂起的 tool_result 和当前 user 文本合并，避免生成连续两条 user message，
			// 从而更接近源仓库直接维护 anthropicMessages 时的交替结构。
			userBlocks := make([]anthropicContentBlock, 0, len(pendingToolResults)+1)
			if len(pendingToolResults) > 0 {
				userBlocks = append(userBlocks, pendingToolResults...)
				pendingToolResults = pendingToolResults[:0]
			}
			userBlocks = append(userBlocks, anthropicContentBlock{
				Type: "text",
				Text: message.Content,
			})
			converted = append(converted, anthropicMessage{
				Role:    "user",
				Content: userBlocks,
			})
		case "assistant":
			flushToolResults()
			blocks := assistantMessageToAnthropicBlocks(message)
			if len(blocks) == 0 {
				continue
			}
			converted = append(converted, anthropicMessage{
				Role:    "assistant",
				Content: blocks,
			})
		}
	}

	flushToolResults()
	return strings.Join(systemParts, "\n\n"), converted
}

// assistantMessageToAnthropicBlocks 把 assistant 消息转换成 Anthropic content blocks。
func assistantMessageToAnthropicBlocks(message Message) []anthropicContentBlock {
	blocks := make([]anthropicContentBlock, 0, len(message.ToolCalls)+1)
	assistantText := ExtractAssistantText(message)
	if strings.TrimSpace(assistantText) != "" {
		blocks = append(blocks, anthropicContentBlock{
			Type: "text",
			Text: assistantText,
		})
	}

	for _, call := range message.ToolCalls {
		blocks = append(blocks, anthropicContentBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Name,
			Input: convertStringArgumentsToAny(call.Arguments),
		})
	}
	return blocks
}

// convertStringArgumentsToAny 把统一的字符串参数恢复成更贴近原始 JSON 的对象。
// 这里优先尝试把 JSON 字符串解码回对象，避免 tool_use 输入在 Anthropic 链路里退化纯文本。
func convertStringArgumentsToAny(arguments map[string]string) map[string]any {
	result := make(map[string]any, len(arguments))
	for key, value := range arguments {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			result[key] = ""
			continue
		}

		var decoded any
		if json.Unmarshal([]byte(trimmed), &decoded) == nil {
			result[key] = decoded
			continue
		}
		result[key] = value
	}
	return result
}

// convertAnyArgumentsToString 把 Anthropic tool_use 输入参数转回统一字符串参数结构。
func convertAnyArgumentsToString(arguments map[string]any) map[string]string {
	if len(arguments) == 0 {
		return map[string]string{}
	}

	result := make(map[string]string, len(arguments))
	for key, value := range arguments {
		result[key] = stringifyToolArgument(value)
	}
	return result
}

// parseAnthropicResponse 解析 Anthropic messages 响应并转成统一结构。
func parseAnthropicResponse(response anthropicResponse) Response {
	textParts := make([]string, 0, len(response.Content))
	thinkingParts := make([]string, 0, len(response.Content))
	toolCalls := make([]ToolCall, 0, len(response.Content))

	for _, block := range response.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				textParts = append(textParts, block.Text)
			}
		case "thinking":
			thinkingText := strings.TrimSpace(block.Thinking)
			if thinkingText == "" {
				thinkingText = strings.TrimSpace(block.Text)
			}
			if thinkingText != "" {
				thinkingParts = append(thinkingParts, thinkingText)
			}
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: convertAnyArgumentsToString(block.Input),
			})
		}
	}

	text := strings.Join(textParts, "")
	thinking := strings.Join(thinkingParts, "")
	return Response{
		Text:       strings.TrimSpace(text),
		Thinking:   strings.TrimSpace(thinking),
		ToolCalls:  toolCalls,
		RawContent: strings.TrimSpace(text),
		Usage: Usage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
	}
}

// parseAnthropicNonStreamBytes 兼容某些代理把流式请求降级成普通 JSON 的情况。
func parseAnthropicNonStreamBytes(body []byte) (Response, error) {
	var decoded anthropicResponse
	if err := json.Unmarshal(body, &decoded); err == nil {
		if decoded.Error != nil {
			return Response{}, fmt.Errorf(decoded.Error.Message)
		}
		if len(decoded.Content) > 0 {
			return parseAnthropicResponse(decoded), nil
		}
	}
	return parseResponse(string(body)), nil
}

