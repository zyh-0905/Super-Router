package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"smart-router/internal/metrics"
	"smart-router/internal/router"
	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ProxyHandler struct {
	router  *router.Router
	db      *store.DB
	logger  *zap.Logger
	client  *http.Client
	circuit *CircuitBreakerManager
}

func NewProxyHandler(r *router.Router, db *store.DB, logger *zap.Logger, circuit *CircuitBreakerManager) *ProxyHandler {
	return &ProxyHandler{
		router:  r,
		db:      db,
		logger:  logger,
		client:  &http.Client{Timeout: 60 * time.Second},
		circuit: circuit,
	}
}

// ChatCompletionRequest OpenAI 格式请求（group 为网关扩展字段，转发上游前移除）
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Tools       []interface{} `json:"tools,omitempty"`
	Group       string        `json:"group,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HandleChatCompletion 处理 /v1/chat/completions 请求
func (h *ProxyHandler) HandleChatCompletion(c *gin.Context) {
	ctx := c.Request.Context()
	requestID := generateRequestID()

	// 解析请求
	var req ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid request body"})
		return
	}

	// 提取认证信息
	tokenID, _ := c.Get("api_key")
	tokenIDStr := fmt.Sprintf("%v", tokenID)
	role, _ := c.Get("role")
	roleStr := fmt.Sprintf("%v", role)

	// 分组解析：body 字段优先，其次 X-Group 头
	groupSpec := req.Group
	if groupSpec == "" {
		groupSpec = c.GetHeader("X-Group")
	}

	var groupID *int
	if groupSpec != "" {
		gid, err := h.resolveGroupID(ctx, groupSpec)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		groupID = &gid
	}

	// 分组权限校验（admin 不受限；caller 绑定分组时受限于绑定集合）
	keyGroups, _ := c.Get("key_groups")
	var boundGroups []int
	if kg, ok := keyGroups.([]int); ok {
		boundGroups = kg
	}

	groupIDsForRoute := []int{}
	if roleStr != "admin" && len(boundGroups) > 0 {
		if groupID != nil {
			allowed := false
			for _, gid := range boundGroups {
				if gid == *groupID {
					allowed = true
					break
				}
			}
			if !allowed {
				c.JSON(403, gin.H{"error": "this api key is not allowed to use the specified group"})
				return
			}
			groupIDsForRoute = []int{*groupID}
		} else {
			// 未指定分组：自动限定在绑定分组的并集内
			groupIDsForRoute = boundGroups
		}
	} else if groupID != nil {
		groupIDsForRoute = []int{*groupID}
	}

	// 提取能力需求
	capabilities := extractCapabilities(&req)

	// 构建路由请求
	routeReq := router.RouteRequest{
		RequestID:      requestID,
		TokenID:        tokenIDStr,
		Model:          req.Model,
		IsStream:       req.Stream,
		Capabilities:   capabilities,
		EstimatedInput: estimateInputTokens(&req),
		MaxOutput:      req.MaxTokens,
		TimeoutMS:      30000,
		GroupID:        groupID,
		GroupIDs:       groupIDsForRoute,
	}

	// 调用路由决策
	routeResult, err := h.router.Route(ctx, routeReq)
	if err != nil {
		h.logger.Error("Route failed", zap.Error(err))
		c.JSON(503, gin.H{
			"error": "no available upstream",
			"details": gin.H{
				"excluded": routeResult.Excluded,
			},
		})
		return
	}

	// 记录决策日志
	if err := h.router.LogDecision(ctx, routeReq, routeResult); err != nil {
		h.logger.Warn("Failed to log decision", zap.Error(err))
	}

	h.logger.Info("Route decision",
		zap.String("request_id", requestID),
		zap.String("model", req.Model),
		zap.String("selected_channel", routeResult.SelectedChannel.Name),
		zap.Int("candidates", len(routeResult.CandidateOrder)),
	)

	// 故障切换循环
	attempts := []router.AttemptRecord{}
	startTime := time.Now()
	maxAttempts := 3
	totalBudgetMS := 15000

	for i, channelID := range routeResult.CandidateOrder {
		if i >= maxAttempts {
			h.logger.Info("Max attempts reached", zap.Int("attempts", i))
			break
		}

		elapsed := time.Since(startTime).Milliseconds()
		if int(elapsed) >= totalBudgetMS {
			h.logger.Info("Total budget exceeded", zap.Int64("elapsed_ms", elapsed))
			break
		}

		// 找到对应的渠道（从原始快照中查找）
		channel := routeResult.SelectedChannel
		if i > 0 {
			// 如果不是第一个候选，需要从快照中查找（简化实现：暂时复用 SelectedChannel）
			// 实际应该保存完整的候选列表
			h.logger.Warn("Fallback to next candidate not fully implemented", zap.Int("channel_id", channelID))
		}

		// 尝试调用
		attemptStart := time.Now()
		result, firstByte, err := h.callUpstream(ctx, channel, &req)

		attempt := router.AttemptRecord{
			ChannelID:       channel.ID,
			StartedAt:       attemptStart,
			DurationMS:      int(time.Since(attemptStart).Milliseconds()),
			FirstByteCommit: firstByte,
		}

		if err != nil {
			attempt.ErrorClass = classifyError(err)
			attempts = append(attempts, attempt)

			// 记录失败的代理请求
			metrics.RecordProxyRequest(
				fmt.Sprintf("%d", channel.ID),
				req.Model,
				"error",
				time.Since(attemptStart).Seconds(),
			)

			// 更新熔断状态
			if h.circuit != nil {
				_ = h.circuit.UpdateCircuitState(ctx, channel.ID, req.Model, groupID, false, attempt.ErrorClass)
			}

			h.logger.Warn("Upstream call failed",
				zap.String("channel", channel.Name),
				zap.Error(err),
				zap.Bool("first_byte_committed", firstByte),
			)

			// 记录故障切换（如果不是最后一个候选）
			if i > 0 {
				prevChannelID := routeResult.CandidateOrder[i-1]
				metrics.RecordFailover(
					fmt.Sprintf("%d", prevChannelID),
					fmt.Sprintf("%d", channel.ID),
					attempt.ErrorClass,
				)
			}

			// 首字节后禁止切换
			if firstByte {
				h.logger.Error("Stream interrupted after first byte",
					zap.String("channel", channel.Name),
				)
				c.JSON(500, gin.H{
					"error": "stream interrupted",
				})

				// 记录失败的请求历史
				h.recordRequestHistory(ctx, channel.ID, req.Model, false, firstByte, int(time.Since(attemptStart).Milliseconds()), groupID)
				return
			}

			// 检查是否可重试
			if !isRetryable(err) {
				h.logger.Info("Error not retryable", zap.String("error_class", attempt.ErrorClass))
				c.JSON(500, gin.H{
					"error": fmt.Sprintf("upstream error: %v", err),
				})
				h.recordRequestHistory(ctx, channel.ID, req.Model, false, firstByte, int(time.Since(attemptStart).Milliseconds()), groupID)
				return
			}

			// 继续尝试下一个候选
			continue
		}

		// 成功
		attempt.StatusCode = 200
		attempts = append(attempts, attempt)

		// 记录成功的代理请求
		metrics.RecordProxyRequest(
			fmt.Sprintf("%d", channel.ID),
			req.Model,
			"success",
			time.Since(attemptStart).Seconds(),
		)

		// 更新熔断状态
		if h.circuit != nil {
			_ = h.circuit.UpdateCircuitState(ctx, channel.ID, req.Model, groupID, true, "")
		}

		// 记录故障切换（如果使用了备选渠道）
		if i > 0 {
			prevChannelID := routeResult.CandidateOrder[i-1]
			metrics.RecordFailover(
				fmt.Sprintf("%d", prevChannelID),
				fmt.Sprintf("%d", channel.ID),
				"success_after_retry",
			)
		}

		// 记录请求历史（用于计算成功率）
		h.recordRequestHistory(ctx, channel.ID, req.Model, true, firstByte, int(time.Since(attemptStart).Milliseconds()), groupID)

		// 设置上下文信息（供 PrometheusMiddleware 使用）
		c.Set("model", req.Model)
		c.Set("channel", fmt.Sprintf("%d", channel.ID))

		// 设置路由决策响应头（供前端展示真实路由结果）
		c.Header("X-Request-ID", requestID)
		c.Header("X-Selected-Channel", channel.Name)
		c.Header("X-Selected-Channel-Id", fmt.Sprintf("%d", channel.ID))
		c.Header("X-Strategy", routeResult.Strategy)
		if routeResult.GroupName != "" {
			c.Header("X-Group", routeResult.GroupName)
		}

		// 返回响应
		if req.Stream {
			h.streamResponse(c, result)
		} else {
			h.returnResponse(c, result)
		}

		return
	}

	// 所有候选都失败
	h.logger.Error("All upstream attempts failed",
		zap.Int("attempts", len(attempts)),
	)

	c.JSON(503, gin.H{
		"error":    "all upstream channels failed",
		"attempts": attempts,
	})
}

func (h *ProxyHandler) callUpstream(ctx context.Context, channel *router.ChannelHealth, req *ChatCompletionRequest) (io.ReadCloser, bool, error) {
	// 映射模型名
	upstreamModel := channel.ModelMapping[req.Model]
	if upstreamModel == "" {
		upstreamModel = req.Model
	}

	// 从数据库读取 api_key
	var apiKey string
	err := h.db.Pool.QueryRow(ctx, `
		SELECT api_key FROM upstreams WHERE id = $1
	`, channel.ID).Scan(&apiKey)
	if err != nil {
		return nil, false, fmt.Errorf("get api_key: %w", err)
	}

	// 构建上游请求（移除网关扩展字段 group）
	upstreamReq := *req
	upstreamReq.Group = ""
	upstreamReq.Model = upstreamModel

	body, err := json.Marshal(upstreamReq)
	if err != nil {
		return nil, false, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/v1/chat/completions", channel.BaseURL)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, false, fmt.Errorf("http request: %w", err)
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, false, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(body))
	}

	// 返回响应体（firstByte 暂时简化为 false，实际需要在第一次读取时标记）
	return resp.Body, false, nil
}

func (h *ProxyHandler) streamResponse(c *gin.Context, body io.ReadCloser) {
	defer body.Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	// 边读边写
	c.Stream(func(w io.Writer) bool {
		buf := make([]byte, 1024)
		n, err := body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
		}
		return err == nil
	})
}

func (h *ProxyHandler) returnResponse(c *gin.Context, body io.ReadCloser) {
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to read upstream response"})
		return
	}

	var response map[string]interface{}
	if err := json.Unmarshal(data, &response); err != nil {
		c.JSON(500, gin.H{"error": "invalid upstream response"})
		return
	}

	c.JSON(200, response)
}

func (h *ProxyHandler) recordRequestHistory(ctx context.Context, channelID int, model string, success bool, firstByte bool, durationMS int, groupID *int) {
	_, err := h.db.Pool.Exec(ctx, `
		INSERT INTO request_history (
			request_id, channel_id, model, success, first_byte_commit,
			ttft_ms, total_duration_ms, group_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
	`, generateRequestID(), channelID, model, success, firstByte, durationMS, durationMS, groupID)

	if err != nil {
		h.logger.Warn("Failed to record request history", zap.Error(err))
	}
}

// resolveGroupID 将分组名（或数字 ID）解析为分组 ID
func (h *ProxyHandler) resolveGroupID(ctx context.Context, spec string) (int, error) {
	// 纯数字视为 ID
	if id, err := strconv.Atoi(spec); err == nil && id > 0 {
		var enabled bool
		err := h.db.Pool.QueryRow(ctx, `SELECT enabled FROM channel_groups WHERE id = $1`, id).Scan(&enabled)
		if err != nil {
			return 0, fmt.Errorf("group %s not found", spec)
		}
		if !enabled {
			return 0, fmt.Errorf("group %s is disabled", spec)
		}
		return id, nil
	}

	var id int
	var enabled bool
	err := h.db.Pool.QueryRow(ctx, `SELECT id, enabled FROM channel_groups WHERE name = $1`, spec).Scan(&id, &enabled)
	if err != nil {
		return 0, fmt.Errorf("group %q not found", spec)
	}
	if !enabled {
		return 0, fmt.Errorf("group %q is disabled", spec)
	}
	return id, nil
}

// 辅助函数

func extractCapabilities(req *ChatCompletionRequest) []string {
	var caps []string
	if len(req.Tools) > 0 {
		caps = append(caps, "tools")
	}
	return caps
}

func estimateInputTokens(req *ChatCompletionRequest) int {
	// 简化：每个消息约 100 tokens
	return len(req.Messages) * 100
}

func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

func classifyError(err error) string {
	// 简化分类
	return "retryable_pre_commit"
}

func isRetryable(err error) bool {
	// 简化：所有错误都可重试（首字节前）
	return true
}
