// Package memory 提供本地文件型记忆系统。
// 本文件负责统一记忆的持久化目录、索引生成、轻量头部扫描、语义召回候选构造和注入格式，
// 让 Go 版在 memory 主链体验上尽量贴近 `claude-code-from-scratch` 的行为。
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mini-claude-code/internal/frontmatter"
)

const (
	// maxSemanticCandidates 表示语义候选阶段最多暴露给模型的记忆头数量。
	maxSemanticCandidates = 200
	// maxSelectedMemories 表示单次最多注入的记忆条数。
	maxSelectedMemories = 5
	// maxMemoryBytesPerFile 表示单条记忆注入前允许保留的最大正文大小。
	maxMemoryBytesPerFile = 4096
	// maxIndexLines 表示 MEMORY.md 注入 prompt 前允许保留的最大行数。
	maxIndexLines = 200
	// maxIndexBytes 表示 MEMORY.md 注入 prompt 前允许保留的最大字节数。
	maxIndexBytes = 25000
)

var validTypes = map[string]struct{}{
	"user":      {},
	"feedback":  {},
	"project":   {},
	"reference": {},
}

// SideQueryFn 表示用同一模型做一次轻量侧查的函数签名。
// 它专门服务于记忆选择，避免额外引入其它模型或额外 provider 分支。
type SideQueryFn func(systemPrompt string, userMessage string) (string, error)

// Entry 表示一条完整记忆记录。
type Entry struct {
	// Path 表示当前记忆文件的绝对路径，便于去重和恢复。
	Path string
	// Name 表示记忆名称。
	Name string
	// Type 表示记忆类型。
	Type string
	// Description 表示记忆摘要。
	Description string
	// Content 表示记忆正文。
	Content string
}

// Header 表示用于语义选择的轻量记忆头信息。
type Header struct {
	// Filename 表示记忆文件名。
	Filename string
	// Path 表示记忆文件绝对路径。
	Path string
	// ModifiedAt 表示记忆文件最近修改时间。
	ModifiedAt time.Time
	// Description 表示 frontmatter 中的摘要字段。
	Description string
	// Type 表示 frontmatter 中的记忆类型字段。
	Type string
}

// RelevantMemory 表示语义选择后准备注入上下文的记忆片段。
type RelevantMemory struct {
	// Entry 表示完整记忆内容。
	Entry Entry
	// HeaderText 表示注入前追加的 freshness / 来源提示头。
	HeaderText string
}

// Store 表示记忆存储器。
type Store struct {
	// root 表示工作目录，用于生成项目级 hash 并定位记忆目录。
	root string
}

// semanticSelectionResult 表示 side-query 返回的最小 JSON 结构。
type semanticSelectionResult struct {
	// SelectedMemories 表示模型挑选出的候选文件名列表。
	SelectedMemories []string `json:"selected_memories"`
}

const selectMemoriesPrompt = `You are selecting memories that will be useful to an AI coding assistant as it processes a user's query. You will be given the user's query and a list of available memory files with their filenames and descriptions.

Return a JSON object with a "selected_memories" array of filenames for the memories that will clearly be useful (up to 5). Only include memories that you are certain will be helpful based on their name and description.
- If you are unsure if a memory will be useful, do not include it.
- If no memories would clearly be useful, return an empty array.`

// NewStore 创建记忆存储器。
func NewStore(root string) *Store {
	return &Store{root: root}
}

// MemoryDir 返回当前工作区对应的 memory 目录。
// 这里改成“用户家目录 + 项目 hash”的布局，以对齐源码仓库的持久化策略，
// 避免把长期记忆直接混在工作区里。
func (s *Store) MemoryDir() string {
	return s.dir()
}

// LoadIndex 读取 MEMORY.md，并在注入 prompt 前按行数和字节数做截断保护。
// 这样 prompt 层看到的索引就和源码仓库一致，不会让超大索引直接挤占上下文窗口。
func (s *Store) LoadIndex() (string, error) {
	indexPath := filepath.Join(s.dir(), "MEMORY.md")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) > maxIndexLines {
		content = strings.Join(lines[:maxIndexLines], "\n") + "\n\n[... truncated, too many memory entries ...]"
	}
	if len([]byte(content)) > maxIndexBytes {
		content = truncatePlainTextByBytes(content, maxIndexBytes) + "\n\n[... truncated, index too large ...]"
	}
	return strings.TrimSpace(content), nil
}

// Save 保存一条记忆并刷新索引。
func (s *Store) Save(entry Entry) error {
	normalizedType := normalizeMemoryType(entry.Type)
	target := filepath.Join(s.dir(), normalizedType+"_"+slug(entry.Name)+".md")
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}

	// 统一写成标准 frontmatter，保证后续扫描和完整读取共享同一份元数据来源。
	body := strings.Join([]string{
		"---",
		fmt.Sprintf("name: %s", entry.Name),
		fmt.Sprintf("type: %s", normalizedType),
		fmt.Sprintf("description: %s", entry.Description),
		"---",
		entry.Content,
	}, "\n")
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		return err
	}
	return s.updateIndex()
}

// RefreshIndex 显式重建 MEMORY.md 索引。
// 这个导出方法专门给工具层使用，让 write_file 和 edit_file 在直接维护记忆文件后，
// 也能和源码仓库一样自动同步索引，保证 /memory 展示与 prompt 注入看到的是最新状态。
func (s *Store) RefreshIndex() error {
	return s.updateIndex()
}

// List 列出记忆目录中的所有文件名。
func (s *Store) List() ([]string, error) {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir())
	if err != nil {
		return nil, err
	}

	result := make([]string, 0, len(entries))
	for _, item := range entries {
		if item.IsDir() {
			continue
		}
		result = append(result, item.Name())
	}
	return result, nil
}

// ListEntries 列出当前工作区可见的完整记忆条目。
// 这给 REPL 展示层提供比“仅文件名”更丰富的类型、名称和描述信息。
func (s *Store) ListEntries() ([]Entry, error) {
	files, err := s.List()
	if err != nil {
		return nil, err
	}

	type entryWithTime struct {
		// Entry 保存完整记忆条目，供展示层和索引层直接复用。
		Entry Entry
		// ModifiedAt 保存文件最近修改时间，用于按源码仓库语义做最新优先排序。
		ModifiedAt time.Time
	}

	entries := make([]entryWithTime, 0, len(files))
	for _, file := range files {
		if file == "MEMORY.md" || !strings.HasSuffix(strings.ToLower(file), ".md") {
			continue
		}

		fullPath := filepath.Join(s.dir(), file)
		entry, loadErr := s.loadEntry(fullPath)
		if loadErr != nil {
			continue
		}

		// 这里显式读取 mtime，是为了让 `/memory` 展示顺序和源码仓库保持一致：
		// 最近修改的记忆应当优先出现，而不是按路径字典序漂移。
		modifiedAt := time.Time{}
		if stat, statErr := os.Stat(fullPath); statErr == nil {
			modifiedAt = stat.ModTime()
		}
		entries = append(entries, entryWithTime{
			Entry:      entry,
			ModifiedAt: modifiedAt,
		})
	}

	sort.Slice(entries, func(i int, j int) bool {
		if entries[i].ModifiedAt.Equal(entries[j].ModifiedAt) {
			return entries[i].Entry.Path < entries[j].Entry.Path
		}
		return entries[i].ModifiedAt.After(entries[j].ModifiedAt)
	})

	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Entry)
	}
	return result, nil
}

// SearchRelevant 根据输入文本检索最相关的记忆。
func MemoryAge(modifiedAt time.Time) string {
	days := int(time.Since(modifiedAt).Hours() / 24)
	if days <= 0 {
		return "today"
	}
	if days == 1 {
		return "yesterday"
	}
	return fmt.Sprintf("%d days ago", days)
}

// MemoryFreshnessWarning 返回陈旧记忆提示。
func MemoryFreshnessWarning(modifiedAt time.Time) string {
	days := int(time.Since(modifiedAt).Hours() / 24)
	if days <= 1 {
		return ""
	}
	return fmt.Sprintf("This memory is %d days old. Memories are point-in-time observations, not live state - claims about code behavior may be outdated. Verify against current code before asserting as fact.", days)
}

func (s *Store) dir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Join(s.root, ".mini-claude", "memory")
	}
	return filepath.Join(homeDir, ".mini-claude", "projects", projectHash(s.root), "memory")
}

// updateIndex 重建 MEMORY.md 索引。
func (s *Store) updateIndex() error {
	entries, err := s.ListEntries()
	if err != nil {
		return err
	}

	lines := []string{"# Memory Index", ""}
	for _, entry := range entries {
		name := firstNonEmpty(strings.TrimSpace(entry.Name), strings.TrimSuffix(filepath.Base(entry.Path), filepath.Ext(entry.Path)))
		entryType := normalizeMemoryType(entry.Type)
		description := strings.TrimSpace(entry.Description)
		fileName := filepath.Base(entry.Path)

		line := fmt.Sprintf("- **[%s](%s)** (%s)", name, fileName, entryType)
		if description != "" {
			line += " - " + description
		}
		lines = append(lines, line)
	}
	return os.WriteFile(filepath.Join(s.dir(), "MEMORY.md"), []byte(strings.Join(lines, "\n")), 0o644)
}

// loadEntry 读取单个记忆文件，并兼容旧格式数据。
func (s *Store) loadEntry(path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}

	parsed := frontmatter.Parse(string(data))
	entry := Entry{
		Path:        path,
		Name:        parsed.Meta["name"],
		Type:        normalizeMemoryType(parsed.Meta["type"]),
		Description: parsed.Meta["description"],
		Content:     strings.TrimSpace(parsed.Body),
	}
	if strings.TrimSpace(entry.Name) == "" {
		entry.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return entry, nil
}

// loadHeader 只解析头部元数据，避免语义选择前把所有记忆全文读入内存。
func (s *Store) loadHeader(path string, filename string) (Header, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return Header{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Header{}, err
	}

	firstLines := firstNLines(string(data), 30)
	parsed := frontmatter.Parse(firstLines)
	return Header{
		Filename:    filename,
		Path:        path,
		ModifiedAt:  stat.ModTime(),
		Description: parsed.Meta["description"],
		Type:        normalizeMemoryType(parsed.Meta["type"]),
	}, nil
}

func slug(input string) string {
	trimmed := strings.ToLower(strings.TrimSpace(input))
	if trimmed == "" {
		return "memory"
	}

	var builder strings.Builder
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteRune('_')
		}
	}

	normalized := strings.Trim(builder.String(), "_")
	for strings.Contains(normalized, "__") {
		normalized = strings.ReplaceAll(normalized, "__", "_")
	}
	if normalized == "" {
		return "memory"
	}
	if len(normalized) > 40 {
		return normalized[:40]
	}
	return normalized
}

// normalizeMemoryType 把类型收敛成源码仓库支持的 4 种值，坏值时回退到 project。
func normalizeMemoryType(input string) string {
	trimmed := strings.ToLower(strings.TrimSpace(input))
	if _, ok := validTypes[trimmed]; ok {
		return trimmed
	}
	return "project"
}

// projectHash 生成当前工作区的稳定 hash，用于按项目隔离 memory 空间。
func projectHash(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])[:16]
}

// buildRelevantMemoryHeader ? RelevantMemory ????????
func buildRelevantMemoryHeader(header Header) string {
	age := MemoryAge(header.ModifiedAt)
	if age == "" {
		return "(" + header.Path + "):"
	}
	return "(" + age + " " + header.Path + "):"
}

