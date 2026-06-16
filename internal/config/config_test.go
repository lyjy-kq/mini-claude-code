package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadFromFile 验证 .miniclaude.json 存在且无对应环境变量时，配置能从文件正确读取。
func TestLoadFromFile(t *testing.T) {
	// 在临时目录创建 .miniclaude.json
	dir := t.TempDir()
	content := `{
  "openai_api_key": "from-file-key",
  "openai_base_url": "https://from-file.com/v1",
  "default_model": "from-file-model"
}`
	if err := os.WriteFile(filepath.Join(dir, ".miniclaude.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// 切换工作目录到临时目录
	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	// 清除环境变量
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("OPENAI_BASE_URL")
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_BASE_URL")
	os.Unsetenv("MINI_CLAUDE_MODEL")

	cfg := Load()

	if cfg.OpenAIAPIKey != "from-file-key" {
		t.Fatalf("OpenAIAPIKey = %q, want %q", cfg.OpenAIAPIKey, "from-file-key")
	}
	if cfg.OpenAIBaseURL != "https://from-file.com/v1" {
		t.Fatalf("OpenAIBaseURL = %q, want %q", cfg.OpenAIBaseURL, "https://from-file.com/v1")
	}
	if cfg.DefaultModel != "from-file-model" {
		t.Fatalf("DefaultModel = %q, want %q", cfg.DefaultModel, "from-file-model")
	}
}

// TestLoadFileMissing 验证 .miniclaude.json 不存在时，配置行为不变（model fallback 正常）。
func TestLoadFileMissing(t *testing.T) {
	dir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("OPENAI_BASE_URL")
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_BASE_URL")
	os.Unsetenv("MINI_CLAUDE_MODEL")

	cfg := Load()

	if cfg.OpenAIAPIKey != "" {
		t.Fatalf("OpenAIAPIKey = %q, want empty", cfg.OpenAIAPIKey)
	}
	if cfg.DefaultModel != "claude-opus-4-6" {
		t.Fatalf("DefaultModel = %q, want %q", cfg.DefaultModel, "claude-opus-4-6")
	}
}

// TestLoadEnvOverridesFile 验证环境变量能覆盖文件配置。
func TestLoadEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	content := `{
  "openai_api_key": "file-key",
  "openai_base_url": "https://file.com/v1"
}`
	if err := os.WriteFile(filepath.Join(dir, ".miniclaude.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	// 设置环境变量，应覆盖文件
	os.Setenv("OPENAI_API_KEY", "env-override-key")
	os.Setenv("OPENAI_BASE_URL", "https://env-override.com/v1")
	defer func() {
		os.Unsetenv("OPENAI_API_KEY")
		os.Unsetenv("OPENAI_BASE_URL")
	}()

	cfg := Load()

	if cfg.OpenAIAPIKey != "env-override-key" {
		t.Fatalf("OpenAIAPIKey = %q, want %q", cfg.OpenAIAPIKey, "env-override-key")
	}
	if cfg.OpenAIBaseURL != "https://env-override.com/v1" {
		t.Fatalf("OpenAIBaseURL = %q, want %q", cfg.OpenAIBaseURL, "https://env-override.com/v1")
	}
}
