// Package api 提供模型客户端抽象、工具调用响应结构以及最小可用的后端实现。
// client_retry.go 为重试逻辑：封装指数退避重试、可重试错误判定、
// 重试原因格式化、延迟计算和带上下文的等待函数。
package api

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	"mini-claude-code/internal/ui"
)

// withRetry 以指数退避执行模型请求。
// 这里集中处理网络抖动、限流与后端过载，让 OpenAI-compatible 和 Anthropic 两条链路都能复用同一套容错策略。
func withRetry(ctx context.Context, maxRetries int, fn func() (Response, error)) (Response, error) {
	for attempt := 0; ; attempt++ {
		response, err := fn()
		if err == nil {
			return response, nil
		}
		if ctx != nil && ctx.Err() != nil {
			return Response{}, err
		}
		if attempt >= maxRetries || !isRetryableError(err) {
			return Response{}, err
		}

		delay := retryDelay(attempt)
		ui.PrintRetry(attempt+1, maxRetries, retryReason(err))
		if waitErr := sleepWithContext(ctx, delay); waitErr != nil {
			return Response{}, err
		}
	}
}

// isRetryableError 判断错误是否属于短暂失败。
// 这里对齐源仓库语义：429、503、529、连接重置、超时，以及 overloaded 文本都视为可重试。
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var httpErr *retryableHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 429 || httpErr.StatusCode == 503 || httpErr.StatusCode == 529
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "econnreset") ||
		strings.Contains(lower, "etimedout") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "overloaded")
}

// retryReason 为终端提示生成简洁失败原因。
func retryReason(err error) string {
	var httpErr *retryableHTTPError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("HTTP %d", httpErr.StatusCode)
	}

	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "econnreset"):
		return "ECONNRESET"
	case strings.Contains(lower, "etimedout"):
		return "ETIMEDOUT"
	case strings.Contains(lower, "overloaded"):
		return "overloaded"
	case strings.Contains(lower, "connection reset"):
		return "connection reset"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "temporary error"
}

// retryDelay 计算指数退避等待时长，并增加少量抖动避免多个请求同时重试。
func retryDelay(attempt int) time.Duration {
	delay := time.Second * time.Duration(1<<attempt)
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
	return delay + jitter
}

// sleepWithContext 在等待重试期间响应上层取消信号。
func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	if ctx == nil {
		<-timer.C
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
