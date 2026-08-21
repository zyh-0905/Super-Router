package api

// GET /admin/costs 真实流量成本聚合（B1）：
// 代理层捕获的上游 usage × 实测倍率（official > baseline 优先）估算
// 真实消费。统计口径：仅统计、不用于计费。
//
// 聚合维度（group_by）：站点（默认）| 模型 | 日 | Key。
// Key 维度经 decision_logs 的 token_id_hash 关联（request_id 为键）。

import (
	"fmt"
	"math"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// costRow 单行聚合结果。
type costRow struct {
	Key              string  `json:"key"`
	ChannelID        int     `json:"channel_id,omitempty"`
	ChannelName      string  `json:"channel_name,omitempty"`
	Model            string  `json:"model,omitempty"`
	Day              string  `json:"day,omitempty"`
	Requests         int64   `json:"requests"`
	SuccessRate      float64 `json:"success_rate"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	UsageCoverage    float64 `json:"usage_coverage"` // 捕获到 usage 的请求占比
	EstCostUSD       float64 `json:"est_cost_usd"`
}

// costAggSQL 成本聚合的公共 CTE：历史（含用量）→ 实测倍率 → 估算成本。
// 无实测倍率的请求成本为 NULL（不可估，聚合时按 0 汇总但 coverage 可区分）。
const costAggSQL = `
WITH rh AS (
	SELECT rh.channel_id, rh.model, rh.success, rh.request_id,
	       rh.prompt_tokens, rh.completion_tokens, rh.created_at,
	       rh.prompt_tokens IS NOT NULL AS has_usage
	FROM request_history rh
	WHERE rh.created_at >= NOW() - make_interval(days => $1)
	  AND COALESCE(rh.is_probe, false) = false
),
price AS (
	SELECT DISTINCT ON (upstream_id, model) upstream_id, model,
	       real_ratio, basis,
	       COALESCE(official_input_per_m, 0) AS off_in,
	       COALESCE(official_output_per_m, 0) AS off_out
	FROM probe_results
	WHERE success = true
	ORDER BY upstream_id, model, checked_at DESC
),
cost AS (
	SELECT rh.*,
	       CASE
	           WHEN price.basis = 'official' AND price.off_in > 0 AND price.off_out > 0 THEN
	               COALESCE(rh.prompt_tokens,0) * price.real_ratio * price.off_in / 1e6
	             + COALESCE(rh.completion_tokens,0) * price.real_ratio * price.off_out / 1e6
	           WHEN price.basis = 'baseline' AND price.real_ratio > 0 THEN
	               (COALESCE(rh.prompt_tokens,0) + COALESCE(rh.completion_tokens,0))
	               * price.real_ratio * 10.0 / 1e6
	           ELSE NULL
	       END AS est_cost
	FROM rh
	LEFT JOIN price ON price.upstream_id = rh.channel_id AND price.model = rh.model
)`

const costSelectTail = `
	COUNT(*) AS requests,
	ROUND(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 4) AS success_rate,
	COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
	COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
	ROUND(AVG(CASE WHEN has_usage THEN 1.0 ELSE 0.0 END), 4) AS usage_coverage,
	ROUND(COALESCE(SUM(est_cost), 0)::numeric, 6) AS est_cost_usd`

// GetCosts 成本聚合入口。
func (h *AdminHandler) GetCosts(c *gin.Context) {
	days := 7
	if _, err := fmt.Sscanf(c.Query("days"), "%d", &days); err != nil || days <= 0 || days > 30 {
		days = 7
	}
	groupBy := c.Query("group_by")
	ctx := c.Request.Context()

	var (
		query string
		args  []interface{}
	)
	args = append(args, days)
	switch groupBy {
	case "model":
		// 站点 × 模型
		query = costAggSQL + `
			SELECT u.name, c.channel_id, u.name, c.model, NULL::text,
			       ` + costSelectTail + `
			FROM cost c
			JOIN upstreams u ON u.id = c.channel_id
			GROUP BY u.name, c.channel_id, c.model
			ORDER BY est_cost_usd DESC NULLS LAST
			LIMIT 200`
	case "day":
		// 日期（全部站点合计）
		query = costAggSQL + `
			SELECT to_char(c.created_at, 'YYYY-MM-DD'), NULL::int, NULL::text, NULL::text,
			       to_char(c.created_at, 'YYYY-MM-DD'),
			       ` + costSelectTail + `
			FROM cost c
			GROUP BY to_char(c.created_at, 'YYYY-MM-DD')
			ORDER BY to_char(c.created_at, 'YYYY-MM-DD') DESC
			LIMIT 200`
	case "key":
		// Key 维度：经 decision_logs 关联（request_id → token_id_hash）
		query = costAggSQL + `
			SELECT COALESCE(d.token_id_hash, 'unknown'), c.channel_id, u.name, NULL::text, NULL::text,
			       ` + costSelectTail + `
			FROM cost c
			JOIN upstreams u ON u.id = c.channel_id
			LEFT JOIN decision_logs d ON d.request_id = c.request_id
			GROUP BY d.token_id_hash, c.channel_id, u.name
			ORDER BY est_cost_usd DESC NULLS LAST
			LIMIT 200`
	default:
		// 站点（默认）
		groupBy = ""
		query = costAggSQL + `
			SELECT u.name, c.channel_id, u.name, NULL::text, NULL::text,
			       ` + costSelectTail + `
			FROM cost c
			JOIN upstreams u ON u.id = c.channel_id
			GROUP BY u.name, c.channel_id
			ORDER BY est_cost_usd DESC NULLS LAST
			LIMIT 200`
	}

	rows, err := h.db.Pool.Query(ctx, query, args...)
	if err != nil {
		h.logger.Warn("GetCosts query failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "查询成本统计失败"})
		return
	}
	defer rows.Close()

	out := []costRow{}
	for rows.Next() {
		var r costRow
		var model, day *string
		if err := rows.Scan(&r.Key, &r.ChannelID, &r.ChannelName, &model, &day,
			&r.Requests, &r.SuccessRate, &r.PromptTokens, &r.CompletionTokens,
			&r.UsageCoverage, &r.EstCostUSD); err != nil {
			continue
		}
		if model != nil {
			r.Model = *model
		}
		if day != nil {
			r.Day = *day
		}
		r.EstCostUSD = roundCost(r.EstCostUSD)
		out = append(out, r)
	}

	c.JSON(200, gin.H{
		"days":     days,
		"group_by": groupBy,
		"rows":     out,
		"total":    len(out),
	})
}

func roundCost(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
