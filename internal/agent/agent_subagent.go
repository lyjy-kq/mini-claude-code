// agent_subagent.go 负责子智能体管理，包括子智能体创建、静默执行、技能/自定义工具调用等。
// 该文件从 agent.go 拆分而来。
package agent

import (
	"fmt"
	"strings"
	"time"
	"mini-claude-code/internal/api"
	"mini-claude-code/internal/memory"
	"mini-claude-code/internal/session"
	"mini-claude-code/internal/skills"
	"mini-claude-code/internal/subagent"
	"mini-claude-code/internal/tools"
	"mini-claude-code/internal/ui"
)


func (a *Agent) RunSubAgent(agentType string, task string) (string, error) {
	config, err := a.subAgentStore.GetConfig(agentType)
	if err != nil {
		return "", err
	}
	ui.PrintSubAgentStart(agentType, subAgentSummary(agentType, config.SystemPrompt))

	subRegistry := tools.NewRegistry(a.registry.WorkingDirectory(), a.mcpManager)
	subAgent := &Agent{
		options: Options{
			WorkingDirectory: a.registry.WorkingDirectory(),
			Model:            a.options.Model,
			PermissionMode: func() tools.PermissionMode {
				if a.options.PermissionMode == tools.PermissionPlan {
					return tools.PermissionPlan
				}
				return tools.PermissionBypass
			}(),
			Resume:        false,
			Thinking:      a.options.Thinking,
			MaxTurns:      a.options.MaxTurns,
			MaxCostUSD:    a.options.MaxCostUSD,
			RuntimeConfig: a.options.RuntimeConfig,
		},
		registry:            subRegistry.CloneWithAllowedTools(config.ToolNames),
		modelClient:         buildModelClient(a.options, a.currentProvider()),
		activeProvider:      a.currentProvider(),
		systemPrompt:        strings.TrimSpace(config.SystemPrompt),
		turns:               0,
		sessionStore:        session.NewStore(a.registry.WorkingDirectory()),
		memoryStore:         memory.NewStore(a.registry.WorkingDirectory()),
		skillStore:          skills.NewStore(a.registry.WorkingDirectory()),
		subAgentStore:       subagent.NewStore(a.registry.WorkingDirectory()),
		mcpManager:          a.mcpManager,
		workspaceManager:    a.workspaceManager,
		specValidator:       a.specValidator,
		commentRules:        a.commentRules,
		lastResponse:        "",
		baseSystemPrompt:    strings.TrimSpace(config.SystemPrompt),
		prePlanMode:         tools.PermissionDefault,
		planFilePath:        "",
		messages:            []api.Message{{Role: "system", Content: strings.TrimSpace(config.SystemPrompt)}},
		readFileState:       map[string]time.Time{},
		sessionID:           time.Now().Format("20060102-150405") + "-sub",
		sessionStartTime:    time.Now(),
		totalInputTokens:    0,
		totalOutputTokens:   0,
		lastInputTokenCount: 0,
		effectiveWindow:     a.effectiveWindow,
		contextState:        a.contextState,
		lastAPICallTime:     time.Time{},
		surfacedMemoryPaths: map[string]struct{}{},
		sessionMemoryBytes:  0,
		// 子智能体也要和主智能体一样维护 provider-specific 快照，
		// 否则它在 compact / restore / auto-save 路径上会重新出现主链与快照脱节的问题。
		providerSnapshot: api.BuildProviderMessageSnapshot([]api.Message{{Role: "system", Content: strings.TrimSpace(config.SystemPrompt)}}),
		openAINativeMessages: api.CloneProviderMessageSnapshot(
			api.BuildProviderMessageSnapshot([]api.Message{{Role: "system", Content: strings.TrimSpace(config.SystemPrompt)}}),
		).OpenAI,
		anthropicSystemPrompt: api.CloneProviderMessageSnapshot(
			api.BuildProviderMessageSnapshot([]api.Message{{Role: "system", Content: strings.TrimSpace(config.SystemPrompt)}}),
		).AnthropicSystem,
		anthropicNativeMessages: api.CloneProviderMessageSnapshot(
			api.BuildProviderMessageSnapshot([]api.Message{{Role: "system", Content: strings.TrimSpace(config.SystemPrompt)}}),
		).AnthropicMessages,
		confirmFn:  a.confirmFn,
		isSubAgent: true,
	}

	runResult, err := subAgent.RunOnce(task)
	if err != nil {
		return "", err
	}

	a.totalInputTokens += runResult.InputTokens
	a.totalOutputTokens += runResult.OutputTokens
	ui.PrintSubAgentEnd(agentType, subAgentSummary(agentType, config.SystemPrompt))
	return runResult.Text, nil
}

// subAgentSummary 为子智能体开始/结束提示生成稳定的简短说明。
// 当前 Config 还没有独立 description 字段，所以先从 system prompt 提取摘要，避免再扩张配置面。
func subAgentSummary(agentType string, systemPrompt string) string {
	trimmedType := strings.TrimSpace(agentType)
	trimmedPrompt := strings.TrimSpace(systemPrompt)
	if trimmedPrompt == "" {
		if trimmedType == "" {
			return "sub-agent task"
		}
		return trimmedType + " task"
	}

	lines := strings.Split(trimmedPrompt, "\n")
	summary := strings.TrimSpace(lines[0])
	if summary == "" {
		summary = trimmedPrompt
	}
	if len(summary) > 80 {
		summary = summary[:77] + "..."
	}
	return summary
}

// executeAgentTool 鎵ц agent 宸ュ叿銆?
func (a *Agent) executeAgentTool(call tools.Invocation) tools.Result {
	agentType := strings.TrimSpace(call.Arguments["type"])
	if agentType == "" {
		agentType = "general"
	}

	taskPrompt := strings.TrimSpace(call.Arguments["prompt"])
	if taskPrompt == "" {
		return tools.Result{Error: fmt.Errorf("agent tool requires prompt")}
	}

	description := strings.TrimSpace(call.Arguments["description"])
	if description != "" {
		taskPrompt = "Task: " + description + "\n\n" + taskPrompt
	}

	output, err := a.RunSubAgent(agentType, taskPrompt)
	if err != nil {
		// agent tool 鍦ㄤ富閾句綋楠屼腑鏄€滃瓙鏅鸿兘浣撳皾璇曚换鍔♀€濈被鍨嬶紝
		// 瀵归綈婧愪粨搴撴椂锛屽瓙浠诲姟澶辫触搴旇鍥炲啓鍙鏂囨湰锛岃€屼笉鏄妸鏁翠釜 tool call 鍗囩骇鎴愬伐鍏烽敊璇紝
		// 杩欐牱妯″瀷鎵嶈兘缁х画鍩轰簬澶辫触鍘熷洜璋冩暣绛栫暐锛岃€屼笉鏄鍔ㄥ洖閫€鍒伴€氱敤 error 璺緞銆?
		return tools.Result{Output: fmt.Sprintf("Sub-agent error: %s", err.Error())}
	}
	if strings.TrimSpace(output) == "" {
		// 绌鸿緭鍑轰篃瀵归綈婧愪粨搴撶殑鍗犱綅鏂囨锛岄伩鍏嶅墠鍚庝袱鏉″瓙鏅鸿兘浣撹矾寰勫悇鑷娇鐢ㄤ笉鍚岃涔夈€?
		output = "(Sub-agent produced no output)"
	}
	return tools.Result{Output: output}
}

// executeSkillTool 执行 skill 工具，并根据技能上下文决定走 inline 还是 fork。
// 这里对齐源仓库：inline 技能返回 prompt 注入主链，fork 技能通过隔离子智能体执行。
func (a *Agent) executeSkillTool(call tools.Invocation) tools.Result {
	skillName := strings.TrimSpace(call.Arguments["skill_name"])
	if skillName == "" {
		return tools.Result{Error: fmt.Errorf("skill_name is required")}
	}
	if a.skillStore == nil {
		return tools.Result{Error: fmt.Errorf("skill store is unavailable")}
	}

	skillDefinition, err := a.skillStore.GetByName(skillName)
	if err != nil {
		return tools.Result{Error: err}
	}
	if skillDefinition == nil {
		return tools.Result{Error: fmt.Errorf("skill not found: %s", skillName)}
	}

	resolvedPrompt := a.skillStore.ResolvePrompt(*skillDefinition, call.Arguments["args"])
	if skillDefinition.Context != skills.ContextFork {
		return tools.Result{Output: fmt.Sprintf("[Skill %q activated]\n\n%s", skillName, resolvedPrompt)}
	}

	// fork 模式下，显式过滤工具视图，默认不给子技能递归暴露 agent。
	allowedToolNames := skillDefinition.AllowedTools
	if len(allowedToolNames) == 0 {
		allowedToolNames = make([]string, 0, len(a.registry.AllDefinitions()))
		for _, toolDefinition := range a.registry.AllDefinitions() {
			if toolDefinition.Name == "agent" {
				continue
			}
			allowedToolNames = append(allowedToolNames, toolDefinition.Name)
		}
	}

	ui.PrintSubAgentStart("skill-fork", skillName)
	subRegistry := tools.NewRegistry(a.registry.WorkingDirectory(), a.mcpManager)
	// 鎶€鑳?fork 鍜?agent tool 鍏辩敤鍚屼竴鏉￠殧绂绘瀯閫犻摼锛岄伩鍏嶉噸澶嶇淮鎶ゅ悓涓€浠藉垵濮嬪寲閫昏緫銆?
	subAgent := a.newIsolatedSubAgent(
		strings.TrimSpace(resolvedPrompt),
		subRegistry.CloneWithAllowedTools(allowedToolNames),
		"skill",
	)

	runTask := strings.TrimSpace(call.Arguments["args"])
	if runTask == "" {
		runTask = "Execute this skill task."
	}

	runResult, err := subAgent.RunOnce(runTask)
	if err != nil {
		ui.PrintSubAgentEnd("skill-fork", skillName)
		return tools.Result{Output: fmt.Sprintf("Skill fork error: %s", err.Error())}
	}

	a.totalInputTokens += runResult.InputTokens
	a.totalOutputTokens += runResult.OutputTokens
	ui.PrintSubAgentEnd("skill-fork", skillName)

	if strings.TrimSpace(runResult.Text) == "" {
		return tools.Result{Output: "(Skill produced no output)"}
	}
	return tools.Result{Output: runResult.Text}
}

// newIsolatedSubAgent 缁熶竴鏋勯€犵敤浜庡瓙浠诲姟鐨勯殧绂?Agent銆?
// 杩欓噷鎶婂瓙鏅鸿兘浣撳拰 skill-fork 鍏辩敤鐨勫垵濮嬪寲閫昏緫鏀跺彛鍒板悓涓€涓叆鍙ｏ紝
// 鏃㈣兘鍑忓皯閲嶅浠ｇ爜锛屼篃璁╁悗缁户缁悜婧愪粨搴撳榻愭椂涓嶉渶鍚屾椂淇袱澶勭浉鍚岄€昏緫銆?
func (a *Agent) newIsolatedSubAgent(systemPrompt string, registry *tools.Registry, sessionSuffix string) *Agent {
	trimmedPrompt := strings.TrimSpace(systemPrompt)
	initialMessages := []api.Message{{Role: "system", Content: trimmedPrompt}}
	initialSnapshot := api.BuildProviderMessageSnapshot(initialMessages)
	clonedSnapshot := api.CloneProviderMessageSnapshot(initialSnapshot)

	return &Agent{
		options: Options{
			WorkingDirectory: a.registry.WorkingDirectory(),
			Model:            a.options.Model,
			PermissionMode: func() tools.PermissionMode {
				if a.options.PermissionMode == tools.PermissionPlan {
					return tools.PermissionPlan
				}
				return tools.PermissionBypass
			}(),
			Resume:        false,
			Thinking:      a.options.Thinking,
			MaxTurns:      a.options.MaxTurns,
			MaxCostUSD:    a.options.MaxCostUSD,
			RuntimeConfig: a.options.RuntimeConfig,
		},
		registry:                registry,
		modelClient:             buildModelClient(a.options, a.currentProvider()),
		activeProvider:          a.currentProvider(),
		systemPrompt:            trimmedPrompt,
		turns:                   0,
		sessionStore:            session.NewStore(a.registry.WorkingDirectory()),
		memoryStore:             memory.NewStore(a.registry.WorkingDirectory()),
		skillStore:              skills.NewStore(a.registry.WorkingDirectory()),
		subAgentStore:           subagent.NewStore(a.registry.WorkingDirectory()),
		mcpManager:              a.mcpManager,
		workspaceManager:        a.workspaceManager,
		specValidator:           a.specValidator,
		commentRules:            a.commentRules,
		lastResponse:            "",
		baseSystemPrompt:        trimmedPrompt,
		prePlanMode:             tools.PermissionDefault,
		planFilePath:            "",
		messages:                initialMessages,
		readFileState:           map[string]time.Time{},
		sessionID:               time.Now().Format("20060102-150405") + "-" + sessionSuffix,
		sessionStartTime:        time.Now(),
		totalInputTokens:        0,
		totalOutputTokens:       0,
		lastInputTokenCount:     0,
		effectiveWindow:         a.effectiveWindow,
		contextState:            a.contextState,
		lastAPICallTime:         time.Time{},
		surfacedMemoryPaths:     map[string]struct{}{},
		sessionMemoryBytes:      0,
		confirmedMessages:       map[string]struct{}{},
		providerSnapshot:        initialSnapshot,
		openAINativeMessages:    clonedSnapshot.OpenAI,
		anthropicSystemPrompt:   clonedSnapshot.AnthropicSystem,
		anthropicNativeMessages: clonedSnapshot.AnthropicMessages,
		confirmFn:               a.confirmFn,
		isSubAgent:              true,
	}
}


// RunOnceResult 表示单次静默执行的文本和 token 增量。
type RunOnceResult struct {
	// Text 表示本次执行产生的最终文本输出。
	Text string
	// InputTokens 表示本次执行消耗的输入 token 增量。
	InputTokens int
	// OutputTokens 表示本次执行消耗的输出 token 增量。
	OutputTokens int
}

// RunOnce 以单次静默方式执行一条指令。
// 这里对齐源仓库 runOnce 的最小语义：执行前记录 token 基线，执行后返回
// 文本结果和增量 usage，让子智能体入口先统一到一条主链上。
func (a *Agent) RunOnce(prompt string) (RunOnceResult, error) {
	beforeInput := a.totalInputTokens
	beforeOutput := a.totalOutputTokens

	err := a.HandlePrompt(prompt)

	deltaInput := a.totalInputTokens - beforeInput
	deltaOutput := a.totalOutputTokens - beforeOutput

	return RunOnceResult{
		Text:             a.lastResponse,
		InputTokens:  deltaInput,
		OutputTokens: deltaOutput,
	}, err
}
