// Package agent 负责验证 provider 选择与 session 恢复的一致性。
// 本测试文件聚焦单一 provider 真相，防止建 client、session 落盘和恢复逻辑再次分叉。
package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mini-claude-code/internal/api"
	"mini-claude-code/internal/config"
	"mini-claude-code/internal/session"
	"mini-claude-code/internal/tools"
)

// TestResolveConfiguredProviderPrefersOpenAI 验证配置解析优先级与模型客户端建链保持一致。
// 当两个 provider 配置同时存在时，这里要固定返回 OpenAI，避免运行期和持久化记录互相打架。
func TestResolveConfiguredProviderPrefersOpenAI(t *testing.T) {
	runtimeConfig := config.Config{
		OpenAIAPIKey:     "openai-key",
		OpenAIBaseURL:    "https://openai.example.com",
		AnthropicAPIKey:  "anthropic-key",
		AnthropicBaseURL: "https://anthropic.example.com",
	}

	if got := resolveConfiguredProvider(runtimeConfig); got != "openai" {
		t.Fatalf("resolveConfiguredProvider() = %q, want %q", got, "openai")
	}
}

// TestRestoreSessionEntrySwitchesActiveProvider 验证恢复 session 时会同步切换运行态 provider。
// 这样旧会话恢复后，后续 API 调用和 provider-specific 分支都会沿用归档里的后端语义继续执行。
func TestRestoreSessionEntrySwitchesActiveProvider(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:     "openai-key",
			OpenAIBaseURL:    "https://openai.example.com",
			AnthropicAPIKey:  "anthropic-key",
			AnthropicBaseURL: "https://anthropic.example.com",
		},
	})

	// 先确认新建会话沿用统一配置解析结果，避免测试本身建立在错误初始状态上。
	if agent.currentProvider() != "openai" {
		t.Fatalf("initial currentProvider() = %q, want %q", agent.currentProvider(), "openai")
	}

	entry := session.Entry{
		Metadata: session.Metadata{
			ID:        "resume-anthropic",
			Model:     "claude-sonnet-4-6",
			Provider:  "anthropic",
			StartTime: time.Unix(100, 0),
		},
		Timestamp: time.Unix(120, 0),
		Model:     "claude-sonnet-4-6",
		Response:  "restored",
	}

	agent.restoreSessionEntry(entry)

	if agent.currentProvider() != "anthropic" {
		t.Fatalf("restored currentProvider() = %q, want %q", agent.currentProvider(), "anthropic")
	}
	if agent.activeProvider != "anthropic" {
		t.Fatalf("activeProvider = %q, want %q", agent.activeProvider, "anthropic")
	}
	if agent.modelClient == nil {
		t.Fatal("modelClient should be rebuilt during restore")
	}
}

// TestRefreshProviderSnapshotSyncsNativeState 验证统一消息主链刷新快照时会同步 native provider 历史。
// 这样后续 Anthropic/OpenAI provider-specific 分支读取到的就是当前运行态原生结构，而不是陈旧副本。
func TestRefreshProviderSnapshotSyncsNativeState(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	agent.messages = []api.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	}
	agent.refreshProviderSnapshot()

	if len(agent.openAINativeMessages) != len(agent.providerSnapshot.OpenAI) {
		t.Fatalf("openAINativeMessages = %d, providerSnapshot.OpenAI = %d", len(agent.openAINativeMessages), len(agent.providerSnapshot.OpenAI))
	}
	if len(agent.anthropicNativeMessages) != len(agent.providerSnapshot.AnthropicMessages) {
		t.Fatalf(
			"anthropicNativeMessages = %d, providerSnapshot.AnthropicMessages = %d",
			len(agent.anthropicNativeMessages),
			len(agent.providerSnapshot.AnthropicMessages),
		)
	}
	if agent.anthropicSystemPrompt != agent.providerSnapshot.AnthropicSystem {
		t.Fatalf("anthropicSystemPrompt = %q, providerSnapshot.AnthropicSystem = %q", agent.anthropicSystemPrompt, agent.providerSnapshot.AnthropicSystem)
	}
}

// TestRestoreSessionEntryKeepsAnthropicNativeSnapshot 验证仅靠 provider snapshot 恢复时会保留 Anthropic 原生历史。
// 这能避免 resume 后立刻把 provider-native 结构再次降级成统一主链的二次投影。
func TestRestoreSessionEntryKeepsAnthropicNativeSnapshot(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "claude-sonnet-4-6",
		RuntimeConfig: config.Config{
			AnthropicAPIKey:  "anthropic-key",
			AnthropicBaseURL: "https://anthropic.example.com",
		},
	})

	entry := session.Entry{
		Metadata: session.Metadata{
			ID:        "native-anthropic-resume",
			Model:     "claude-sonnet-4-6",
			Provider:  "anthropic",
			StartTime: time.Unix(100, 0),
		},
		Timestamp: time.Unix(120, 0),
		Model:     "claude-sonnet-4-6",
		ProviderMessages: api.ProviderMessageSnapshot{
			AnthropicSystem: "system",
			AnthropicMessages: []map[string]any{
				{
					"role": "user",
					"content": []any{
						map[string]any{"type": "text", "text": "hello"},
					},
				},
			},
		},
	}

	agent.restoreSessionEntry(entry)

	if agent.currentProvider() != "anthropic" {
		t.Fatalf("currentProvider() = %q, want %q", agent.currentProvider(), "anthropic")
	}
	if agent.anthropicSystemPrompt != "system" {
		t.Fatalf("anthropicSystemPrompt = %q, want %q", agent.anthropicSystemPrompt, "system")
	}
	if len(agent.anthropicNativeMessages) != 1 {
		t.Fatalf("len(anthropicNativeMessages) = %d, want %d", len(agent.anthropicNativeMessages), 1)
	}
	if len(agent.providerSnapshot.AnthropicMessages) != 1 {
		t.Fatalf("len(providerSnapshot.AnthropicMessages) = %d, want %d", len(agent.providerSnapshot.AnthropicMessages), 1)
	}
}

// TestCurrentProviderSnapshotUsesNativeState 验证会话落盘前会优先读取当前 native provider 历史。
// 这样恢复后保留下来的原生结构不会在下一次保存时又被旧投影覆盖掉。
func TestCurrentProviderSnapshotUsesNativeState(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "claude-sonnet-4-6",
		PermissionMode:   tools.PermissionBypass,
		RuntimeConfig: config.Config{
			AnthropicAPIKey:  "anthropic-key",
			AnthropicBaseURL: "https://anthropic.example.com",
		},
	})

	// 这里故意制造“providerSnapshot 已过期、native 运行态已更新”的场景，
	// 确认落盘时使用的是 native 状态，而不是直接把旧 snapshot 原样写回。
	agent.providerSnapshot = api.ProviderMessageSnapshot{
		AnthropicSystem: "stale-system",
		AnthropicMessages: []map[string]any{
			{"role": "user", "content": []any{map[string]any{"type": "text", "text": "stale"}}},
		},
	}
	agent.anthropicSystemPrompt = "native-system"
	agent.anthropicNativeMessages = []map[string]any{
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": "fresh"}}},
	}

	snapshot := agent.currentProviderSnapshot()

	if snapshot.AnthropicSystem != "native-system" {
		t.Fatalf("snapshot.AnthropicSystem = %q, want %q", snapshot.AnthropicSystem, "native-system")
	}
	if len(snapshot.AnthropicMessages) != 1 {
		t.Fatalf("len(snapshot.AnthropicMessages) = %d, want %d", len(snapshot.AnthropicMessages), 1)
	}
	contentBlocks, ok := snapshot.AnthropicMessages[0]["content"].([]any)
	if !ok || len(contentBlocks) != 1 {
		t.Fatalf("snapshot.AnthropicMessages[0].content malformed: %#v", snapshot.AnthropicMessages[0]["content"])
	}
	firstBlock, ok := contentBlocks[0].(map[string]any)
	if !ok {
		t.Fatalf("snapshot.AnthropicMessages[0].content[0] malformed: %#v", contentBlocks[0])
	}
	if firstBlock["text"] != "fresh" {
		t.Fatalf("snapshot native text = %#v, want %#v", firstBlock["text"], "fresh")
	}
}

// TestFindAnthropicToolCallByIDUsesNativeMessages 验证 Anthropic tool_use 反查优先读取 native 运行态。
// 这样 providerSnapshot 过期时，压缩链仍能基于当前会话真实维护的 tool_use 元数据工作。
func TestFindAnthropicToolCallByIDUsesNativeMessages(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{WorkingDirectory: root, Model: "claude-sonnet-4-6"})

	agent.providerSnapshot = api.ProviderMessageSnapshot{
		AnthropicMessages: []map[string]any{
			{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type": "tool_use",
						"id":   "stale-call",
						"name": "read_file",
						"input": map[string]any{
							"file_path": "stale.txt",
						},
					},
				},
			},
		},
	}
	agent.anthropicNativeMessages = []map[string]any{
		{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "tool_use",
					"id":   "native-call",
					"name": "read_file",
					"input": map[string]any{
						"file_path": "fresh.txt",
					},
				},
			},
		},
	}

	call, ok := agent.findAnthropicToolCallByID("native-call")
	if !ok {
		t.Fatal("findAnthropicToolCallByID() should find native tool_use")
	}
	if call.Arguments["file_path"] != "fresh.txt" {
		t.Fatalf("call.Arguments[file_path] = %q, want %q", call.Arguments["file_path"], "fresh.txt")
	}
}

// TestCollectAnthropicSnipCandidatesUsesNativeMessages 验证 Anthropic snip 候选收集优先读取 native 运行态。
// 这样同一路径 read_file 的旧结果裁剪顺序会跟随当前会话真实原生历史，而不是受旧 snapshot 干扰。
func TestCollectAnthropicSnipCandidatesUsesNativeMessages(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{WorkingDirectory: root, Model: "claude-sonnet-4-6"})

	agent.messages = []api.Message{
		{Role: "tool", ToolCallID: "native-call", Content: "file content"},
	}
	agent.providerSnapshot = api.ProviderMessageSnapshot{
		AnthropicMessages: []map[string]any{
			{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "tool_use", "id": "stale-call", "name": "read_file", "input": map[string]any{"file_path": "stale.txt"}},
				},
			},
			{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "stale-call", "content": "stale"},
				},
			},
		},
	}
	agent.anthropicNativeMessages = []map[string]any{
		{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "tool_use", "id": "native-call", "name": "read_file", "input": map[string]any{"file_path": "fresh.txt"}},
			},
		},
		{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "native-call", "content": "fresh"},
			},
		},
	}

	candidates := agent.collectAnthropicSnipCandidatesFromSnapshot()
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want %d", len(candidates), 1)
	}
	if candidates[0].FilePath != "fresh.txt" {
		t.Fatalf("candidate.FilePath = %q, want %q", candidates[0].FilePath, "fresh.txt")
	}
}

// TestCollectAnthropicToolResultIndexesUsesNativeMessages 验证 Anthropic tool_result 顺序清理优先读取 native 运行态。
// 这样 microcompact 不会再因为 snapshot 副本滞后而清错 tool 结果顺序。
func TestCollectAnthropicToolResultIndexesUsesNativeMessages(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{WorkingDirectory: root, Model: "claude-sonnet-4-6"})

	agent.messages = []api.Message{
		{Role: "tool", ToolCallID: "native-call", Content: "fresh"},
	}
	agent.providerSnapshot = api.ProviderMessageSnapshot{
		AnthropicMessages: []map[string]any{
			{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "stale-call", "content": "stale"},
				},
			},
		},
	}
	agent.anthropicNativeMessages = []map[string]any{
		{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "native-call", "content": "fresh"},
			},
		},
	}

	indexes := agent.collectAnthropicToolResultIndexesFromSnapshot()
	if len(indexes) != 1 {
		t.Fatalf("len(indexes) = %d, want %d", len(indexes), 1)
	}
	if indexes[0] != 0 {
		t.Fatalf("indexes[0] = %d, want %d", indexes[0], 0)
	}
}

// TestDescribeProviderSnapshotUsesCurrentNativeState 验证 provider 快照摘要展示基于当前 native 运行态生成。
// 这样恢复后的状态说明不会再被过期 snapshot 误导，展示内容与真实会话后端历史保持一致。
func TestDescribeProviderSnapshotUsesCurrentNativeState(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{WorkingDirectory: root, Model: "claude-sonnet-4-6"})

	agent.providerSnapshot = api.ProviderMessageSnapshot{
		OpenAI: []map[string]any{
			{"role": "system", "content": "stale"},
		},
	}
	agent.anthropicSystemPrompt = "native-system"
	agent.anthropicNativeMessages = []map[string]any{
		{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "fresh"},
			},
		},
	}
	agent.openAINativeMessages = nil

	summary := agent.describeProviderSnapshot(agent.currentProviderSnapshot())
	expected := "openai=0 anthropic_messages=1 anthropic_system=true"
	if summary != expected {
		t.Fatalf("summary = %q, want %q", summary, expected)
	}
}

// TestAppendMessageSyncsNativeProviderState 验证统一消息追加时会同步维护 native provider 运行态。
// 这样高频 user/assistant 追加路径不必再完全依赖整条历史重投影才能让 native 状态追平。
func TestAppendMessageSyncsNativeProviderState(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{WorkingDirectory: root, Model: "claude-sonnet-4-6"})

	initialOpenAI := len(agent.openAINativeMessages)
	initialAnthropic := len(agent.anthropicNativeMessages)

	agent.appendMessage(api.Message{
		Role:    "assistant",
		Content: "hello from assistant",
		ToolCalls: []api.ToolCall{
			{
				ID:   "tool-1",
				Name: "read_file",
				Arguments: map[string]string{
					"file_path": "demo.txt",
				},
			},
		},
	})

	if len(agent.messages) != 2 {
		t.Fatalf("len(messages) = %d, want %d", len(agent.messages), 2)
	}
	if len(agent.openAINativeMessages) <= initialOpenAI {
		t.Fatalf("openAINativeMessages should grow, before=%d after=%d", initialOpenAI, len(agent.openAINativeMessages))
	}
	if len(agent.anthropicNativeMessages) <= initialAnthropic {
		t.Fatalf("anthropicNativeMessages should grow, before=%d after=%d", initialAnthropic, len(agent.anthropicNativeMessages))
	}
	if len(agent.providerSnapshot.OpenAI) != len(agent.openAINativeMessages) {
		t.Fatalf("providerSnapshot.OpenAI = %d, openAINativeMessages = %d", len(agent.providerSnapshot.OpenAI), len(agent.openAINativeMessages))
	}
}

// TestAppendToolResultMessageUsesAppendMessageFlow 验证 tool 结果回写会沿用统一的 native 增量维护入口。
// 这样 tool_result 这条高频路径也能直接推进行为对齐，而不是每次都靠全量刷新追平。
func TestAppendToolResultMessageUsesAppendMessageFlow(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{WorkingDirectory: root, Model: "claude-sonnet-4-6"})

	initialAnthropic := len(agent.anthropicNativeMessages)
	agent.appendToolResultMessage(api.ToolCall{
		ID:   "tool-2",
		Name: "read_file",
		Arguments: map[string]string{
			"file_path": "demo.txt",
		},
	}, tools.Result{Output: "tool output"})

	if len(agent.messages) != 2 {
		t.Fatalf("len(messages) = %d, want %d", len(agent.messages), 2)
	}
	if agent.messages[1].Role != "tool" {
		t.Fatalf("messages[1].Role = %q, want %q", agent.messages[1].Role, "tool")
	}
	if len(agent.anthropicNativeMessages) <= initialAnthropic {
		t.Fatalf("anthropicNativeMessages should grow, before=%d after=%d", initialAnthropic, len(agent.anthropicNativeMessages))
	}
}

// TestAppendMemoryInjectionSyncsNativeState 验证记忆注入原位改写 user 消息后会同步 native provider 运行态。
// 这样 memory injection 不再只是改统一主链文本，而是会把 provider-native 历史一并追平。
func TestAppendMemoryInjectionSyncsNativeState(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{WorkingDirectory: root, Model: "claude-sonnet-4-6"})

	agent.messages = []api.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
	}
	agent.refreshProviderSnapshot()

	agent.appendMemoryInjection(1, "memory note")

	if agent.messages[1].Content != "hello\n\nmemory note" {
		t.Fatalf("messages[1].Content = %q, want %q", agent.messages[1].Content, "hello\n\nmemory note")
	}
	if len(agent.providerSnapshot.AnthropicMessages) == 0 {
		t.Fatal("providerSnapshot.AnthropicMessages should not be empty after memory injection")
	}
}

// TestRebuildSystemPromptSyncsNativeSystem 验证 system prompt 重建后会同步 native provider 的 system 状态。
// 这样 system 边界变化不会只停留在统一主链里，而是会反映到当前原生运行态。
func TestRebuildSystemPromptSyncsNativeSystem(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{WorkingDirectory: root, Model: "claude-sonnet-4-6"})

	agent.systemPrompt = "updated system prompt"
	agent.messages = []api.Message{
		{Role: "system", Content: "old system"},
		{Role: "user", Content: "hello"},
	}

	agent.rebuildSystemPrompt()

	if agent.messages[0].Content != agent.systemPrompt {
		t.Fatalf("messages[0].Content = %q, want %q", agent.messages[0].Content, agent.systemPrompt)
	}
	if agent.providerSnapshot.OpenAI[0]["content"] != agent.systemPrompt {
		t.Fatalf("providerSnapshot.OpenAI[0][content] = %#v, want %#v", agent.providerSnapshot.OpenAI[0]["content"], agent.systemPrompt)
	}
}

// TestEnsureReadBeforeWriteRequiresPriorRead 验证写入和编辑现有文件前必须先读取文件。
// 这里锁定与源码仓库对齐后的用户提示语，避免后续回退成更模糊的本地错误文本。
func TestEnsureReadBeforeWriteRequiresPriorRead(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "demo.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	agent := New(Options{WorkingDirectory: root, Model: "gpt-4o"})

	writeErr := agent.ensureReadBeforeWrite(tools.Invocation{
		Name: "write_file",
		Arguments: map[string]string{
			"file_path": target,
		},
	})
	if writeErr == nil {
		t.Fatal("ensureReadBeforeWrite() should require prior read for write_file")
	}
	if got := writeErr.Error(); got != "You must read this file before writing. Use read_file first to see its current contents." {
		t.Fatalf("write error = %q", got)
	}

	editErr := agent.ensureReadBeforeWrite(tools.Invocation{
		Name: "edit_file",
		Arguments: map[string]string{
			"file_path": target,
		},
	})
	if editErr == nil {
		t.Fatal("ensureReadBeforeWrite() should require prior read for edit_file")
	}
	if got := editErr.Error(); got != "You must read this file before editing. Use read_file first to see its current contents." {
		t.Fatalf("edit error = %q", got)
	}
}

// TestEnsureReadBeforeWriteWarnsOnExternalModification 验证读取后若文件被外部修改，会要求先重新读取。
// 这样 Go 版用户和模型看到的警告文本就能与源码仓库保持一致，不会误以为是普通写入失败。
func TestEnsureReadBeforeWriteWarnsOnExternalModification(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "demo.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	agent := New(Options{WorkingDirectory: root, Model: "gpt-4o"})
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		t.Fatalf("Abs() error = %v", err)
	}
	info, err := os.Stat(absoluteTarget)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	agent.readFileState[absoluteTarget] = info.ModTime()

	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(target, []byte("changed"), 0o644); err != nil {
		t.Fatalf("WriteFile() second error = %v", err)
	}

	writeErr := agent.ensureReadBeforeWrite(tools.Invocation{
		Name: "write_file",
		Arguments: map[string]string{
			"file_path": target,
		},
	})
	if writeErr == nil {
		t.Fatal("ensureReadBeforeWrite() should warn after external modification for write_file")
	}
	if got := writeErr.Error(); got != "Warning: "+target+" was modified externally since your last read. Please read_file again before writing." {
		t.Fatalf("write warning = %q", got)
	}

	editErr := agent.ensureReadBeforeWrite(tools.Invocation{
		Name: "edit_file",
		Arguments: map[string]string{
			"file_path": target,
		},
	})
	if editErr == nil {
		t.Fatal("ensureReadBeforeWrite() should warn after external modification for edit_file")
	}
	if got := editErr.Error(); got != "Warning: "+target+" was modified externally since your last read. Please read_file again before editing." {
		t.Fatalf("edit warning = %q", got)
	}
}
