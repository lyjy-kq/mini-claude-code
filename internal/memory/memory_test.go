// Package memory 验证记忆存储路径、索引格式和索引截断行为。
// 这些测试锁住与源码仓库对齐后的 memory 主链语义，避免后续迁移把持久化位置和索引格式改回旧版本。
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMemoryDirUsesHomeScopedProjectHash 验证 memory 目录会落到用户主目录下的项目 hash 空间。
func TestMemoryDirUsesHomeScopedProjectHash(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	store := NewStore(root)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}

	expected := filepath.Join(homeDir, ".mini-claude", "projects", projectHash(root), "memory")
	if store.MemoryDir() != expected {
		t.Fatalf("MemoryDir() = %q, want %q", store.MemoryDir(), expected)
	}
}

// TestSaveWritesTypedSlugFileAndReadableIndex 验证保存记忆时会生成类型前缀文件名，并重建可读索引。
func TestSaveWritesTypedSlugFileAndReadableIndex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	store := NewStore(root)

	if err := store.Save(Entry{
		Name:        "Alice Preferences",
		Type:        "user",
		Description: "Editor and tone preferences",
		Content:     "Prefers Vim and concise replies.",
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	filePath := filepath.Join(store.MemoryDir(), "user_alice_preferences.md")
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("expected memory file %q to exist: %v", filePath, err)
	}

	index, err := store.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex() error = %v", err)
	}
	if !strings.Contains(index, "# Memory Index") {
		t.Fatalf("LoadIndex() missing header: %s", index)
	}
	if !strings.Contains(index, "**[Alice Preferences](user_alice_preferences.md)** (user) - Editor and tone preferences") {
		t.Fatalf("LoadIndex() missing formatted entry: %s", index)
	}
}

// TestListEntriesSortsNewestFirst 验证完整记忆列表会按最近修改时间倒序返回。
// 这能锁住 `/memory` 和 MEMORY.md 看板的主链体感，避免展示顺序退回成路径字典序。
func TestListEntriesSortsNewestFirst(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	store := NewStore(root)

	if err := store.Save(Entry{
		Name:        "Older Note",
		Type:        "project",
		Description: "Older memory entry",
		Content:     "older",
	}); err != nil {
		t.Fatalf("Save() older error = %v", err)
	}

	if err := store.Save(Entry{
		Name:        "Newer Note",
		Type:        "project",
		Description: "Newer memory entry",
		Content:     "newer",
	}); err != nil {
		t.Fatalf("Save() newer error = %v", err)
	}

	entries, err := store.ListEntries()
	if err != nil {
		t.Fatalf("ListEntries() error = %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("ListEntries() returned %d entries, want at least 2", len(entries))
	}
	if entries[0].Name != "Newer Note" {
		t.Fatalf("ListEntries()[0].Name = %q, want %q", entries[0].Name, "Newer Note")
	}
	if entries[1].Name != "Older Note" {
		t.Fatalf("ListEntries()[1].Name = %q, want %q", entries[1].Name, "Older Note")
	}
}

// TestLoadIndexTruncatesLargeContent 验证 MEMORY.md 超出限制时会按源码仓库风格截断。
func TestLoadIndexTruncatesLargeContent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	store := NewStore(root)

	if err := os.MkdirAll(store.MemoryDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	lines := make([]string, 0, maxIndexLines+25)
	for index := 0; index < maxIndexLines+25; index++ {
		lines = append(lines, "- entry "+strings.Repeat("x", 120))
	}
	indexPath := filepath.Join(store.MemoryDir(), "MEMORY.md")
	if err := os.WriteFile(indexPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	index, err := store.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex() error = %v", err)
	}
	if !strings.Contains(index, "[... truncated, too many memory entries ...]") && !strings.Contains(index, "[... truncated, index too large ...]") {
		t.Fatalf("LoadIndex() should truncate large index, got: %s", index)
	}
}
