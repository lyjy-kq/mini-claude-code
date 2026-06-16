// Package tools 中的 permissions 负责集中处理工具权限判定。
// 本文件把旧的 error 二态升级为 allow / deny / confirm 三态，
// 让 Go 版可以像源仓库一样在 Agent 和 REPL 之间共享统一的确认链路。
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var readOnlyToolNames = map[string]struct{}{
	"read_file":   {},
	"list_files":  {},
	"grep_search": {},
	"skill":       {},
	"tool_search": {},
	"web_fetch":   {},
}

var editToolNames = map[string]struct{}{
	"write_file": {},
	"edit_file":  {},
}

// parsedRule 表示一条解析后的权限规则。
// 规则支持 `tool_name` 全量匹配，以及 `tool_name(pattern)` 这种带参数模式的写法。
type parsedRule struct {
	// Tool 表示规则作用的工具名。
	Tool string
	// Pattern 表示可选的参数匹配模式；为空表示匹配该工具的所有调用。
	Pattern string
}

// permissionRules 表示从 settings.json 聚合出的 allow / deny 规则集合。
type permissionRules struct {
	// Allow 保存允许规则列表。
	Allow []parsedRule
	// Deny 保存拒绝规则列表。
	Deny []parsedRule
}

// rawSettings 表示 settings.json 中和权限相关的最小结构。
type rawSettings struct {
	// Permissions 保存 allow / deny 配置段。
	Permissions struct {
		// Allow 保存允许规则原始字符串列表。
		Allow []string `json:"allow"`
		// Deny 保存拒绝规则原始字符串列表。
		Deny []string `json:"deny"`
	} `json:"permissions"`
}

// CheckPermission 根据权限模式判断当前工具是否允许执行。
// 这里返回三态结果而不是直接报错，方便 Agent 决定是自动执行、直接拒绝还是向用户发起确认。
func CheckPermission(mode PermissionMode, tool Tool, call Invocation, permissionContext PermissionContext) PermissionDecision {
	// bypassPermissions 模式下不做额外拦截，尽量贴近 yolo 语义。
	if mode == PermissionBypass {
		return PermissionDecision{Action: "allow"}
	}

	// 先应用 settings.json 里的权限规则，并保持 deny 高于 allow。
	// 这一步要放在默认模式判断前面，才能与源仓库的“规则优先级高于交互模式”对齐。
	if ruleDecision := checkPermissionRules(tool.Name, call.Arguments); ruleDecision.Action != "" {
		return ruleDecision
	}

	// 先统一放行所有只读工具，避免后面的模式分支把正常探索动作一起拦住。
	if isReadOnlyTool(tool.Name) {
		return PermissionDecision{Action: "allow"}
	}

	// plan mode 中只允许读工具，以及唯一例外的 plan 文件写入/编辑。
	// 这里对齐源码仓库语义：模型在规划态里既可以整段重写计划文件，也可以增量 edit 计划文件，
	// 只要目标路径就是当前 plan file，就不应被权限层额外拦下。
	if mode == PermissionPlan {
		if isPlanModeWriteAllowed(tool, call, permissionContext) {
			return PermissionDecision{Action: "allow"}
		}
		if tool.Name == "run_shell" {
			return PermissionDecision{
				Action:  "deny",
				Message: "Shell commands blocked in plan mode",
			}
		}
		return PermissionDecision{
			Action:  "deny",
			Message: fmt.Sprintf("Blocked in plan mode: %s", tool.Name),
		}
	}

	// acceptEdits 自动放行文件编辑，但 shell 仍需走危险动作检查。
	if mode == PermissionAcceptEdits && isEditTool(tool.Name) {
		return PermissionDecision{Action: "allow"}
	}

	// run_shell 在默认模式下不再直接拦截，而是改成要求确认，
	// 这样 Go 版可以补齐源仓库已有的 Allow? (y/n) 审批体验。
	if tool.Name == "run_shell" && IsDangerousCommand(call.Arguments["command"]) {
		if mode == PermissionDontAsk {
			return PermissionDecision{
				Action:  "deny",
				Message: fmt.Sprintf("dangerous shell command auto-denied in dontAsk mode: %s", strings.TrimSpace(call.Arguments["command"])),
			}
		}
		return PermissionDecision{
			Action:  "confirm",
			Message: strings.TrimSpace(call.Arguments["command"]),
		}
	}

	// dontAsk 模式下直接拒绝所有原本需要确认的写操作。
	if mode == PermissionDontAsk && !tool.ReadOnly {
		return PermissionDecision{
			Action:  "deny",
			Message: fmt.Sprintf("tool %s requires confirmation and is denied in dontAsk mode", tool.Name),
		}
	}

	// 默认模式下，新建文件和编辑不存在文件需要显式确认。
	// 这样做可以保留源仓库对高风险文件写入的额外谨慎。
	if tool.Name == "write_file" {
		target := strings.TrimSpace(call.Arguments["file_path"])
		if target != "" && !fileExists(target) {
			return PermissionDecision{
				Action:  "confirm",
				Message: "write new file: " + target,
			}
		}
	}
	if tool.Name == "edit_file" {
		target := strings.TrimSpace(call.Arguments["file_path"])
		if target != "" && !fileExists(target) {
			return PermissionDecision{
				Action:  "confirm",
				Message: "edit non-existent file: " + target,
			}
		}
	}

	return PermissionDecision{Action: "allow"}
}

// PermissionError 把 deny 结果转换回统一错误对象。
// 这样旧调用方在过渡期仍可用 error 语义消费权限拒绝，而新的确认链路则可以直接读三态结果。
func PermissionError(decision PermissionDecision) error {
	if decision.Action != "deny" {
		return nil
	}
	if strings.TrimSpace(decision.Message) == "" {
		return fmt.Errorf("tool execution denied by permission policy")
	}
	return fmt.Errorf(decision.Message)
}

// isPlanModeWriteAllowed 判断 plan mode 下是否属于“只允许改计划文件”的合法例外。
func isPlanModeWriteAllowed(tool Tool, call Invocation, permissionContext PermissionContext) bool {
	if permissionContext.PlanFilePath == "" {
		return false
	}
	if tool.Name != "write_file" && tool.Name != "edit_file" {
		return false
	}

	target := call.Arguments["file_path"]
	if target == "" {
		return false
	}

	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	absolutePlan, err := filepath.Abs(permissionContext.PlanFilePath)
	if err != nil {
		return false
	}
	return absoluteTarget == absolutePlan
}

// isReadOnlyTool 判断工具是否属于可直接放行的读工具。
func isReadOnlyTool(name string) bool {
	_, ok := readOnlyToolNames[name]
	return ok
}

// isEditTool 判断工具是否属于文件写编辑工具。
func isEditTool(name string) bool {
	_, ok := editToolNames[name]
	return ok
}

// fileExists 判断目标路径当前是否已经存在。
// 这里单独抽成辅助函数，方便权限策略在“新建文件”与“改已有文件”之间做不同确认。
func fileExists(path string) bool {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	_, statErr := os.Stat(absolutePath)
	return statErr == nil
}

// checkPermissionRules 检查 settings.json 中是否存在命中的 allow / deny 规则。
// 与源仓库一致，deny 规则优先级高于 allow；只有命中规则时才返回非空动作。
func checkPermissionRules(toolName string, input map[string]string) PermissionDecision {
	rules := loadPermissionRules()

	for _, rule := range rules.Deny {
		if matchesRule(rule, toolName, input) {
			return PermissionDecision{
				Action:  "deny",
				Message: fmt.Sprintf("Denied by permission rule for %s", toolName),
			}
		}
	}
	for _, rule := range rules.Allow {
		if matchesRule(rule, toolName, input) {
			return PermissionDecision{Action: "allow"}
		}
	}
	return PermissionDecision{}
}

// loadPermissionRules 读取用户级与项目级 settings.json，并聚合出权限规则。
// 这里故意容忍文件不存在或 JSON 非法，避免配置缺失把主链执行整体打断。
func loadPermissionRules() permissionRules {
	combined := permissionRules{
		Allow: []parsedRule{},
		Deny:  []parsedRule{},
	}

	homeDir, homeErr := os.UserHomeDir()
	if homeErr == nil && strings.TrimSpace(homeDir) != "" {
		appendRulesFromSettings(filepath.Join(homeDir, ".claude", "settings.json"), &combined)
	}

	if workingDir, wdErr := os.Getwd(); wdErr == nil && strings.TrimSpace(workingDir) != "" {
		appendRulesFromSettings(filepath.Join(workingDir, ".claude", "settings.json"), &combined)
	}

	return combined
}

// appendRulesFromSettings 从单个 settings.json 文件里追加 allow / deny 规则。
func appendRulesFromSettings(filePath string, combined *permissionRules) {
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	var settings rawSettings
	if err := json.Unmarshal(bytes, &settings); err != nil {
		return
	}

	for _, rule := range settings.Permissions.Allow {
		combined.Allow = append(combined.Allow, parseRule(rule))
	}
	for _, rule := range settings.Permissions.Deny {
		combined.Deny = append(combined.Deny, parseRule(rule))
	}
}

// parseRule 把字符串规则解析成工具名 + 可选参数模式。
// 支持 `tool_name` 和 `tool_name(pattern)` 两种形态，与源仓库保持一致。
func parseRule(rule string) parsedRule {
	trimmed := strings.TrimSpace(rule)
	if trimmed == "" {
		return parsedRule{}
	}

	openIndex := strings.Index(trimmed, "(")
	closeIndex := strings.LastIndex(trimmed, ")")
	if openIndex > 0 && closeIndex > openIndex {
		return parsedRule{
			Tool:    strings.TrimSpace(trimmed[:openIndex]),
			Pattern: strings.TrimSpace(trimmed[openIndex+1 : closeIndex]),
		}
	}
	return parsedRule{Tool: trimmed}
}

// matchesRule 判断某条规则是否命中当前工具调用。
// 参数匹配优先使用 shell command，其次使用 file_path；带 `*` 后缀的模式按前缀匹配处理。
func matchesRule(rule parsedRule, toolName string, input map[string]string) bool {
	if strings.TrimSpace(rule.Tool) == "" || rule.Tool != toolName {
		return false
	}
	if strings.TrimSpace(rule.Pattern) == "" {
		return true
	}

	value := ""
	switch {
	case toolName == "run_shell":
		value = input["command"]
	case strings.TrimSpace(input["file_path"]) != "":
		value = input["file_path"]
	default:
		return true
	}

	pattern := rule.Pattern
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))
	}
	return value == pattern
}
