package metrics

import (
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var modelLabelRe = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,64}$`)

// 模型标签基数防护：只放行合法命名的前 N 个模型，超出归为 "other"，
// 防止恶意调用方用随机模型名撑爆 Prometheus 标签基数。
const modelLabelCap = 300

var (
	modelLabelMu  sync.Mutex
	modelLabelSet = map[string]struct{}{}
)

func sanitizeModelLabel(model string) string {
	if model == "" {
		return "unknown"
	}
	if !modelLabelRe.MatchString(model) {
		return "invalid"
	}
	modelLabelMu.Lock()
	defer modelLabelMu.Unlock()
	if _, ok := modelLabelSet[model]; ok {
		return model
	}
	if len(modelLabelSet) >= modelLabelCap {
		return "other"
	}
	modelLabelSet[model] = struct{}{}
	return model
}

// PrometheusMiddleware 记录 HTTP 请求指标
func PrometheusMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// 处理请求
		c.Next()

		// 计算耗时
		duration := time.Since(start).Seconds()

		// 确定状态
		status := "success"
		if c.Writer.Status() >= 400 {
			status = "error"
		}

		// 从上下文中获取信息（由路由处理器设置）
		model := sanitizeModelLabel(c.GetString("model"))

		channel := c.GetString("channel")
		if channel == "" {
			channel = "unknown"
		}

		// 记录请求总数和延迟
		RequestsTotal.WithLabelValues(model, channel, status).Inc()
		RequestDuration.WithLabelValues(model, channel).Observe(duration)
	}
}

// RecordRoutingDuration 记录路由决策耗时
func RecordRoutingDuration(duration float64) {
	RoutingDuration.Observe(duration)
}

// RecordSnapshotLoadDuration 记录快照加载耗时
func RecordSnapshotLoadDuration(duration float64) {
	SnapshotLoadDuration.Observe(duration)
}

// RecordProxyRequest 记录代理请求
func RecordProxyRequest(channel, model, status string, duration float64) {
	ProxyRequestsTotal.WithLabelValues(channel, model, status).Inc()
	ProxyDuration.WithLabelValues(channel, model).Observe(duration)
}

// RecordFailover 记录故障切换
func RecordFailover(fromChannel, toChannel, reason string) {
	FailoverCount.WithLabelValues(fromChannel, toChannel, reason).Inc()
}
