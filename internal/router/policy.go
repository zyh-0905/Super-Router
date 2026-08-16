package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"smart-router/internal/store"
)

// Policy 路由策略配置
type Policy struct {
	Version  string                 `json:"version"`
	Strategy string                 `json:"strategy"` // custom_priority, price_first, latency_first, reliability_first, balanced
	Config   map[string]interface{} `json:"config"`
}

// ChannelGroup 分组信息（路由决策相关字段）
type ChannelGroup struct {
	ID              int
	Name            string
	Enabled         bool
	DefaultStrategy string
	GroupPriority   int
	// StrategyConfig 分组级策略配置（018 迁移，如 balanced_weights）；空时使用策略内置默认
	StrategyConfig map[string]interface{}
}

// PolicyLoader 策略加载器
type PolicyLoader struct {
	db       *store.DB
	defaults *PolicyDefaults
}

// PolicyDefaults 系统级策略默认值（来自配置文件，DB 无策略记录时的兜底）
type PolicyDefaults struct {
	DefaultStrategy    string
	MaxAttempts        int
	TotalBudgetMS      int
	MaxPriceCap        float64
	MaxTTFTMS          int
	HalfOpenProbeCount int
	BalancedWeights    map[string]float64 // 键：cost / reliability / latency / load
}

func NewPolicyLoader(db *store.DB) *PolicyLoader {
	return &PolicyLoader{db: db}
}

// SetDefaults 注入系统级默认值（未设置则回退硬编码兜底）
func (p *PolicyLoader) SetDefaults(d PolicyDefaults) {
	p.defaults = &d
}

// LoadGroup 按 ID 加载分组（路由决策用，含分组级策略配置）
func (p *PolicyLoader) LoadGroup(ctx context.Context, groupID int) (*ChannelGroup, error) {
	var g ChannelGroup
	var cfgJSON []byte
	err := p.db.Pool.QueryRow(ctx, `
		SELECT id, name, enabled, default_strategy, group_priority,
		       COALESCE(strategy_config::text, '{}')
		FROM channel_groups WHERE id = $1
	`, groupID).Scan(&g.ID, &g.Name, &g.Enabled, &g.DefaultStrategy, &g.GroupPriority, &cfgJSON)
	if err != nil {
		return nil, err
	}
	g.StrategyConfig = map[string]interface{}{}
	if len(cfgJSON) > 0 {
		_ = json.Unmarshal(cfgJSON, &g.StrategyConfig)
	}
	return &g, nil
}

// LoadPolicy 按优先级查找策略：
// Token×Model → Token默认 → 分组默认 → 系统默认 → 硬编码兜底
func (p *PolicyLoader) LoadPolicy(ctx context.Context, tokenID, model string, group *ChannelGroup) (*Policy, error) {
	// 1. 尝试 Token × Model 专属策略
	policy, err := p.loadFromDB(ctx, &tokenID, &model)
	if err == nil {
		return policy, nil
	}

	// 2. 尝试 Token 默认策略（model = NULL）
	policy, err = p.loadFromDB(ctx, &tokenID, nil)
	if err == nil {
		return policy, nil
	}

	// 3. 尝试分组默认策略（含分组级策略配置：balanced 权重等）
	if group != nil && group.DefaultStrategy != "" {
		cfg := group.StrategyConfig
		if cfg == nil {
			cfg = map[string]interface{}{}
		}
		return &Policy{
			Version:  fmt.Sprintf("group-%d", group.ID),
			Strategy: group.DefaultStrategy,
			Config:   cfg,
		}, nil
	}

	// 4. 尝试系统默认策略（token_id = NULL, model = NULL）
	policy, err = p.loadFromDB(ctx, nil, nil)
	if err == nil {
		return policy, nil
	}

	// 5. 返回兜底默认策略（优先使用配置注入的系统默认值）
	strategy, maxAttempts, totalBudgetMS, maxPriceCap, maxTTFTMS, halfOpenProbeCount, weights := resolvePolicyDefaults(p.defaults)
	weightsIface := make(map[string]interface{}, len(weights))
	for k, v := range weights {
		weightsIface[k] = v
	}
	return &Policy{
		Version:  "default",
		Strategy: strategy,
		Config: map[string]interface{}{
			"max_attempts":          maxAttempts,
			"total_budget_ms":       totalBudgetMS,
			"max_price_cap":         maxPriceCap,
			"max_ttft_ms":           maxTTFTMS,
			"half_open_probe_count": halfOpenProbeCount,
			"balanced_weights":      weightsIface,
		},
	}, nil
}

// resolvePolicyDefaults 合并配置默认值与硬编码兜底
func resolvePolicyDefaults(d *PolicyDefaults) (strategy string, maxAttempts, totalBudgetMS int, maxPriceCap float64, maxTTFTMS int, halfOpenProbeCount int, weights map[string]float64) {
	strategy, maxAttempts, totalBudgetMS = "custom_priority", 3, 30000
	maxPriceCap, maxTTFTMS, halfOpenProbeCount = 100.0, 5000, 1
	if d != nil {
		if d.DefaultStrategy != "" {
			strategy = d.DefaultStrategy
		}
		if d.MaxAttempts > 0 {
			maxAttempts = d.MaxAttempts
		}
		if d.TotalBudgetMS > 0 {
			totalBudgetMS = d.TotalBudgetMS
		}
		if d.MaxPriceCap > 0 {
			maxPriceCap = d.MaxPriceCap
		}
		if d.MaxTTFTMS > 0 {
			maxTTFTMS = d.MaxTTFTMS
		}
		if d.HalfOpenProbeCount > 0 {
			halfOpenProbeCount = d.HalfOpenProbeCount
		}
		if len(d.BalancedWeights) > 0 {
			weights = d.BalancedWeights
		}
	}
	if weights == nil {
		weights = map[string]float64{"cost": 0.35, "reliability": 0.30, "latency": 0.25, "load": 0.10}
	}
	return strategy, maxAttempts, totalBudgetMS, maxPriceCap, maxTTFTMS, halfOpenProbeCount, weights
}

func (p *PolicyLoader) loadFromDB(ctx context.Context, tokenID, model *string) (*Policy, error) {
	var policyVersion, strategy string
	var configJSON []byte

	query := `
		SELECT policy_version, strategy, config
		FROM routing_policies
		WHERE ($1::text IS NULL AND token_id IS NULL OR token_id = $1)
		  AND ($2::text IS NULL AND model IS NULL OR model = $2)
		LIMIT 1
	`

	err := p.db.Pool.QueryRow(ctx, query, tokenID, model).Scan(
		&policyVersion, &strategy, &configJSON,
	)
	if err != nil {
		return nil, err
	}

	var config map[string]interface{}
	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return &Policy{
		Version:  policyVersion,
		Strategy: strategy,
		Config:   config,
	}, nil
}

// GetConfigInt 从配置中读取整数，带默认值
func (p *Policy) GetConfigInt(key string, defaultValue int) int {
	if val, ok := p.Config[key]; ok {
		switch v := val.(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
	}
	return defaultValue
}

// GetConfigFloat 从配置中读取浮点数，带默认值
func (p *Policy) GetConfigFloat(key string, defaultValue float64) float64 {
	if val, ok := p.Config[key]; ok {
		switch v := val.(type) {
		case float64:
			return v
		case int:
			return float64(v)
		}
	}
	return defaultValue
}

// GetConfigString 从配置中读取字符串，带默认值
func (p *Policy) GetConfigString(key string, defaultValue string) string {
	if val, ok := p.Config[key]; ok {
		if v, ok := val.(string); ok {
			return v
		}
	}
	return defaultValue
}

// GetConfigBool 从配置中读取布尔值，带默认值
func (p *Policy) GetConfigBool(key string, defaultValue bool) bool {
	if val, ok := p.Config[key]; ok {
		if v, ok := val.(bool); ok {
			return v
		}
	}
	return defaultValue
}

// GetConfigNestedFloat 按点路径读取配置浮点数，带默认值。
// 优先匹配扁平的字面键（如 DB 中直接存 "balanced_weights.cost"），
// 其次按 "." 逐层下钻嵌套 map（如 {"balanced_weights": {"cost": 0.35}}）。
func (p *Policy) GetConfigNestedFloat(key string, defaultValue float64) float64 {
	// 1. 扁平字面键
	if val, ok := p.Config[key]; ok {
		if f, ok := asFloat64(val); ok {
			return f
		}
	}

	// 2. 嵌套路径下钻
	parts := strings.Split(key, ".")
	var cur interface{} = p.Config
	for i, part := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return defaultValue
		}
		v, ok := m[part]
		if !ok {
			return defaultValue
		}
		if i == len(parts)-1 {
			if f, ok := asFloat64(v); ok {
				return f
			}
			return defaultValue
		}
		cur = v
	}
	return defaultValue
}

func asFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}
