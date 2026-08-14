package metrics

import (
	"time"

	"github.com/gin-gonic/gin"
)

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
		model := c.GetString("model")
		if model == "" {
			model = "unknown"
		}

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
