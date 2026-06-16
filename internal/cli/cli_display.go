// Package cli 负责命令行参数解析和交互式 REPL。
// cli_display.go — 展示函数模块，负责格式化输出会话列表、记忆条目和技能定义。
// 已从 cli.go 拆出的函数：showSessions() / showMemories() / formatMemoriesOutput() / showSkills() / formatSkillsOutput()
package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"mini-claude-code/internal/agent"
	"mini-claude-code/internal/memory"
	"mini-claude-code/internal/skills"
	"mini-claude-code/internal/ui"
)

// showSessions 展示当前归档会话列表。
// 这里先给 REPL 一个可见入口，方便验证 session 归档已经从"只 latest"进化到"可枚举历史"。
func showSessions(runner *agent.Agent) error {
	summaries, err := runner.ListSessions()
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		ui.PrintInfo("No archived sessions found.")
		return nil
	}

	lines := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		lines = append(lines, fmt.Sprintf("%s | %s | %s | messages=%d",
			summary.ID,
			summary.StartTime.Format(time.RFC3339),
			summary.Model,
			summary.MessageCount,
		))
	}
	ui.PrintInfo(strings.Join(lines, "\n"))
	return nil
}

// showMemories 展示当前工作区已保存的 memory 文件。
// 这里把隐式存在于 prompt/recall 流程中的 memory 系统显式暴露给用户，方便检查当前持久化上下文状态。
func showMemories(runner *agent.Agent) error {
	memories, err := runner.ListMemoryEntries()
	if err != nil {
		return err
	}
	ui.PrintInfo(formatMemoriesOutput(memories))
	return nil
}

// formatMemoriesOutput 把记忆条目格式化成 REPL 可直接展示的文本。
// 单独抽出 helper 后，CLI 测试可以直接锁定 memory 看板语义，避免后续迁移又退回成只列文件名。
func formatMemoriesOutput(memories []memory.Entry) string {
	filtered := make([]memory.Entry, 0, len(memories))
	for _, item := range memories {
		if strings.EqualFold(filepath.Base(item.Path), "MEMORY.md") {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return "No memories saved yet."
	}

	lines := make([]string, 0, len(filtered)+1)
	lines = append(lines, fmt.Sprintf("%d memories:", len(filtered)))
	for _, item := range filtered {
		entryType := strings.TrimSpace(item.Type)
		if entryType == "" {
			entryType = "memory"
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(item.Path), filepath.Ext(item.Path))
		}
		description := strings.TrimSpace(item.Description)
		if description == "" {
			description = "(no description)"
		}
		lines = append(lines, fmt.Sprintf("    [%s] %s - %s", entryType, name, description))
	}
	return strings.Join(lines, "\n")
}

// showSkills 展示当前工作区可见的技能定义。
// 这样用户既能看到项目级技能，也能看到用户级技能覆盖后的最终结果，
// 同时也能像源码仓库一样快速区分"可直接/调用"和"仅自动触发"的技能。
func showSkills(runner *agent.Agent) error {
	skills, err := runner.ListSkills()
	if err != nil {
		return err
	}
	ui.PrintInfo(formatSkillsOutput(skills))
	return nil
}

// formatSkillsOutput 把技能列表格式化成 REPL 可直接展示的文本。
// 单独抽出 helper 后，CLI 测试就能直接锁定技能展示语义，避免后续迁移把这类用户可见细节悄悄改回去。
func formatSkillsOutput(discoveredSkills []skills.Skill) string {
	if len(discoveredSkills) == 0 {
		return "No skills found. Add skills to .claude/skills/<name>/SKILL.md"
	}

	lines := make([]string, 0, len(discoveredSkills)*2+1)
	lines = append(lines, fmt.Sprintf("%d skills:", len(discoveredSkills)))
	for _, skill := range discoveredSkills {
		description := strings.TrimSpace(skill.Description)
		if description == "" {
			description = "No description"
		}

		tag := skill.Name
		if skill.UserInvocable {
			tag = "/" + skill.Name
		}

		lines = append(lines, fmt.Sprintf("    %s (%s, context=%s) - %s",
			tag,
			skill.Source,
			skill.Context,
			description,
		))
		if strings.TrimSpace(skill.WhenToUse) != "" {
			lines = append(lines, "      When to use: "+strings.TrimSpace(skill.WhenToUse))
		}
	}
	return strings.Join(lines, "\n")
}