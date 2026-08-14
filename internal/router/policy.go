package router

import (
	"context"
	"encoding/json"
	"fmt"

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
}

// PolicyLoader 策略加载器
type PolicyLoader struct {
	db *store.DB
}

func NewPolicyLoader(db *store.DB) *PolicyLoader {
	return &PolicyLoader{db: db}
}

// LoadGroup 按 ID 加载分组（路由决策用）
func (p *PolicyLoader) LoadGroup(ctx context.Context, groupID int) (*ChannelGroup, error) {
	var g ChannelGroup
	err := p.db.Pool.QueryRow(ctx, `
		SELECT id, name, enabled, default_strategy, group_priority
		FROM channel_groups WHERE id = $1
	`, groupID).Scan(&g.ID, &g.Name, &g.Enabled, &g.DefaultStrategy, &g.GroupPriority)
	if err != nil {
		return nil, err
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

	// 3. 尝试分组默认策略
	if group != nil && group.DefaultStrategy != "" {
		return &Policy{
			Version:  fmt.Sprintf("group-%d", group.ID),
			Strategy: group.DefaultStrategy,
			Config:   map[string]interface{}{},
		}, nil
	}

	// 4. 尝试系统默认策略（token_id = NULL, model = NULL）
	policy, err = p.loadFromDB(ctx, nil, nil)
	if err == nil {
		return policy, nil
	}

	// 5. 返回硬编码的最小默认策略
	return &Policy{
		Version:  "default",
		Strategy: "custom_priority",
		Config: map[string]interface{}{
			"max_attempts":    3,
			"total_budget_ms": 15000,
			"max_price_cap":   100.0,
			"max_ttft_ms":     5000,
		},
	}, nil
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
