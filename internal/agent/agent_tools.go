// agent_tools.go 负责工具执行，包括工具调用入口、结果处理、批量编排、读前写检查、预算工具结果压缩等。
// 该文件从 agent.go 拆分而来。
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"mini-claude-code/internal/api"
	"mini-claude-code/internal/tools"
	"mini-claude-code/internal/ui"
)

func (a *Agent) ExecuteTool(ctx context.Context, call tools.Invocation) tools.Result {
	switch call.Name {
	case "enter_plan_mode":
		return tools.Result{Output: a.EnterPlanMode()}
	case "exit_plan_mode":
		return tools.Result{Output: a.ExitPlanMode()}
	case "skill":
		return a.executeSkillTool(call)
	case "tool_search":
		matches := a.registry.ActivateDeferredTools(call.Arguments["query"])
		a.rebuildSystemPrompt()
		lines := make([]string, 0, len(matches))
		for _, match := range matches {
			lines = append(lines, tools.FormatToolDefinition(match))
		}
		if len(lines) == 0 {
			return tools.Result{Output: "No matching deferred tools found."}
		}
		return tools.Result{Output: strings.Join(lines, "\n\n")}
	}

	definition, ok := a.registry.ToolByName(call.Name)
	if !ok {
		return tools.Result{Error: fmt.Errorf("tool not found: %s", call.Name)}
	}

	permissionDecision := tools.CheckPermission(a.options.PermissionMode, *definition, call, tools.PermissionContext{
		PlanFilePath: a.planFilePath,
	})
	switch permissionDecision.Action {
	case "deny":
		// 这里补一条显式拒绝提示，让权限系统拦截工具时能像源码仓库一样先把“为什么被拒绝”直接告诉用户。
		ui.PrintInfo("Denied: " + permissionDecision.Message)
		return tools.Result{Error: tools.PermissionError(permissionDecision)}
	case "confirm":
		// 和源码仓库保持一致：同一条确认消息一旦在当前会话获批，后续重复出现时直接复用，
		// 避免模型在批量创建文件或重复运行同一危险命令时反复打断用户。
		if a.hasConfirmedMessage(permissionDecision.Message) {
			break
		}
		if !a.confirmToolExecution(permissionDecision.Message) {
			return tools.Result{Error: fmt.Errorf("user denied this action")}
		}
		a.rememberConfirmedMessage(permissionDecision.Message)
	}

	// 鍦ㄧ湡姝ｆ墽琛屽啓宸ュ叿鍓嶈ˉ涓€灞傗€滃厛璇诲悗鍐?/ 缂栬緫鈥濈害鏉燂紝淇濊瘉涓婚摼琛屼负涓€鑷淬€?
	if err := a.ensureReadBeforeWrite(call); err != nil {
		return tools.Result{Error: err}
	}

	// agent 宸ュ叿闇€瑕佺敱璋冨害鍣ㄦ帴绠★紝鑰屼笉鏄户缁笅鍙戝埌鏈湴宸ュ叿鎵ц灞傘€?
	if call.Name == "agent" {
		return a.executeAgentTool(call)
	}

	result := a.registry.Execute(ctx, call)
	a.updateReadStateAfterTool(call, result)
	return result
}

// ShowStatus 杈撳嚭褰撳墠 Agent 鐨勫熀纭€鐘舵€併€?

type toolExecutionBatch struct {
	// Concurrent 琛ㄧず璇ユ壒娆℃槸鍚﹀簲璇ュ苟鍙戞墽琛屻€?
	Concurrent bool
	// Calls 淇濆瓨鎵规涓殑宸ュ叿璋冪敤銆?
	Calls []api.ToolCall
}

// buildToolExecutionBatches 鎶婂伐鍏疯皟鐢ㄥ垏鎴愨€滆繛缁苟鍙戝畨鍏ㄥ伐鍏锋壒娆?+ 鏅€氫覆琛屾壒娆♀€濄€?
// 杩欐牱鏃㈣兘鑾峰緱婧愪粨搴撻偅绉嶆壒閲忚宸ュ叿鎻愰€燂紝鍙堜笉浼氶噸鎺掗渶瑕佷繚鎸侀『搴忕殑鍓綔鐢ㄥ伐鍏枫€?
func (a *Agent) buildToolExecutionBatches(calls []api.ToolCall) []toolExecutionBatch {
	batches := make([]toolExecutionBatch, 0, len(calls))
	for _, call := range calls {
		concurrent := tools.IsConcurrencySafeTool(call.Name)
		if len(batches) > 0 && batches[len(batches)-1].Concurrent == concurrent {
			batches[len(batches)-1].Calls = append(batches[len(batches)-1].Calls, call)
			continue
		}
		batches = append(batches, toolExecutionBatch{
			Concurrent: concurrent,
			Calls:      []api.ToolCall{call},
		})
	}
	return batches
}

// resolveToolExecutionResult 缁熶竴瑙ｆ瀽宸ュ叿鎵ц缁撴灉鏉ユ簮銆?
// 濡傛灉璇ュ伐鍏峰湪娴佸紡闃舵宸茬粡鎻愬墠鍚姩锛屽垯杩欓噷绛夊緟骞跺鐢ㄧ粨鏋滐紱鍚﹀垯鎸夊師閫昏緫鍗虫椂鎵ц銆?
func (a *Agent) resolveToolExecutionResult(ctx context.Context, toolCall api.ToolCall, earlyExecutions map[string]chan tools.Result) tools.Result {
	if resultCh, ok := earlyExecutions[toolCall.ID]; ok && resultCh != nil {
		return <-resultCh
	}
	return a.ExecuteTool(ctx, tools.Invocation{
		Name:      toolCall.Name,
		Arguments: toolCall.Arguments,
	})
}

// appendToolResultMessage 璐熻矗鎶婂伐鍏锋墽琛岀粨鏋滄爣鍑嗗寲鍚庡啓鍥炴秷鎭巻鍙层€?
// 杩欐牱涓茶鍜屽苟鍙戜袱绉嶈矾寰勫叡鐢ㄥ悓涓€濂楃粨鏋滆惤鐩樹笌娑堟伅杩藉姞閫昏緫锛屽噺灏戝垎鏀亸宸€?
func (a *Agent) appendToolResultMessage(toolCall api.ToolCall, result tools.Result) {
	toolOutput := result.Output
	if result.Error != nil {
		// 权限拒绝与用户拒绝属于模型需要继续理解的控制流结果，
		// 这里单独翻译成主链同款文案，避免被通用 error 前缀稀释掉语义。
		toolOutput = a.formatToolErrorOutput(result.Error)
	}
	toolOutput = a.persistLargeResult(toolCall.Name, toolOutput)

	// 如果此前刚发生过 clear-context 边界，这个执行结果就不应继续作为旧 assistant tool call 的结果回写。
	// 这里直接改写成新的 user message，让统一历史、provider 快照和恢复链都把它视为新的上下文起点。
	if a.contextCleared {
		a.appendMessage(api.Message{
			Role:    "user",
			Content: toolOutput,
		})
		a.contextCleared = false
		return
	}

	a.appendMessage(api.Message{
		Role:       "tool",
		Name:       toolCall.Name,
		Content:    toolOutput,
		ToolCallID: toolCall.ID,
	})
}

// formatToolErrorOutput 把工具错误翻译成更贴近主链语义的 tool result 文本。
// 这里优先识别权限系统与用户确认拒绝，再回退到通用 error 前缀，保证模型能读懂“为何没执行”。
func (a *Agent) formatToolErrorOutput(toolErr error) string {
	if toolErr == nil {
		return ""
	}

	trimmedMessage := strings.TrimSpace(toolErr.Error())
	switch {
	case trimmedMessage == "":
		return "error: tool execution failed"
	case trimmedMessage == "user denied this action":
		return "User denied this action."
	case strings.HasPrefix(trimmedMessage, "Blocked in plan mode:"),
		trimmedMessage == "Shell commands blocked in plan mode",
		strings.HasPrefix(trimmedMessage, "Denied by permission rule for "),
		strings.HasPrefix(trimmedMessage, "dangerous shell command auto-denied in dontAsk mode:"),
		strings.HasPrefix(trimmedMessage, "tool ") && strings.Contains(trimmedMessage, " denied in dontAsk mode"):
		return "Action denied: " + trimmedMessage
	default:
		return "error: " + trimmedMessage
	}
}

// appendMessage 统一追加一条内部消息，并同步刷新 native provider 运行态与快照。
// 这样高频的 user / assistant / tool 追加路径不必分散手写“主链 append + 全量重投影”的重复逻辑。
func (a *Agent) ensureReadBeforeWrite(call tools.Invocation) error {
	if call.Name != "write_file" && call.Name != "edit_file" {
		return nil
	}
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()

	targetPath := strings.TrimSpace(call.Arguments["file_path"])
	if targetPath == "" {
		return nil
	}
	absolutePath, err := a.resolveToolPath(targetPath)
	if err != nil {
		return err
	}

	info, statErr := os.Stat(absolutePath)
	if statErr != nil {
		return nil
	}

	recordedTime, ok := a.readFileState[absolutePath]
	if !ok {
		if call.Name == "write_file" {
			return fmt.Errorf("You must read this file before writing. Use read_file first to see its current contents.")
		}
		return fmt.Errorf("You must read this file before editing. Use read_file first to see its current contents.")
	}

	if !info.ModTime().Equal(recordedTime) {
		if call.Name == "write_file" {
			return fmt.Errorf("Warning: %s was modified externally since your last read. Please read_file again before writing.", targetPath)
		}
		return fmt.Errorf("Warning: %s was modified externally since your last read. Please read_file again before editing.", targetPath)
	}
	return nil
}

// updateReadStateAfterTool 鍒锋柊鏂囦欢璇诲彇鐘舵€併€?
func (a *Agent) updateReadStateAfterTool(call tools.Invocation, result tools.Result) {
	if result.Error != nil {
		return
	}

	targetPath := strings.TrimSpace(call.Arguments["file_path"])
	if targetPath == "" {
		return
	}

	absolutePath, err := a.resolveToolPath(targetPath)
	if err != nil {
		return
	}

	info, statErr := os.Stat(absolutePath)
	if statErr != nil {
		return
	}

	switch call.Name {
	case "read_file", "write_file", "edit_file":
		a.stateMu.Lock()
		a.readFileState[absolutePath] = info.ModTime()
		a.stateMu.Unlock()
	}
}

// resolveToolPath 浣跨敤宸ヤ綔鐩綍瑙ｆ瀽宸ュ叿鐩爣璺緞銆?
func (a *Agent) resolveToolPath(input string) (string, error) {
	target := input
	if !filepath.IsAbs(target) {
		target = filepath.Join(a.registry.WorkingDirectory(), target)
	}
	return filepath.Abs(target)
}

func (a *Agent) budgetToolResults() bool {
	if a.effectiveWindow <= 0 {
		return false
	}
	utilization := float64(a.lastInputTokenCount) / float64(a.effectiveWindow)
	if utilization < budgetThreshold {
		return false
	}

	budget := budgetLimitLow
	if utilization > highBudgetThreshold {
		budget = budgetLimitHigh
	}

	switch a.currentProvider() {
	case "anthropic":
		return a.budgetAnthropicToolResults(budget)
	default:
		return a.budgetOpenAIToolResults(budget)
	}
}

// budgetOpenAIToolResults 以 OpenAI-compatible 的 tool role 消息视角压缩长结果。
// 这里保留 tool 消息结构本身，只对正文内容做预算截断，避免偏离当前 OpenAI 主链。
func (a *Agent) budgetOpenAIToolResults(budget int) bool {
	changed := false
	for index := range a.messages {
		message := &a.messages[index]
		if message.Role != "tool" {
			continue
		}
		if len(message.Content) <= budget {
			continue
		}

		keepEach := (budget - 80) / 2
		if keepEach <= 0 || len(message.Content) <= keepEach*2 {
			continue
		}
		message.Content = message.Content[:keepEach] +
			fmt.Sprintf("\n\n[... budgeted: %d chars truncated ...]\n\n", len(message.Content)-keepEach*2) +
			message.Content[len(message.Content)-keepEach:]
		changed = true
	}
	return changed
}

// budgetAnthropicToolResults 以 Anthropic 的 tool_result 语义视角压缩长结果。
// 当前运行态仍然存放在统一消息历史里，但这里优先只处理真正绑定到 tool_use 的结果，先把语义边界对齐。
func (a *Agent) budgetAnthropicToolResults(budget int) bool {
	changed := false
	toolIndexes := a.collectAnthropicToolResultIndexesFromSnapshot()
	if len(toolIndexes) == 0 {
		// 如果当前会话还没有可用的 Anthropic 原生快照，就退回统一消息主链过滤，
		// 避免旧会话恢复或快照缺失时，budget 阶段直接失去作用。
		toolIndexes = make([]int, 0, len(a.messages))
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
			toolIndexes = append(toolIndexes, index)
		}
	}

	for _, index := range toolIndexes {
		message := &a.messages[index]
		if len(message.Content) <= budget {
			continue
		}

		keepEach := (budget - 80) / 2
		if keepEach <= 0 || len(message.Content) <= keepEach*2 {
			continue
		}

		// 这里沿用首尾保留的裁剪策略，尽量保住 Anthropic tool_result 的上下文线索。
		message.Content = message.Content[:keepEach] +
			fmt.Sprintf("\n\n[... anthropic tool_result budgeted: %d chars truncated ...]\n\n", len(message.Content)-keepEach*2) +
			message.Content[len(message.Content)-keepEach:]
		changed = true
	}
	return changed
}

// snipCandidate 表示一个可参与 stale-result 裁剪的工具结果。

func (a *Agent) canEarlyExecuteTool(call api.ToolCall) bool {
	definition, ok := a.registry.ToolByName(call.Name)
	if !ok || definition == nil {
		return false
	}
	if !tools.IsConcurrencySafeTool(call.Name) || !definition.ReadOnly {
		return false
	}

	invocation := tools.Invocation{
		Name:      call.Name,
		Arguments: call.Arguments,
	}
	permissionDecision := tools.CheckPermission(a.options.PermissionMode, *definition, invocation, tools.PermissionContext{
		PlanFilePath: a.planFilePath,
	})
	if permissionDecision.Action != "allow" {
		return false
	}
	return true
}

// buildAssistantMessage 鎶婄粨鏋勫寲鍝嶅簲杞垚鍙寔涔呭寲 assistant 娑堟伅銆?
