// Package ui 负责终端输出。
// 本文件集中维护欢迎文案、提示符、工具结果、重试提示和计划审批展示，
// 让 Go 版 CLI 的首屏和关键交互反馈尽量贴近源码仓库的主链体验。
package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	// spinnerMu 保护全局 spinner 状态，避免重复启动或并发停止时互相覆盖。
	spinnerMu sync.Mutex
	// spinnerStop 保存当前 spinner 的停止信号通道；为 nil 表示没有 spinner 在运行。
	spinnerStop chan struct{}
)

// PrintWelcome 输出启动欢迎信息。
// 这里对齐源码仓库的首屏文案，优先保留用户第一次进入 REPL 时最容易感知到的产品语气。
func PrintWelcome() {
	fmt.Println()
	fmt.Println("  Mini Claude Code - A minimal coding agent")
	fmt.Println()
	fmt.Println("  Type your request, or 'exit' to quit.")
	fmt.Println("  Commands: /clear /plan /cost /compact /memory /skills")
	fmt.Println()
}

// PrintPrompt 输出交互提示符。
// 这里保留源码仓库风格的前置空行，让多轮 REPL 对话在纯文本终端里更容易分辨。
func PrintPrompt() {
	fmt.Print("\n> ")
}

// PrintAssistant 输出助手答复。
func PrintAssistant(text string) {
	fmt.Println(text)
}

// StartSpinner 启动一个最小终端 spinner。
// 这里先复用标准输出做轻量动画，帮助用户在模型调用期间感知“系统仍在工作”。
func StartSpinner(label string) {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	if spinnerStop != nil {
		return
	}
	if strings.TrimSpace(label) == "" {
		label = "Thinking"
	}

	stop := make(chan struct{})
	spinnerStop = stop
	frames := []string{"|", "/", "-", "\\"}

	go func(currentLabel string, currentStop chan struct{}) {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		frameIndex := 0
		fmt.Printf("\r[%s] %s...", frames[frameIndex], currentLabel)
		for {
			select {
			case <-ticker.C:
				frameIndex = (frameIndex + 1) % len(frames)
				fmt.Printf("\r[%s] %s...", frames[frameIndex], currentLabel)
			case <-currentStop:
				fmt.Print("\r\033[K")
				return
			}
		}
	}(label, stop)
}

// StopSpinner 停止当前 spinner 并清空对应终端行。
// 这样后续 thinking、assistant 文本和工具输出不会和动画混在一起。
func StopSpinner() {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	if spinnerStop == nil {
		return
	}
	close(spinnerStop)
	spinnerStop = nil
}

// PrintToolCall 输出工具调用开始信息。
// 这里把原本直接打印参数 map 的展示，收敛成更接近源码仓库的“工具名 + 摘要”格式，
// 让用户在 REPL 中更快看懂当前 Agent 正在读哪个文件、搜什么模式、跑什么命令。
func PrintToolCall(name string, input map[string]string) {
	summary := toolCallSummary(name, input)
	if strings.TrimSpace(summary) == "" {
		fmt.Printf("[tool] %s\n", name)
		return
	}
	fmt.Printf("[tool] %s %s\n", name, summary)
}

// PrintThinking 输出仅用于展示的 thinking 文本。
func PrintThinking(text string) {
	if text == "" {
		return
	}
	fmt.Printf("[thinking]\n%s\n", text)
}

// PrintToolResult 输出工具执行结果。
// 这里把普通工具结果从“内部标签 + 原样正文”收敛成更接近源码仓库的缩进展示，
// 让用户在终端里更容易连续浏览多次工具输出，而不会被调试味很重的标签打断阅读节奏。
func PrintToolResult(text string) {
	// 对 write/edit 结果单独走更易读的 diff/预览渲染；
	// 其它结果统一做截断，避免大段输出直接淹没终端。
	if looksLikeFileChangeResult(text) {
		printFileChangeResult(text)
		return
	}

	const maxLen = 500
	display := text
	if len(display) > maxLen {
		display = display[:maxLen] + fmt.Sprintf("\n  ... (%d chars total)", len(text))
	}
	lines := strings.Split(display, "\n")
	for _, line := range lines {
		fmt.Println("  " + line)
	}
}

// toolCallSummary 把工具输入压缩成更适合终端快速浏览的摘要。
// 这里优先沿用源码仓库的展示意图：文件类工具显示目标路径，搜索类工具显示模式，
// shell 显示命令摘要，子智能体显示类型和描述，避免把底层 map 结构直接暴露给用户。
func toolCallSummary(name string, input map[string]string) string {
	switch name {
	case "read_file", "write_file", "edit_file":
		return strings.TrimSpace(input["file_path"])
	case "list_files":
		pattern := strings.TrimSpace(input["pattern"])
		if pattern != "" {
			return pattern
		}
		return strings.TrimSpace(input["path"])
	case "grep_search":
		pattern := strings.TrimSpace(input["pattern"])
		searchPath := strings.TrimSpace(input["path"])
		if pattern == "" {
			return searchPath
		}
		if searchPath == "" {
			searchPath = "."
		}
		return fmt.Sprintf("%q in %s", pattern, searchPath)
	case "run_shell":
		command := strings.TrimSpace(input["command"])
		if len(command) > 60 {
			return command[:60] + "..."
		}
		return command
	case "skill":
		return strings.TrimSpace(input["skill_name"])
	case "agent":
		agentType := strings.TrimSpace(input["type"])
		description := strings.TrimSpace(input["description"])
		if agentType == "" {
			agentType = "general"
		}
		if description == "" {
			return "[" + agentType + "]"
		}
		return fmt.Sprintf("[%s] %s", agentType, description)
	case "web_fetch":
		return strings.TrimSpace(input["url"])
	default:
		return ""
	}
}

// PrintSubAgentStart 输出子智能体开始信息。
// 这里把原先偏日志风格的 `[sub-agent:type] started` 提示改成更接近源码仓库的“开始分隔提示”，
// 让复杂任务里频繁出现的子智能体协作更容易被用户一眼识别出来。
func PrintSubAgentStart(agentType string, description string) {
	if strings.TrimSpace(agentType) == "" {
		agentType = "general"
	}
	if strings.TrimSpace(description) == "" {
		description = agentType + " task"
	}
	fmt.Printf("\n  +- Sub-agent [%s]: %s\n", agentType, description)
}

// PrintSubAgentEnd 输出子智能体结束信息。
// 结束提示和开始提示配对输出，帮助用户在终端里快速看出某个子任务已经跑完，
// 避免多个子智能体交错出现时只剩下难以扫读的完成日志。
func PrintSubAgentEnd(agentType string, description string) {
	if strings.TrimSpace(agentType) == "" {
		agentType = "general"
	}
	fmt.Printf("  +- Sub-agent [%s] completed\n", agentType)
}

// PrintPlanForApproval 输出计划审批展示块。
// 这里把原本偏“裸文本”的计划展示补成更接近源码仓库的审阅框，
// 让用户在进入审批时更容易一眼分辨“下面这一段就是待审核计划”。
func PrintPlanForApproval(planContent string) {
	fmt.Println()
	fmt.Println("  === Plan for Approval ===")
	lines := strings.Split(planContent, "\n")
	limit := 60
	if len(lines) < limit {
		limit = len(lines)
	}
	for _, line := range lines[:limit] {
		fmt.Println("  " + line)
	}
	if len(lines) > limit {
		fmt.Printf("  ... (%d more lines)\n", len(lines)-limit)
	}
	fmt.Println("  =========================")
	fmt.Println()
}

// PrintPlanApprovalOptions 输出计划审批选项说明。
// 这里把 1/2/3/4 的审批文案尽量对齐源码仓库，让用户在 Go 版 REPL 里看到的选择含义
// 与主链保持一致，而不是只剩下偏内部实现视角的简写说明。
func PrintPlanApprovalOptions() {
	fmt.Println("Choose an option:")
	fmt.Println("  1) Yes, clear context and execute - fresh start with auto-accept edits")
	fmt.Println("  2) Yes, and execute - keep context, auto-accept edits")
	fmt.Println("  3) Yes, manually approve edits - keep context, confirm each edit")
	fmt.Println("  4) No, keep planning - provide feedback to revise")
}

// PrintInfo 输出普通信息。
// 这里把原先直接裸打印的提示收敛成更接近源码仓库的“前置空行 + 统一前缀”格式，
// 让状态提醒、恢复摘要和操作提示在终端里更容易和普通正文区分开来。
func PrintInfo(text string) {
	fmt.Printf("\n  i %s\n", text)
}

// PrintError 输出错误信息。
// 错误提示和信息提示分开走统一的可见前缀，避免失败场景里只剩下一条普通文本，
// 让用户能更快识别“这是需要处理的错误”，而不是继续往下读正文。
func PrintError(text string) {
	fmt.Printf("\n  Error: %s\n", text)
}

// PrintConfirmation 输出高风险操作确认提示。
// 这里补上源码仓库同款的 “Dangerous command” 明确信号，让用户在确认前先意识到
// 这不是普通信息提示，而是一次需要明确授权的危险动作。
func PrintConfirmation(command string) {
	fmt.Printf("\n  Dangerous command: %s\n", command)
}

// PrintRetry 输出模型请求重试提示。
// 这里沿用源码仓库的“Retry N/M: reason”语义，让瞬时失败后的重试反馈更直接，
// 用户不需要再从底层日志词汇里猜当前是第几次重试、为什么重试。
func PrintRetry(attempt int, maxRetries int, reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "temporary error"
	}
	fmt.Printf("\n  Retry %d/%d: %s\n", attempt, maxRetries, reason)
}

// PrintCost 输出 token 与成本摘要。
// 这里把 `/cost` 的核心展示职责收敛到 UI 层，主体沿用源码仓库的 `Tokens ... (~$...)` 形式，
// 同时保留 Go 版已经补上的预算与轮次信息，避免这些额外上下文在继续迁移时被丢掉。
func PrintCost(inputTokens int, outputTokens int, maxCostUSD float64, turns int, maxTurns int) {
	total := (float64(inputTokens)/1_000_000)*3 + (float64(outputTokens)/1_000_000)*15
	budgetInfo := ""
	if maxCostUSD > 0 {
		budgetInfo = fmt.Sprintf(" / $%.4f budget", maxCostUSD)
	}
	turnInfo := ""
	if maxTurns > 0 {
		turnInfo = fmt.Sprintf(" | Turns: %d/%d", turns, maxTurns)
	}
	fmt.Printf("\n  Tokens: %d in / %d out (~$%.4f)%s%s\n", inputTokens, outputTokens, total, budgetInfo, turnInfo)
}

// PrintDivider 输出简单分隔线。
// 这里补上前置空行和缩进，让分隔提示在视觉上更接近源码仓库，而不是贴着上一段正文硬切开。
func PrintDivider() {
	fmt.Println()
	fmt.Println("  " + strings.Repeat("-", 50))
}

// looksLikeFileChangeResult 判断工具结果是否更像写文件或编辑文件的 diff/预览输出。
func looksLikeFileChangeResult(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.Contains(trimmed, "Successfully wrote to ") ||
		strings.Contains(trimmed, "Successfully edited ") ||
		strings.Contains(trimmed, "@@ -")
}

// printFileChangeResult 输出文件写入/编辑结果的精简预览。
// 文件类工具仍然保留首行成功提示和后续 diff/预览，但去掉内部标签头，
// 让写入/编辑反馈更接近源码仓库里“直接展示变更内容”的阅读体验。
func printFileChangeResult(text string) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return
	}

	fmt.Println("  " + lines[0])
	maxLines := 40
	remaining := lines[1:]
	if len(remaining) < maxLines {
		maxLines = len(remaining)
	}
	for _, line := range remaining[:maxLines] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Println("  " + line)
	}
	if len(remaining) > maxLines {
		fmt.Printf("  ... (%d more lines)\n", len(remaining)-maxLines)
	}
}

// captureOutput 捕获单次打印函数的标准输出，用于 UI 层回归测试。
// 这样测试可以直接锁定用户可见文案，而不需要改动业务代码去暴露额外状态。
func captureOutput(fn func()) string {
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		return ""
	}

	os.Stdout = writer
	fn()
	_ = writer.Close()
	os.Stdout = originalStdout

	var buffer bytes.Buffer
	_, _ = io.Copy(&buffer, reader)
	_ = reader.Close()
	return buffer.String()
}
