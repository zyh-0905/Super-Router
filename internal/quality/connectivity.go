package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"smart-router/internal/protocol"
)

// RunConnectivity connectivity 阶段：调用当前协议对应的 /v1/models 做连接、认证和可达性检查。
// 上游不支持模型列表但聊天端点可用时记录 models_endpoint_unavailable（attention），继续后续检测。
// 凭据绝不写入 result.Error / Details。
func RunConnectivity(ctx context.Context, ch *Channel, timeout time.Duration) StageResult {
	res := StageResult{Stage: StageConnectivity, CheckName: "models_endpoint", Details: map[string]interface{}{}}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, protocol.ModelsEndpoint(ch.BaseURL), nil)
	if err != nil {
		res.Status = StatusFailed
		res.Error = "create_request_failed"
		return res
	}
	if protocol.IsAnthropic(ch.Protocol) {
		for k, v := range protocol.AnthropicHeaders(ch.APIKey) {
			req.Header.Set(k, v)
		}
	} else if ch.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		res.Status = StatusFailed
		res.Error = classifyNetError(err, reqCtx)
		res.LatencyMS = intPtr(int(time.Since(start).Milliseconds()))
		return res
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	res.HTTPStatus = intPtr(resp.StatusCode)
	res.LatencyMS = intPtr(int(time.Since(start).Milliseconds()))
	// 记录 TTFB 近似值：连接建立到响应头到达
	res.TTFBMS = intPtr(int(time.Since(start).Milliseconds()))
	res.Details["http_status"] = resp.StatusCode

	switch {
	case resp.StatusCode == http.StatusOK:
		// 200 但响应不是合法 JSON → 无法确认模型列表
		var raw map[string]interface{}
		if json.Unmarshal(body, &raw) != nil {
			res.Status = StatusAttention
			res.Error = "models_endpoint_invalid_json"
			res.Details["code"] = "models_endpoint_invalid_json"
			return res
		}
		// 兼容 {data:[...]} 与 {models:[...]}；两者皆空也算端点可用（部分上游空列表）
		res.Status = StatusPassed
		res.Details["model_count"] = countModels(raw)
	case resp.StatusCode == http.StatusNotFound:
		// 上游不支持模型列表但聊天端点可用 → 记录并继续
		res.Status = StatusAttention
		res.Error = "models_endpoint_unavailable"
		res.Details["code"] = "models_endpoint_unavailable"
	default:
		res.Status = StatusFailed
		res.Error = classifyHTTPStatus(resp.StatusCode)
	}
	return res
}

// countModels 统计响应中的模型数量。
func countModels(raw map[string]interface{}) int {
	for _, key := range []string{"data", "models"} {
		if arr, ok := raw[key].([]interface{}); ok {
			return len(arr)
		}
	}
	return 0
}

// classifyHTTPStatus HTTP 状态 → 错误类别（不含响应体，避免凭据/敏感内容入库）。
func classifyHTTPStatus(code int) string {
	switch {
	case code == 401 || code == 403:
		return "auth_error"
	case code == 429:
		return "rate_limited"
	case code >= 500:
		return "upstream_error"
	default:
		return fmt.Sprintf("http_%d", code)
	}
}

// classifyNetError 网络错误 → 错误类别（超时/拒绝/其它）。
func classifyNetError(err error, ctx context.Context) string {
	if ctx.Err() != nil {
		return "timeout"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "refused"):
		return "connection_refused"
	case strings.Contains(msg, "no such host"):
		return "dns_error"
	case strings.Contains(msg, "tls"), strings.Contains(msg, "certificate"):
		return "tls_error"
	default:
		return "unreachable"
	}
}

func intPtr(v int) *int { return &v }
