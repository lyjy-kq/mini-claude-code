// Package contextx 负责承载上下文压缩相关状态。
// 这里先把核心参数抽出来，便于后续补充 budget、snip 和 compact 策略。
package contextx

// State 表示上下文压缩运行状态。
type State struct {
	// EffectiveWindow 表示可用上下文窗口大小。
	EffectiveWindow int
	// SnipThreshold 表示大结果被裁剪的阈值。
	SnipThreshold float64
	// KeepRecentResults 表示最近保留结果数量。
	KeepRecentResults int
}

// Default 返回默认上下文压缩配置。
func Default() State {
	return State{
		EffectiveWindow:   180000,
		SnipThreshold:     0.60,
		KeepRecentResults: 3,
	}
}
