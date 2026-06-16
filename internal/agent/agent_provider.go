// agent_provider.go 负责 Provider 相关功能，包括 provider 快照、模型客户端构建、预算控制、snip 压缩等。
// 该文件从 agent.go 拆分而来。
package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"mini-claude-code/internal/api"
	"mini-claude-code/internal/config"
	"mini-claude-code/internal/contextx"
	"mini-claude-code/internal/prompt"
	"mini-claude-code/internal/session"
	"mini-claude-code/internal/tools"
	"mini-claude-code/internal/ui"
)
// currentProviderSnapshot 返回当前运行态维护的 provider-specific 原生快照。
// 这里优先使用 native 历史，而不是直接复用可能来自旧投影的 providerSnapshot 字段。
func (a *Agent) currentProviderSnapshot() api.ProviderMessageSnapshot {

	return api.BuildProviderMessageSnapshotFromNative(
		a.openAINativeMessages,
		a.anthropicSystemPrompt,
		a.anthropicNativeMessages,
	)
}


func (a *Agent) Abort() {
	a.stateMu.RLock()
	cancel := a.currentCancel
	a.stateMu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// IsProcessing 返回当前是否正在处理一次用户输入。
// CLI 会据此把 Ctrl+C 区分为“中断当前轮”或“提示再次按下以退出”。
func (a *Agent) IsProcessing() bool {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.processing
}

// SetConfirmFn 注册一个外部确认回调。
// CLI 会把基于当前 REPL 输入流的确认函数注入进来，让工具确认和主输入循环共用同一套终端交互。
func (a *Agent) SetConfirmFn(fn func(message string) bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.confirmFn = fn
}


func (a *Agent) rebuildSystemPrompt() {
	a.baseSystemPrompt = prompt.Build(a.registry.WorkingDirectory(), a.options.PermissionMode, a.registry)
	if a.options.PermissionMode == tools.PermissionPlan && strings.TrimSpace(a.planFilePath) != "" {
		a.systemPrompt = a.baseSystemPrompt + buildPlanModePrompt(a.planFilePath)
	} else {
		a.systemPrompt = a.baseSystemPrompt
	}

	if len(a.messages) == 0 {
		a.resetMessagesWithSystem([]api.Message{{Role: "system", Content: a.systemPrompt}})
		return
	}
	if a.messages[0].Role == "system" {
		updated := a.messages[0]
		updated.Content = a.systemPrompt
		a.replaceMessageAt(0, updated)
		a.resetMemoryRecallState()
		return
	}

	a.resetMessagesWithSystem(append([]api.Message{{Role: "system", Content: a.systemPrompt}}, a.messages...))
}

// resetMemoryRecallState 重置当前会话的记忆召回运行态。
// 当 system prompt、权限模式或上下文边界发生明显变化时，旧的 surfaced memory 集合与字节预算不再可靠，
// 这里统一清理，避免新上下文沿用旧上下文的记忆召回痕迹。
func (a *Agent) resetMemoryRecallState() {
	a.surfacedMemoryPaths = map[string]struct{}{}
	a.sessionMemoryBytes = 0
}


func (a *Agent) guardTurnBudget() error {
	if a.options.MaxTurns > 0 && a.turns >= a.options.MaxTurns {
		// 这里对齐源码仓库的预算原因文案，让预检路径和主循环中途超限路径共享同一套语义。
		return fmt.Errorf("Turn limit reached (%d >= %d)", a.turns, a.options.MaxTurns)
	}
	if exceeded, reason := a.checkCostBudget(); exceeded {
		return fmt.Errorf(reason)
	}
	return nil
}

// bootstrapResources 涓诲姩鍔犺浇鍛ㄨ竟璧勬簮銆?
func (a *Agent) bootstrapResources() {
	_, _ = a.memoryStore.List()
	_, _ = a.skillStore.Discover()
	_, _ = a.subAgentStore.Discover()
	if a.mcpManager != nil {
		// 只有在首次真正发现到 MCP 工具时才重建 prompt，
		// 避免每轮都因为幂等的 ConnectAndDiscover 结果而重复刷新 system prompt。
		if discovered, err := a.mcpManager.ConnectAndDiscover(context.Background()); err == nil && len(discovered) > 0 {
			a.rebuildSystemPrompt()
		}
	}
}

// ensureReady 鏍￠獙宸ヤ綔鍖轰笌 spec 绾︽潫銆?
func (a *Agent) ensureReady() error {
	if a.workspaceManager != nil {
		if err := a.workspaceManager.EnsureExists(); err != nil {
			return err
		}
	}
	if a.specValidator != nil {
		if _, err := a.specValidator.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// injectKeywordMemories 在语义预取未命中时执行一次同步关键词回退。
// 这条路径继续保留最小可执行保障，确保记忆系统增强失败时不会让主链退化为“完全无召回”。

func (a *Agent) checkAndCompact() {
	if a.effectiveWindow <= 0 {
		return
	}
	if float64(a.lastInputTokenCount) <= float64(a.effectiveWindow)*autoCompactThreshold {
		return
	}
	if a.compactConversation() {
		ui.PrintInfo("Context window filling up, compacting conversation...")
	}
}

// compactConversation 鐢ㄦā鍨嬫憳瑕佹浛鎹腑闂村巻鍙叉秷鎭紝鍙繚鐣?system 鍜屾渶鍚庝竴涓?user 娑堟伅銆?
// 杩欓噷鍏堝鍒绘簮浠撳簱鏈€鍏抽敭鐨勮涓虹洰鏍囷細鎽樿鏉ヨ嚜妯″瀷鑰屼笉鏄湰鍦板瓧绗︿覆鎷兼帴锛?
// 浠庤€岃鎭㈠鍚庣殑浼氳瘽涓婁笅鏂囨洿鎺ヨ繎鈥滃彲缁х画鎵ц鈥濈殑鐪熷疄璇箟锛岃€屼笉鏄急鍗犱綅璇存槑銆?

func resolveEffectiveWindow(model string, defaults contextx.State) int {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if value, ok := modelContextWindows[normalized]; ok {
		if value > 20000 {
			return value - 20000
		}
		return value
	}
	return defaults.EffectiveWindow
}

// modelSupportsThinking 判断当前模型是否具备 Anthropic thinking 能力。
// 这里沿用源仓库的迁移策略：只为 Claude 4 系列打开 thinking 能力，
// 避免把不支持的参数误发到 OpenAI 或更老的 Claude 3 系列模型。
func modelSupportsThinking(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(normalized, "claude-3-") || strings.Contains(normalized, "3-5-") || strings.Contains(normalized, "3-7-") {
		return false
	}
	if strings.Contains(normalized, "claude") &&
		(strings.Contains(normalized, "opus") || strings.Contains(normalized, "sonnet") || strings.Contains(normalized, "haiku")) {
		return true
	}
	return false
}

// modelSupportsAdaptiveThinking 判断模型是否支持 adaptive thinking。
// 当前先对齐源仓库的最小规则：仅 Claude 4.6 家族进入 adaptive，其余支持 thinking 的模型走 enabled。
func modelSupportsAdaptiveThinking(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "opus-4-6") || strings.Contains(normalized, "sonnet-4-6")
}

// getMaxOutputTokens 根据模型名返回更贴近后端能力的最大输出预算。
// 这能让 thinking 模式下的 `max_tokens` 随 `budget_tokens` 同步放大，
// 避免仍然卡在默认 16384 时无法体现 Claude 4.x 的输出上限。
func getMaxOutputTokens(model string) int {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(normalized, "opus-4-6"):
		return 64000
	case strings.Contains(normalized, "sonnet-4-6"):
		return 32000
	case strings.Contains(normalized, "opus-4"),
		strings.Contains(normalized, "sonnet-4"),
		strings.Contains(normalized, "haiku-4"):
		return 32000
	default:
		return 16384
	}
}

// resolveThinkingMode 把布尔开关转换成 provider 侧更细的 thinking 模式。
// 这样 Agent 仍保持简单的 `Options.Thinking` 输入，但 API 层已经能拿到
// `adaptive / enabled / disabled` 三态信息，便于继续向源仓库行为对齐。
func resolveThinkingMode(options Options) string {
	if !options.Thinking {
		return "disabled"
	}
	if !modelSupportsThinking(options.Model) {
		return "disabled"
	}
	if modelSupportsAdaptiveThinking(options.Model) {
		return "adaptive"
}

	return "enabled"
}

func buildModelClient(options Options, provider string) api.Client {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return api.NewClient(api.Config{
			Model:    options.Model,
			APIKey:   options.RuntimeConfig.OpenAIAPIKey,
			BaseURL:  options.RuntimeConfig.OpenAIBaseURL,
			Provider: "openai",
		})
	case "anthropic":
		// 这里为 Anthropic 分支补齐 thinking mode 与输出预算，让 API 层的新参数真正生效。
		return api.NewClient(api.Config{
			Model:           options.Model,
			APIKey:          options.RuntimeConfig.AnthropicAPIKey,
			BaseURL:         options.RuntimeConfig.AnthropicBaseURL,
			Provider:        "anthropic",
			ThinkingMode:    resolveThinkingMode(options),
			MaxOutputTokens: getMaxOutputTokens(options.Model),
		})
	}
	return api.NewClient(api.Config{Model: options.Model})
}
// resolveConfiguredProvider 统一解析当前配置应绑定的 provider。
// 这里把优先级收敛成单一真相，避免建 client、保存 session 和 provider-specific 分支各自猜测。
func resolveConfiguredProvider(runtimeConfig config.Config) string {
	if strings.TrimSpace(runtimeConfig.OpenAIAPIKey) != "" && strings.TrimSpace(runtimeConfig.OpenAIBaseURL) != "" {
		return "openai"
	}
	if strings.TrimSpace(runtimeConfig.AnthropicAPIKey) != "" && strings.TrimSpace(runtimeConfig.AnthropicBaseURL) != "" {
		return "anthropic"
	}
	return "openai"
}

// restoreRuntimeState 鎶?session 閲岀殑杩愯鎬佹仮澶嶅埌褰撳墠 Agent銆?
func (a *Agent) restoreRuntimeState(runtime session.RuntimeState) {
	a.turns = runtime.Turns
	if strings.TrimSpace(runtime.LastResponse) != "" {
		a.lastResponse = runtime.LastResponse
	}
	a.totalInputTokens = runtime.TotalInputTokens
	a.totalOutputTokens = runtime.TotalOutputTokens
	a.lastInputTokenCount = runtime.LastInputTokenCount
	if runtime.EffectiveWindow > 0 {
		a.effectiveWindow = runtime.EffectiveWindow
	}
	if runtime.ContextState.EffectiveWindow > 0 {
		a.contextState = runtime.ContextState
	}
	a.lastAPICallTime = runtime.LastAPICallTime
	a.contextCleared = runtime.ContextCleared
	a.readFileState = cloneTimeMap(runtime.ReadFileState)
	a.surfacedMemoryPaths = map[string]struct{}{}
	for _, path := range runtime.SurfacedMemoryPaths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		a.surfacedMemoryPaths[trimmed] = struct{}{}
	}
	a.sessionMemoryBytes = runtime.SessionMemoryBytes
}

// currentProvider 根据当前运行配置推断会话首选 provider。
// 当前统一改为直接读取运行态绑定值，避免各条链路重新猜测出彼此矛盾的 provider。
func (a *Agent) currentProvider() string {
	if strings.TrimSpace(a.activeProvider) != "" {
		return a.activeProvider
	}
	return "openai"
}

// listSurfacedMemoryPaths 鎶婂凡娉ㄥ叆璁板繂闆嗗悎杞垚鍙寔涔呭寲鍒囩墖銆?
func (a *Agent) listSurfacedMemoryPaths() []string {
	result := make([]string, 0, len(a.surfacedMemoryPaths))
	for path := range a.surfacedMemoryPaths {
		result = append(result, path)
	}
	return result
}

// setProcessingState 统一更新当前轮次的取消句柄与处理中标记。
// 这里集中加锁，避免 HandlePrompt、Abort 和 REPL 状态探测分散改写同一组运行态字段。
func (a *Agent) setProcessingState(cancel context.CancelFunc, processing bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.currentCancel = cancel
	a.processing = processing
}

// confirmToolExecution 根据当前确认回调决定是否放行高风险工具调用。
// 如果 CLI 已注入 confirmFn，就复用当前 REPL 输入流；否则退回到标准输入确认，
// 让 one-shot 或非 REPL 场景也能保留主链同款的 Allow? (y/n) 兜底交互。
func (a *Agent) confirmToolExecution(message string) bool {
	a.stateMu.RLock()
	confirmFn := a.confirmFn
	a.stateMu.RUnlock()

	if confirmFn != nil {
		return confirmFn(message)
	}

	// 没有外部确认回调时，退回到一次性的标准输入确认。
	// 这样脚本单次调用或尚未进入 REPL 的路径也不会直接丢失危险动作审批能力。
	ui.PrintConfirmation(strings.TrimSpace(message))
	fmt.Print("  Allow? (y/n): ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}

	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

// hasConfirmedMessage 判断当前确认消息是否已经在本会话中获批过。
// 这里单独抽成 helper，避免 ExecuteTool 直接操作共享 map 时把会话级确认复用逻辑散落在调用点里。
func (a *Agent) hasConfirmedMessage(message string) bool {
	trimmedMessage := strings.TrimSpace(message)
	if trimmedMessage == "" {
		return false
	}

	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	_, ok := a.confirmedMessages[trimmedMessage]
	return ok
}

// rememberConfirmedMessage 记录一次已经获批的危险动作确认消息。
// 这样后续同一条确认消息再次出现时，就可以直接复用确认结果而不必重复询问用户。
func (a *Agent) rememberConfirmedMessage(message string) {
	trimmedMessage := strings.TrimSpace(message)
	if trimmedMessage == "" {
		return
	}

	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.confirmedMessages == nil {
		a.confirmedMessages = map[string]struct{}{}
	}
	a.confirmedMessages[trimmedMessage] = struct{}{}
}

// resetConfirmedMessages 清空当前会话的危险动作确认白名单。
// 会话被 `/clear` 重置后，旧的确认结果不应继续污染新会话的权限交互边界。
func (a *Agent) resetConfirmedMessages() {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.confirmedMessages = map[string]struct{}{}
}

// cloneTimeMap 澶嶅埗璺緞鏃堕棿鎴虫槧灏勶紝閬垮厤杩愯鏃跺拰鎸佷箙鍖栧璞″叡浜悓涓€寮?map銆?
func cloneTimeMap(input map[string]time.Time) map[string]time.Time {
	result := map[string]time.Time{}
	for key, value := range input {
		result[key] = value
	}
	return result
}

// refreshProviderSnapshot 使用当前统一消息历史重建 provider-specific 快照。
// 这让 compact、clear、restore、system prompt 重建等关键路径都能同步刷新快照，
// 从而把 provider-specific 恢复兜底建立在“最新会话状态”上，而不是陈旧副本上。
func (a *Agent) refreshProviderSnapshot() {
	a.providerSnapshot = api.BuildProviderMessageSnapshot(a.messages)
	a.syncNativeProviderStateFromSnapshot(a.providerSnapshot)
}

// syncNativeProviderStateFromSnapshot 用 provider-specific 快照回填运行态的 native 历史。
// 这样恢复旧 session 或统一主链重建快照之后，Anthropic / OpenAI 分支都能继续读取原生结构。
func (a *Agent) syncNativeProviderStateFromSnapshot(snapshot api.ProviderMessageSnapshot) {
	cloned := api.CloneProviderMessageSnapshot(snapshot)
	a.openAINativeMessages = cloned.OpenAI
	a.anthropicSystemPrompt = cloned.AnthropicSystem
	a.anthropicNativeMessages = cloned.AnthropicMessages
}

// describeProviderSnapshot 输出 provider-specific 快照摘要。
// 这里先不直接把快照灌回运行态主链，而是把归档里是否具备两套原生历史明确展示出来，
// 方便继续验证迁移质量，并为下一阶段的原生恢复切换做准备。
func (a *Agent) describeProviderSnapshot(snapshot api.ProviderMessageSnapshot) string {
	return fmt.Sprintf(
		"openai=%d anthropic_messages=%d anthropic_system=%t",
		len(snapshot.OpenAI),
		len(snapshot.AnthropicMessages),
		strings.TrimSpace(snapshot.AnthropicSystem) != "",
	)
}

