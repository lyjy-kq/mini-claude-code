// Package tools 中的 registry_exec 负责工具执行逻辑的分发与实现。
// 本文件包含 Execute 主分发函数，以及文件读写、搜索、Shell、技能、
// 网页抓取等各类工具的具体执行函数。
package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"mini-claude-code/internal/memory"
)

// Execute 根据调用名称分发到对应的本地实现。
func (r *Registry) Execute(ctx context.Context, call Invocation) Result {
	if r.mcpManager != nil && r.mcpManager.IsMCPToolName(call.Name) {
		// MCP 工具不走本地 switch，而是转发到对应的 stdio JSON-RPC server。
		// 这样可以在不改写 Agent 主循环的前提下，把远端工具平滑并入统一执行入口。
		output, err := r.mcpManager.CallTool(ctx, call.Name, call.Arguments)
		if err != nil {
			return Result{Error: err}
		}
		return Result{Output: truncateResult(output)}
	}

	switch call.Name {
	case "read_file":
		return r.readFile(call.Arguments["file_path"])
	case "write_file":
		return r.writeFile(call.Arguments["file_path"], call.Arguments["content"])
	case "edit_file":
		return r.editFile(call.Arguments["file_path"], call.Arguments["old_string"], call.Arguments["new_string"])
	case "list_files":
		return r.listFiles(call.Arguments["path"], call.Arguments["pattern"])
	case "grep_search":
		return r.grepSearch(call.Arguments["path"], call.Arguments["pattern"], call.Arguments["include"])
	case "run_shell":
		return r.runShell(ctx, call.Arguments["command"], call.Arguments["timeout"])
	case "skill":
		return r.resolveSkill(call.Arguments["skill_name"], call.Arguments["args"])
	case "web_fetch":
		return r.webFetch(ctx, call.Arguments["url"], call.Arguments["max_length"])
	default:
		return Result{Error: fmt.Errorf("unknown tool: %s", call.Name)}
	}
}

// resolvePath 负责把传入路径转换为工作目录下的绝对路径。
func (r *Registry) resolvePath(input string) string {
	if input == "" {
		return r.workingDirectory
	}
	if filepath.IsAbs(input) {
		return input
	}
	return filepath.Join(r.workingDirectory, input)
}

// readFile 读取文件内容，并带上行号返回。
func (r *Registry) readFile(path string) Result {
	content, err := os.ReadFile(r.resolvePath(path))
	if err != nil {
		return Result{Error: err}
	}

	lines := strings.Split(string(content), "\n")
	numbered := make([]string, 0, len(lines))
	for index, line := range lines {
		numbered = append(numbered, fmt.Sprintf("%4d | %s", index+1, line))
	}
	return Result{Output: strings.Join(numbered, "\n")}
}

// writeFile 写入文件内容，并返回前几行预览。
func (r *Registry) writeFile(path string, content string) Result {
	target := r.resolvePath(path)

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Result{Error: err}
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return Result{Error: err}
	}
	if err := r.refreshMemoryIndexIfNeeded(target); err != nil {
		return Result{Error: err}
	}

	lines := strings.Split(content, "\n")
	previewCount := min(30, len(lines))
	preview := make([]string, 0, previewCount)
	for index := 0; index < previewCount; index++ {
		preview = append(preview, fmt.Sprintf("%4d | %s", index+1, lines[index]))
	}

	output := fmt.Sprintf("Successfully wrote to %s (%d lines)", target, len(lines))
	if previewCount > 0 {
		output += "\n\n" + strings.Join(preview, "\n")
	}
	if len(lines) > previewCount {
		output += fmt.Sprintf("\n  ... (%d lines total)", len(lines))
	}
	return Result{Output: truncateResult(output)}
}

// editFile 以精确字符串替换方式编辑文件，并返回统一 diff。
func (r *Registry) editFile(path string, oldString string, newString string) Result {
	target := r.resolvePath(path)
	content, err := os.ReadFile(target)
	if err != nil {
		return Result{Error: err}
	}

	text := string(content)
	actualOld, normalizedMatch := findActualString(text, oldString)
	if actualOld == "" {
		return Result{Error: fmt.Errorf("old_string not found in file")}
	}
	if strings.Count(text, actualOld) > 1 {
		return Result{Error: fmt.Errorf("old_string is not unique in file")}
	}

	updated := strings.Replace(text, actualOld, newString, 1)
	if err := os.WriteFile(target, []byte(updated), 0o644); err != nil {
		return Result{Error: err}
	}
	if err := r.refreshMemoryIndexIfNeeded(target); err != nil {
		return Result{Error: err}
	}

	diff := generateDiff(text, actualOld, newString)
	resultText := fmt.Sprintf("Successfully edited %s", target)
	if normalizedMatch {
		resultText += " (matched via quote normalization)"
	}
	resultText += "\n\n" + diff
	return Result{Output: truncateResult(resultText)}
}

// refreshMemoryIndexIfNeeded 在工具写入 memory 目录下的 markdown 记忆文件后自动刷新索引。
// 这样模型即使直接通过 write_file 或 edit_file 维护记忆文件，也不会让 MEMORY.md、/memory 展示
// 和 prompt 中读取到的索引状态彼此脱节。
func (r *Registry) refreshMemoryIndexIfNeeded(target string) error {
	store := memory.NewStore(r.workingDirectory)
	memoryDir := filepath.Clean(store.MemoryDir())
	cleanTarget := filepath.Clean(target)

	relativePath, err := filepath.Rel(memoryDir, cleanTarget)
	if err != nil {
		return nil
	}
	if relativePath == "." {
		return nil
	}
	if strings.HasPrefix(relativePath, "..") {
		return nil
	}
	if filepath.Ext(cleanTarget) != ".md" {
		return nil
	}
	if strings.EqualFold(filepath.Base(cleanTarget), "MEMORY.md") {
		return nil
	}
	return store.RefreshIndex()
}

// listFiles 根据 glob 模式列出文件。
func (r *Registry) listFiles(path string, pattern string) Result {
	root := r.resolvePath(path)
	if strings.TrimSpace(pattern) == "" {
		pattern = "**/*"
	}

	matcher, err := globMatcher(pattern)
	if err != nil {
		return Result{Error: err}
	}

	lines := make([]string, 0, 64)
	walkErr := filepath.Walk(root, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if shouldSkipDirectory(info.Name()) && current != root {
				return filepath.SkipDir
			}
			return nil
		}

		relative, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return nil
		}
		normalizedRelative := filepath.ToSlash(relative)
		if matcher.MatchString(normalizedRelative) {
			lines = append(lines, normalizedRelative)
		}
		if len(lines) >= maxListFilesResults {
			return errResultLimitReached
		}
		return nil
	})

	if walkErr != nil && walkErr != errResultLimitReached {
		return Result{Error: walkErr}
	}
	if len(lines) == 0 {
		return Result{Output: "No files found matching the pattern."}
	}

	sort.Strings(lines)
	output := strings.Join(lines, "\n")
	if len(lines) >= maxListFilesResults {
		output += fmt.Sprintf("\n... and more (showing first %d results)", maxListFilesResults)
	}
	return Result{Output: output}
}

// grepSearch 使用正则搜索文件内容，并返回带文件路径与行号的匹配行。
func (r *Registry) grepSearch(path string, pattern string, include string) Result {
	root := r.resolvePath(path)
	if strings.TrimSpace(pattern) == "" {
		return Result{Error: fmt.Errorf("pattern is required")}
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return Result{Error: err}
	}

	var includeMatcher *regexp.Regexp
	if strings.TrimSpace(include) != "" {
		includeMatcher, err = globMatcher(include)
		if err != nil {
			return Result{Error: err}
		}
	}

	lines := make([]string, 0, 32)
	walkErr := filepath.Walk(root, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if shouldSkipDirectory(info.Name()) && current != root {
				return filepath.SkipDir
			}
			return nil
		}

		relative, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return nil
		}
		normalizedRelative := filepath.ToSlash(relative)
		if includeMatcher != nil && !includeMatcher.MatchString(normalizedRelative) {
			return nil
		}

		data, readErr := os.ReadFile(current)
		if readErr != nil {
			return nil
		}

		fileLines := strings.Split(string(data), "\n")
		for index, line := range fileLines {
			if re.MatchString(line) {
				lines = append(lines, fmt.Sprintf("%s:%d:%s", normalizedRelative, index+1, line))
				if len(lines) >= maxGrepSearchResults {
					return errResultLimitReached
				}
			}
		}
		return nil
	})

	if walkErr != nil && walkErr != errResultLimitReached {
		return Result{Error: walkErr}
	}
	if len(lines) == 0 {
		return Result{Output: "No matches found."}
	}

	output := strings.Join(lines, "\n")
	if len(lines) >= maxGrepSearchResults {
		output += fmt.Sprintf("\n... and more (showing first %d matches)", maxGrepSearchResults)
	}
	return Result{Output: truncateResult(output)}
}

// runShell 执行系统命令，并支持可选超时。
func (r *Registry) runShell(ctx context.Context, command string, timeoutText string) Result {
	if strings.TrimSpace(command) == "" {
		return Result{Error: fmt.Errorf("empty command")}
	}
	// 在真正执行 PowerShell 之前先做一层危险命令拦截，
	// 避免高风险命令绕过前置规则直接落到本地执行。
	if IsDangerousCommand(command) {
		return Result{Error: fmt.Errorf("command blocked by dangerous command policy")}
	}

	timeout := defaultShellTimeout
	if strings.TrimSpace(timeoutText) != "" {
		parsed, err := strconv.Atoi(timeoutText)
		if err != nil {
			return Result{Error: fmt.Errorf("invalid timeout: %w", err)}
		}
		timeout = parsed
	}

	runContext, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(runContext, "powershell", "-NoProfile", "-Command", command)
	cmd.Dir = r.workingDirectory
	output, err := cmd.CombinedOutput()
	resultText := string(output)

	if runContext.Err() == context.DeadlineExceeded {
		if strings.TrimSpace(resultText) == "" {
			resultText = "Command timed out with no output."
		}
		return Result{
			Output: truncateResult(resultText),
			Error:  fmt.Errorf("command timed out after %dms", timeout),
		}
	}
	if err != nil {
		return Result{Output: truncateResult(resultText), Error: err}
	}
	if strings.TrimSpace(resultText) == "" {
		resultText = "(no output)"
	}
	return Result{Output: truncateResult(resultText)}
}

// resolveSkill 根据名称解析技能提示词。
func (r *Registry) resolveSkill(name string, args string) Result {
	if strings.TrimSpace(name) == "" {
		return Result{Error: fmt.Errorf("skill_name is required")}
	}

	skill, err := r.skillStore.GetByName(name)
	if err != nil {
		return Result{Error: err}
	}
	if skill == nil {
		return Result{Error: fmt.Errorf("skill not found: %s", name)}
	}
	return Result{Output: r.skillStore.ResolvePrompt(*skill, args)}
}

// webFetch 抓取 URL 并返回可读文本。
func (r *Registry) webFetch(ctx context.Context, url string, maxLengthText string) Result {
	if strings.TrimSpace(url) == "" {
		return Result{Error: fmt.Errorf("url is required")}
	}

	maxLength := defaultFetchMaxChars
	if strings.TrimSpace(maxLengthText) != "" {
		parsed, err := strconv.Atoi(maxLengthText)
		if err != nil {
			return Result{Error: fmt.Errorf("invalid max_length: %w", err)}
		}
		maxLength = parsed
	}

	requestContext, cancel := context.WithTimeout(ctx, defaultFetchTimeout*time.Millisecond)
	defer cancel()

	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, url, nil)
	if err != nil {
		return Result{Error: err}
	}
	request.Header.Set("User-Agent", "mini-claude/1.0")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Result{Error: err}
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{Error: fmt.Errorf("HTTP error: %d %s", response.StatusCode, response.Status)}
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return Result{Error: err}
	}

	text := string(body)
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "html") {
		text = htmlToText(text)
	}
	if len(text) > maxLength {
		text = text[:maxLength] + fmt.Sprintf("\n\n[... truncated at %d characters]", maxLength)
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty response)"
	}
	return Result{Output: truncateResult(text)}
}

