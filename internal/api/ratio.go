package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"smart-router/internal/checker"
	"smart-router/internal/config"
	"smart-router/internal/router"
	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	ratioProbeLockTTL        = 120 * time.Second // 探测最长约 60s，TTL 留余量
	defaultManualProbeTokens = 64
	maxManualProbeTokens     = 256
)

// 手动探测预算原子预留（Redis，按自然日键）：防止并发探测 check-then-act 超支
var reserveProbeBudgetScript = redis.NewScript(`
local ch = tonumber(redis.call('get', KEYS[1]) or '0')
local gl = tonumber(redis.call('get', KEYS[2]) or '0')
local r = tonumber(ARGV[1])
if ch + r > tonumber(ARGV[2]) or gl + r > tonumber(ARGV[3]) then
	return 0
end
redis.call('set', KEYS[1], ch + r, 'EX', 172800)
redis.call('set', KEYS[2], gl + r, 'EX', 172800)
return 1
`)

var refundProbeBudgetScript = redis.NewScript(`
local ch = tonumber(redis.call('get', KEYS[1]) or '0')
local gl = tonumber(redis.call('get', KEYS[2]) or '0')
local r = tonumber(ARGV[1])
ch = math.max(0, ch - r)
gl = math.max(0, gl - r)
redis.call('set', KEYS[1], ch, 'EX', 172800)
redis.call('set', KEYS[2], gl, 'EX', 172800)
return 1
`)

// 带所有权的锁释放：仅当 token 匹配才删除，避免误删他方持有的锁
var releaseOwnedLockScript = redis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
	return redis.call('del', KEYS[1])
end
return 0
`)

func probeBudgetKeys(channelID int) (string, string) {
	day := time.Now().Format("2006-01-02")
	return fmt.Sprintf("ratio:budget:%d:%s", channelID, day),
		fmt.Sprintf("ratio:budget:global:%s", day)
}

// RatioHandler 实时倍率：声明/实测查询 + 按需手动实测
type RatioHandler struct {
	db     *store.DB
	redis  *store.RedisClient
	cfg    *config.Config
	probe  *checker.ProbeChecker
	logger *zap.Logger
}

func NewRatioHandler(db *store.DB, redis *store.RedisClient, cfg *config.Config, probe *checker.ProbeChecker, logger *zap.Logger) *RatioHandler {
	return &RatioHandler{db: db, redis: redis, cfg: cfg, probe: probe, logger: logger}
}

// loadUpstream 加载站点探测所需字段与模型映射
func (h *RatioHandler) loadUpstream(ctx context.Context, channelID int) (*checker.Upstream, map[string]string, error) {
	var u checker.Upstream
	var mmJSON []byte
	err := h.db.Pool.QueryRow(ctx, `
		SELECT id, name, base_url, access_token, api_key, enabled, role, protocol,
		       daily_probe_budget, balance_api_url, balance_api_token,
		       timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms,
		       COALESCE(model_mapping::text, '{}')
		FROM upstreams WHERE id = $1
	`, channelID).Scan(
		&u.ID, &u.Name, &u.BaseURL, &u.AccessToken, &u.APIKey, &u.Enabled, &u.Role, &u.Protocol,
		&u.DailyProbeBudget, &u.BalanceAPIURL, &u.BalanceAPIToken,
		&u.TimeoutConnectMS, &u.TimeoutFirstByteMS, &u.TimeoutTotalMS,
		&mmJSON,
	)
	if err != nil {
		return nil, nil, err
	}
	mapping := map[string]string{}
	_ = json.Unmarshal(mmJSON, &mapping)
	return &u, mapping, nil
}

// clampProbeTokens 归一化手动探测的 token 上限（默认 64，封顶 256）
func clampProbeTokens(n int) int {
	if n <= 0 {
		return defaultManualProbeTokens
	}
	if n > maxManualProbeTokens {
		return maxManualProbeTokens
	}
	return n
}

// channelEffectiveBudget 站点有效探测预算：站点自身 > 所属分组最小值 > 全局。
// 禁用站点不在调度表内（LoadSchedules 仅加载启用站点），回退全局预算。
func (h *RatioHandler) channelEffectiveBudget(ctx context.Context, channelID int, globalBudget float64) float64 {
	schedules, err := checker.LoadSchedules(ctx, h.db,
		h.cfg.Checker.AliveInterval, h.cfg.Checker.PricingInterval,
		h.cfg.Checker.ProbeInterval, h.cfg.Checker.BalanceInterval, globalBudget)
	if err != nil {
		return globalBudget
	}
	for _, sch := range schedules {
		if sch.ID == channelID {
			return sch.EffectiveBudget
		}
	}
	return globalBudget
}

// latestDeclared 读取某模型最新的声明倍率（当前 epoch 内）
func (h *RatioHandler) latestDeclared(ctx context.Context, channelID int, model string, epoch int64) map[string]interface{} {
	var promptRatio, completionRatio float64
	var checkedAt time.Time
	err := h.db.Pool.QueryRow(ctx, `
		SELECT prompt_ratio, completion_ratio, checked_at
		FROM declared_prices
		WHERE upstream_id = $1 AND model = $2 AND epoch <= $3
		ORDER BY epoch DESC
		LIMIT 1
	`, channelID, model, epoch).Scan(&promptRatio, &completionRatio, &checkedAt)
	if err != nil {
		return nil
	}
	return declaredEntry(model, promptRatio, completionRatio, checkedAt)
}

func declaredEntry(model string, promptRatio, completionRatio float64, checkedAt time.Time) map[string]interface{} {
	return map[string]interface{}{
		"model":                 model,
		"prompt_ratio":          promptRatio,
		"completion_ratio":      completionRatio,
		"prompt_price_per_m":    round2(promptRatio * 10.0), // 基准 $10/1M
		"completion_price_per_m": round2(completionRatio * 10.0),
		"checked_at":            checkedAt.Format(time.RFC3339),
	}
}

// driftPct 实测倍率相对声明倍率的漂移百分比（按 token 加权混合输入/输出声明倍率）
func driftPct(res *checker.ProbeResult, declared map[string]interface{}) float64 {
	if res == nil || res.RealRatio <= 0 {
		return 0
	}
	prompt, _ := declared["prompt_ratio"].(float64)
	completion, _ := declared["completion_ratio"].(float64)
	total := res.PromptTokens + res.CompletionTokens
	blended := prompt
	if total > 0 {
		blended = (prompt*float64(res.PromptTokens) + completion*float64(res.CompletionTokens)) / float64(total)
	}
	if blended <= 0 {
		return 0
	}
	return (res.RealRatio - blended) / blended * 100
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// nullableFloat 将 0 转为 NULL（可选价格字段）
func nullableFloat(v float64) interface{} {
	if v <= 0 {
		return nil
	}
	return v
}

// orEmptyList 将 nil 切片归一为空切片（前端无空值守卫时不至于崩溃）
func orEmptyList(v []map[string]interface{}) []map[string]interface{} {
	if v == nil {
		return []map[string]interface{}{}
	}
	return v
}

// GetRatio GET /admin/channels/:id/ratio - 声明倍率表 + 实测历史 + 各模型最新实测
func (h *RatioHandler) GetRatio(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}
	ctx := context.Background()

	var exists bool
	if err := h.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstreams WHERE id = $1)`, channelID).Scan(&exists); err != nil || !exists {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}

	epoch, _ := h.db.GetCurrentEpoch(ctx)

	// 声明倍率（每模型最新）
	declared := []map[string]interface{}{}
	rows, err := h.db.Pool.Query(ctx, `
		SELECT DISTINCT ON (model) model, prompt_ratio, completion_ratio, checked_at
		FROM declared_prices
		WHERE upstream_id = $1 AND epoch <= $2
		ORDER BY model, epoch DESC
	`, channelID, epoch)
	if err == nil {
		for rows.Next() {
			var model string
			var promptRatio, completionRatio float64
			var checkedAt time.Time
			if err := rows.Scan(&model, &promptRatio, &completionRatio, &checkedAt); err != nil {
				continue
			}
			declared = append(declared, declaredEntry(model, promptRatio, completionRatio, checkedAt))
		}
		rows.Close()
	}

	// 实测历史（最近 100 次成功）
	history := []map[string]interface{}{}
	rows, err = h.db.Pool.Query(ctx, `
		SELECT model, real_ratio, cost, ttft_ms, tokens_used, source, checked_at
		FROM probe_results
		WHERE upstream_id = $1 AND success = true
		ORDER BY checked_at DESC
		LIMIT 100
	`, channelID)
	if err == nil {
		for rows.Next() {
			var model, source string
			var realRatio, cost float64
			var ttftMS, tokensUsed int
			var checkedAt time.Time
			if err := rows.Scan(&model, &realRatio, &cost, &ttftMS, &tokensUsed, &source, &checkedAt); err != nil {
				continue
			}
			history = append(history, map[string]interface{}{
				"model": model, "real_ratio": realRatio, "cost": cost,
				"ttft_ms": ttftMS, "tokens_used": tokensUsed,
				"source": source, "checked_at": checkedAt.Format(time.RFC3339),
			})
		}
		rows.Close()
	}

	// 各模型最新实测
	latest := map[string]interface{}{}
	rows, err = h.db.Pool.Query(ctx, `
		SELECT DISTINCT ON (model) model, real_ratio, source, checked_at
		FROM probe_results
		WHERE upstream_id = $1 AND success = true
		ORDER BY model, checked_at DESC
	`, channelID)
	if err == nil {
		for rows.Next() {
			var model, source string
			var realRatio float64
			var checkedAt time.Time
			if err := rows.Scan(&model, &realRatio, &source, &checkedAt); err != nil {
				continue
			}
			latest[model] = map[string]interface{}{
				"real_ratio": realRatio, "source": source,
				"checked_at": checkedAt.Format(time.RFC3339),
			}
		}
		rows.Close()
	}

	// 倍率检测分组（含代表倍率与组内明细）
	groups := []map[string]interface{}{}
	rows, err = h.db.Pool.Query(ctx, `
		SELECT id, name, default_model, COALESCE(models::text, '[]')
		FROM channel_ratio_groups
		WHERE channel_id = $1
		ORDER BY id
	`, channelID)
	if err == nil {
		for rows.Next() {
			var gid int
			var name, defaultModel, modelsJSON string
			if err := rows.Scan(&gid, &name, &defaultModel, &modelsJSON); err != nil {
				continue
			}
			var models []string
			_ = json.Unmarshal([]byte(modelsJSON), &models)
			if models == nil {
				models = []string{}
			}

			g := map[string]interface{}{
				"id":            gid,
				"name":          name,
				"default_model": defaultModel,
				"models":        models,
				"members":       []map[string]interface{}{},
			}
			members := make([]map[string]interface{}, 0, len(models))
			for _, m := range models {
				entry := map[string]interface{}{"model": m}
				if l, ok := latest[m].(map[string]interface{}); ok {
					entry["real_ratio"] = l["real_ratio"]
					entry["source"] = l["source"]
					entry["checked_at"] = l["checked_at"]
				}
				members = append(members, entry)
			}
			g["members"] = members
			if l, ok := latest[defaultModel].(map[string]interface{}); ok {
				g["default_ratio"] = l["real_ratio"]
				g["default_source"] = l["source"]
				g["default_checked_at"] = l["checked_at"]
			}
			groups = append(groups, g)
		}
		rows.Close()
	}

	c.JSON(200, gin.H{
		"channel_id": channelID,
		"epoch":      epoch,
		"declared":   declared,
		"history":    history,
		"latest":     latest,
		"groups":     groups,
	})
}

// modelPriceValues 读取模型官方价格（未收录返回 0,0）
func (h *RatioHandler) modelPriceValues(ctx context.Context, model string) (float64, float64) {
	var in, out float64
	if err := h.db.Pool.QueryRow(ctx, `
		SELECT input_price_per_m, output_price_per_m FROM model_prices WHERE model = $1
	`, model).Scan(&in, &out); err != nil {
		return 0, 0
	}
	return in, out
}

// estimateProbeCost 预估单次探测成本：按 (估算 prompt 5000 + maxTokens) × 输出价/1M，
// 无官方价时按保守 $30/1M
func estimateProbeCost(maxTokens int, officialIn, officialOut float64) float64 {
	perM := 30.0
	if officialOut > 0 {
		perM = officialOut
	} else if officialIn > 0 {
		perM = officialIn
	}
	const promptEst = 5000
	return float64(promptEst+maxTokens) * perM / 1_000_000
}

// validGroupModels 校验分组模型：至少一个模型、默认模型必须在组内
func validGroupModels(models []string, defaultModel string) error {
	if len(models) == 0 {
		return fmt.Errorf("请至少选择一个模型")
	}
	if defaultModel == "" {
		return fmt.Errorf("请选择默认检测模型")
	}
	found := false
	for _, m := range models {
		if m == defaultModel {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("默认检测模型必须在组内模型中")
	}
	return nil
}

// ProbeRatio POST /admin/channels/:id/probe-ratio - 立即实测指定模型的真实倍率
func (h *RatioHandler) ProbeRatio(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}

	var req struct {
		Model           string  `json:"model" binding:"required"`
		MaxTokens       int     `json:"max_tokens"`
		InputPricePerM  float64 `json:"input_price_per_m"`  // 可选：官方输入价（$/1M，价格库未收录时声明）
		OutputPricePerM float64 `json:"output_price_per_m"` // 可选：官方输出价（$/1M）
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	resp, status := h.probeChannelModel(c.Request.Context(), channelID, req.Model, clampProbeTokens(req.MaxTokens), req.InputPricePerM, req.OutputPricePerM)
	c.JSON(status, resp)
}

// probeChannelModel 单模型手动实测公共路径：
// 模型校验 → 官方价格确认（未收录可随请求声明并入库）→ Redis 并发锁 → 三级预算 → 探测 → 快照失效
func (h *RatioHandler) probeChannelModel(ctx context.Context, channelID int, model string, maxTokens int, declaredInPerM, declaredOutPerM float64) (gin.H, int) {
	upstream, mapping, err := h.loadUpstream(ctx, channelID)
	if err != nil {
		return gin.H{"error": "channel not found"}, 404
	}

	// 模型必须是该站点映射内的键
	if _, ok := mapping[model]; !ok {
		return gin.H{"error": fmt.Sprintf("模型 %q 不在该站点的模型映射中", model)}, 400
	}

	// 官方价格：价格库未收录时，用户随请求声明并自动入库；未声明则拒绝
	hasPrice, err := h.modelPriceExists(ctx, model)
	if err != nil {
		return gin.H{"error": "无法查询官方价格库"}, 500
	}
	if !hasPrice {
		if declaredInPerM > 0 && declaredOutPerM > 0 {
			if err := h.upsertModelPrice(ctx, model, declaredInPerM, declaredOutPerM, 0, 0, "用户声明（实测时提交）"); err != nil {
				return gin.H{"error": "官方价格写入失败"}, 500
			}
			router.InvalidateSnapshotCache(ctx, h.redis)
		} else {
			return gin.H{"error": fmt.Sprintf("价格库中暂无模型 %q 的官方价格，请在请求中声明输入/输出价（$/1M）或先到设置页添加", model)}, 400
		}
	}

	// 并发锁：同站点同模型同一时间只允许一个探测（带所有权，超时自动释放且不会误删他人锁）
	lockKey := fmt.Sprintf("ratio:probe:%d:%s", channelID, model)
	lockToken := fmt.Sprintf("%d-%d", time.Now().UnixNano(), channelID)
	locked := false
	if h.redis != nil && h.redis.Client != nil {
		ok, lerr := h.redis.Client.SetNX(ctx, lockKey, lockToken, ratioProbeLockTTL).Result()
		if lerr != nil {
			h.logger.Warn("Ratio probe lock check failed, proceeding without lock", zap.Error(lerr))
		} else if !ok {
			return gin.H{"error": "该站点的该模型已有探测正在进行，请稍后再试"}, 409
		} else {
			locked = true
		}
	}
	if locked {
		defer func() {
			releaseOwnedLockScript.Run(ctx, h.redis.Client, []string{lockKey}, lockToken)
		}()
	}

	// 预算检查：全局 + 站点有效预算（失败关闭：读不到花费时拒绝探测）
	globalSpent, err := h.probe.TodaySpent(ctx)
	if err != nil {
		return gin.H{"error": "无法读取今日探针花费，探测已取消"}, 500
	}
	globalBudget := h.cfg.Checker.DailyProbeBudget
	remainingGlobal := globalBudget - globalSpent
	if remainingGlobal <= 0 {
		return gin.H{"error": "今日全局探测预算已用完", "remaining_budget": 0.0}, 429
	}
	upstreamSpent, err := h.probe.UpstreamTodaySpent(ctx, channelID)
	if err != nil {
		return gin.H{"error": "无法读取站点探针花费，探测已取消"}, 500
	}
	effectiveBudget := h.channelEffectiveBudget(ctx, channelID, globalBudget)
	remainingChannel := effectiveBudget - upstreamSpent
	if remainingChannel <= 0 {
		return gin.H{
			"error":            "该站点今日探测预算已用完",
			"remaining_budget": round2(remainingChannel),
		}, 429
	}

	// 原子预留预算（Redis）：按预估单次成本预留，探测结束后按实际成本退还，
	// 防止并发探测 check-then-act 导致超支。
	priceIn, priceOut := h.modelPriceValues(ctx, model)
	estCost := estimateProbeCost(maxTokens, priceIn, priceOut)
	reserve := math.Min(estCost, math.Min(remainingGlobal, remainingChannel))
	if reserve <= 0 {
		return gin.H{"error": "今日探测预算已用完", "remaining_budget": round2(math.Min(remainingGlobal, remainingChannel))}, 429
	}
	budgetKeyCh, budgetKeyGl := probeBudgetKeys(channelID)
	reserved := false
	refund := 0.0
	if h.redis != nil && h.redis.Client != nil {
		n, rerr := reserveProbeBudgetScript.Run(ctx, h.redis.Client,
			[]string{budgetKeyCh, budgetKeyGl},
			reserve, remainingChannel, remainingGlobal).Int()
		if rerr != nil {
			return gin.H{"error": "预算预留失败，探测已取消"}, 500
		}
		if n == 0 {
			return gin.H{"error": "今日探测预算已用完（并发探测占用中）", "remaining_budget": round2(math.Min(remainingGlobal, remainingChannel))}, 429
		}
		reserved = true
		refund = reserve
	}
	// 探测结束后按实际成本退还（失败全额退还；成功分支会把 refund 改写为 reserve-cost）
	defer func() {
		if reserved {
			refundProbeBudgetScript.Run(ctx, h.redis.Client, []string{budgetKeyCh, budgetKeyGl}, refund)
		}
	}()

	epoch, err := h.db.GetCurrentEpoch(ctx)
	if err != nil {
		return gin.H{"error": "无法读取 epoch"}, 500
	}

	res, err := h.probe.ProbeModel(ctx, *upstream, epoch, model, maxTokens, checker.ProbeSourceManual)
	if err != nil {
		h.logger.Warn("Manual ratio probe failed",
			zap.Int("channel_id", channelID), zap.String("model", model), zap.Error(err))
		var msg string
		switch res.Stage {
		case "balance_before", "balance_after":
			if res.Error != "" {
				msg = "无法读取该站点的余额：" + res.Error
			} else {
				msg = "无法读取该站点的余额（可能未开放 /api/user/self 或 Access Token 无效），无法实测倍率"
			}
		case "chat":
			msg = "推理请求失败：" + res.Error
		default:
			msg = "探测失败：" + res.Error
		}
		return gin.H{"error": msg, "stage": res.Stage}, 502
	}

	// 实测成功：失效快照缓存，让新倍率立即参与路由；按实际成本退还预留差额
	router.InvalidateSnapshotCache(ctx, h.redis)
	if reserved {
		refund = reserve - res.Cost
		if refund < 0 {
			refund = 0
		}
	}

	resp := gin.H{
		"channel_id":        channelID,
		"model":             model,
		"real_ratio":        res.RealRatio,
		"basis":             res.Basis,
		"price_per_million": round2(res.RealRatio * 10.0),
		"cost":              res.Cost,
		"ttft_ms":           res.TTFTMS,
		"tokens_used":       res.TokensUsed,
		"prompt_tokens":     res.PromptTokens,
		"completion_tokens": res.CompletionTokens,
		"balance_before":    res.BalanceBefore,
		"balance_after":     res.BalanceAfter,
		"epoch":             epoch,
		"source":            checker.ProbeSourceManual,
		"checked_at":        time.Now().Format(time.RFC3339),
	}
	if res.Basis == checker.BasisOfficial {
		resp["official_input_per_m"] = res.OfficialInputPerM
		resp["official_output_per_m"] = res.OfficialOutputPerM
		// 推算实际单价：实测倍率 × 官网输入/输出价
		resp["estimated_input_per_m"] = round2(res.RealRatio * res.OfficialInputPerM)
		resp["estimated_output_per_m"] = round2(res.RealRatio * res.OfficialOutputPerM)
	}
	if declared := h.latestDeclared(ctx, channelID, model, epoch); declared != nil {
		resp["declared"] = declared
		resp["drift_pct"] = round2(driftPct(res, declared))
	}
	if res.RealRatio <= 0 {
		resp["warning"] = "余额精度不足以区分本次扣费，建议提高 max_tokens 后重测"
	}
	return resp, 200
}

// ==================== 倍率检测分组 ====================

type ratioGroupPayload struct {
	Name         string   `json:"name"`
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models"`
}

// loadRatioGroup 按组 ID 加载分组（校验归属渠道）
func (h *RatioHandler) loadRatioGroup(ctx context.Context, channelID, groupID int) (id int, name, defaultModel string, models []string, err error) {
	var modelsJSON string
	err = h.db.Pool.QueryRow(ctx, `
		SELECT id, name, default_model, COALESCE(models::text, '[]')
		FROM channel_ratio_groups
		WHERE id = $1 AND channel_id = $2
	`, groupID, channelID).Scan(&id, &name, &defaultModel, &modelsJSON)
	if err != nil {
		return 0, "", "", nil, err
	}
	_ = json.Unmarshal([]byte(modelsJSON), &models)
	if models == nil {
		models = []string{}
	}
	return id, name, defaultModel, models, nil
}

func (h *RatioHandler) parseRatioGroupPayload(c *gin.Context) (*ratioGroupPayload, bool) {
	var req ratioGroupPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return nil, false
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(400, gin.H{"error": "分组名称不能为空"})
		return nil, false
	}
	if err := validGroupModels(req.Models, req.DefaultModel); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return nil, false
	}
	return &req, true
}

// CreateRatioGroup POST /admin/channels/:id/ratio-groups - 新建倍率检测分组
func (h *RatioHandler) CreateRatioGroup(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}
	ctx := context.Background()

	var exists bool
	if err := h.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstreams WHERE id = $1)`, channelID).Scan(&exists); err != nil || !exists {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}

	req, ok := h.parseRatioGroupPayload(c)
	if !ok {
		return
	}

	// 模型必须属于该站点的映射键
	if _, mapping, err := h.loadUpstream(ctx, channelID); err == nil {
		for _, m := range req.Models {
			if _, ok := mapping[m]; !ok {
				c.JSON(400, gin.H{"error": fmt.Sprintf("模型 %q 不在该站点的模型映射中", m)})
				return
			}
		}
	}

	modelsJSON, _ := json.Marshal(req.Models)
	var id int
	err = h.db.Pool.QueryRow(ctx, `
		INSERT INTO channel_ratio_groups (channel_id, name, default_model, models)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, channelID, req.Name, req.DefaultModel, modelsJSON).Scan(&id)
	if err != nil {
		h.logger.Warn("Failed to create ratio group", zap.Error(err))
		c.JSON(409, gin.H{"error": "分组创建失败（分组名可能已存在）"})
		return
	}

	c.JSON(201, gin.H{"id": id, "message": "分组已创建"})
}

// UpdateRatioGroup PATCH /admin/channels/:id/ratio-groups/:gid - 更新倍率检测分组
func (h *RatioHandler) UpdateRatioGroup(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}
	groupID, err := strconv.Atoi(c.Param("gid"))
	if err != nil || groupID <= 0 {
		c.JSON(400, gin.H{"error": "invalid group id"})
		return
	}
	ctx := context.Background()

	_, curName, curDefault, curModels, err := h.loadRatioGroup(ctx, channelID, groupID)
	if err != nil {
		c.JSON(404, gin.H{"error": "group not found"})
		return
	}

	var req struct {
		Name         *string   `json:"name"`
		DefaultModel *string   `json:"default_model"`
		Models       *[]string `json:"models"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	name := curName
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			c.JSON(400, gin.H{"error": "分组名称不能为空"})
			return
		}
	}
	models := curModels
	if req.Models != nil {
		models = *req.Models
	}
	defaultModel := curDefault
	if req.DefaultModel != nil {
		defaultModel = *req.DefaultModel
	}

	if err := validGroupModels(models, defaultModel); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 模型必须属于该站点的映射键
	if req.Models != nil {
		if _, mapping, err := h.loadUpstream(ctx, channelID); err == nil {
			for _, m := range models {
				if _, ok := mapping[m]; !ok {
					c.JSON(400, gin.H{"error": fmt.Sprintf("模型 %q 不在该站点的模型映射中", m)})
					return
				}
			}
		}
	}

	modelsJSON, _ := json.Marshal(models)
	ct, err := h.db.Pool.Exec(ctx, `
		UPDATE channel_ratio_groups
		SET name = $1, default_model = $2, models = $3, updated_at = NOW()
		WHERE id = $4 AND channel_id = $5
	`, name, defaultModel, modelsJSON, groupID, channelID)
	if err != nil {
		c.JSON(409, gin.H{"error": "分组更新失败（分组名可能已存在）"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "group not found"})
		return
	}

	c.JSON(200, gin.H{"message": "分组已更新"})
}

// DeleteRatioGroup DELETE /admin/channels/:id/ratio-groups/:gid - 删除倍率检测分组
func (h *RatioHandler) DeleteRatioGroup(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}
	groupID, err := strconv.Atoi(c.Param("gid"))
	if err != nil || groupID <= 0 {
		c.JSON(400, gin.H{"error": "invalid group id"})
		return
	}
	ctx := context.Background()

	ct, err := h.db.Pool.Exec(ctx, `DELETE FROM channel_ratio_groups WHERE id = $1 AND channel_id = $2`, groupID, channelID)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to delete ratio group"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "group not found"})
		return
	}

	c.JSON(200, gin.H{"message": "分组已删除"})
}

// ProbeRatioGroup POST /admin/channels/:id/ratio-groups/:gid/probe - 实测分组的默认检测模型
func (h *RatioHandler) ProbeRatioGroup(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}
	groupID, err := strconv.Atoi(c.Param("gid"))
	if err != nil || groupID <= 0 {
		c.JSON(400, gin.H{"error": "invalid group id"})
		return
	}
	ctx := c.Request.Context()

	_, name, defaultModel, _, err := h.loadRatioGroup(ctx, channelID, groupID)
	if err != nil {
		c.JSON(404, gin.H{"error": "group not found"})
		return
	}
	if defaultModel == "" {
		c.JSON(400, gin.H{"error": "该分组未设置默认检测模型"})
		return
	}

	resp, status := h.probeChannelModel(ctx, channelID, defaultModel, defaultManualProbeTokens, 0, 0)
	if status == 200 {
		resp["group_id"] = groupID
		resp["group_name"] = name
	}
	c.JSON(status, resp)
}

// ==================== 官方模型价格 ====================

// modelPriceExists 查询价格库是否收录该模型
func (h *RatioHandler) modelPriceExists(ctx context.Context, model string) (bool, error) {
	var exists bool
	err := h.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_prices WHERE model = $1)`, model).Scan(&exists)
	return exists, err
}

// upsertModelPrice 写入/更新官方模型价格（缓存价 0 = 空）
func (h *RatioHandler) upsertModelPrice(ctx context.Context, model string, inputPerM, outputPerM, cachedReadPerM, cachedWritePerM float64, note string) error {
	_, err := h.db.Pool.Exec(ctx, `
		INSERT INTO model_prices (model, input_price_per_m, output_price_per_m, cached_read_per_m, cached_write_per_m, note, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (model)
		DO UPDATE SET input_price_per_m = $2, output_price_per_m = $3,
		              cached_read_per_m = $4, cached_write_per_m = $5, note = $6, updated_at = NOW()
	`, model, inputPerM, outputPerM, nullableFloat(cachedReadPerM), nullableFloat(cachedWritePerM), note)
	return err
}

// ListModelPrices GET /admin/model-prices - 官方模型价格库
func (h *RatioHandler) ListModelPrices(c *gin.Context) {
	ctx := context.Background()
	rows, err := h.db.Pool.Query(ctx, `
		SELECT model, input_price_per_m, output_price_per_m,
		       COALESCE(cached_read_per_m, 0), COALESCE(cached_write_per_m, 0),
		       note, updated_at
		FROM model_prices
		ORDER BY model
	`)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query model prices"})
		return
	}
	defer rows.Close()

	prices := []map[string]interface{}{}
	for rows.Next() {
		var model, note string
		var inputPerM, outputPerM, cachedReadPerM, cachedWritePerM float64
		var updatedAt time.Time
		if err := rows.Scan(&model, &inputPerM, &outputPerM, &cachedReadPerM, &cachedWritePerM, &note, &updatedAt); err != nil {
			continue
		}
		prices = append(prices, map[string]interface{}{
			"model":              model,
			"input_price_per_m":  inputPerM,
			"output_price_per_m": outputPerM,
			"cached_read_per_m":  nullableFloat(cachedReadPerM),
			"cached_write_per_m": nullableFloat(cachedWritePerM),
			"note":               note,
			"updated_at":         updatedAt.Format(time.RFC3339),
		})
	}

	c.JSON(200, gin.H{"prices": prices, "total": len(prices)})
}

// UpsertModelPrice POST /admin/model-prices - 添加/更新官方模型价格（路由立即生效）
func (h *RatioHandler) UpsertModelPrice(c *gin.Context) {
	var req struct {
		Model            string  `json:"model" binding:"required"`
		InputPricePerM   float64 `json:"input_price_per_m"`
		OutputPricePerM  float64 `json:"output_price_per_m"`
		CachedReadPerM   float64 `json:"cached_read_per_m"`
		CachedWritePerM  float64 `json:"cached_write_per_m"`
		Note             string  `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		c.JSON(400, gin.H{"error": "模型名不能为空"})
		return
	}
	if req.InputPricePerM <= 0 || req.OutputPricePerM <= 0 {
		c.JSON(400, gin.H{"error": "输入/输出价格必须大于 0"})
		return
	}

	ctx := context.Background()
	if err := h.upsertModelPrice(ctx, req.Model, req.InputPricePerM, req.OutputPricePerM, req.CachedReadPerM, req.CachedWritePerM, req.Note); err != nil {
		h.logger.Warn("Failed to upsert model price", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to save model price"})
		return
	}
	router.InvalidateSnapshotCache(ctx, h.redis)

	c.JSON(200, gin.H{"message": "官方价格已保存（立即生效）"})
}

// DeleteModelPrice DELETE /admin/model-prices/:model - 删除官方模型价格
func (h *RatioHandler) DeleteModelPrice(c *gin.Context) {
	model := c.Param("model")
	ctx := context.Background()

	ct, err := h.db.Pool.Exec(ctx, `DELETE FROM model_prices WHERE model = $1`, model)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to delete model price"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "model price not found"})
		return
	}
	router.InvalidateSnapshotCache(ctx, h.redis)

	c.JSON(200, gin.H{"message": "官方价格已删除"})
}

// GetChannelMetrics GET /admin/channel-metrics?group_id= - 站点综合信息：
// 倍率（各模型最新实测）、余额（24h 序列）、成功率/延迟（24h 小时桶）、
// 健康（最近 50 次存活探测）、倍率上限与超限标记、默认检测模型
func (h *RatioHandler) GetChannelMetrics(c *gin.Context) {
	gid := parseGroupIDParam(c)
	ctx := context.Background()

	// 1. 站点列表（支持分组筛选）
	rows, err := h.db.Pool.Query(ctx, `
		SELECT u.id, u.name, u.enabled, COALESCE(u.ratio_limit, 0)
		FROM upstreams u
		WHERE $1::int IS NULL OR EXISTS (
			SELECT 1 FROM channel_group_members cgm WHERE cgm.channel_id = u.id AND cgm.group_id = $1
		)
		ORDER BY u.id
	`, gid)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query channels"})
		return
	}
	defer rows.Close()

	type chBase struct {
		id      int
		name    string
		enabled bool
		limit   float64
	}
	var bases []chBase
	for rows.Next() {
		var b chBase
		if err := rows.Scan(&b.id, &b.name, &b.enabled, &b.limit); err != nil {
			continue
		}
		bases = append(bases, b)
	}

	// 2. 每站点每模型最新实测倍率
	ratioMap := map[int][]map[string]interface{}{}
	rrows, err := h.db.Pool.Query(ctx, `
		SELECT DISTINCT ON (upstream_id, model) upstream_id, model, real_ratio, source, checked_at
		FROM probe_results
		WHERE success = true
		ORDER BY upstream_id, model, checked_at DESC
	`)
	if err == nil {
		for rrows.Next() {
			var cid int
			var model, source string
			var ratio float64
			var checkedAt time.Time
			if err := rrows.Scan(&cid, &model, &ratio, &source, &checkedAt); err != nil {
				continue
			}
			ratioMap[cid] = append(ratioMap[cid], map[string]interface{}{
				"model": model, "real_ratio": ratio, "source": source,
				"checked_at": checkedAt.Format(time.RFC3339),
			})
		}
		rrows.Close()
	}

	// 3. 每站点默认检测模型（第一个倍率分组的默认模型）
	groupDefault := map[int]string{}
	grows, err := h.db.Pool.Query(ctx, `
		SELECT DISTINCT ON (channel_id) channel_id, default_model
		FROM channel_ratio_groups
		ORDER BY channel_id, id
	`)
	if err == nil {
		for grows.Next() {
			var cid int
			var dm string
			if err := grows.Scan(&cid, &dm); err != nil {
				continue
			}
			if dm != "" {
				groupDefault[cid] = dm
			}
		}
		grows.Close()
	}

	// 4. 余额序列（24h 成功记录）+ 当前余额
	balanceSeries := map[int][]map[string]interface{}{}
	balanceCurrent := map[int]map[string]interface{}{}
	brows, err := h.db.Pool.Query(ctx, `
		SELECT channel_id, balance, currency, checked_at
		FROM balance_checks
		WHERE source != '' AND checked_at >= NOW() - INTERVAL '24 hours'
		ORDER BY channel_id, checked_at
	`)
	if err == nil {
		for brows.Next() {
			var cid int
			var balance float64
			var currency string
			var checkedAt time.Time
			if err := brows.Scan(&cid, &balance, &currency, &checkedAt); err != nil {
				continue
			}
			balanceSeries[cid] = append(balanceSeries[cid], map[string]interface{}{
				"t": checkedAt.Format(time.RFC3339), "v": balance,
			})
			balanceCurrent[cid] = map[string]interface{}{
				"balance": balance, "currency": currency,
				"checked_at": checkedAt.Format(time.RFC3339),
			}
		}
		brows.Close()
	}

	// 5. 小时级成功率/延迟（24h，分组过滤）
	hourlyMap := map[int]map[string]map[string]interface{}{}
	hrows, err := h.db.Pool.Query(ctx, `
		SELECT channel_id, date_trunc('hour', created_at) AS bucket,
		       COUNT(*),
		       COALESCE(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 0),
		       COALESCE(AVG(total_duration_ms), 0)
		FROM request_history
		WHERE created_at >= NOW() - INTERVAL '24 hours'
		  AND ($1::int IS NULL OR group_id = $1)
		GROUP BY channel_id, bucket
		ORDER BY channel_id, bucket
	`, gid)
	if err == nil {
		for hrows.Next() {
			var cid int
			var bucket time.Time
			var cnt int
			var sr, lat float64
			if err := hrows.Scan(&cid, &bucket, &cnt, &sr, &lat); err != nil {
				continue
			}
			if hourlyMap[cid] == nil {
				hourlyMap[cid] = map[string]map[string]interface{}{}
			}
			hourlyMap[cid][bucket.Format("2006-01-02 15:04")] = map[string]interface{}{
				"count": cnt, "success_rate": sr, "latency_ms": lat,
			}
		}
		hrows.Close()
	}

	// 6. 最近 50 次存活探测（每站点，按时间升序；health_checks 使用 upstream_id 列）
	healthMap := map[int][]map[string]interface{}{}
	hhrows, err := h.db.Pool.Query(ctx, `
		SELECT upstream_id, is_alive, latency_ms, checked_at FROM (
			SELECT upstream_id, is_alive, latency_ms, checked_at,
			       ROW_NUMBER() OVER (PARTITION BY upstream_id ORDER BY checked_at DESC) AS rn
			FROM health_checks
			WHERE checked_at >= NOW() - INTERVAL '24 hours'
		) t WHERE rn <= 50
		ORDER BY upstream_id, checked_at
	`)
	if err == nil {
		for hhrows.Next() {
			var cid int
			var alive bool
			var latency *int
			var checkedAt time.Time
			if err := hhrows.Scan(&cid, &alive, &latency, &checkedAt); err != nil {
				continue
			}
			healthMap[cid] = append(healthMap[cid], map[string]interface{}{
				"checked_at": checkedAt.Format(time.RFC3339),
				"alive":      alive,
				"latency_ms": latency,
			})
		}
		hhrows.Close()
	}

	// 组装（小时桶补全 24 个，空桶为 null 以在图中留空）
	channels := []map[string]interface{}{}
	now := time.Now().Truncate(time.Hour)
	for _, b := range bases {
		ratios := ratioMap[b.id]
		overLimit := false
		if b.limit > 0 {
			for _, mr := range ratios {
				if v, ok := mr["real_ratio"].(float64); ok && v > b.limit {
					overLimit = true
					break
				}
			}
		}
		probeModel := groupDefault[b.id]
		if probeModel == "" && len(ratios) > 0 {
			if m, ok := ratios[0]["model"].(string); ok {
				probeModel = m
			}
		}

		hourly := make([]map[string]interface{}, 0, 24)
		for i := 23; i >= 0; i-- {
			bucket := now.Add(-time.Duration(i) * time.Hour)
			key := bucket.Format("2006-01-02 15:04")
			label := bucket.Format("15:00")
			if v, ok := hourlyMap[b.id][key]; ok {
				v["hour"] = label
				hourly = append(hourly, v)
			} else {
				hourly = append(hourly, map[string]interface{}{
					"hour": label, "count": 0, "success_rate": nil, "latency_ms": nil,
				})
			}
		}

		channels = append(channels, map[string]interface{}{
			"id":                  b.id,
			"name":                b.name,
			"enabled":             b.enabled,
			"ratio_limit":         b.limit,
			"over_limit":          overLimit,
			"default_probe_model": probeModel,
			"ratios":              orEmptyList(ratioMap[b.id]),
			"balance_current":     balanceCurrent[b.id],
			"balance_series":      orEmptyList(balanceSeries[b.id]),
			"hourly":              hourly,
			"health":              orEmptyList(healthMap[b.id]),
		})
	}

	c.JSON(200, gin.H{
		"channels":     channels,
		"total":        len(channels),
		"generated_at": time.Now().Format(time.RFC3339),
	})
}
