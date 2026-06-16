// Package api 提供模型客户端抽象、工具调用响应结构以及最小可用的后端实现。
// client_snapshot.go 为快照管理：定义 provider-specific 消息快照结构、
// 快照的构造、深度复制、以及从快照恢复到统一消息历史的各种转换函数。
package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProviderMessageSnapshot 表示 provider-specific 消息快照。
// 这里不急着让 Agent 主链完全切成两套消息栈，但先把两侧原生结构稳定落盘，
// 方便后续继续向源仓库的原生恢复、压缩和 provider-specific 处理对齐。
type ProviderMessageSnapshot struct {
	// OpenAI 保存 OpenAI-compatible 原生消息数组。
	OpenAI []map[string]any `json:"openai,omitempty"`
	// AnthropicSystem 保存 Anthropic 独立 system 文本。
	AnthropicSystem string `json:"anthropic_system,omitempty"`
	// AnthropicMessages 保存 Anthropic messages 数组。
	AnthropicMessages []map[string]any `json:"anthropic_messages,omitempty"`
}

// CloneProviderMessageSnapshot 复制 provider-specific 快照，避免运行态直接共享同一底层切片或 map。
// 恢复 session 后我们会把这份原生历史挂回 Agent 运行态，因此这里需要显式深拷贝来保护快照边界。
func CloneProviderMessageSnapshot(snapshot ProviderMessageSnapshot) ProviderMessageSnapshot {
	return ProviderMessageSnapshot{
		OpenAI:            cloneGenericMessageSlice(snapshot.OpenAI),
		AnthropicSystem:   snapshot.AnthropicSystem,
		AnthropicMessages: cloneGenericMessageSlice(snapshot.AnthropicMessages),
	}
}

// BuildProviderMessageSnapshotFromNative 按当前运行态持有的原生 provider 历史构造可持续化快照。
// 这允许 Agent 在 restore 后优先保留原始 native 结构，而不是每次落盘都强制从统一消息主链重新投影。
func BuildProviderMessageSnapshotFromNative(
	openAI []map[string]any,
	anthropicSystem string,
	anthropicMessages []map[string]any,
) ProviderMessageSnapshot {
	return ProviderMessageSnapshot{
		OpenAI:            cloneGenericMessageSlice(openAI),
		AnthropicSystem:   anthropicSystem,
		AnthropicMessages: cloneGenericMessageSlice(anthropicMessages),
	}
}

// BuildProviderMessageSnapshot 把统一消息历史同时投影成 OpenAI-compatible 和 Anthropic 两套原生快照。
// 这一步的目标不是立刻替换 Agent 主链，而是先把 provider-specific 历史完整持久化下来，
// 让后续恢复、压缩与差异化调度具备可靠输入。
func BuildProviderMessageSnapshot(messages []Message) ProviderMessageSnapshot {
	openAISnapshot := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		openAISnapshot = append(openAISnapshot, toGenericSnapshotMap(toOpenAICompatMessage(message)))
	}

	anthropicSystem, anthropicMessages := convertToAnthropicMessages(messages)
	anthropicSnapshot := make([]map[string]any, 0, len(anthropicMessages))
	for _, message := range anthropicMessages {
		anthropicSnapshot = append(anthropicSnapshot, toGenericSnapshotMap(message))
	}

	return ProviderMessageSnapshot{
		OpenAI:            openAISnapshot,
		AnthropicSystem:   anthropicSystem,
		AnthropicMessages: anthropicSnapshot,
	}
}

// BuildMessagesFromProviderSnapshot 从 provider-specific 快照恢复统一消息历史。
// 采用覆盖式恢复策略：优先尝试指定的 provider，按 provider 分别恢复。
// 如果指定 provider 没有数据，则依次尝试所有 provider，取第一个有数据的。
// provider 参数可选（空字符串表示自动探测）。
func BuildMessagesFromProviderSnapshot(snapshot ProviderMessageSnapshot, preferredProvider string) []Message {
	type restoreAttempt struct {
		Provider string
		Restore  func() []Message
	}

	var attempts []restoreAttempt

	switch strings.ToLower(strings.TrimSpace(preferredProvider)) {
	case "anthropic":
		attempts = append(attempts,
			restoreAttempt{
				Provider: "anthropic",
				Restore: func() []Message {
					if len(snapshot.AnthropicMessages) == 0 && strings.TrimSpace(snapshot.AnthropicSystem) == "" {
						return nil
					}
					return buildMessagesFromAnthropicSnapshot(snapshot.AnthropicSystem, snapshot.AnthropicMessages)
				},
			},
			restoreAttempt{
				Provider: "openai",
				Restore: func() []Message {
					if len(snapshot.OpenAI) == 0 {
						return nil
					}
					return buildMessagesFromOpenAISnapshot(snapshot.OpenAI)
				},
			},
		)
	case "openai":
		attempts = append(attempts,
			restoreAttempt{
				Provider: "openai",
				Restore: func() []Message {
					if len(snapshot.OpenAI) == 0 {
						return nil
					}
					return buildMessagesFromOpenAISnapshot(snapshot.OpenAI)
				},
			},
			restoreAttempt{
				Provider: "anthropic",
				Restore: func() []Message {
					if len(snapshot.AnthropicMessages) == 0 && strings.TrimSpace(snapshot.AnthropicSystem) == "" {
						return nil
					}
					return buildMessagesFromAnthropicSnapshot(snapshot.AnthropicSystem, snapshot.AnthropicMessages)
				},
			},
		)
	default:
		attempts = append(attempts,
			restoreAttempt{
				Provider: "openai",
				Restore: func() []Message {
					if len(snapshot.OpenAI) == 0 {
						return nil
					}
					return buildMessagesFromOpenAISnapshot(snapshot.OpenAI)
				},
			},
			restoreAttempt{
				Provider: "anthropic",
				Restore: func() []Message {
					if len(snapshot.AnthropicMessages) == 0 && strings.TrimSpace(snapshot.AnthropicSystem) == "" {
						return nil
					}
					return buildMessagesFromAnthropicSnapshot(snapshot.AnthropicSystem, snapshot.AnthropicMessages)
				},
			},
		)
	}

	for _, attempt := range attempts {
		if restored := attempt.Restore(); len(restored) > 0 {
			return restored
		}
	}
	return nil
}

// toGenericSnapshotMap 把任意结构体用 JSON marshal/unmarshal 走一遍，转成通用 map 快照。
// 这样可以安全持久化 API 相关结构，避免运行态类型泄漏到序列化层。
func toGenericSnapshotMap(input any) map[string]any {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil
	}

	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		return nil
	}
	return output
}

// buildMessagesFromOpenAISnapshot 从 OpenAI-compatible 快照恢复成统一消息历史。
// id 是 OpenAI 原生 tool_calls 恢复的关键桥梁，因此这里对每个消息都做一次 tool_calls 检测。
func buildMessagesFromOpenAISnapshot(snapshot []map[string]any) []Message {
	restored := make([]Message, 0, len(snapshot))
	for _, raw := range snapshot {
		message := Message{
			Role:       stringValue(raw["role"]),
			Content:    stringValue(raw["content"]),
			Name:       stringValue(raw["name"]),
			ToolCallID: stringValue(raw["tool_call_id"]),
		}
		if toolCalls := buildToolCallsFromOpenAISnapshot(raw["tool_calls"]); len(toolCalls) > 0 {
			message.ToolCalls = toolCalls
			if message.Role == "assistant" && strings.TrimSpace(message.Content) == "" {
				message.Content = "[Assistant issued tool calls.]"
			}
		}
		restored = append(restored, message)
	}
	return restored
}

// buildToolCallsFromOpenAISnapshot 从 OpenAI-compatible tool_calls 快照里恢复统一工具调用结构。
func buildToolCallsFromOpenAISnapshot(raw any) []ToolCall {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}

	result := make([]ToolCall, 0, len(items))
	for _, item := range items {
		payload, ok := item.(map[string]any)
		if !ok {
			continue
		}
		functionPayload, _ := payload["function"].(map[string]any)
		result = append(result, ToolCall{
			ID:        stringValue(payload["id"]),
			Name:      stringValue(functionPayload["name"]),
			Arguments: parseToolArguments(stringValue(functionPayload["arguments"])),
		})
	}
	return result
}

// buildMessagesFromAnthropicSnapshot 把 Anthropic system + messages 快照恢复成统一消息历史。
// 该恢复链不追求 100% 无损，而是优先保证 tool_use/tool_result 与 user/assistant 文本能继续驱动主循环。
func buildMessagesFromAnthropicSnapshot(systemText string, snapshot []map[string]any) []Message {
	restored := make([]Message, 0, len(snapshot)+1)
	if strings.TrimSpace(systemText) != "" {
		restored = append(restored, Message{
			Role:    "system",
			Content: systemText,
		})
	}

	for _, raw := range snapshot {
		role := strings.TrimSpace(stringValue(raw["role"]))
		contentItems, _ := raw["content"].([]any)
		switch role {
		case "user":
			userTextParts := make([]string, 0, len(contentItems))
			pendingToolResults := make([]Message, 0, len(contentItems))
			for _, item := range contentItems {
				block, ok := item.(map[string]any)
				if !ok {
					continue
				}
				switch strings.TrimSpace(stringValue(block["type"])) {
				case "text":
					if text := strings.TrimSpace(stringValue(block["text"])); text != "" {
						userTextParts = append(userTextParts, text)
					}
				case "tool_result":
					pendingToolResults = append(pendingToolResults, Message{
						Role:       "tool",
						Content:    stringValue(block["content"]),
						ToolCallID: stringValue(block["tool_use_id"]),
					})
				}
			}
			// 这里先把当前 user block 里的 tool_result 按原顺序落回统一主链，
			// 再补对应的 user text，尽量维持"同一块里的工具结果先于用户补充文本"这一层局部顺序。
			restored = append(restored, pendingToolResults...)
			if len(userTextParts) > 0 {
				restored = append(restored, Message{
					Role:    "user",
					Content: strings.Join(userTextParts, "\n\n"),
				})
			}
		case "assistant":
			assistantTextParts := make([]string, 0, len(contentItems))
			toolCalls := make([]ToolCall, 0, len(contentItems))
			for _, item := range contentItems {
				block, ok := item.(map[string]any)
				if !ok {
					continue
				}
				switch strings.TrimSpace(stringValue(block["type"])) {
				case "text":
					if text := strings.TrimSpace(stringValue(block["text"])); text != "" {
						assistantTextParts = append(assistantTextParts, text)
					}
				case "tool_use":
					inputPayload, _ := block["input"].(map[string]any)
					toolCalls = append(toolCalls, ToolCall{
						ID:        stringValue(block["id"]),
						Name:      stringValue(block["name"]),
						Arguments: convertAnyArgumentsToString(inputPayload),
					})
				}
			}

			content := strings.Join(assistantTextParts, "")
			if strings.TrimSpace(content) == "" && len(toolCalls) > 0 {
				content = "[Assistant issued tool calls.]"
			}
			if strings.TrimSpace(content) != "" || len(toolCalls) > 0 {
				restored = append(restored, Message{
					Role:      "assistant",
					Content:   content,
					ToolCalls: toolCalls,
				})
			}
		}
	}
	return restored
}

// stringValue 统一把 provider 快照里的弱类型字段恢复成字符串。
func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

