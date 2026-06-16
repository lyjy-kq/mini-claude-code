// Package config 负责集中读取环境变量和默认配置。
// 本文件包含从项目级配置文件 .miniclaude.json 读取 API 配置的实现。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// apiConfigFile 表示 .miniclaude.json 的磁盘结构。
// 所有字段均为指针，便于区分"未设置"和"空字符串"。
type apiConfigFile struct {
	// OpenAIAPIKey 表示 OpenAI 兼容接口的 API Key。
	OpenAIAPIKey *string `json:"openai_api_key,omitempty"`
	// OpenAIBaseURL 表示 OpenAI 兼容接口的基础地址。
	OpenAIBaseURL *string `json:"openai_base_url,omitempty"`
	// AnthropicAPIKey 表示 Anthropic 接口的 API Key。
	AnthropicAPIKey *string `json:"anthropic_api_key,omitempty"`
	// AnthropicBaseURL 表示 Anthropic 兼容代理地址。
	AnthropicBaseURL *string `json:"anthropic_base_url,omitempty"`
	// DefaultModel 表示未显式传参时使用的默认模型名。
	DefaultModel *string `json:"default_model,omitempty"`
}

// fileCfg field accessor：从文件配置读取指定字段的字符串值，文件配置为 nil 时返回空。
func fileCfgDefaultModel(cfg *apiConfigFile) string {
	if cfg == nil || cfg.DefaultModel == nil {
		return ""
	}
	return *cfg.DefaultModel
}

// fileCfgOpenAIKey 从文件配置读取 openai_api_key。
func fileCfgOpenAIKey(cfg *apiConfigFile) string {
	if cfg == nil || cfg.OpenAIAPIKey == nil {
		return ""
	}
	return *cfg.OpenAIAPIKey
}

// fileCfgOpenAIBase 从文件配置读取 openai_base_url。
func fileCfgOpenAIBase(cfg *apiConfigFile) string {
	if cfg == nil || cfg.OpenAIBaseURL == nil {
		return ""
	}
	return *cfg.OpenAIBaseURL
}

// fileCfgAnthropicKey 从文件配置读取 anthropic_api_key。
func fileCfgAnthropicKey(cfg *apiConfigFile) string {
	if cfg == nil || cfg.AnthropicAPIKey == nil {
		return ""
	}
	return *cfg.AnthropicAPIKey
}

// fileCfgAnthropicBase 从文件配置读取 anthropic_base_url。
func fileCfgAnthropicBase(cfg *apiConfigFile) string {
	if cfg == nil || cfg.AnthropicBaseURL == nil {
		return ""
	}
	return *cfg.AnthropicBaseURL
}

// envOrConfig 按优先级取值：环境变量 > 文件配置。
// 环境变量非空时优先使用，否则回退到 fromFile(cfg) 的返回值。
func envOrConfig(envKey string, cfg *apiConfigFile, fromFile func(*apiConfigFile) string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fromFile(cfg)
}

// loadAPIConfigFromFile 尝试从指定目录加载 .miniclaude.json，
// 文件不存在时返回 nil，不报错。
func loadAPIConfigFromFile(dir string) (*apiConfigFile, error) {
	tryFiles := []string{
		".miniclaude.json",
		".miniclaude.example.json",
	}

	for _, name := range tryFiles {
		path := filepath.Join(dir, name)

		func() {
			f, err := os.Open(path)
			if err != nil {
				return
			}
			defer f.Close()

			var cfg apiConfigFile
			if err := json.NewDecoder(f).Decode(&cfg); err != nil {
				return
			}

			// 成功就“返回外层”
			// 用命名返回值更优雅，这里简化写法：
		}()
	}

	return nil, nil
}
