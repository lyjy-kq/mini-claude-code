// agent_memloop.go 负责主模型循环（runModelLoop）和记忆预取相关功能。
// 该文件从 agent_messages.go 拆分而来。
package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mini-claude-code/internal/api"
	"mini-claude-code/internal/memory"
	"mini-claude-code/internal/tools"
	"sync"
	"mini-claude-code/internal/ui"
)

// memoryPrefetch 表示一次异步记忆预取的状态。
// 它允许主循环用零等待方式轮询结果，而不是在首轮模型调用前同步阻塞。
type memoryPrefetch struct {
	// ResultCh 用于接收预取完成后的记忆列表。
	ResultCh <-chan []memory.RelevantMemory
	// ErrCh 用于接收预取阶段的错误。
	ErrCh <-chan error
	// Consumed 标记预取结果是否已经被主循环消费。
	Consumed bool
}

// runModelLoop 负责执行模型与工具的循环。
func (a *Agent) runModelLoop(ctx context.Context, userInput string, userMessageIndex int) (string, error) {
	lastAssistantText := ""


	prefetch := a.startMemoryPrefetch(userInput)
	if prefetch == nil {
		// 如果本轮没有启动语义预取，就立刻回到旧的关键词召回路径，
		// 避免因为“增强条件未满足”而让原本可用的最小召回能力消失。
		a.injectKeywordMemories(userInput, userMessageIndex)
	}

	for round := 0; round < maxToolRoundsPerPrompt; round++ {
		a.runCompressionPipeline()
		a.consumeMemoryPrefetch(prefetch, userInput, userMessageIndex)

		// 对支持流式 tool 回调的客户端，允许在 Anthropic `content_block_stop`
		// 完成一个完整 tool_use 时提前启动并发安全工具，尽量贴近源仓库的边收边执行体验。
		earlyExecutions := map[string]chan tools.Result{}
		streamingToolClient, supportsStreamingTools := a.modelClient.(api.StreamingToolClient)

		var response api.Response
		var err error
		if !a.isSubAgent {
			ui.StartSpinner("Thinking")
		}
		if supportsStreamingTools {
			response, err = streamingToolClient.CompleteWithToolCallback(
				ctx,
				a.messages,
				a.registry.ActiveDefinitions(),
				func(call api.ToolCall) {
					if !a.canEarlyExecuteTool(call) {
						return
					}
					// 这里改成真正的异步提前启动：tool_use 一旦在流里闭合，
					// 就立刻开 goroutine 执行；正式工具循环只负责等待并复用结果。
					if _, exists := earlyExecutions[call.ID]; exists {
						return
					}
					resultCh := make(chan tools.Result, 1)
					earlyExecutions[call.ID] = resultCh
					go func(toolCall api.ToolCall) {
						resultCh <- a.ExecuteTool(ctx, tools.Invocation{
							Name:      toolCall.Name,
							Arguments: toolCall.Arguments,
						})
					}(call)
				},
			)
		} else {
			response, err = a.modelClient.Complete(ctx, a.messages, a.registry.ActiveDefinitions())
		}
		if !a.isSubAgent {
			ui.StopSpinner()
		}
		if err != nil {
			return lastAssistantText, err
		}

		a.recordUsage(response.Usage)
		a.lastAPICallTime = time.Now()

		// thinking 只用于终端展示，不进入正式 assistant 历史，
		// 这样可以先对齐源仓库“可见但不过度污染上下文”的主链行为。
		if !a.isSubAgent && strings.TrimSpace(response.Thinking) != "" {
			ui.PrintThinking(response.Thinking)
		}

		if strings.TrimSpace(response.Text) != "" {
			lastAssistantText = response.Text
		}

		a.ensureToolCallIDs(round, &response)
		a.appendMessage(a.buildAssistantMessage(response))

		if exceeded, reason := a.checkCostBudget(); exceeded {
			ui.PrintInfo("Budget exceeded: " + reason)
			return lastAssistantText, nil
		}

		if len(response.ToolCalls) == 0 {
			return lastAssistantText, nil
		}

		// 鎶婅繛缁殑骞跺彂瀹夊叏宸ュ叿鍒嗙粍鎴愭壒澶勭悊锛屽敖閲忚创杩戞簮浠撳簱鈥滆繛缁畨鍏ㄥ伐鍏峰苟鍙戙€佸叾浠栧伐鍏蜂覆琛屸€濈殑绛栫暐銆?
		for _, batch := range a.buildToolExecutionBatches(response.ToolCalls) {
			if batch.Concurrent {
				type concurrentToolResult struct {
					ToolCall api.ToolCall
					Result   tools.Result
				}
				results := make([]concurrentToolResult, len(batch.Calls))
				var waitGroup sync.WaitGroup
				waitGroup.Add(len(batch.Calls))

				for index, toolCall := range batch.Calls {
					go func(resultIndex int, currentCall api.ToolCall) {
						defer waitGroup.Done()
						results[resultIndex] = concurrentToolResult{
							ToolCall: currentCall,
							Result:   a.resolveToolExecutionResult(ctx, currentCall, earlyExecutions),
						}
					}(index, toolCall)
				}

				waitGroup.Wait()
				for _, item := range results {
					a.appendToolResultMessage(item.ToolCall, item.Result)
				}
				continue
			}

			for _, toolCall := range batch.Calls {
				a.appendToolResultMessage(toolCall, a.resolveToolExecutionResult(ctx, toolCall, earlyExecutions))
			}
		}
	}

	return lastAssistantText, fmt.Errorf("tool loop exceeded limit: %d", maxToolRoundsPerPrompt)
}

// startMemoryPrefetch 启动一次异步记忆预取。
func (a *Agent) startMemoryPrefetch(userInput string) *memoryPrefetch {
	if a.memoryStore == nil || !isSubstantialMemoryQuery(userInput) {
		return nil
	}
	if a.sessionMemoryBytes >= sessionMemoryBudget {
		return nil
	}

	files, err := a.memoryStore.List()
	if err != nil {
		return nil
	}

	hasMemories := false
	for _, file := range files {
		if strings.HasSuffix(strings.ToLower(file), ".md") && file != "MEMORY.md" {
			hasMemories = true
			break
		}
	}
	if !hasMemories {
		return nil
	}

	sideQuery := a.buildMemorySideQuery()
	if sideQuery == nil {
		return nil
	}

	resultCh := make(chan []memory.RelevantMemory, 1)
	errCh := make(chan error, 1)
	go func() {
		selected, selectErr := a.memoryStore.SelectRelevantMemories(userInput, sideQuery, a.surfacedMemoryPaths)
		if selectErr != nil {
			errCh <- selectErr
			return
		}
		resultCh <- selected
	}()

	return &memoryPrefetch{
		ResultCh: resultCh,
		ErrCh:    errCh,
		Consumed: false,
	}
}

// consumeMemoryPrefetch 以零等待方式轮询并消费预取结果。
// 如果语义预取尚未完成，本轮直接跳过；如果语义预取失败，则回退到同步关键词召回。
func (a *Agent) consumeMemoryPrefetch(prefetch *memoryPrefetch, userInput string, userMessageIndex int) {
	if prefetch == nil || prefetch.Consumed {
		return
	}

	select {
	case memories := <-prefetch.ResultCh:
		prefetch.Consumed = true
		if len(memories) == 0 {
			a.injectKeywordMemories(userInput, userMessageIndex)
			return
		}
		injection := memory.FormatRelevantInjection(memories)
		if strings.TrimSpace(injection) == "" {
			a.injectKeywordMemories(userInput, userMessageIndex)
			return
		}
		a.appendMemoryInjection(userMessageIndex, injection)
		for _, item := range memories {
			a.surfacedMemoryPaths[item.Entry.Path] = struct{}{}
			a.sessionMemoryBytes += len(item.Entry.Content)
		}
	case <-prefetch.ErrCh:
		prefetch.Consumed = true
		a.injectKeywordMemories(userInput, userMessageIndex)
	default:
		return
	}
}

// isSubstantialMemoryQuery 判断输入是否值得触发记忆预取。
// 这里兼顾中日韩文本和英文多词输入，避免短提示词把 side-query 预算浪费在低价值召回上。
func isSubstantialMemoryQuery(input string) bool {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return false
	}

	cjkCount := 0
	for _, r := range trimmed {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3040 && r <= 0x30FF) || (r >= 0xAC00 && r <= 0xD7AF) {
			cjkCount++
			if cjkCount >= 2 {
				return true
			}
		}
	}
	return strings.ContainsAny(trimmed, " \t\r\n")
}

// appendMemoryInjection 把记忆注入文本回写到指定的 user 消息。
// 这里不再盲目寻找“最后一条 user”，而是固定附着到当前轮次原始 user message，
// 避免 prefetch 在第一轮模型响应之后完成时把记忆挂到错误位置。
func (a *Agent) appendMemoryInjection(userMessageIndex int, injection string) {
	if strings.TrimSpace(injection) == "" {
		return
	}

	if userMessageIndex >= 0 && userMessageIndex < len(a.messages) && a.messages[userMessageIndex].Role == "user" {
		if strings.TrimSpace(a.messages[userMessageIndex].Content) == "" {
			updated := a.messages[userMessageIndex]
			updated.Content = injection
			// 记忆注入是“原位改写现有 user message”的典型路径，这里统一走 replace 入口收口 native 同步。
			a.replaceMessageAt(userMessageIndex, updated)
			return
		}
		updated := a.messages[userMessageIndex]
		updated.Content = strings.TrimSpace(updated.Content) + "\n\n" + injection
		// 记忆注入是“原位改写现有 user message”的典型路径，这里统一走 replace 入口收口 native 同步。
		a.replaceMessageAt(userMessageIndex, updated)
		return
	}

	// 只有在原始 user 索引已经失效时，才退回到追加新消息的兜底路径。
	a.appendMessage(api.Message{
		Role:    "user",
		Content: injection,
	})
}

// toolExecutionBatch 琛ㄧず涓€娆″伐鍏锋墽琛屾壒娆°€?

func (a *Agent) injectKeywordMemories(input string, userMessageIndex int) string {
	if a.memoryStore == nil || strings.TrimSpace(input) == "" {
		return ""
	}
	if a.sessionMemoryBytes >= sessionMemoryBudget {
		return ""
	}

	candidates, err := a.memoryStore.SearchRelevant(input, 3)
	if err != nil || len(candidates) == 0 {
		return ""
	}

	selected := make([]memory.Entry, 0, len(candidates))
	for _, candidate := range candidates {
		if _, exists := a.surfacedMemoryPaths[candidate.Path]; exists {
			continue
		}
		selected = append(selected, candidate)
	}
	if len(selected) == 0 {
		return ""
	}

	injection := memory.FormatInjection(selected)
	if strings.TrimSpace(injection) == "" {
		return ""
	}

	for _, entry := range selected {
		a.surfacedMemoryPaths[entry.Path] = struct{}{}
		a.sessionMemoryBytes += len(entry.Content)
	}
	a.appendMemoryInjection(userMessageIndex, injection)
	return injection
}

// buildMemorySideQuery 构造记忆召回专用的轻量旁路查询函数。
// 它复用当前用户已经选定的模型与 provider，避免为了选记忆再引入额外模型分支。
func (a *Agent) buildMemorySideQuery() memory.SideQueryFn {
	if a.modelClient == nil {
		return nil
	}

	return func(systemPrompt string, userMessage string) (string, error) {
		messages := []api.Message{
			{
				Role:    "system",
				Content: systemPrompt,
			},
			{
				Role:    "user",
				Content: userMessage,
			},
		}

		// 记忆选择只需要轻量文本响应，因此不暴露工具，避免 side-query 意外进入工具循环。
		response, err := a.modelClient.Complete(context.Background(), messages, nil)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(response.Text) != "" {
			return response.Text, nil
		}
		return response.RawContent, nil
	}
}

// recordUsage 绱 token 浣跨敤銆?

