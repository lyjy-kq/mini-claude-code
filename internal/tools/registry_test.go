// Package tools 验证工具层在写入 memory 目录时会自动刷新 MEMORY.md 索引。
// 这些测试锁住与源码仓库对齐后的联动行为，避免后续迁移时出现“记忆文件更新了但索引没跟上”的回退。
package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"mini-claude-code/internal/memory"
)

// TestWriteFileRefreshesMemoryIndex 验证写入新的记忆 markdown 文件后会自动生成对应索引条目。
func TestWriteFileRefreshesMemoryIndex(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(root, nil)
	store := memory.NewStore(root)

	target := filepath.Join(store.MemoryDir(), "project_cli_notes.md")
	content := strings.Join([]string{
		"---",
		"name: CLI Notes",
		"type: project",
		"description: Captured by write_file",
		"---",
		"Remember to keep startup text aligned.",
	}, "\n")

	result := registry.writeFile(target, content)
	if result.Error != nil {
		t.Fatalf("writeFile() error = %v", result.Error)
	}

	index, err := store.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex() error = %v", err)
	}
	if !strings.Contains(index, "**[CLI Notes](project_cli_notes.md)** (project) - Captured by write_file") {
		t.Fatalf("LoadIndex() missing new memory entry: %s", index)
	}
}

// TestEditFileRefreshesMemoryIndex 验证编辑记忆文件 frontmatter 后，索引中的摘要也会同步更新。
func TestEditFileRefreshesMemoryIndex(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(root, nil)
	store := memory.NewStore(root)

	if err := store.Save(memory.Entry{
		Name:        "Deploy Notes",
		Type:        "project",
		Description: "Initial description",
		Content:     "Initial content",
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	target := filepath.Join(store.MemoryDir(), "project_deploy_notes.md")
	result := registry.editFile(target, "description: Initial description", "description: Updated after edit_file")
	if result.Error != nil {
		t.Fatalf("editFile() error = %v", result.Error)
	}

	index, err := store.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex() error = %v", err)
	}
	if !strings.Contains(index, "**[Deploy Notes](project_deploy_notes.md)** (project) - Updated after edit_file") {
		t.Fatalf("LoadIndex() did not refresh edited description: %s", index)
	}
}

// TestWriteFileOutsideMemoryDoesNotCreateIndex 验证普通目录写入不会凭空触发 memory 索引副作用。
func TestWriteFileOutsideMemoryDoesNotCreateIndex(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(root, nil)
	store := memory.NewStore(root)

	target := filepath.Join(root, "notes", "plain.md")
	result := registry.writeFile(target, "plain content")
	if result.Error != nil {
		t.Fatalf("writeFile() error = %v", result.Error)
	}

	index, err := store.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex() error = %v", err)
	}
	if strings.TrimSpace(index) != "" {
		t.Fatalf("LoadIndex() = %q, want empty index for non-memory write", index)
	}
}
