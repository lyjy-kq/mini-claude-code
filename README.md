# Mini Claude Code (Go)

这是一个按 `claude-code-from-scratch` 思路持续迁移的 Go 版 Coding Agent 项目。

当前已经完成的核心能力：

1. Go 项目基础结构、CLI 与 REPL 骨架。
2. 工作区检查、`./docx` spec 校验，以及中文注释规范接入。
3. 技能、子智能体、MCP、记忆等外围模块的最小发现骨架。
4. 本地工具注册与执行入口，包含：
   - `read_file`
   - `write_file`
   - `edit_file`
   - `list_files`
   - `grep_search`
   - `run_shell`
   - `skill`
   - `web_fetch`
   - `agent`
5. 文件工具能力增强：
   - `read_file` 支持带行号输出。
   - `write_file` 支持写入预览。
   - `edit_file` 支持最小 diff 回显与引号归一化匹配。
   - `list_files` 支持 glob 模式。
   - `grep_search` 支持 regex + include 过滤。
   - `web_fetch` 支持基础网页抓取与 HTML 转纯文本。
6. Plan Mode 基础切换与“仅允许写 plan 文件”的权限例外。
7. 最小可用的模型工具调用闭环：
   - 维护完整消息历史。
   - 支持 OpenAI-compatible 原生 `tools` 下发。
   - 支持原生 `tool_calls` 响应解析。
   - 兼容 JSON 文本 bridge 协议回退。
   - `tool_search` 支持激活 deferred tools，并返回完整 schema 定义。
8. API 协议层已开始进入 provider 分流：
    - 请求侧会发送 `tools`。
    - 支持 `stream: true` 与 `stream_options.include_usage`。
    - 支持流式累积 `delta.tool_calls[].function.arguments`。
    - 不支持流式的兼容后端会自动回退到普通 completions。
    - OpenAI-compatible 继续走原生 `tools` + streaming。
    - Anthropic 已接入最小可执行的 `messages` API 路径，包含非流式与基础 streaming / `tool_use` 增量拼装。
    - Agent -> API 已接通 Anthropic `thinking` 模式解析与 `max_output_tokens` 映射入口，可按模型解析为 `adaptive / enabled / disabled`。
    - Anthropic 的 thinking 文本现已支持单独展示，且不会写回正式 assistant 历史，避免污染后续上下文主链。
    - Anthropic 已接入 `content_block_stop` 级别的最小流式工具提前执行链路，当前先对自动放行的只读工具生效。
9. 工具元数据层已开始向原生 function-calling 对齐：
   - 核心工具已补齐最小 `InputSchema`。
   - prompt 会把工具参数结构一起暴露给模型。
10. 会话持久化已升级到“消息历史 + 运行态”：
    - 保存 `system/user/assistant/tool` 完整消息历史。
    - 同步保存 OpenAI-compatible / Anthropic 的 provider-specific 原生消息快照。
    - 当统一消息历史缺失时，可按当前 provider 优先使用对应快照做恢复兜底。
    - 保存 token、轮次、上下文窗口、压缩状态、`read-before-edit` 时间戳。
    - 保存记忆注入去重状态与会话记忆体积预算。
    - REPL 支持 `/sessions` 查看归档会话摘要。
    - REPL 支持 `/resume <session_id>` 恢复指定归档会话。
11. Prompt 构造已支持：
    - 递归解析 `CLAUDE.md` 与 `.claude/rules/*.md` 中的 `@include`。
    - memory / skills / agents / deferred tools 段落注入。
    - 原生 tool-calling 优先、JSON bridge 回退的双通道提示。
12. 编辑安全约束已具备最小 `read-before-edit` 闭环：
    - 读取文件后记录 `mtime`。
    - 写入或编辑已有文件前要求先读。
    - 文件在读取后被外部修改时，要求重新读取再编辑。
13. 子智能体骨架已从“只发现定义”推进到“可最小执行”：
    - 内置 `explore`、`plan`、`general` 三类子智能体配置。
    - REPL 提供 `/agent <type> <task>` 入口。
    - 模型主链路可通过 `agent` 工具直接委托子智能体。
14. usage / 预算 / 状态链已接通：
    - 累计输入输出 token。
    - 支持 `--max-cost` 成本预算。
    - `/cost` 与 `/status` 可查看 token、估算成本、context window、记忆状态等信息。
15. 上下文压缩主链已接入：
    - `budgetToolResults`
    - `snipStaleResults`
    - `snipStaleResults` 会优先裁剪同一路径的重复 `read_file` 旧结果
    - `microcompact`
    - 基于模型摘要的 `compactConversation`
    - compact / clear / restore / system prompt 重建后会同步刷新 provider-specific 原生消息快照
    - 运行时的 budget / snip / microcompact 压缩层一旦改写消息历史，也会同步刷新 provider-specific 原生消息快照
    - 子智能体运行态同样维护 provider-specific 原生消息快照
    - REPL 支持 `/compact`
16. memory recall 已接入主链：
    - 本地记忆采用标准 frontmatter 保存。
    - 每轮用户输入前会按轻量文本相关性检索记忆。
    - 相关记忆会直接注入当前 user message。
    - 会话内会记录已注入记忆路径，避免重复注入。
17. 大工具结果已支持落盘与可回读预览：
    - 超过阈值的工具结果会写入 `.mini-claude/tool-results/`
    - 上下文中仅保留预览与文件路径
    - 模型后续可通过 `read_file` 回读完整结果

当前尚未完成的迁移部分：

1. 更完整的 Anthropic 原生 function-calling / thinking 对齐，当前虽已接入基础 thinking 配置入口、thinking 展示，以及保守版 streaming tool 提前执行，但仍缺少与源仓库一致的并发安全工具全集策略、thinking 样式透传和更高保真的 provider-specific 历史结构细节。
2. provider-specific 消息栈虽已进入 API 分流，session 也已落盘并开始用于恢复兜底，但 Agent 层压缩、主调度与常态恢复仍主要采用统一 `[]api.Message` 主链。
3. memory recall 的 side-query 语义筛选与 freshness 提示。
4. 更完整的并发工具执行覆盖面与更细粒度的早启动执行时机控制，当前仅完成最小白名单版。
5. 更完整的子智能体生命周期、预算回流与可观测性。
6. MCP JSON-RPC 转发与真实工具接入。
7. 更高保真的 compact / restore 行为细节，尤其是 provider 细分后的结构约束。

当前环境未检测到 `go` 命令，因此本项目尚未执行：

- `go build`
- `go fmt`
- `go test`

现阶段结论基于静态审查与结构迁移。

## 目录

```text
cmd/mini-claude
internal/agent
internal/api
internal/cli
internal/config
internal/contextx
internal/mcp
internal/memory
internal/prompt
internal/session
internal/skills
internal/spec
internal/subagent
internal/tools
internal/ui
internal/workspace
docx
```

## 下一步

建议继续按以下顺序推进：

1. 继续补全 Anthropic streaming / tool_use / thinking 真路径。
2. 把 memory recall 从轻量文本命中提升到 side-query + 语义筛选。
3. 继续提升 compact / restore 的 provider-specific 一致性。
4. 在具备 Go 环境后执行 `go fmt`、`go build`、`go test` 做结构收敛。
