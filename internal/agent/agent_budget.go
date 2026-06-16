// agent_budget.go 负责预算控制、上下文压缩和成本统计相关功能。
// 该文件从 agent_messages.go 拆分而来。
package agent

import (
	"fmt"
	"strings"

	"time"
	"mini-claude-code/internal/api"
)

// recordUsage 累计 token 使用量。
func (a *Agent) recordUsage(usage api.Usage) {
	a.totalInputTokens += usage.PromptTokens
	a.totalOutputTokens += usage.CompletionTokens
	a.lastInputTokenCount = usage.PromptTokens
	a.lastAPICallTime = time.Now()
}

// currentCostUSD 估算累计成本。
func (a *Agent) currentCostUSD() float64 {
	const inputRatePer1K = 0.003
	const outputRatePer1K = 0.015
	inputCost := float64(a.totalInputTokens) / 1000 * inputRatePer1K
	outputCost := float64(a.totalOutputTokens) / 1000 * outputRatePer1K
	return inputCost + outputCost
}

// checkCostBudget 检查成本预算是否超限。
func (a *Agent) checkCostBudget() (bool, string) {
	if a.options.MaxCostUSD <= 0 {
		return false, ""
	}
	current := a.currentCostUSD()
	if current >= a.options.MaxCostUSD {
		return true, fmt.Sprintf("Cost limit reached ($%.4f >= $%.4f)", current, a.options.MaxCostUSD)
	}
	return false, ""
}

// runCompressionPipeline 依次执行 snip 和 microcompact 压缩。
func (a *Agent) runCompressionPipeline() {
	if a.effectiveWindow <= 0 {
		return
	}
	if a.snipStaleResults() {
		a.refreshProviderSnapshot()
	}
	if a.microcompact() {
		a.refreshProviderSnapshot()
	}
}

// compactConversation 用模型摘要替换中间历史消息，只保留 system 和最后一个 user 消息。
func (a *Agent) compactConversation() bool {
	if a.effectiveWindow <= 0 {
		return false
	}
	utilization := float64(a.lastInputTokenCount) / float64(a.effectiveWindow)
	if utilization < autoCompactThreshold {
		return false
	}

	lastUserIndex := -1
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "user" {
			lastUserIndex = i
			break
		}
	}
	if lastUserIndex <= 1 {
		return false
	}

	lastUserMessage := a.messages[lastUserIndex]
	summary := a.generateConversationSummary(lastUserMessage)
	if strings.TrimSpace(summary) == "" {
		return false
	}

	systemMsg := a.messages[0]
	newMessages := []api.Message{systemMsg}
	if strings.TrimSpace(summary) != "" {
		newMessages = append(newMessages, api.Message{
			Role:    "assistant",
			Content: summary,
		})
	}
	newMessages = append(newMessages, lastUserMessage)
	a.resetMessagesWithSystem(newMessages)
	return true
}

// generateConversationSummary 为摘要压缩生成摘要文本。
func (a *Agent) generateConversationSummary(lastUserMessage api.Message) string {
	return fmt.Sprintf("[Conversation history compacted. Last user message repeated below for context.]\n\n%s", strings.TrimSpace(lastUserMessage.Content))
}

// sanitizeMessageForSummary 清洗消息内容，使其适合放入摘要 prompt。
func (a *Agent) sanitizeMessageForSummary(message api.Message) api.Message {
	sanitized := message
	switch message.Role {
	case "tool":
		if len(sanitized.Content) > 500 {
			sanitized.Content = sanitized.Content[:500] + "... [truncated]"
		}
	case "user":
		sanitized.Content = normalizeMemoryRemindersForSummary(message.Content)
	}
	return sanitized
}
