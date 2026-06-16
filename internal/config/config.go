// Package config 负责集中读取环境变量、默认配置以及项目级配置文件。
// 这样可以避免各个业务模块各自读取环境变量，导致行为不一致。
package config

import (
	"os"
)

// Config 表示程序运行所需的全局配置。
type Config struct {
	// DefaultModel 表示未显式传参时使用的默认模型名。
	DefaultModel string
	// WorkingDirectory 表示默认工作目录。
	WorkingDirectory string
	// OpenAIAPIKey 表示 OpenAI 兼容接口的 API Key。
	OpenAIAPIKey string
	// OpenAIBaseURL 表示 OpenAI 兼容接口的基础地址。
	OpenAIBaseURL string
	// AnthropicAPIKey 表示 Anthropic 接口的 API Key。
	AnthropicAPIKey string
	// AnthropicBaseURL 表示 Anthropic 兼容代理地址。
	AnthropicBaseURL string
}

// Load 负责从环境变量和项目级配置文件组装统一配置。
// 优先级：环境变量 > .miniclaude.json > 内置默认值。
// 如果环境变量为空但配置文件中有对应字段，则使用配置文件的值。
func Load() Config {
	// 第一步：确定工作目录
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}

	// 第二步：加载项目级配置文件（最低优先级）
	fileCfg, _ := loadAPIConfigFromFile(wd) // 文件不存在或格式错误时不报错

	// 第三步：从文件配置填充初始值，再让环境变量覆盖
	cfg := Config{
		WorkingDirectory: wd,
		DefaultModel:     envOrConfig("MINI_CLAUDE_MODEL", fileCfg, fileCfgDefaultModel),
		OpenAIAPIKey:     envOrConfig("OPENAI_API_KEY", fileCfg, fileCfgOpenAIKey),
		OpenAIBaseURL:    envOrConfig("OPENAI_BASE_URL", fileCfg, fileCfgOpenAIBase),
		AnthropicAPIKey:  envOrConfig("ANTHROPIC_API_KEY", fileCfg, fileCfgAnthropicKey),
		AnthropicBaseURL: envOrConfig("ANTHROPIC_BASE_URL", fileCfg, fileCfgAnthropicBase),
	}

	// 第四步：处理 DefaultModel 的最终 fallback
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "claude-opus-4-6"
	}

	return cfg
}
