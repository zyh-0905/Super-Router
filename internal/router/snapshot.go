package router

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"smart-router/internal/store"
)

// HealthSnapshot 健康数据快照（不可变）
type HealthSnapshot struct {
	Epoch       int64                  `json:"epoch"`
	Channels    map[int]*ChannelHealth `json:"channels"`
	GeneratedAt time.Time              `json:"generated_at"`
	ContentHash string                 `json:"content_hash"`
}

// ChannelHealth 单个渠道的健康数据
type ChannelHealth struct {
	ID           int               `json:"id"`
	Name         string            `json:"name"`
	BaseURL      string            `json:"base_url"`
	Role         string            `json:"role"`
	Enabled      bool              `json:"enabled"`
	UserPriority int               `json:"user_priority"`
	ModelMapping map[string]string `json:"model_mapping"`
	Capabilities []string          `json:"capabilities"`
	Weight       int               `json:"weight"`
	GroupIDs     []int             `json:"group_ids"`

	// 从健康检测子系统读取
	IsAlive       bool                     `json:"is_alive"`
	DeclaredPrice map[string]*PriceProfile `json:"declared_price"`
	RealRatio     map[string]float64       `json:"real_ratio"`
	TTFTP50       map[string]int           `json:"ttft_p50"`
	TTFTP95       map[string]int           `json:"ttft_p95"`

	// 从请求历史计算（滑动窗口）
	RecentAttempts   int     `json:"recent_attempts"`
	RecentSuccesses  int     `json:"recent_successes"`
	ReliabilityScore float64 `json:"reliability_score"`

	// 熔断状态
	CircuitState string    `json:"circuit_state"`
	CoolingUntil time.Time `json:"cooling_until"`
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

// LoadSnapshot 加载当前快照（带 Redis 缓存）
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

	// 2. 缓存未命中，从 PG 重建
	snapshot, err := buildSnapshot(ctx, db)
	if err != nil {
		return nil, err
	}

	// 3. 计算内容哈希
	snapshot.ContentHash = computeSnapshotHash(snapshot)

	// 4. 写入 Redis 缓存
	if redis != nil {
		data, _ := json.Marshal(snapshot)
		redis.Client.Set(ctx, snapshotCacheKey, data, snapshotCacheTTL)
	}

	return snapshot, nil
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
		GeneratedAt: time.Now(),
	}

	// 读取所有渠道配置
	channels, err := loadChannels(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("load channels: %w", err)
	}

	// 填充每个渠道的健康数据
	for _, ch := range channels {
		snapshot.Channels[ch.ID] = ch

		// 加载存活状态
		if err := loadAliveStatus(ctx, db, ch, epoch); err != nil {
			// 非致命错误，记录日志但继续
			ch.IsAlive = false
		}

		// 加载声明价格
		if err := loadDeclaredPrices(ctx, db, ch, epoch); err != nil {
			ch.DeclaredPrice = make(map[string]*PriceProfile)
		}

		// 加载实测倍率和 TTFT
		if err := loadProbeResults(ctx, db, ch, epoch); err != nil {
			ch.RealRatio = make(map[string]float64)
			ch.TTFTP50 = make(map[string]int)
			ch.TTFTP95 = make(map[string]int)
		}

		// 加载请求历史（滑动窗口：最近 1 小时或最近 1000 次）
		if err := loadRequestHistory(ctx, db, ch); err != nil {
			ch.RecentAttempts = 0
			ch.RecentSuccesses = 0
			ch.ReliabilityScore = 0.5 // 默认值
		}

		// 加载熔断状态
		if err := loadCircuitState(ctx, db, ch); err != nil {
			ch.CircuitState = "closed"
		}
	}

	return snapshot, nil
}

// loadChannels 读取所有渠道配置及其分组归属
func loadChannels(ctx context.Context, db *store.DB) ([]*ChannelHealth, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, name, base_url, enabled, role, user_priority,
		       model_mapping, capabilities, weight
		FROM upstreams
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []*ChannelHealth
	for rows.Next() {
		ch := &ChannelHealth{
			DeclaredPrice: make(map[string]*PriceProfile),
			RealRatio:     make(map[string]float64),
			TTFTP50:       make(map[string]int),
			TTFTP95:       make(map[string]int),
			GroupIDs:      []int{},
		}

		var modelMappingJSON, capabilitiesJSON []byte
		if err := rows.Scan(
			&ch.ID, &ch.Name, &ch.BaseURL, &ch.Enabled, &ch.Role, &ch.UserPriority,
			&modelMappingJSON, &capabilitiesJSON, &ch.Weight,
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

func loadAliveStatus(ctx context.Context, db *store.DB, ch *ChannelHealth, epoch int64) error {
	// 读取最近的存活记录（最近 3 个 epoch）
	err := db.Pool.QueryRow(ctx, `
		SELECT is_alive
		FROM health_checks
		WHERE upstream_id = $1 AND epoch <= $2
		ORDER BY epoch DESC
		LIMIT 1
	`, ch.ID, epoch).Scan(&ch.IsAlive)

	if err != nil {
		return err
	}
	return nil
}

func loadDeclaredPrices(ctx context.Context, db *store.DB, ch *ChannelHealth, epoch int64) error {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (model) model, prompt_ratio, completion_ratio
		FROM declared_prices
		WHERE upstream_id = $1 AND epoch <= $2
		ORDER BY model, epoch DESC
	`, ch.ID, epoch)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var model string
		var promptRatio, completionRatio float64
		if err := rows.Scan(&model, &promptRatio, &completionRatio); err != nil {
			continue
		}

		ch.DeclaredPrice[model] = &PriceProfile{
			InputPrice:  promptRatio * 10.0, // 转换为 $/1M tokens（假设基准 $10/1M）
			OutputPrice: completionRatio * 10.0,
		}
	}

	return rows.Err()
}

func loadProbeResults(ctx context.Context, db *store.DB, ch *ChannelHealth, epoch int64) error {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (model) model, real_ratio, ttft_ms
		FROM probe_results
		WHERE upstream_id = $1 AND epoch <= $2 AND success = true
		ORDER BY model, epoch DESC
	`, ch.ID, epoch)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var model string
		var realRatio float64
		var ttftMS int
		if err := rows.Scan(&model, &realRatio, &ttftMS); err != nil {
			continue
		}

		ch.RealRatio[model] = realRatio
		ch.TTFTP50[model] = ttftMS                     // 简化：单个值作为 P50
		ch.TTFTP95[model] = int(float64(ttftMS) * 1.2) // 简化：P95 约为 P50 的 1.2 倍
	}

	return rows.Err()
}

func loadRequestHistory(ctx context.Context, db *store.DB, ch *ChannelHealth) error {
	// 最近 1 小时或最近 1000 次
	err := db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) as attempts,
			SUM(CASE WHEN success THEN 1 ELSE 0 END) as successes
		FROM (
			SELECT success
			FROM request_history
			WHERE channel_id = $1 AND created_at >= NOW() - INTERVAL '1 hour'
			ORDER BY created_at DESC
			LIMIT 1000
		) recent
	`, ch.ID).Scan(&ch.RecentAttempts, &ch.RecentSuccesses)

	if err != nil {
		return err
	}

	// 贝叶斯平滑：prior_success=5, prior_attempts=10
	ch.ReliabilityScore = float64(ch.RecentSuccesses+5) / float64(ch.RecentAttempts+10)

	return nil
}

func loadCircuitState(ctx context.Context, db *store.DB, ch *ChannelHealth) error {
	// 读取通用熔断状态（capability=''）
	err := db.Pool.QueryRow(ctx, `
		SELECT state, COALESCE(cooling_until, '1970-01-01'::timestamp)
		FROM circuit_states
		WHERE channel_id = $1 AND model = '' AND capability = ''
	`, ch.ID).Scan(&ch.CircuitState, &ch.CoolingUntil)

	if err != nil {
		// 没有记录时默认 closed
		ch.CircuitState = "closed"
		ch.CoolingUntil = time.Time{}
		return nil
	}

	return nil
}

func computeSnapshotHash(snapshot *HealthSnapshot) string {
	// 序列化快照内容（不包括 generated_at 和 content_hash 本身）
	data, _ := json.Marshal(struct {
		Epoch    int64                  `json:"epoch"`
		Channels map[int]*ChannelHealth `json:"channels"`
	}{
		Epoch:    snapshot.Epoch,
		Channels: snapshot.Channels,
	})

	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}
