// Package spec 负责执行“无 spec 禁止编码”的前置校验。
// 当前阶段先校验 docx 目录存在、文件名格式和至少一份 spec 可用。
package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Validator 表示 spec 校验器。
type Validator struct {
	// root 表示工作区根目录。
	root string
}

// Result 表示一次 spec 校验结果。
type Result struct {
	// Directory 表示 docx 目录路径。
	Directory string
	// Files 表示发现到的合法 spec 文件。
	Files []string
}

var specPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{2}-.+-.+-.+\.md$`)

// NewValidator 创建新的 spec 校验器。
func NewValidator(root string) *Validator {
	return &Validator{root: root}
}

// Validate 执行 spec 前置校验。
func (v *Validator) Validate() (Result, error) {
	docxDir := filepath.Join(v.root, "docx")
	info, err := os.Stat(docxDir)
	if err != nil {
		return Result{}, fmt.Errorf("spec validation failed: docx directory missing")
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("spec validation failed: docx is not a directory")
	}

	entries, err := os.ReadDir(docxDir)
	if err != nil {
		return Result{}, err
	}

	files := make([]string, 0, len(entries))
	for _, item := range entries {
		if item.IsDir() {
			continue
		}
		if specPattern.MatchString(item.Name()) {
			files = append(files, item.Name())
		}
	}

	if len(files) == 0 {
		return Result{}, fmt.Errorf("spec validation failed: no valid spec file found in docx")
	}

	return Result{
		Directory: docxDir,
		Files:     files,
	}, nil
}
