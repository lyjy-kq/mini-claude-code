// Package skills 负责验证技能元数据解析与系统提示展示行为。
// 本测试文件聚焦源仓库已有、Go 版刚补齐的 when-to-use 元数据与分组展示能力。
package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoverParsesWhenToUse 验证技能发现会解析 when-to-use 元数据。
// 这样 system prompt 和 `/skills` 展示就能携带更细的触发语义，而不只剩下一行 description。
func TestDiscoverParsesWhenToUse(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".claude", "skills", "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	content := `---
name: demo-skill
description: Demo description
when-to-use: Use when the user asks for a demo
user-invocable: true
---

Demo body`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(root)
	discovered, err := store.Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("len(discovered) = %d, want %d", len(discovered), 1)
	}
	if discovered[0].WhenToUse != "Use when the user asks for a demo" {
		t.Fatalf("discovered[0].WhenToUse = %q, want %q", discovered[0].WhenToUse, "Use when the user asks for a demo")
	}
}

// TestBuildPromptSectionGroupsSkills 验证 system prompt 中的技能展示会按用户可调用与自动调用分组。
// 这样 Go 版的技能可见性就更贴近源仓库，而不再只是简单地按一列平铺所有技能。
func TestBuildPromptSectionGroupsSkills(t *testing.T) {
	root := t.TempDir()

	userSkillDir := filepath.Join(root, ".claude", "skills", "user-skill")
	if err := os.MkdirAll(userSkillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(userSkillDir) error = %v", err)
	}
	userSkill := `---
name: user-skill
description: User callable skill
when-to-use: Use when the user asks directly
user-invocable: true
---

User body`
	if err := os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte(userSkill), 0o644); err != nil {
		t.Fatalf("WriteFile(userSkill) error = %v", err)
	}

	autoSkillDir := filepath.Join(root, ".claude", "skills", "auto-skill")
	if err := os.MkdirAll(autoSkillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(autoSkillDir) error = %v", err)
	}
	autoSkill := `---
name: auto-skill
description: Auto only skill
when_to_use: Use when the model should self-route
user-invocable: false
---

Auto body`
	if err := os.WriteFile(filepath.Join(autoSkillDir, "SKILL.md"), []byte(autoSkill), 0o644); err != nil {
		t.Fatalf("WriteFile(autoSkill) error = %v", err)
	}

	store := NewStore(root)
	section := store.BuildPromptSection()

	expectedSnippets := []string{
		"User-invocable skills (user types /<name> to invoke):",
		"- /user-skill: User callable skill",
		"When to use: Use when the user asks directly",
		"Auto-invocable skills (use the skill tool when appropriate):",
		"- auto-skill: Auto only skill",
		"When to use: Use when the model should self-route",
		"To invoke a skill programmatically, use the `skill` tool with the skill name and optional arguments.",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(section, snippet) {
			t.Fatalf("BuildPromptSection() missing snippet %q in:\n%s", snippet, section)
		}
	}
}
