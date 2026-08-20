package router

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"smart-router/internal/store"

	"golang.org/x/sync/singleflight"
)

// HealthSnapshot 健康数据快照（不可变）
type HealthSnapshot struct {
	Epoch       int64                  `json:"epoch"`
	Channels    map[int]*ChannelHealth `json:"channels"`
	ModelPrices map[string]*ModelPrice `json:"model_prices"`
	GeneratedAt time.Time              `json:"generated_at"`
	ContentHash string                 `json:"content_hash"`
}

// ModelPrice 官方模型价格（$/1M）
type ModelPrice struct {
	Model      string  `json:"model"`
	InputPerM  float64 `json:"input_per_m"`
	OutputPerM float64 `json:"output_per_m"`
}

// ChannelHealth 单个渠道的健康数据
type ChannelHealth struct {
	ID           int               `json:"id"`
	Name         string            `json:"name"`
	BaseURL      string            `json:"base_url"`
	Role         string            `json:"role"`
	Protocol     string            `json:"protocol"`   // openai（默认）| anthropic
	RelayType    string            `json:"relay_type"` // newapi | sub2api | custom
	Enabled      bool              `json:"enabled"`
	UserPriority int               `json:"user_priority"`
	ModelMapping map[string]string `json:"model_mapping"`
	Capabilities []string          `json:"capabilities"`
	Weight       int               `json:"weight"`
	GroupIDs     []int             `json:"group_ids"`

	// 数据库中的渠道级超时配置（代理层映射为 per-request 超时，P2-07）
	TimeoutConnectMS   int `json:"timeout_connect_ms"`
	TimeoutFirstByteMS int `json:"timeout_first_byte_ms"`
	TimeoutTotalMS     int `json:"timeout_total_ms"`

	// 从健康检测子系统读取
	IsAlive        bool                     `json:"is_alive"`
	DeclaredPrice  map[string]*PriceProfile `json:"declared_price"`
	RealRatio      map[string]float64       `json:"real_ratio"`
	RealRatioBasis map[string]string        `json:"real_ratio_basis"` // official | baseline
	// 探测当时使用的官网价快照（价格库调整后历史倍率仍保持一致）
	RealRatioOfficialInPerM  map[string]float64 `json:"real_ratio_official_in_per_m"`
	RealRatioOfficialOutPerM map[string]float64 `json:"real_ratio_official_out_per_m"`
	TTFTP50                  map[string]int     `json:"ttft_p50"`
	TTFTP95                  map[string]int     `json:"ttft_p95"`

	// 从请求历史计算（滑动窗口）
	RecentAttempts   int     `json:"recent_attempts"`
	RecentSuccesses  int     `json:"recent_successes"`
	ReliabilityScore float64 `json:"reliability_score"`

	// 熔断状态（渠道级聚合，供仪表盘/指标使用）
	CircuitState string    `json:"circuit_state"`
	CoolingUntil time.Time `json:"cooling_until"`

	// 分组级熔断状态（P1-04）：key 为 "0"（全局桶）或分组 ID 字符串。
	// 每个分组桶内聚合该组所有模型行的最严重状态（已折算冷却到期 → half_open）。
	CircuitStates map[string]string `json:"circuit_states"`
}

// circuitGroupKey 熔断分组桶键：nil → 全局桶 "0"。
func circuitGroupKey(groupID *int) string {
	if groupID == nil {
		return "0"
	}
	return fmt.Sprintf("%d", *groupID)
}

// CircuitStateForGroup 取指定分组桶的熔断状态：
// 优先分组专属桶，缺失时回退全局桶，再回退 closed。
func (ch *ChannelHealth) CircuitStateForGroup(groupID *int) string {
	if ch.CircuitStates == nil {
		if ch.CircuitState == "" {
			return "closed"
		}
		return ch.CircuitState
	}
	if s, ok := ch.CircuitStates[circuitGroupKey(groupID)]; ok {
		return s
	}
	if s, ok := ch.CircuitStates["0"]; ok {
		return s
	}
	return "closed"
}

// PriceProfile 价格配置
type PriceProfile struct {
	InputPrice      float64 `json:"input_price"`  // 每 1M tokens
	OutputPrice     float64 `json:"output_price"` // 每 1M tokens
	CacheReadPrice  float64 `json:"cache_read_price"`
	CacheWritePrice float64 `json:"cache_write_price"`
	PerRequestFee   float64 `json:"per_request_fee"`
}

const snapshotCacheKey = "router:snapshot"
const snapshotCacheTTL = 10 * time.Second

// declaredRatioBasePerM 声明倍率（declared_prices.prompt_ratio / completion_ratio）
// 换算成 $/1M tokens 时使用的基准单价。
//
// 注意：这是一个未经上游核实的假设值。上游 /api/pricing 返回的是无量纲倍率，
// 具体基准取决于中转站实现（one-api 系通常以某个固定单价为倍率 1）。
// 声明价格只在「没有实测倍率」时作为兜底参与成本估算，实测倍率（basis=official）
// 优先级更高且是相对官网价的真实测量值，因此该常量的误差不影响主路径。
// 若要精确化，应实测校准后再调整，不要凭猜测改动。
const declaredRatioBasePerM = 10.0

// InvalidateSnapshotCache 清除快照缓存。
// 手动实测倍率/站点配置变更后调用，使下一请求立即基于新数据路由。
func InvalidateSnapshotCache(ctx context.Context, redis *store.RedisClient) {
	if redis == nil || redis.Client == nil {
		return
	}
	redis.Client.Del(ctx, snapshotCacheKey)
}

// snapshotGroup 进程内 singleflight：缓存失效瞬间的并发请求
// 合并为一次重建（E1），避免 N 个请求同时执行 N 轮全表查询
// 造成 DB 尖峰与路由延迟抖动。
var snapshotGroup singleflight.Group

// LoadSnapshot 加载当前快照（带 Redis 缓存）。
// 缓存未命中重建后，将快照按内容哈希归档到 snapshot_archive（供确定性重放，P1-05）。
// 缓存失效时并发重建由 singleflight 合并（E1）。
func LoadSnapshot(ctx context.Context, db *store.DB, redis *store.RedisClient) (*HealthSnapshot, error) {
	// 1. 尝试从 Redis 读取缓存
	if redis != nil {
		cached, err := redis.Client.Get(ctx, snapshotCacheKey).Bytes()
		if err == nil {
			var snapshot HealthSnapshot
			if json.Unmarshal(cached, &snapshot) == nil {
				return &snapshot, nil
			}
		}
	}

	// 2. 缓存未命中：singleflight 合并并发重建（同实例内一次实际查询）
	v, err, _ := snapshotGroup.Do("snapshot", func() (interface{}, error) {
		snapshot, err := buildSnapshot(ctx, db)
		if err != nil {
			return nil, err
		}

		// 3. 计算内容哈希（不含 epoch：相同内容共享哈希，归档去重）
		snapshot.ContentHash = computeSnapshotHash(snapshot)

		// 4. 归档历史快照（best-effort：归档失败不影响路由，仅影响重放确定性）
		archiveSnapshot(ctx, db, snapshot)

		// 5. 写入 Redis 缓存
		if redis != nil {
			data, _ := json.Marshal(snapshot)
			redis.Client.Set(ctx, snapshotCacheKey, data, snapshotCacheTTL)
		}

		return snapshot, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*HealthSnapshot), nil
}

// archiveSnapshot 将快照写入 snapshot_archive（按内容哈希去重）。
func archiveSnapshot(ctx context.Context, db *store.DB, snapshot *HealthSnapshot) {
	if snapshot == nil || snapshot.ContentHash == "" || db == nil || db.Pool == nil {
		return
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	_, _ = db.Pool.Exec(ctx, `
		INSERT INTO snapshot_archive (checksum, payload)
		VALUES ($1, $2)
		ON CONFLICT (checksum) DO NOTHING
	`, snapshot.ContentHash, string(payload))
}

func buildSnapshot(ctx context.Context, db *store.DB) (*HealthSnapshot, error) {
	// 读取当前 epoch
	epoch, err := db.GetCurrentEpoch(ctx)
	if err != nil {
		return nil, fmt.Errorf("get epoch: %w", err)
	}

	snapshot := &HealthSnapshot{
		Epoch:       epoch,
		Channels:    make(map[int]*ChannelHealth),
		ModelPrices: loadModelPrices(ctx, db),
		GeneratedAt: time.Now(),
	}

	// 读取所有渠道配置
	channels, err := loadChannels(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("load channels: %w", err)
	}

	byID := make(map[int]*ChannelHealth, len(channels))
	for _, ch := range channels {
		snapshot.Channels[ch.ID] = ch
		byID[ch.ID] = ch
	}

	// 健康数据按表批量加载（每张表一次查询，与渠道数无关）：
	// 逐渠道查询会让快照重建的往返次数随渠道数线性增长（N+1），
	// 缓存到期的瞬间所有并发请求都会撞上重建，放大为 DB 压力尖峰。
	// 单表失败不致命：该表对应字段退化为默认值，路由继续。
	loadAliveStatusAll(ctx, db, byID, epoch)
	loadDeclaredPricesAll(ctx, db, byID, epoch)
	loadProbeResultsAll(ctx, db, byID, epoch)
	loadRequestHistoryAll(ctx, db, byID)
	loadCircuitStatesAll(ctx, db, byID)

	return snapshot, nil
}

// loadChannels 读取所有渠道配置及其分组归属（含渠道级超时配置）
func loadChannels(ctx context.Context, db *store.DB) ([]*ChannelHealth, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, name, base_url, enabled, role, protocol, relay_type, user_priority,
		       model_mapping, capabilities, weight,
		       timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms
		FROM upstreams
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []*ChannelHealth
	for rows.Next() {
		ch := &ChannelHealth{
			DeclaredPrice:            make(map[string]*PriceProfile),
			RealRatio:                make(map[string]float64),
			RealRatioBasis:           make(map[string]string),
			RealRatioOfficialInPerM:  make(map[string]float64),
			RealRatioOfficialOutPerM: make(map[string]float64),
			TTFTP50:                  make(map[string]int),
			TTFTP95:                  make(map[string]int),
			GroupIDs:                 []int{},
			CircuitStates:            make(map[string]string),
			// 无请求历史时的默认可靠性 = 贝叶斯先验 5/10；批量加载只覆盖有历史的渠道
			ReliabilityScore: 0.5,
			CircuitState:     "closed",
		}

		var modelMappingJSON, capabilitiesJSON []byte
		if err := rows.Scan(
			&ch.ID, &ch.Name, &ch.BaseURL, &ch.Enabled, &ch.Role, &ch.Protocol, &ch.RelayType, &ch.UserPriority,
			&modelMappingJSON, &capabilitiesJSON, &ch.Weight,
			&ch.TimeoutConnectMS, &ch.TimeoutFirstByteMS, &ch.TimeoutTotalMS,
		); err != nil {
			return nil, err
		}

		json.Unmarshal(modelMappingJSON, &ch.ModelMapping)
		json.Unmarshal(capabilitiesJSON, &ch.Capabilities)

		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 批量加载分组归属
	members, err := db.Pool.Query(ctx, `SELECT channel_id, group_id FROM channel_group_members`)
	if err != nil {
		return nil, err
	}
	defer members.Close()

	groupsByChannel := map[int][]int{}
	for members.Next() {
		var chID, gID int
		if err := members.Scan(&chID, &gID); err != nil {
			continue
		}
		groupsByChannel[chID] = append(groupsByChannel[chID], gID)
	}
	for _, ch := range channels {
		ch.GroupIDs = groupsByChannel[ch.ID]
		if ch.GroupIDs == nil {
			ch.GroupIDs = []int{}
		}
	}

	return channels, nil
}

func loadAliveStatusAll(ctx context.Context, db *store.DB, byID map[int]*ChannelHealth, epoch int64) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (upstream_id) upstream_id, is_alive
		FROM health_checks
		WHERE epoch <= $1
		ORDER BY upstream_id, epoch DESC
	`, epoch)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var alive bool
		if err := rows.Scan(&id, &alive); err != nil {
			continue
		}
		if ch := byID[id]; ch != nil {
			ch.IsAlive = alive
		}
	}
}

func loadDeclaredPricesAll(ctx context.Context, db *store.DB, byID map[int]*ChannelHealth, epoch int64) {
	// 只取有效行：上游 /api/pricing 可能返回空模型名或零倍率，
	// 零价格会让 estimateCost 算出 0 成本，使该渠道在 price_first 下永远排第一。
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (upstream_id, model) upstream_id, model, prompt_ratio, completion_ratio
		FROM declared_prices
		WHERE epoch <= $1 AND model <> '' AND prompt_ratio > 0 AND completion_ratio > 0
		ORDER BY upstream_id, model, epoch DESC
	`, epoch)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var model string
		var promptRatio, completionRatio float64
		if err := rows.Scan(&id, &model, &promptRatio, &completionRatio); err != nil {
			continue
		}
		ch := byID[id]
		if ch == nil {
			continue
		}
		ch.DeclaredPrice[model] = &PriceProfile{
			InputPrice:  promptRatio * declaredRatioBasePerM,
			OutputPrice: completionRatio * declaredRatioBasePerM,
		}
	}
}

// ttftSampleWindow 计算 TTFT 分位数时每个 (渠道, 模型) 采用的最近样本数。
const ttftSampleWindow = 20

func loadProbeResultsAll(ctx context.Context, db *store.DB, byID map[int]*ChannelHealth, epoch int64) {
	// 1. 每个 (渠道, 模型) 最新一次成功探测的倍率与官网价快照。
	//    手动实测（source=manual）与定时探针写入同一 epoch，按时间取最新才正确。
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (upstream_id, model)
		       upstream_id, model, real_ratio, basis,
		       COALESCE(official_input_per_m, 0), COALESCE(official_output_per_m, 0)
		FROM probe_results
		WHERE epoch <= $1 AND success = true
		ORDER BY upstream_id, model, checked_at DESC
	`, epoch)
	if err == nil {
		for rows.Next() {
			var id int
			var model, basis string
			var realRatio, officialInPerM, officialOutPerM float64
			if err := rows.Scan(&id, &model, &realRatio, &basis, &officialInPerM, &officialOutPerM); err != nil {
				continue
			}
			ch := byID[id]
			if ch == nil {
				continue
			}
			ch.RealRatio[model] = realRatio
			ch.RealRatioBasis[model] = basis
			ch.RealRatioOfficialInPerM[model] = officialInPerM
			ch.RealRatioOfficialOutPerM[model] = officialOutPerM
		}
		rows.Close()
	}

	// 2. TTFT 分位数：对最近 ttftSampleWindow 个样本求真实 P50/P95。
	//    单次探测的 TTFT 抖动很大（实测同一渠道同一模型可在 2.2s~15.4s 之间），
	//    用最新一次充当 P50 会让 latency_first 的排序随机化。
	pRows, err := db.Pool.Query(ctx, `
		WITH ranked AS (
			SELECT upstream_id, model, ttft_ms,
			       ROW_NUMBER() OVER (
			           PARTITION BY upstream_id, model ORDER BY checked_at DESC
			       ) AS rn
			FROM probe_results
			WHERE epoch <= $1 AND success = true AND ttft_ms > 0
		)
		SELECT upstream_id, model,
		       percentile_cont(0.5)  WITHIN GROUP (ORDER BY ttft_ms)::int,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms)::int
		FROM ranked
		WHERE rn <= $2
		GROUP BY upstream_id, model
	`, epoch, ttftSampleWindow)
	if err != nil {
		return
	}
	defer pRows.Close()

	for pRows.Next() {
		var id, p50, p95 int
		var model string
		if err := pRows.Scan(&id, &model, &p50, &p95); err != nil {
			continue
		}
		if ch := byID[id]; ch != nil {
			ch.TTFTP50[model] = p50
			ch.TTFTP95[model] = p95
		}
	}
}

// loadModelPrices 加载官方模型价格库（快照全局共享，失败返回空表）
func loadModelPrices(ctx context.Context, db *store.DB) map[string]*ModelPrice {
	prices := map[string]*ModelPrice{}
	rows, err := db.Pool.Query(ctx, `
		SELECT model, input_price_per_m, output_price_per_m FROM model_prices
	`)
	if err != nil {
		return prices
	}
	defer rows.Close()

	for rows.Next() {
		var p ModelPrice
		if err := rows.Scan(&p.Model, &p.InputPerM, &p.OutputPerM); err != nil {
			continue
		}
		cp := p
		prices[p.Model] = &cp
	}
	return prices
}

// loadRequestHistoryAll 滑动窗口成功率（最近 1 小时，每渠道最多 1000 次）。
// 无历史的渠道保持 loadChannels 设置的默认值（贝叶斯先验 0.5）。
func loadRequestHistoryAll(ctx context.Context, db *store.DB, byID map[int]*ChannelHealth) {
	rows, err := db.Pool.Query(ctx, `
		SELECT channel_id,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0)
		FROM (
			SELECT channel_id, success,
			       ROW_NUMBER() OVER (
			           PARTITION BY channel_id ORDER BY created_at DESC
			       ) AS rn
			FROM request_history
			WHERE created_at >= NOW() - INTERVAL '1 hour'
		) recent
		WHERE rn <= 1000
		GROUP BY channel_id
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, attempts, successes int
		if err := rows.Scan(&id, &attempts, &successes); err != nil {
			continue
		}
		ch := byID[id]
		if ch == nil {
			continue
		}
		ch.RecentAttempts = attempts
		ch.RecentSuccesses = successes
		// 贝叶斯平滑：prior_success=5, prior_attempts=10
		ch.ReliabilityScore = float64(successes+5) / float64(attempts+10)
	}
}

// loadCircuitStatesAll 读取全部渠道的熔断状态（P1-04：按分组桶聚合）。
// 每个分组桶（含全局桶 "0"）取该组所有模型行中的最严重状态
// （open > degraded > half_open > closed），冷却到期折算为 half_open。
// 同时保留渠道级最严重状态（CircuitState）供仪表盘/指标使用。
func loadCircuitStatesAll(ctx context.Context, db *store.DB, byID map[int]*ChannelHealth) {
	rows, err := db.Pool.Query(ctx, `
		SELECT channel_id, COALESCE(group_id, 0) AS group_id, state,
		       COALESCE(cooling_until, '1970-01-01'::timestamp) AS cooling_until
		FROM circuit_states
		WHERE capability = ''
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	// 每渠道每分组桶：{最严重状态, open 行中最大的冷却截止}
	type bucketAgg struct {
		worstState string
		maxCooling time.Time
	}
	agg := map[int]map[string]*bucketAgg{}

	for rows.Next() {
		var channelID, groupID int
		var state string
		var coolingUntil time.Time
		if err := rows.Scan(&channelID, &groupID, &state, &coolingUntil); err != nil {
			continue
		}
		if byID[channelID] == nil {
			continue
		}
		buckets := agg[channelID]
		if buckets == nil {
			buckets = map[string]*bucketAgg{}
			agg[channelID] = buckets
		}
		key := fmt.Sprintf("%d", groupID)
		b := buckets[key]
		if b == nil {
			// 初值必须是 closed 而不是零值 ""：closed 的严重度为 0，
			// 用 rank 比较无法把 "" 抬升为 "closed"，会让整个桶留下空状态。
			b = &bucketAgg{worstState: "closed"}
			buckets[key] = b
		}
		if circuitStateRank[state] > circuitStateRank[b.worstState] {
			b.worstState = state
		}
		if state == "open" && (b.maxCooling.IsZero() || coolingUntil.After(b.maxCooling)) {
			b.maxCooling = coolingUntil
		}
	}

	now := time.Now()
	for channelID, buckets := range agg {
		ch := byID[channelID]
		if ch == nil {
			continue
		}
		ch.CircuitStates = make(map[string]string, len(buckets))
		overall := "closed"
		for key, b := range buckets {
			eff := EffectiveCircuitState(b.worstState, b.maxCooling, now)
			ch.CircuitStates[key] = eff
			if circuitStateRank[eff] > circuitStateRank[overall] {
				overall = eff
			}
		}
		ch.CircuitState = overall
	}
}

// circuitStateRank 熔断状态严重度排序（聚合分组桶时取最严重）。
var circuitStateRank = map[string]int{"closed": 0, "half_open": 1, "degraded": 2, "open": 3}


// EffectiveCircuitState 时间驱动的状态换算：open 且冷却到期 → half_open。
// 冷却截止取所有 open 行中的最大值（只有全部开闸行都冷却完成才允许探测）。
func EffectiveCircuitState(state string, coolingUntil, now time.Time) string {
	if state == "open" && !coolingUntil.IsZero() && !now.Before(coolingUntil) {
		return "half_open"
	}
	return state
}

// computeSnapshotHash 计算快照内容哈希（仅渠道与价格，不含 epoch 与时间戳）：
// 相同内容共享哈希，snapshot_archive 以此去重，确定性重放按此查档。
func computeSnapshotHash(snapshot *HealthSnapshot) string {
	// 序列化快照内容（不包括 epoch/generated_at/content_hash 本身）
	data, _ := json.Marshal(struct {
		Channels    map[int]*ChannelHealth `json:"channels"`
		ModelPrices map[string]*ModelPrice `json:"model_prices"`
	}{
		Channels:    snapshot.Channels,
		ModelPrices: snapshot.ModelPrices,
	})

	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}
