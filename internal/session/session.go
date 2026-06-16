// Package session 负责会话持久化。
// 本文件不仅保存消息历史，也保存恢复继续执行所需的最小运行态，
// 这样 resume 后的 Agent 能延续预算、压缩和记忆注入去重等行为。
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mini-claude-code/internal/api"
	"mini-claude-code/internal/contextx"
)

// Metadata 表示会话元信息。
type Metadata struct {
	// ID 表示会话唯一标识。
	ID string `json:"id"`
	// Model 表示本次会话使用的模型。
	Model string `json:"model"`
	// Provider 表示本次会话保存时使用的 provider 标识。
	// 显式落盘该字段，是为了让 resume 优先沿用原会话后端，而不是完全依赖当前环境推断。
	Provider string `json:"provider"`
	// WorkingDirectory 表示会话启动时的工作目录。
	WorkingDirectory string `json:"working_directory"`
	// StartTime 表示会话开始时间。
	StartTime time.Time `json:"start_time"`
	// MessageCount 表示当前消息数。
	MessageCount int `json:"message_count"`
}

// RuntimeState 表示恢复会话时需要带回的最小运行态。
type RuntimeState struct {
	// Turns 表示当前会话已经消耗的轮次数。
	Turns int `json:"turns"`
	// LastResponse 保存最近一次助手输出。
	LastResponse string `json:"last_response"`
	// TotalInputTokens 表示当前会话累计输入 token 数。
	TotalInputTokens int `json:"total_input_tokens"`
	// TotalOutputTokens 表示当前会话累计输出 token 数。
	TotalOutputTokens int `json:"total_output_tokens"`
	// LastInputTokenCount 表示最近一轮请求输入 token 数。
	LastInputTokenCount int `json:"last_input_token_count"`
	// EffectiveWindow 表示当前会话可用上下文窗口。
	EffectiveWindow int `json:"effective_window"`
	// ContextState 保存压缩策略阈值。
	ContextState contextx.State `json:"context_state"`
	// LastAPICallTime 表示最近一次模型调用完成时间。
	LastAPICallTime time.Time `json:"last_api_call_time"`
	// ReadFileState 保存读后写约束的文件读取时间戳。
	ReadFileState map[string]time.Time `json:"read_file_state"`
	// SurfacedMemoryPaths 保存本会话已注入过的记忆路径。
	SurfacedMemoryPaths []string `json:"surfaced_memory_paths"`
	// SessionMemoryBytes 表示本会话累计注入记忆体积。
	SessionMemoryBytes int `json:"session_memory_bytes"`
	// ContextCleared 表示当前运行态是否刚经历过清空上下文，正在等待首个执行结果重建新的 user 边界。
	// 这里显式持久化该标记，是为了让 clear-and-execute 这类流程在恢复会话后仍保持和源仓库一致的语义。
	ContextCleared bool `json:"context_cleared"`
}

// Entry 表示一次最小会话记录。
type Entry struct {
	// Metadata 表示会话元信息。
	Metadata Metadata `json:"metadata"`
	// Timestamp 表示最近一次记录时间。
	Timestamp time.Time `json:"timestamp"`
	// Model 表示本次会话使用的模型。
	Model string `json:"model"`
	// Prompt 表示最近一次用户输入。
	Prompt string `json:"prompt"`
	// Response 表示最近一次助手输出。
	Response string `json:"response"`
	// Messages 表示当前完整消息历史，用于恢复会话上下文。
	Messages []api.Message `json:"messages"`
	// ProviderMessages 保存 provider-specific 原生消息快照。
	// 当前 Go 主链仍以统一 `[]api.Message` 继续驱动，但这里先把 OpenAI/Anthropic
	// 的原生历史同步落盘，为后续更高保真的恢复与压缩迁移打基础。
	ProviderMessages api.ProviderMessageSnapshot `json:"provider_messages"`
	// Runtime 保存恢复继续执行所需的运行态快照。
	Runtime RuntimeState `json:"runtime"`
}

// Store 表示会话存储器。
type Store struct {
	// root 表示工作目录，用于生成本地会话文件位置。
	root string
}

// NewStore 创建新的会话存储器。
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Save 保存会话，并同时写入 latest 与按 id 的归档文件。
func (s *Store) Save(entry Entry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	if entry.Model != "" && strings.TrimSpace(entry.Metadata.Model) == "" {
		entry.Metadata.Model = entry.Model
	}
	if strings.TrimSpace(entry.Metadata.Provider) == "" {
		entry.Metadata.Provider = inferProviderFromSnapshot(entry.ProviderMessages)
	}
	if strings.TrimSpace(entry.Metadata.ID) == "" {
		entry.Metadata.ID = entry.Timestamp.Format("20060102-150405")
	}
	if entry.Metadata.StartTime.IsZero() {
		entry.Metadata.StartTime = entry.Timestamp
	}
	// 每次保存都用当前统一消息历史重新同步消息数，
	// 避免恢复后继续运行、compact 或 clear 之后沿用旧的 MessageCount 元数据。
	if len(entry.Messages) > 0 {
		entry.Metadata.MessageCount = len(entry.Messages)
	}
	if strings.TrimSpace(entry.Metadata.WorkingDirectory) == "" {
		entry.Metadata.WorkingDirectory = s.root
	}
	if entry.Runtime.ReadFileState == nil {
		entry.Runtime.ReadFileState = map[string]time.Time{}
	}
	if entry.Runtime.ContextState.EffectiveWindow == 0 {
		entry.Runtime.ContextState = contextx.Default()
	}

	if err := os.MkdirAll(s.sessionDir(), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(s.latestPath(), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(s.sessionPath(entry.Metadata.ID), data, 0o644)
}

// LoadLatest 读取最近一次会话记录。
func (s *Store) LoadLatest() (Entry, error) {
	data, err := os.ReadFile(s.latestPath())
	if err != nil {
		return Entry{}, err
	}
	return decodeEntry(data)
}

// LoadByID 根据会话 id 读取归档会话。
func (s *Store) LoadByID(id string) (Entry, error) {
	data, err := os.ReadFile(s.sessionPath(id))
	if err != nil {
		return Entry{}, err
	}
	return decodeEntry(data)
}

// List 返回已归档会话元信息列表，按开始时间倒序排列。
func (s *Store) List() ([]Metadata, error) {
	if err := os.MkdirAll(s.sessionDir(), 0o755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(s.sessionDir())
	if err != nil {
		return nil, err
	}

	result := make([]Metadata, 0, len(entries))
	for _, item := range entries {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".json") || item.Name() == "latest.json" {
			continue
		}

		data, readErr := os.ReadFile(filepath.Join(s.sessionDir(), item.Name()))
		if readErr != nil {
			continue
		}

		entry, decodeErr := decodeEntry(data)
		if decodeErr != nil {
			continue
		}
		result = append(result, entry.Metadata)
	}

	sort.Slice(result, func(left int, right int) bool {
		return result[left].StartTime.After(result[right].StartTime)
	})
	return result, nil
}

// LatestSessionID 返回最近归档会话的 id。
func (s *Store) LatestSessionID() (string, error) {
	sessions, err := s.List()
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", nil
	}
	return sessions[0].ID, nil
}

// Exists 返回最近会话文件是否存在。
func (s *Store) Exists() bool {
	_, err := os.Stat(s.latestPath())
	return err == nil
}

// ClearLatest 删除最近会话记录，但保留历史归档。
func (s *Store) ClearLatest() error {
	if _, err := os.Stat(s.latestPath()); err != nil {
		return nil
	}
	return os.Remove(s.latestPath())
}

// decodeEntry 统一处理会话 JSON 反序列化和向后兼容填充。
func decodeEntry(data []byte) (Entry, error) {
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, err
	}

	if strings.TrimSpace(entry.Metadata.ID) == "" {
		entry.Metadata.ID = entry.Timestamp.Format("20060102-150405")
	}
	if strings.TrimSpace(entry.Metadata.Model) == "" {
		entry.Metadata.Model = entry.Model
	}
	if strings.TrimSpace(entry.Metadata.Provider) == "" {
		entry.Metadata.Provider = inferProviderFromSnapshot(entry.ProviderMessages)
	}
	if entry.Metadata.StartTime.IsZero() {
		entry.Metadata.StartTime = entry.Timestamp
	}
	// 反序列化后同样优先用真实消息历史修正消息数，
	// 这样旧归档或手工编辑过的 session 文件也不会把过期计数带回运行态。
	if len(entry.Messages) > 0 {
		entry.Metadata.MessageCount = len(entry.Messages)
	}
	if entry.Runtime.ReadFileState == nil {
		entry.Runtime.ReadFileState = map[string]time.Time{}
	}
	if entry.Runtime.ContextState.EffectiveWindow == 0 {
		entry.Runtime.ContextState = contextx.Default()
	}
	return entry, nil
}

// sessionDir 返回会话归档目录。
func (s *Store) sessionDir() string {
	return filepath.Join(s.root, ".mini-claude", "session")
}

// latestPath 返回默认最近会话文件路径。
func (s *Store) latestPath() string {
	return filepath.Join(s.sessionDir(), "latest.json")
}

// sessionPath 返回指定会话 id 的归档文件路径。
func (s *Store) sessionPath(id string) string {
	return filepath.Join(s.sessionDir(), id+".json")
}

// inferProviderFromSnapshot 根据已保存的 provider-specific 快照推断 provider。
// 这是旧 session 文件缺少 metadata.provider 时的向后兼容兜底，避免恢复链直接退回空字符串。
func inferProviderFromSnapshot(snapshot api.ProviderMessageSnapshot) string {
	if len(snapshot.AnthropicMessages) > 0 || strings.TrimSpace(snapshot.AnthropicSystem) != "" {
		return "anthropic"
	}
	if len(snapshot.OpenAI) > 0 {
		return "openai"
	}
	return ""
}
