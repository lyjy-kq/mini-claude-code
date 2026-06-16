// Package agent 验证 agent tool 与源码仓库对齐后的用户可见语义。
// 本文件专门覆盖子智能体失败与空输出占位文案，避免后续迁移把主链体验回退成通用工具错误。
package agent

import (
	"testing"

	"mini-claude-code/internal/config"
	"mini-claude-code/internal/tools"
)

// TestExecuteAgentToolFormatsSubAgentError 验证 agent tool 在子智能体失败时会回写主链同款的可读文本，而不是把整个 tool call 直接标记为错误。
// 这样模型可以基于“Sub-agent error: ...”决定是否修正子任务指令，而不会被动回退到通用 tool error 路径。
func TestExecuteAgentToolFormatsSubAgentError(t *testing.T) {
	root := t.TempDir()
	agent := New(Options{
		WorkingDirectory: root,
		Model:            "gpt-4o",
		RuntimeConfig: config.Config{
			OpenAIAPIKey:  "openai-key",
			OpenAIBaseURL: "https://openai.example.com",
		},
	})

	result := agent.executeAgentTool(tools.Invocation{
		Name: "agent",
		Arguments: map[string]string{
			"type":   "missing-agent",
			"prompt": "do something",
		},
	})
	if result.Error != nil {
		t.Fatalf("executeAgentTool() error = %v, want nil", result.Error)
	}
	if result.Output != "Sub-agent error: subagent type not found: missing-agent" {
		t.Fatalf("result.Output = %q", result.Output)
	}
}
