// Package api 提供模型客户端抽象、工具调用响应结构以及最小可用的后端实现。
// client_stream.go 为流式解析逻辑：同时覆盖 Anthropic SSE 和 OpenAI-compatible SSE
// 两种协议的事件累积、增量拼接与工具调用提前回调。
package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// anthropicStreamState 表示 Anthropic 流式响应累积器。
type anthropicStreamState struct {
	// Text 保存累积后的 assistant 文本。
	Text strings.Builder
	// Thinking 保存 thinking 增量，便于后续展示与调试。
	Thinking strings.Builder
	// ToolBlocksByIndex 保存按 block 索引累积的 tool_use 内容。
	ToolBlocksByIndex map[int]*anthropicContentBlock
	// Usage 保存最终 token 使用统计。
	Usage Usage
}

// parseAnthropicStreamResponse 解析 Anthropic SSE 流，并重建最终 assistant 响应。
// 这里额外接收一个可选回调，用于在 tool_use 对应的 content block 完整结束时把工具提前抛给上层。
func parseAnthropicStreamResponse(body io.Reader, onToolComplete func(ToolCall)) (Response, error) {
	state := anthropicStreamState{
		ToolBlocksByIndex: map[int]*anthropicContentBlock{},
	}

	reader := bufio.NewReader(body)
	dataLines := make([]string, 0, 4)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return Response{}, err
		}

		trimmedLine := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmedLine, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmedLine, "data:")))
		} else if trimmedLine == "" && len(dataLines) > 0 {
			done, parseErr := consumeAnthropicStreamEvent(strings.Join(dataLines, "\n"), &state, onToolComplete)
			if parseErr != nil {
				return Response{}, parseErr
			}
			dataLines = dataLines[:0]
			if done {
				break
			}
		}

		if err == io.EOF {
			if len(dataLines) > 0 {
				done, parseErr := consumeAnthropicStreamEvent(strings.Join(dataLines, "\n"), &state, onToolComplete)
				if parseErr != nil {
					return Response{}, parseErr
				}
				if done {
					break
				}
			}
			break
		}
	}

	text := strings.TrimSpace(state.Text.String())
	thinking := strings.TrimSpace(state.Thinking.String())
	toolCalls := flattenAnthropicToolBlocks(state.ToolBlocksByIndex)
	return Response{
		Text:       text,
		Thinking:   thinking,
		ToolCalls:  toolCalls,
		RawContent: text,
		Usage:      state.Usage,
	}, nil
}

// consumeAnthropicStreamEvent 处理一个 Anthropic SSE data 事件。
// 当 tool_use 的 block 完整结束时，这里会把已经拼好的输入参数立刻转换成 ToolCall，
// 供上层决定是否提前执行。
func consumeAnthropicStreamEvent(data string, state *anthropicStreamState, onToolComplete func(ToolCall)) (bool, error) {
	if strings.TrimSpace(data) == "" {
		return false, nil
	}

	var event anthropicStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return false, err
	}
	if event.Error != nil {
		return false, fmt.Errorf(event.Error.Message)
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			state.Usage.PromptTokens = event.Message.Usage.InputTokens
			state.Usage.CompletionTokens = event.Message.Usage.OutputTokens
			state.Usage.TotalTokens = state.Usage.PromptTokens + state.Usage.CompletionTokens
		}
	case "message_delta":
		if event.Usage != nil {
			state.Usage.PromptTokens = event.Usage.InputTokens
			state.Usage.CompletionTokens = event.Usage.OutputTokens
			state.Usage.TotalTokens = state.Usage.PromptTokens + state.Usage.CompletionTokens
		} else if event.Message != nil {
			state.Usage.PromptTokens = event.Message.Usage.InputTokens
			state.Usage.CompletionTokens = event.Message.Usage.OutputTokens
			state.Usage.TotalTokens = state.Usage.PromptTokens + state.Usage.CompletionTokens
		}
	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			state.ToolBlocksByIndex[event.Index] = &anthropicContentBlock{
				Type:  "tool_use",
				ID:    event.ContentBlock.ID,
				Name:  event.ContentBlock.Name,
				Input: map[string]any{},
			}
		} else if event.ContentBlock != nil && event.ContentBlock.Type == "text" && strings.TrimSpace(event.ContentBlock.Text) != "" {
			state.Text.WriteString(event.ContentBlock.Text)
		}
	case "content_block_delta":
		if event.Delta == nil {
			return false, nil
		}
		switch event.Delta.Type {
		case "text_delta":
			state.Text.WriteString(event.Delta.Text)
		case "thinking_delta":
			state.Thinking.WriteString(event.Delta.Thinking)
		case "input_json_delta":
			block, ok := state.ToolBlocksByIndex[event.Index]
			if !ok || block == nil {
				return false, nil
			}
			raw := stringifyToolArgument(block.Input["__partial_json"])
			raw += event.Delta.PartialJSON
			block.Input["__partial_json"] = raw
		}
	case "content_block_stop":
		block, ok := state.ToolBlocksByIndex[event.Index]
		if !ok || block == nil {
			return false, nil
		}
		raw := stringifyToolArgument(block.Input["__partial_json"])
		if strings.TrimSpace(raw) != "" {
			var decoded map[string]any
			if json.Unmarshal([]byte(raw), &decoded) == nil {
				block.Input = decoded
			} else {
				block.Input = map[string]any{"raw": raw}
			}
		} else {
			block.Input = map[string]any{}
		}
		if block.Type == "tool_use" && onToolComplete != nil {
			onToolComplete(ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: convertAnyArgumentsToString(block.Input),
			})
		}
	case "message_stop":
		return true, nil
	}
	return false, nil
}

// flattenAnthropicToolBlocks 把按索引累积的 Anthropic tool_use blocks 恢复成内部工具调用切片。
func flattenAnthropicToolBlocks(byIndex map[int]*anthropicContentBlock) []ToolCall {
	if len(byIndex) == 0 {
		return nil
	}

	indexes := make([]int, 0, len(byIndex))
	for index := range byIndex {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	result := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		block := byIndex[index]
		if block == nil {
			continue
		}
		result = append(result, ToolCall{
			ID:        block.ID,
			Name:      block.Name,
			Arguments: convertAnyArgumentsToString(block.Input),
		})
	}
	return result
}

// parseStreamResponse 解析 OpenAI-compatible SSE 流，并在本地重建最终 assistant 消息。
func parseStreamResponse(body io.Reader) (Response, error) {
	state := openAICompatStreamState{
		ToolCallsByIndex: map[int]*openAICompatToolCall{},
	}

	reader := bufio.NewReader(body)
	dataLines := make([]string, 0, 4)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return Response{}, err
		}

		trimmedLine := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmedLine, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmedLine, "data:")))
		} else if trimmedLine == "" && len(dataLines) > 0 {
			done, parseErr := consumeStreamEvent(strings.Join(dataLines, "\n"), &state)
			if parseErr != nil {
				return Response{}, parseErr
			}
			dataLines = dataLines[:0]
			if done {
				break
			}
		}

		if err == io.EOF {
			if len(dataLines) > 0 {
				done, parseErr := consumeStreamEvent(strings.Join(dataLines, "\n"), &state)
				if parseErr != nil {
					return Response{}, parseErr
				}
				if done {
					break
				}
			}
			break
		}
	}

	message := openAICompatChoiceMessage{
		Content:   state.Content.String(),
		ToolCalls: flattenToolCalls(state.ToolCallsByIndex),
	}
	response := parseOpenAICompatMessage(message)
	response.Usage = state.Usage
	return response, nil
}

// consumeStreamEvent 处理一个 OpenAI-compatible SSE data 事件。
func consumeStreamEvent(data string, state *openAICompatStreamState) (bool, error) {
	if strings.TrimSpace(data) == "" {
		return false, nil
	}
	if strings.TrimSpace(data) == "[DONE]" {
		return true, nil
	}

	var chunk openAICompatStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, err
	}
	if chunk.Error != nil {
		return false, fmt.Errorf(chunk.Error.Message)
	}
	if chunk.Usage != nil {
		state.Usage = *chunk.Usage
	}

	for _, choice := range chunk.Choices {
		if strings.TrimSpace(choice.Delta.Content) != "" {
			state.Content.WriteString(choice.Delta.Content)
		}
		for _, call := range choice.Delta.ToolCalls {
			aggregateToolCallDelta(state.ToolCallsByIndex, call)
		}
		if strings.TrimSpace(choice.FinishReason) != "" {
			state.FinishReason = choice.FinishReason
		}
	}
	return false, nil
}

// aggregateToolCallDelta 把流式 tool_call 增量累积到索引映射中。
func aggregateToolCallDelta(target map[int]*openAICompatToolCall, delta openAICompatToolCallDelta) {
	current, ok := target[delta.Index]
	if !ok {
		current = &openAICompatToolCall{
			Function: openAICompatFunctionCall{},
		}
		target[delta.Index] = current
	}

	if strings.TrimSpace(delta.ID) != "" {
		current.ID = delta.ID
	}
	if strings.TrimSpace(delta.Type) != "" {
		current.Type = delta.Type
	}
	if strings.TrimSpace(delta.Function.Name) != "" {
		current.Function.Name += delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		current.Function.Arguments += delta.Function.Arguments
	}
}

// flattenToolCalls 把按索引累积的工具调用映射恢复成有序切片。
func flattenToolCalls(byIndex map[int]*openAICompatToolCall) []openAICompatToolCall {
	if len(byIndex) == 0 {
		return nil
	}

	indexes := make([]int, 0, len(byIndex))
	for index := range byIndex {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	result := make([]openAICompatToolCall, 0, len(byIndex))
	for _, index := range indexes {
		call := byIndex[index]
		if call == nil {
			continue
		}
		if strings.TrimSpace(call.Type) == "" {
			call.Type = "function"
		}
		result = append(result, *call)
	}
	return result
}
