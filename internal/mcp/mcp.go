// Package mcp 负责加载、连接和转发本地 MCP 服务。
// 本次迁移把 Go 版从“只能读取配置骨架”推进到“可以合并配置、建立 stdio JSON-RPC 连接、
// 发现 MCP 工具并按 `mcp__server__tool` 前缀转发调用”的最小可运行闭环，
// 让整体行为更贴近源仓库 `claude-code-from-scratch/src/mcp.ts`。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// requestTimeout 表示单次 MCP initialize、tools/list、tools/call 的默认超时时间。
	requestTimeout = 15 * time.Second
)

// ServerConfig 表示单个 MCP server 的启动配置。
type ServerConfig struct {
	// Command 表示要启动的可执行命令。
	Command string `json:"command"`
	// Args 表示启动命令的参数列表。
	Args []string `json:"args"`
	// Env 表示额外注入到子进程的环境变量。
	Env map[string]string `json:"env"`
}

// ToolInfo 表示单个 MCP 工具的元数据。
type ToolInfo struct {
	// Name 表示 MCP server 暴露的原始工具名。
	Name string
	// Description 表示工具说明。
	Description string
	// InputSchema 表示工具参数 schema。
	InputSchema map[string]any
	// ServerName 表示工具所属的 MCP server 名称。
	ServerName string
}

// Config 表示归一化后的 MCP 根配置。
type Config struct {
	// Servers 表示最终合并得到的 MCP server 列表。
	Servers map[string]ServerConfig
}

// Manager 表示 MCP 配置与连接管理器。
type Manager struct {
	// root 表示当前项目根目录，用于解析 `.claude/settings.json` 与 `.mcp.json`。
	root string
	// mu 保护连接状态、配置缓存和工具缓存。
	mu sync.Mutex
	// loadedConfig 缓存最近一次合并得到的配置。
	loadedConfig Config
	// connections 保存已经成功连接的 server。
	connections map[string]*connection
	// tools 缓存所有已发现的 MCP 工具。
	tools []ToolInfo
	// lastErrors 记录最近一次连接/发现过程中每个 server 的失败原因，
	// 让上层可以把静默失败转成用户可见的诊断信息。
	lastErrors map[string]string
	// connected 标记当前 manager 是否已经完成连接初始化。
	connected bool
}

// connection 表示一个 MCP server 的 stdio JSON-RPC 会话。
type connection struct {
	// serverName 表示当前连接绑定的 server 名称。
	serverName string
	// config 表示当前连接使用的启动配置。
	config ServerConfig
	// command 表示底层子进程句柄。
	command *exec.Cmd
	// stdin 表示发起 JSON-RPC 请求的写入端。
	stdin io.WriteCloser
	// pendingMu 保护 pending 请求映射。
	pendingMu sync.Mutex
	// pending 保存尚未返回的 JSON-RPC 请求。
	pending map[int64]chan rpcResponse
	// nextID 负责分配 JSON-RPC request id。
	nextID int64
}

// rpcRequest 表示标准 JSON-RPC 2.0 请求。
type rpcRequest struct {
	// JSONRPC 固定为 "2.0"。
	JSONRPC string `json:"jsonrpc"`
	// ID 表示请求 id；通知消息可省略。
	ID *int64 `json:"id,omitempty"`
	// Method 表示调用的方法名。
	Method string `json:"method"`
	// Params 表示方法参数。
	Params any `json:"params,omitempty"`
}

// rpcError 表示 JSON-RPC 错误对象。
type rpcError struct {
	// Code 表示错误码。
	Code int `json:"code"`
	// Message 表示错误消息。
	Message string `json:"message"`
}

// rpcResponse 表示标准 JSON-RPC 2.0 响应。
type rpcResponse struct {
	// JSONRPC 固定为 "2.0"。
	JSONRPC string `json:"jsonrpc"`
	// ID 表示对应请求 id。
	ID *int64 `json:"id,omitempty"`
	// Result 表示成功返回的数据。
	Result json.RawMessage `json:"result,omitempty"`
	// Error 表示失败返回的错误。
	Error *rpcError `json:"error,omitempty"`
}

// initializeResult 表示 initialize 返回值的最小结构。
type initializeResult struct {
	// ProtocolVersion 表示服务端接受的协议版本。
	ProtocolVersion string `json:"protocolVersion"`
}

// listToolsResult 表示 tools/list 返回值的最小结构。
type listToolsResult struct {
	// Tools 表示服务端暴露的工具列表。
	Tools []struct {
		// Name 表示工具名。
		Name string `json:"name"`
		// Description 表示工具说明。
		Description string `json:"description"`
		// InputSchema 表示工具参数 schema。
		InputSchema map[string]any `json:"inputSchema"`
	} `json:"tools"`
}

// callToolResult 表示 tools/call 返回值的最小结构。
type callToolResult struct {
	// Content 表示 MCP 结果内容块数组。
	Content []struct {
		// Type 表示内容块类型。
		Type string `json:"type"`
		// Text 表示文本内容块正文。
		Text string `json:"text"`
	} `json:"content"`
}

// NewManager 创建 MCP 管理器。
func NewManager(root string) *Manager {
	return &Manager{
		root:        root,
		connections: map[string]*connection{},
		lastErrors:  map[string]string{},
		tools:       []ToolInfo{},
	}
}

// LoadConfig 读取并合并 MCP 配置。
// 这里对齐源仓库的配置来源顺序：先全局 `~/.claude/settings.json`，再项目 `.claude/settings.json`，最后 `.mcp.json`。
func (m *Manager) LoadConfig() (Config, error) {
	cfg := Config{Servers: map[string]ServerConfig{}}
	for _, candidate := range m.configCandidates() {
		m.mergeConfigFile(candidate, cfg.Servers)
	}

	m.mu.Lock()
	m.loadedConfig = cfg
	m.mu.Unlock()
	return cfg, nil
}

// ConnectAndDiscover 连接所有已配置的 MCP server，并发现可用工具。
// 这一步完成后，调用方就可以把 `mcp__server__tool` 工具挂到注册表里继续走统一调用链。
func (m *Manager) ConnectAndDiscover(ctx context.Context) ([]ToolInfo, error) {
	m.mu.Lock()
	if m.connected {
		toolsCopy := append([]ToolInfo(nil), m.tools...)
		m.mu.Unlock()
		return toolsCopy, nil
	}
	m.mu.Unlock()

	cfg, err := m.LoadConfig()
	if err != nil {
		// 配置文件缺失不应阻断主链，这里只在真正解析失败时向上抛。
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(cfg.Servers) == 0 {
		return nil, nil
	}

	connectedTools := make([]ToolInfo, 0, 8)
	connectedMap := make(map[string]*connection, len(cfg.Servers))
	lastErrors := make(map[string]string, len(cfg.Servers))
	for serverName, serverConfig := range cfg.Servers {
		conn, connectErr := newConnection(serverName, serverConfig)
		if connectErr != nil {
			lastErrors[serverName] = connectErr.Error()
			continue
		}
		if initErr := conn.initialize(ctx); initErr != nil {
			lastErrors[serverName] = initErr.Error()
			conn.close()
			continue
		}
		serverTools, listErr := conn.listTools(ctx)
		if listErr != nil {
			lastErrors[serverName] = listErr.Error()
			conn.close()
			continue
		}
		connectedMap[serverName] = conn
		connectedTools = append(connectedTools, serverTools...)
	}

	m.mu.Lock()
	m.connections = connectedMap
	m.tools = connectedTools
	m.lastErrors = lastErrors
	m.connected = true
	toolsCopy := append([]ToolInfo(nil), m.tools...)
	m.mu.Unlock()
	return toolsCopy, nil
}

// ToolDefinitions 返回可直接注入到工具注册表的 MCP 工具定义。
func (m *Manager) ToolDefinitions() []ToolInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ToolInfo(nil), m.tools...)
}

// Snapshot 表示当前 MCP 运行态的只读快照。
// 这里统一暴露配置、连接、已发现工具和错误，便于 CLI 做状态展示。
type Snapshot struct {
	// ConfiguredServers 表示当前配置里声明过的 server 列表。
	ConfiguredServers []string
	// ConnectedServers 表示当前成功建立连接的 server 列表。
	ConnectedServers []string
	// ToolCount 表示当前已发现的 MCP 工具数量。
	ToolCount int
	// Errors 表示最近一次连接/发现过程中记录下来的 server 级错误。
	Errors map[string]string
}

// StatusSnapshot 返回当前 MCP 运行态快照。
// 这样 Agent 无需读取 Manager 内部状态，就能向用户展示可诊断信息。
func (m *Manager) StatusSnapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	configured := make([]string, 0, len(m.loadedConfig.Servers))
	for name := range m.loadedConfig.Servers {
		configured = append(configured, name)
	}
	connected := make([]string, 0, len(m.connections))
	for name := range m.connections {
		connected = append(connected, name)
	}
	sort.Strings(configured)
	sort.Strings(connected)

	errorsCopy := make(map[string]string, len(m.lastErrors))
	for name, message := range m.lastErrors {
		errorsCopy[name] = message
	}

	return Snapshot{
		ConfiguredServers: configured,
		ConnectedServers:  connected,
		ToolCount:         len(m.tools),
		Errors:            errorsCopy,
	}
}

// IsMCPToolName 判断一个工具名是否属于 MCP 前缀工具。
func (m *Manager) IsMCPToolName(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "mcp__")
}

// CallTool 把 `mcp__server__tool` 前缀工具路由到对应 server。
func (m *Manager) CallTool(ctx context.Context, prefixedName string, arguments map[string]string) (string, error) {
	serverName, toolName, err := splitPrefixedToolName(prefixedName)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	conn := m.connections[serverName]
	m.mu.Unlock()
	if conn == nil {
		return "", fmt.Errorf("mcp server not connected: %s", serverName)
	}
	return conn.callTool(ctx, toolName, arguments)
}

// DisconnectAll 主动断开所有 MCP 连接。
func (m *Manager) DisconnectAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, conn := range m.connections {
		conn.close()
	}
	m.connections = map[string]*connection{}
	m.tools = nil
	m.connected = false
}

// configCandidates 返回当前 manager 需要按顺序合并的配置文件路径。
func (m *Manager) configCandidates() []string {
	paths := make([]string, 0, 3)
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".claude", "settings.json"))
	}
	paths = append(paths, filepath.Join(m.root, ".claude", "settings.json"))
	paths = append(paths, filepath.Join(m.root, ".mcp.json"))
	return paths
}

// mergeConfigFile 把单个配置文件中的 mcpServers 合并到目标 map。
// 后出现的配置会覆盖前面的同名 server，保持和源仓库一致的“项目优先于全局”策略。
func (m *Manager) mergeConfigFile(filePath string, target map[string]ServerConfig) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	serversSource := raw
	if nested, ok := raw["mcpServers"].(map[string]any); ok {
		serversSource = nested
	}
	for name, value := range serversSource {
		serverMap, ok := value.(map[string]any)
		if !ok {
			continue
		}
		normalized, normalizeOK := normalizeServerConfig(serverMap)
		if !normalizeOK {
			continue
		}
		target[name] = normalized
	}
}

// normalizeServerConfig 把弱类型配置转换成结构化的 server 配置。
func normalizeServerConfig(raw map[string]any) (ServerConfig, bool) {
	command := strings.TrimSpace(stringValue(raw["command"]))
	if command == "" {
		return ServerConfig{}, false
	}

	config := ServerConfig{
		Command: command,
		Args:    []string{},
		Env:     map[string]string{},
	}
	if args, ok := raw["args"].([]any); ok {
		for _, item := range args {
			argument := strings.TrimSpace(stringValue(item))
			if argument != "" {
				config.Args = append(config.Args, argument)
			}
		}
	}
	if envMap, ok := raw["env"].(map[string]any); ok {
		for key, value := range envMap {
			config.Env[key] = stringValue(value)
		}
	}
	return config, true
}

// newConnection 创建并启动一个新的 MCP 连接。
func newConnection(serverName string, config ServerConfig) (*connection, error) {
	command := exec.Command(config.Command, config.Args...)
	command.Dir = ""
	command.Env = os.Environ()
	for key, value := range config.Env {
		command.Env = append(command.Env, key+"="+value)
	}

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}

	conn := &connection{
		serverName: serverName,
		config:     config,
		command:    command,
		stdin:      stdin,
		pending:    map[int64]chan rpcResponse{},
	}
	go conn.consumeStdout(stdout)
	go conn.watchProcessExit()
	return conn, nil
}

// initialize 完成 MCP initialize 握手。
func (c *connection) initialize(ctx context.Context) error {
	var result initializeResult
	if err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "mini-claude",
			"version": "1.0.0",
		},
	}, &result); err != nil {
		return err
	}
	return c.notify("notifications/initialized", map[string]any{})
}

// listTools 获取当前 server 暴露的所有工具定义。
func (c *connection) listTools(ctx context.Context) ([]ToolInfo, error) {
	var result listToolsResult
	if err := c.request(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}

	tools := make([]ToolInfo, 0, len(result.Tools))
	for _, tool := range result.Tools {
		tools = append(tools, ToolInfo{
			Name:        tool.Name,
			Description: strings.TrimSpace(tool.Description),
			InputSchema: tool.InputSchema,
			ServerName:  c.serverName,
		})
	}
	return tools, nil
}

// callTool 调用远端 MCP 工具并提取文本结果。
func (c *connection) callTool(ctx context.Context, toolName string, arguments map[string]string) (string, error) {
	var result callToolResult
	if err := c.request(ctx, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": arguments,
	}, &result); err != nil {
		return "", err
	}

	textParts := make([]string, 0, len(result.Content))
	for _, item := range result.Content {
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			textParts = append(textParts, item.Text)
		}
	}
	if len(textParts) == 0 {
		raw, _ := json.Marshal(result)
		return string(raw), nil
	}
	return strings.Join(textParts, "\n"), nil
}

// request 发送一条 JSON-RPC 请求并等待响应。
func (c *connection) request(ctx context.Context, method string, params any, target any) error {
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	id := atomic.AddInt64(&c.nextID, 1)
	responseCh := make(chan rpcResponse, 1)

	c.pendingMu.Lock()
	c.pending[id] = responseCh
	c.pendingMu.Unlock()

	if err := c.writeMessage(rpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return err
	}

	select {
	case <-requestContext.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return fmt.Errorf("mcp request timeout: %s", method)
	case response := <-responseCh:
		if response.Error != nil {
			return fmt.Errorf("mcp error %d: %s", response.Error.Code, response.Error.Message)
		}
		if target == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, target)
	}
}

// notify 发送一条无需响应的 JSON-RPC 通知。
func (c *connection) notify(method string, params any) error {
	return c.writeMessage(rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

// writeMessage 把 JSON-RPC 消息按换行分隔格式写入 MCP stdin。
func (c *connection) writeMessage(message rpcRequest) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// consumeStdout 持续读取 MCP stdout，并把响应路由回 pending 请求。
func (c *connection) consumeStdout(stdoutReader io.Reader) {
	scanner := bufio.NewScanner(stdoutReader)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var response rpcResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			continue
		}
		if response.ID == nil {
			continue
		}

		c.pendingMu.Lock()
		responseCh := c.pending[*response.ID]
		delete(c.pending, *response.ID)
		c.pendingMu.Unlock()
		if responseCh != nil {
			responseCh <- response
		}
	}
}

// watchProcessExit 在子进程退出时清空所有挂起请求，避免调用方无限等待。
func (c *connection) watchProcessExit() {
	if c.command == nil {
		return
	}
	_ = c.command.Wait()

	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, responseCh := range c.pending {
		responseCh <- rpcResponse{
			ID:    &id,
			Error: &rpcError{Code: -32000, Message: fmt.Sprintf("mcp server '%s' exited", c.serverName)},
		}
		delete(c.pending, id)
	}
}

// close 关闭当前 MCP 子进程连接。
func (c *connection) close() {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.command != nil && c.command.Process != nil {
		_ = c.command.Process.Kill()
	}
}

// splitPrefixedToolName 把 `mcp__server__tool` 拆成 serverName 与原始 toolName。
func splitPrefixedToolName(prefixedName string) (string, string, error) {
	parts := strings.Split(prefixedName, "__")
	if len(parts) < 3 || strings.TrimSpace(parts[0]) != "mcp" {
		return "", "", fmt.Errorf("invalid mcp tool name: %s", prefixedName)
	}
	serverName := strings.TrimSpace(parts[1])
	toolName := strings.TrimSpace(strings.Join(parts[2:], "__"))
	if serverName == "" || toolName == "" {
		return "", "", fmt.Errorf("invalid mcp tool name: %s", prefixedName)
	}
	return serverName, toolName, nil
}

// stringValue 把弱类型配置字段统一转成字符串。
func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}
