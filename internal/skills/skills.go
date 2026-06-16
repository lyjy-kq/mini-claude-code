// Package skills 负责发现、解析和组织本地技能定义。
// 本文件对齐 `.claude/skills/<name>/SKILL.md` 约定，支持用户级与项目级覆盖，
// 并补齐源仓库里的 `when-to-use` 元数据与分组展示能力。
package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mini-claude-code/internal/frontmatter"
)

// ContextMode 表示技能的执行上下文模式。
type ContextMode string

const (
	// ContextInline 表示技能内容直接注入主会话。
	ContextInline ContextMode = "inline"
	// ContextFork 表示技能内容应交给独立子智能体处理。
	ContextFork ContextMode = "fork"
)

// Skill 表示一个已发现的技能定义。
type Skill struct {
	// Name 表示技能名称。
	Name string
	// Description 表示技能说明。
	Description string
	// WhenToUse 表示技能何时应该被使用。
	// 这个字段对齐源仓库的 `when-to-use` 元数据，便于在 system prompt 和 `/skills` 展示更细的触发语义。
	WhenToUse string
	// Path 表示技能文件完整路径。
	Path string
	// Source 表示技能来源，例如 `project` 或 `user`。
	Source string
	// UserInvocable 表示用户是否可以直接通过 `/技能名` 调用。
	UserInvocable bool
	// Context 表示技能应以内联还是 `fork` 模式生效。
	Context ContextMode
	// AllowedTools 表示技能显式允许的工具列表。
	AllowedTools []string
	// PromptTemplate 表示技能正文模板。
	PromptTemplate string
}

// Store 表示技能存储器。
type Store struct {
	// root 表示项目工作目录。
	root string
}

// NewStore 创建技能存储器。
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Discover 扫描用户级和项目级技能目录，并按项目级覆盖用户级同名技能。
func (s *Store) Discover() ([]Skill, error) {
	resultMap := map[string]Skill{}

	for _, source := range []struct {
		baseDir string
		label   string
	}{
		{baseDir: filepath.Join(userHomeDir(), ".claude", "skills"), label: "user"},
		{baseDir: filepath.Join(s.root, ".claude", "skills"), label: "project"},
	} {
		loaded, err := s.loadFromDir(source.baseDir, source.label)
		if err != nil {
			return nil, err
		}
		for _, skill := range loaded {
			resultMap[skill.Name] = skill
		}
	}

	names := make([]string, 0, len(resultMap))
	for name := range resultMap {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]Skill, 0, len(names))
	for _, name := range names {
		result = append(result, resultMap[name])
	}
	return result, nil
}

// GetByName 根据名称返回单个技能。
func (s *Store) GetByName(name string) (*Skill, error) {
	skills, err := s.Discover()
	if err != nil {
		return nil, err
	}
	for _, skill := range skills {
		if skill.Name == name {
			copySkill := skill
			return &copySkill, nil
		}
	}
	return nil, nil
}

// ResolvePrompt 根据传入参数展开技能模板占位符。
func (s *Store) ResolvePrompt(skill Skill, args string) string {
	prompt := skill.PromptTemplate
	prompt = strings.ReplaceAll(prompt, "$ARGUMENTS", args)
	prompt = strings.ReplaceAll(prompt, "${ARGUMENTS}", args)
	prompt = strings.ReplaceAll(prompt, "${CLAUDE_SKILL_DIR}", filepath.Dir(skill.Path))
	return prompt
}

// BuildPromptSection 构造 system prompt 中的技能描述段落。
// 这里对齐源仓库的展示方式：把“用户可直接调用”和“仅自动调用”的技能分组展示，并补上 when-to-use 提示。
func (s *Store) BuildPromptSection() string {
	skills, err := s.Discover()
	if err != nil || len(skills) == 0 {
		return ""
	}

	lines := []string{"# Available Skills", ""}
	userInvocable := make([]Skill, 0, len(skills))
	autoOnly := make([]Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.UserInvocable {
			userInvocable = append(userInvocable, skill)
			continue
		}
		autoOnly = append(autoOnly, skill)
	}

	if len(userInvocable) > 0 {
		lines = append(lines, "User-invocable skills (user types /<name> to invoke):")
		for _, skill := range userInvocable {
			description := strings.TrimSpace(skill.Description)
			if description == "" {
				description = "No description"
			}
			lines = append(lines, "- /"+skill.Name+": "+description)
			if strings.TrimSpace(skill.WhenToUse) != "" {
				lines = append(lines, "  When to use: "+strings.TrimSpace(skill.WhenToUse))
			}
		}
		lines = append(lines, "")
	}

	if len(autoOnly) > 0 {
		lines = append(lines, "Auto-invocable skills (use the skill tool when appropriate):")
		for _, skill := range autoOnly {
			description := strings.TrimSpace(skill.Description)
			if description == "" {
				description = "No description"
			}
			lines = append(lines, "- "+skill.Name+": "+description)
			if strings.TrimSpace(skill.WhenToUse) != "" {
				lines = append(lines, "  When to use: "+strings.TrimSpace(skill.WhenToUse))
			}
		}
		lines = append(lines, "")
	}

	lines = append(lines, "To invoke a skill programmatically, use the `skill` tool with the skill name and optional arguments.")
	return strings.Join(lines, "\n")
}

// loadFromDir 从给定目录加载技能定义。
func (s *Store) loadFromDir(root string, source string) ([]Skill, error) {
	if root == "" {
		return []Skill{}, nil
	}
	if _, err := os.Stat(root); err != nil {
		return []Skill{}, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	result := make([]Skill, 0, len(entries))
	for _, item := range entries {
		if !item.IsDir() {
			continue
		}

		target := filepath.Join(root, item.Name(), "SKILL.md")
		data, err := os.ReadFile(target)
		if err != nil {
			continue
		}

		parsed := frontmatter.Parse(string(data))
		context := ContextInline
		if parsed.Meta["context"] == "fork" {
			context = ContextFork
		}

		result = append(result, Skill{
			Name:           firstNonEmpty(parsed.Meta["name"], item.Name()),
			Description:    parsed.Meta["description"],
			WhenToUse:      firstNonEmpty(parsed.Meta["when_to_use"], parsed.Meta["when-to-use"]),
			Path:           target,
			Source:         source,
			UserInvocable:  parsed.Meta["user-invocable"] != "false",
			Context:        context,
			AllowedTools:   splitCSV(parsed.Meta["allowed-tools"]),
			PromptTemplate: parsed.Body,
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

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
