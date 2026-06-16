// Package prompt 负责构造系统提示词。
// 本次修改继续向源仓库对齐，补上 `@include` 解析、rules 聚合、memory 提示段和 deferred tools 注入，
// 让 Go 版的上下文装配不再只停留在“能跑”的阶段，而是更接近真实使用时的提示词结构。
package prompt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"mini-claude-code/internal/memory"
	"mini-claude-code/internal/skills"
	"mini-claude-code/internal/subagent"
	"mini-claude-code/internal/tools"
)

const maxIncludeDepth = 5

// Build 根据工作目录、权限模式和工具注册表组装系统提示词。
func Build(workingDirectory string, permissionMode tools.PermissionMode, registry *tools.Registry) string {
	lines := []string{
		"You are a teaching-oriented coding agent implemented in Go.",
		fmt.Sprintf("Working directory: %s", workingDirectory),
		fmt.Sprintf("Current date: %s", time.Now().Format("2006-01-02")),
		fmt.Sprintf("Platform: %s/%s", runtime.GOOS, runtime.GOARCH),
		fmt.Sprintf("Shell: %s", defaultShell()),
		fmt.Sprintf("Permission mode: %s", permissionMode),
		"Read context before deciding to use tools.",
		"Keep code comments complete and clear when editing code.",
		"",
		"# Tool Response Protocol",
		"Prefer the backend's native tool-calling protocol when tools are available.",
		`If native tool calling is unavailable, fall back to this JSON shape: {"assistant_text":"optional user-facing text","tool_calls":[{"name":"tool_name","arguments":{"key":"value"}}]}`,
		"If no tool is needed, return normal plain text.",
		"After receiving tool results, continue the task and decide whether more tools are needed.",
	}

	if claudeSection := loadClaudeSection(workingDirectory); claudeSection != "" {
		lines = append(lines, "", "# Project Instructions", claudeSection)
	}
	if gitSection := loadGitContext(workingDirectory); gitSection != "" {
		lines = append(lines, "", "# Git Context", gitSection)
	}
	if memorySection := buildMemoryPromptSection(workingDirectory); memorySection != "" {
		lines = append(lines, "", memorySection)
	}
	if skillSection := skills.NewStore(workingDirectory).BuildPromptSection(); skillSection != "" {
		lines = append(lines, "", skillSection)
	}
	if agentSection := subagent.NewStore(workingDirectory).BuildPromptSection(); agentSection != "" {
		lines = append(lines, "", agentSection)
	}
	if toolsSection := buildToolsPromptSection(registry); toolsSection != "" {
		lines = append(lines, "", toolsSection)
	}
	if deferredSection := buildDeferredToolsSection(registry); deferredSection != "" {
		lines = append(lines, "", deferredSection)
	}

	return strings.Join(lines, "\n")
}

// loadClaudeSection 递归向上查找 CLAUDE.md，并拼接当前项目的规则文件内容。
func loadClaudeSection(workingDirectory string) string {
	parts := make([]string, 0, 4)
	current := workingDirectory

	for {
		target := filepath.Join(current, "CLAUDE.md")
		if data, err := os.ReadFile(target); err == nil {
			content := resolveIncludes(string(data), current, map[string]struct{}{}, 0)
			parts = append([]string{strings.TrimSpace(content)}, parts...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	rulesDir := filepath.Join(workingDirectory, ".claude", "rules")
	if entries, err := os.ReadDir(rulesDir); err == nil {
		for _, item := range entries {
			if item.IsDir() || !strings.HasSuffix(item.Name(), ".md") {
				continue
			}
			if data, readErr := os.ReadFile(filepath.Join(rulesDir, item.Name())); readErr == nil {
				content := resolveIncludes(string(data), rulesDir, map[string]struct{}{}, 0)
				parts = append(parts, fmt.Sprintf("<!-- rule: %s -->\n%s", item.Name(), strings.TrimSpace(content)))
			}
		}
	}

	return strings.TrimSpace(strings.Join(parts, "\n\n---\n\n"))
}

// resolveIncludes 递归解析 `@./path`、`@~/path`、`@/abs/path` 形式的 include 指令。
// 这里限制最大深度并记录 visited，避免循环引用把系统提示词展开到不可控大小。
func resolveIncludes(content string, basePath string, visited map[string]struct{}, depth int) string {
	if depth >= maxIncludeDepth {
		return content
	}

	lines := strings.Split(content, "\n")
	resolvedLines := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "@") {
			resolvedLines = append(resolvedLines, line)
			continue
		}

		includePath := strings.TrimPrefix(trimmed, "@")
		target := resolveIncludePath(includePath, basePath)
		if strings.TrimSpace(target) == "" {
			resolvedLines = append(resolvedLines, fmt.Sprintf("<!-- invalid include: %s -->", includePath))
			continue
		}

		absoluteTarget, err := filepath.Abs(target)
		if err != nil {
			resolvedLines = append(resolvedLines, fmt.Sprintf("<!-- invalid include: %s -->", includePath))
			continue
		}
		if _, seen := visited[absoluteTarget]; seen {
			resolvedLines = append(resolvedLines, fmt.Sprintf("<!-- circular include: %s -->", includePath))
			continue
		}

		data, readErr := os.ReadFile(absoluteTarget)
		if readErr != nil {
			resolvedLines = append(resolvedLines, fmt.Sprintf("<!-- include not found: %s -->", includePath))
			continue
		}

		nextVisited := cloneVisited(visited)
		nextVisited[absoluteTarget] = struct{}{}
		resolvedContent := resolveIncludes(string(data), filepath.Dir(absoluteTarget), nextVisited, depth+1)
		resolvedLines = append(resolvedLines, resolvedContent)
	}

	return strings.Join(resolvedLines, "\n")
}

// resolveIncludePath 根据 include 语法解析目标路径。
func resolveIncludePath(rawPath string, basePath string) string {
	if strings.HasPrefix(rawPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, strings.TrimPrefix(rawPath, "~/"))
	}
	if filepath.IsAbs(rawPath) {
		return rawPath
	}
	if strings.HasPrefix(rawPath, "./") {
		return filepath.Join(basePath, strings.TrimPrefix(rawPath, "./"))
	}
	return ""
}

// cloneVisited 复制 include visited 集合，避免兄弟分支互相污染。
func cloneVisited(input map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(input))
	for key := range input {
		result[key] = struct{}{}
	}
	return result
}

// loadGitContext 读取当前仓库的分支、最近提交和工作区状态。
func loadGitContext(workingDirectory string) string {
	branch := runGit(workingDirectory, "rev-parse", "--abbrev-ref", "HEAD")
	log := runGit(workingDirectory, "log", "--oneline", "-5")
	status := runGit(workingDirectory, "status", "--short")

	lines := make([]string, 0, 3)
	if branch != "" {
		lines = append(lines, "branch: "+branch)
	}
	if log != "" {
		lines = append(lines, "recent commits:\n"+log)
	}
	if status != "" {
		lines = append(lines, "status:\n"+status)
	}
	return strings.Join(lines, "\n")
}

// runGit 使用 git 子命令读取当前仓库上下文，失败时返回空字符串。
func runGit(workingDirectory string, args ...string) string {
	command := exec.Command("git", args...)
	command.Dir = workingDirectory
	output, err := command.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// buildMemoryPromptSection 把本地 memory 系统说明注入到系统提示词。
// 这里继续向源仓库对齐，不再只暴露索引，而是把用途、保存规范和当前索引一起告诉模型。
func buildMemoryPromptSection(workingDirectory string) string {
	store := memory.NewStore(workingDirectory)
	memoryDir := store.MemoryDir()
	indexText, _ := store.LoadIndex()

	lines := []string{
		"# Memory System",
		"",
		fmt.Sprintf("You have a persistent, file-based memory system at `%s`.", memoryDir),
		"",
		"## Memory Types",
		"- `user`: User's role, preferences, and knowledge level",
		"- `feedback`: Corrections and guidance from the user",
		"- `project`: Ongoing work, goals, deadlines, and decisions",
		"- `reference`: External resources, dashboards, and tool entry points",
		"",
		"## How to Save Memories",
		"Use the `write_file` tool to create a memory file with YAML frontmatter:",
		"",
		"```markdown",
		"---",
		"name: memory name",
		"description: one-line description",
		"type: user|feedback|project|reference",
		"---",
		"Memory content here.",
		"```",
		"",
		fmt.Sprintf("Save to: `%s/`", memoryDir),
		"Filename format: `{type}_{slugified_name}.md`",
		"The MEMORY.md index is auto-updated when you write to the memory directory - do NOT update it manually.",
		"",
		"## What Not to Save",
		"- Code patterns or architecture that can be re-read from the codebase",
		"- Git history that should come from git commands",
		"- Anything already captured in project instruction files",
		"- Ephemeral task details that are not worth persisting",
		"",
		"## When to Recall",
		"Recall memories when the user asks to remember or recall, or when earlier context seems directly relevant.",
	}
	if indexText != "" {
		lines = append(lines, "", "## Current Memory Index", indexText)
	} else {
		lines = append(lines, "", "(No memories saved yet.)")
	}
	return strings.Join(lines, "\n")
}

// buildToolsPromptSection 把当前已公开工具定义注入到系统提示词。
// 当前 Go 版仍依赖提示词来告诉模型工具能力，因此这个段落是实际可用性的关键组成部分。
func buildToolsPromptSection(registry *tools.Registry) string {
	if registry == nil {
		return ""
	}

	definitions := registry.ActiveDefinitions()
	if len(definitions) == 0 {
		return ""
	}

	lines := []string{"# Available Tools", ""}
	for _, tool := range definitions {
		lines = append(lines, "- "+formatToolForPrompt(tool))
	}
	return strings.Join(lines, "\n")
}

// buildDeferredToolsSection 把延迟工具名称注入到提示词，提醒模型先用 tool_search 再请求完整 schema。
func buildDeferredToolsSection(registry *tools.Registry) string {
	if registry == nil {
		return ""
	}

	names := registry.DeferredToolNames()
	if len(names) == 0 {
		return ""
	}
	return "Deferred tools available via tool_search: " + strings.Join(names, ", ") + ". Use tool_search to fetch their full schemas when needed."
}

// formatToolForPrompt 把工具名称、说明和输入结构压缩成一行提示词描述。
// 这里先用文本方式把 schema 暴露给模型，为后续迁移到原生 function calling 提前对齐元数据层。
func formatToolForPrompt(tool tools.Tool) string {
	parts := []string{tool.Name + ": " + strings.TrimSpace(tool.Description)}

	properties, _ := tool.InputSchema["properties"].(map[string]any)
	requiredNames, _ := tool.InputSchema["required"].([]string)
	if len(properties) == 0 {
		return strings.Join(parts, " ")
	}

	required := map[string]bool{}
	for _, name := range requiredNames {
		required[name] = true
	}

	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	argParts := make([]string, 0, len(keys))
	for _, key := range keys {
		field, _ := properties[key].(map[string]any)
		description, _ := field["description"].(string)
		requiredMark := "optional"
		if required[key] {
			requiredMark = "required"
		}
		argParts = append(argParts, fmt.Sprintf("%s (%s): %s", key, requiredMark, description))
	}
	parts = append(parts, "Arguments: "+strings.Join(argParts, "; "))
	return strings.Join(parts, " ")
}

// defaultShell 返回当前平台最常见的 shell 名称，便于让系统提示词更接近真实环境。
func defaultShell() string {
	if runtime.GOOS == "windows" {
		if value := os.Getenv("ComSpec"); strings.TrimSpace(value) != "" {
			return value
		}
		return "powershell"
	}
	if value := os.Getenv("SHELL"); strings.TrimSpace(value) != "" {
		return value
	}
	return "/bin/sh"
}
