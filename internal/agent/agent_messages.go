// agent_messages.go 负责消息管理，包括消息追加/替换/重置和原生消息同步。
// 该文件从 agent.go 拆分而来。
package agent

import (
	"fmt"
	"strings"

	"mini-claude-code/internal/api"
)
func (a *Agent) appendMessage(message api.Message) {
	a.messages = append(a.messages, message)
	a.appendNativeProviderMessage(message)
	a.providerSnapshot = a.currentProviderSnapshot()
}

// replaceMessageAt 统一替换指定索引的消息，并同步重建 native provider 运行态。
// 对于 memory injection 这类“原位修改消息正文”的路径，当前先用局部封装收口，避免分散手写 refresh 逻辑。
func (a *Agent) replaceMessageAt(index int, message api.Message) {
	if index < 0 || index >= len(a.messages) {
		return
	}
	a.messages[index] = message
	a.refreshProviderSnapshot()
}

// resetMessagesWithSystem 重建以 system prompt 为边界的统一消息历史，并同步 native provider 运行态。
// 这条入口服务于 system prompt 重建和 clear-context 类场景，减少直接改数组后再外部补 refresh 的分叉。
func (a *Agent) resetMessagesWithSystem(messages []api.Message) {
	a.messages = messages
	a.resetMemoryRecallState()
	a.refreshProviderSnapshot()
}

// appendNativeProviderMessage 增量维护 provider-native 运行态。
// 当前先覆盖 user / assistant / tool 三类主链高频消息，让运行态逐步向 live native stack 靠拢。
func (a *Agent) appendNativeProviderMessage(message api.Message) {
	openAIMessage := api.BuildProviderMessageSnapshot([]api.Message{message}).OpenAI
	if len(openAIMessage) > 0 {
		a.openAINativeMessages = append(a.openAINativeMessages, openAIMessage...)
	}

	anthropicSnapshot := api.BuildProviderMessageSnapshot([]api.Message{message})
	if strings.TrimSpace(anthropicSnapshot.AnthropicSystem) != "" && message.Role == "system" {
		a.anthropicSystemPrompt = anthropicSnapshot.AnthropicSystem
	}
	if len(anthropicSnapshot.AnthropicMessages) > 0 {
		a.anthropicNativeMessages = append(a.anthropicNativeMessages, anthropicSnapshot.AnthropicMessages...)
	}
}

// canEarlyExecuteTool 判断一个工具是否适合在流式阶段提前启动。
// 这里刻意保持保守：只有读工具，以及已经被权限系统判定为可自动放行的调用才会提前执行，
// 从而把“更快”建立在“行为不变”和“不会跳过确认”之上。
func (a *Agent) buildAssistantMessage(response api.Response) api.Message {
	content := strings.TrimSpace(response.RawContent)
	if content == "" {
		content = strings.TrimSpace(response.Text)
	}

	// 当后端只返回 tool_calls 且没有正文时，不再把整段 JSON bridge 塞进 assistant 文本。
	// 统一历史里真正驱动后续工具循环的是 ToolCalls 结构本身；这里保留一条轻量占位文本，
	// 让持久化/恢复链继续可读，同时更贴近源仓库“原生 assistant message 本身可为空文本”的形态。
	if content == "" && len(response.ToolCalls) > 0 {
		content = "[Assistant issued tool calls.]"
	}

	return api.Message{
		Role:      "assistant",
		Content:   content,
		ToolCalls: response.ToolCalls,
	}
}

// ensureToolCallIDs 涓哄伐鍏疯皟鐢ㄨˉ榻愮ǔ瀹?id銆?
func (a *Agent) ensureToolCallIDs(round int, response *api.Response) {
	for index := range response.ToolCalls {
		if strings.TrimSpace(response.ToolCalls[index].ID) != "" {
			continue
		}
		response.ToolCalls[index].ID = fmt.Sprintf("tool-%d-%d", round+1, index+1)
	}
}

// ensureReadBeforeWrite 鍦ㄥ啓鏂囦欢鍓嶇‘淇濆凡缁忚鍙栬繃鐩爣鏂囦欢銆?


