// Package memory 提供本地文件型记忆系统。
// memory_format.go 负责记忆注入格式的构建、正文截断保护、
// 行数/缩进等纯文本辅助函数，确保记忆注入上下文时符合 prompt 窗口限制。
package memory

import (
	"fmt"
	"strings"
)

func FormatInjection(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}

	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		blockLines := []string{"<system-reminder>"}
		blockLines = append(blockLines, fmt.Sprintf("Memory: %s:", entry.Path))
		blockLines = append(blockLines, "")
		if strings.TrimSpace(entry.Description) != "" {
			blockLines = append(blockLines, "Summary: "+strings.TrimSpace(entry.Description))
			blockLines = append(blockLines, "")
		}
		if strings.TrimSpace(entry.Content) != "" {
			blockLines = append(blockLines, strings.TrimSpace(entry.Content))
		}
		blockLines = append(blockLines, "</system-reminder>")
		lines = append(lines, strings.Join(blockLines, "\n"))
	}
	return strings.TrimSpace(strings.Join(lines, "\n\n"))
}

func FormatRelevantInjection(memories []RelevantMemory) string {
	if len(memories) == 0 {
		return ""
	}

	lines := make([]string, 0, len(memories))
	for _, item := range memories {
		blockLines := []string{"<system-reminder>"}
		if header := strings.TrimSpace(item.HeaderText); header != "" {
			blockLines = append(blockLines, header)
			blockLines = append(blockLines, "")
		}
		if strings.TrimSpace(item.Entry.Content) != "" {
			blockLines = append(blockLines, strings.TrimSpace(item.Entry.Content))
		}
		blockLines = append(blockLines, "</system-reminder>")
		lines = append(lines, strings.Join(blockLines, "\n"))
	}
	return strings.TrimSpace(strings.Join(lines, "\n\n"))
}

// truncateByBytes 按字节截断内容，避免单条记忆正文占用过多上下文。
func truncateByBytes(input string, maxBytes int) string {
	trimmed := strings.TrimSpace(input)
	if len([]byte(trimmed)) <= maxBytes {
		return trimmed
	}

	runes := []rune(trimmed)
	builder := strings.Builder{}
	for _, r := range runes {
		next := builder.String() + string(r)
		if len([]byte(next)) > maxBytes {
			break
		}
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String()) + "\n\n[... truncated, memory file too large ...]"
}
// truncatePlainTextByBytes 按字节截断纯文本，但不自动附加记忆正文专用提示。
// 这样 prompt 索引截断和记忆正文截断可以使用不同的提示文案。
func truncatePlainTextByBytes(input string, maxBytes int) string {
	trimmed := strings.TrimSpace(input)
	if len([]byte(trimmed)) <= maxBytes {
		return trimmed
	}

	runes := []rune(trimmed)
	builder := strings.Builder{}
	for _, r := range runes {
		next := builder.String() + string(r)
		if len([]byte(next)) > maxBytes {
			break
		}
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String())
}
// firstNLines 返回文本前 n 行。
func firstNLines(input string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(input, "\n")
	if len(lines) <= n {
		return input
	}
	return strings.Join(lines[:n], "\n")
}
// indentLines 为多行文本统一增加缩进。
func indentLines(input string, prefix string) string {
	lines := strings.Split(input, "\n")
	for index := range lines {
		lines[index] = prefix + lines[index]
	}
	return strings.Join(lines, "\n")
}
// firstNonEmpty 返回第一个非空文本。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
