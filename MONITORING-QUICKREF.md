# 📊 Smart Router 监控系统 - 快速参考

## 🚀 快速启动

```bash
# 前提：Gateway 已运行（暴露 :8080/metrics）
./start-monitoring.sh
```

| 服务 | URL | 凭证 |
|-----|-----|------|
| Gateway Health | http://localhost:8080/health | - |
| Metrics | http://localhost:8080/metrics | - |
| Admin API | http://localhost:8080/admin/* | Bearer test-admin-key |
| Prometheus | http://localhost:9090 | - |
| Grafana | http://localhost:3000 | admin / admin |

## 📊 核心指标（9 个）

| 指标 | 类型 | 说明 |
|-----|------|------|
| `smart_router_requests_total` | Counter | 总请求数（按渠道/模型/结果） |
| `smart_router_request_duration_seconds` | Histogram | 端到端延迟 |
| `smart_router_routing_duration_seconds` | Histogram | 路由决策耗时 |
| `smart_router_snapshot_load_duration_seconds` | Histogram | 快照加载耗时 |
| `smart_router_channel_success_rate` | Gauge | 渠道成功率（10 分钟窗口） |
| `smart_router_circuit_breaker_state` | Gauge | 熔断状态（0=closed 1=open 2=half_open 3=degraded） |
| `smart_router_proxy_requests_total` | Counter | 上游调用数 |
| `smart_router_proxy_duration_seconds` | Histogram | 上游延迟 |
| `smart_router_failover_total` | Counter | 故障切换次数 |

## 🚨 告警规则（14 条）

- **可用性**：Gateway 宕机、数据库连接失败
- **成功率**：全局 < 95%、单渠道 < 80%（持续 5 分钟）
- **延迟**：P95 > 2s、路由决策 P95 > 200ms
- **熔断**：渠道进入 open 状态
- **故障切换**：短时间切换次数激增

## 🖥 Grafana 仪表板（9 个面板）

导入 `grafana-dashboard.json`：请求趋势、成功率、延迟分位数、渠道成功率排行、熔断矩阵、故障切换、决策耗时、快照耗时、系统总览。

## 🔍 常用查询

```bash
# 原始指标
curl -s http://localhost:8080/metrics | grep smart_router

# PromQL 示例
rate(smart_router_requests_total[5m])                                    # QPS
histogram_quantile(0.95, rate(smart_router_request_duration_seconds_bucket[5m]))  # P95 延迟
smart_router_circuit_breaker_state == 1                                  # 当前熔断中的渠道
```

## 🧪 验证脚本

```bash
./test-metrics.sh    # 自动发测试请求并校验指标采集
```
