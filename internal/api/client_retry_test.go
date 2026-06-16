// Package api 验证模型请求重试链路的关键语义。
// 本文件专门覆盖可重试错误判定、最大重试次数与取消行为，防止后续迁移时把运行时容错能力回退掉。
package api

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// timeoutNetError 用于构造一个可被识别为超时的网络错误。
type timeoutNetError struct{}

// Error 返回测试网络错误文本。
func (timeoutNetError) Error() string {
	return "i/o timeout"
}

// Timeout 表示该错误属于超时错误。
func (timeoutNetError) Timeout() bool {
	return true
}

// Temporary 保持与部分历史 net.Error 实现兼容。
func (timeoutNetError) Temporary() bool {
	return true
}

var _ net.Error = timeoutNetError{}

// TestIsRetryableErrorRecognizesHTTPStatus 验证限流和过载状态码会进入重试链路。
func TestIsRetryableErrorRecognizesHTTPStatus(t *testing.T) {
	if !isRetryableError(newRetryableHTTPError("request", 429, "429 Too Many Requests", []byte("slow down"))) {
		t.Fatal("expected HTTP 429 to be retryable")
	}
	if !isRetryableError(newRetryableHTTPError("request", 503, "503 Service Unavailable", []byte("busy"))) {
		t.Fatal("expected HTTP 503 to be retryable")
	}
	if isRetryableError(newRetryableHTTPError("request", 400, "400 Bad Request", []byte("bad input"))) {
		t.Fatal("expected HTTP 400 to be non-retryable")
	}
}

// TestIsRetryableErrorRecognizesNetworkAndOverload 验证网络超时和 overloaded 文本会触发重试。
func TestIsRetryableErrorRecognizesNetworkAndOverload(t *testing.T) {
	if !isRetryableError(timeoutNetError{}) {
		t.Fatal("expected timeout net.Error to be retryable")
	}
	if !isRetryableError(errors.New("backend overloaded right now")) {
		t.Fatal("expected overloaded message to be retryable")
	}
	if !isRetryableError(errors.New("read tcp: connection reset by peer")) {
		t.Fatal("expected connection reset to be retryable")
	}
}

// TestWithRetryRetriesUntilSuccess 验证可重试错误会在限定次数内继续尝试直到成功。
func TestWithRetryRetriesUntilSuccess(t *testing.T) {
	ctx := context.Background()
	attempts := 0

	response, err := withRetry(ctx, 2, func() (Response, error) {
		attempts++
		if attempts < 3 {
			return Response{}, newRetryableHTTPError("request", 429, "429 Too Many Requests", []byte("retry"))
		}
		return Response{Text: "ok"}, nil
	})
	if err != nil {
		t.Fatalf("expected eventual success, got error: %v", err)
	}
	if response.Text != "ok" {
		t.Fatalf("expected successful response, got %#v", response)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

// TestWithRetryStopsAfterMaxRetries 验证超过最大重试次数后会返回最后一次错误。
func TestWithRetryStopsAfterMaxRetries(t *testing.T) {
	ctx := context.Background()
	attempts := 0

	_, err := withRetry(ctx, 2, func() (Response, error) {
		attempts++
		return Response{}, newRetryableHTTPError("request", 503, "503 Service Unavailable", []byte("busy"))
	})
	if err == nil {
		t.Fatal("expected retry exhaustion error")
	}
	if attempts != 3 {
		t.Fatalf("expected initial attempt plus 2 retries, got %d attempts", attempts)
	}
}

// TestWithRetryDoesNotRetryNonRetryableError 验证非短暂失败会立即返回，避免无意义等待。
func TestWithRetryDoesNotRetryNonRetryableError(t *testing.T) {
	ctx := context.Background()
	attempts := 0

	_, err := withRetry(ctx, 3, func() (Response, error) {
		attempts++
		return Response{}, errors.New("validation failed")
	})
	if err == nil {
		t.Fatal("expected non-retryable error")
	}
	if attempts != 1 {
		t.Fatalf("expected no retry for non-retryable error, got %d attempts", attempts)
	}
}

// TestSleepWithContextHonorsCancellation 验证等待重试期间能响应取消信号。
func TestSleepWithContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepWithContext(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("expected canceled sleep to return promptly")
	}
}
