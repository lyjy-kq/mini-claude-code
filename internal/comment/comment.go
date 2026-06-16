// Package comment 负责承载中文注释规范检查的基础能力。
// 当前阶段先提供可扩展的数据结构，后续再补自动扫描和报告输出。
package comment

// RuleSet 表示中文注释规范集合。
type RuleSet struct {
	// RequireFileHeader 表示是否强制文件头注释。
	RequireFileHeader bool
	// RequireFunctionComment 表示是否强制函数注释。
	RequireFunctionComment bool
	// RequireFieldComment 表示是否强制字段注释。
	RequireFieldComment bool
}

// DefaultRules 返回当前项目默认注释规范。
func DefaultRules() RuleSet {
	return RuleSet{
		RequireFileHeader:    true,
		RequireFunctionComment: true,
		RequireFieldComment:  true,
	}
}
