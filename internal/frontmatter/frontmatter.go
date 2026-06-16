// Package frontmatter 负责解析简单的 YAML 风格 frontmatter。
// 该能力会被 skills、subagent、memory 等模块复用，避免重复实现元数据读取逻辑。
package frontmatter

import "strings"

// Result 表示 frontmatter 解析结果。
type Result struct {
	// Meta 保存 frontmatter 中的键值对。
	Meta map[string]string
	// Body 保存 frontmatter 之后的正文内容。
	Body string
}

// Parse 解析文档中的 frontmatter 与正文。
func Parse(input string) Result {
	result := Result{
		Meta: map[string]string{},
		Body: strings.TrimSpace(input),
	}

	lines := strings.Split(input, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return result
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return result
	}

	for _, line := range lines[1:end] {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"'`)
		if key != "" {
			result.Meta[key] = value
		}
	}

	result.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return result
}
