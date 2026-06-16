// Package agent 验证面向用户的状态与恢复摘要输出。
// 本文件锁定 REPL 可见的恢复提示、状态摘要和成本摘要，避免后续迁移把交互重新退回成难读的内部字段串。
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mini-claude-code/internal/api"
	"mini-claude-code/internal/config"
	"mini-claude-code/internal/session"
	"mini-claude-code/internal/tools"
)

// scriptedClient 琛ㄧず涓€涓彲鎺у埗鍥炲簲鍜?usage 鐨勬祴璇曟ā鍨嬪鎴风銆?
// 杩欓噷鐢ㄥ畠鏉ラ攣瀹?RunOnce 鐨勬枃鏈繑鍥炲拰澧為噺 token 璁＄畻锛岄伩鍏嶆祴璇曚緷璧栫湡瀹瀉PI 鎴栨洿澶氫富寰幆缁嗚妭銆?
type scriptedClient struct {
	// response 琛ㄧず鏈 `Complete` 瑕佽繑鍥炵殑鍥哄畾鍝嶅簲銆?
	response api.Response
}

// Complete 鎸夐璁捐剼鏈繑鍥炲浐瀹氬搷搴旓紝鐢ㄤ簬椹卞姩 RunOnce 娴嬭瘯璺緞銆?
func (c *scriptedClient) Complete(_ context.Context, _ []api.Message, _ []tools.Tool) (api.Response, error) {
	return c.response, nil
}

// TestFormatRestoreSummaryIncludesSessionRestored 验证恢复摘要会先给出源码仓库同款的直观恢复提示。
func TestFormatRestoreSummaryIncludesSessionRestored(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "claude-sonnet-4-6",
		RuntimeConfig: config.Config{
			AnthropicAPIKey:  "anthropic-key",
			AnthropicBaseURL: "https://anthropic.example.com",
		},
	})
	agent.messages = []api.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	}
	agent.sessionID = "session-123"
	agent.turns = 4
	agent.totalInputTokens = 12
	agent.totalOutputTokens = 34

	summary := agent.formatRestoreSummary(session.Entry{
		Model:     "claude-sonnet-4-6",
		Prompt:    "continue from here",
		Timestamp: time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC),
	}, "openai=0 anthropic_messages=3 anthropic_system=true")

	if !strings.Contains(summary, "Session restored (3 messages).") {
		t.Fatalf("formatRestoreSummary() missing restore banner: %s", summary)
	}
	if !strings.Contains(summary, "Model: claude-sonnet-4-6") {
		t.Fatalf("formatRestoreSummary() missing model line: %s", summary)
	}
	if !strings.Contains(summary, "Last prompt: continue from here") {
		t.Fatalf("formatRestoreSummary() missing prompt line: %s", summary)
	}
}

// TestFormatStatusSummaryIsReadable 验证状态摘要采用更贴近主链的人读格式，而不是暴露过多内部调试字段。
func TestFormatStatusSummaryIsReadable(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		PermissionMode:   tools.PermissionPlan,
		Resume:           true,
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})
	agent.messages = []api.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "inspect"},
	}
	agent.turns = 2
	agent.sessionID = ""
	agent.planFilePath = ""

	summary := agent.formatStatusSummary()

	if !strings.Contains(summary, "Model: gpt-4o") {
		t.Fatalf("formatStatusSummary() missing model line: %s", summary)
	}
	if !strings.Contains(summary, "Provider: openai | Mode: plan") {
		t.Fatalf("formatStatusSummary() missing provider/mode line: %s", summary)
	}
	if !strings.Contains(summary, "Session: (new session) | Resume: true") {
		t.Fatalf("formatStatusSummary() missing session fallback: %s", summary)
	}
	if !strings.Contains(summary, "Plan file: (none)") {
		t.Fatalf("formatStatusSummary() missing plan fallback: %s", summary)
	}
	if !strings.Contains(summary, "\nTokens: ") {
		t.Fatalf("formatStatusSummary() should be multiline: %s", summary)
	}
	if !strings.Contains(summary, "\nContext: ") {
		t.Fatalf("formatStatusSummary() missing context line: %s", summary)
	}
	if !strings.Contains(summary, "\nMemories: 0 surfaced / 0 bytes injected") {
		t.Fatalf("formatStatusSummary() missing memory summary: %s", summary)
	}
	if strings.Contains(summary, "Require file header:") {
		t.Fatalf("formatStatusSummary() should not expose internal comment-rule flags: %s", summary)
	}
	if strings.Contains(summary, "Context cleared:") {
		t.Fatalf("formatStatusSummary() should not expose internal context-cleared flags: %s", summary)
	}
}

// TestFormatCostSummaryIncludesBudgetAndTurns 验证成本摘要继续保留预算与轮次信息。
func TestFormatCostSummaryIncludesBudgetAndTurns(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		MaxTurns:         10,
		MaxCostUSD:       1.25,
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})
	agent.turns = 3
	agent.totalInputTokens = 100
	agent.totalOutputTokens = 50

	summary := agent.formatCostSummary()

	if !strings.Contains(summary, "Tokens: 100 in / 50 out") {
		t.Fatalf("formatCostSummary() missing token line: %s", summary)
	}
	if !strings.Contains(summary, "/ $1.2500 budget") {
		t.Fatalf("formatCostSummary() missing budget info: %s", summary)
	}
	if !strings.Contains(summary, "| Turns: 3/10") {
		t.Fatalf("formatCostSummary() missing turn info: %s", summary)
	}
}

// TestGuardTurnBudgetUsesSourceStyleTurnLimitMessage 验证进入新轮次前的轮次预算预检，
// 会返回与主链一致的 Turn limit reached 文案，而不是旧的内部错误措辞。
func TestGuardTurnBudgetUsesSourceStyleTurnLimitMessage(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		MaxTurns:         3,
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})
	agent.turns = 3

	err := agent.guardTurnBudget()
	if err == nil {
		t.Fatal("guardTurnBudget() should fail when turn budget is exhausted")
	}
	if got := err.Error(); got != "Turn limit reached (3 >= 3)" {
		t.Fatalf("guardTurnBudget() error = %q, want %q", got, "Turn limit reached (3 >= 3)")
	}
}

// TestCheckCostBudgetUsesSourceStyleMessage 验证成本预算 helper 会返回与主链一致的 Cost limit reached 文案，
// 这样预检路径和主循环中的 Budget exceeded 提示才能共用同一套原因文本。
func TestCheckCostBudgetUsesSourceStyleMessage(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		MaxCostUSD:       0.0001,
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})
	agent.totalInputTokens = 100000

	exceeded, reason := agent.checkCostBudget()
	if !exceeded {
		t.Fatal("checkCostBudget() should report exceeded budget")
	}
	if got := reason; got != "Cost limit reached ($0.3000 >= $0.0001)" {
		t.Fatalf("checkCostBudget() reason = %q, want %q", got, "Cost limit reached ($0.3000 >= $0.0001)")
	}
}

// TestEnterPlanModeMessage 验证进入 plan mode 时会返回面向用户的只读提示。
// 这能锁住当前迁移中对齐源码仓库的关键文案，避免后续把规划模式提示改回过于简略的说明。
func TestEnterPlanModeMessage(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "claude-sonnet-4-6",
		RuntimeConfig: config.Config{
			AnthropicAPIKey:  "anthropic-key",
			AnthropicBaseURL: "https://anthropic.example.com",
		},
	})

	message := agent.EnterPlanMode()
	if !strings.Contains(message, "Entered plan mode. You are now in read-only mode.") {
		t.Fatalf("EnterPlanMode() missing read-only banner: %s", message)
	}
	if !strings.Contains(message, "Your plan file: "+agent.planFilePath) {
		t.Fatalf("EnterPlanMode() missing plan file path: %s", message)
	}
	if !strings.Contains(agent.planFilePath, "plan-"+agent.sessionID+".md") {
		t.Fatalf("EnterPlanMode() should use session-scoped plan file, got: %s", agent.planFilePath)
	}
	if !strings.Contains(message, "When your plan is complete, call exit_plan_mode.") {
		t.Fatalf("EnterPlanMode() missing exit guidance: %s", message)
	}
	if !strings.Contains(agent.systemPrompt, "You MUST NOT make any edits") {
		t.Fatalf("EnterPlanMode() should inject strict plan mode guardrails, got: %s", agent.systemPrompt)
	}
	if !strings.Contains(agent.systemPrompt, "Do NOT ask the user to approve - exit_plan_mode handles that.") {
		t.Fatalf("EnterPlanMode() should inject approval guidance, got: %s", agent.systemPrompt)
	}
}

// TestExitPlanModeMessage 验证退出 plan mode 时会说明恢复到哪种权限模式。
// 这样可以保证 REPL 和工具层在退出规划态后，都向用户暴露一致的恢复结果。
func TestExitPlanModeMessage(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		PermissionMode:   tools.PermissionAcceptEdits,
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	agent.EnterPlanMode()
	message := agent.ExitPlanMode()
	if !strings.Contains(message, "Exited plan mode. Permission mode restored to: acceptEdits") {
		t.Fatalf("ExitPlanMode() missing restore message: %s", message)
	}
	if !strings.Contains(message, "## Your Plan:\n(No plan file found)") {
		t.Fatalf("ExitPlanMode() should include fallback plan body: %s", message)
	}
}

// TestExitPlanModeMessageAfterPlanFileExists 验证已有计划文件时，退出 plan mode 仍会返回源码仓库风格的恢复提示。
// 这能锁住“规划完成后退出”的用户可见文案，避免后续因为计划文件存在与否而漂回两套不同提示。
func TestExitPlanModeMessageAfterPlanFileExists(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		PermissionMode:   tools.PermissionAcceptEdits,
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	agent.EnterPlanMode()
	planPath := agent.planFilePath
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(planPath, []byte("Step 1\nStep 2"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	message := agent.ExitPlanMode()
	if !strings.Contains(message, "Exited plan mode. Permission mode restored to: acceptEdits") {
		t.Fatalf("ExitPlanMode() missing restore message after plan file exists: %s", message)
	}
	if !strings.Contains(message, "## Your Plan:\nStep 1\nStep 2") {
		t.Fatalf("ExitPlanMode() should include saved plan content: %s", message)
	}
}

// TestBuildApprovedPlanExecutionPromptMatchesSourceStyle 验证审批通过后的执行 prompt 与源码仓库风格保持一致。
// 这里锁住 clear-and-execute 与 execute 两条路径在计划文件回灌上的差异，避免后续把两条分支重新混成同一套文案。
func TestBuildApprovedPlanExecutionPromptMatchesSourceStyle(t *testing.T) {
	clearPrompt := buildApprovedPlanExecutionPrompt("C:/plans/plan.md", "Step 1", true)
	if !strings.Contains(clearPrompt, "User approved the plan. Context was cleared. Permission mode: acceptEdits") {
		t.Fatalf("clear prompt missing cleared-context header: %s", clearPrompt)
	}
	if !strings.Contains(clearPrompt, "Plan file: C:/plans/plan.md") {
		t.Fatalf("clear prompt should include plan path: %s", clearPrompt)
	}

	executePrompt := buildApprovedPlanExecutionPrompt("C:/plans/plan.md", "Step 1", false)
	if !strings.Contains(executePrompt, "User approved the plan. Permission mode: acceptEdits") {
		t.Fatalf("execute prompt missing approval header: %s", executePrompt)
	}
	if strings.Contains(executePrompt, "Plan file: C:/plans/plan.md") {
		t.Fatalf("execute prompt should not include plan path in non-clear path: %s", executePrompt)
	}
}

// TestBuildPlanFeedbackPromptMatchesSourceStyle 验证继续规划反馈 prompt 与源码仓库风格保持一致。
// 这样 keep-planning 路径回灌给模型的文本就不会继续停留在 Go 版自定义措辞上。
func TestBuildPlanFeedbackPromptMatchesSourceStyle(t *testing.T) {
	defaultPrompt := buildPlanFeedbackPrompt("")
	if !strings.Contains(defaultPrompt, "User rejected the plan and wants to keep planning.") {
		t.Fatalf("default feedback prompt missing rejection header: %s", defaultPrompt)
	}
	if !strings.Contains(defaultPrompt, "User feedback: Please revise the plan.") {
		t.Fatalf("default feedback prompt missing fallback feedback: %s", defaultPrompt)
	}

	customPrompt := buildPlanFeedbackPrompt("Add verification steps.")
	if !strings.Contains(customPrompt, "User feedback: Add verification steps.") {
		t.Fatalf("custom feedback prompt missing user feedback: %s", customPrompt)
	}
	if !strings.Contains(customPrompt, "When done, call exit_plan_mode again.") {
		t.Fatalf("custom feedback prompt missing exit guidance: %s", customPrompt)
	}
}

// TestClearSessionPrintsSourceStyleConfirmation 验证清空会话后会输出明确的用户确认提示。
// 这能锁住 `/clear` 的高频可见反馈，避免只清空内部状态却没有任何直观回显。
func TestClearSessionPrintsSourceStyleConfirmation(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	output := captureAgentOutput(func() {
		if err := agent.ClearSession(); err != nil {
			t.Fatalf("ClearSession() error = %v", err)
		}
	})
	if !strings.Contains(output, "Conversation cleared.") {
		t.Fatalf("ClearSession() should print confirmation, got: %q", output)
	}
}

// TestExecuteToolPrintsDeniedMessage 验证工具被权限系统直接拒绝时，会先输出源码仓库风格的 Denied 提示。
// 这能锁住高频权限拦截场景的用户可见反馈，避免拒绝原因只藏在工具错误结果里。
func TestExecuteToolPrintsDeniedMessage(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		PermissionMode:   tools.PermissionPlan,
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	output := captureAgentOutput(func() {
		result := agent.ExecuteTool(t.Context(), tools.Invocation{
			Name: "write_file",
			Arguments: map[string]string{
				"file_path": "blocked.txt",
				"content":   "nope",
			},
		})
		if result.Error == nil {
			t.Fatal("ExecuteTool() should deny write_file in plan mode")
		}
	})
	if !strings.Contains(output, "Denied: Blocked in plan mode: write_file") {
		t.Fatalf("ExecuteTool() should print denied message, got: %q", output)
	}
}

// TestAppendToolResultMessageFormatsPermissionDenied 验证模型发起的工具调用若被权限系统拒绝，
// 写回历史的 tool result 会使用主链同款的 Action denied 文案，而不是退化成普通 error。
func TestAppendToolResultMessageFormatsPermissionDenied(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	agent.appendToolResultMessage(api.ToolCall{
		ID:   "tool-deny",
		Name: "write_file",
		Arguments: map[string]string{
			"file_path": "blocked.txt",
		},
	}, tools.Result{Error: tools.PermissionError(tools.PermissionDecision{
		Action:  "deny",
		Message: "Blocked in plan mode: write_file",
	})})

	if len(agent.messages) != 2 {
		t.Fatalf("len(messages) = %d, want %d", len(agent.messages), 2)
	}
	if agent.messages[1].Role != "tool" {
		t.Fatalf("messages[1].Role = %q, want %q", agent.messages[1].Role, "tool")
	}
	if agent.messages[1].Content != "Action denied: Blocked in plan mode: write_file" {
		t.Fatalf("messages[1].Content = %q, want %q", agent.messages[1].Content, "Action denied: Blocked in plan mode: write_file")
	}
}

// TestAppendToolResultMessageFormatsUserDenied 验证用户拒绝危险动作确认后，
// 写回历史的 tool result 会保留主链同款的 User denied this action. 文案。
func TestAppendToolResultMessageFormatsUserDenied(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	agent.appendToolResultMessage(api.ToolCall{
		ID:   "tool-user-deny",
		Name: "run_shell",
		Arguments: map[string]string{
			"command": "dangerous",
		},
	}, tools.Result{Error: fmt.Errorf("user denied this action")})

	if len(agent.messages) != 2 {
		t.Fatalf("len(messages) = %d, want %d", len(agent.messages), 2)
	}
	if agent.messages[1].Content != "User denied this action." {
		t.Fatalf("messages[1].Content = %q, want %q", agent.messages[1].Content, "User denied this action.")
	}
}

// TestExecuteToolReusesConfirmedMessage 验证同一条危险动作确认消息在当前会话中只会询问一次，
// 后续重复出现时会直接复用确认结果，贴近主链的会话级确认白名单体验。
func TestExecuteToolReusesConfirmedMessage(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	confirmCalls := 0
	agent.SetConfirmFn(func(message string) bool {
		confirmCalls++
		return message == "write new file: repeated.txt"
	})

	firstResult := agent.ExecuteTool(t.Context(), tools.Invocation{
		Name: "write_file",
		Arguments: map[string]string{
			"file_path": "repeated.txt",
			"content":   "first",
		},
	})
	if firstResult.Error != nil {
		t.Fatalf("first ExecuteTool() error = %v", firstResult.Error)
	}

	secondResult := agent.ExecuteTool(t.Context(), tools.Invocation{
		Name: "write_file",
		Arguments: map[string]string{
			"file_path": "repeated.txt",
			"content":   "second",
		},
	})
	if secondResult.Error != nil {
		t.Fatalf("second ExecuteTool() error = %v", secondResult.Error)
	}

	if confirmCalls != 1 {
		t.Fatalf("confirmFn call count = %d, want %d", confirmCalls, 1)
	}
}

// TestClearSessionResetsConfirmedMessages 验证清空会话后会重置危险动作确认白名单，
// 避免旧会话里已经确认过的命令继续影响新会话的权限边界。
func TestClearSessionResetsConfirmedMessages(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	agent.rememberConfirmedMessage("write new file: stale.txt")
	if !agent.hasConfirmedMessage("write new file: stale.txt") {
		t.Fatal("hasConfirmedMessage() should report remembered message before clear")
	}

	if err := agent.ClearSession(); err != nil {
		t.Fatalf("ClearSession() error = %v", err)
	}
	if agent.hasConfirmedMessage("write new file: stale.txt") {
		t.Fatal("hasConfirmedMessage() should be reset after ClearSession()")
	}
}

// TestConfirmToolExecutionFallsBackToStdin 验证没有注入 confirmFn 时，
// 危险动作确认会退回到标准输入交互，而不是像旧实现那样直接保守拒绝。
func TestConfirmToolExecutionFallsBackToStdin(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	originalStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	if _, err := writer.WriteString("y\n"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	_ = writer.Close()
	os.Stdin = reader
	defer func() {
		os.Stdin = originalStdin
		_ = reader.Close()
	}()

	confirmed := agent.confirmToolExecution("write new file: fallback.txt")
	if !confirmed {
		t.Fatal("confirmToolExecution() should accept fallback stdin confirmation")
	}
}

// TestRunOnceReturnsDeltaTokensAndText 楠岃瘉 RunOnce 浼氳繑鍥炲崟娆℃墽琛岀殑鏂囨湰缁撴灉鍜?token 澧為噺銆?
// 杩欓噷鐢ㄥ彲鎺у埗鐨勫鎴风鏇胯韩鎶婇獙璇佽仛鐒﹀湪 run-once 鍏ュ彛鏈韩锛岀‘淇?sub-agent
// 鍜?skill-fork 鏀剁敤杩欐潯閾惧悗锛屼笉浼氬啀鍚勮嚜缁存姢涓嶄竴鑷寸殑 token 绱姞閫昏緫銆?
func TestRunOnceReturnsDeltaTokensAndText(t *testing.T) {
	root := t.TempDir()
	docxDir := filepath.Join(root, "docx")
	if err := os.MkdirAll(docxDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(docxDir, "2026-06-16-10-测试-RunOnce-夹具.md"), []byte("# spec"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})
	agent.modelClient = &scriptedClient{
		response: api.Response{
			Text: "subagent answer",
			Usage: api.Usage{
				PromptTokens:     11,
				CompletionTokens: 7,
				TotalTokens:      18,
			},
		},
	}

	result, err := agent.RunOnce("hello")
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Text != "subagent answer" {
		t.Fatalf("result.Text = %q, want %q", result.Text, "subagent answer")
	}
	if result.InputTokens != 11 {
		t.Fatalf("result.InputTokens = %d, want %d", result.InputTokens, 11)
	}
	if result.OutputTokens != 7 {
		t.Fatalf("result.OutputTokens = %d, want %d", result.OutputTokens, 7)
	}
	if agent.totalInputTokens != 11 {
		t.Fatalf("agent.totalInputTokens = %d, want %d", agent.totalInputTokens, 11)
	}
	if agent.totalOutputTokens != 7 {
		t.Fatalf("agent.totalOutputTokens = %d, want %d", agent.totalOutputTokens, 7)
	}
	if agent.lastResponse != "subagent answer" {
		t.Fatalf("agent.lastResponse = %q, want %q", agent.lastResponse, "subagent answer")
	}
}

// captureAgentOutput 捕获单次 Agent 用户可见输出，用于验证状态类回显文案。
func captureAgentOutput(fn func()) string {
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
