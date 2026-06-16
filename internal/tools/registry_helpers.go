// Package tools 中的 registry_helpers 负责提供工具系统中通用的辅助函数。
// 本文件包含文本清洗、引号归一化、字符串查找、diff 生成、glob 匹配、
// 目录跳过、结果裁剪以及危险命令检测等工具函数。
package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// htmlToText 把常见 HTML 内容清洗成更适合模型消费的纯文本。
func htmlToText(input string) string {
	text := regexp.MustCompile(`(?is)<script[\s\S]*?</script>`).ReplaceAllString(input, "")
	text = regexp.MustCompile(`(?is)<style[\s\S]*?</style>`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`(?is)<[^>]*>`).ReplaceAllString(text, " ")
	replacer := strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
	)
	text = replacer.Replace(text)
	text = regexp.MustCompile(`\s{2,}`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// normalizeQuotes 归一化弯引号与直引号，减少 edit_file 因复制文本差异造成的误失败。
func normalizeQuotes(input string) string {
	replacer := strings.NewReplacer(
		"\u2018", "'",
		"\u2019", "'",
		"\u2032", "'",
		"\u201c", `"`,
		"\u201d", `"`,
		"\u2033", `"`,
	)
	return replacer.Replace(input)
}

// findActualString 在文件中寻找真实匹配文本，并告诉调用方是否使用了引号归一化回退。
func findActualString(fileContent string, searchString string) (string, bool) {
	if strings.Contains(fileContent, searchString) {
		return searchString, false
	}

	normalizedFile := normalizeQuotes(fileContent)
	normalizedSearch := normalizeQuotes(searchString)
	if strings.Contains(normalizedFile, normalizedSearch) {
		index := strings.Index(normalizedFile, normalizedSearch)
		return fileContent[index : index+len(normalizedSearch)], true
	}
	return "", false
}

// generateDiff 生成统一格式的 diff 片段，便于模型直观看到改动前后的差异。
func generateDiff(text string, oldString string, newString string) string {
	beforeChange := strings.Split(text, oldString)[0]
	lineNum := strings.Count(beforeChange, "\n") + 1
	oldLines := strings.Split(oldString, "\n")
	newLines := strings.Split(newString, "\n")

	parts := []string{fmt.Sprintf("@@ -%d,%d +%d,%d @@", lineNum, len(oldLines), lineNum, len(newLines))}
	for _, line := range oldLines {
		parts = append(parts, "- "+line)
	}
	for _, line := range newLines {
		parts = append(parts, "+ "+line)
	}
	return strings.Join(parts, "\n")
}

// globMatcher 把常用 glob 模式转换为正则，用于 list_files 和 grep_search。
func globMatcher(pattern string) (*regexp.Regexp, error) {
	normalized := filepath.ToSlash(strings.TrimSpace(pattern))
	if normalized == "" {
		normalized = "**/*"
	}

	if normalized == "**/*" {
		return regexp.Compile(`^.+$`)
	}

	var builder strings.Builder
	builder.WriteString("^")
	for index := 0; index < len(normalized); index++ {
		current := normalized[index]

		switch current {
		case '*':
			if index+1 < len(normalized) && normalized[index+1] == '*' {
				builder.WriteString(".*")
				index++
			} else {
				builder.WriteString(`[^/]*`)
			}
		case '?':
			builder.WriteString(`.`)
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			builder.WriteString(`\`)
			builder.WriteByte(current)
		default:
			builder.WriteByte(current)
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

// shouldSkipDirectory 统一处理递归搜索时需要跳过的目录。
func shouldSkipDirectory(name string) bool {
	switch name {
	case ".git", "node_modules":
		return true
	default:
		return false
	}
}

// truncateResult 负责裁剪超长工具结果，避免上下文被大块输出吞掉。
func truncateResult(result string) string {
	if len(result) <= maxResultChars {
		return result
	}

	keepEach := (maxResultChars - 60) / 2
	return result[:keepEach] +
		fmt.Sprintf("\n\n[... truncated %d chars ...]\n\n", len(result)-keepEach*2) +
		result[len(result)-keepEach:]
}

// IsDangerousCommand 返回命令是否命中内置危险模式。
func IsDangerousCommand(command string) bool {
	for _, pattern := range dangerousShellPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}
	return false
}
