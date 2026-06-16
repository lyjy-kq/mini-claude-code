// Package tools 中的 registry 负责集中管理工具定义和本地执行逻辑。
// 本文件持续向源仓库能力对齐，重点覆盖工具注册、延迟工具激活、Schema 构造以及 MCP 工具整合。
package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"mini-claude-code/internal/mcp"
	"mini-claude-code/internal/skills"
)

const (
	// maxListFilesResults 表示 list_files 单次返回的最大结果数。
	maxListFilesResults = 200
	// maxGrepSearchResults 表示 grep_search 单次返回的最大匹配行数。
	maxGrepSearchResults = 200
	// maxResultChars 表示单次工具返回的最大字符数，超长结果将被截断。
	maxResultChars = 50000
	// defaultShellTimeout 表示 run_shell 的默认超时（毫秒）。
	defaultShellTimeout = 30000
	// defaultFetchTimeout 表示 web_fetch 的默认超时（毫秒）。
	defaultFetchTimeout = 30000
	// defaultFetchMaxChars 表示 web_fetch 默认返回内容的最大字符数。
	defaultFetchMaxChars = 50000
)

// dangerousShellPatterns 表示内置的危险命令正则模式列表，用于 run_shell 前置拦截。
var dangerousShellPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s`),
	regexp.MustCompile(`(?i)\bgit\s+(push|reset|clean|checkout\s+\.)`),
	regexp.MustCompile(`(?i)\bsudo\b`),
	regexp.MustCompile(`(?i)\bmkfs\b`),
	regexp.MustCompile(`(?i)\bdd\s`),
	regexp.MustCompile(`(?i)>\s*/dev/`),
	regexp.MustCompile(`(?i)\bkill\b`),
	regexp.MustCompile(`(?i)\bpkill\b`),
	regexp.MustCompile(`(?i)\breboot\b`),
	regexp.MustCompile(`(?i)\bshutdown\b`),
	regexp.MustCompile(`(?i)\bdel\s`),
	regexp.MustCompile(`(?i)\brmdir\s`),
	regexp.MustCompile(`(?i)\bformat\s`),
	regexp.MustCompile(`(?i)\btaskkill\s`),
	regexp.MustCompile(`(?i)\bRemove-Item\s`),
	regexp.MustCompile(`(?i)\bStop-Process\s`),
}

// Registry 表示一个最小可用的工具注册表。
type Registry struct {
	// workingDirectory 表示工具默认运行目录。
	workingDirectory string
	// skillStore 表示技能存储器，用于 skill 工具解析。
	skillStore *skills.Store
	// definitions 保存当前立即公开的工具定义。
	definitions []Tool
	// deferredDefinitions 保存延迟公开的工具定义。
	deferredDefinitions []Tool
	// activatedDeferred 保存已经通过 tool_search 激活的延迟工具。
	activatedDeferred map[string]bool
	// mcpManager 保存当前注册表绑定的 MCP 管理器，用于把远端 MCP 工具并入统一工具集合。
	mcpManager *mcp.Manager
}

// WorkingDirectory 返回当前工具注册表绑定的工作目录。
func (r *Registry) WorkingDirectory() string {
	return r.workingDirectory
}

// NewRegistry 创建一个带默认工具集的注册表。
func NewRegistry(workingDirectory string, mcpManager *mcp.Manager) *Registry {
	return &Registry{
		workingDirectory:  workingDirectory,
		skillStore:        skills.NewStore(workingDirectory),
		activatedDeferred: map[string]bool{},
		mcpManager:        mcpManager,
		definitions: []Tool{
			{
				Name:        "read_file",
				Description: "Read a file and return its content with line numbers.",
				InputSchema: requiredSchema(map[string]any{
					"file_path": schemaString("Path to the file to read."),
				}, "file_path"),
				ReadOnly: true,
			},
			{
				Name:        "write_file",
				Description: "Write content to a file and return a preview.",
				InputSchema: requiredSchema(map[string]any{
					"file_path": schemaString("Path to the file to write."),
					"content":   schemaString("Full file content to write."),
				}, "file_path", "content"),
				ReadOnly: false,
			},
			{
				Name:        "edit_file",
				Description: "Replace an exact string in a file and return a diff.",
				InputSchema: requiredSchema(map[string]any{
					"file_path":  schemaString("Path to the file to edit."),
					"old_string": schemaString("Exact string to find in the file."),
					"new_string": schemaString("Replacement string."),
				}, "file_path", "old_string", "new_string"),
				ReadOnly: false,
			},
			{
				Name:        "list_files",
				Description: "List files that match a glob pattern.",
				InputSchema: optionalSchema(map[string]any{
					"path":    schemaString("Base directory to search from."),
					"pattern": schemaString("Glob pattern to match, such as **/*.go."),
				}),
				ReadOnly: true,
			},
			{
				Name:        "grep_search",
				Description: "Search file content using a regex pattern.",
				InputSchema: requiredSchema(map[string]any{
					"path":    schemaString("Directory or file to search in."),
					"pattern": schemaString("Regular expression pattern."),
					"include": schemaString("Optional glob filter for file names."),
				}, "pattern"),
				ReadOnly: true,
			},
			{
				Name:        "run_shell",
				Description: "Run a shell command and return the output.",
				InputSchema: requiredSchema(map[string]any{
					"command": schemaString("Shell command to execute."),
					"timeout": schemaString("Optional timeout in milliseconds."),
				}, "command"),
				ReadOnly: false,
			},
			{
				Name:        "skill",
				Description: "Resolve a registered skill prompt by name.",
				InputSchema: requiredSchema(map[string]any{
					"skill_name": schemaString("Skill name to resolve."),
					"args":       schemaString("Optional arguments for the skill."),
				}, "skill_name"),
				ReadOnly: false,
			},
			{
				Name:        "web_fetch",
				Description: "Fetch a URL and return the response body as plain text.",
				InputSchema: requiredSchema(map[string]any{
					"url":        schemaString("URL to fetch."),
					"max_length": schemaString("Optional max response length in characters."),
				}, "url"),
				ReadOnly: true,
			},
			{
				Name:        "agent",
				Description: "Launch a sub-agent for an isolated task. Supported types are explore, plan, and general.",
				InputSchema: requiredSchema(map[string]any{
					"description": schemaString("Short task title for the sub-agent."),
					"prompt":      schemaString("Detailed task instructions for the sub-agent."),
					"type":        schemaEnum("Sub-agent type.", "explore", "plan", "general"),
				}, "description", "prompt"),
				ReadOnly: false,
			},
		},
		deferredDefinitions: []Tool{
			{
				Name:        "enter_plan_mode",
				Description: "Enter a read-only planning phase.",
				InputSchema: optionalSchema(map[string]any{}),
				ReadOnly:    true,
				Deferred:    true,
			},
			{
				Name:        "exit_plan_mode",
				Description: "Exit plan mode after writing the plan file.",
				InputSchema: optionalSchema(map[string]any{}),
				ReadOnly:    true,
				Deferred:    true,
			},
			{
				Name:        "tool_search",
				Description: "List delayed tools by keyword.",
				InputSchema: requiredSchema(map[string]any{
					"query": schemaString("Tool name or keyword to search for."),
				}, "query"),
				ReadOnly: true,
				Deferred: true,
			},
		},
	}
}

// Definitions 返回当前立即公开的工具定义。
func (r *Registry) Definitions() []Tool {
	return r.definitions
}

// ActiveDefinitions 返回当前已经对模型公开的工具定义。
func (r *Registry) ActiveDefinitions() []Tool {
	active := make([]Tool, 0, len(r.definitions)+len(r.deferredDefinitions))
	active = append(active, r.definitions...)
	active = append(active, r.mcpDefinitions()...)
	for _, tool := range r.deferredDefinitions {
		if r.activatedDeferred[tool.Name] {
			active = append(active, tool)
		}
	}
	return active
}

// DeferredDefinitions 返回当前延迟公开的工具定义。
func (r *Registry) DeferredDefinitions() []Tool {
	return r.deferredDefinitions
}

// CloneWithAllowedTools 返回一个只保留指定工具名的注册表副本。
// 把过滤逻辑下沉到 tools 包内部，可以避免上层直接操作未导出字段。
func (r *Registry) CloneWithAllowedTools(allowedToolNames []string) *Registry {
	allowed := map[string]bool{}
	for _, name := range allowedToolNames {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			allowed[trimmed] = true
		}
	}

	clone := &Registry{
		workingDirectory:    r.workingDirectory,
		skillStore:          skills.NewStore(r.workingDirectory),
		definitions:         filterToolsByName(r.definitions, allowed),
		deferredDefinitions: filterToolsByName(r.deferredDefinitions, allowed),
		activatedDeferred:   map[string]bool{},
		mcpManager:          r.mcpManager,
	}

	// 对于被允许的延迟工具，如果上游已经激活，则在副本里同步激活状态，
	// 这样子智能体或受限上下文看到的工具视图会和主注册表保持一致。
	for name := range allowed {
		if r.activatedDeferred[name] {
			clone.activatedDeferred[name] = true
		}
	}
	return clone
}

// AllDefinitions 返回立即工具和延迟工具的并集。
func (r *Registry) AllDefinitions() []Tool {
	all := make([]Tool, 0, len(r.definitions)+len(r.deferredDefinitions))
	all = append(all, r.definitions...)
	all = append(all, r.mcpDefinitions()...)
	all = append(all, r.deferredDefinitions...)
	return all
}

// ToolByName 根据名称返回工具定义。
func (r *Registry) ToolByName(name string) (*Tool, bool) {
	for _, tool := range r.AllDefinitions() {
		if tool.Name == name {
			copyTool := tool
			return &copyTool, true
		}
	}
	return nil, false
}

// DeferredToolNames 返回尚未激活的延迟工具名称列表。
func (r *Registry) DeferredToolNames() []string {
	names := make([]string, 0, len(r.deferredDefinitions))
	for _, tool := range r.deferredDefinitions {
		if !r.activatedDeferred[tool.Name] {
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	return names
}

// SearchDeferredTools 根据关键字搜索延迟工具。
func (r *Registry) SearchDeferredTools(query string) []Tool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return r.deferredDefinitions
	}

	result := make([]Tool, 0, len(r.deferredDefinitions))
	for _, tool := range r.deferredDefinitions {
		name := strings.ToLower(tool.Name)
		description := strings.ToLower(tool.Description)
		if strings.Contains(name, query) || strings.Contains(description, query) {
			result = append(result, tool)
		}
	}
	return result
}

// ActivateDeferredTools 按关键字激活延迟工具，并返回本次命中的工具定义。
func (r *Registry) ActivateDeferredTools(query string) []Tool {
	matches := r.SearchDeferredTools(query)
	for _, tool := range matches {
		r.activatedDeferred[tool.Name] = true
	}
	return matches
}

// FormatToolDefinition 把工具定义格式化为便于模型消费的 JSON 文本。
// 这里复用统一的 schema 元数据，把 deferred tool 从"只返回名字"推进到"返回完整可用定义"。
func FormatToolDefinition(tool Tool) string {
	payload := map[string]any{
		"name":         tool.Name,
		"description":  tool.Description,
		"input_schema": tool.InputSchema,
		"read_only":    tool.ReadOnly,
		"deferred":     tool.Deferred,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return tool.Name + ": " + tool.Description
	}
	return string(data)
}

// mcpDefinitions 把当前已发现的 MCP 工具转换成统一 Tool 定义。
// 这里沿用源仓库的 `mcp__server__tool` 前缀策略，避免与本地工具重名冲突。
func (r *Registry) mcpDefinitions() []Tool {
	if r.mcpManager == nil {
		return nil
	}

	remoteTools := r.mcpManager.ToolDefinitions()
	definitions := make([]Tool, 0, len(remoteTools))
	for _, item := range remoteTools {
		schema := item.InputSchema
		if schema == nil {
			schema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}

		description := strings.TrimSpace(item.Description)
		if description == "" {
			description = fmt.Sprintf("MCP tool %s from %s.", item.Name, item.ServerName)
		}
		definitions = append(definitions, Tool{
			Name:        fmt.Sprintf("mcp__%s__%s", item.ServerName, item.Name),
			Description: description,
			InputSchema: schema,
			ReadOnly:    false,
		})
	}
	return definitions
}

// errResultLimitReached 用作 Walk 提前退出的内部哨兵错误。
var errResultLimitReached = fmt.Errorf("result limit reached")

// filterToolsByName 按名称白名单过滤工具定义切片。
func filterToolsByName(source []Tool, allowed map[string]bool) []Tool {
	result := make([]Tool, 0, len(source))
	for _, definition := range source {
		if allowed[definition.Name] {
			result = append(result, definition)
		}
	}
	return result
}

// schemaString 构造字符串字段 schema。
func schemaString(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

// schemaEnum 构造带枚举约束的字符串字段 schema。
func schemaEnum(description string, values ...string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
		"enum":        values,
	}
}

// requiredSchema 构造带 required 列表的对象 schema。
func requiredSchema(properties map[string]any, required ...string) map[string]any {
	schema := optionalSchema(properties)
	schema["required"] = required
	return schema
}

// optionalSchema 构造对象 schema。
func optionalSchema(properties map[string]any) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
	}
}

// min 返回两个整数中的较小值。
func min(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

