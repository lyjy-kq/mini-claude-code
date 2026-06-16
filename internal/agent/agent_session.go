// agent_session.go 负责会话管理，包括上下文压缩、会话重置、会话恢复、会话列表、技能/记忆查询等。
// 该文件从 agent.go 拆分而来。
package agent

import (
	"fmt"
	"strings"
	"time"
	"sort"
	"mini-claude-code/internal/api"
	"mini-claude-code/internal/memory"
	"mini-claude-code/internal/session"
	"mini-claude-code/internal/skills"
	"mini-claude-code/internal/tools"
	"mini-claude-code/internal/ui"
)

func (a *Agent) Compact() {
	if a.compactConversation() {
		ui.PrintInfo("Conversation compacted.")
		return
	}
	ui.PrintInfo("Conversation is too short to compact.")
}

// ClearSession 閲嶇疆褰撳墠浼氳瘽鐘舵€併€?
func (a *Agent) ClearSession() error {
	a.turns = 0
	a.lastResponse = ""
	a.messages = []api.Message{{Role: "system", Content: a.systemPrompt}}
	a.readFileState = map[string]time.Time{}
	a.sessionID = time.Now().Format("20060102-150405")
	a.sessionStartTime = time.Now()
	a.totalInputTokens = 0
	a.totalOutputTokens = 0
	a.lastInputTokenCount = 0
	a.contextCleared = false
	a.lastAPICallTime = time.Time{}
	a.resetMemoryRecallState()
	a.resetConfirmedMessages()
	a.refreshProviderSnapshot()
	if err := a.sessionStore.ClearLatest(); err != nil {
		return err
	}
	// 这里补一条显式回显，让 `/clear` 在用户视角上与源码仓库一样“清空后立刻可见”。
	ui.PrintInfo("Conversation cleared.")
	return nil
}

// ResumeLatest 恢复最近一次会话。
func (a *Agent) ResumeLatest() (string, error) {
	entry, err := a.sessionStore.LoadLatest()
	if err != nil {
		return "", err
	}
	return a.restoreSessionEntry(entry), nil
}

// ResumeSessionByID 根据会话 id 恢复指定归档会话。
func (a *Agent) ResumeSessionByID(id string) (string, error) {
	entry, err := a.sessionStore.LoadByID(id)
	if err != nil {
		return "", err
	}
	return a.restoreSessionEntry(entry), nil
}

// WantsResume 返回当前 Agent 是否以恢复模式启动。
func (a *Agent) WantsResume() bool {
	return a.options.Resume
}

// Abort 中断当前正在执行的用户轮次。
// 这里不会清空既有消息历史，只负责触发当前上下文的取消，让 REPL 可以继续接收后续输入。

// DeferredToolNames 杩斿洖鏈縺娲荤殑寤惰繜宸ュ叿鍚嶇О銆?
func (a *Agent) DeferredToolNames() []string {
	return a.registry.DeferredToolNames()
}

// ListSessions 杩斿洖褰撳墠宸ヤ綔鍖虹殑浼氳瘽褰掓。鍒楄〃銆?
func (a *Agent) ListSessions() ([]session.Metadata, error) {
	return a.sessionStore.List()
}

// ListSkills 返回当前工作区可见的技能定义列表。
// 这给 REPL 提供一个显式入口，避免技能系统只存在于 system prompt 里而用户无法直接检查当前发现结果。
func (a *Agent) ListSkills() ([]skills.Skill, error) {
	if a.skillStore == nil {
		return nil, nil
	}
	return a.skillStore.Discover()
}

// ResolveSkillPrompt 根据技能名和参数展开技能 prompt。
// 这给 CLI 提供一个稳定入口，让 inline 技能可以像源仓库一样直接注入主对话继续执行。
func (a *Agent) ResolveSkillPrompt(name string, args string) (string, error) {
	if a.skillStore == nil {
		return "", fmt.Errorf("skill store is unavailable")
	}

	skillDefinition, err := a.skillStore.GetByName(name)
	if err != nil {
		return "", err
	}
	if skillDefinition == nil {
		return "", fmt.Errorf("skill not found: %s", name)
	}
	return a.skillStore.ResolvePrompt(*skillDefinition, args), nil
}

// ListMemories 返回当前工作区保存的记忆文件列表。
// 这让 REPL 可以像源仓库一样把 memory 系统显式展示出来，便于用户验证持久化上下文当前是否已生效。
func (a *Agent) ListMemories() ([]string, error) {
	if a.memoryStore == nil {
		return nil, nil
	}
	return a.memoryStore.List()
}

// ListMemoryEntries 返回当前工作区保存的完整记忆条目。
// 这给 CLI 一个更丰富的展示入口，让 `/memory` 不再只能列文件名，而是能展示类型和描述。
func (a *Agent) ListMemoryEntries() ([]memory.Entry, error) {
	if a.memoryStore == nil {
		return nil, nil
	}
	return a.memoryStore.ListEntries()
}

// CurrentPlanContent 返回当前 plan mode 对应的计划文件正文。
// 这给 CLI 提供一个只读入口，让交互式审批可以先展示计划，再决定执行路径。
func (a *Agent) CurrentPlanContent() (string, error) {
	if !a.InPlanMode() {
		return "", fmt.Errorf("plan mode is not active")
	}
	return a.readCurrentPlan()
}

// MCPStatusSummary 返回当前 MCP 配置、连接和发现结果的摘要文本。
// 这给 REPL 提供一个显式入口，让用户直接检查 MCP 是否已经接通。
func (a *Agent) MCPStatusSummary() string {
	if a.mcpManager == nil {
		return "MCP manager is unavailable."
	}

	snapshot := a.mcpManager.StatusSnapshot()
	lines := []string{
		fmt.Sprintf(
			"configured_servers=%d connected_servers=%d discovered_tools=%d",
			len(snapshot.ConfiguredServers),
			len(snapshot.ConnectedServers),
			snapshot.ToolCount,
		),
	}

	if len(snapshot.ConfiguredServers) > 0 {
		lines = append(lines, "configured: "+strings.Join(snapshot.ConfiguredServers, ", "))
	}
	if len(snapshot.ConnectedServers) > 0 {
		lines = append(lines, "connected: "+strings.Join(snapshot.ConnectedServers, ", "))
	}
	if len(snapshot.Errors) > 0 {
		errorNames := make([]string, 0, len(snapshot.Errors))
		for name := range snapshot.Errors {
			errorNames = append(errorNames, name)
		}
		sort.Strings(errorNames)
		for _, name := range errorNames {
			lines = append(lines, fmt.Sprintf("error[%s]: %s", name, snapshot.Errors[name]))
		}
	}
	if len(lines) == 1 && snapshot.ToolCount == 0 {
		lines = append(lines, "No MCP servers configured or discovered.")
	}
	return strings.Join(lines, "\n")
}

func (a *Agent) restoreSessionEntry(entry session.Entry) string {
	a.lastResponse = entry.Response
	a.sessionID = entry.Metadata.ID
	if !entry.Metadata.StartTime.IsZero() {
		a.sessionStartTime = entry.Metadata.StartTime
	}
	restoredFromUnifiedMessages := false
	// 优先沿用 session 落盘时的 provider，只有旧归档缺字段时才退回当前环境推断。
	// 这样可以减少“切换环境变量后恢复旧会话”导致的 provider 恢复偏移。
	preferredProvider := strings.TrimSpace(entry.Metadata.Provider)
	if preferredProvider == "" {
		preferredProvider = a.currentProvider()
	}
	// 恢复会话时同步切换当前运行态 provider 与模型客户端，
	// 避免 session 已经恢复到旧后端语义，但后续请求仍沿用启动时绑定的另一个 client。
	a.activeProvider = preferredProvider
	a.modelClient = buildModelClient(a.options, a.activeProvider)
	if len(entry.Messages) > 0 {
		a.messages = entry.Messages
		restoredFromUnifiedMessages = true
	} else if restored := api.BuildMessagesFromProviderSnapshot(entry.ProviderMessages, preferredProvider); len(restored) > 0 {
		// 当统一消息历史缺失时，优先使用已落盘的 provider-specific 快照做恢复兜底，
		// 让 Anthropic / OpenAI 会话至少能继续跑起来，而不是直接退回空白 system-only 会话。
		a.messages = restored
	} else {
		a.messages = []api.Message{{Role: "system", Content: a.systemPrompt}}
	}

	// 如果恢复出的主链自带 system message，就同步刷新运行态 systemPrompt，
	// 避免后续 clear / rebuild 继续误用“启动时提示词”而不是“恢复后的真实提示词”。
	a.syncSystemPromptFromMessages()

	if restoredFromUnifiedMessages {
		// 当统一消息历史存在时，优先把它视作当前最权威状态，并即时重建 provider-specific 快照。
		a.refreshProviderSnapshot()
	} else {
		a.providerSnapshot = api.CloneProviderMessageSnapshot(entry.ProviderMessages)
		a.syncNativeProviderStateFromSnapshot(a.providerSnapshot)
		if currentSnapshot := a.currentProviderSnapshot(); len(currentSnapshot.OpenAI) == 0 &&
			len(currentSnapshot.AnthropicMessages) == 0 &&
			strings.TrimSpace(currentSnapshot.AnthropicSystem) == "" {
			a.refreshProviderSnapshot()
		}
	}
	a.restoreRuntimeState(entry.Runtime)

	// 当前阶段先把 provider-specific 快照作为会话恢复结果的一部分显式保留下来，
	// 既帮助校验归档质量，也为下一步把恢复逻辑切向原生消息栈提供直接证据。
	providerSnapshotSummary := a.describeProviderSnapshot(a.currentProviderSnapshot())

	return a.formatRestoreSummary(entry, providerSnapshotSummary)
}

// formatRestoreSummary 生成恢复会话后的用户可见摘要。
// 这里先给出源码仓库同款的“Session restored”直观反馈，再补充 Go 版当前更关心的 provider 与 token 状态，方便继续排查恢复质量。
func (a *Agent) formatRestoreSummary(entry session.Entry, providerSnapshotSummary string) string {
	summaryLines := []string{
		fmt.Sprintf("Session restored (%d messages).", len(a.messages)),
		fmt.Sprintf("Model: %s", fallbackStatusValue(entry.Model, a.options.Model)),
		fmt.Sprintf("Session ID: %s", fallbackStatusValue(a.sessionID, "(unknown)")),
		fmt.Sprintf("Turns: %d", a.turns),
		fmt.Sprintf("Tokens: %d in / %d out", a.totalInputTokens, a.totalOutputTokens),
		fmt.Sprintf("Provider snapshot: %s", fallbackStatusValue(providerSnapshotSummary, "(empty)")),
	}
	if !entry.Timestamp.IsZero() {
		summaryLines = append(summaryLines, "Restored at: "+entry.Timestamp.Format(time.RFC3339))
	}
	if strings.TrimSpace(entry.Prompt) != "" {
		summaryLines = append(summaryLines, "Last prompt: "+strings.TrimSpace(entry.Prompt))
	}
	return strings.Join(summaryLines, "\n")
}

// syncSystemPromptFromMessages 尝试用当前消息主链里的 system message 回写运行态提示词。
// 这样恢复后的 clear / rebuild / compact 路径都会基于已恢复会话的真实 system prompt 继续工作。
func (a *Agent) syncSystemPromptFromMessages() {
	if len(a.messages) == 0 || a.messages[0].Role != "system" {
		return
	}
	if strings.TrimSpace(a.messages[0].Content) == "" {
		return
	}

	a.systemPrompt = a.messages[0].Content
	if a.options.PermissionMode != tools.PermissionPlan {
		a.baseSystemPrompt = a.systemPrompt
	}
}


func messageIndexInRange(index int, total int) bool {
	return index >= 0 && index < total
}


// truncateInlineSummary 鍘嬬缉闀挎枃鏈紝閬垮厤鎽樿娑堟伅鑷韩鑶ㄨ儉銆?
func truncateInlineSummary(input string, maxLen int) string {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) <= maxLen {
		return trimmed
	}
	if maxLen <= 3 {
		return trimmed[:maxLen]
	}
	return trimmed[:maxLen-3] + "..."
}

// sanitizeToolContentForSummary 把工具结果正文压成更适合摘要模型理解的形式。
// 这里会把压缩占位符和大结果预览改写成短说明，减少摘要把“上下文维护痕迹”误当作核心工作内容。
func sanitizeToolContentForSummary(input string) string {
	trimmed := strings.TrimSpace(input)
	switch trimmed {
	case "":
		return ""
	case snipPlaceholder:
		return "[Older tool output was snipped; re-read from the source if needed.]"
	case oldResultPlaceholder:
		return "[Older tool output was cleared during context compaction.]"
	default:
		return stripLargeResultPreview(trimmed)
	}
}

// normalizeAssistantContentForSummary 把 assistant 消息压成更适合摘要模型理解的形式。
// 当 unified history 里的 assistant 内容是 JSON bridge 时，这里优先抽取 assistant_text；
// 如果只有 tool_calls 没有正文，则改写成一条短说明，避免把整段 JSON 噪声塞进摘要请求。
func normalizeAssistantContentForSummary(message api.Message) string {
	assistantText := api.ExtractAssistantText(message)
	if strings.TrimSpace(assistantText) != "" {
		return assistantText
	}
	if len(message.ToolCalls) > 0 {
		return fmt.Sprintf("[Assistant issued %d tool call(s).]", len(message.ToolCalls))
	}
	return strings.TrimSpace(message.Content)
}

// normalizeMemoryRemindersForSummary 把注入到 user message 里的 system-reminder 记忆块改写成摘要友好文本。
// 这里会去掉标签外壳，但保留 freshness 提示、来源路径和正文内容，避免摘要把 `<system-reminder>` 本身当成关键信息。
func normalizeMemoryRemindersForSummary(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}

	normalized := strings.ReplaceAll(trimmed, "<system-reminder>\n", "[Recalled memory]\n")
	normalized = strings.ReplaceAll(normalized, "\n</system-reminder>", "")
	normalized = strings.ReplaceAll(normalized, "<system-reminder>", "[Recalled memory]")
	normalized = strings.ReplaceAll(normalized, "</system-reminder>", "")
	return strings.TrimSpace(normalized)
}

// stripLargeResultPreview 把“大工具结果已落盘 + preview”文本压成更短的摘要友好说明。
// 这样 compact 生成摘要时仍然知道“有大结果被保存过”，但不会把 preview 正文也吞进摘要里。
func stripLargeResultPreview(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}

	marker := "Full output saved to "
	markerIndex := strings.Index(trimmed, marker)
	if markerIndex < 0 {
		return trimmed
	}

	previewIndex := strings.Index(trimmed, "\n\nPreview")
	if previewIndex < 0 {
		return trimmed
	}

	summaryText := strings.TrimSpace(trimmed[:previewIndex])
	if summaryText == "" {
		return "[Large tool result omitted from summary input; full output was saved separately.]"
	}
	return summaryText
}

// stripTrailingDuplicateUserText 尝试移除摘要末尾对最后一条 user message 的明显重复。
// 这里只做保守裁剪：只有当摘要尾部直接包含最后用户消息，或包含其长前缀时才移除，
// 避免把真正重要的总结内容误删掉。
func stripTrailingDuplicateUserText(summary string, lastUserText string) string {
	trimmedSummary := strings.TrimSpace(summary)
	trimmedUser := strings.TrimSpace(lastUserText)
	if trimmedSummary == "" || trimmedUser == "" {
		return trimmedSummary
	}

	if strings.HasSuffix(trimmedSummary, trimmedUser) {
		return strings.TrimSpace(strings.TrimSuffix(trimmedSummary, trimmedUser))
	}

	// 对很长的 user message，只拿一个较长前缀做尾部重复检测，
	// 兼容模型只复述了问题开头而不是逐字整段照搬的情况。
	runes := []rune(trimmedUser)
	if len(runes) >= 40 {
		prefix := string(runes[:40])
		if strings.HasSuffix(trimmedSummary, prefix) {
			return strings.TrimSpace(strings.TrimSuffix(trimmedSummary, prefix))
		}
	}
	return trimmedSummary
}

