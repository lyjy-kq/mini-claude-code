// Package agent 实现 Go 版 mini claude 的核心调度器。
// 本文件保留 Agent 核心结构体、Options、New() 和 HandlePrompt()。
package agent

import (
	"context"
	"strings"
	"sync"
	"time"

	"mini-claude-code/internal/api"
	"mini-claude-code/internal/comment"
	"mini-claude-code/internal/config"
	"mini-claude-code/internal/contextx"
	"mini-claude-code/internal/mcp"
	"mini-claude-code/internal/memory"
	"mini-claude-code/internal/prompt"
	"mini-claude-code/internal/session"
	"mini-claude-code/internal/skills"
	"mini-claude-code/internal/spec"
	"mini-claude-code/internal/subagent"
	"mini-claude-code/internal/tools"
	"mini-claude-code/internal/ui"
	"mini-claude-code/internal/workspace"
)

const (
	// maxToolRoundsPerPrompt 表示单次用户输入允许的最大工具往返轮数。
	maxToolRoundsPerPrompt = 8
	// snipPlaceholder 表示旧工具结果被裁剪后的占位文案。
	snipPlaceholder = "[Content snipped - re-read if needed]"
	// oldResultPlaceholder 表示更旧工具结果被清空后的占位文案。
	oldResultPlaceholder = "[Old result cleared]"
	// microcompactIdle 表示触发 microcompact 前要求的空闲时长。
	microcompactIdle = 5 * time.Minute
	// budgetThreshold 表示开始压缩工具结果的上下文利用率阈值。
	budgetThreshold = 0.50
	// highBudgetThreshold 表示启用更激进截断的上下文利用率阈值。
	highBudgetThreshold = 0.70
	// autoCompactThreshold 表示自动摘要压缩的上下文利用率阈值。
	autoCompactThreshold = 0.85
	// budgetLimitLow 表示普通压力下保留的单条工具结果字符预算。
	budgetLimitLow = 30000
	// budgetLimitHigh 表示高压力下保留的单条工具结果字符预算。
	budgetLimitHigh = 15000
	// largeResultThreshold 表示工具结果超过该大小时要落盘。
	largeResultThreshold = 30 * 1024
	// largeResultPreviewMax 表示落盘后回写上下文时最多保留的预览行数。
	largeResultPreviewMax = 200
	// sessionMemoryBudget 表示单个会话最多向消息历史注入的记忆体积。
	sessionMemoryBudget = 32 * 1024
)

var modelContextWindows = map[string]int{
	"claude-opus-4-6":           200000,
	"claude-sonnet-4-6":         200000,
	"claude-sonnet-4-20250514":  200000,
	"claude-haiku-4-5-20251001": 200000,
	"claude-opus-4-20250514":    200000,
	"gpt-4o":                    128000,
	"gpt-4o-mini":               128000,
}

// snippableToolNames 标记允许参与 stale-result 裁剪的读工具白名单。
// 这里先对齐源仓库的 SNIPPABLE_TOOLS 语义，只允许无副作用工具进入 Anthropic 裁剪路径。
var snippableToolNames = map[string]struct{}{
	"read_file":   {},
	"list_files":  {},
	"grep_search": {},
	"web_fetch":   {},
}

// Options 表示创建 Agent 时需要的参数。
type Options struct {
	// WorkingDirectory 表示 Agent 默认处理文件的工作目录。
	WorkingDirectory string
	// Model 表示当前会话配置的模型名。
	Model string
	// PermissionMode 表示当前权限模式。
	PermissionMode tools.PermissionMode
	// Resume 表示是否尝试恢复最近会话。
	Resume bool
	// Thinking 表示是否启用更深的思考模式。
	Thinking bool
	// MaxTurns 表示最大轮次数限制。
	MaxTurns int
	// MaxCostUSD 表示会话成本预算上限。
	MaxCostUSD float64
	// RuntimeConfig 表示运行时全局配置。
	RuntimeConfig config.Config
}


// Agent 琛ㄧず涓€涓渶灏忓彲鐢ㄧ殑 Coding Agent銆?
type Agent struct {
	// options 淇濆瓨鍒濆鍖栧弬鏁帮紝渚夸簬鍏朵綑閫昏緫璇诲彇杩愯涓婁笅鏂囥€?
	options Options
	// registry 琛ㄧず褰撳墠宸ュ叿娉ㄥ唽琛ㄣ€?
	registry *tools.Registry
	// modelClient 琛ㄧず褰撳墠浣跨敤鐨勬ā鍨嬪鎴风銆?
	modelClient api.Client
	// activeProvider 表示当前会话实际绑定的 provider。
	// 把它显式挂在运行态上，是为了让建 client、session 落盘、恢复和 provider-specific 分支都共享同一真相。
	activeProvider string
	// systemPrompt 淇濆瓨缁勮鍚庣殑绯荤粺鎻愮ず璇嶃€?
	systemPrompt string
	// turns 璁板綍褰撳墠浼氳瘽宸茬粡娑堣€楃殑杞鏁般€?
	turns int
	// sessionStore 绠＄悊浼氳瘽鎸佷箙鍖栥€?
	sessionStore *session.Store
	// memoryStore 绠＄悊鏈湴璁板繂銆?
	memoryStore *memory.Store
	// skillStore 绠＄悊鎶€鑳藉彂鐜般€?
	skillStore *skills.Store
	// subAgentStore 绠＄悊瀛愭櫤鑳戒綋閰嶇疆鍙戠幇銆?
	subAgentStore *subagent.Store
	// mcpManager 绠＄悊 MCP 閰嶇疆鍔犺浇銆?
	mcpManager *mcp.Manager
	// workspaceManager 绠＄悊宸ヤ綔鍖鸿竟鐣屼笌璺緞瑙ｆ瀽銆?
	workspaceManager *workspace.Manager
	// specValidator 璐熻矗鎵ц缂栫爜鍓?spec 鏍￠獙銆?
	specValidator *spec.Validator
	// commentRules 淇濆瓨褰撳墠椤圭洰鐨勯粯璁ゆ敞閲婅鑼冦€?
	commentRules comment.RuleSet
	// lastResponse 淇濆瓨鏈€杩戜竴娆¤緭鍑猴紝渚夸簬鎭㈠鎴栫姸鎬佸睍绀恒€?
	lastResponse string
	// baseSystemPrompt 淇濆瓨鏈彔鍔?plan mode 鎻愮ず鐨勫熀纭€绯荤粺鎻愮ず璇嶃€?
	baseSystemPrompt string
	// prePlanMode 淇濆瓨杩涘叆 plan mode 鍓嶇殑鏉冮檺妯″紡銆?
	prePlanMode tools.PermissionMode
	// planFilePath 淇濆瓨褰撳墠璁″垝鏂囦欢璺緞銆?
	planFilePath string
	// messages 淇濆瓨褰撳墠瀹屾暣娑堟伅鍘嗗彶銆?
	messages []api.Message
	// readFileState 璁板綍鏈細璇濆凡璇诲彇鏂囦欢鐨勬渶鏂颁慨鏀规椂闂达紝鐢ㄤ簬 read-before-edit 绾︽潫銆?
	readFileState map[string]time.Time
	// sessionID 琛ㄧず褰撳墠浼氳瘽褰掓。 id銆?
	sessionID string
	// sessionStartTime 琛ㄧず褰撳墠浼氳瘽寮€濮嬫椂闂淬€?
	sessionStartTime time.Time
	// totalInputTokens 琛ㄧず褰撳墠浼氳瘽绱杈撳叆 token 鏁般€?
	totalInputTokens int
	// totalOutputTokens 琛ㄧず褰撳墠浼氳瘽绱杈撳嚭 token 鏁般€?
	totalOutputTokens int
	// lastInputTokenCount 琛ㄧず鏈€杩戜竴娆℃ā鍨嬭姹傝緭鍏?token 鏁般€?
	lastInputTokenCount int
	// effectiveWindow 琛ㄧず褰撳墠妯″瀷鍙敤鐨勪笂涓嬫枃绐楀彛澶у皬銆?
	effectiveWindow int
	// contextState 淇濆瓨褰撳墠涓婁笅鏂囧帇缂╁弬鏁般€?
	contextState contextx.State
	// contextCleared 表示计划审批或同类流程是否刚刚清空过上下文。
	// 当它为 true 时，下一次工具执行结果需要改写成新的 user 边界，
	// 避免恢复链把“新上下文起点”误还原成旧 tool call 往返的一部分。
	contextCleared bool
	// lastAPICallTime 表示最近一次模型调用完成时间。
	lastAPICallTime time.Time
	// surfacedMemoryPaths 记录已经注入过的记忆路径，避免重复注入。
	surfacedMemoryPaths map[string]struct{}
	// sessionMemoryBytes 记录当前会话已经注入的记忆体积。
	sessionMemoryBytes int
	// providerSnapshot 保存当前会话对应的 provider-specific 原生消息快照。
	// 这样在 compact、clear、restore 之后，provider 恢复兜底看到的也是最新状态，
	// 不会出现统一消息历史已经变了、原生快照却还停留在旧版本的问题。
	providerSnapshot api.ProviderMessageSnapshot
	// openAINativeMessages 保存当前会话的 OpenAI-compatible 原生消息历史。
	// 这份运行态是为了解决 resume 后反复从统一主链“二次投影”导致的结构损耗问题。
	openAINativeMessages []map[string]any
	// anthropicSystemPrompt 保存当前会话的 Anthropic 原生 system 文本。
	// Anthropic 的 system 与 messages 分离，因此需要单独保持原生边界以贴近源仓库行为。
	anthropicSystemPrompt string
	// anthropicNativeMessages 保存当前会话的 Anthropic 原生 messages 数组。
	// 后续 provider-specific 恢复、snip 和 microcompact 会优先读取它，而不是重新反解统一主链。
	anthropicNativeMessages []map[string]any
	// currentCancel 保存当前进行中主循环的取消函数。
	// CLI 的 Ctrl+C 会通过它中断本轮模型调用与工具执行，贴近源仓库的可打断交互。
	currentCancel context.CancelFunc
	// confirmFn 保存外部传入的确认回调。
	// REPL 会复用同一个 scanner/stdio 实例来完成确认，避免重复创建输入循环。
	confirmFn func(message string) bool
	// confirmedMessages 记录本会话里已经确认过的危险动作提示。
	// 这样相同命令或相同确认消息在后续轮次里可以直接复用确认结果，贴近主链的会话级白名单体验。
	confirmedMessages map[string]struct{}
	// isSubAgent 表示当前 Agent 是否作为隔离子智能体运行。
	// 子智能体默认静默执行，避免把内部推理、spinner 和最终答复直接打到主 REPL。
	isSubAgent bool
	// processing 表示当前是否正在处理一次用户输入。
	// 该状态会被 REPL 的 SIGINT 逻辑读取，用于区分“中断当前轮”和“双击退出”两种路径。
	processing bool
	// stateMu 保护 readFileState 等会被并发安全工具访问的运行态。
	// 这是为了给并发读工具批处理和流式提前执行留出安全边界，避免 map 并发读写。
	stateMu sync.RWMutex
}

// New 根据参数构造 Agent。
func New(options Options) *Agent {
	workspaceManager, err := workspace.NewManager(options.WorkingDirectory)
	if err != nil {
		workspaceManager, _ = workspace.NewManager(".")
	}

	resolvedRoot := options.WorkingDirectory
	if workspaceManager != nil {
		resolvedRoot = workspaceManager.Root()
	}

	mcpManager := mcp.NewManager(resolvedRoot)
	registry := tools.NewRegistry(resolvedRoot, mcpManager)
	baseSystemPrompt := prompt.Build(resolvedRoot, options.PermissionMode, registry)
	systemPrompt := baseSystemPrompt
	planFilePath := ""
	prePlanMode := tools.PermissionDefault

	if options.PermissionMode == tools.PermissionPlan {
		prePlanMode = tools.PermissionDefault
		planFilePath = generatePlanFilePath(resolvedRoot, time.Now().Format("20060102-150405"))
		systemPrompt = baseSystemPrompt + buildPlanModePrompt(planFilePath)
	}

	contextState := contextx.Default()
	activeProvider := resolveConfiguredProvider(options.RuntimeConfig)
	return &Agent{
		options:             options,
		registry:            registry,
		modelClient:         buildModelClient(options, activeProvider),
		activeProvider:      activeProvider,
		systemPrompt:        systemPrompt,
		turns:               0,
		sessionStore:        session.NewStore(resolvedRoot),
		memoryStore:         memory.NewStore(resolvedRoot),
		skillStore:          skills.NewStore(resolvedRoot),
		subAgentStore:       subagent.NewStore(resolvedRoot),
		mcpManager:          mcpManager,
		workspaceManager:    workspaceManager,
		specValidator:       spec.NewValidator(resolvedRoot),
		commentRules:        comment.DefaultRules(),
		lastResponse:        "",
		baseSystemPrompt:    baseSystemPrompt,
		prePlanMode:         prePlanMode,
		planFilePath:        planFilePath,
		messages:            []api.Message{{Role: "system", Content: systemPrompt}},
		readFileState:       map[string]time.Time{},
		sessionID:           time.Now().Format("20060102-150405"),
		sessionStartTime:    time.Now(),
		totalInputTokens:    0,
		totalOutputTokens:   0,
		lastInputTokenCount: 0,
		effectiveWindow:     resolveEffectiveWindow(options.Model, contextState),
		contextState:        contextState,
		contextCleared:      false,
		lastAPICallTime:     time.Time{},
		surfacedMemoryPaths: map[string]struct{}{},
		sessionMemoryBytes:  0,
		confirmedMessages:   map[string]struct{}{},
		providerSnapshot:    api.BuildProviderMessageSnapshot([]api.Message{{Role: "system", Content: systemPrompt}}),
		openAINativeMessages: api.CloneProviderMessageSnapshot(
			api.BuildProviderMessageSnapshot([]api.Message{{Role: "system", Content: systemPrompt}}),
		).OpenAI,
		anthropicSystemPrompt: api.CloneProviderMessageSnapshot(
			api.BuildProviderMessageSnapshot([]api.Message{{Role: "system", Content: systemPrompt}}),
		).AnthropicSystem,
		anthropicNativeMessages: api.CloneProviderMessageSnapshot(
			api.BuildProviderMessageSnapshot([]api.Message{{Role: "system", Content: systemPrompt}}),
		).AnthropicMessages,
	}
}

// HandlePrompt 处理一次用户输入。
func (a *Agent) HandlePrompt(input string) error {
	if err := a.ensureReady(); err != nil {
		return err
	}
	if err := a.guardTurnBudget(); err != nil {
		// 进入新一轮前的预算预检也要先给用户明确反馈，
		// 避免这里只返回内部错误，而中途超限路径却会打印主链风格的提示。
		ui.PrintInfo("Budget exceeded: " + err.Error())
		return err
	}

	a.turns++
	a.bootstrapResources()
	// 先把原始用户输入写入消息历史，再由主循环异步轮询记忆 prefetch 结果并追加注入，
	// 这样可以贴近源仓库“先起主链、记忆零等待补入”的行为。
	a.appendMessage(api.Message{
		Role:    "user",
		Content: strings.TrimSpace(input),
	})
	a.checkAndCompact()

	// 为当前用户轮次创建可取消上下文，并把取消句柄暴露给 REPL 的 Ctrl+C 处理器。
	// 这样用户在长时间推理、流式输出或工具执行中都可以中断本轮，而不必直接退出进程。
	runCtx, cancel := context.WithCancel(context.Background())
	a.setProcessingState(cancel, true)
	defer a.setProcessingState(nil, false)

	finalText, err := a.runModelLoop(runCtx, input, len(a.messages)-1)
	if err != nil {
		return err
	}

	a.lastResponse = finalText
	if !a.isSubAgent && strings.TrimSpace(finalText) != "" {
		ui.PrintAssistant(finalText)
	}
	if !a.isSubAgent {
		ui.PrintDivider()
	}
	a.refreshProviderSnapshot()

	if a.isSubAgent {
		return nil
	}

	return a.sessionStore.Save(session.Entry{
		Metadata: session.Metadata{
			ID:    a.sessionID,
			Model: a.options.Model,
			// 会话元信息里显式写入 provider，避免恢复时只能依赖当前环境配置猜测原后端。
			Provider:         a.currentProvider(),
			WorkingDirectory: a.registry.WorkingDirectory(),
			StartTime:        a.sessionStartTime,
			MessageCount:     len(a.messages),
		},
		Timestamp: time.Now(),
		Model:     a.options.Model,
		Prompt:    input,
		Response:  finalText,
		Messages:  a.messages,
		// 会话归档优先使用当前运行态维护的 native provider 历史落盘，
		// 避免刚恢复出的原生结构又在保存时被统一主链重新投影，进一步贴近源仓库的 backend-native 会话语义。
		ProviderMessages: a.currentProviderSnapshot(),
		Runtime: session.RuntimeState{
			Turns:               a.turns,
			LastResponse:        a.lastResponse,
			TotalInputTokens:    a.totalInputTokens,
			TotalOutputTokens:   a.totalOutputTokens,
			LastInputTokenCount: a.lastInputTokenCount,
			EffectiveWindow:     a.effectiveWindow,
			ContextState:        a.contextState,
			LastAPICallTime:     a.lastAPICallTime,
			ReadFileState:       cloneTimeMap(a.readFileState),
			SurfacedMemoryPaths: a.listSurfacedMemoryPaths(),
			SessionMemoryBytes:  a.sessionMemoryBytes,
			// 把 contextCleared 一并落盘，避免 clear-context 之后恰好中断会话时丢失边界语义。
			ContextCleared: a.contextCleared,
		},
	})
}

// currentProviderSnapshot 返回当前运行态维护的 provider-specific 原生快照。
// 这里优先使用 native 历史，而不是直接复用可能来自旧投影的 providerSnapshot 字段。
// RunOnceResult 琛ㄧず鍗曟闈欓粯鎵ц鐨勬枃鏈拰 token 澧為噺銆?
// 杩欎釜缁撴瀯鐢ㄤ簬鎶?RunSubAgent 鍜?skill-fork 鏀舵潫鍒板悓涓€鏉″瓙浠诲姟鍏ュ彛锛?
// 鍚庣画缁х画瀵归綈婧愪粨搴撶殑 output buffer 鎴栨洿缁嗙殑 run-once 璇箟鏃讹紝涔熻兘鍩轰簬杩欎釜杩斿洖缁撴瀯绋冲畾婕斿寲銆?

// 它允许主循环用零等待方式轮询结果，而不是在首轮模型调用前同步阻塞。
