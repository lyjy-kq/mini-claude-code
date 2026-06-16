// Package cli 负责命令行参数解析和交互式 REPL。
// 这里保留源项目的使用习惯，同时用 Go 的标准库实现最小闭环，并补充 plan mode 切换入口。
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"

	"mini-claude-code/internal/agent"
	"mini-claude-code/internal/config"
	"mini-claude-code/internal/tools"
	"mini-claude-code/internal/ui"
)

// resumableRunner 抽象出 CLI 启动阶段恢复会话所需的最小能力。
// 这样既能令 main 和 REPL 共用同一套 resume 初始化逻辑，也便于为该行为补单元测试。
type resumableRunner interface {
	// WantsResume 表示当前是否启用了 --resume。
	WantsResume() bool
	// ResumeLatest 恢复最近一次会话，并返回用户可见的摘要文案。
	ResumeLatest() (string, error)
}

// ParsedArgs 表示命令行参数解析结果。
type ParsedArgs struct {
	// ShowHelp 表示是否输出帮助信息。
	ShowHelp bool
	// Prompt 表示命令行直接传入的一次性 prompt。
	Prompt string
	// Model 表示当前使用的模型名。
	Model string
	// PermissionMode 表示工具权限模式。
	PermissionMode tools.PermissionMode
	// Resume 表示是否恢复最近会话。
	Resume bool
	// Thinking 表示是否开启 thinking。
	Thinking bool
	// APIBaseURL 表示命令行临时覆盖的 OpenAI-compatible 基础地址。
	// 这用于对齐源码仓库的 --api-base 入口，让用户无需改环境变量也能切换兼容后端。
	APIBaseURL string
	// MaxTurns 表示轮次上限。
	MaxTurns int
	// MaxCostUSD 表示成本上限。
	MaxCostUSD float64
	// WorkingDirectory 表示默认工作目录。
	WorkingDirectory string
}

// ParseArgs 负责把原始命令行参数转换为统一结构。
func ParseArgs(args []string, runtime config.Config) (ParsedArgs, error) {
	parsed := ParsedArgs{
		Model:            runtime.DefaultModel,
		PermissionMode:   tools.PermissionDefault,
		WorkingDirectory: runtime.WorkingDirectory,
	}

	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			parsed.ShowHelp = true
		case "--yolo", "-y":
			parsed.PermissionMode = tools.PermissionBypass
		case "--plan":
			parsed.PermissionMode = tools.PermissionPlan
		case "--accept-edits":
			parsed.PermissionMode = tools.PermissionAcceptEdits
		case "--dont-ask":
			parsed.PermissionMode = tools.PermissionDontAsk
		case "--thinking":
			parsed.Thinking = true
		case "--resume":
			parsed.Resume = true
		case "--api-base":
			i++
			if i >= len(args) {
				return ParsedArgs{}, fmt.Errorf("--api-base requires a value")
			}
			parsed.APIBaseURL = args[i]
		case "--model", "-m":
			i++
			if i >= len(args) {
				return ParsedArgs{}, fmt.Errorf("--model requires a value")
			}
			parsed.Model = args[i]
		case "--max-turns":
			i++
			if i >= len(args) {
				return ParsedArgs{}, fmt.Errorf("--max-turns requires a value")
			}
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return ParsedArgs{}, fmt.Errorf("invalid --max-turns value: %w", err)
			}
			parsed.MaxTurns = value
		case "--max-cost":
			i++
			if i >= len(args) {
				return ParsedArgs{}, fmt.Errorf("--max-cost requires a value")
			}
			value, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				return ParsedArgs{}, fmt.Errorf("invalid --max-cost value: %w", err)
			}
			parsed.MaxCostUSD = value
		default:
			positional = append(positional, args[i])
		}
	}

	parsed.Prompt = strings.Join(positional, " ")
	return parsed, nil
}

// HelpText 返回 CLI 帮助信息。
func HelpText() string {
	return `Usage: mini-claude [options] [prompt]

Options:
  --yolo, -y          Skip all confirmation prompts (bypassPermissions mode)
  --plan              Plan mode: read-only, describe changes without executing
  --accept-edits      Auto-approve file edits, still confirm dangerous shell
  --dont-ask          Auto-deny anything needing confirmation (for CI)
  --thinking          Enable extended thinking (Anthropic only)
  --model, -m         Model to use (default: claude-opus-4-6, or MINI_CLAUDE_MODEL env)
  --api-base URL      Use an OpenAI-compatible API endpoint
  --resume            Resume the last session
  --max-cost USD      Stop when estimated cost exceeds this amount
  --max-turns N       Stop after N agentic turns
  --help, -h          Show this help

REPL commands:
  /clear              Clear conversation history
  /plan               Toggle plan mode (read-only ↔ normal)
  /cost               Show token usage and cost
  /compact            Manually compact conversation
  /memory             List saved memories
  /skills             List available skills
  /<skill-name>       Invoke a skill (e.g. /commit "fix types")
  /status
  /resume
  /resume <session_id>
  /sessions
  /mcp
  /plan approve
  /plan clear
  /plan manual
  /plan exit
  /tools
  /agent <type> <task>
  /read <path>
  /write <path> <content>
  /ls [path] [pattern]
  /grep <path> <pattern> [include]
  /fetch <url> [max_length]
  /shell <command>
  exit

Examples:
  mini-claude "fix the bug in src/app.ts"
  mini-claude --yolo "run all tests and fix failures"
  mini-claude --plan "how would you refactor this?"
  mini-claude --accept-edits "add error handling to api.ts"
  mini-claude --max-cost 0.50 --max-turns 20 "implement feature X"
  mini-claude --api-base https://aihubmix.com/v1 --model gpt-4o "hello"
  mini-claude --resume
  mini-claude  # starts interactive REPL
`
}

// InitializeResumeForCLI 在 CLI 启动阶段执行一次统一的 resume 初始化。
// 这里把恢复时机提升到入口层，确保单次 prompt 和 REPL 都能复用上一轮会话，而不是只在 REPL 模式生效。
func InitializeResumeForCLI(runner resumableRunner) (string, error) {
	if runner == nil || !runner.WantsResume() {
		return "", nil
	}
	return runner.ResumeLatest()
}

// ResolveRuntimeConfigForCLI 负责把命令行参数和环境配置合成为最终启动配置。
// 这里对齐源码仓库的启动语义：优先解析可用 API key 与后端类型，并在缺少 key 时直接返回错误，而不是静默退回 mock。
func ResolveRuntimeConfigForCLI(parsed ParsedArgs, runtime config.Config) (config.Config, error) {
	resolved := runtime

	// 命令行的 api-base 只覆盖当前进程的 OpenAI-compatible 基础地址，
	// 这样既能临时切换兼容后端，也不会改写用户原有环境变量。
	if strings.TrimSpace(parsed.APIBaseURL) != "" {
		resolved.OpenAIBaseURL = strings.TrimSpace(parsed.APIBaseURL)
	}

	// 只要用户显式传了 --api-base，就优先按 OpenAI-compatible 语义解析。
	// 这直接对齐源码仓库的行为：命令行显式指定兼容网关时，应覆盖默认的 Anthropic 路径选择。
	if strings.TrimSpace(parsed.APIBaseURL) != "" {
		fallbackKey := strings.TrimSpace(runtime.OpenAIAPIKey)
		if fallbackKey == "" {
			fallbackKey = strings.TrimSpace(runtime.AnthropicAPIKey)
		}
		if fallbackKey == "" {
			return config.Config{}, fmt.Errorf(
				"API key is required.\n  Set ANTHROPIC_API_KEY (+ optional ANTHROPIC_BASE_URL),\n  or OPENAI_API_KEY (+ optional OPENAI_BASE_URL / --api-base).",
			)
		}

		resolved.OpenAIAPIKey = fallbackKey
		resolved.AnthropicAPIKey = ""
		resolved.AnthropicBaseURL = ""
		return resolved, nil
	}

	// 对齐源码仓库优先级：
	// 1. OPENAI_API_KEY + OPENAI_BASE_URL
	// 2. ANTHROPIC_API_KEY（可带可不带 ANTHROPIC_BASE_URL）
	// 3. OPENAI_API_KEY（可带可不带 OPENAI_BASE_URL）
	// 4. 其余情况由当前环境变量决定。
	switch {
	case strings.TrimSpace(runtime.OpenAIAPIKey) != "" && strings.TrimSpace(runtime.OpenAIBaseURL) != "":
		resolved.OpenAIAPIKey = runtime.OpenAIAPIKey
		if strings.TrimSpace(parsed.APIBaseURL) == "" {
			resolved.OpenAIBaseURL = runtime.OpenAIBaseURL
		}
		resolved.AnthropicAPIKey = ""
		resolved.AnthropicBaseURL = ""
	case strings.TrimSpace(runtime.AnthropicAPIKey) != "":
		resolved.AnthropicAPIKey = runtime.AnthropicAPIKey
		resolved.AnthropicBaseURL = runtime.AnthropicBaseURL
		resolved.OpenAIAPIKey = ""
		if strings.TrimSpace(parsed.APIBaseURL) == "" {
			resolved.OpenAIBaseURL = ""
		}
	case strings.TrimSpace(runtime.OpenAIAPIKey) != "":
		resolved.OpenAIAPIKey = runtime.OpenAIAPIKey
		if strings.TrimSpace(parsed.APIBaseURL) == "" {
			resolved.OpenAIBaseURL = runtime.OpenAIBaseURL
		}
		resolved.AnthropicAPIKey = ""
		resolved.AnthropicBaseURL = ""
	}

	if strings.TrimSpace(resolved.OpenAIAPIKey) == "" && strings.TrimSpace(resolved.AnthropicAPIKey) == "" {
		return config.Config{}, fmt.Errorf(
			"API key is required.\n  Set ANTHROPIC_API_KEY (+ optional ANTHROPIC_BASE_URL),\n  or OPENAI_API_KEY (+ optional OPENAI_BASE_URL / --api-base).",
		)
	}

	return resolved, nil
}

// RunREPL 启动交互式命令行循环。
func RunREPL(runner *agent.Agent) error {
	ui.PrintWelcome()

	scanner := bufio.NewScanner(os.Stdin)
	// 复用当前 REPL 的 scanner 处理高风险工具确认，避免额外创建输入循环。
	// 这样模型触发和手工触发的确认都能走同一条 Allow? (y/n) 交互链。
	runner.SetConfirmFn(func(message string) bool {
		ui.PrintConfirmation(strings.TrimSpace(message))
		for {
			fmt.Print("  Allow? (y/n): ")
			if !scanner.Scan() {
				return false
			}

			answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
			switch answer {
			case "y", "yes":
				return true
			case "n", "no":
				return false
			default:
				ui.PrintError("please enter y or n")
			}
		}
	})
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	// 空闲状态下连续两次 Ctrl+C 才退出；如果当前正在处理，则第一次就中断当前轮。
	// 这里用原子计数是为了让信号 goroutine 与主 REPL 循环安全共享退出计数状态。
	var idleInterruptCount int32
	go func() {
		for range interrupts {
			if runner.IsProcessing() {
				runner.Abort()
				atomic.StoreInt32(&idleInterruptCount, 0)
				ui.PrintInfo("Interrupted current run.")
				continue
			}

			if atomic.AddInt32(&idleInterruptCount, 1) >= 2 {
				fmt.Println()
				os.Exit(0)
			}
			ui.PrintInfo("Press Ctrl+C again to exit.")
			ui.PrintPrompt()
		}
	}()
	for {
		ui.PrintPrompt()
		if !scanner.Scan() {
			return scanner.Err()
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			return nil
		}
		atomic.StoreInt32(&idleInterruptCount, 0)

		if err := handleCommand(scanner, runner, input); err == nil {
			continue
		}

		if err := runner.HandlePrompt(input); err != nil {
			if errors.Is(err, context.Canceled) {
				continue
			}
			ui.PrintError(err.Error())
		}
	}
}