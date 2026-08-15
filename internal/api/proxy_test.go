package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestClassifyErrorUpstreamStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusUnauthorized, "auth_error"},
		{http.StatusForbidden, "auth_error"},
		{http.StatusInternalServerError, "upstream_error"},
		{http.StatusBadGateway, "upstream_error"},
		{http.StatusBadRequest, "bad_request"},
	}
	for _, tc := range cases {
		err := &upstreamError{StatusCode: tc.status}
		if got := classifyError(err); got != tc.want {
			t.Errorf("classifyError(status=%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestClassifyErrorContext(t *testing.T) {
	if got := classifyError(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("classifyError(deadline) = %q, want timeout", got)
	}
	if got := classifyError(context.Canceled); got != "client_canceled" {
		t.Errorf("classifyError(canceled) = %q, want client_canceled", got)
	}
	if got := classifyError(fmt.Errorf("connection refused")); got != "network_error" {
		t.Errorf("classifyError(network) = %q, want network_error", got)
	}
}

func TestIsRetryable(t *testing.T) {
	ctx := context.Background()
	retryable := []error{
		&upstreamError{StatusCode: http.StatusTooManyRequests},
		&upstreamError{StatusCode: http.StatusRequestTimeout},
		&upstreamError{StatusCode: http.StatusInternalServerError},
		&upstreamError{StatusCode: http.StatusBadGateway},
		&upstreamError{Err: fmt.Errorf("dial tcp: connection refused")}, // 无 HTTP 响应：按网络错误处理
		fmt.Errorf("dial tcp: connection refused"),
		context.DeadlineExceeded, // 调用方仍存活：尝试级超时可换渠道
	}
	for _, err := range retryable {
		if !isRetryable(err, ctx) {
			t.Errorf("isRetryable(%v) = false, want true", err)
		}
	}

	nonRetryable := []error{
		&upstreamError{StatusCode: http.StatusUnauthorized},
		&upstreamError{StatusCode: http.StatusBadRequest},
		&upstreamError{StatusCode: http.StatusNotFound},
		context.Canceled,
	}
	for _, err := range nonRetryable {
		if isRetryable(err, ctx) {
			t.Errorf("isRetryable(%v) = true, want false", err)
		}
	}

	// 调用方已超时/断开：deadline 错误不可重试
	deadCtx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	<-deadCtx.Done()
	if isRetryable(context.DeadlineExceeded, deadCtx) {
		t.Error("deadline exceeded with dead caller context must not be retryable")
	}
}

func TestClassifyErrorNetworkWrappedUpstream(t *testing.T) {
	// 上游连接被拒：StatusCode 为 0，应归类为网络错误
	err := &upstreamError{Err: fmt.Errorf("http request: %w", fmt.Errorf("connection refused"))}
	if got := classifyError(err); got != "network_error" {
		t.Errorf("classifyError(wrapped network) = %q, want network_error", got)
	}
	if !isRetryable(err, context.Background()) {
		t.Error("isRetryable(wrapped network) = false, want true")
	}
}

func TestUpstreamErrorWrapsUnderlyingCause(t *testing.T) {
	cause := fmt.Errorf("dial timeout")
	err := &upstreamError{Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("upstreamError must wrap its underlying error")
	}
	if err.Error() != "dial timeout" {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
}
