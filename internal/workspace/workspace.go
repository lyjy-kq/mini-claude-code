// Package workspace 负责管理工作区边界与路径安全。
// 当前阶段先落实“根目录识别、路径规范化、工作区内校验”三项基础能力。
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Manager 表示工作区管理器。
type Manager struct {
	// root 表示工作区根目录。
	root string
}

// NewManager 创建工作区管理器，并将根目录标准化为绝对路径。
func NewManager(root string) (*Manager, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Manager{root: absoluteRoot}, nil
}

// Root 返回工作区根目录。
func (m *Manager) Root() string {
	return m.root
}

// EnsureExists 校验工作区根目录存在且可访问。
func (m *Manager) EnsureExists() error {
	info, err := os.Stat(m.root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace root is not a directory: %s", m.root)
	}
	return nil
}

// ResolveInWorkspace 把输入路径解析为工作区内绝对路径，并阻止越界访问。
func (m *Manager) ResolveInWorkspace(input string) (string, error) {
	target := input
	if !filepath.IsAbs(target) {
		target = filepath.Join(m.root, input)
	}

	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}

	// 通过统一前缀检查阻止路径跳出工作区根目录。
	normalizedRoot := filepath.Clean(m.root)
	normalizedTarget := filepath.Clean(absoluteTarget)
	if normalizedTarget != normalizedRoot && !strings.HasPrefix(normalizedTarget, normalizedRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", input)
	}
	return normalizedTarget, nil
}
