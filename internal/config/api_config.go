// Package config 负责集中读取环境变量和项目级配置。
// 本文件专门负责解析项目根目录下的 .miniclaude.json，为 Load 提供文件级默认值。
package config

import (
	"encoding/json"
	"fmt"
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

// fileCfgDefaultModel 从文件配置读取 default_model。
// 参数 cfg 表示已经解析好的文件配置；返回值为对应字段的字符串值，不存在时返回空字符串。
func fileCfgDefaultModel(cfg *apiConfigFile) string {
	if cfg == nil || cfg.DefaultModel == nil {
		return ""
	}
	return *cfg.DefaultModel
}

// fileCfgOpenAIKey 从文件配置读取 openai_api_key。
// 参数 cfg 表示已经解析好的文件配置；返回值为对应字段的字符串值，不存在时返回空字符串。
func fileCfgOpenAIKey(cfg *apiConfigFile) string {
	if cfg == nil || cfg.OpenAIAPIKey == nil {
		return ""
	}
	return *cfg.OpenAIAPIKey
}

// fileCfgOpenAIBase 从文件配置读取 openai_base_url。
// 参数 cfg 表示已经解析好的文件配置；返回值为对应字段的字符串值，不存在时返回空字符串。
func fileCfgOpenAIBase(cfg *apiConfigFile) string {
	if cfg == nil || cfg.OpenAIBaseURL == nil {
		return ""
	}
	return *cfg.OpenAIBaseURL
}

// fileCfgAnthropicKey 从文件配置读取 anthropic_api_key。
// 参数 cfg 表示已经解析好的文件配置；返回值为对应字段的字符串值，不存在时返回空字符串。
func fileCfgAnthropicKey(cfg *apiConfigFile) string {
	if cfg == nil || cfg.AnthropicAPIKey == nil {
		return ""
	}
	return *cfg.AnthropicAPIKey
}

// fileCfgAnthropicBase 从文件配置读取 anthropic_base_url。
// 参数 cfg 表示已经解析好的文件配置；返回值为对应字段的字符串值，不存在时返回空字符串。
func fileCfgAnthropicBase(cfg *apiConfigFile) string {
	if cfg == nil || cfg.AnthropicBaseURL == nil {
		return ""
	}
	return *cfg.AnthropicBaseURL
}

// envOrConfig 按优先级取值：环境变量 > 文件配置。
// 参数 envKey 表示环境变量名；cfg 表示文件配置；fromFile 用于从文件配置提取对应字段。
// 返回值为最终生效的字符串值。
func envOrConfig(envKey string, cfg *apiConfigFile, fromFile func(*apiConfigFile) string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fromFile(cfg)
}

// loadAPIConfigFromFile 尝试从指定目录加载 .miniclaude.json。
// 参数 dir 表示候选配置目录；返回值 cfg 为读取到的文件配置，error 仅用于保留底层 I/O 和解析异常。
// 该函数优先读取 .miniclaude.json；如果不存在，再尝试 .miniclaude.example.json。
func loadAPIConfigFromFile(dir string) (*apiConfigFile, error) {
	tryFiles := []string{
		".miniclaude.json",
		".miniclaude.example.json",
	}

	for _, name := range tryFiles {
		path := filepath.Join(dir, name)
		f, err := os.Open(path)
		if err != nil {
			// 配置文件不存在时继续尝试下一个候选文件，保持“无配置文件也能启动”的行为。
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("open api config file %s: %w", path, err)
		}

		var cfg apiConfigFile
		decodeErr := json.NewDecoder(f).Decode(&cfg)
		closeErr := f.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode api config file %s: %w", path, decodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close api config file %s: %w", path, closeErr)
		}

		// 这里返回第一个成功解析的配置文件，保证 .miniclaude.json 优先于 example 文件。
		return &cfg, nil
	}

	return nil, nil
}
