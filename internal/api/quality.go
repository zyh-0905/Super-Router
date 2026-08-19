package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"smart-router/internal/quality"
	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// QualityHandler 质量任务管理 API（创建/查询/取消/SSE）。
// 所有接口要求 admin；SSE 同样经过 Bearer admin 认证。
type QualityHandler struct {
	Repo      quality.Repository
	Publisher quality.Publisher
	DB        *store.DB
	Logger    *zap.Logger
}

// NewQualityHandler 创建质量任务 Handler。
func NewQualityHandler(repo quality.Repository, pub quality.Publisher, db *store.DB, logger *zap.Logger) *QualityHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &QualityHandler{Repo: repo, Publisher: pub, DB: db, Logger: logger}
}

// capHistoryLimit 历史查询 limit 上限（默认 5，最大 100）。
func capHistoryLimit(limit int) int {
	if limit <= 0 {
		return 5
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// CreateQualityCheck POST /admin/channels/:id/quality-checks
// body: {"model": "claude-sonnet-5", "depth": "full"}。
// 同站点活跃任务返回 409 与 existing_run_id。
func (h *QualityHandler) CreateQualityCheck(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}
	var req struct {
		Model string `json:"model"`
		Depth string `json:"depth"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Model == "" {
		c.JSON(400, gin.H{"error": "model is required"})
		return
	}
	if req.Depth == "" {
		req.Depth = "full"
	}
	if req.Depth != "basic" && req.Depth != "full" {
		c.JSON(400, gin.H{"error": "depth must be 'basic' or 'full'"})
		return
	}

	// 站点存在性与模型映射校验
	if h.DB != nil {
		var name string
		var mappingJSON string
		err := h.DB.Pool.QueryRow(c.Request.Context(), `
			SELECT name, COALESCE(model_mapping::text, '{}') FROM upstreams WHERE id = $1
		`, channelID).Scan(&name, &mappingJSON)
		if err != nil {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		var mapping map[string]string
		_ = json.Unmarshal([]byte(mappingJSON), &mapping)
		if mapping == nil {
			mapping = map[string]string{}
		}
		if _, ok := mapping[req.Model]; !ok {
			c.JSON(400, gin.H{"error": "model not in channel model_mapping"})
			return
		}
	}

	// 创建任务（活跃任务唯一索引冲突 → 409）
	keyHash, _ := c.Get("key_hash")
	requesterHash, _ := keyHash.(string)
	run, err := h.Repo.Create(c.Request.Context(), channelID, req.Model, req.Depth, requesterHash)
	if err != nil {
		var busy *quality.ErrChannelBusy
		if errors.As(err, &busy) {
			c.JSON(409, gin.H{
				"error":           "channel already has an active quality check",
				"existing_run_id": quality.PublicRunID(busy.ExistingRunID),
			})
			return
		}
		h.Logger.Error("Failed to create quality check", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to create quality check"})
		return
	}

	c.JSON(201, gin.H{
		"run_id":     quality.PublicRunID(run.ID),
		"channel_id": run.ChannelID,
		"model":      run.Model,
		"depth":      run.Depth,
		"status":     string(run.Status),
	})
}

// ListQualityChecks GET /admin/channels/:id/quality-checks?limit=5
func (h *QualityHandler) ListQualityChecks(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}
	limit := capHistoryLimit(parseLimit(c))
	runs, err := h.Repo.ListByChannel(c.Request.Context(), channelID, limit)
	if err != nil {
		h.Logger.Error("Failed to list quality checks", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to list quality checks"})
		return
	}
	out := make([]gin.H, 0, len(runs))
	for i := range runs {
		out = append(out, h.runSummary(&runs[i], nil))
	}
	c.JSON(200, gin.H{"runs": out, "total": len(out)})
}

// GetQualityCheck GET /admin/quality-checks/:run_id
func (h *QualityHandler) GetQualityCheck(c *gin.Context) {
	runID, err := quality.ParseRunID(c.Param("run_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	run, results, err := h.Repo.Get(c.Request.Context(), runID)
	if err != nil {
		c.JSON(404, gin.H{"error": "run not found"})
		return
	}
	c.JSON(200, h.runSummary(run, results))
}

// CancelQualityCheck POST /admin/quality-checks/:run_id/cancel
func (h *QualityHandler) CancelQualityCheck(c *gin.Context) {
	runID, err := quality.ParseRunID(c.Param("run_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := h.Repo.RequestCancel(c.Request.Context(), runID); err != nil {
		// 终态任务不可取消 → 409
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "cancel requested"})
}

// EventsQualityCheck GET /admin/quality-checks/:run_id/events - SSE 实时进度。
// 先发送当前 DB snapshot，再每秒 poll DB；Redis 可用时订阅加速（本实现以 poll 为主，
// Publisher 事件在后续版本订阅；DB poll 保证 Redis 断开也能得到最终状态）。
func (h *QualityHandler) EventsQualityCheck(c *gin.Context) {
	runID, err := quality.ParseRunID(c.Param("run_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// SSE 响应头
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	ctx := c.Request.Context()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	send := func(typ string, data interface{}) bool {
		payload, _ := json.Marshal(data)
		_, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", typ, payload)
		if err != nil {
			return false
		}
		c.Writer.Flush()
		return true
	}

	// 先发送当前快照
	run, results, err := h.Repo.Get(ctx, runID)
	if err != nil {
		send("task_failed", gin.H{"error": "run not found"})
		return
	}
	if !send("task_started", h.runSummary(run, results)) {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, results, err = h.Repo.Get(ctx, runID)
			if err != nil {
				return
			}
			if quality.IsTerminal(run.Status) {
				// 终态：发送最终事件后结束
				eventType := "task_completed"
				if run.Status == quality.RunFailed {
					eventType = "task_failed"
				} else if run.Status == quality.RunCancelled {
					eventType = "task_cancelled"
				} else if run.Status == quality.RunExpired {
					eventType = "task_failed"
				}
				send(eventType, h.runSummary(run, results))
				return
			}
			// 非终态：发送进度事件（每秒一次，避免刷屏）
			send("task_progress", h.runSummary(run, results))
		}
	}
}

// runSummary 任务详情响应（run 摘要 + 各阶段结果 + 时间与总体结论）。
func (h *QualityHandler) runSummary(run *quality.Run, results []quality.StageResult) gin.H {
	summary := gin.H{
		"run_id":            quality.PublicRunID(run.ID),
		"channel_id":        run.ChannelID,
		"model":             run.Model,
		"depth":             run.Depth,
		"status":            string(run.Status),
		"overall_status":    string(run.OverallStatus),
		"current_stage":     run.CurrentStage,
		"progress":          run.Progress,
		"attempt_count":     run.AttemptCount,
		"error":             run.Error,
		"created_at":        formatTime(run.CreatedAt),
		"started_at":        formatTimePtr(run.StartedAt),
		"finished_at":       formatTimePtr(run.FinishedAt),
		"stages":            []gin.H{},
	}
	if results != nil {
		stages := make([]gin.H, 0, len(results))
		for _, r := range results {
			stages = append(stages, gin.H{
				"stage":            r.Stage,
				"check_name":       r.CheckName,
				"status":           string(r.Status),
				"http_status":      r.HTTPStatus,
				"latency_ms":       r.LatencyMS,
				"ttfb_ms":          r.TTFBMS,
				"actual_model":     r.ActualModel,
				"prompt_tokens":    r.PromptTokens,
				"completion_tokens": r.CompletionTokens,
				"total_tokens":     r.TotalTokens,
				"details":          r.Details,
				"error":            r.Error,
			})
		}
		summary["stages"] = stages
	}
	return summary
}

func parseLimit(c *gin.Context) int {
	n, err := strconv.Atoi(c.DefaultQuery("limit", "5"))
	if err != nil {
		return 5
	}
	return n
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}
