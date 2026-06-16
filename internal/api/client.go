// Package api 提供模型客户端抽象、工具调用响应结构以及最小可用的后端实现。
// 本文件为核心类型和接口：定义消息结构、响应模型、客户端接口、配置、Mock 客户端、
// 工厂函数以及辅助工具函数。是 api 包的骨架文件。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"mini-claude-code/internal/tools"
)

// Message 表示一次对话消息。
type Message struct {
	// Role 表示消息角色，例如 system、user、assistant、tool。
	Role string `json:"role"`
	// Content 表示消息正文。
	Content string `json:"content"`
	// Name 表示可选消息名称，当前主要用于 tool 角色。
	Name string `json:"name,omitempty"`
	// ToolCalls 表示 assistant 消息中的工具调用列表。
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID 表示 tool 消息对应的工具调用标识。
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall 表示模型发起的一次工具调用请求。
type ToolCall struct {
	// ID 表示本次工具调用的唯一标识。
	ID string `json:"id,omitempty"`
	// Name 表示工具名称。
	Name string `json:"name"`
	// Arguments 表示工具参数。
	Arguments map[string]string `json:"arguments"`
}

// Usage 表示一次模型响应的最小 token 使用统计。
type Usage struct {
	// PromptTokens 表示提示侧 token 数量。
	PromptTokens int `json:"prompt_tokens"`
	// CompletionTokens 表示生成侧 token 数量。
	CompletionTokens int `json:"completion_tokens"`
	// TotalTokens 表示总 token 数量。
	TotalTokens int `json:"total_tokens"`
}

// Response 表示模型一次补全返回的标准结构。
type Response struct {
	// Text 表示本轮可以直接展示给用户的助手文本。
	Text string `json:"assistant_text"`
	// Thinking 表示仅用于展示的思考过程文本。
	// 它用于贴近源仓库的 Anthropic thinking 体验，但不会作为正式 assistant 历史写回消息主链。
	Thinking string `json:"thinking,omitempty"`
	// ToolCalls 表示本轮需要执行的工具调用列表。
	ToolCalls []ToolCall `json:"tool_calls"`
	// RawContent 保留模型原始文本，便于排查协议差异或恢复上下文。
	RawContent string `json:"raw_content"`
	// Usage 保留本轮最小 token 统计。
	Usage Usage `json:"usage"`
}

// Client 定义模型客户端接口。
type Client interface {
	// Complete 根据消息列表和当前已公开工具生成结构化响应。
	Complete(ctx context.Context, messages []Message, availableTools []tools.Tool) (Response, error)
}

// StreamingToolClient 定义支持流式工具完成回调的可选客户端接口。
// 当前主要用于 Anthropic：当 content_block_stop 结束一个完整 tool_use 时，
// Agent 可以提前收到该工具并启动执行，而不用等整条 assistant 响应完全结束。
type StreamingToolClient interface {
	Client
	// CompleteWithToolCallback 在正常返回最终响应的同时，
	// 会对流式阶段已经完整结束的 tool_use 触发一次提前回调。
	CompleteWithToolCallback(
		ctx context.Context,
		messages []Message,
		availableTools []tools.Tool,
		onToolComplete func(ToolCall),
	) (Response, error)
}

// Config 表示模型客户端初始化配置。
type Config struct {
	// Model 表示当前调用的模型名。
	Model string
	// APIKey 表示目标后端 API Key。
	APIKey string
	// BaseURL 表示目标后端基础地址。
	BaseURL string
	// Provider 表示后端类型，例如 openai 或 anthropic。
	Provider string
	// ThinkingMode 表示 thinking 模式，当前主要用于 Anthropic。
	ThinkingMode string
	// MaxOutputTokens 表示单次调用的最大输出 token 预算。
	MaxOutputTokens int
}

const (
	// defaultRetryLimit 表示默认最多额外重试的次数。
	// 这里对齐源仓库的 3 次重试策略，兼顾限流恢复与终端等待时长。
	defaultRetryLimit = 3
	// maxRetryDelay 表示指数退避的最大等待时长。
	// 这里设置上限，避免连续高负载时等待时间无限增长。
	maxRetryDelay = 30 * time.Second
)

// retryableHTTPError 表示允许重试的 HTTP 失败信息。
// 单独定义这个错误类型，方便把状态码、状态文本和响应正文一起保留下来，并供重试判定逻辑读取。
type retryableHTTPError struct {
	// StatusCode 表示本次失败对应的 HTTP 状态码。
	StatusCode int
	// Status 表示本次失败对应的 HTTP 状态文本。
	Status string
	// Body 表示服务端返回的错误正文摘要。
	Body string
	// RequestName 表示失败发生在哪一类模型请求上。
	RequestName string
}

// Error 把可重试的 HTTP 失败格式化为稳定错误信息。
func (e *retryableHTTPError) Error() string {
	return fmt.Sprintf("%s failed: %d %s %s", e.RequestName, e.StatusCode, e.Status, strings.TrimSpace(e.Body))
}

// newRetryableHTTPError 构造带上下文的 HTTP 错误。
func newRetryableHTTPError(requestName string, statusCode int, status string, body []byte) error {
	return &retryableHTTPError{
		StatusCode:  statusCode,
		Status:      status,
		Body:        strings.TrimSpace(string(body)),
		RequestName: requestName,
	}
}

// NewClient 根据配置构造模型客户端。
func NewClient(config Config) Client {
	provider := strings.ToLower(strings.TrimSpace(config.Provider))
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")

	if strings.TrimSpace(config.APIKey) != "" && baseURL != "" {
		switch provider {
		case "anthropic":
			return &anthropicClient{
				model:           config.Model,
				apiKey:          config.APIKey,
				baseURL:         baseURL,
				thinkingMode:    strings.ToLower(strings.TrimSpace(config.ThinkingMode)),
				maxOutputTokens: config.MaxOutputTokens,
				httpClient: &http.Client{
					Timeout: 90 * time.Second,
				},
			}
		case "openai", "":
			return &openAICompatClient{
				model:    config.Model,
				apiKey:   config.APIKey,
				baseURL:  baseURL,
				provider: provider,
				httpClient: &http.Client{
					Timeout: 90 * time.Second,
				},
			}
		default:
			return &openAICompatClient{
				model:    config.Model,
				apiKey:   config.APIKey,
				baseURL:  baseURL,
				provider: provider,
				httpClient: &http.Client{
					Timeout: 90 * time.Second,
				},
			}
		}
	}

	// 当环境没有配置真实后端时，回退到 mock 客户端。
	return &mockClient{model: config.Model}
}

// mockClient 表示本地兜底客户端。
type mockClient struct {
	// model 表示模拟调用时暴露给上层的模型名。
	model string
}

// Complete 返回一个稳定的本地模拟回复。
func (m *mockClient) Complete(_ context.Context, messages []Message, _ []tools.Tool) (Response, error) {
	lastUser := ""
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			lastUser = messages[index].Content
			break
		}
	}

	text := fmt.Sprintf("[mock:%s] %s", m.model, strings.TrimSpace(lastUser))
	return Response{
		Text:       text,
		ToolCalls:  []ToolCall{},
		RawContent: text,
	}, nil
}

// ExtractAssistantText 兼容从 JSON bridge 结构里抽取真正的 assistant 文本。
// 这个 helper 同时服务 provider 转换和上层摘要输入清洗，避免同一套 bridge 解析规则在多处漂移。
func ExtractAssistantText(message Message) string {
	trimmed := strings.TrimSpace(message.Content)
	if trimmed == "" {
		return ""
	}

	var payload struct {
		AssistantText string            `json:"assistant_text"`
		ToolCalls     []json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil && len(payload.ToolCalls) > 0 {
		return strings.TrimSpace(payload.AssistantText)
	}
	return trimmed
}

// cloneGenericMessageSlice 复制 provider-specific 原生消息数组。
// 这里的元素本身是通用 map 结构，因此需要逐项复制，避免后续运行态修改污染已保存快照。
func cloneGenericMessageSlice(input []map[string]any) []map[string]any {
	if len(input) == 0 {
		return nil
	}

	output := make([]map[string]any, 0, len(input))
	for _, item := range input {
		output = append(output, toGenericSnapshotMap(item))
	}
	return output
}

// parseResponse 兼容普通文本回复和 JSON bridge 响应。
func parseResponse(raw string) Response {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Response{
			ToolCalls:  []ToolCall{},
			RawContent: raw,
		}
	}

	var parsed Response
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		parsed.RawContent = raw
		if parsed.ToolCalls == nil {
			parsed.ToolCalls = []ToolCall{}
		}
		return parsed
	}

	return Response{
		Text:       raw,
		ToolCalls:  []ToolCall{},
		RawContent: raw,
	}
}
