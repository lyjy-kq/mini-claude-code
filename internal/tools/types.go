// Package tools 定义工具元数据、权限模式和执行接口。
// 本文件负责提供工具层最基础的共享类型，供注册表、权限检查和 Agent 调度统一复用。
package tools

import "context"

// PermissionMode 表示工具执行时的权限模式。
type PermissionMode string

const (
	// PermissionDefault 表示默认确认模式。
	PermissionDefault PermissionMode = "default"
	// PermissionPlan 表示只读规划模式。
	PermissionPlan PermissionMode = "plan"
	// PermissionAcceptEdits 表示自动接受编辑，但仍保留危险操作防护。
	PermissionAcceptEdits PermissionMode = "acceptEdits"
	// PermissionBypass 表示绕过确认，接近 yolo 模式。
	PermissionBypass PermissionMode = "bypassPermissions"
	// PermissionDontAsk 表示遇到需要确认的动作时直接拒绝。
	PermissionDontAsk PermissionMode = "dontAsk"
)

// Tool 表示一个可被 Agent 调用的工具定义。
type Tool struct {
	// Name 表示工具名称。
	Name string
	// Description 表示工具用途说明。
	Description string
	// InputSchema 表示工具的最小输入结构定义，便于 prompt 注入和后续原生工具协议迁移。
	InputSchema map[string]any
	// ReadOnly 标记该工具是否属于只读工具。
	ReadOnly bool
	// Deferred 标记该工具是否属于延迟公开工具。
	Deferred bool
}

// Invocation 表示一次工具调用请求。
type Invocation struct {
	// Name 表示要调用的工具名称。
	Name string
	// Arguments 表示工具参数，当前统一使用字符串键值对承载最小骨架。
	Arguments map[string]string
}

// PermissionContext 表示权限判断需要的附加上下文。
type PermissionContext struct {
	// PlanFilePath 表示 plan mode 下唯一允许编辑的计划文件。
	PlanFilePath string
}

// Result 表示工具执行结果。
// PermissionDecision 表示一次权限检查的三态结果。
// 它把“可以直接执行”“必须拒绝”“需要用户确认”从旧的 error 二态里拆开，
// 让 Agent 和 REPL 能共用与源仓库更接近的审批主链。
type PermissionDecision struct {
	// Action 表示当前工具调用的权限动作：allow / deny / confirm。
	Action string
	// Message 保存拒绝原因或确认提示文案。
	Message string
}

type Result struct {
	// Output 表示标准结果文本。
	Output string
	// Error 表示执行错误，便于统一上抛给 Agent。
	Error error
}

var concurrencySafeToolNames = map[string]struct{}{
	"read_file":   {},
	"list_files":  {},
	"grep_search": {},
	"web_fetch":   {},
}

// IsConcurrencySafeTool 判断工具是否属于可并发执行的白名单。
// 这里先对齐源仓库当前使用的最小集合：只允许无副作用的读工具并发运行，
// 避免把 `skill`、`tool_search`、写工具或 agent 委派误纳入并发批处理。
func IsConcurrencySafeTool(name string) bool {
	_, ok := concurrencySafeToolNames[name]
	return ok
}

// Executor 定义工具执行器接口，便于后续替换为更复杂的注册中心。
type Executor interface {
	// Execute 负责执行一次工具调用。
	Execute(ctx context.Context, call Invocation) Result
	// Definitions 返回当前已注册工具。
	Definitions() []Tool
	// DeferredDefinitions 返回当前延迟工具定义。
	DeferredDefinitions() []Tool
}
