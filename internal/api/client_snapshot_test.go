package api

import "testing"

// TestBuildProviderMessageSnapshotRoundTripOpenAI 验证 OpenAI-compatible 快照可以往返恢复统一消息历史。
// 这能帮助我们锁住 provider-specific 持久化后的最小可恢复语义，避免后续迁移把 tool_calls 或 tool 结果丢掉。
func TestBuildProviderMessageSnapshotRoundTripOpenAI(t *testing.T) {
	original := []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "user question"},
		{
			Role:    "assistant",
			Content: "[Assistant issued tool calls.]",
			ToolCalls: []ToolCall{
				{
					ID:   "tool-1",
					Name: "read_file",
					Arguments: map[string]string{
						"file_path": "main.go",
					},
				},
			},
		},
		{
			Role:       "tool",
			Name:       "read_file",
			Content:    "1 | package main",
			ToolCallID: "tool-1",
		},
	}

	snapshot := BuildProviderMessageSnapshot(original)
	restored := BuildMessagesFromProviderSnapshot(snapshot, "openai")

	if len(restored) != len(original) {
		t.Fatalf("expected %d restored messages, got %d", len(original), len(restored))
	}
	if restored[0].Role != "system" || restored[0].Content != "system prompt" {
		t.Fatalf("unexpected restored system message: %#v", restored[0])
	}
	if len(restored[2].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool calls to survive round trip, got %#v", restored[2].ToolCalls)
	}
	if restored[2].ToolCalls[0].Name != "read_file" {
		t.Fatalf("unexpected restored tool call: %#v", restored[2].ToolCalls[0])
	}
	if restored[3].Role != "tool" || restored[3].ToolCallID != "tool-1" {
		t.Fatalf("unexpected restored tool message: %#v", restored[3])
	}
}

// TestBuildProviderMessageSnapshotRoundTripAnthropic 验证 Anthropic 快照可以恢复 tool_use / tool_result 语义。
// 这里重点保护 Anthropic provider-aware 压缩和 session 恢复后继续运行所依赖的最小消息结构。
func TestBuildProviderMessageSnapshotRoundTripAnthropic(t *testing.T) {
	original := []Message{
		{Role: "system", Content: "anthropic system"},
		{Role: "user", Content: "inspect this project"},
		{
			Role:    "assistant",
			Content: "[Assistant issued tool calls.]",
			ToolCalls: []ToolCall{
				{
					ID:   "tool-a",
					Name: "grep_search",
					Arguments: map[string]string{
						"pattern": "TODO",
						"path":    ".",
					},
				},
			},
		},
		{
			Role:       "tool",
			Name:       "grep_search",
			Content:    "file.go:10:// TODO",
			ToolCallID: "tool-a",
		},
		{Role: "user", Content: "continue"},
	}

	snapshot := BuildProviderMessageSnapshot(original)
	restored := BuildMessagesFromProviderSnapshot(snapshot, "anthropic")

	if len(restored) < 4 {
		t.Fatalf("expected at least 4 restored messages, got %d", len(restored))
	}
	if restored[0].Role != "system" || restored[0].Content != "anthropic system" {
		t.Fatalf("unexpected restored anthropic system message: %#v", restored[0])
	}
	if restored[2].Role != "assistant" || len(restored[2].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool_use message, got %#v", restored[2])
	}
	if restored[3].Role != "tool" || restored[3].ToolCallID != "tool-a" {
		t.Fatalf("expected tool_result message, got %#v", restored[3])
	}
}
