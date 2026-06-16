// Package main 是 Go 版 mini claude 的命令行入口。
// 本文件负责串联参数解析、Agent 创建以及 REPL/单次任务执行流程。
package main

import (
	"fmt"
	"os"

	"mini-claude-code/internal/agent"
	"mini-claude-code/internal/cli"
	"mini-claude-code/internal/config"
	"mini-claude-code/internal/ui"
)

// main 负责初始化运行时配置，并按用户输入进入单次执行或 REPL 模式。
func main() {
	// 先解析配置与命令行，确保后续模块拿到统一的运行参数。
	runtimeConfig := config.Load()
	parsed, err := cli.ParseArgs(os.Args[1:], runtimeConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	// 当用户显式请求帮助时，直接输出帮助并结束进程。
	if parsed.ShowHelp {
		fmt.Println(cli.HelpText())
		return
	}

	// 在 CLI 入口统一解析最终运行配置，提前校验 API key / base URL 组合是否可用。
	// 这样可避免当前 Go 版静默退回 mock client，更贴近源仓库“启动即明确后端语义”的行为。
	resolvedRuntimeConfig, err := cli.ResolveRuntimeConfigForCLI(parsed, runtimeConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	// 统一在入口处创建 Agent，避免交互逻辑分散到多个调用方。
	runner := agent.New(agent.Options{
		WorkingDirectory: parsed.WorkingDirectory,
		Model:            parsed.Model,
		PermissionMode:   parsed.PermissionMode,
		Resume:           parsed.Resume,
		MaxTurns:         parsed.MaxTurns,
		MaxCostUSD:       parsed.MaxCostUSD,
		Thinking:         parsed.Thinking,
		RuntimeConfig:    resolvedRuntimeConfig,
	})

	// 统一在 CLI 入口阶段处理 --resume，确保单次 prompt 与 REPL 都复用同一条恢复链。
	// 这样可避免当前 Go 版“只有进入 REPL 才会恢复”的行为偏差。
	resumeSummary, err := cli.InitializeResumeForCLI(runner)
	if err != nil {
		ui.PrintError(err.Error())
		os.Exit(1)
	}
	if resumeSummary != "" {
		ui.PrintInfo(resumeSummary)
	}

	// 单次 prompt 与 REPL 是两种不同交互模式，这里统一分流。
	if parsed.Prompt != "" {
		if err := runner.HandlePrompt(parsed.Prompt); err != nil {
			ui.PrintError(err.Error())
			os.Exit(1)
		}
		return
	}

	if err := cli.RunREPL(runner); err != nil {
		ui.PrintError(err.Error())
		os.Exit(1)
	}
}
