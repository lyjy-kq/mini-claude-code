// agent_compact.go 负责上下文的 snip 压缩、microcompact、大结果持久化等压缩策略相关功能。
// 该文件从 agent_provider.go 拆分而来。
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mini-claude-code/internal/api"
)

// snipCandidate 表示一条可参与 stale-result 裁剪的工具结果候选。
type snipCandidate struct {
	// MessageIndex 表示 tool 消息在统一消息历史中的位置。
	MessageIndex int
	// ToolName 表示对应工具名。
	ToolName string
	// FilePath 表示与该工具结果关联的文件路径，仅在能解析出时填充。
	FilePath string
}

// snipStaleResults 在上下文继续升高时把较旧工具结果替换成占位符。
// 当前策略向源仓库靠近一层：先优先裁掉重复的 read_file 结果，再裁掉更老的结果，
// 避免同一文件被多次读取时旧结果长期占据上下文窗口。
func (a *Agent) snipStaleResults() bool {
	if a.effectiveWindow <= 0 {
		return false
	}
	utilization := float64(a.lastInputTokenCount) / float64(a.effectiveWindow)
	if utilization < a.contextState.SnipThreshold {
		return false
	}

	switch a.currentProvider() {
	case "anthropic":
		return a.snipAnthropicResults()
	default:
		return a.snipOpenAIResults()
	}
}

// microcompact 在长时间闲置后进一步清空更旧的工具结果。
// 返回值表示是否真的清空了旧结果，便于和 provider 快照保持同步。
func (a *Agent) microcompact() bool {
	if a.lastAPICallTime.IsZero() || time.Since(a.lastAPICallTime) < microcompactIdle {
		return false
	}

	switch a.currentProvider() {
	case "anthropic":
		return a.microcompactAnthropicResults()
	default:
		return a.microcompactOpenAIResults()
	}
}

// collectSnipCandidates 收集当前统一消息历史里可参与 stale-result 裁剪的工具结果。
// 这里顺手按 tool_call_id 反查对应的 assistant tool call，
// 这样 snip 策略就能基于工具名和 file_path 做更贴近源仓库的判断。
func (a *Agent) collectSnipCandidates() []snipCandidate {
	candidates := make([]snipCandidate, 0, len(a.messages))
	for index, message := range a.messages {
		if message.Role != "tool" {
			continue
		}
		if message.Content == snipPlaceholder || message.Content == oldResultPlaceholder {
			continue
		}

		toolName := strings.TrimSpace(message.Name)
		filePath := ""
		if associatedCall, ok := a.findAnthropicToolCallByID(message.ToolCallID); ok {
			if toolName == "" {
				toolName = associatedCall.Name
			}
			filePath = strings.TrimSpace(associatedCall.Arguments["file_path"])
		} else if associatedCall, ok := a.findToolCallByID(message.ToolCallID); ok {
			if toolName == "" {
				toolName = associatedCall.Name
			}
			filePath = strings.TrimSpace(associatedCall.Arguments["file_path"])
		}
		if !isSnippableTool(toolName) {
			// 只把可安全裁剪的读工具结果纳入候选，避免把写操作或副作用工具结果错误地当成可丢弃上下文。
			continue
		}

		candidates = append(candidates, snipCandidate{
			MessageIndex: index,
			ToolName:     toolName,
			FilePath:     filePath,
		})
	}
	return candidates
}

// collectAnthropicSnipCandidatesFromSnapshot 按 Anthropic provider 快照里的 tool_result 顺序收集裁剪候选。
// 这样 Anthropic 压缩路径能更贴近源仓库直接遍历 anthropicMessages 的结构，再映射回统一消息索引执行改写。
func (a *Agent) collectAnthropicSnipCandidatesFromSnapshot() []snipCandidate {
	if len(a.anthropicNativeMessages) == 0 {
		return nil
	}

	toolMessageIndexesByID := map[string][]int{}
	for index, message := range a.messages {
		if message.Role != "tool" {
			continue
		}
		if message.Content == snipPlaceholder || message.Content == oldResultPlaceholder {
			continue
		}

		toolCallID := strings.TrimSpace(message.ToolCallID)
		if toolCallID == "" {
			continue
		}
		toolMessageIndexesByID[toolCallID] = append(toolMessageIndexesByID[toolCallID], index)
	}

	candidates := make([]snipCandidate, 0, len(toolMessageIndexesByID))
	for _, rawMessage := range a.anthropicNativeMessages {
		if strings.TrimSpace(stringValue(rawMessage["role"])) != "user" {
			continue
		}

		contentBlocks, ok := rawMessage["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range contentBlocks {
			block, ok := rawBlock.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(block["type"])) != "tool_result" {
				continue
			}

			toolCallID := strings.TrimSpace(stringValue(block["tool_use_id"]))
			if toolCallID == "" {
				continue
			}

			associatedCall, ok := a.findAnthropicToolCallByID(toolCallID)
			if !ok || !isSnippableTool(associatedCall.Name) {
				continue
			}

			indexes := toolMessageIndexesByID[toolCallID]
			if len(indexes) == 0 {
				continue
			}
			messageIndex := indexes[0]
			toolMessageIndexesByID[toolCallID] = indexes[1:]

			candidates = append(candidates, snipCandidate{
				MessageIndex: messageIndex,
				ToolName:     strings.TrimSpace(associatedCall.Name),
				FilePath:     strings.TrimSpace(associatedCall.Arguments["file_path"]),
			})
		}
	}
	return candidates
}

// collectAnthropicToolResultIndexesFromSnapshot 按 Anthropic provider 快照里的 tool_result 顺序收集可清理的统一消息索引。
// 这样 microcompact 路径也能更贴近源仓库直接遍历 anthropicMessages 的顺序，而不是只在统一消息主链上做近似筛选。
func (a *Agent) collectAnthropicToolResultIndexesFromSnapshot() []int {
	if len(a.anthropicNativeMessages) == 0 {
		return nil
	}

	toolMessageIndexesByID := map[string][]int{}
	for index, message := range a.messages {
		if message.Role != "tool" {
			continue
		}
		if strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		if message.Content == snipPlaceholder || message.Content == oldResultPlaceholder {
			continue
		}
		toolMessageIndexesByID[strings.TrimSpace(message.ToolCallID)] = append(
			toolMessageIndexesByID[strings.TrimSpace(message.ToolCallID)],
			index,
		)
	}

	indexes := make([]int, 0, len(toolMessageIndexesByID))
	for _, rawMessage := range a.anthropicNativeMessages {
		if strings.TrimSpace(stringValue(rawMessage["role"])) != "user" {
			continue
		}

		contentBlocks, ok := rawMessage["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range contentBlocks {
			block, ok := rawBlock.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(block["type"])) != "tool_result" {
				continue
			}

			content := stringValue(block["content"])
			if content == snipPlaceholder || content == oldResultPlaceholder {
				continue
			}

			toolCallID := strings.TrimSpace(stringValue(block["tool_use_id"]))
			if toolCallID == "" {
				continue
			}

			matchedIndexes := toolMessageIndexesByID[toolCallID]
			if len(matchedIndexes) == 0 {
				continue
			}
			indexes = append(indexes, matchedIndexes[0])
			toolMessageIndexesByID[toolCallID] = matchedIndexes[1:]
		}
	}
	return indexes
}

// findAnthropicToolCallByID 优先从 Anthropic provider 快照里反查指定 tool_use。
// 这样 Anthropic 压缩链可以尽量直接复用原生 messages 快照里的 tool_use 元数据，而不是只靠统一消息主链近似推断。
func (a *Agent) findAnthropicToolCallByID(toolCallID string) (api.ToolCall, bool) {
	if strings.TrimSpace(toolCallID) == "" {
		return api.ToolCall{}, false
	}

	for messageIndex := len(a.anthropicNativeMessages) - 1; messageIndex >= 0; messageIndex-- {
		message := a.anthropicNativeMessages[messageIndex]
		if strings.TrimSpace(stringValue(message["role"])) != "assistant" {
			continue
		}

		contentBlocks, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range contentBlocks {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(stringValue(block["type"])) != "tool_use" {
				continue
			}
			if strings.TrimSpace(stringValue(block["id"])) != toolCallID {
				continue
			}
			return api.ToolCall{
				ID:        strings.TrimSpace(stringValue(block["id"])),
				Name:      strings.TrimSpace(stringValue(block["name"])),
									Arguments: resolveAnthropicToolInput(block["input"]),
			}, true
		}
	}
	return api.ToolCall{}, false
}

// findToolCallByID 从历史 assistant 消息里反查指定 id 的工具调用。
// 这让统一消息主链也能拿到接近 provider-specific tool_use 的元数据，
// 从而支持更细粒度的压缩与恢复策略。
func (a *Agent) findToolCallByID(toolCallID string) (api.ToolCall, bool) {
	if strings.TrimSpace(toolCallID) == "" {
		return api.ToolCall{}, false
	}

	for messageIndex := len(a.messages) - 1; messageIndex >= 0; messageIndex-- {
		message := a.messages[messageIndex]
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID == toolCallID {
				return call, true
			}
		}
	}
	return api.ToolCall{}, false
}

// persistLargeResult 在工具结果过大时把完整内容落盘，并只把预览写回上下文。
func (a *Agent) persistLargeResult(toolName string, result string) string {
	if len(result) <= largeResultThreshold {
		return result
	}

	outputDir := filepath.Join(a.registry.WorkingDirectory(), ".mini-claude", "tool-results")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return result
	}

	fileName := fmt.Sprintf("%s-%s.txt", time.Now().Format("20060102-150405"), sanitizeToolFileName(toolName))
	fullPath := filepath.Join(outputDir, fileName)
	if err := os.WriteFile(fullPath, []byte(result), 0o644); err != nil {
		return result
	}

	lines := strings.Split(result, "\n")
	previewCount := largeResultPreviewMax
	if len(lines) < previewCount {
		previewCount = len(lines)
	}
	preview := strings.Join(lines[:previewCount], "\n")
	sizeKB := float64(len(result)) / 1024

	return fmt.Sprintf(
		"[Result too large (%.1f KB, %d lines). Full output saved to %s. You can use read_file to see the full result.]\n\nPreview (first %d lines):\n%s",
		sizeKB,
		len(lines),
		fullPath,
		previewCount,
		preview,
	)
}

// snipOpenAIResults 从 OpenAI-compatible 的 tool 消息视角裁掉更旧结果。
// 这里采用保守策略，只保留最近 N 条 tool 消息，把更早结果整体替换成占位符。
func (a *Agent) snipOpenAIResults() bool {
	candidates := make([]snipCandidate, 0, len(a.messages))
	for index, message := range a.messages {
		if message.Role != "tool" {
			continue
		}
		if message.Content == snipPlaceholder || message.Content == oldResultPlaceholder {
			continue
		}
		candidates = append(candidates, snipCandidate{
			MessageIndex: index,
			ToolName:     strings.TrimSpace(message.Name),
		})
	}
	if len(candidates) <= a.contextState.KeepRecentResults {
		return false
	}

	snipBefore := len(candidates) - a.contextState.KeepRecentResults
	if snipBefore <= 0 {
		return false
	}
	for index := 0; index < snipBefore; index++ {
		a.messages[candidates[index].MessageIndex].Content = snipPlaceholder
	}
	return true
}

// snipAnthropicResults 从 Anthropic 的 tool_result 语义视角优先裁掉重复 read_file 旧结果。
// 这条路径会先清理同一路径的旧读取结果，再补最近 N 条保留规则。
func (a *Agent) snipAnthropicResults() bool {
	candidates := a.collectAnthropicSnipCandidatesFromSnapshot()
	if len(candidates) == 0 {
		// 如果当前会话还没有可用的 Anthropic 原生快照，就退回统一消息主链候选收集，
		// 避免旧会话恢复或快照缺失时整条压缩链失效。
		candidates = a.collectSnipCandidates()
	}
	if len(candidates) <= a.contextState.KeepRecentResults {
		return false
	}

	deduped := make([]snipCandidate, 0, len(candidates))
	seenPaths := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.FilePath != "" {
			if seenPaths[candidate.FilePath] {
				deduped = append(deduped, candidate)
				continue
			}
			seenPaths[candidate.FilePath] = true
		}
	}
	snipCount := len(deduped)
	keepCount := a.contextState.KeepRecentResults
	if keepCount < 0 {
		keepCount = 0
	}
	if snipCount > keepCount {
		snipCount = snipCount - keepCount
	} else {
		snipCount = 0
	}
	if snipCount <= 0 {
		return false
	}
	for index := 0; index < snipCount && index < len(deduped); index++ {
		a.messages[deduped[index].MessageIndex].Content = snipPlaceholder
	}
	return true
}

// microcompactOpenAIResults 在长时间闲置后把 OpenAI 路径下更旧工具结果替换为清空占位符。
func (a *Agent) microcompactOpenAIResults() bool {
	candidates := a.collectSnipCandidates()
	if len(candidates) <= a.contextState.KeepRecentResults {
		return false
	}

	clearBefore := len(candidates) - a.contextState.KeepRecentResults
	if clearBefore <= 0 {
		return false
	}
	for index := 0; index < clearBefore; index++ {
		if index < len(candidates) {
			a.messages[candidates[index].MessageIndex].Content = oldResultPlaceholder
		}
	}
	return true
}

// microcompactAnthropicResults 在长时间闲置后根据 Anthropic tool_result 语义视角清空更旧结果。
func (a *Agent) microcompactAnthropicResults() bool {
	indexes := a.collectAnthropicToolResultIndexesFromSnapshot()
	if len(indexes) == 0 {
		return false
	}

	clearCount := len(indexes)
	keepCount := a.contextState.KeepRecentResults
	if keepCount < 0 {
		keepCount = 0
	}
	if clearCount > keepCount {
		clearCount = clearCount - keepCount
	} else {
		clearCount = 0
	}
	if clearCount <= 0 {
		return false
	}
	for index := 0; index < clearCount && index < len(indexes); index++ {
		a.messages[indexes[index]].Content = oldResultPlaceholder
	}
	return true
}

// isSnippableTool 判断指定工具名是否属于可安全裁剪的只读工具。
func isSnippableTool(name string) bool {
	_, ok := snippableToolNames[name]
	return ok
}

// normalizeAnthropicToolArguments 从 Anthropic 的 input 字段恢复 map[string]string 参数。
func normalizeAnthropicToolArguments(rawInput string) map[string]string {
	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		return nil
	}

	var rawMap map[string]any
	if err := json.Unmarshal([]byte(rawInput), &rawMap); err != nil {
		return map[string]string{"input": rawInput}
	}

	result := make([]string, 0, len(rawMap))
	for key := range rawMap {
		result = append(result, key)
	}
	sort.Strings(result)

	normalized := make(map[string]string, len(rawMap))
	for _, key := range result {
		normalized[key] = stringifyAnthropicToolInputValue(rawMap[key])
	}
	return normalized
}

// stringifyAnthropicToolInputValue 把 Anthropic 输入值转成统一参数所需的字符串形式。
// 这样回查出来的 tool_use 元数据既能保留标量，也不会因结构化值而丢失可追踪信息。
func stringifyAnthropicToolInputValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%v", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(encoded)
	}
}

// stringValue 把快照里的任意值尽量转成字符串，供 provider-native 快照回查复用。
// 这里保持最小转换规则，避免为压缩链辅助逻辑引入额外的快照解析依赖。
func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// sanitizeToolFileName 把工具名清洗成安全的文件名片段。
func sanitizeToolFileName(name string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	if strings.TrimSpace(sanitized) == "" {
		return "tool"
	}
	return sanitized
}

// resolveAnthropicToolInput 统一处理 Anthropic tool_use 的 input 字段，支持字符串 JSON 和 map 两种格式。
func resolveAnthropicToolInput(input any) map[string]string {
	switch typed := input.(type) {
	case map[string]any:
		result := make(map[string]string, len(typed))
		for key, value := range typed {
			result[key] = stringifyAnthropicToolInputValue(value)
		}
		return result
	case string:
		return normalizeAnthropicToolArguments(typed)
	default:
		if typed == nil {
			return nil
		}
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		return normalizeAnthropicToolArguments(string(encoded))
	}
}
