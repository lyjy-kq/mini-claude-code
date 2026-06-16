// Package prompt 验证 system prompt 中的 memory 提示段与源码仓库主链语义保持一致。
// 这些测试锁住 memory 路径暴露、索引注入和关键保存规则，避免后续重构把模型看到的记忆能力说明改弱。
package prompt

import (
	"path/filepath"
	"strings"
	"testing"

	"mini-claude-code/internal/memory"
)

// TestBuildMemoryPromptSectionUsesStoreMemoryDir 验证 prompt 会暴露 home-scoped 的 memory 目录。
func TestBuildMemoryPromptSectionUsesStoreMemoryDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	store := memory.NewStore(root)

	section := buildMemoryPromptSection(root)
	if !strings.Contains(section, "You have a persistent, file-based memory system at `"+store.MemoryDir()+"`.") {
		t.Fatalf("buildMemoryPromptSection() should mention store memory dir, got: %s", section)
	}
	if !strings.Contains(section, "The MEMORY.md index is auto-updated when you write to the memory directory - do NOT update it manually.") {
		t.Fatalf("buildMemoryPromptSection() missing auto-index guidance, got: %s", section)
	}
}

// TestBuildMemoryPromptSectionIncludesCurrentIndex 验证已有 MEMORY.md 时，prompt 会注入当前索引内容。
func TestBuildMemoryPromptSectionIncludesCurrentIndex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	store := memory.NewStore(root)
	if err := store.Save(memory.Entry{
		Name:        "API Gateway",
		Type:        "project",
		Description: "Current deployment entrypoint",
		Content:     "Use the internal gateway first.",
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	section := buildMemoryPromptSection(root)
	if !strings.Contains(section, "## Current Memory Index") {
		t.Fatalf("buildMemoryPromptSection() missing memory index header: %s", section)
	}
	if !strings.Contains(section, "API Gateway") {
		t.Fatalf("buildMemoryPromptSection() missing saved memory in index: %s", section)
	}
}
