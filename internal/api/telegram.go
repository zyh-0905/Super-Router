package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"smart-router/internal/crypto"
	"smart-router/internal/store"
	"smart-router/internal/telegram"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// TelegramHandler Telegram 配置、订阅者与投递日志管理接口。
// 全部接口要求 admin 角色（路由注册在 adminGroup 内）。
type TelegramHandler struct {
	DB        *store.DB
	CryptoKey string // 应用层加密密钥（enc:v1: 信封）
	Logger    *zap.Logger
}

// NewTelegramHandler 创建 Telegram 管理 Handler。
func NewTelegramHandler(db *store.DB, cryptoKey string, logger *zap.Logger) *TelegramHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TelegramHandler{DB: db, CryptoKey: cryptoKey, Logger: logger}
}

// telegramConfigResponse 配置脱敏响应（绝不包含完整 Bot Token）。
type telegramConfigResponse struct {
	Enabled               bool       `json:"enabled"`
	BotConfigured         bool       `json:"bot_configured"`
	BotTokenSuffix        string     `json:"bot_token_suffix"`
	ReportEnabled         bool       `json:"report_enabled"`
	ReportIntervalMinutes int        `json:"report_interval_minutes"`
	ReportMinute          int        `json:"report_minute"`
	Timezone              string     `json:"timezone"`
	IncludeRecovered      bool       `json:"include_recovered"`
	IncludeOngoing        bool       `json:"include_ongoing"`
	WebBaseURL            string     `json:"web_base_url"`
	LastPollAt            *time.Time `json:"last_poll_at"`
	LastReportAt          *time.Time `json:"last_report_at"`
	LastError             string     `json:"last_error"`
}

// telegramConfigPatch PATCH 请求体（全部可选；bot_token 空 = 不覆盖已有 Token）。
type telegramConfigPatch struct {
	Enabled               *bool   `json:"enabled"`
	BotToken              *string `json:"bot_token"`
	ReportEnabled         *bool   `json:"report_enabled"`
	ReportIntervalMinutes *int    `json:"report_interval_minutes"`
	ReportMinute          *int    `json:"report_minute"`
	Timezone              *string `json:"timezone"`
	IncludeRecovered      *bool   `json:"include_recovered"`
	IncludeOngoing        *bool   `json:"include_ongoing"`
	WebBaseURL            *string `json:"web_base_url"`
}

// validateTelegramConfigPatch 校验 PATCH 字段（直接校验绑定后的结构体）。
// 注意：不能再在 ShouldBindJSON 之后 GetRawData 二次读 body——
// gin 的绑定已消费请求体，raw 恒为空，导致校验被整体绕过（已实证
// report_minute=99 可写入数据库）。
func validateTelegramConfigPatch(req telegramConfigPatch) error {
	if req.ReportIntervalMinutes != nil {
		if *req.ReportIntervalMinutes <= 0 {
			return fmt.Errorf("report_interval_minutes must be positive")
		}
	}
	if req.ReportMinute != nil {
		if *req.ReportMinute < 0 || *req.ReportMinute > 59 {
			return fmt.Errorf("report_minute must be between 0 and 59")
		}
	}
	if req.Timezone != nil && *req.Timezone != "" {
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			return fmt.Errorf("timezone invalid: %s", *req.Timezone)
		}
	}
	return nil
}

// loadConfigRow 读取当前配置行（含密文 Token 供脱敏计算）。
func (h *TelegramHandler) loadConfigRow(c *gin.Context) (configRow, error) {
	var row configRow
	err := h.DB.Pool.QueryRow(c.Request.Context(), `
		SELECT enabled, bot_token, report_enabled, report_interval_minutes, report_minute,
		       timezone, include_recovered, include_ongoing, web_base_url,
		       last_poll_at, last_report_at, COALESCE(last_error, '')
		FROM telegram_config WHERE id = 1
	`).Scan(&row.Enabled, &row.BotToken, &row.ReportEnabled, &row.ReportIntervalMinutes,
		&row.ReportMinute, &row.Timezone, &row.IncludeRecovered, &row.IncludeOngoing,
		&row.WebBaseURL, &row.LastPollAt, &row.LastReportAt, &row.LastError)
	return row, err
}

type configRow struct {
	Enabled               bool
	BotToken              string
	ReportEnabled         bool
	ReportIntervalMinutes int
	ReportMinute          int
	Timezone              string
	IncludeRecovered      bool
	IncludeOngoing        bool
	WebBaseURL            string
	LastPollAt            *time.Time
	LastReportAt          *time.Time
	LastError             string
}

// GetConfig GET /admin/telegram/config - 脱敏配置。
func (h *TelegramHandler) GetConfig(c *gin.Context) {
	row, err := h.loadConfigRow(c)
	if err != nil {
		h.Logger.Error("Failed to load telegram config", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to load telegram config"})
		return
	}
	resp := telegramConfigResponse{
		Enabled:               row.Enabled,
		BotConfigured:         row.BotToken != "",
		BotTokenSuffix:        "",
		ReportEnabled:         row.ReportEnabled,
		ReportIntervalMinutes: row.ReportIntervalMinutes,
		ReportMinute:          row.ReportMinute,
		Timezone:              row.Timezone,
		IncludeRecovered:      row.IncludeRecovered,
		IncludeOngoing:        row.IncludeOngoing,
		WebBaseURL:            row.WebBaseURL,
		LastPollAt:            row.LastPollAt,
		LastReportAt:          row.LastReportAt,
		LastError:             row.LastError,
	}
	if row.BotToken != "" {
		if plain, derr := crypto.Decrypt(row.BotToken, h.CryptoKey); derr == nil && plain != "" {
			// 只回显脱敏尾缀（解密失败也仅标记已配置）
			suffix := plain
			if len(suffix) > 4 {
				suffix = suffix[len(suffix)-4:]
			}
			resp.BotTokenSuffix = suffix
		}
	}
	c.JSON(200, resp)
}

// UpdateConfig PATCH /admin/telegram/config - 更新配置（新 Token 加密入库）。
func (h *TelegramHandler) UpdateConfig(c *gin.Context) {
	var req telegramConfigPatch
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	// 先做纯字段校验（直接校验绑定后的字段，不再二次读 body）
	if err := validateTelegramConfigPatch(req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	updates := []string{}
	args := []interface{}{}
	add := func(col string, val interface{}) {
		updates = append(updates, col+" = $"+strconv.Itoa(len(args)+1))
		args = append(args, val)
	}

	if req.Enabled != nil {
		add("enabled", *req.Enabled)
	}
	if req.BotToken != nil && *req.BotToken != "" {
		// 空 Token 不覆盖已有 Token（PATCH 语义）
		enc, err := crypto.Encrypt(*req.BotToken, h.CryptoKey)
		if err != nil {
			c.JSON(500, gin.H{"error": "failed to encrypt bot token"})
			return
		}
		add("bot_token", enc)
	}
	if req.ReportEnabled != nil {
		add("report_enabled", *req.ReportEnabled)
	}
	if req.ReportIntervalMinutes != nil {
		add("report_interval_minutes", *req.ReportIntervalMinutes)
	}
	if req.ReportMinute != nil {
		add("report_minute", *req.ReportMinute)
	}
	if req.Timezone != nil {
		add("timezone", *req.Timezone)
	}
	if req.IncludeRecovered != nil {
		add("include_recovered", *req.IncludeRecovered)
	}
	if req.IncludeOngoing != nil {
		add("include_ongoing", *req.IncludeOngoing)
	}
	if req.WebBaseURL != nil {
		add("web_base_url", *req.WebBaseURL)
	}

	if len(updates) == 0 {
		c.JSON(400, gin.H{"error": "no fields to update"})
		return
	}

	query := "UPDATE telegram_config SET " + strings.Join(updates, ", ") + ", updated_at = NOW() WHERE id = 1"
	if _, err := h.DB.Pool.Exec(ctx, query, args...); err != nil {
		h.Logger.Error("Failed to update telegram config", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to update telegram config"})
		return
	}
	c.JSON(200, gin.H{"message": "telegram config updated"})
}

// subscriberRequest 订阅者创建/更新请求。
type subscriberRequest struct {
	ChatID       int64  `json:"chat_id"`
	ChatType     string `json:"chat_type"`
	DisplayName  string `json:"display_name"`
	Enabled      *bool  `json:"enabled"`
	AlertEnabled *bool  `json:"alert_enabled"`
	QueryEnabled *bool  `json:"query_enabled"`
	GroupIDs     []int  `json:"group_ids"`
}

// validateSubscriberRequest 订阅者字段校验（chat_id 非零整数；分组存在性由 DB 校验）。
func validateSubscriberRequest(req subscriberRequest) error {
	if req.ChatID <= 0 {
		return fmt.Errorf("chat_id must be a positive integer")
	}
	if req.ChatType != "" && req.ChatType != "private" && req.ChatType != "group" && req.ChatType != "channel" {
		return fmt.Errorf("chat_type must be private, group or channel")
	}
	return nil
}

// ListSubscribers GET /admin/telegram/subscribers。
func (h *TelegramHandler) ListSubscribers(c *gin.Context) {
	rows, err := h.DB.Pool.Query(c.Request.Context(), `
		SELECT id, chat_id, chat_type, display_name, enabled, alert_enabled, query_enabled,
		       COALESCE(group_ids::text, '[]'), last_sent_at, COALESCE(last_error, ''), failure_count,
		       created_at, updated_at
		FROM telegram_subscribers
		ORDER BY id
	`)
	if err != nil {
		h.Logger.Error("Failed to list telegram subscribers", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to list telegram subscribers"})
		return
	}
	defer rows.Close()

	subs := []gin.H{}
	for rows.Next() {
		var (
			id                                             int64
			chatID                                         int64
			chatType, displayName, groupIDsJSON            string
			enabled, alertEnabled, queryEnabled            bool
			lastSentAt                                     *time.Time
			lastError                                      string
			failureCount                                   int
			createdAt, updatedAt                           time.Time
		)
		if err := rows.Scan(&id, &chatID, &chatType, &displayName, &enabled, &alertEnabled, &queryEnabled,
			&groupIDsJSON, &lastSentAt, &lastError, &failureCount, &createdAt, &updatedAt); err != nil {
			continue
		}
		var groupIDs []int
		_ = json.Unmarshal([]byte(groupIDsJSON), &groupIDs)
		if groupIDs == nil {
			groupIDs = []int{}
		}
		subs = append(subs, gin.H{
			"id":            id,
			"chat_id":       chatID,
			"chat_type":     chatType,
			"display_name":  displayName,
			"enabled":       enabled,
			"alert_enabled": alertEnabled,
			"query_enabled": queryEnabled,
			"group_ids":     groupIDs,
			"last_sent_at":  lastSentAt,
			"last_error":    lastError,
			"failure_count": failureCount,
			"created_at":    createdAt.Format(time.RFC3339),
			"updated_at":    updatedAt.Format(time.RFC3339),
		})
	}
	c.JSON(200, gin.H{"subscribers": subs, "total": len(subs)})
}

// CreateSubscriber POST /admin/telegram/subscribers。
func (h *TelegramHandler) CreateSubscriber(c *gin.Context) {
	var req subscriberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := validateSubscriberRequest(req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.ChatType == "" {
		req.ChatType = "private"
	}
	// 校验分组存在且启用（group_ids 允许为空 = 全部）
	if err := h.validateGroupIDs(c, req.GroupIDs); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	groupIDsJSON, _ := json.Marshal(req.GroupIDs)

	ctx := c.Request.Context()
	var id int64
	err := h.DB.Pool.QueryRow(ctx, `
		INSERT INTO telegram_subscribers (chat_id, chat_type, display_name, enabled, alert_enabled, query_enabled, group_ids)
		VALUES ($1, $2, $3, COALESCE($4, true), COALESCE($5, true), COALESCE($6, true), $7)
		RETURNING id
	`, req.ChatID, req.ChatType, req.DisplayName, req.Enabled, req.AlertEnabled, req.QueryEnabled, groupIDsJSON).Scan(&id)
	if err != nil {
		h.Logger.Error("Failed to create telegram subscriber", zap.Error(err))
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(409, gin.H{"error": "chat_id already subscribed"})
			return
		}
		c.JSON(500, gin.H{"error": "failed to create telegram subscriber"})
		return
	}
	c.JSON(201, gin.H{"id": id, "message": "subscriber created"})
}

// UpdateSubscriber PATCH /admin/telegram/subscribers/:id。
func (h *TelegramHandler) UpdateSubscriber(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "invalid subscriber id"})
		return
	}
	var req subscriberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.ChatID != 0 && req.ChatID < 0 {
		c.JSON(400, gin.H{"error": "chat_id must be a positive integer"})
		return
	}
	if req.GroupIDs != nil {
		if err := h.validateGroupIDs(c, req.GroupIDs); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
	}

	ctx := c.Request.Context()
	updates := []string{}
	args := []interface{}{}
	add := func(col string, val interface{}) {
		updates = append(updates, col+" = $"+strconv.Itoa(len(args)+1))
		args = append(args, val)
	}
	if req.ChatID != 0 {
		add("chat_id", req.ChatID)
	}
	if req.ChatType != "" {
		add("chat_type", req.ChatType)
	}
	if req.DisplayName != "" {
		add("display_name", req.DisplayName)
	}
	if req.Enabled != nil {
		add("enabled", *req.Enabled)
	}
	if req.AlertEnabled != nil {
		add("alert_enabled", *req.AlertEnabled)
	}
	if req.QueryEnabled != nil {
		add("query_enabled", *req.QueryEnabled)
	}
	if req.GroupIDs != nil {
		groupIDsJSON, _ := json.Marshal(req.GroupIDs)
		add("group_ids", groupIDsJSON)
	}
	if len(updates) == 0 {
		c.JSON(400, gin.H{"error": "no fields to update"})
		return
	}

	args = append(args, id)
	query := "UPDATE telegram_subscribers SET " + strings.Join(updates, ", ") + ", updated_at = NOW() WHERE id = $" + strconv.Itoa(len(args))
	ct, err := h.DB.Pool.Exec(ctx, query, args...)
	if err != nil {
		h.Logger.Error("Failed to update telegram subscriber", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to update telegram subscriber"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "subscriber not found"})
		return
	}
	c.JSON(200, gin.H{"message": "subscriber updated"})
}

// DeleteSubscriber DELETE /admin/telegram/subscribers/:id。
func (h *TelegramHandler) DeleteSubscriber(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "invalid subscriber id"})
		return
	}
	ct, err := h.DB.Pool.Exec(c.Request.Context(), `DELETE FROM telegram_subscribers WHERE id = $1`, id)
	if err != nil {
		h.Logger.Error("Failed to delete telegram subscriber", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to delete telegram subscriber"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "subscriber not found"})
		return
	}
	c.JSON(200, gin.H{"message": "subscriber deleted"})
}

// GetDeliveryLogs GET /admin/telegram/delivery-logs（最近 100 条）。
func (h *TelegramHandler) GetDeliveryLogs(c *gin.Context) {
	rows, err := h.DB.Pool.Query(c.Request.Context(), `
		SELECT dl.id, dl.subscriber_id, dl.message_kind, dl.window_start, dl.window_end,
		       dl.success, dl.telegram_message_id, COALESCE(dl.error, ''), dl.sent_at
		FROM telegram_delivery_logs dl
		ORDER BY dl.sent_at DESC
		LIMIT 100
	`)
	if err != nil {
		h.Logger.Error("Failed to list delivery logs", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to list delivery logs"})
		return
	}
	defer rows.Close()

	logs := []gin.H{}
	for rows.Next() {
		var (
			id, subscriberID     int64
			kind                 string
			ws, we               *time.Time
			success              bool
			msgID                *int64
			errMsg               string
			sentAt               time.Time
		)
		if err := rows.Scan(&id, &subscriberID, &kind, &ws, &we, &success, &msgID, &errMsg, &sentAt); err != nil {
			continue
		}
		logs = append(logs, gin.H{
			"id":                  id,
			"subscriber_id":       subscriberID,
			"message_kind":        kind,
			"window_start":        ws,
			"window_end":          we,
			"success":             success,
			"telegram_message_id": msgID,
			"error":               errMsg,
			"sent_at":             sentAt.Format(time.RFC3339),
		})
	}
	c.JSON(200, gin.H{"logs": logs, "total": len(logs)})
}

// validateGroupIDs 校验分组全部存在且启用。
func (h *TelegramHandler) validateGroupIDs(c *gin.Context, groupIDs []int) error {
	for _, gid := range groupIDs {
		var enabled bool
		err := h.DB.Pool.QueryRow(c.Request.Context(), `
			SELECT enabled FROM channel_groups WHERE id = $1
		`, gid).Scan(&enabled)
		if err != nil {
			return fmt.Errorf("group %d not found", gid)
		}
		if !enabled {
			return fmt.Errorf("group %d is disabled", gid)
		}
	}
	return nil
}

// loadRawConfig 读取配置行原始值（含密文 Token），供 test/report 端点使用。
func (h *TelegramHandler) loadRawConfig(c *gin.Context) (enabled bool, botToken, webBaseURL string, err error) {
	err = h.DB.Pool.QueryRow(c.Request.Context(), `
		SELECT enabled, bot_token, web_base_url FROM telegram_config WHERE id = 1
	`).Scan(&enabled, &botToken, &webBaseURL)
	return
}

// TestConnection POST /admin/telegram/test - 校验 Bot Token（getMe）。
func (h *TelegramHandler) TestConnection(c *gin.Context) {
	var req struct {
		BotToken *string `json:"bot_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	token := ""
	if req.BotToken != nil && *req.BotToken != "" {
		token = *req.BotToken
	} else {
		_, stored, _, err := h.loadRawConfig(c)
		if err != nil {
			c.JSON(500, gin.H{"error": "failed to load telegram config"})
			return
		}
		plain, derr := crypto.Decrypt(stored, h.CryptoKey)
		if derr != nil {
			c.JSON(500, gin.H{"error": "failed to decrypt bot token"})
			return
		}
		token = plain
	}
	if token == "" {
		c.JSON(400, gin.H{"error": "bot token not configured"})
		return
	}
	client := telegram.NewClient(token)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if err := client.GetMe(ctx); err != nil {
		c.JSON(502, gin.H{"error": "getMe failed", "detail": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "message": "Bot Token 有效"})
}

// SendReport POST /admin/telegram/report - 立即向全部订阅者发送当前告警汇总。
// 只触发一次发送，不改变正常调度。
func (h *TelegramHandler) SendReport(c *gin.Context) {
	_, stored, _, err := h.loadRawConfig(c)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to load telegram config"})
		return
	}
	plain, derr := crypto.Decrypt(stored, h.CryptoKey)
	if derr != nil || plain == "" {
		c.JSON(400, gin.H{"error": "bot token not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	builder := telegram.NewReportBuilder(h.DB)
	cfg := telegram.Config{IncludeRecovered: true, IncludeOngoing: true}
	_ = h.DB.Pool.QueryRow(ctx, `
		SELECT include_recovered, include_ongoing, web_base_url FROM telegram_config WHERE id = 1
	`).Scan(&cfg.IncludeRecovered, &cfg.IncludeOngoing, &cfg.WebBaseURL)
	now := time.Now()
	msg, err := builder.Build(ctx, now, 1, cfg, nil)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to build report"})
		return
	}

	subs, err := telegram.NewSQLConfigStore(h.DB).LoadSubscribers(ctx)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to load subscribers"})
		return
	}
	client := telegram.NewClient(plain)
	sent, failed := 0, 0
	for _, s := range subs {
		if !s.Enabled || !s.AlertEnabled {
			continue
		}
		for _, part := range telegram.SplitMessage(msg, telegram.MaxTelegramMessageLen) {
			if _, err := client.SendMessage(ctx, s.ChatID, part); err != nil {
				failed++
				break
			}
			sent++
		}
	}
	c.JSON(200, gin.H{
		"message": fmt.Sprintf("已发送 %d 条消息，失败 %d", sent, failed),
		"sent":    sent,
		"failed":  failed,
	})
}

// SendSubscriberTest POST /admin/telegram/subscribers/:id/test - 向单个订阅者发送测试消息。
func (h *TelegramHandler) SendSubscriberTest(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "invalid subscriber id"})
		return
	}
	_, stored, _, err := h.loadRawConfig(c)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to load telegram config"})
		return
	}
	plain, derr := crypto.Decrypt(stored, h.CryptoKey)
	if derr != nil || plain == "" {
		c.JSON(400, gin.H{"error": "bot token not configured"})
		return
	}
	var chatID int64
	if err := h.DB.Pool.QueryRow(c.Request.Context(), `
		SELECT chat_id FROM telegram_subscribers WHERE id = $1
	`, id).Scan(&chatID); err != nil {
		c.JSON(404, gin.H{"error": "subscriber not found"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	client := telegram.NewClient(plain)
	if _, err := client.SendMessage(ctx, chatID, "✅ Smart Router 测试消息：Telegram 通知链路正常。"); err != nil {
		c.JSON(502, gin.H{"error": "sendMessage failed", "detail": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "message": "测试消息已发送"})
}
