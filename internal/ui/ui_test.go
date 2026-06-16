// Package ui 验证终端首屏文案与提示符输出。
// 这些测试锁住源码仓库风格的欢迎文案和 REPL 提示符，避免后续迁移把最直观的 CLI 体验改散。
package ui

import (
	"strings"
	"testing"
)

// TestPrintWelcomeMatchesPrimaryCommands 验证欢迎文案会展示源码仓库风格的定位和核心命令列表。
func TestPrintWelcomeMatchesPrimaryCommands(t *testing.T) {
	output := captureOutput(PrintWelcome)
	if !strings.Contains(output, "Mini Claude Code - A minimal coding agent") {
		t.Fatalf("PrintWelcome() missing title, got: %q", output)
	}
	if !strings.Contains(output, "Type your request, or 'exit' to quit.") {
		t.Fatalf("PrintWelcome() missing exit guidance, got: %q", output)
	}
	if !strings.Contains(output, "Commands: /clear /plan /cost /compact /memory /skills") {
		t.Fatalf("PrintWelcome() missing primary command list, got: %q", output)
	}
}

// TestPrintPromptUsesSourceStylePrefix 验证提示符使用源码仓库风格的前置空行和 `> ` 前缀。
func TestPrintPromptUsesSourceStylePrefix(t *testing.T) {
	output := captureOutput(PrintPrompt)
	if output != "\n> " {
		t.Fatalf("PrintPrompt() = %q, want %q", output, "\n> ")
	}
}

// TestPrintPlanApprovalOptionsMatchesSourceStyle 验证计划审批菜单采用与源码仓库一致的 1/2/3/4 选项文案。
// 这能锁住 REPL 中最直观的审批体验，避免后续迁移把用户可见含义又改回内部化描述。
func TestPrintPlanApprovalOptionsMatchesSourceStyle(t *testing.T) {
	output := captureOutput(PrintPlanApprovalOptions)
	if !strings.Contains(output, "Choose an option:") {
		t.Fatalf("PrintPlanApprovalOptions() missing title, got: %q", output)
	}
	if !strings.Contains(output, "1) Yes, clear context and execute - fresh start with auto-accept edits") {
		t.Fatalf("PrintPlanApprovalOptions() missing option 1, got: %q", output)
	}
	if !strings.Contains(output, "2) Yes, and execute - keep context, auto-accept edits") {
		t.Fatalf("PrintPlanApprovalOptions() missing option 2, got: %q", output)
	}
	if !strings.Contains(output, "3) Yes, manually approve edits - keep context, confirm each edit") {
		t.Fatalf("PrintPlanApprovalOptions() missing option 3, got: %q", output)
	}
	if !strings.Contains(output, "4) No, keep planning - provide feedback to revise") {
		t.Fatalf("PrintPlanApprovalOptions() missing option 4, got: %q", output)
	}
}

// TestPrintToolCallUsesReadableSummary 验证工具调用展示使用用户可读摘要，而不是直接打印底层参数 map。
// 这能锁住 REPL 中最常见的“当前在做什么”提示，避免工具调用又退回到难扫读的内部结构。
func TestPrintToolCallUsesReadableSummary(t *testing.T) {
	output := captureOutput(func() {
		PrintToolCall("grep_search", map[string]string{
			"path":    "internal",
			"pattern": "ExitPlanMode",
		})
	})
	if !strings.Contains(output, `[tool] grep_search "ExitPlanMode" in internal`) {
		t.Fatalf("PrintToolCall() should use grep summary, got: %q", output)
	}
	if strings.Contains(output, "map[") {
		t.Fatalf("PrintToolCall() should not expose raw map formatting, got: %q", output)
	}
}

// TestPrintToolCallTruncatesShellSummary 验证 shell 工具展示会保留可读摘要，并在过长时做源码仓库风格的截断。
// 这样用户既能知道正在运行什么命令，也不会被超长命令直接刷屏。
func TestPrintToolCallTruncatesShellSummary(t *testing.T) {
	longCommand := "go test ./... && " + strings.Repeat("x", 80)
	output := captureOutput(func() {
		PrintToolCall("run_shell", map[string]string{
			"command": longCommand,
		})
	})
	if !strings.Contains(output, "[tool] run_shell go test ./... && ") {
		t.Fatalf("PrintToolCall() missing shell prefix, got: %q", output)
	}
	if !strings.Contains(output, "...") {
		t.Fatalf("PrintToolCall() should truncate long shell summary, got: %q", output)
	}
}

// TestPrintPlanForApprovalMatchesSourceStyle 验证计划审批展示块采用更接近源码仓库的标题、缩进和截断提示。
// 这能锁住 plan mode 审批阶段最醒目的可视反馈，避免计划正文重新退回到难分辨的裸输出。
func TestPrintPlanForApprovalMatchesSourceStyle(t *testing.T) {
	output := captureOutput(func() {
		PrintPlanForApproval("Step 1\nStep 2")
	})
	if !strings.Contains(output, "  === Plan for Approval ===") {
		t.Fatalf("PrintPlanForApproval() missing title, got: %q", output)
	}
	if !strings.Contains(output, "  Step 1") || !strings.Contains(output, "  Step 2") {
		t.Fatalf("PrintPlanForApproval() should indent plan lines, got: %q", output)
	}
	if !strings.Contains(output, "  =========================") {
		t.Fatalf("PrintPlanForApproval() missing closing divider, got: %q", output)
	}
}

// TestPrintPlanForApprovalTruncatesLongPlans 验证超长计划会保留前 60 行并给出剩余行数提示。
// 这样 REPL 在面对很长的实现计划时仍然可读，不会被整段内容直接淹没。
func TestPrintPlanForApprovalTruncatesLongPlans(t *testing.T) {
	lines := make([]string, 0, 65)
	for index := 1; index <= 65; index++ {
		lines = append(lines, "Line "+strings.Repeat("x", 1)+string(rune('A'+(index%26))))
	}
	output := captureOutput(func() {
		PrintPlanForApproval(strings.Join(lines, "\n"))
	})
	if !strings.Contains(output, "  ... (5 more lines)") {
		t.Fatalf("PrintPlanForApproval() should report truncated lines, got: %q", output)
	}
}

// TestPrintSubAgentStartMatchesSourceStyle 验证子智能体开始提示采用更接近源码仓库的显式分隔格式。
// 这能锁住复杂任务里最常出现的协作提示，避免它又退回到难扫读的内部日志样式。
func TestPrintSubAgentStartMatchesSourceStyle(t *testing.T) {
	output := captureOutput(func() {
		PrintSubAgentStart("plan", "inspect CLI flow")
	})
	if !strings.Contains(output, "  +- Sub-agent [plan]: inspect CLI flow") {
		t.Fatalf("PrintSubAgentStart() should use source-style banner, got: %q", output)
	}
}

// TestPrintSubAgentEndMatchesSourceStyle 验证子智能体结束提示采用更接近源码仓库的完成标记格式。
// 这样用户能更快看出某个子任务已经结束，而不是再去读长句式状态文本。
func TestPrintSubAgentEndMatchesSourceStyle(t *testing.T) {
	output := captureOutput(func() {
		PrintSubAgentEnd("explore", "ignored description")
	})
	if !strings.Contains(output, "  +- Sub-agent [explore] completed") {
		t.Fatalf("PrintSubAgentEnd() should use source-style completion banner, got: %q", output)
	}
}

// TestPrintInfoMatchesSourceStyle 验证普通提示采用带前置空行和统一前缀的源码仓库风格。
// 这能锁住跨很多链路都会复用的信息提示，避免状态类输出退回成难区分的裸文本。
func TestPrintInfoMatchesSourceStyle(t *testing.T) {
	output := captureOutput(func() {
		PrintInfo("Session restored")
	})
	if output != "\n  i Session restored\n" {
		t.Fatalf("PrintInfo() = %q", output)
	}
}

// TestPrintErrorMatchesSourceStyle 验证错误提示采用更醒目的 Error 前缀和前置空行。
// 这样用户在失败场景里能更快分辨当前输出是错误，而不是普通说明文本。
func TestPrintErrorMatchesSourceStyle(t *testing.T) {
	output := captureOutput(func() {
		PrintError("permission denied")
	})
	if output != "\n  Error: permission denied\n" {
		t.Fatalf("PrintError() = %q", output)
	}
}

// TestPrintConfirmationMatchesSourceStyle 验证危险操作确认提示采用源码仓库风格的显式文案。
// 这能锁住高风险确认前最关键的可见信号，避免确认流程退回成普通信息提示。
func TestPrintConfirmationMatchesSourceStyle(t *testing.T) {
	output := captureOutput(func() {
		PrintConfirmation("git reset --hard")
	})
	if output != "\n  Dangerous command: git reset --hard\n" {
		t.Fatalf("PrintConfirmation() = %q", output)
	}
}

// TestPrintRetryMatchesSourceStyle 验证重试提示采用源码仓库风格的 Retry N/M: reason 文案。
// 这能锁住 API 暂时失败时最关键的反馈，避免用户只能看到偏底层的 attempt/after 描述。
func TestPrintRetryMatchesSourceStyle(t *testing.T) {
	output := captureOutput(func() {
		PrintRetry(2, 5, "overloaded")
	})
	if output != "\n  Retry 2/5: overloaded\n" {
		t.Fatalf("PrintRetry() = %q", output)
	}
}

// TestPrintCostMatchesSourceStyle 验证成本展示采用源码仓库风格的 Tokens ... (~$...) 主体，
// 同时保留 Go 版额外补上的预算与轮次信息，避免后续迁移时两边能力彼此覆盖。
func TestPrintCostMatchesSourceStyle(t *testing.T) {
	output := captureOutput(func() {
		PrintCost(100, 50, 1.25, 3, 10)
	})
	if output != "\n  Tokens: 100 in / 50 out (~$0.0011) / $1.2500 budget | Turns: 3/10\n" {
		t.Fatalf("PrintCost() = %q", output)
	}
}

// TestPrintDividerMatchesSourceStyle 验证分隔线带前置空行和缩进，保持与源码仓库相近的视觉节奏。
// 这能锁住多段输出之间的分隔体验，避免分隔线贴着正文出现得过于生硬。
func TestPrintDividerMatchesSourceStyle(t *testing.T) {
	output := captureOutput(PrintDivider)
	if output != "\n  --------------------------------------------------\n" {
		t.Fatalf("PrintDivider() = %q", output)
	}
}

// TestPrintToolResultUsesIndentedDisplay 验证普通工具结果使用缩进展示，而不是内部标签头。
// 这样用户在连续阅读多个工具输出时会更像在看终端结果，而不是调试日志。
func TestPrintToolResultUsesIndentedDisplay(t *testing.T) {
	output := captureOutput(func() {
		PrintToolResult("line 1\nline 2")
	})
	if !strings.Contains(output, "  line 1\n  line 2\n") {
		t.Fatalf("PrintToolResult() should indent lines, got: %q", output)
	}
	if strings.Contains(output, "[tool-result]") {
		t.Fatalf("PrintToolResult() should not print internal header, got: %q", output)
	}
}

// TestPrintToolResultForFileChangeUsesReadablePreview 验证文件写入/编辑结果会直接展示成功行和预览内容。
// 这能锁住高频文件修改反馈的可读性，避免写入结果重新退回到内部标签式输出。
func TestPrintToolResultForFileChangeUsesReadablePreview(t *testing.T) {
	output := captureOutput(func() {
		PrintToolResult("Successfully wrote to demo.txt (2 lines)\n\n   1 | hello\n   2 | world")
	})
	if !strings.Contains(output, "  Successfully wrote to demo.txt (2 lines)\n") {
		t.Fatalf("PrintToolResult() missing success line, got: %q", output)
	}
	if !strings.Contains(output, "     1 | hello\n     2 | world\n") {
		t.Fatalf("PrintToolResult() should indent file preview lines, got: %q", output)
	}
	if strings.Contains(output, "[tool-result]") {
		t.Fatalf("PrintToolResult() should not print internal header for file changes, got: %q", output)
	}
}
