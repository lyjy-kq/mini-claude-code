// Package cli 负责命令行参数解析和交互式 REPL。
// cli_commands.go — 命令处理模块，负责解析 REPL 内置命令并转发到工具层或技能层。
// 已从 cli.go 拆出的函数：handleCommand() / runToolCommand() / runSkillCommand() / handlePlanApprovalPrompt()
package cli

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"mini-claude-code/internal/agent"
	"mini-claude-code/internal/skills"
	"mini-claude-code/internal/tools"
	"mini-claude-code/internal/ui"
)

// handleCommand 处理内置 REPL 命令，未命中时返回错误让上层继续走普通 prompt。
// 通过 switch 分发所有 /xxx 开头的命令，包括 /clear /compact /cost /status /resume /plan 等。
func handleCommand(scanner *bufio.Scanner, runner *agent.Agent, input string) error {
	switch {
	case input == "/clear":
		// 清空当前会话历史
		return runner.ClearSession()
	case input == "/compact":
		// 手动压缩会话上下文
		runner.Compact()
		return nil
	case input == "/cost":
		// 显示 token 消耗和费用
		runner.ShowCost()
		return nil
	case input == "/status":
		// 显示当前会话状态摘要
		runner.ShowStatus()
		return nil
	case input == "/resume":
		// 恢复最近一次归档会话
		summary, err := runner.ResumeLatest()
		if err != nil {
			return err
		}
		ui.PrintInfo(summary)
		return nil
	case strings.HasPrefix(input, "/resume "):
		// 根据 session ID 恢复指定归档会话
		sessionID := strings.TrimSpace(strings.TrimPrefix(input, "/resume "))
		if sessionID == "" {
			return fmt.Errorf("not a builtin command")
		}
		summary, err := runner.ResumeSessionByID(sessionID)
		if err != nil {
			return err
		}
		ui.PrintInfo(summary)
		return nil
	case input == "/sessions":
		// 展示所有归档会话列表
		return showSessions(runner)
	case input == "/memory":
		// 展示当前工作区已保存的 memory 文件
		return showMemories(runner)
	case input == "/mcp":
		// 显示 MCP 连接状态摘要
		ui.PrintInfo(runner.MCPStatusSummary())
		return nil
	case input == "/skills":
		// 展示当前工作区可见的技能定义
		return showSkills(runner)
	case input == "/plan":
		// 切换 plan mode，如果已在 plan mode 则弹出审批交互
		if runner.InPlanMode() {
			return handlePlanApprovalPrompt(scanner, runner)
		}
		ui.PrintInfo(runner.TogglePlanMode())
		return nil
	case input == "/plan approve":
		// 批准当前 plan 并仅执行
		return runner.ApprovePlan("execute")
	case input == "/plan clear":
		// 批准当前 plan 并先清除已有计划再执行
		return runner.ApprovePlan("clear-and-execute")
	case input == "/plan manual":
		// 批准当前 plan 并进入手动执行模式
		return runner.ApprovePlan("manual-execute")
	case input == "/plan keep":
		// 保留当前计划继续规划（带反馈）
		return runner.ApprovePlanWithFeedback("keep-planning", "")
	case input == "/plan exit":
		// 退出 plan mode，保留已生成但未执行的计划
		ui.PrintInfo(runner.ExitPlanMode())
		return nil
	case input == "/tools":
		// 展示可用工具列表（发送 /tools prompt 到 agent）
		return runner.HandlePrompt("/tools")
	case strings.HasPrefix(input, "/agent "):
		// 启动子智能体：/agent <type> <task>
		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(input, "/agent ")), " ", 2)
		if len(parts) < 2 {
			return fmt.Errorf("not a builtin command")
		}
		output, err := runner.RunSubAgent(parts[0], parts[1])
		if err != nil {
			return err
		}
		ui.PrintInfo(output)
		return nil
	case strings.HasPrefix(input, "/read "):
		// 读取文件内容：/read <path>
		return runToolCommand(runner, "read_file", map[string]string{
			"file_path": strings.TrimSpace(strings.TrimPrefix(input, "/read ")),
		})
	case strings.HasPrefix(input, "/ls"):
		// 列出目录文件：/ls [path] [pattern]
		parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(input, "/ls")))
		args := map[string]string{}
		if len(parts) >= 1 {
			if strings.ContainsAny(parts[0], "*?") {
				args["pattern"] = parts[0]
			} else {
				args["path"] = parts[0]
			}
		}
		if len(parts) >= 2 {
			args["pattern"] = parts[1]
		}
		return runToolCommand(runner, "list_files", args)
	case strings.HasPrefix(input, "/grep "):
		// 文本搜索：/grep <path> <pattern> [include]
		parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(input, "/grep ")))
		if len(parts) < 2 {
			return fmt.Errorf("not a builtin command")
		}
		args := map[string]string{
			"path":    parts[0],
			"pattern": parts[1],
		}
		if len(parts) >= 3 {
			args["include"] = parts[2]
		}
		return runToolCommand(runner, "grep_search", args)
	case strings.HasPrefix(input, "/fetch "):
		// 网页抓取：/fetch <url> [max_length]
		parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(input, "/fetch ")))
		if len(parts) < 1 {
			return fmt.Errorf("not a builtin command")
		}
		args := map[string]string{
			"url": parts[0],
		}
		if len(parts) >= 2 {
			args["max_length"] = parts[1]
		}
		return runToolCommand(runner, "web_fetch", args)
	case strings.HasPrefix(input, "/shell "):
		// 执行 shell 命令：/shell <command>
		return runToolCommand(runner, "run_shell", map[string]string{
			"command": strings.TrimSpace(strings.TrimPrefix(input, "/shell ")),
		})
	case strings.HasPrefix(input, "/write "):
		// 写入文件内容：/write <path> <content>
		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(input, "/write ")), " ", 2)
		if len(parts) < 2 {
			return fmt.Errorf("not a builtin command")
		}
		return runToolCommand(runner, "write_file", map[string]string{
			"file_path": parts[0],
			"content":   parts[1],
		})
	case strings.HasPrefix(input, "/"):
		// 尝试作为技能调用（/<skill-name> [args]）
		return runSkillCommand(runner, input)
	default:
		// 非内置命令，返回错误让上层继续走普通 prompt 流程
		return fmt.Errorf("not a builtin command")
	}
}

// runToolCommand 统一把 REPL 命令转换成工具调用。
// 在执行前先把工具调用显式打印出来，这样 REPL 用户可以看到"正在调用什么工具"。
func runToolCommand(runner *agent.Agent, name string, args map[string]string) error {
	ui.PrintToolCall(name, args)
	result := runner.ExecuteTool(context.Background(), tools.Invocation{
		Name:      name,
		Arguments: args,
	})
	if result.Error != nil {
		return result.Error
	}
	ui.PrintToolResult(result.Output)
	return nil
}

// runSkillCommand 处理 `/<skill-name> [args]` 形式的 REPL 技能直调。
// 这里复用已经接入 Agent 的 skill 工具执行链，让 inline/fork 技能都能按当前实现生效。
func runSkillCommand(runner *agent.Agent, input string) error {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return fmt.Errorf("not a builtin command")
	}

	commandText := strings.TrimPrefix(trimmed, "/")
	parts := strings.SplitN(commandText, " ", 2)
	skillName := strings.TrimSpace(parts[0])
	if skillName == "" {
		return fmt.Errorf("not a builtin command")
	}

	// 从 agent 获取已发现的技能列表，查找匹配的可调用技能
	discoveredSkills, err := runner.ListSkills()
	if err != nil {
		return err
	}

	var matchedSkill *skills.Skill
	for _, skill := range discoveredSkills {
		if skill.Name != skillName {
			continue
		}
		copySkill := skill
		matchedSkill = &copySkill
		break
	}
	if matchedSkill == nil || !matchedSkill.UserInvocable {
		return fmt.Errorf("not a builtin command")
	}

	args := ""
	if len(parts) == 2 {
		args = strings.TrimSpace(parts[1])
	}

	ui.PrintInfo("Invoking skill: " + matchedSkill.Name)
	if matchedSkill.Context == skills.ContextFork {
		// fork 技能：启动子对话执行
		return runToolCommand(runner, "skill", map[string]string{
			"skill_name": matchedSkill.Name,
			"args":       args,
		})
	}

	// inline 技能：在当前对话中解析并执行
	resolvedPrompt, err := runner.ResolveSkillPrompt(matchedSkill.Name, args)
	if err != nil {
		return err
	}
	return runner.HandlePrompt(resolvedPrompt)
}

// handlePlanApprovalPrompt 在 plan mode 下展示计划并等待用户选择审批路径。
// 这里把原本分散的 /plan approve|clear|manual|keep 入口收束成一次交互流程，
// 让 Go 版更接近源码仓库"先看计划，再输入 1/2/3/4"的 REPL 体验。
func handlePlanApprovalPrompt(scanner *bufio.Scanner, runner *agent.Agent) error {
	planContent, err := runner.CurrentPlanContent()
	if err != nil {
		return err
	}

	// 展示当前计划和审批选项
	ui.PrintPlanForApproval(planContent)
	ui.PrintPlanApprovalOptions()

	for {
		fmt.Print("Enter choice (1-4): ")
		if !scanner.Scan() {
			return scanner.Err()
		}

		switch strings.TrimSpace(scanner.Text()) {
		case "1":
			// 清除并执行
			return runner.ApprovePlan("clear-and-execute")
		case "2":
			// 仅执行
			return runner.ApprovePlan("execute")
		case "3":
			// 手动执行
			return runner.ApprovePlan("manual-execute")
		case "4":
			// 提供反馈继续规划
			fmt.Print("Feedback (what to change): ")
			if !scanner.Scan() {
				return scanner.Err()
			}
			return runner.ApprovePlanWithFeedback("keep-planning", strings.TrimSpace(scanner.Text()))
		default:
			ui.PrintError("invalid plan choice, enter 1, 2, 3, or 4")
		}
	}
}
