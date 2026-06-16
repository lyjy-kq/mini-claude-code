// Package cli 负责验证命令行参数解析与帮助文案行为。
// 本测试文件聚焦源仓库已支持、Go 版刚补齐的 --api-base 入口，避免后续回归丢失用户可见能力。
package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"mini-claude-code/internal/config"
	"mini-claude-code/internal/memory"
	"mini-claude-code/internal/skills"
)

// stubResumableRunner 用于验证 CLI 启动阶段的 resume 初始化逻辑。
// 这里用最小替身隔离真实 session 文件读写，只关注入口层是否按预期触发恢复。
type stubResumableRunner struct {
	// wantsResume 表示当前是否模拟传入了 --resume。
	wantsResume bool
	// summary 表示恢复成功时返回的摘要文案。
	summary string
	// err 表示恢复时返回的错误。
	err error
	// resumeCalls 记录 ResumeLatest 被调用的次数。
	resumeCalls int
}

// WantsResume 返回当前测试替身是否处于 resume 模式。
func (s *stubResumableRunner) WantsResume() bool {
	return s.wantsResume
}

// ResumeLatest 模拟恢复最近一次会话，并记录调用次数。
func (s *stubResumableRunner) ResumeLatest() (string, error) {
	s.resumeCalls++
	return s.summary, s.err
}

// TestParseArgsParsesAPIBase 验证 CLI 能正确解析 --api-base 并保留其他位置参数。
// 这样用户就可以像源仓库一样，通过命令行临时切换 OpenAI-compatible 后端。
func TestParseArgsParsesAPIBase(t *testing.T) {
	runtimeConfig := config.Config{
		DefaultModel:     "claude-opus-4-6",
		WorkingDirectory: ".",
	}

	parsed, err := ParseArgs([]string{"--api-base", "https://example.com/v1", "--model", "gpt-4o", "hello"}, runtimeConfig)
	if err != nil {
		t.Fatalf("ParseArgs() error = %v", err)
	}
	if parsed.APIBaseURL != "https://example.com/v1" {
		t.Fatalf("parsed.APIBaseURL = %q, want %q", parsed.APIBaseURL, "https://example.com/v1")
	}
	if parsed.Model != "gpt-4o" {
		t.Fatalf("parsed.Model = %q, want %q", parsed.Model, "gpt-4o")
	}
	if parsed.Prompt != "hello" {
		t.Fatalf("parsed.Prompt = %q, want %q", parsed.Prompt, "hello")
	}
}

// TestParseArgsRejectsMissingAPIBaseValue 验证 --api-base 缺值时会返回明确错误。
// 这样命令行输入错误不会默默落成无效配置，便于用户快速修正。
func TestParseArgsRejectsMissingAPIBaseValue(t *testing.T) {
	runtimeConfig := config.Config{
		DefaultModel:     "claude-opus-4-6",
		WorkingDirectory: ".",
	}

	_, err := ParseArgs([]string{"--api-base"}, runtimeConfig)
	if err == nil {
		t.Fatal("ParseArgs() should reject missing --api-base value")
	}
	if !strings.Contains(err.Error(), "--api-base requires a value") {
		t.Fatalf("ParseArgs() error = %q, want message containing %q", err.Error(), "--api-base requires a value")
	}
}

// TestHelpTextMentionsAPIBase 验证帮助文案显式暴露 --api-base 入口。
// 这保证补齐的 CLI 能力对用户是可发现的，而不是只能靠阅读源码知道。
func TestHelpTextMentionsAPIBase(t *testing.T) {
	help := HelpText()
	if !strings.Contains(help, "--api-base URL") {
		t.Fatalf("HelpText() should mention --api-base URL, got: %s", help)
	}
}

// TestHelpTextMentionsSkillsCommand 验证帮助文案会显式说明 /skills 命令用途。
// 这能保证用户进入 REPL 后可以直接发现技能列表入口，而不是只能靠猜测命令名。
func TestHelpTextMentionsSkillsCommand(t *testing.T) {
	help := HelpText()
	if !strings.Contains(help, "/skills             List available skills") {
		t.Fatalf("HelpText() should describe /skills command, got: %s", help)
	}
}

// TestHelpTextMentionsReplStartupExample 验证帮助文案会显式告诉用户可以直接启动交互式 REPL。
// 这样 CLI 首次使用路径就和源码仓库更一致，用户不需要猜空参数是否会进入交互模式。
func TestHelpTextMentionsReplStartupExample(t *testing.T) {
	help := HelpText()
	if !strings.Contains(help, `mini-claude  # starts interactive REPL`) {
		t.Fatalf("HelpText() should mention REPL startup example, got: %s", help)
	}
}

// TestHelpTextMentionsPlanAndClearDescriptions 验证帮助文案会给核心 REPL 命令补上源码仓库风格的用途说明。
// 这能锁住首屏之后最常用的命令认知路径，避免后续又退回成只列命令名的弱提示。
func TestHelpTextMentionsPlanAndClearDescriptions(t *testing.T) {
	help := HelpText()
	if !strings.Contains(help, "/clear              Clear conversation history") {
		t.Fatalf("HelpText() should describe /clear, got: %s", help)
	}
	if !strings.Contains(help, "/plan               Toggle plan mode (read-only ↔ normal)") {
		t.Fatalf("HelpText() should describe /plan, got: %s", help)
	}
}

// TestFormatSkillsOutputEmpty 验证没有技能时会给出与源码仓库一致的引导文案。
func TestFormatSkillsOutputEmpty(t *testing.T) {
	output := formatSkillsOutput(nil)
	if !strings.Contains(output, "No skills found. Add skills to .claude/skills/<name>/SKILL.md") {
		t.Fatalf("formatSkillsOutput() = %q, want no-skills guidance", output)
	}
}

// TestFormatSkillsOutputIncludesWhenToUse 验证技能列表会同时展示调用方式、来源和 When to use 提示。
// 这能锁住本轮补齐的用户可见信息，避免后续迁移把展示又退回成只剩技能名。
func TestFormatSkillsOutputIncludesWhenToUse(t *testing.T) {
	output := formatSkillsOutput([]skills.Skill{
		{
			Name:          "review",
			Description:   "Review the current change set",
			WhenToUse:     "Use when the user asks for a review",
			Source:        "project",
			Context:       skills.ContextInline,
			UserInvocable: true,
		},
		{
			Name:          "refactor-helper",
			Description:   "Assist with larger refactors",
			Source:        "user",
			Context:       skills.ContextFork,
			UserInvocable: false,
		},
	})

	if !strings.Contains(output, "2 skills:") {
		t.Fatalf("formatSkillsOutput() should include skill count, got: %s", output)
	}
	if !strings.Contains(output, "/review (project, context=inline) - Review the current change set") {
		t.Fatalf("formatSkillsOutput() missing user-invocable skill line, got: %s", output)
	}
	if !strings.Contains(output, "When to use: Use when the user asks for a review") {
		t.Fatalf("formatSkillsOutput() missing when-to-use hint, got: %s", output)
	}
	if !strings.Contains(output, "refactor-helper (user, context=fork) - Assist with larger refactors") {
		t.Fatalf("formatSkillsOutput() missing auto skill line, got: %s", output)
	}
}

// TestResolveRuntimeConfigForCLIPrefersOpenAIPair 验证当 OpenAI key+base 已完整存在时，CLI 解析优先选择 OpenAI-compatible 后端。
// 这样运行时后端选择会和源仓库保持一致，不会在多套环境变量共存时漂移到另一条链路。
func TestResolveRuntimeConfigForCLIPrefersOpenAIPair(t *testing.T) {
	parsed := ParsedArgs{}
	runtimeConfig := config.Config{
		OpenAIAPIKey:     "openai-key",
		OpenAIBaseURL:    "https://openai.example.com",
		AnthropicAPIKey:  "anthropic-key",
		AnthropicBaseURL: "https://anthropic.example.com",
	}

	resolved, err := ResolveRuntimeConfigForCLI(parsed, runtimeConfig)
	if err != nil {
		t.Fatalf("ResolveRuntimeConfigForCLI() error = %v", err)
	}
	if resolved.OpenAIAPIKey != "openai-key" {
		t.Fatalf("resolved.OpenAIAPIKey = %q, want %q", resolved.OpenAIAPIKey, "openai-key")
	}
	if resolved.OpenAIBaseURL != "https://openai.example.com" {
		t.Fatalf("resolved.OpenAIBaseURL = %q, want %q", resolved.OpenAIBaseURL, "https://openai.example.com")
	}
	if resolved.AnthropicAPIKey != "" {
		t.Fatalf("resolved.AnthropicAPIKey = %q, want empty", resolved.AnthropicAPIKey)
	}
}

// TestResolveRuntimeConfigForCLIUsesAPIBaseWithFallbackKey 验证仅传 --api-base 时会尝试复用现有 key，并按 OpenAI-compatible 处理。
// 这对应源仓库的临时兼容后端切换能力，能让用户不改环境变量就快速试跑兼容网关。
func TestResolveRuntimeConfigForCLIUsesAPIBaseWithFallbackKey(t *testing.T) {
	parsed := ParsedArgs{
		APIBaseURL: "https://gateway.example.com/v1",
	}
	runtimeConfig := config.Config{
		AnthropicAPIKey:  "anthropic-key",
		AnthropicBaseURL: "https://anthropic.example.com",
	}

	resolved, err := ResolveRuntimeConfigForCLI(parsed, runtimeConfig)
	if err != nil {
		t.Fatalf("ResolveRuntimeConfigForCLI() error = %v", err)
	}
	if resolved.OpenAIAPIKey != "anthropic-key" {
		t.Fatalf("resolved.OpenAIAPIKey = %q, want fallback key %q", resolved.OpenAIAPIKey, "anthropic-key")
	}
	if resolved.OpenAIBaseURL != "https://gateway.example.com/v1" {
		t.Fatalf("resolved.OpenAIBaseURL = %q, want %q", resolved.OpenAIBaseURL, "https://gateway.example.com/v1")
	}
	if resolved.AnthropicAPIKey != "" {
		t.Fatalf("resolved.AnthropicAPIKey = %q, want empty", resolved.AnthropicAPIKey)
	}
}

// TestResolveRuntimeConfigForCLIRejectsMissingKeys 验证没有任何可用 API key 时会直接报错。
// 这样 Go 版不会再静默退回 mock client，而是像源仓库一样在启动阶段明确暴露配置问题。
func TestResolveRuntimeConfigForCLIRejectsMissingKeys(t *testing.T) {
	parsed := ParsedArgs{}
	runtimeConfig := config.Config{}

	_, err := ResolveRuntimeConfigForCLI(parsed, runtimeConfig)
	if err == nil {
		t.Fatal("ResolveRuntimeConfigForCLI() should reject missing API keys")
	}
	if !strings.Contains(err.Error(), "API key is required.") {
		t.Fatalf("ResolveRuntimeConfigForCLI() error = %q, want message containing %q", err.Error(), "API key is required.")
	}
}

// TestInitializeResumeForCLISkipsWhenDisabled 验证未启用 --resume 时不会误触发会话恢复。
// 这样默认单次执行和 REPL 都不会无意中加载旧上下文，保持显式 opt-in 行为。
func TestInitializeResumeForCLISkipsWhenDisabled(t *testing.T) {
	runner := &stubResumableRunner{
		wantsResume: false,
		summary:     "restored",
	}

	summary, err := InitializeResumeForCLI(runner)
	if err != nil {
		t.Fatalf("InitializeResumeForCLI() error = %v", err)
	}
	if summary != "" {
		t.Fatalf("InitializeResumeForCLI() summary = %q, want empty", summary)
	}
	if runner.resumeCalls != 0 {
		t.Fatalf("runner.resumeCalls = %d, want %d", runner.resumeCalls, 0)
	}
}

// TestInitializeResumeForCLIReturnsResumeSummary 验证启用 --resume 时会统一走最近会话恢复入口。
// 这样单次 prompt 与 REPL 都能共享同一条启动恢复逻辑，而不是只有 REPL 模式生效。
func TestInitializeResumeForCLIReturnsResumeSummary(t *testing.T) {
	runner := &stubResumableRunner{
		wantsResume: true,
		summary:     "session restored",
	}

	summary, err := InitializeResumeForCLI(runner)
	if err != nil {
		t.Fatalf("InitializeResumeForCLI() error = %v", err)
	}
	if summary != "session restored" {
		t.Fatalf("InitializeResumeForCLI() summary = %q, want %q", summary, "session restored")
	}
	if runner.resumeCalls != 1 {
		t.Fatalf("runner.resumeCalls = %d, want %d", runner.resumeCalls, 1)
	}
}

// TestFormatMemoriesOutputEmpty 验证没有可展示记忆时会给出清晰的空态提示。
// 这能锁住本轮 `/memory` 面板的用户可见文案，避免后续又退回成空白输出。
func TestFormatMemoriesOutputEmpty(t *testing.T) {
	output := formatMemoriesOutput(nil)
	if output != "No memories saved yet." {
		t.Fatalf("formatMemoriesOutput() = %q, want %q", output, "No memories saved yet.")
	}
}

// TestFormatMemoriesOutputIncludesTypeNameAndDescription 验证 `/memory` 会展示类型、名称和描述。
// 这里同时覆盖索引文件过滤和名称/描述 fallback，确保展示层和 memory 条目结构对齐。
func TestFormatMemoriesOutputIncludesTypeNameAndDescription(t *testing.T) {
	root := t.TempDir()
	output := formatMemoriesOutput([]memory.Entry{
		{
			Path:        filepath.Join(root, ".mini-claude", "memory", "workspace-notes.md"),
			Name:        "workspace-notes",
			Type:        "project",
			Description: "Important workspace conventions",
		},
		{
			Path: filepath.Join(root, ".mini-claude", "memory", "recent-findings.md"),
		},
		{
			Path:        filepath.Join(root, ".mini-claude", "memory", "MEMORY.md"),
			Name:        "MEMORY",
			Type:        "index",
			Description: "Should be filtered",
		},
	})

	if !strings.Contains(output, "2 memories:") {
		t.Fatalf("formatMemoriesOutput() should include count, got: %s", output)
	}
	if !strings.Contains(output, "[project] workspace-notes - Important workspace conventions") {
		t.Fatalf("formatMemoriesOutput() missing full entry line, got: %s", output)
	}
	if !strings.Contains(output, "[memory] recent-findings - (no description)") {
		t.Fatalf("formatMemoriesOutput() missing fallback entry line, got: %s", output)
	}
	if strings.Contains(output, "MEMORY") {
		t.Fatalf("formatMemoriesOutput() should filter MEMORY.md, got: %s", output)
	}
}
