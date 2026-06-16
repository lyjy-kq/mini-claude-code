// Package memory 提供本地文件型记忆系统。
// memory_search.go 负责语义搜索、头部扫描、模型侧查选择打分等搜索链路，
// 包含关键词打分和语义 JSON 解析，是记忆召回的核心搜索逻辑。
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Store) SearchRelevant(query string, limit int) ([]Entry, error) {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return nil, err
	}

	items, err := os.ReadDir(s.dir())
	if err != nil {
		return nil, err
	}

	type scoredEntry struct {
		// Entry 保存已解析的记忆数据。
		Entry Entry
		// Score 表示当前记忆对查询的相关性分数。
		Score int
	}

	terms := normalizeTerms(query)
	scored := make([]scoredEntry, 0, len(items))
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".md") || item.Name() == "MEMORY.md" {
			continue
		}

		fullPath := filepath.Join(s.dir(), item.Name())
		entry, readErr := s.loadEntry(fullPath)
		if readErr != nil {
			continue
		}

		score := scoreEntry(entry, terms)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredEntry{
			Entry: entry,
			Score: score,
		})
	}

	sort.Slice(scored, func(i int, j int) bool {
		if scored[i].Score == scored[j].Score {
			return scored[i].Entry.Path < scored[j].Entry.Path
		}
		return scored[i].Score > scored[j].Score
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}

	result := make([]Entry, 0, len(scored))
	for _, item := range scored {
		result = append(result, item.Entry)
	}
	return result, nil
}

// ScanHeaders 扫描记忆目录中的 frontmatter 头信息。
func (s *Store) ScanHeaders() ([]Header, error) {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return nil, err
	}

	items, err := os.ReadDir(s.dir())
	if err != nil {
		return nil, err
	}

	headers := make([]Header, 0, len(items))
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".md") || item.Name() == "MEMORY.md" {
			continue
		}

		fullPath := filepath.Join(s.dir(), item.Name())
		header, readErr := s.loadHeader(fullPath, item.Name())
		if readErr != nil {
			// 坏文件不阻塞主链，直接跳过，保持记忆系统“尽力而为”。
			continue
		}
		headers = append(headers, header)
	}

	sort.Slice(headers, func(i int, j int) bool {
		return headers[i].ModifiedAt.After(headers[j].ModifiedAt)
	})
	if len(headers) > maxSemanticCandidates {
		headers = headers[:maxSemanticCandidates]
	}
	return headers, nil
}

// SelectRelevantMemories 使用 side-query 对记忆头做一次语义选择。
func (s *Store) SelectRelevantMemories(query string, sideQuery SideQueryFn, alreadySurfaced map[string]struct{}) ([]RelevantMemory, error) {
	if strings.TrimSpace(query) == "" || sideQuery == nil {
		return nil, nil
	}

	headers, err := s.ScanHeaders()
	if err != nil || len(headers) == 0 {
		return nil, err
	}

	// 先排除当前会话已经浮现过的记忆，减少重复注入和无效 side-query token 消耗。
	candidates := make([]Header, 0, len(headers))
	for _, header := range headers {
		if _, exists := alreadySurfaced[header.Path]; exists {
			continue
		}
		candidates = append(candidates, header)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	manifest := FormatMemoryManifest(candidates)
	text, queryErr := sideQuery(selectMemoriesPrompt, fmt.Sprintf("Query: %s\n\nAvailable memories:\n%s", strings.TrimSpace(query), manifest))
	if queryErr != nil {
		return nil, queryErr
	}

	selectedNames, parseErr := parseSelectedMemoryFilenames(text)
	if parseErr != nil || len(selectedNames) == 0 {
		return nil, parseErr
	}

	nameSet := make(map[string]struct{}, len(selectedNames))
	for _, name := range selectedNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		nameSet[trimmed] = struct{}{}
	}
	if len(nameSet) == 0 {
		return nil, nil
	}

	selected := make([]RelevantMemory, 0, maxSelectedMemories)
	for _, header := range candidates {
		if len(selected) >= maxSelectedMemories {
			break
		}
		if _, ok := nameSet[header.Filename]; !ok {
			continue
		}

		entry, readErr := s.loadEntry(header.Path)
		if readErr != nil {
			continue
		}
		entry.Content = truncateByBytes(entry.Content, maxMemoryBytesPerFile)
		selected = append(selected, RelevantMemory{
			Entry:      entry,
			HeaderText: buildRelevantMemoryHeader(header),
		})
	}
	return selected, nil
}

func FormatMemoryManifest(headers []Header) string {
	lines := make([]string, 0, len(headers))
	for _, header := range headers {
		typeTag := ""
		if strings.TrimSpace(header.Type) != "" {
			typeTag = "[" + strings.TrimSpace(header.Type) + "] "
		}

		timestamp := header.ModifiedAt.UTC().Format(time.RFC3339)
		line := fmt.Sprintf("- %s%s (%s)", typeTag, header.Filename, timestamp)
		if strings.TrimSpace(header.Description) != "" {
			line += ": " + strings.TrimSpace(header.Description)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func parseSelectedMemoryFilenames(input string) ([]string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("memory selector returned no JSON object")
	}

	var parsed semanticSelectionResult
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &parsed); err != nil {
		return nil, err
	}
	return parsed.SelectedMemories, nil
}

func scoreEntry(entry Entry, terms []string) int {
	if len(terms) == 0 {
		return 0
	}

	name := strings.ToLower(entry.Name)
	entryType := strings.ToLower(entry.Type)
	description := strings.ToLower(entry.Description)
	content := strings.ToLower(entry.Content)

	score := 0
	for _, term := range terms {
		if term == "" {
			continue
		}
		if strings.Contains(name, term) {
			score += 5
		}
		if strings.Contains(entryType, term) {
			score += 3
		}
		if strings.Contains(description, term) {
			score += 4
		}
		if strings.Contains(content, term) {
			score += 2
		}
	}
	return score
}
// normalizeTerms 对查询文本做去重和清洗，避免噪声词干扰排序。
func normalizeTerms(input string) []string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(input)))
	seen := map[string]struct{}{}
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, ".,!?;:\"'`()[]{}<>")
		if len(field) < 2 {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	return result
}
