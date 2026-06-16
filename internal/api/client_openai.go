// Package api 提供模型客户端抽象、工具调用响应结构以及最小可用的后端实现。
// client_openai.go 为 OpenAI-compatible 客户端实现：定义请求/响应类型、
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

// openAICompatClient 表示 OpenAI-compatible chat completions 客户端。
// 这里单独保留一个 provider 分支，是为了让 OpenAI 原生接口和兼容接口继续共用同一条实现主链。
type openAICompatClient struct {
	// model 表示实际调用的模型名。
	model string
	// apiKey 表示认证使用的 API Key。
	apiKey string
	// baseURL 表示请求基础地址。
	baseURL string
	// provider 表示当前后端提供方名称，便于后续继续扩展差异化行为。
	provider string
	// httpClient 表示底层 HTTP 客户端。
	httpClient *http.Client
}

// openAICompatRequest 表示最小可用的 OpenAI-compatible chat completions 请求体。
type openAICompatRequest struct {
	// Model 表示目标模型名。
	Model string `json:"model"`
	// Messages 表示请求消息历史。
	Messages []openAICompatMessage `json:"messages"`
	// Tools 表示公开给模型的工具定义。
	Tools []openAICompatToolDef `json:"tools,omitempty"`
	// Stream 表示是否启用流式。
	Stream bool `json:"stream,omitempty"`
	// StreamOptions 表示流式附加选项，例如 usage 回传。
	StreamOptions *openAICompatStreamOptions `json:"stream_options,omitempty"`
}

// openAICompatStreamOptions 表示 OpenAI-compatible 流式附加选项。
type openAICompatStreamOptions struct {
	// IncludeUsage 表示是否在流式尾包里返回 usage。
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// openAICompatMessage 表示 OpenAI-compatible 消息结构。
type openAICompatMessage struct {
	// Role 表示消息角色。
	Role string `json:"role"`
	// Content 表示消息正文。
	Content string `json:"content,omitempty"`
	// Name 表示可选消息名称。
	Name string `json:"name,omitempty"`
	// ToolCallID 表示 tool 角色绑定的工具调用 id。
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCalls 表示 assistant 消息里携带的工具调用列表。
	ToolCalls []openAICompatToolCall `json:"tool_calls,omitempty"`
}

// openAICompatToolDef 表示 OpenAI-compatible 工具定义。
type openAICompatToolDef struct {
	// Type 表示工具定义类型，当前固定为 function。
	Type string `json:"type"`
	// Function 表示函数工具定义。
	Function openAICompatFunctionDef `json:"function"`
}

// openAICompatFunctionDef 表示 OpenAI-compatible 函数工具定义。
type openAICompatFunctionDef struct {
	// Name 表示工具名称。
	Name string `json:"name"`
	// Description 表示工具说明。
	Description string `json:"description,omitempty"`
	// Parameters 表示工具参数 schema。
	Parameters map[string]any `json:"parameters,omitempty"`
}

// openAICompatToolCall 表示 OpenAI-compatible assistant 工具调用。
type openAICompatToolCall struct {
	// ID 表示本次工具调用唯一标识。
	ID string `json:"id,omitempty"`
	// Type 表示调用类型，当前固定为 function。
	Type string `json:"type,omitempty"`
	// Function 表示函数调用载荷。
	Function openAICompatFunctionCall `json:"function"`
}

// openAICompatFunctionCall 表示 OpenAI-compatible 函数调用正文。
type openAICompatFunctionCall struct {
	// Name 表示函数名称。
	Name string `json:"name,omitempty"`
	// Arguments 表示 JSON 字符串形式的参数正文。
	Arguments string `json:"arguments,omitempty"`
}

// openAICompatResponse 表示最小可用的 OpenAI-compatible 非流式响应体。
type openAICompatResponse struct {
	// Choices 表示候选结果列表。
	Choices []struct {
		// Message 表示当前候选消息。
		Message openAICompatChoiceMessage `json:"message"`
	} `json:"choices"`
	// Usage 表示 token 使用统计。
	Usage Usage `json:"usage"`
	// Error 表示结构化错误。
	Error *struct {
		// Message 表示错误消息。
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// openAICompatChoiceMessage 表示 OpenAI-compatible choice 中的 assistant 消息。
type openAICompatChoiceMessage struct {
	// Content 表示 assistant 文本内容。
	Content string `json:"content,omitempty"`
	// ToolCalls 表示 assistant 原生工具调用列表。
	ToolCalls []openAICompatToolCall `json:"tool_calls,omitempty"`
}

// openAICompatStreamChunk 表示 OpenAI-compatible SSE 流中的单个 chunk。
type openAICompatStreamChunk struct {
	// Choices 表示增量候选列表。
	Choices []struct {
		// Delta 表示本次增量消息块。
		Delta struct {
			// Content 表示文本增量。
			Content string `json:"content,omitempty"`
			// ToolCalls 表示工具调用增量。
			ToolCalls []openAICompatToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		// FinishReason 表示当前候选结束原因。
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	// Usage 表示流式尾包里的 usage。
	Usage *Usage `json:"usage,omitempty"`
	// Error 表示结构化错误。
	Error *struct {
		// Message 表示错误消息。
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// openAICompatToolCallDelta 表示 SSE 中单个 tool_call 的增量片段。
type openAICompatToolCallDelta struct {
	// Index 表示当前工具调用在 tool_calls 数组中的位置。
	Index int `json:"index"`
	// ID 表示工具调用 id。
	ID string `json:"id,omitempty"`
	// Type 表示调用类型。
	Type string `json:"type,omitempty"`
	// Function 表示函数增量内容。
	Function openAICompatFunctionCall `json:"function"`
}

// openAICompatStreamState 表示 OpenAI-compatible 流式响应累积器。
// 这里把文本、工具调用和 usage 分开累积，便于最终重建非流式风格的统一响应。
type openAICompatStreamState struct {
	// Content 保存累积后的 assistant 文本。
	Content strings.Builder
	// ToolCallsByIndex 保存按索引累积的工具调用。
	ToolCallsByIndex map[int]*openAICompatToolCall
	// FinishReason 保存最终结束原因。
	FinishReason string
	// Usage 保存最终 token 统计。
	Usage Usage
}

// Complete 对 OpenAI-compatible 接口发起最小可用的流式请求。
func (c *openAICompatClient) Complete(ctx context.Context, messages []Message, availableTools []tools.Tool) (Response, error) {
	requestBody := openAICompatRequest{
		Model:    c.model,
		Messages: make([]openAICompatMessage, 0, len(messages)),
		Tools:    convertOpenAIToolDefinitions(availableTools),
		Stream:   true,
		StreamOptions: &openAICompatStreamOptions{
			IncludeUsage: true,
		},
	}
	for _, message := range messages {
		requestBody.Messages = append(requestBody.Messages, toOpenAICompatMessage(message))
	}

	return withRetry(ctx, defaultRetryLimit, func() (Response, error) {
		streamResponse, streamErr := c.completeWithStream(ctx, requestBody)
		if streamErr == nil {
			return streamResponse, nil
		}

		requestBody.Stream = false
		requestBody.StreamOptions = nil
		return c.completeWithoutStream(ctx, requestBody)
	})
}

// completeWithStream 使用 SSE 流式读取 OpenAI-compatible 响应。
func (c *openAICompatClient) completeWithStream(ctx context.Context, requestBody openAICompatRequest) (Response, error) {
	request, err := c.newChatCompletionRequest(ctx, requestBody)
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
		return Response{}, newRetryableHTTPError("chat completions stream request", response.StatusCode, response.Status, body)
	}

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/event-stream") {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return Response{}, err
		}
		return parseNonStreamBytes(body)
	}

	return parseStreamResponse(response.Body)
}

// completeWithoutStream 使用普通 JSON 响应读取 OpenAI-compatible completions。
func (c *openAICompatClient) completeWithoutStream(ctx context.Context, requestBody openAICompatRequest) (Response, error) {
	request, err := c.newChatCompletionRequest(ctx, requestBody)
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
		return Response{}, newRetryableHTTPError("chat completions request", response.StatusCode, response.Status, body)
	}

	var decoded openAICompatResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return Response{}, err
	}
	if decoded.Error != nil {
		return Response{}, fmt.Errorf(decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return Response{}, fmt.Errorf("empty model response")
	}

	parsed := parseOpenAICompatMessage(decoded.Choices[0].Message)
	parsed.Usage = decoded.Usage
	return parsed, nil
}

// newChatCompletionRequest 统一构造 chat completions 请求。
func (c *openAICompatClient) newChatCompletionRequest(ctx context.Context, requestBody openAICompatRequest) (*http.Request, error) {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/chat/completions",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	return request, nil
}

// toOpenAICompatMessage 把内部消息结构转换为 OpenAI-compatible 请求消息。
func toOpenAICompatMessage(message Message) openAICompatMessage {
	converted := openAICompatMessage{
		Role:       message.Role,
		Content:    message.Content,
		Name:       message.Name,
		ToolCallID: message.ToolCallID,
	}
	if len(message.ToolCalls) > 0 {
		converted.ToolCalls = make([]openAICompatToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			arguments, _ := json.Marshal(call.Arguments)
			converted.ToolCalls = append(converted.ToolCalls, openAICompatToolCall{
				ID:   call.ID,
				Type: "function",
				Function: openAICompatFunctionCall{
					Name:      call.Name,
					Arguments: string(arguments),
				},
			})
		}
	}
	return converted
}

// convertOpenAIToolDefinitions 把工具定义转换为 OpenAI-compatible function calling 结构。
func convertOpenAIToolDefinitions(definitions []tools.Tool) []openAICompatToolDef {
	if len(definitions) == 0 {
		return nil
	}

	converted := make([]openAICompatToolDef, 0, len(definitions))
	for _, definition := range definitions {
		converted = append(converted, openAICompatToolDef{
			Type: "function",
			Function: openAICompatFunctionDef{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.InputSchema,
			},
		})
	}
	return converted
}

// parseNonStreamBytes 把非 SSE 的 HTTP 响应正文解析成统一 Response。
func parseNonStreamBytes(body []byte) (Response, error) {
	var decoded openAICompatResponse
	if err := json.Unmarshal(body, &decoded); err == nil {
		if decoded.Error != nil {
			return Response{}, fmt.Errorf(decoded.Error.Message)
		}
		if len(decoded.Choices) > 0 {
			parsed := parseOpenAICompatMessage(decoded.Choices[0].Message)
			parsed.Usage = decoded.Usage
			return parsed, nil
		}
	}
	return parseResponse(string(body)), nil
}

// parseOpenAICompatMessage 优先解析原生 tool_calls，回退到 JSON 文本 bridge 协议。
func parseOpenAICompatMessage(message openAICompatChoiceMessage) Response {
	if len(message.ToolCalls) > 0 {
		return Response{
			Text:      message.Content,
			ToolCalls: convertOpenAIToolCalls(message.ToolCalls),
		}
	}
	return parseResponse(message.Content)
}

// convertOpenAIToolCalls 把 OpenAI-compatible 工具调用转换为内部统一结构。
func convertOpenAIToolCalls(calls []openAICompatToolCall) []ToolCall {
	converted := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		converted = append(converted, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: parseToolArguments(call.Function.Arguments),
		})
	}
	return converted
}

// parseToolArguments 解析函数调用中的 JSON 参数字符串。
func parseToolArguments(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || len(parsed) == 0 {
		return map[string]string{"raw": raw}
	}

	result := make(map[string]string, len(parsed))
	for key, value := range parsed {
		result[key] = stringifyToolArgument(value)
	}
	return result
}

// stringifyToolArgument 把工具参数值转换为字符串。
func stringifyToolArgument(value any) string {
	if value == nil {
		return ""
	}

	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return fmt.Sprintf("%v", typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

