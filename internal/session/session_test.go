// Package session 负责验证会话持久化与恢复的兼容行为。
// 本测试文件聚焦 provider 元信息的落盘与旧归档回填，避免恢复链因环境变化选错后端。
package session

import (
	"testing"
	"time"

	"mini-claude-code/internal/api"
)

// TestStoreSaveAndLoadPreservesProvider 验证 session 保存后会保留 metadata.provider。
// 这样恢复链可以优先沿用原会话后端，而不是完全依赖当前环境重新猜测。
func TestStoreSaveAndLoadPreservesProvider(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	// 这里构造最小会话归档，确认 provider 元信息在最新会话与归档文件之间都能 round-trip。
	entry := Entry{
		Metadata: Metadata{
			ID:               "session-provider-openai",
			Model:            "gpt-4o",
			Provider:         "openai",
			WorkingDirectory: root,
			StartTime:        time.Unix(100, 0),
		},
		Timestamp: time.Unix(120, 0),
		Model:     "gpt-4o",
		Messages: []api.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "hello"},
		},
	}

	if err := store.Save(entry); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := store.LoadLatest()
	if err != nil {
		t.Fatalf("LoadLatest() error = %v", err)
	}
	if loaded.Metadata.Provider != "openai" {
		t.Fatalf("LoadLatest() provider = %q, want %q", loaded.Metadata.Provider, "openai")
	}

	loadedByID, err := store.LoadByID(entry.Metadata.ID)
	if err != nil {
		t.Fatalf("LoadByID() error = %v", err)
	}
	if loadedByID.Metadata.Provider != "openai" {
		t.Fatalf("LoadByID() provider = %q, want %q", loadedByID.Metadata.Provider, "openai")
	}
}

// TestDecodeEntryBackfillsProviderFromSnapshot 验证旧 session 文件缺少 metadata.provider 时仍能回填 provider。
// 这保证向后兼容旧归档，同时让恢复逻辑可以继续从 provider-specific 快照中选对后端。
func TestDecodeEntryBackfillsProviderFromSnapshot(t *testing.T) {
	// 这里直接走 decodeEntry，模拟历史归档还没有 provider 字段的兼容恢复路径。
	raw := []byte(`{
  "metadata": {
    "id": "legacy-anthropic-session",
    "model": "claude-sonnet-4-6"
  },
  "timestamp": "2026-06-16T00:00:00Z",
  "model": "claude-sonnet-4-6",
  "provider_messages": {
    "anthropic_system": "system",
    "anthropic_messages": [
      {
        "role": "user",
        "content": [{"type":"text","text":"hi"}]
      }
    ]
  }
}`)

	entry, err := decodeEntry(raw)
	if err != nil {
		t.Fatalf("decodeEntry() error = %v", err)
	}
	if entry.Metadata.Provider != "anthropic" {
		t.Fatalf("decodeEntry() provider = %q, want %q", entry.Metadata.Provider, "anthropic")
	}
}
