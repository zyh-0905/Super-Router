package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"smart-router/internal/config"
	"smart-router/internal/router"
	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

type AdminHandler struct {
	db     *store.DB
	redis  *store.RedisClient
	cfg    *config.Config
	logger *zap.Logger
}

type channelUpdateRequest struct {
	Name             *string           `json:"name"`
	BaseURL          *string           `json:"base_url"`
	AccessToken      *string           `json:"access_token"`
	APIKey           *string           `json:"api_key"`
	Enabled          *bool             `json:"enabled"`
	Role             *string           `json:"role"`
	UserPriority     *int              `json:"user_priority"`
	Weight           *int              `json:"weight"`
	ModelMapping     map[string]string `json:"model_mapping"`
	Capabilities     []string          `json:"capabilities"`
	DailyProbeBudget *float64          `json:"daily_probe_budget"`
	RatioLimit       *float64          `json:"ratio_limit"`
	BalanceAPIURL    *string           `json:"balance_api_url"`
	BalanceAPIToken  *string           `json:"balance_api_token"`
	GroupIDs         *[]int            `json:"group_ids"`
}

type keyUpdateRequest struct {
	Enabled  *bool  `json:"enabled"`
	GroupIDs *[]int `json:"group_ids"`
}

var errAPIKeyNotFound = errors.New("api key not found")

func apiKeyUpdateFound(rowsAffected int64) bool {
	return rowsAffected > 0
}

func NewAdminHandler(db *store.DB, redis *store.RedisClient, cfg *config.Config, logger *zap.Logger) *AdminHandler {
	return &AdminHandler{
		db:     db,
		redis:  redis,
		cfg:    cfg,
		logger: logger,
	}
}

// invalidateSnapshot 使路由快照缓存失效（站点/分组/Key 变更后立即生效）
func (h *AdminHandler) invalidateSnapshot(ctx context.Context) {
	router.InvalidateSnapshotCache(ctx, h.redis)
}

// ==================== 站点 (Channels) ====================

// CreateChannel POST /admin/channels - 添加站点
func (h *AdminHandler) CreateChannel(c *gin.Context) {
	var req struct {
		Name             string            `json:"name" binding:"required"`
		BaseURL          string            `json:"base_url" binding:"required"`
		AccessToken      string            `json:"access_token" binding:"required"`
		APIKey           string            `json:"api_key" binding:"required"`
		Role             string            `json:"role"`
		UserPriority     int               `json:"user_priority"`
		Weight           int               `json:"weight"`
		ModelMapping     map[string]string `json:"model_mapping"`
		Capabilities     []string          `json:"capabilities"`
		DailyProbeBudget float64           `json:"daily_probe_budget"`
		RatioLimit       float64           `json:"ratio_limit"`
		BalanceAPIURL    string            `json:"balance_api_url"`
		BalanceAPIToken  string            `json:"balance_api_token"`
		GroupIDs         []int             `json:"group_ids"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 默认值
	if req.Role == "" {
		req.Role = "primary"
	}
	if req.UserPriority == 0 {
		req.UserPriority = 50
	}
	if req.Weight == 0 {
		req.Weight = 1
	}
	if req.DailyProbeBudget == 0 {
		req.DailyProbeBudget = 0.5
	}

	ctx := context.Background()
	modelMappingJSON, _ := json.Marshal(req.ModelMapping)
	capabilitiesJSON, _ := json.Marshal(req.Capabilities)

	var id int
	err := h.db.Pool.QueryRow(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key, enabled, role, user_priority, weight, model_mapping, capabilities, daily_probe_budget, ratio_limit, balance_api_url, balance_api_token)
		VALUES ($1, $2, $3, $4, true, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id
	`, req.Name, req.BaseURL, req.AccessToken, req.APIKey, req.Role, req.UserPriority, req.Weight, modelMappingJSON, capabilitiesJSON, req.DailyProbeBudget, req.RatioLimit, req.BalanceAPIURL, req.BalanceAPIToken).Scan(&id)

	if err != nil {
		h.logger.Error("Failed to create channel", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to create channel"})
		return
	}

	// 同步分组归属（未指定时归入默认分组）
	groupIDs := req.GroupIDs
	if len(groupIDs) == 0 {
		if dgid, err := h.defaultGroupID(ctx); err == nil {
			groupIDs = []int{dgid}
		}
	}
	if err := h.syncChannelGroups(ctx, id, groupIDs); err != nil {
		h.logger.Warn("Failed to sync channel groups", zap.Int("channel_id", id), zap.Error(err))
	}
	h.invalidateSnapshot(ctx)

	c.JSON(201, gin.H{
		"id":      id,
		"message": "channel created successfully",
	})
}

// loadChannelGroupsMap 加载所有站点的分组归属：channelID → [{id,name}]
func (h *AdminHandler) loadChannelGroupsMap(ctx context.Context) (map[int][]map[string]interface{}, error) {
	rows, err := h.db.Pool.Query(ctx, `
		SELECT cgm.channel_id, g.id, g.name
		FROM channel_group_members cgm
		JOIN channel_groups g ON g.id = cgm.group_id
		ORDER BY g.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := map[int][]map[string]interface{}{}
	for rows.Next() {
		var chID, gID int
		var gName string
		if err := rows.Scan(&chID, &gID, &gName); err != nil {
			continue
		}
		m[chID] = append(m[chID], map[string]interface{}{"id": gID, "name": gName})
	}
	return m, rows.Err()
}

// syncChannelGroups 全量同步站点的分组归属
func (h *AdminHandler) syncChannelGroups(ctx context.Context, channelID int, groupIDs []int) error {
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM channel_group_members WHERE channel_id = $1`, channelID); err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO channel_group_members (channel_id, group_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, channelID, gid); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// defaultGroupID 返回「默认分组」的 ID（不存在时创建）
func (h *AdminHandler) defaultGroupID(ctx context.Context) (int, error) {
	var id int
	err := h.db.Pool.QueryRow(ctx, `SELECT id FROM channel_groups WHERE name = '默认分组' LIMIT 1`).Scan(&id)
	if err == nil {
		return id, nil
	}
	err = h.db.Pool.QueryRow(ctx, `
		INSERT INTO channel_groups (name, description)
		VALUES ('默认分组', '系统自动创建的默认分组')
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`).Scan(&id)
	return id, err
}

// ListChannels GET /admin/channels - 列出所有站点
func (h *AdminHandler) ListChannels(c *gin.Context) {
	ctx := context.Background()

	rows, err := h.db.Pool.Query(ctx, `
		SELECT id, name, base_url, enabled, role, user_priority, weight,
		       COALESCE(model_mapping::text, '{}'), COALESCE(capabilities::text, '[]'),
		       daily_probe_budget, ratio_limit, balance_api_url, balance_api_token, created_at
		FROM upstreams
		ORDER BY id
	`)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query channels"})
		return
	}
	defer rows.Close()

	groupsMap, _ := h.loadChannelGroupsMap(ctx)

	channels := []map[string]interface{}{}
	for rows.Next() {
		var id, userPriority, weight int
		var name, baseURL, role string
		var enabled bool
		var dailyProbeBudget, ratioLimit float64
		var modelMappingJSON, capabilitiesJSON string
		var balanceAPIURL, balanceAPIToken string
		var createdAt time.Time

		if err := rows.Scan(&id, &name, &baseURL, &enabled, &role, &userPriority, &weight,
			&modelMappingJSON, &capabilitiesJSON, &dailyProbeBudget, &ratioLimit, &balanceAPIURL, &balanceAPIToken, &createdAt); err != nil {
			h.logger.Warn("Failed to scan channel row", zap.Error(err))
			continue
		}

		var modelMapping map[string]string
		var capabilities []string
		_ = json.Unmarshal([]byte(modelMappingJSON), &modelMapping)
		_ = json.Unmarshal([]byte(capabilitiesJSON), &capabilities)

		groups := groupsMap[id]
		if groups == nil {
			groups = []map[string]interface{}{}
		}

		channels = append(channels, map[string]interface{}{
			"id":                 id,
			"name":               name,
			"base_url":           baseURL,
			"enabled":            enabled,
			"role":               role,
			"user_priority":      userPriority,
			"weight":             weight,
			"model_mapping":      modelMapping,
			"capabilities":       capabilities,
			"daily_probe_budget": dailyProbeBudget,
			"ratio_limit":        ratioLimit,
			"balance_api_url":    balanceAPIURL,
			"groups":             groups,
			"created_at":         createdAt.Format(time.RFC3339),
		})
	}

	c.JSON(200, gin.H{
		"channels": channels,
		"total":    len(channels),
	})
}

// GetChannel GET /admin/channels/:id - 站点详情
func (h *AdminHandler) GetChannel(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	var (
		channelID, userPriority, weight    int
		name, baseURL, role                string
		enabled                            bool
		dailyProbeBudget, ratioLimit       float64
		modelMappingJSON, capabilitiesJSON string
		balanceAPIURL, balanceAPIToken     string
		createdAt                          time.Time
	)

	err := h.db.Pool.QueryRow(ctx, `
		SELECT id, name, base_url, enabled, role, user_priority, weight,
		       COALESCE(model_mapping::text, '{}'), COALESCE(capabilities::text, '[]'),
		       daily_probe_budget, ratio_limit, balance_api_url, balance_api_token, created_at
		FROM upstreams WHERE id = $1
	`, id).Scan(&channelID, &name, &baseURL, &enabled, &role, &userPriority, &weight,
		&modelMappingJSON, &capabilitiesJSON, &dailyProbeBudget, &ratioLimit, &balanceAPIURL, &balanceAPIToken, &createdAt)

	if err != nil {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}

	var modelMapping map[string]string
	var capabilities []string
	_ = json.Unmarshal([]byte(modelMappingJSON), &modelMapping)
	_ = json.Unmarshal([]byte(capabilitiesJSON), &capabilities)

	c.JSON(200, gin.H{
		"id":                 channelID,
		"name":               name,
		"base_url":           baseURL,
		"enabled":            enabled,
		"role":               role,
		"user_priority":      userPriority,
		"weight":             weight,
		"model_mapping":      modelMapping,
		"capabilities":       capabilities,
		"daily_probe_budget": dailyProbeBudget,
		"ratio_limit":        ratioLimit,
		"balance_api_url":    balanceAPIURL,
		"balance_api_token":  balanceAPIToken,
		"created_at":         createdAt.Format(time.RFC3339),
	})
}

// UpdateChannel PATCH /admin/channels/:id - 更新站点配置
func (h *AdminHandler) UpdateChannel(c *gin.Context) {
	id := c.Param("id")

	var req channelUpdateRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()

	// 校验站点存在
	var exists bool
	if err := h.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstreams WHERE id = $1)`, id).Scan(&exists); err != nil || !exists {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}

	// 构建动态更新语句
	updates := []string{}
	args := []interface{}{}

	if req.Name != nil {
		updates = append(updates, "name = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.Name)
	}
	if req.BaseURL != nil {
		updates = append(updates, "base_url = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.BaseURL)
	}
	if req.AccessToken != nil {
		updates = append(updates, "access_token = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.AccessToken)
	}
	if req.APIKey != nil {
		updates = append(updates, "api_key = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.APIKey)
	}
	if req.Enabled != nil {
		updates = append(updates, "enabled = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.Enabled)
	}
	if req.Role != nil {
		updates = append(updates, "role = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.Role)
	}
	if req.UserPriority != nil {
		updates = append(updates, "user_priority = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.UserPriority)
	}
	if req.Weight != nil {
		updates = append(updates, "weight = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.Weight)
	}
	if req.ModelMapping != nil {
		mmJSON, _ := json.Marshal(req.ModelMapping)
		updates = append(updates, "model_mapping = $"+strconv.Itoa(len(args)+1))
		args = append(args, mmJSON)
	}
	if req.Capabilities != nil {
		capJSON, _ := json.Marshal(req.Capabilities)
		updates = append(updates, "capabilities = $"+strconv.Itoa(len(args)+1))
		args = append(args, capJSON)
	}
	if req.DailyProbeBudget != nil {
		updates = append(updates, "daily_probe_budget = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.DailyProbeBudget)
	}
	if req.RatioLimit != nil {
		updates = append(updates, "ratio_limit = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.RatioLimit)
	}
	if req.BalanceAPIURL != nil {
		updates = append(updates, "balance_api_url = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.BalanceAPIURL)
	}
	if req.BalanceAPIToken != nil {
		updates = append(updates, "balance_api_token = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.BalanceAPIToken)
	}

	if len(updates) == 0 && req.GroupIDs == nil {
		c.JSON(400, gin.H{"error": "no fields to update"})
		return
	}

	if len(updates) > 0 {
		args = append(args, id)
		query := "UPDATE upstreams SET " + updates[0]
		for i := 1; i < len(updates); i++ {
			query += ", " + updates[i]
		}
		query += ", updated_at = NOW() WHERE id = $" + strconv.Itoa(len(args))

		if _, err := h.db.Pool.Exec(ctx, query, args...); err != nil {
			h.logger.Error("Failed to update channel", zap.Error(err))
			c.JSON(500, gin.H{"error": "failed to update channel"})
			return
		}
	}

	// 同步分组归属
	if req.GroupIDs != nil {
		if err := h.syncChannelGroups(ctx, mustAtoi(id), *req.GroupIDs); err != nil {
			h.logger.Error("Failed to sync channel groups", zap.Error(err))
			c.JSON(500, gin.H{"error": "failed to update channel groups"})
			return
		}
	}
	h.invalidateSnapshot(ctx)

	c.JSON(200, gin.H{"message": "channel updated successfully"})
}

// mustAtoi 转换字符串 ID（路由参数均为合法数字）
func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// DeleteChannel DELETE /admin/channels/:id - 删除站点
func (h *AdminHandler) DeleteChannel(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	ct, err := h.db.Pool.Exec(ctx, `DELETE FROM upstreams WHERE id = $1`, id)
	if err != nil {
		h.logger.Error("Failed to delete channel", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to delete channel"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	h.invalidateSnapshot(ctx)

	c.JSON(200, gin.H{"message": "channel deleted successfully"})
}

// GetChannelBalance GET /admin/channels/:id/balance - 当前余额与历史
func (h *AdminHandler) GetChannelBalance(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	rows, err := h.db.Pool.Query(ctx, `
		SELECT id, balance, currency, source, error, checked_at
		FROM balance_checks
		WHERE channel_id = $1
		ORDER BY checked_at DESC
		LIMIT 200
	`, id)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query balance checks"})
		return
	}
	defer rows.Close()

	history := []map[string]interface{}{}
	var latest map[string]interface{}
	for rows.Next() {
		var bid int64
		var balance float64
		var currency, source, errMsg string
		var checkedAt time.Time
		if err := rows.Scan(&bid, &balance, &currency, &source, &errMsg, &checkedAt); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"id":         bid,
			"balance":    balance,
			"currency":   currency,
			"source":     source,
			"error":      errMsg,
			"checked_at": checkedAt.Format(time.RFC3339),
		}
		history = append(history, entry)
		if latest == nil && source != "" {
			latest = entry
		}
	}
	if latest == nil && len(history) > 0 {
		latest = history[0]
	}

	c.JSON(200, gin.H{
		"channel_id": id,
		"latest":     latest,
		"history":    history,
		"count":      len(history),
	})
}

// ==================== 系统设置 ====================

// GetSettings GET /admin/settings - 读取系统设置
func (h *AdminHandler) GetSettings(c *gin.Context) {
	ctx := context.Background()

	threshold := 1.0
	var v string
	if err := h.db.Pool.QueryRow(ctx, `
		SELECT value FROM system_settings WHERE key = 'low_balance_threshold'
	`).Scan(&v); err == nil {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = f
		}
	}

	c.JSON(200, gin.H{
		"low_balance_threshold": threshold,
	})
}

// UpdateSettings PATCH /admin/settings - 更新系统设置
func (h *AdminHandler) UpdateSettings(c *gin.Context) {
	var req struct {
		LowBalanceThreshold *float64 `json:"low_balance_threshold"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	if req.LowBalanceThreshold != nil {
		if *req.LowBalanceThreshold < 0 {
			c.JSON(400, gin.H{"error": "threshold must be >= 0"})
			return
		}
		_, err := h.db.Pool.Exec(ctx, `
			INSERT INTO system_settings (key, value, updated_at)
			VALUES ('low_balance_threshold', $1, NOW())
			ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = NOW()
		`, strconv.FormatFloat(*req.LowBalanceThreshold, 'f', 2, 64))
		if err != nil {
			c.JSON(500, gin.H{"error": "failed to update settings"})
			return
		}
	}

	c.JSON(200, gin.H{"message": "settings updated successfully"})
}

// lowBalanceThreshold 读取低余额告警阈值（默认 $1）
func (h *AdminHandler) lowBalanceThreshold(ctx context.Context) float64 {
	var v string
	if err := h.db.Pool.QueryRow(ctx, `
		SELECT value FROM system_settings WHERE key = 'low_balance_threshold'
	`).Scan(&v); err == nil {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 1.0
}

// ==================== 上游模型列表 ====================

// UpstreamModel 上游返回的单个模型
type UpstreamModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Created int64  `json:"created"`
}

// fetchUpstreamModels 调用上游 GET /v1/models 并解析模型列表
// 兼容 OpenAI / OneAPI 等格式：{data:[...]} 或 {models:[...]}
func fetchUpstreamModels(baseURL, apiKey string) ([]UpstreamModel, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is empty")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上游不可达: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 最多读 1MB
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(body))
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		return nil, fmt.Errorf("上游返回 %d: %s", resp.StatusCode, detail)
	}

	var raw struct {
		Data   []UpstreamModel `json:"data"`
		Models []UpstreamModel `json:"models"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("解析上游响应失败: %w", err)
	}

	models := raw.Data
	if len(models) == 0 {
		models = raw.Models
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("上游响应中未找到模型列表")
	}
	return models, nil
}

// GetUpstreamModels GET /admin/channels/:id/models - 拉取已保存站点的上游模型列表
func (h *AdminHandler) GetUpstreamModels(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	var baseURL, accessToken, apiKey string
	err := h.db.Pool.QueryRow(ctx, `
		SELECT base_url, access_token, api_key FROM upstreams WHERE id = $1
	`, id).Scan(&baseURL, &accessToken, &apiKey)
	if err != nil {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}

	// 优先 API Key，其次 Access Token
	key := apiKey
	if key == "" {
		key = accessToken
	}

	models, err := fetchUpstreamModels(baseURL, key)
	if err != nil {
		c.JSON(502, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"channel_id": id,
		"models":     models,
		"count":      len(models),
	})
}

// ProbeUpstreamModels POST /admin/upstream/models - 探测任意上游的模型列表（表单用）
func (h *AdminHandler) ProbeUpstreamModels(c *gin.Context) {
	var req struct {
		BaseURL     string `json:"base_url" binding:"required"`
		APIKey      string `json:"api_key"`
		AccessToken string `json:"access_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	key := req.APIKey
	if key == "" {
		key = req.AccessToken
	}

	models, err := fetchUpstreamModels(req.BaseURL, key)
	if err != nil {
		c.JSON(502, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"models": models,
		"count":  len(models),
	})
}

// ==================== 中转站分组 ====================

// ListGroups GET /admin/groups - 列出所有分组（含站点数与统计）
func (h *AdminHandler) ListGroups(c *gin.Context) {
	ctx := context.Background()

	rows, err := h.db.Pool.Query(ctx, `
		SELECT g.id, g.name, g.description, g.enabled, g.default_strategy, g.group_priority,
		       g.cb_min_samples, g.cb_open_failure_rate, g.cb_open_min_failures,
		       g.cb_initial_cooling_seconds, g.cb_max_cooling_seconds,
		       g.alive_interval_seconds, g.pricing_interval_seconds, g.probe_interval_seconds,
		       g.balance_interval_seconds, g.daily_probe_budget, g.created_at,
		       COUNT(cgm.channel_id) AS channel_count
		FROM channel_groups g
		LEFT JOIN channel_group_members cgm ON cgm.group_id = g.id
		GROUP BY g.id
		ORDER BY g.id
	`)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query groups"})
		return
	}
	defer rows.Close()

	groups := []map[string]interface{}{}
	for rows.Next() {
		var (
			id, priority, channelCount                                   int
			cbMinSamples, cbOpenMinFailures, cbInitCooling, cbMaxCooling int
			aliveSec, pricingSec, probeSec, balanceSec                   int
			name, desc, strategy                                         string
			enabled                                                      bool
			cbOpenFailureRate, dailyBudget                               float64
			createdAt                                                    time.Time
		)
		if err := rows.Scan(&id, &name, &desc, &enabled, &strategy, &priority,
			&cbMinSamples, &cbOpenFailureRate, &cbOpenMinFailures, &cbInitCooling, &cbMaxCooling,
			&aliveSec, &pricingSec, &probeSec, &balanceSec, &dailyBudget, &createdAt, &channelCount); err != nil {
			h.logger.Warn("Failed to scan group row", zap.Error(err))
			continue
		}

		groups = append(groups, map[string]interface{}{
			"id":                         id,
			"name":                       name,
			"description":                desc,
			"enabled":                    enabled,
			"default_strategy":           strategy,
			"group_priority":             priority,
			"cb_min_samples":             cbMinSamples,
			"cb_open_failure_rate":       cbOpenFailureRate,
			"cb_open_min_failures":       cbOpenMinFailures,
			"cb_initial_cooling_seconds": cbInitCooling,
			"cb_max_cooling_seconds":     cbMaxCooling,
			"alive_interval_seconds":     aliveSec,
			"pricing_interval_seconds":   pricingSec,
			"probe_interval_seconds":     probeSec,
			"balance_interval_seconds":   balanceSec,
			"daily_probe_budget":         dailyBudget,
			"channel_count":              channelCount,
			"created_at":                 createdAt.Format(time.RFC3339),
		})
	}

	c.JSON(200, gin.H{"groups": groups, "total": len(groups)})
}

// CreateGroup POST /admin/groups - 创建分组
func (h *AdminHandler) CreateGroup(c *gin.Context) {
	var req struct {
		Name                    string  `json:"name" binding:"required"`
		Description             string  `json:"description"`
		DefaultStrategy         string  `json:"default_strategy"`
		GroupPriority           int     `json:"group_priority"`
		CbMinSamples            int     `json:"cb_min_samples"`
		CbOpenFailureRate       float64 `json:"cb_open_failure_rate"`
		CbOpenMinFailures       int     `json:"cb_open_min_failures"`
		CbInitialCoolingSeconds int     `json:"cb_initial_cooling_seconds"`
		CbMaxCoolingSeconds     int     `json:"cb_max_cooling_seconds"`
		AliveIntervalSeconds    int     `json:"alive_interval_seconds"`
		PricingIntervalSeconds  int     `json:"pricing_interval_seconds"`
		ProbeIntervalSeconds    int     `json:"probe_interval_seconds"`
		BalanceIntervalSeconds  int     `json:"balance_interval_seconds"`
		DailyProbeBudget        float64 `json:"daily_probe_budget"`
		ChannelIDs              []int   `json:"channel_ids"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	var id int
	err := h.db.Pool.QueryRow(ctx, `
		INSERT INTO channel_groups (
			name, description, enabled, default_strategy, group_priority,
			cb_min_samples, cb_open_failure_rate, cb_open_min_failures,
			cb_initial_cooling_seconds, cb_max_cooling_seconds,
			alive_interval_seconds, pricing_interval_seconds, probe_interval_seconds,
			balance_interval_seconds, daily_probe_budget
		) VALUES ($1,$2,true,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id
	`, req.Name, req.Description, req.DefaultStrategy, req.GroupPriority,
		req.CbMinSamples, req.CbOpenFailureRate, req.CbOpenMinFailures,
		req.CbInitialCoolingSeconds, req.CbMaxCoolingSeconds,
		req.AliveIntervalSeconds, req.PricingIntervalSeconds, req.ProbeIntervalSeconds,
		req.BalanceIntervalSeconds, req.DailyProbeBudget).Scan(&id)

	if err != nil {
		h.logger.Error("Failed to create group", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to create group (name may already exist)"})
		return
	}

	// 可选：立即绑定站点
	for _, chID := range req.ChannelIDs {
		_, _ = h.db.Pool.Exec(ctx, `
			INSERT INTO channel_group_members (channel_id, group_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, chID, id)
	}
	h.invalidateSnapshot(ctx)

	c.JSON(201, gin.H{"id": id, "message": "group created successfully"})
}

// UpdateGroup PATCH /admin/groups/:id - 更新分组
func (h *AdminHandler) UpdateGroup(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		Name                    *string  `json:"name"`
		Description             *string  `json:"description"`
		Enabled                 *bool    `json:"enabled"`
		DefaultStrategy         *string  `json:"default_strategy"`
		GroupPriority           *int     `json:"group_priority"`
		CbMinSamples            *int     `json:"cb_min_samples"`
		CbOpenFailureRate       *float64 `json:"cb_open_failure_rate"`
		CbOpenMinFailures       *int     `json:"cb_open_min_failures"`
		CbInitialCoolingSeconds *int     `json:"cb_initial_cooling_seconds"`
		CbMaxCoolingSeconds     *int     `json:"cb_max_cooling_seconds"`
		AliveIntervalSeconds    *int     `json:"alive_interval_seconds"`
		PricingIntervalSeconds  *int     `json:"pricing_interval_seconds"`
		ProbeIntervalSeconds    *int     `json:"probe_interval_seconds"`
		BalanceIntervalSeconds  *int     `json:"balance_interval_seconds"`
		DailyProbeBudget        *float64 `json:"daily_probe_budget"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	updates := []string{}
	args := []interface{}{}

	add := func(col string, val interface{}) {
		updates = append(updates, col+" = $"+strconv.Itoa(len(args)+1))
		args = append(args, val)
	}
	if req.Name != nil {
		add("name", *req.Name)
	}
	if req.Description != nil {
		add("description", *req.Description)
	}
	if req.Enabled != nil {
		add("enabled", *req.Enabled)
	}
	if req.DefaultStrategy != nil {
		add("default_strategy", *req.DefaultStrategy)
	}
	if req.GroupPriority != nil {
		add("group_priority", *req.GroupPriority)
	}
	if req.CbMinSamples != nil {
		add("cb_min_samples", *req.CbMinSamples)
	}
	if req.CbOpenFailureRate != nil {
		add("cb_open_failure_rate", *req.CbOpenFailureRate)
	}
	if req.CbOpenMinFailures != nil {
		add("cb_open_min_failures", *req.CbOpenMinFailures)
	}
	if req.CbInitialCoolingSeconds != nil {
		add("cb_initial_cooling_seconds", *req.CbInitialCoolingSeconds)
	}
	if req.CbMaxCoolingSeconds != nil {
		add("cb_max_cooling_seconds", *req.CbMaxCoolingSeconds)
	}
	if req.AliveIntervalSeconds != nil {
		add("alive_interval_seconds", *req.AliveIntervalSeconds)
	}
	if req.PricingIntervalSeconds != nil {
		add("pricing_interval_seconds", *req.PricingIntervalSeconds)
	}
	if req.ProbeIntervalSeconds != nil {
		add("probe_interval_seconds", *req.ProbeIntervalSeconds)
	}
	if req.BalanceIntervalSeconds != nil {
		add("balance_interval_seconds", *req.BalanceIntervalSeconds)
	}
	if req.DailyProbeBudget != nil {
		add("daily_probe_budget", *req.DailyProbeBudget)
	}

	if len(updates) == 0 {
		c.JSON(400, gin.H{"error": "no fields to update"})
		return
	}

	args = append(args, id)
	query := "UPDATE channel_groups SET " + updates[0]
	for i := 1; i < len(updates); i++ {
		query += ", " + updates[i]
	}
	query += ", updated_at = NOW() WHERE id = $" + strconv.Itoa(len(args))

	if _, err := h.db.Pool.Exec(ctx, query, args...); err != nil {
		h.logger.Error("Failed to update group", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to update group"})
		return
	}
	h.invalidateSnapshot(ctx)

	c.JSON(200, gin.H{"message": "group updated successfully"})
}

// DeleteGroup DELETE /admin/groups/:id - 删除分组（成员关系级联删除）
func (h *AdminHandler) DeleteGroup(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	ct, err := h.db.Pool.Exec(ctx, `DELETE FROM channel_groups WHERE id = $1`, id)
	if err != nil {
		h.logger.Error("Failed to delete group", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to delete group"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "group not found"})
		return
	}
	h.invalidateSnapshot(ctx)

	c.JSON(200, gin.H{"message": "group deleted successfully"})
}

// ==================== 健康数据 ====================

// GetHealth GET /admin/health/:channel_id - 查看站点健康数据
func (h *AdminHandler) GetHealth(c *gin.Context) {
	channelID := c.Param("channel_id")
	ctx := context.Background()

	rows, err := h.db.Pool.Query(ctx, `
		SELECT id, epoch, is_alive, latency_ms, checked_at
		FROM health_checks
		WHERE upstream_id = $1
		ORDER BY checked_at DESC
		LIMIT 20
	`, channelID)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query health checks"})
		return
	}
	defer rows.Close()

	healthChecks := []map[string]interface{}{}
	for rows.Next() {
		var id, epoch int64
		var isAlive bool
		var latencyMS *int
		var checkedAt time.Time

		if err := rows.Scan(&id, &epoch, &isAlive, &latencyMS, &checkedAt); err != nil {
			continue
		}

		healthChecks = append(healthChecks, map[string]interface{}{
			"id":         id,
			"epoch":      epoch,
			"is_alive":   isAlive,
			"latency_ms": latencyMS,
			"checked_at": checkedAt.Format(time.RFC3339),
		})
	}

	// 最近 24h 成功率与平均延迟
	var successRate, avgLatency interface{}
	var reqCount int
	err = h.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(ROUND(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 4), 0),
		       COALESCE(ROUND(AVG(total_duration_ms)), 0)
		FROM request_history
		WHERE channel_id = $1 AND created_at >= NOW() - INTERVAL '24 hours'
	`, channelID).Scan(&reqCount, &successRate, &avgLatency)
	if err != nil {
		h.logger.Warn("Failed to query channel stats", zap.Error(err))
	}

	c.JSON(200, gin.H{
		"channel_id":    channelID,
		"health_checks": healthChecks,
		"stats_24h": gin.H{
			"requests":       reqCount,
			"success_rate":   successRate,
			"avg_latency_ms": avgLatency,
		},
	})
}

// ==================== 决策日志 ====================

// GetDecisions GET /admin/decisions - 查看最近的决策日志（含渠道名与候选明细）
func (h *AdminHandler) GetDecisions(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "20")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	ctx := context.Background()
	gid := parseGroupIDParam(c)

	// 渠道 ID → 名称映射
	nameMap, err := h.channelNameMap(ctx)
	if err != nil {
		h.logger.Warn("Failed to load channel names", zap.Error(err))
		nameMap = map[int]string{}
	}

	// 分组 ID → 名称映射
	groupNameMap, err := h.groupNameMap(ctx)
	if err != nil {
		groupNameMap = map[int]string{}
	}

	rows, err := h.db.Pool.Query(ctx, `
		SELECT request_id, token_id_hash, model, is_stream, policy_version, strategy,
		       epoch, snapshot_checksum, COALESCE(candidate_order::text, '[]'),
		       COALESCE(excluded::text, '[]'), COALESCE(all_scores::text, '{}'),
		       COALESCE(candidate_details::text, '[]'),
		       COALESCE(attempts::text, '[]'), selected_channel, decision_reason, group_id, decided_at
		FROM decision_logs
		WHERE ($1::int IS NULL OR group_id = $1)
		ORDER BY decided_at DESC
		LIMIT $2
	`, gid, limit)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query decisions"})
		return
	}
	defer rows.Close()

	decisions := []map[string]interface{}{}
	for rows.Next() {
		var requestID, tokenHash, model, policyVersion, strategy string
		var isStream bool
		var epoch int64
		var snapshotChecksum *string
		var candidateOrderJSON, excludedJSON, allScoresJSON, candidateDetailsJSON, attemptsJSON string
		var selectedChannel *int
		var decisionReason string
		var groupID *int
		var decidedAt time.Time

		if err := rows.Scan(&requestID, &tokenHash, &model, &isStream, &policyVersion, &strategy,
			&epoch, &snapshotChecksum, &candidateOrderJSON, &excludedJSON, &allScoresJSON,
			&candidateDetailsJSON, &attemptsJSON, &selectedChannel, &decisionReason, &groupID, &decidedAt); err != nil {
			h.logger.Warn("Failed to scan decision row", zap.Error(err))
			continue
		}

		// 解析 JSON 字段
		var candidateIDs []int
		var excluded []map[string]interface{}
		var allScores map[string]float64
		var attempts []map[string]interface{}
		var candidateDetails []map[string]interface{}
		_ = json.Unmarshal([]byte(candidateOrderJSON), &candidateIDs)
		_ = json.Unmarshal([]byte(excludedJSON), &excluded)
		_ = json.Unmarshal([]byte(allScoresJSON), &allScores)
		_ = json.Unmarshal([]byte(attemptsJSON), &attempts)
		_ = json.Unmarshal([]byte(candidateDetailsJSON), &candidateDetails)

		// 六维评分富化渠道名
		detailsEnriched := []map[string]interface{}{}
		for _, cd := range candidateDetails {
			cid := 0
			if v, ok := cd["channel_id"].(float64); ok {
				cid = int(v)
			}
			detailsEnriched = append(detailsEnriched, map[string]interface{}{
				"channel_id": cid,
				"channel":    nameMap[cid],
				"dims":       cd["dims"],
			})
		}

		// 候选排序 → 带渠道名与得分
		var candidates []map[string]interface{}
		for _, cid := range candidateIDs {
			score, ok := allScores[strconv.Itoa(cid)]
			if !ok {
				score = 0
			}
			candidates = append(candidates, map[string]interface{}{
				"id":      cid,
				"channel": nameMap[cid],
				"score":   score,
			})
		}

		// 排除项 → 带渠道名
		var excludedEnriched []map[string]interface{}
		for _, ex := range excluded {
			cid := 0
			if v, ok := ex["channel_id"].(float64); ok {
				cid = int(v)
			}
			excludedEnriched = append(excludedEnriched, map[string]interface{}{
				"channel_id": cid,
				"channel":    nameMap[cid],
				"reason":     ex["reason"],
			})
		}

		selectedName := ""
		if selectedChannel != nil {
			selectedName = nameMap[*selectedChannel]
		}

		groupName := ""
		if groupID != nil {
			groupName = groupNameMap[*groupID]
		}

		decisions = append(decisions, map[string]interface{}{
			"request_id":          requestID,
			"token_id_hash":       tokenHash,
			"model":               model,
			"is_stream":           isStream,
			"policy_version":      policyVersion,
			"strategy":            strategy,
			"epoch":               epoch,
			"snapshot_checksum":   snapshotChecksum,
			"candidate_order":     candidates,
			"candidate_details":   detailsEnriched,
			"excluded":            excludedEnriched,
			"all_scores":          allScores,
			"attempts":            attempts,
			"selected_channel_id": selectedChannel,
			"selected_channel":    selectedName,
			"decision_reason":     decisionReason,
			"group_id":            groupID,
			"group_name":          groupName,
			"decided_at":          decidedAt.Format(time.RFC3339),
		})
	}

	c.JSON(200, gin.H{
		"decisions": decisions,
		"total":     len(decisions),
	})
}

func (h *AdminHandler) channelNameMap(ctx context.Context) (map[int]string, error) {
	rows, err := h.db.Pool.Query(ctx, `SELECT id, name FROM upstreams`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := map[int]string{}
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		m[id] = name
	}
	return m, rows.Err()
}

func (h *AdminHandler) groupNameMap(ctx context.Context) (map[int]string, error) {
	rows, err := h.db.Pool.Query(ctx, `SELECT id, name FROM channel_groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := map[int]string{}
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		m[id] = name
	}
	return m, rows.Err()
}

// ==================== 熔断状态 ====================

// GetCircuitStates GET /admin/circuit - 查看所有熔断状态（支持 ?group_id= 筛选）
func (h *AdminHandler) GetCircuitStates(c *gin.Context) {
	ctx := context.Background()
	gid := parseGroupIDParam(c)

	rows, err := h.db.Pool.Query(ctx, `
		SELECT cs.channel_id, u.name, cs.model, cs.state, cs.failure_count, cs.success_count,
		       cs.cooling_until, cs.updated_at
		FROM circuit_states cs
		JOIN upstreams u ON u.id = cs.channel_id
		WHERE $1::int IS NULL OR EXISTS (
			SELECT 1 FROM channel_group_members cgm
			WHERE cgm.channel_id = cs.channel_id AND cgm.group_id = $1
		)
		ORDER BY cs.updated_at DESC
	`, gid)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query circuit states"})
		return
	}
	defer rows.Close()

	states := []map[string]interface{}{}
	for rows.Next() {
		var channelID, failureCount, successCount int
		var channelName, model, state string
		var coolingUntil *time.Time
		var updatedAt time.Time

		if err := rows.Scan(&channelID, &channelName, &model, &state, &failureCount, &successCount, &coolingUntil, &updatedAt); err != nil {
			h.logger.Warn("Failed to scan circuit row", zap.Error(err))
			continue
		}

		var coolingStr *string
		if coolingUntil != nil {
			s := coolingUntil.Format(time.RFC3339)
			coolingStr = &s
		}

		states = append(states, map[string]interface{}{
			"channel_id":    channelID,
			"channel_name":  channelName,
			"model":         model,
			"state":         state,
			"failure_count": failureCount,
			"success_count": successCount,
			"cooling_until": coolingStr,
			"updated_at":    updatedAt.Format(time.RFC3339),
		})
	}

	c.JSON(200, gin.H{
		"states": states,
		"total":  len(states),
	})
}

// ResetCircuit POST /admin/circuit/:channel_id/reset - 手动重置熔断器
func (h *AdminHandler) ResetCircuit(c *gin.Context) {
	channelID := c.Param("channel_id")
	ctx := context.Background()

	ct, err := h.db.Pool.Exec(ctx, `
		UPDATE circuit_states
		SET state = 'closed', failure_count = 0, success_count = 0,
		    cooling_until = NULL, updated_at = NOW()
		WHERE channel_id = $1
	`, channelID)
	if err != nil {
		h.logger.Error("Failed to reset circuit", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to reset circuit"})
		return
	}
	h.invalidateSnapshot(ctx)

	c.JSON(200, gin.H{
		"message": fmt.Sprintf("circuit reset successfully (%d rows)", ct.RowsAffected()),
	})
}

// ==================== 统计聚合 (仪表板) ====================

// parseGroupIDParam 解析 group_id 查询参数（空或非法返回 nil = 不筛选）
func parseGroupIDParam(c *gin.Context) *int {
	s := c.Query("group_id")
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

// GetStats GET /admin/stats - 24h 聚合统计（支持 ?group_id= 分组筛选）
func (h *AdminHandler) GetStats(c *gin.Context) {
	ctx := context.Background()
	stats := gin.H{}

	gid := parseGroupIDParam(c)
	stats["group_id"] = gid

	// 24h 与前一天 24h 的请求统计
	var reqs24h, fails24h, reqsPrev int
	var sr24h, srPrev, avgLat24h, avgLatPrev float64
	err := h.db.Pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN created_at >= NOW() - INTERVAL '24 hours' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN created_at >= NOW() - INTERVAL '24 hours' AND NOT success THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN created_at >= NOW() - INTERVAL '48 hours' AND created_at < NOW() - INTERVAL '24 hours' THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(CASE WHEN created_at >= NOW() - INTERVAL '24 hours' AND success THEN 1.0 ELSE NULL END), 0),
			COALESCE(AVG(CASE WHEN created_at >= NOW() - INTERVAL '48 hours' AND created_at < NOW() - INTERVAL '24 hours' AND success THEN 1.0 ELSE NULL END), 0),
			COALESCE(AVG(CASE WHEN created_at >= NOW() - INTERVAL '24 hours' THEN total_duration_ms ELSE NULL END), 0),
			COALESCE(AVG(CASE WHEN created_at >= NOW() - INTERVAL '48 hours' AND created_at < NOW() - INTERVAL '24 hours' THEN total_duration_ms ELSE NULL END), 0)
		FROM request_history
		WHERE ($1::int IS NULL OR group_id = $1)
	`, gid).Scan(&reqs24h, &fails24h, &reqsPrev, &sr24h, &srPrev, &avgLat24h, &avgLatPrev)
	if err != nil {
		h.logger.Warn("Failed to query request stats", zap.Error(err))
	}

	stats["totals"] = gin.H{
		"requests_24h":       reqs24h,
		"failures_24h":       fails24h,
		"requests_prev_24h":  reqsPrev,
		"success_rate_24h":   sr24h,
		"success_rate_prev":  srPrev,
		"avg_latency_ms_24h": avgLat24h,
		"avg_latency_prev":   avgLatPrev,
	}

	// 每小时趋势（24 个桶）
	rows, err := h.db.Pool.Query(ctx, `
		SELECT date_trunc('hour', created_at) AS bucket,
		       COUNT(*),
		       SUM(CASE WHEN NOT success THEN 1 ELSE 0 END)
		FROM request_history
		WHERE created_at >= NOW() - INTERVAL '24 hours'
		  AND ($1::int IS NULL OR group_id = $1)
		GROUP BY bucket ORDER BY bucket
	`, gid)
	if err == nil {
		trendMap := map[string]map[string]interface{}{}
		for rows.Next() {
			var bucket time.Time
			var cnt, failed int
			if err := rows.Scan(&bucket, &cnt, &failed); err != nil {
				continue
			}
			trendMap[bucket.Format("2006-01-02 15:04")] = map[string]interface{}{"count": cnt, "failed": failed}
		}
		rows.Close()

		trend := []map[string]interface{}{}
		now := time.Now().Truncate(time.Hour)
		for i := 23; i >= 0; i-- {
			b := now.Add(-time.Duration(i) * time.Hour)
			key := b.Format("2006-01-02 15:04")
			label := b.Format("15:00")
			if v, ok := trendMap[key]; ok {
				trend = append(trend, map[string]interface{}{"hour": label, "count": v["count"], "failed": v["failed"]})
			} else {
				trend = append(trend, map[string]interface{}{"hour": label, "count": 0, "failed": 0})
			}
		}
		stats["trend"] = trend
	} else {
		h.logger.Warn("Failed to query hourly trend", zap.Error(err))
		stats["trend"] = []map[string]interface{}{}
	}

	// 模型分布
	rows, err = h.db.Pool.Query(ctx, `
		SELECT model, COUNT(*)
		FROM request_history
		WHERE created_at >= NOW() - INTERVAL '24 hours'
		  AND ($1::int IS NULL OR group_id = $1)
		GROUP BY model ORDER BY COUNT(*) DESC LIMIT 10
	`, gid)
	if err == nil {
		models := []map[string]interface{}{}
		for rows.Next() {
			var m string
			var cnt int
			if err := rows.Scan(&m, &cnt); err != nil {
				continue
			}
			models = append(models, map[string]interface{}{"name": m, "count": cnt})
		}
		rows.Close()
		stats["models"] = models
	} else {
		stats["models"] = []map[string]interface{}{}
	}

	// 最近请求（实时监控请求流）
	rows, err = h.db.Pool.Query(ctx, `
		SELECT rh.id, rh.created_at, rh.model, rh.success, rh.total_duration_ms, u.name
		FROM request_history rh
		LEFT JOIN upstreams u ON u.id = rh.channel_id
		WHERE rh.created_at >= NOW() - INTERVAL '1 hour'
		  AND ($1::int IS NULL OR rh.group_id = $1)
		ORDER BY rh.id DESC LIMIT 50
	`, gid)
	recentRequests := []map[string]interface{}{}
	if err == nil {
		for rows.Next() {
			var id int64
			var createdAt time.Time
			var model, channelName string
			var ok bool
			var ms int
			if err := rows.Scan(&id, &createdAt, &model, &ok, &ms, &channelName); err != nil {
				continue
			}
			recentRequests = append(recentRequests, map[string]interface{}{
				"id":      id,
				"time":    createdAt.Format("15:04:05"),
				"model":   model,
				"channel": channelName,
				"ok":      ok,
				"ms":      ms,
			})
		}
		rows.Close()
	}
	stats["recent_requests"] = recentRequests

	// 站点健康汇总（可选分组筛选）
	var totalChannels, activeChannels int
	_ = h.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE enabled)
		FROM upstreams u
		WHERE $1::int IS NULL OR EXISTS (
			SELECT 1 FROM channel_group_members cgm
			WHERE cgm.channel_id = u.id AND cgm.group_id = $1
		)
	`, gid).Scan(&totalChannels, &activeChannels)
	stats["total_channels"] = totalChannels
	stats["active_channels"] = activeChannels

	// 每站点 24h 统计 + 熔断状态
	rows, err = h.db.Pool.Query(ctx, `
		SELECT u.id, u.name, u.enabled, u.role,
		       COALESCE(r.cnt, 0),
		       COALESCE(r.sr, 0),
		       COALESCE(r.avg_lat, 0)
		FROM upstreams u
		LEFT JOIN (
			SELECT channel_id,
			       COUNT(*) AS cnt,
			       ROUND(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 4) AS sr,
			       ROUND(AVG(total_duration_ms)) AS avg_lat
			FROM request_history
			WHERE created_at >= NOW() - INTERVAL '24 hours'
			  AND ($1::int IS NULL OR group_id = $1)
			GROUP BY channel_id
		) r ON r.channel_id = u.id
		WHERE $1::int IS NULL OR EXISTS (
			SELECT 1 FROM channel_group_members cgm
			WHERE cgm.channel_id = u.id AND cgm.group_id = $1
		)
		ORDER BY u.id
	`, gid)
	channelStats := []map[string]interface{}{}
	if err == nil {
		for rows.Next() {
			var id int
			var name, role string
			var enabled bool
			var cnt int
			var sr, avgLat float64
			if err := rows.Scan(&id, &name, &enabled, &role, &cnt, &sr, &avgLat); err != nil {
				continue
			}
			channelStats = append(channelStats, map[string]interface{}{
				"id":             id,
				"name":           name,
				"enabled":        enabled,
				"role":           role,
				"requests_24h":   cnt,
				"success_rate":   sr,
				"avg_latency_ms": avgLat,
			})
		}
		rows.Close()
	}
	// 每站点最严重熔断状态
	worstMap := map[int]string{}
	rows, err = h.db.Pool.Query(ctx, `
		SELECT channel_id, state FROM circuit_states
	`)
	if err == nil {
		for rows.Next() {
			var cid int
			var state string
			if err := rows.Scan(&cid, &state); err != nil {
				continue
			}
			if worse(state, worstMap[cid]) {
				worstMap[cid] = state
			}
		}
		rows.Close()
	}
	for _, ch := range channelStats {
		id := ch["id"].(int)
		ch["circuit_state"] = worstMap[id]
	}

	// 每站点最新余额（仅成功记录，失败行 balance=0 会误报低余额）
	balMap := map[int]map[string]interface{}{}
	rows, err = h.db.Pool.Query(ctx, `
		SELECT DISTINCT ON (channel_id) channel_id, balance, currency, source, error, checked_at
		FROM balance_checks
		WHERE source != ''
		ORDER BY channel_id, checked_at DESC
	`)
	if err == nil {
		for rows.Next() {
			var cid int
			var balance float64
			var currency, source, errMsg string
			var checkedAt time.Time
			if err := rows.Scan(&cid, &balance, &currency, &source, &errMsg, &checkedAt); err != nil {
				continue
			}
			balMap[cid] = map[string]interface{}{
				"balance": balance, "currency": currency, "source": source,
				"error": errMsg, "checked_at": checkedAt.Format(time.RFC3339),
			}
		}
		rows.Close()
	}
	threshold := h.lowBalanceThreshold(ctx)
	for _, ch := range channelStats {
		id := ch["id"].(int)
		if b, ok := balMap[id]; ok {
			ch["balance"] = b["balance"]
			ch["balance_currency"] = b["currency"]
			ch["balance_source"] = b["source"]
			ch["balance_error"] = b["error"]
			ch["balance_checked_at"] = b["checked_at"]
		} else {
			ch["balance"] = nil
		}
	}
	stats["channels"] = channelStats

	// 倍率检测摘要（按站点：最新实测倍率/今日实测次数/默认检测模型/超限标记）
	ratioSummary := h.buildRatioSummary(ctx, gid)
	stats["ratio_summary"] = ratioSummary

	// 告警：熔断开启 + 已禁用站点
	alerts := []map[string]interface{}{}

	// 低余额告警
	for _, ch := range channelStats {
		b, ok := ch["balance"].(float64)
		if !ok {
			continue
		}
		name := ch["name"].(string)
		if b <= threshold {
			alerts = append(alerts, map[string]interface{}{
				"id":      fmt.Sprintf("bal_%d", ch["id"].(int)),
				"name":    fmt.Sprintf("余额不足: %s 剩余 $%.2f（阈值 $%.2f）", name, b, threshold),
				"channel": name,
				"sev":     "critical",
				"ago":     formatAgo(time.Now()),
			})
		}
	}

	// 倍率超限告警
	for _, ch := range ratioSummary {
		limit, _ := ch["ratio_limit"].(float64)
		if limit <= 0 {
			continue
		}
		ratios, _ := ch["ratios"].([]map[string]interface{})
		for _, mr := range ratios {
			ratio, _ := mr["real_ratio"].(float64)
			if ratio <= limit {
				continue
			}
			model, _ := mr["model"].(string)
			name, _ := ch["name"].(string)
			cid, _ := ch["id"].(int)
			ago := "—"
			if s, ok := mr["checked_at"].(string); ok {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					ago = formatAgo(t)
				}
			}
			alerts = append(alerts, map[string]interface{}{
				"id":      fmt.Sprintf("ratio_%d_%s", cid, model),
				"name":    fmt.Sprintf("倍率超标: %s %s 实测 %.4fx 超过上限 %.4fx", name, model, ratio, limit),
				"channel": name,
				"model":   model,
				"sev":     "critical",
				"ago":     ago,
			})
		}
	}

	rows, err = h.db.Pool.Query(ctx, `
		SELECT cs.channel_id, u.name, cs.model, cs.state, cs.updated_at
		FROM circuit_states cs
		JOIN upstreams u ON u.id = cs.channel_id
		WHERE cs.state IN ('open', 'degraded')
		  AND ($1::int IS NULL OR EXISTS (
			SELECT 1 FROM channel_group_members cgm
			WHERE cgm.channel_id = cs.channel_id AND cgm.group_id = $1
		  ))
		ORDER BY cs.updated_at DESC LIMIT 20
	`, gid)
	if err == nil {
		for rows.Next() {
			var cid int
			var name, model, state string
			var updatedAt time.Time
			if err := rows.Scan(&cid, &name, &model, &state, &updatedAt); err != nil {
				continue
			}
			sev := "warning"
			if state == "open" {
				sev = "critical"
			}
			alerts = append(alerts, map[string]interface{}{
				"id":      fmt.Sprintf("cb_%d_%s", cid, model),
				"name":    fmt.Sprintf("熔断%s: %s (%s)", map[string]string{"open": "已开启", "degraded": "降级"}[state], name, model),
				"channel": name,
				"model":   model,
				"sev":     sev,
				"ago":     formatAgo(updatedAt),
			})
		}
		rows.Close()
	}
	rows, err = h.db.Pool.Query(ctx, `
		SELECT id, name FROM upstreams WHERE enabled = false
		  AND ($1::int IS NULL OR EXISTS (
			SELECT 1 FROM channel_group_members cgm
			WHERE cgm.channel_id = upstreams.id AND cgm.group_id = $1
		  ))
		ORDER BY id
	`, gid)
	if err == nil {
		for rows.Next() {
			var id int
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				continue
			}
			alerts = append(alerts, map[string]interface{}{
				"id":      fmt.Sprintf("dis_%d", id),
				"name":    fmt.Sprintf("站点已禁用: %s", name),
				"channel": name,
				"sev":     "warning",
				"ago":     "—",
			})
		}
		rows.Close()
	}
	stats["alerts"] = alerts

	// 分组列表（供前端切换器）+ 当前筛选分组名
	groupRows, err := h.db.Pool.Query(ctx, `
		SELECT g.id, g.name, g.enabled, COUNT(cgm.channel_id)
		FROM channel_groups g
		LEFT JOIN channel_group_members cgm ON cgm.group_id = g.id
		GROUP BY g.id ORDER BY g.id
	`)
	groupsList := []map[string]interface{}{}
	if err == nil {
		for groupRows.Next() {
			var gid, cnt int
			var gname string
			var genabled bool
			if err := groupRows.Scan(&gid, &gname, &genabled, &cnt); err != nil {
				continue
			}
			groupsList = append(groupsList, map[string]interface{}{
				"id": gid, "name": gname, "enabled": genabled, "channel_count": cnt,
			})
		}
		groupRows.Close()
	}
	stats["groups"] = groupsList

	if gid != nil {
		for _, g := range groupsList {
			if g["id"] == *gid {
				stats["group_name"] = g["name"]
				break
			}
		}
	}

	// Epoch
	epoch, err := h.db.GetCurrentEpoch(ctx)
	if err != nil {
		epoch = 0
	}
	stats["epoch"] = epoch
	stats["generated_at"] = time.Now().Format(time.RFC3339)

	c.JSON(200, stats)
}

// worse 判断熔断状态严重程度 a > b
func worse(a, b string) bool {
	rank := map[string]int{"": 0, "closed": 0, "half_open": 1, "degraded": 2, "open": 3}
	return rank[a] > rank[b]
}

// buildRatioSummary 构建站点倍率检测摘要（支持分组筛选）：
// 每站点：最新实测倍率（每模型）、今日实测次数、默认检测模型、超限标记
func (h *AdminHandler) buildRatioSummary(ctx context.Context, gid *int) []map[string]interface{} {
	rows, err := h.db.Pool.Query(ctx, `
		SELECT u.id, u.name, u.enabled, COALESCE(u.ratio_limit, 0),
		       COALESCE(u.model_mapping::text, '{}'), COALESCE(p.today_cnt, 0)
		FROM upstreams u
		LEFT JOIN (
			SELECT upstream_id, COUNT(*) AS today_cnt
			FROM probe_results
			WHERE success = true AND checked_at >= CURRENT_DATE
			GROUP BY upstream_id
		) p ON p.upstream_id = u.id
		WHERE $1::int IS NULL OR EXISTS (
			SELECT 1 FROM channel_group_members cgm WHERE cgm.channel_id = u.id AND cgm.group_id = $1
		)
		ORDER BY u.id
	`, gid)
	if err != nil {
		h.logger.Warn("Failed to query ratio summary channels", zap.Error(err))
		return []map[string]interface{}{}
	}
	defer rows.Close()

	type chRow struct {
		id       int
		name     string
		enabled  bool
		limit    float64
		mapping  map[string]string
		todayCnt int
	}
	var channels []chRow
	for rows.Next() {
		var r chRow
		var mmJSON string
		if err := rows.Scan(&r.id, &r.name, &r.enabled, &r.limit, &mmJSON, &r.todayCnt); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(mmJSON), &r.mapping)
		if r.mapping == nil {
			r.mapping = map[string]string{}
		}
		channels = append(channels, r)
	}

	// 每站点每模型最新实测（成功）
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
				"model":      model,
				"real_ratio": ratio,
				"source":     source,
				"checked_at": checkedAt.Format(time.RFC3339),
			})
		}
		rrows.Close()
	}

	// 每站点默认检测模型：取该站点第一个倍率分组的默认模型
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

	summary := []map[string]interface{}{}
	for _, r := range channels {
		ratios := ratioMap[r.id]
		overLimit := false
		if r.limit > 0 {
			for _, mr := range ratios {
				if v, ok := mr["real_ratio"].(float64); ok && v > r.limit {
					overLimit = true
					break
				}
			}
		}
		// 默认检测模型：分组默认 → 最新实测过的模型 → 首个映射键
		probeModel := groupDefault[r.id]
		if probeModel == "" && len(ratios) > 0 {
			if m, ok := ratios[0]["model"].(string); ok {
				probeModel = m
			}
		}
		if probeModel == "" && len(r.mapping) > 0 {
			keys := make([]string, 0, len(r.mapping))
			for k := range r.mapping {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			probeModel = keys[0]
		}
		summary = append(summary, map[string]interface{}{
			"id":                  r.id,
			"name":                r.name,
			"enabled":             r.enabled,
			"ratio_limit":         r.limit,
			"probe_count_today":   r.todayCnt,
			"default_probe_model": probeModel,
			"over_limit":          overLimit,
			"ratios":              ratios,
		})
	}
	return summary
}

func formatAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时前", int(d.Hours()))
	default:
		return fmt.Sprintf("%d天前", int(d.Hours()/24))
	}
}

// ==================== API Keys ====================

// ListKeys GET /admin/keys - 列出所有 API Keys
func (h *AdminHandler) ListKeys(c *gin.Context) {
	ctx := context.Background()

	rows, err := h.db.Pool.Query(ctx, `
		SELECT id, key_prefix, role, enabled, created_at
		FROM api_keys ORDER BY id
	`)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to query api keys"})
		return
	}
	defer rows.Close()

	keys := []map[string]interface{}{}
	for rows.Next() {
		var id int
		var prefix, role string
		var enabled bool
		var createdAt time.Time
		if err := rows.Scan(&id, &prefix, &role, &enabled, &createdAt); err != nil {
			continue
		}
		groups, _ := h.keyGroups(ctx, id)
		keys = append(keys, map[string]interface{}{
			"id":         id,
			"prefix":     prefix,
			"role":       role,
			"enabled":    enabled,
			"groups":     groups,
			"created_at": createdAt.Format(time.RFC3339),
		})
	}

	c.JSON(200, gin.H{"keys": keys, "total": len(keys)})
}

// keyGroups 读取 Key 绑定的分组
func (h *AdminHandler) keyGroups(ctx context.Context, keyID int) ([]map[string]interface{}, error) {
	rows, err := h.db.Pool.Query(ctx, `
		SELECT g.id, g.name FROM api_key_groups ak
		JOIN channel_groups g ON g.id = ak.group_id
		WHERE ak.api_key_id = $1 ORDER BY g.id
	`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := []map[string]interface{}{}
	for rows.Next() {
		var gid int
		var gname string
		if err := rows.Scan(&gid, &gname); err != nil {
			continue
		}
		groups = append(groups, map[string]interface{}{"id": gid, "name": gname})
	}
	return groups, rows.Err()
}

// syncKeyGroups 全量同步 Key 的分组绑定
func (h *AdminHandler) syncKeyGroups(ctx context.Context, keyID int, groupIDs []int) error {
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var existingID int
	if err := tx.QueryRow(ctx, `SELECT id FROM api_keys WHERE id = $1 FOR UPDATE`, keyID).Scan(&existingID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errAPIKeyNotFound
		}
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM api_key_groups WHERE api_key_id = $1`, keyID); err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO api_key_groups (api_key_id, group_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, keyID, gid); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// CreateKey POST /admin/keys - 创建 API Key（明文仅返回一次）
func (h *AdminHandler) CreateKey(c *gin.Context) {
	var req struct {
		Role     string `json:"role"`
		GroupIDs []int  `json:"group_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Role != "admin" && req.Role != "caller" {
		req.Role = "caller"
	}

	// 生成随机 Key
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		c.JSON(500, gin.H{"error": "failed to generate key"})
		return
	}
	apiKey := "sr-" + hex.EncodeToString(buf)

	ctx := context.Background()
	var id int
	err := h.db.Pool.QueryRow(ctx, `
		INSERT INTO api_keys (key_hash, key_prefix, role, enabled)
		VALUES ($1, $2, $3, true)
		RETURNING id
	`, hashAPIKey(apiKey), apiKey[:8], req.Role).Scan(&id)
	if err != nil {
		h.logger.Error("Failed to create api key", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to create api key"})
		return
	}

	// 绑定分组（仅 caller 有效）
	if req.Role == "caller" && len(req.GroupIDs) > 0 {
		if err := h.syncKeyGroups(ctx, id, req.GroupIDs); err != nil {
			h.logger.Warn("Failed to bind key groups", zap.Error(err))
		}
	}
	h.invalidateSnapshot(ctx)

	c.JSON(201, gin.H{
		"id":      id,
		"key":     apiKey,
		"prefix":  apiKey[:8],
		"role":    req.Role,
		"warning": "明文 Key 仅返回这一次，请立即保存",
	})
}

// UpdateKey PATCH /admin/keys/:id - 启用/禁用 Key、更新分组绑定
func (h *AdminHandler) UpdateKey(c *gin.Context) {
	id := c.Param("id")
	var req keyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()

	if req.Enabled != nil {
		ct, err := h.db.Pool.Exec(ctx, `UPDATE api_keys SET enabled = $1 WHERE id = $2`, *req.Enabled, id)
		if err != nil {
			c.JSON(500, gin.H{"error": "failed to update api key"})
			return
		}
		if !apiKeyUpdateFound(ct.RowsAffected()) {
			c.JSON(404, gin.H{"error": "api key not found"})
			return
		}
	}

	if req.GroupIDs != nil {
		if err := h.syncKeyGroups(ctx, mustAtoi(id), *req.GroupIDs); err != nil {
			if errors.Is(err, errAPIKeyNotFound) {
				c.JSON(404, gin.H{"error": "api key not found"})
				return
			}
			c.JSON(500, gin.H{"error": "failed to update key groups"})
			return
		}
	}
	h.invalidateSnapshot(ctx)

	c.JSON(200, gin.H{"message": "api key updated successfully"})
}

// DeleteKey DELETE /admin/keys/:id - 撤销 Key
func (h *AdminHandler) DeleteKey(c *gin.Context) {
	id := c.Param("id")
	ctx := context.Background()

	ct, err := h.db.Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, id)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to delete api key"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "api key not found"})
		return
	}
	h.invalidateSnapshot(ctx)

	c.JSON(200, gin.H{"message": "api key deleted successfully"})
}

// ==================== 运行配置 ====================

// GetConfig GET /admin/config - 当前运行配置（敏感信息已脱敏）
func (h *AdminHandler) GetConfig(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(200, gin.H{})
		return
	}

	c.JSON(200, gin.H{
		"server": gin.H{
			"port": h.cfg.Server.Port,
		},
		"checker": gin.H{
			"alive_interval":     h.cfg.Checker.AliveInterval.Seconds(),
			"pricing_interval":   h.cfg.Checker.PricingInterval.Minutes(),
			"probe_interval":     h.cfg.Checker.ProbeInterval.Hours(),
			"daily_probe_budget": h.cfg.Checker.DailyProbeBudget,
		},
		"routing": gin.H{
			"default_strategy":  h.cfg.Routing.DefaultStrategy,
			"max_attempts":      h.cfg.Routing.MaxAttempts,
			"total_budget_ms":   h.cfg.Routing.TotalBudgetMS,
			"max_price_cap":     h.cfg.Routing.Filter.MaxPriceCap,
			"max_ttft_ms":       h.cfg.Routing.Filter.MaxTTFTMS,
			"open_failure_rate": h.cfg.Routing.CircuitBreaker.OpenFailureRate,
		},
	})
}
