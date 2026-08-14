package upstream

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ParseRetryAfter 解析 Retry-After 头
func ParseRetryAfter(header string) (time.Duration, error) {
	if header == "" {
		return 0, fmt.Errorf("empty retry-after header")
	}

	// 尝试解析为秒数
	if seconds, err := strconv.Atoi(header); err == nil {
		return time.Duration(seconds) * time.Second, nil
	}

	// 尝试解析为 HTTP 日期格式
	retryTime, err := http.ParseTime(header)
	if err != nil {
		return 0, fmt.Errorf("invalid retry-after format: %w", err)
	}

	duration := time.Until(retryTime)
	if duration < 0 {
		duration = 0
	}

	return duration, nil
}

// ShouldWaitForRetry 判断是否应该等待重试
func ShouldWaitForRetry(retryAfter time.Duration, remainingBudget time.Duration) bool {
	// 如果等待时间超过剩余预算，不等待
	if retryAfter > remainingBudget {
		return false
	}

	// 如果等待时间过长（超过 10 秒），也不等待
	if retryAfter > 10*time.Second {
		return false
	}

	return true
}
