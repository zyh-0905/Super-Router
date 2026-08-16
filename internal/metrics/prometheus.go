package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RequestsTotal 请求总数
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smart_router_requests_total",
			Help: "Total number of requests",
		},
		[]string{"model", "channel", "status"},
	)

	// RequestDuration 请求延迟
	RequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "smart_router_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"model", "channel"},
	)

	// RoutingDuration 路由决策耗时
	RoutingDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "smart_router_routing_duration_seconds",
			Help:    "Routing decision duration in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1},
		},
	)

	// ChannelSuccessRate 渠道成功率（使用 Gauge 周期性更新）
	ChannelSuccessRate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "smart_router_channel_success_rate",
			Help: "Channel success rate (0-1)",
		},
		[]string{"channel", "model"},
	)

	// CircuitBreakerState 熔断状态
	// 0=closed, 1=open, 2=half_open, 3=degraded
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "smart_router_circuit_breaker_state",
			Help: "Circuit breaker state (0=closed, 1=open, 2=half_open, 3=degraded)",
		},
		[]string{"channel", "model"},
	)

	// SnapshotLoadDuration 快照加载耗时
	SnapshotLoadDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "smart_router_snapshot_load_duration_seconds",
			Help:    "Snapshot load duration in seconds",
			Buckets: []float64{.001, .005, .01, .05, .1},
		},
	)

	// ProxyRequestsTotal 代理请求总数
	ProxyRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smart_router_proxy_requests_total",
			Help: "Total number of proxy requests to upstream",
		},
		[]string{"channel", "model", "status"},
	)

	// ProxyDuration 代理请求延迟
	ProxyDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "smart_router_proxy_duration_seconds",
			Help:    "Proxy request duration in seconds",
			Buckets: []float64{.1, .5, 1, 2, 5, 10, 30},
		},
		[]string{"channel", "model"},
	)

	// FailoverCount 故障切换次数
	FailoverCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smart_router_failover_total",
			Help: "Total number of failovers",
		},
		[]string{"from_channel", "to_channel", "reason"},
	)

	// AdminRequestsTotal 管理/健康/指标等非推理 HTTP 请求数（P2-08：与业务指标分离，
	// 避免 /admin、/health、/metrics 污染推理 QPS/成功率告警）
	AdminRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smart_router_admin_requests_total",
			Help: "Total number of non-inference HTTP requests (admin/health/metrics)",
		},
		[]string{"route", "status"},
	)

	// AdminRequestDuration 管理/健康/指标请求延迟
	AdminRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "smart_router_admin_request_duration_seconds",
			Help:    "Duration of non-inference HTTP requests in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"route"},
	)
)
