// agent_status.go 负责状态/成本展示，包括 Agent 状态摘要、token 统计与成本估算等。
// 该文件从 agent.go 拆分而来。
package agent

import (
	"fmt"
	"strings"
	"mini-claude-code/internal/ui"
)

func (a *Agent) ShowStatus() {
	ui.PrintInfo(a.formatStatusSummary())
}

// ShowCost 杈撳嚭褰撳墠浼氳瘽鐨?token 涓庢垚鏈粺璁°€?
func (a *Agent) ShowCost() {
	ui.PrintCost(a.totalInputTokens, a.totalOutputTokens, a.options.MaxCostUSD, a.turns, a.options.MaxTurns)
}

// formatStatusSummary 生成面向用户的状态摘要。
// 这里把 `/status` 压成更接近主链体验的人读格式，只保留会话、预算、上下文和记忆注入等高价值信息。
func (a *Agent) formatStatusSummary() string {
	// 复用统一的成本摘要文案，避免 `/status` 和 `/cost` 在 token 与预算描述上再次分叉。
	costSummary := a.formatCostSummary()
	// 把记忆注入状态折叠成一句短摘要，既保留迁移排查价值，也避免暴露过多内部标志位。
	memorySummary := fmt.Sprintf(
		"Memories: %d surfaced / %d bytes injected",
		len(a.surfacedMemoryPaths),
		a.sessionMemoryBytes,
	)

	return fmt.Sprintf(
		"Model: %s\nProvider: %s | Mode: %s\nSession: %s | Resume: %t\nMessages: %d | Turns: %d\nPlan file: %s\n%s\nContext: %d window | %d last prompt tokens\n%s",
		a.options.Model,
		a.currentProvider(),
		a.options.PermissionMode,
		fallbackStatusValue(a.sessionID, "(new session)"),
		a.options.Resume,
		len(a.messages),
		a.turns,
		fallbackStatusValue(a.planFilePath, "(none)"),
		costSummary,
		a.effectiveWindow,
		a.lastInputTokenCount,
		memorySummary,
	)
}

// formatCostSummary 生成 token 与成本摘要。
// 单独抽成 helper 后，成本展示可以被测试直接锁定，也便于继续对齐源码仓库的交互格式。
func (a *Agent) formatCostSummary() string {
	budgetInfo := ""
	if a.options.MaxCostUSD > 0 {
		budgetInfo = fmt.Sprintf(" / $%.4f budget", a.options.MaxCostUSD)
	}
	turnInfo := ""
	if a.options.MaxTurns > 0 {
		turnInfo = fmt.Sprintf(" | Turns: %d/%d", a.turns, a.options.MaxTurns)
	}

	return fmt.Sprintf(
		"Tokens: %d in / %d out\nEstimated cost: $%.4f%s%s",
		a.totalInputTokens,
		a.totalOutputTokens,
		a.currentCostUSD(),
		budgetInfo,
		turnInfo,
	)
}

// fallbackStatusValue 为状态展示里的可选字段提供稳定占位文案。
func fallbackStatusValue(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

