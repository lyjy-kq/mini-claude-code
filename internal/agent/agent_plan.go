// agent_plan.go 负责规划模式管理，包括计划模式进入/退出、计划审批、执行等。
// 该文件从 agent.go 拆分而来。
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mini-claude-code/internal/api"
	"mini-claude-code/internal/tools"
	"mini-claude-code/internal/prompt"
	"mini-claude-code/internal/ui"
)

// TogglePlanMode 切换是否处于只读规划模式。
func (a *Agent) TogglePlanMode() string {
	if a.options.PermissionMode == tools.PermissionPlan {
		return a.ExitPlanMode()
	}
	return a.EnterPlanMode()
}

// InPlanMode 返回当前 Agent 是否处于只读规划模式。
func (a *Agent) InPlanMode() bool {
	return a.options.PermissionMode == tools.PermissionPlan
}

// PlanCommandHelp 返回当前规划模式下可用的审批指令说明。
func (a *Agent) PlanCommandHelp() string {
	if !a.InPlanMode() {
		return "Plan mode is inactive. Use /plan to enter plan mode."
	}
	return "Plan mode is active. Use /plan approve to execute, /plan clear to clear context and execute, or /plan manual to exit without automatic execution."
}

// EnterPlanMode 进入只读规划模式。
func (a *Agent) EnterPlanMode() string {
	if a.options.PermissionMode == tools.PermissionPlan {
		return "Plan mode already active."
	}

	a.prePlanMode = a.options.PermissionMode
	a.options.PermissionMode = tools.PermissionPlan
	a.planFilePath = generatePlanFilePath(a.registry.WorkingDirectory(), a.sessionID)
	a.baseSystemPrompt = prompt.Build(a.registry.WorkingDirectory(), a.options.PermissionMode, a.registry)
	a.systemPrompt = a.baseSystemPrompt + buildPlanModePrompt(a.planFilePath)
	a.messages = []api.Message{{Role: "system", Content: a.systemPrompt}}
	a.readFileState = map[string]time.Time{}
	a.contextCleared = false
	a.resetMemoryRecallState()
	a.refreshProviderSnapshot()

	return fmt.Sprintf(
		"Entered plan mode. You are now in read-only mode.\n\nYour plan file: %s\nWrite your plan to this file. This is the only file you can edit.\n\nWhen your plan is complete, call exit_plan_mode.",
		a.planFilePath,
	)
}

// ApprovePlan 批准当前计划，并根据选择决定是否自动执行、是否清空上下文或继续规划。
func (a *Agent) ApprovePlan(choice string) error {
	if !a.InPlanMode() {
		return fmt.Errorf("plan mode is not active")
	}

	normalizedChoice := strings.ToLower(strings.TrimSpace(choice))
	planContent, err := a.readCurrentPlan()
	if err != nil {
		return err
	}
	savedPlanPath := a.planFilePath

	switch normalizedChoice {
	case "manual", "manual-execute":
		ui.PrintPlanForApproval(planContent)
		ui.PrintPlanApprovalOptions()
		exitMessage := a.ExitPlanMode()
		ui.PrintInfo(exitMessage)
		ui.PrintInfo(fmt.Sprintf("Plan preserved for manual execution.\n\nPlan file: %s\n\n## Your Plan:\n%s", savedPlanPath, planContent))
		return nil
	case "approve", "execute":
		ui.PrintPlanForApproval(planContent)
		ui.PrintPlanApprovalOptions()
		return a.approvePlanForExecution(savedPlanPath, planContent, false)
	case "clear", "clear-and-execute":
		ui.PrintPlanForApproval(planContent)
		ui.PrintPlanApprovalOptions()
		return a.approvePlanForExecution(savedPlanPath, planContent, true)
	case "keep", "keep-planning":
		ui.PrintPlanForApproval(planContent)
		ui.PrintPlanApprovalOptions()
		return a.keepPlanning("")
	default:
		return fmt.Errorf("unknown plan approval choice: %s", choice)
	}
}

// ApprovePlanWithFeedback 批准当前计划审阅选择，并允许附带驳回反馈继续规划。
func (a *Agent) ApprovePlanWithFeedback(choice string, feedback string) error {
	normalizedChoice := strings.ToLower(strings.TrimSpace(choice))
	if normalizedChoice != "keep-planning" && normalizedChoice != "keep" {
		return a.ApprovePlan(choice)
	}

	planContent, err := a.readCurrentPlan()
	if err != nil {
		return err
	}
	ui.PrintPlanForApproval(planContent)
	ui.PrintPlanApprovalOptions()
	return a.keepPlanning(feedback)
}

// ExitPlanMode 退出规划模式并恢复权限模式。
func (a *Agent) ExitPlanMode() string {
	if a.options.PermissionMode != tools.PermissionPlan {
		return "Plan mode is not active."
	}

	planContent := "(No plan file found)"
	if strings.TrimSpace(a.planFilePath) != "" {
		if currentPlan, err := a.readCurrentPlan(); err == nil && strings.TrimSpace(currentPlan) != "" {
			planContent = currentPlan
		}
	}

	restoreMode := a.prePlanMode
	if restoreMode == "" {
		restoreMode = tools.PermissionDefault
	}

	a.options.PermissionMode = restoreMode
	a.prePlanMode = ""
	a.planFilePath = ""
	a.baseSystemPrompt = prompt.Build(a.registry.WorkingDirectory(), a.options.PermissionMode, a.registry)
	a.systemPrompt = a.baseSystemPrompt
	a.messages = []api.Message{{Role: "system", Content: a.systemPrompt}}
	a.readFileState = map[string]time.Time{}
	a.contextCleared = false
	a.resetMemoryRecallState()
	a.refreshProviderSnapshot()

	return fmt.Sprintf(
		"Exited plan mode. Permission mode restored to: %s\n\n## Your Plan:\n%s",
		a.options.PermissionMode,
		planContent,
	)
}

// readCurrentPlan 读取当前计划文件内容。
func (a *Agent) readCurrentPlan() (string, error) {
	if strings.TrimSpace(a.planFilePath) == "" {
		return "", fmt.Errorf("plan file path is empty")
	}

	absolutePath, err := filepath.Abs(a.planFilePath)
	if err != nil {
		return "", err
	}
	bytes, err := os.ReadFile(absolutePath)
	if err != nil {
		return "", err
	}
	planContent := strings.TrimSpace(string(bytes))
	if planContent == "" {
		return "(No plan file found)", nil
	}
	return planContent, nil
}

// approvePlanForExecution 把审批通过后的计划转换成一次真正的执行入口。
func (a *Agent) approvePlanForExecution(planPath string, planContent string, clearContext bool) error {
	a.options.PermissionMode = tools.PermissionAcceptEdits
	a.prePlanMode = ""
	a.planFilePath = planPath
	a.baseSystemPrompt = prompt.Build(a.registry.WorkingDirectory(), a.options.PermissionMode, a.registry)
	a.systemPrompt = a.baseSystemPrompt + buildApprovedPlanExecutionPrompt(planPath, planContent, clearContext)

	if clearContext {
		a.clearHistoryKeepSystem()
	} else {
		a.messages = []api.Message{{Role: "system", Content: a.systemPrompt}}
	}
	a.readFileState = map[string]time.Time{}
	a.contextCleared = clearContext
	a.resetMemoryRecallState()
	a.refreshProviderSnapshot()

	return a.HandlePrompt(buildApprovedPlanExecutionPrompt(planPath, planContent, clearContext))
}

// keepPlanning 把用户对当前计划的反馈回灌给模型，并保持在 plan mode 内继续迭代。
func (a *Agent) keepPlanning(feedback string) error {
	return a.HandlePrompt(buildPlanFeedbackPrompt(feedback))
}

// generatePlanFilePath 生成当前会话专属的计划文件路径。
func generatePlanFilePath(workingDirectory string, sessionID string) string {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		trimmedSessionID = "unnamed"
	}
	planDir := filepath.Join(workingDirectory, ".mini-claude", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return filepath.Join(workingDirectory, "plan-"+trimmedSessionID+".md")
	}
	return filepath.Join(planDir, "plan-"+trimmedSessionID+".md")
}

// buildPlanModePrompt 构造 plan mode 额外提示。
func buildPlanModePrompt(planFilePath string) string {
	return "\n\n# Plan Mode Active\n" +
		"Plan mode is active. You MUST NOT make any edits (except the plan file below), run non-readonly tools, or make any changes to the system.\n\n" +
		fmt.Sprintf("## Plan File: %s\n", planFilePath) +
		"Write your plan incrementally to this file using write_file or edit_file. This is the ONLY file you are allowed to edit.\n\n" +
		"## Workflow\n" +
		"1. Explore: Read code to understand the task. Use read_file, list_files, grep_search.\n" +
		"2. Design: Design your implementation approach. Use the agent tool with type=\"plan\" if the task is complex.\n" +
		"3. Plan: Write your detailed plan to the plan file.\n" +
		"4. Review: Write your detailed plan to the plan file.\n" +
		"5. Finalize: When your plan is complete, call exit_plan_mode to exit plan mode.\n" +
		"Do NOT ask the user to approve - exit_plan_mode handles that.\n"
}

// buildApprovedPlanExecutionPrompt 构造批准计划后的执行系统提示。
func buildApprovedPlanExecutionPrompt(planPath string, planContent string, clearContext bool) string {
	if clearContext {
		return fmt.Sprintf(
			"User approved the plan. Context was cleared. Permission mode: acceptEdits\n\nPlan file: %s\n\n## Plan:\n%s\n\nExecute the plan step by step.",
			planPath,
			planContent,
		)
	}
	return fmt.Sprintf(
		"User approved the plan. Permission mode: acceptEdits\n\n## Plan:\n%s\n\nExecute the plan step by step.",
		planContent,
	)
}
// buildPlanFeedbackPrompt 构造用户否决计划后的反馈提示。
func buildPlanFeedbackPrompt(feedback string) string {
	trimmedFeedback := strings.TrimSpace(feedback)
	if trimmedFeedback == "" {
		return "User rejected the plan and wants to keep planning.\n\nUser feedback: Please revise the plan.\n\nPlease revise your plan based on this feedback. When done, call exit_plan_mode again."
	}
	return "User rejected the plan and wants to keep planning.\n\n" +
		"User feedback: " + trimmedFeedback + "\n\n" +
		"Please revise your plan based on this feedback. When done, call exit_plan_mode again."
}

// clearHistoryKeepSystem 清空当前会话历史，但保留最新的 system prompt。
func (a *Agent) clearHistoryKeepSystem() {
	a.messages = []api.Message{{Role: "system", Content: a.systemPrompt}}
	a.lastInputTokenCount = 0
	a.lastAPICallTime = time.Time{}
}
