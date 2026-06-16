// Package subagent 负责发现、组织和执行本地子智能体定义。
// 本文件在“只发现定义”的基础上继续补齐最小执行配置，让主 Agent 可以按内置或自定义类型创建隔离子任务。
package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mini-claude-code/internal/frontmatter"
)

// Definition 表示一个子智能体定义。
type Definition struct {
	// Name 表示子智能体名称。
	Name string
	// Description 表示子智能体说明。
	Description string
	// Path 表示定义文件路径。
	Path string
	// SystemPrompt 表示子智能体正文系统提示词。
	SystemPrompt string
	// AllowedTools 表示允许使用的工具列表。
	AllowedTools []string
}

// Config 表示子智能体执行时需要的最小配置。
type Config struct {
	// SystemPrompt 表示子智能体系统提示词。
	SystemPrompt string
	// ToolNames 表示允许给子智能体暴露的工具名称。
	ToolNames []string
}

// Store 表示子智能体定义存储器。
type Store struct {
	// root 表示项目工作目录。
	root string
}

// builtInReadOnlyToolNames 定义 explore / plan 子智能体默认可见的只读代码探索工具。
// 这里刻意向源仓库对齐为最小只读集合，避免把技能解析或外部抓取能力混入“代码勘探”语义。
var builtInReadOnlyToolNames = []string{"read_file", "list_files", "grep_search"}

// defaultGeneralToolNames 定义 general 子智能体默认可见的工具集合。
// 这里显式排除 agent 本身，避免子智能体递归再拉起子智能体，先保持当前轻量执行链路可控。
var defaultGeneralToolNames = []string{
	"read_file", "write_file", "edit_file", "list_files",
	"grep_search", "run_shell", "skill", "web_fetch",
}

const (
	explorePrompt = "You are an explore sub-agent. You are read-only and specialized in searching code, reading files, and reporting findings quickly."
	planPrompt    = "You are a plan sub-agent. You are read-only and specialized in analyzing codebases and producing structured implementation plans."
	generalPrompt = "You are a general sub-agent. Complete the delegated task fully, using the allowed tools only, and return a concise result summary."
)

// NewStore 创建新的子智能体定义存储器。
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Discover 扫描用户级和项目级 agent 定义，并按项目级覆盖用户级同名定义。
func (s *Store) Discover() ([]Definition, error) {
	resultMap := map[string]Definition{}

	for _, baseDir := range []string{
		filepath.Join(userHomeDir(), ".claude", "agents"),
		filepath.Join(s.root, ".claude", "agents"),
	} {
		loaded, err := s.loadFromDir(baseDir)
		if err != nil {
			return nil, err
		}
		for _, definition := range loaded {
			resultMap[definition.Name] = definition
		}
	}

	names := make([]string, 0, len(resultMap))
	for name := range resultMap {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]Definition, 0, len(names))
	for _, name := range names {
		result = append(result, resultMap[name])
	}
	return result, nil
}

// BuildPromptSection 构造 system prompt 中的自定义 agent 描述段落。
func (s *Store) BuildPromptSection() string {
	definitions, err := s.Discover()
	if err != nil || len(definitions) == 0 {
		return ""
	}

	lines := []string{"# Custom Agent Types", ""}
	for _, definition := range definitions {
		description := strings.TrimSpace(definition.Description)
		if description == "" {
			description = "No description"
		}
		lines = append(lines, "- "+definition.Name+": "+description)
	}
	return strings.Join(lines, "\n")
}

// GetConfig 返回指定子智能体类型的执行配置。
// 这里先对齐源仓库最重要的三类内置 agent，并支持项目/用户自定义定义的最小执行闭环。
func (s *Store) GetConfig(name string) (Config, error) {
	customDefinitions, err := s.Discover()
	if err != nil {
		return Config{}, err
	}

	for _, definition := range customDefinitions {
		if definition.Name == name {
			toolNames := sanitizeToolNames(definition.AllowedTools)
			if len(toolNames) == 0 {
				toolNames = append([]string{}, defaultGeneralToolNames...)
			}
			return Config{
				SystemPrompt: strings.TrimSpace(definition.SystemPrompt),
				ToolNames:    toolNames,
			}, nil
		}
	}

	switch name {
	case "explore":
		return Config{SystemPrompt: explorePrompt, ToolNames: append([]string{}, builtInReadOnlyToolNames...)}, nil
	case "plan":
		return Config{SystemPrompt: planPrompt, ToolNames: append([]string{}, builtInReadOnlyToolNames...)}, nil
	case "general", "":
		return Config{
			SystemPrompt: generalPrompt,
			ToolNames:    append([]string{}, defaultGeneralToolNames...),
		}, nil
	default:
		return Config{}, fmt.Errorf("subagent type not found: %s", name)
	}
}

// loadFromDir 从指定目录读取 agent 定义文件。
func (s *Store) loadFromDir(root string) ([]Definition, error) {
	if root == "" {
		return []Definition{}, nil
	}
	if _, err := os.Stat(root); err != nil {
		return []Definition{}, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	result := make([]Definition, 0, len(entries))
	for _, item := range entries {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".md") {
			continue
		}

		target := filepath.Join(root, item.Name())
		data, err := os.ReadFile(target)
		if err != nil {
			continue
		}

		parsed := frontmatter.Parse(string(data))
		name := strings.TrimSuffix(item.Name(), ".md")
		if strings.TrimSpace(parsed.Meta["name"]) != "" {
			name = parsed.Meta["name"]
		}

		result = append(result, Definition{
			Name:         name,
			Description:  parsed.Meta["description"],
			Path:         target,
			SystemPrompt: parsed.Body,
			AllowedTools: splitCSV(parsed.Meta["allowed-tools"]),
		})
	}
	return result, nil
}

// userHomeDir 返回当前用户主目录，失败时返回空字符串。
func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// splitCSV 把逗号分隔内容拆成字符串数组。
func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// sanitizeToolNames 过滤掉当前轻量子智能体实现里不应该递归暴露的工具名。
// 这样即便用户在 frontmatter 里手动写了 agent，也不会让子智能体形成递归拉起链路。
func sanitizeToolNames(toolNames []string) []string {
	result := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || trimmed == "agent" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}
