package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"smart-router/internal/crypto"
	"smart-router/internal/metrics"
	"smart-router/internal/protocol"
	"smart-router/internal/router"
	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// errTTFBTimeout 网关尝试级首字节超时的专用 sentinel 错误（P1-02）：
// 与调用方主动断开（context.Canceled）严格区分，允许切换到下一个候选渠道，
// 且不会被熔断统计误记为 client_canceled。
var errTTFBTimeout = errors.New("upstream first byte timeout")

type ProxyHandler struct {
	router    *router.Router
	db        *store.DB
	logger    *zap.Logger
	client    *http.Client
	circuit   *CircuitBreakerManager
	buffer    *CircuitBuffer // A3：请求历史+熔断更新异步缓冲（nil = 同步路径，测试用）
	cryptoKey string         // 上游凭据加密密钥（security.encryption_key，可为空 = 明文）
}

func NewProxyHandler(r *router.Router, db *store.DB, logger *zap.Logger, circuit *CircuitBreakerManager, cryptoKey string) *ProxyHandler {
	return &ProxyHandler{
		router:    r,
		db:        db,
		logger:    logger,
		cryptoKey: cryptoKey,
		// A3：异步缓冲请求历史与熔断更新（批量事务，单 worker 保序）。
		// 队列满时自动回退同步路径，绝不丢样本。
		buffer: NewCircuitBuffer(db.Pool, circuit, logger),
		// 不设整体 Timeout：长流式响应不应被掐断；
		// 拨号/握手/响应头均有边界（P2-06），首字节由每尝试的 TTFB 计时器控制。
		client: &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       90 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		circuit: circuit,
	}
}

// SetBuffer 注入自定义缓冲器（测试用；nil = 回退同步路径）。
func (h *ProxyHandler) SetBuffer(b *CircuitBuffer) { h.buffer = b }

// Close 关闭异步缓冲（优雅关闭时排空剩余样本；幂等）。
func (h *ProxyHandler) Close() {
	if h.buffer != nil {
		h.buffer.Close()
	}
}

// firstByteReader 在首次读到数据时触发一次回调（用于首字节后解除 TTFB 预算计时器）
type firstByteReader struct {
	rc      io.ReadCloser
	onFirst func()
	once    sync.Once
}

func (f *firstByteReader) Read(p []byte) (int, error) {
	n, err := f.rc.Read(p)
	if n > 0 {
		f.once.Do(f.onFirst)
	}
	return n, err
}

func (f *firstByteReader) Close() error { return f.rc.Close() }

// ctxReader 将 context 取消/超时映射到读取错误（用于约束非流式响应的读取阶段）。
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr ctxReader) Read(p []byte) (int, error) {
	select {
	case <-cr.ctx.Done():
		return 0, cr.ctx.Err()
	default:
	}
	return cr.r.Read(p)
}

// ChatCompletionRequest OpenAI 格式请求的最小路由视图。
// 网关只解析路由所需字段；请求体以原始 JSON 原样转发上游，仅移除网关扩展字段
// group，避免静默丢弃 top_p/stop/response_format/tool_choice/多模态内容等合法字段（P1-01）。
type ChatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream"`
	Tools     []interface{} `json:"tools"`
	Group     string        `json:"group"`
}

// ChatMessage 仅绑定 role；content 保持原始 JSON（文本/多模态数组均合法）。
type ChatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// upstreamError 上游调用失败（携带 HTTP 状态码便于分类）
type upstreamError struct {
	StatusCode int
	Body       string
	Err        error
}

func (e *upstreamError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.StatusCode > 0 {
		detail := strings.TrimSpace(e.Body)
		if len(detail) > 200 {
			detail = detail[:200] + "…"
		}
		if detail == "" {
			return fmt.Sprintf("upstream status %d", e.StatusCode)
		}
		return fmt.Sprintf("upstream status %d: %s", e.StatusCode, detail)
	}
	return "upstream error"
}

func (e *upstreamError) Unwrap() error { return e.Err }

// HandleChatCompletion 处理 /v1/chat/completions 请求
func (h *ProxyHandler) HandleChatCompletion(c *gin.Context) {
	ctx := c.Request.Context()
	requestID := generateRequestID()

	// 请求体大小限制（防止超大 JSON 造成内存 DoS）
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<20)
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid request body"})
		return
	}

	// 解析最小路由视图 + 保留原始 JSON（P1-01：上游转发用原始体，仅移除网关扩展字段）
	var req ChatCompletionRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(400, gin.H{"error": "invalid request body"})
		return
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(rawBody, &raw); err != nil {
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

	// P1-03 分组语义：
	//   1. 显式指定组（body/X-Group）：限定该组路由，越组 403，生效组写入审计/响应头/组级熔断；
	//   2. Key 绑定恰好一个组且未显式指定：自动采用该组（组默认策略/审计/响应头/组级熔断全部生效）；
	//   3. Key 绑定多个组且未显式指定：限定并集路由，策略/熔断按全局桶，group_ids 写入决策日志。
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
		} else if len(boundGroups) == 1 {
			g := boundGroups[0]
			groupID = &g
			groupIDsForRoute = []int{g}
		} else {
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
		c.Set("proxy_outcome", "no_upstream")
		if routeResult == nil {
			// 快照/策略加载失败：返回不含排除明细的通用错误
			c.JSON(503, gin.H{"error": "no available upstream"})
			return
		}
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

	// 故障切换循环：按候选顺序依次尝试，客户端未收到首字节前可切换
	attempts := []router.AttemptRecord{}
	// 请求结束时回填决策日志的 attempts（审计故障切换明细）
	defer func() {
		if len(attempts) > 0 {
			h.persistAttempts(requestID, attempts)
		}
	}()
	startTime := time.Now()
	maxAttempts := routeResult.MaxAttempts
	totalBudgetMS := routeResult.TotalBudgetMS
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if totalBudgetMS <= 0 {
		totalBudgetMS = 30000
	}

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

		// 从候选集中取出完整的渠道健康数据
		channel := routeResult.Candidates[channelID]
		if channel == nil {
			h.logger.Warn("Candidate channel missing from snapshot, skipped", zap.Int("channel_id", channelID))
			continue
		}

		// 半开渠道（按生效分组桶判断，P1-04）：预约探测名额（half_open_probe_count 个在途探测），
		// 名额耗尽则跳过该候选
		probeReserved := false
		if channel.CircuitStateForGroup(groupID) == "half_open" {
			ok, rerr := h.router.ReserveHalfOpenProbe(ctx, channel.ID, req.Model, groupID, routeResult.HalfOpenProbeCount)
			if rerr != nil {
				h.logger.Warn("Failed to reserve half-open probe slot, candidate skipped",
					zap.Int("channel_id", channel.ID), zap.Error(rerr))
				continue
			}
			if !ok {
				h.logger.Info("Half-open probe limit reached, candidate skipped",
					zap.Int("channel_id", channel.ID))
				continue
			}
			probeReserved = true
		}

		attemptStart := time.Now()

		// 单次尝试的 TTFB 预算（P1-02 修复）：不能把整个剩余总预算都给第一个候选，
		// 否则首候选慢时后续候选永远没有机会（总预算已被耗尽 → 直接 break）。
		// 预算 = min(剩余总预算, 策略首字节上限 max_ttft_ms + 渠道连接超时)，
		// 为后续候选保留切换份额。
		remainingBudgetMS := totalBudgetMS - int(elapsed)
		if remainingBudgetMS < 1000 {
			remainingBudgetMS = 1000
		}
		ttfbBudgetMS := attemptTTFBBudget(remainingBudgetMS, routePolicyMaxTTFTMS(routeResult), channel.TimeoutConnectMS)
		var firstByte atomic.Bool
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		defer cancelAttempt() // 错误路径显式调用；成功路径随 handler 结束释放（不能提前取消，否则响应体读取中断）
		ttfbTimer := time.AfterFunc(time.Duration(ttfbBudgetMS)*time.Millisecond, func() {
			if !firstByte.Load() {
				cancelAttempt()
			}
		})

		upstreamBody, err := h.callUpstream(attemptCtx, channel, raw, &req)
		if err != nil {
			ttfbTimer.Stop()
			cancelAttempt()
			// P1-02：网关尝试级首字节超时（计时器触发）与调用方断开严格区分，
			// 前者允许切换到下一个候选渠道。
			if errors.Is(err, context.Canceled) && ctx.Err() == nil && !firstByte.Load() {
				err = fmt.Errorf("%w: no first byte within %dms", errTTFBTimeout, ttfbBudgetMS)
			}
		} else {
			upstreamBody = &firstByteReader{rc: upstreamBody, onFirst: func() {
				firstByte.Store(true)
				ttfbTimer.Stop()
			}}
		}
		if err != nil {
			h.failAttempt(ctx, requestID, &req, routeResult, groupID, channel, attemptStart, err, &attempts, i, false, probeReserved, 0, 0)
			if !isRetryable(err, ctx) {
				h.logger.Info("Error not retryable", zap.String("error_class", classifyError(err)))
				c.Set("proxy_outcome", "upstream_error")
				c.JSON(502, gin.H{"error": fmt.Sprintf("upstream error: %v", err)})
				return
			}
			// 首字节未发出，切换到下一个候选
			continue
		}

		if req.Stream {
			// 流式：边读边转发；首字节写给客户端后无法再切换
			committed, ok, clientGone := h.streamResponse(c, upstreamBody, func() {
				h.setRoutingHeaders(c, requestID, channel, routeResult)
				c.Set("model", req.Model)
				c.Set("channel", fmt.Sprintf("%d", channel.ID))
			})
			if !ok {
				if clientGone && committed {
					// H5：客户端主动断开 ≠ 上游故障。
					// 只记历史（error_class=client_canceled，熔断统计排除），
					// 不更新熔断、不记上游失败指标。
					h.recordRequestHistory(ctx, requestID, channel.ID, req.Model, false, true,
						int(time.Since(attemptStart).Milliseconds()), groupID, "client_canceled", 0, 0)
					c.Set("proxy_outcome", "client_canceled")
					return
				}
				if committed {
					// 客户端已收到部分数据，只能中断连接（上游中断）
					c.Set("proxy_outcome", "stream_interrupted")
					h.failAttempt(ctx, requestID, &req, routeResult, groupID, channel, attemptStart,
						fmt.Errorf("stream interrupted: %w", io.ErrUnexpectedEOF), &attempts, i, true, probeReserved, 0, 0)
					return
				}
				// 客户端未收到任何字节（含 H4 空 SSE），安全切换
				h.failAttempt(ctx, requestID, &req, routeResult, groupID, channel, attemptStart,
					fmt.Errorf("stream failed before first byte"), &attempts, i, false, probeReserved, 0, 0)
				continue
			}
			c.Set("proxy_outcome", "success")
			// B1：流式末尾 usage chunk 经 gin context 传给 succeedAttempt 落库
			h.succeedAttempt(ctx, requestID, &req, routeResult, groupID, channel, attemptStart, &attempts, i, probeReserved,
				c.GetInt("usage_prompt"), c.GetInt("usage_completion"))
			return
		}

		// 非流式：先完整缓冲响应；读取失败时客户端未收到任何数据，可安全切换。
		// B1：响应体已在内存中，顺带解析 usage（成本统计，零额外开销）。
		// 读取阶段受渠道 timeout_total_ms 约束（P2-07），并保留 10 分钟兜底上限。
		readCtx, cancelRead := context.WithTimeout(ctx, 10*time.Minute)
		if channel.TimeoutTotalMS > 0 {
			cancelRead()
			elapsedMS := int(time.Since(attemptStart).Milliseconds())
			remaining := channel.TimeoutTotalMS - elapsedMS
			if remaining < 1000 {
				remaining = 1000
			}
			readCtx, cancelRead = context.WithTimeout(ctx, time.Duration(remaining)*time.Millisecond)
		}
		// H3：读取超时必须能中断已阻塞的网络读。ctxReader 只在读前检查
		// context，无法打断阻塞中的 body.Read——把 readCtx 与上游请求
		// context 联动：readCtx 到期 → 取消 attemptCtx → 阻塞读立即返回。
		stopAfter := context.AfterFunc(readCtx, cancelAttempt)
		data, readErr := io.ReadAll(io.LimitReader(ctxReader{ctx: readCtx, r: upstreamBody}, 64<<20))
		stopAfter()
		cancelRead()
		upstreamBody.Close()
		if readErr != nil {
			attemptErr := readErr
			if errors.Is(readErr, context.Canceled) && ctx.Err() == nil && !firstByte.Load() {
				attemptErr = fmt.Errorf("%w: no first byte within %dms", errTTFBTimeout, ttfbBudgetMS)
			}
			h.failAttempt(ctx, requestID, &req, routeResult, groupID, channel, attemptStart, attemptErr, &attempts, i, false, probeReserved, 0, 0)
			if isRetryable(attemptErr, ctx) {
				continue
			}
			c.Set("proxy_outcome", "upstream_error")
			c.JSON(502, gin.H{"error": fmt.Sprintf("upstream error: %v", attemptErr)})
			return
		}

		var response map[string]interface{}
		if err := json.Unmarshal(data, &response); err != nil {
			h.failAttempt(ctx, requestID, &req, routeResult, groupID, channel, attemptStart,
				fmt.Errorf("invalid upstream response: %w", err), &attempts, i, false, probeReserved, 0, 0)
			continue
		}

		// B1：非流式响应直接解析 usage（已在内存中，零额外开销）
		if usage, ok := response["usage"].(map[string]interface{}); ok {
			c.Set("usage_prompt", asInt(usage["prompt_tokens"]))
			c.Set("usage_completion", asInt(usage["completion_tokens"]))
		}

		c.Set("proxy_outcome", "success")
		// B1：非流式 usage 已解析进 gin context
		h.succeedAttempt(ctx, requestID, &req, routeResult, groupID, channel, attemptStart, &attempts, i, probeReserved,
			c.GetInt("usage_prompt"), c.GetInt("usage_completion"))
		h.setRoutingHeaders(c, requestID, channel, routeResult)
		c.Set("model", req.Model)
		c.Set("channel", fmt.Sprintf("%d", channel.ID))
		c.JSON(200, response)
		return
	}

	// 所有候选都失败
	h.logger.Error("All upstream attempts failed",
		zap.Int("attempts", len(attempts)),
	)
	c.Set("proxy_outcome", "no_upstream")

	c.JSON(503, gin.H{
		"error":    "all upstream channels failed",
		"attempts": attempts,
	})
}

// persistAttempts 回填决策日志的 attempts 明细。
// 刻意使用独立的 Background 上下文（生命周期任务）：客户端断开也需完成审计回填。
func (h *ProxyHandler) persistAttempts(requestID string, attempts []router.AttemptRecord) {
	data, err := json.Marshal(attempts)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := h.db.Pool.Exec(ctx, `
		UPDATE decision_logs SET attempts = $1 WHERE request_id = $2
	`, string(data), requestID); err != nil {
		h.logger.Warn("Failed to persist decision attempts", zap.String("request_id", requestID), zap.Error(err))
	}
}

// routePolicyMaxTTFTMS 取本次决策生效策略的首字节上限（默认 5000ms）。
func routePolicyMaxTTFTMS(routeResult *router.RouteResult) int {
	if routeResult != nil && routeResult.Policy != nil {
		if v := routeResult.Policy.GetConfigInt("max_ttft_ms", 5000); v > 0 {
			return v
		}
	}
	return 5000
}

// attemptTTFBBudget 计算单次尝试的首字节预算（ms）：
// min(剩余总预算, 策略首字节上限 + 连接超时)，至少 1s。
// 关键意义：不为单个候选耗尽全部剩余总预算——首候选超时后，
// 后续候选仍有预算份额可以切换（修复"all upstream channels failed"且 attempts=1 的问题）。
func attemptTTFBBudget(remainingTotalMS, maxTTFTMS, connectMS int) int {
	budget := remainingTotalMS
	if budget < 1000 {
		budget = 1000
	}
	if maxTTFTMS <= 0 {
		maxTTFTMS = 5000
	}
	if connectMS <= 0 {
		connectMS = 5000
	}
	if v := maxTTFTMS + connectMS; v < budget {
		budget = v
	}
	if budget < 1000 {
		budget = 1000
	}
	return budget
}

// headerSafe 将可能含非 ASCII 字符的响应头值做 URI 编码。
// HTTP 响应头按 Latin-1 解读：中文直接写入会在浏览器侧（fetch headers.get）变成乱码。
// 纯 ASCII 值原样返回，编码后的值由前端 decodeURIComponent 还原。
func headerSafe(v string) string {
	if v == "" {
		return v
	}
	for _, r := range v {
		if r < 32 || r > 126 {
			return url.PathEscape(v)
		}
	}
	return v
}

// setRoutingHeaders 设置路由决策响应头（供前端/客户端查看真实路由结果）
func (h *ProxyHandler) setRoutingHeaders(c *gin.Context, requestID string, channel *router.ChannelHealth, routeResult *router.RouteResult) {
	c.Header("X-Request-ID", requestID)
	c.Header("X-Selected-Channel", headerSafe(channel.Name))
	c.Header("X-Selected-Channel-Id", fmt.Sprintf("%d", channel.ID))
	c.Header("X-Strategy", routeResult.Strategy)
	if routeResult.GroupName != "" {
		c.Header("X-Group", headerSafe(routeResult.GroupName))
	}
}

// failAttempt 记录一次失败尝试：指标、熔断、请求历史与故障切换计数
func (h *ProxyHandler) failAttempt(ctx context.Context, requestID string, req *ChatCompletionRequest, routeResult *router.RouteResult, groupID *int, channel *router.ChannelHealth, attemptStart time.Time, err error, attempts *[]router.AttemptRecord, index int, firstByteCommitted bool, probeReserved bool, promptTokens, completionTokens int) {
	if probeReserved {
		// 探测完成（无论成败），释放半开探测名额（按分组桶，P1-04）
		h.router.ReleaseHalfOpenProbe(ctx, channel.ID, req.Model, groupID)
	}

	errClass := classifyError(err)
	*attempts = append(*attempts, router.AttemptRecord{
		ChannelID:       channel.ID,
		StartedAt:       attemptStart,
		DurationMS:      int(time.Since(attemptStart).Milliseconds()),
		FirstByteCommit: firstByteCommitted,
		ErrorClass:      errClass,
	})

	metrics.RecordProxyRequest(
		fmt.Sprintf("%d", channel.ID),
		req.Model,
		"error",
		time.Since(attemptStart).Seconds(),
	)

	// H1：先写请求历史（含当前失败结果），再更新熔断——
	// 开闸判定读 10 分钟窗口，必须包含当前请求本身。
	// A3：两个写入合并为一个缓冲样本，批量事务内保持先历史后熔断的顺序。
	if h.buffer != nil {
		h.buffer.Enqueue(circuitSample{
			requestID: requestID, channelID: channel.ID, model: req.Model, groupID: groupID,
			success: false, firstByte: firstByteCommitted,
			durationMS:       int(time.Since(attemptStart).Milliseconds()),
			errorClass:       errClass,
			promptTokens:     promptTokens,
			completionTokens: completionTokens,
		})
	} else {
		h.recordRequestHistory(ctx, requestID, channel.ID, req.Model, false, firstByteCommitted, int(time.Since(attemptStart).Milliseconds()), groupID, errClass, promptTokens, completionTokens)
		if h.circuit != nil {
			if cerr := h.circuit.UpdateCircuitState(ctx, channel.ID, req.Model, groupID, false, errClass); cerr != nil {
				h.logger.Warn("Failed to update circuit state (failure)",
					zap.Int("channel_id", channel.ID), zap.Error(cerr))
			}
		}
	}

	if index > 0 {
		prevChannelID := routeResult.CandidateOrder[index-1]
		metrics.RecordFailover(
			fmt.Sprintf("%d", prevChannelID),
			fmt.Sprintf("%d", channel.ID),
			errClass,
		)
	}

	h.logger.Warn("Upstream call failed",
		zap.String("channel", channel.Name),
		zap.Error(err),
		zap.Bool("first_byte_committed", firstByteCommitted),
	)
}

// succeedAttempt 记录一次成功尝试：指标、熔断、请求历史与故障切换计数。
// promptTokens/completionTokens 为上游返回的真实用量（B1 成本统计；0 = 未捕获）。
func (h *ProxyHandler) succeedAttempt(ctx context.Context, requestID string, req *ChatCompletionRequest, routeResult *router.RouteResult, groupID *int, channel *router.ChannelHealth, attemptStart time.Time, attempts *[]router.AttemptRecord, index int, probeReserved bool, promptTokens, completionTokens int) {
	if probeReserved {
		// 探测成功，释放半开探测名额（按分组桶，P1-04）
		h.router.ReleaseHalfOpenProbe(ctx, channel.ID, req.Model, groupID)
	}

	*attempts = append(*attempts, router.AttemptRecord{
		ChannelID:       channel.ID,
		StartedAt:       attemptStart,
		DurationMS:      int(time.Since(attemptStart).Milliseconds()),
		FirstByteCommit: true,
		StatusCode:      200,
	})

	metrics.RecordProxyRequest(
		fmt.Sprintf("%d", channel.ID),
		req.Model,
		"success",
		time.Since(attemptStart).Seconds(),
	)

	// H1：先写请求历史（含当前成功结果），再更新熔断
	// A3：同上，合并为单个缓冲样本。
	if h.buffer != nil {
		h.buffer.Enqueue(circuitSample{
			requestID: requestID, channelID: channel.ID, model: req.Model, groupID: groupID,
			success: true, firstByte: true,
			durationMS:       int(time.Since(attemptStart).Milliseconds()),
			errorClass:       "",
			promptTokens:     promptTokens,
			completionTokens: completionTokens,
		})
	} else {
		h.recordRequestHistory(ctx, requestID, channel.ID, req.Model, true, true, int(time.Since(attemptStart).Milliseconds()), groupID, "", promptTokens, completionTokens)
		if h.circuit != nil {
			if cerr := h.circuit.UpdateCircuitState(ctx, channel.ID, req.Model, groupID, true, ""); cerr != nil {
				h.logger.Warn("Failed to update circuit state (success)",
					zap.Int("channel_id", channel.ID), zap.Error(cerr))
			}
		}
	}

	if index > 0 {
		prevChannelID := routeResult.CandidateOrder[index-1]
		metrics.RecordFailover(
			fmt.Sprintf("%d", prevChannelID),
			fmt.Sprintf("%d", channel.ID),
			"success_after_retry",
		)
	}
}

// cloneMap 浅拷贝 JSON map（值不被改写，仅顶层键操作）。
func cloneMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// callUpstream 调用上游站点，返回 200 时的响应体（未读取）。
// raw 为客户端原始请求 JSON：仅移除网关扩展字段 group 并映射模型名，
// 其余字段（top_p/stop/response_format/tool_choice/多模态内容等）原样保留（P1-01）。
// anthropic 协议站点：自动转换请求/响应/流式格式与认证头，对外仍为 OpenAI 格式。
func (h *ProxyHandler) callUpstream(ctx context.Context, channel *router.ChannelHealth, raw map[string]interface{}, req *ChatCompletionRequest) (io.ReadCloser, error) {
	// 映射模型名
	upstreamModel := channel.ModelMapping[req.Model]
	if upstreamModel == "" {
		upstreamModel = req.Model
	}

	// 从数据库读取并解密 api_key（P1-07）
	var apiKey string
	err := h.db.Pool.QueryRow(ctx, `
		SELECT api_key FROM upstreams WHERE id = $1
	`, channel.ID).Scan(&apiKey)
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("get api_key: %w", err)}
	}
	apiKey, err = crypto.Decrypt(apiKey, h.cryptoKey)
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("decrypt api_key: %w", err)}
	}

	if protocol.IsAnthropic(channel.Protocol) {
		return h.callAnthropic(ctx, channel, raw, upstreamModel, apiKey, req)
	}

	// 构建上游请求（移除网关扩展字段 group，原样保留其余字段）
	upstream := cloneMap(raw)
	delete(upstream, "group")
	upstream["model"] = upstreamModel

	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("marshal request: %w", err)}
	}

	url := fmt.Sprintf("%s/v1/chat/completions", channel.BaseURL)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("create request: %w", err)}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("http request: %w", err)}
	}

	if resp.StatusCode != 200 {
		// C4：不读取上游错误正文——上游可能在错误中回显请求头
		// （含我们附加的上游 API Key），正文会被返回给 caller 并写入日志。
		// 只保留状态码与稳定错误类别，诊断靠 checker 侧脱敏日志。
		resp.Body.Close()
		return nil, &upstreamError{StatusCode: resp.StatusCode}
	}

	return resp.Body, nil
}

// callAnthropic 调用 anthropic 协议站点（x-api-key 认证 + /v1/messages + 格式转换）。
// 注：协议转换目前仅覆盖文本消息与工具定义；多模态内容块的转换是已知限制（参见 README）。
func (h *ProxyHandler) callAnthropic(ctx context.Context, channel *router.ChannelHealth, raw map[string]interface{}, upstreamModel, apiKey string, req *ChatCompletionRequest) (io.ReadCloser, error) {
	// 构建 OpenAI 格式请求再转换（移除网关扩展字段 group）
	upstream := cloneMap(raw)
	delete(upstream, "group")
	upstream["model"] = upstreamModel

	payload, err := json.Marshal(protocol.OpenAIToAnthropic(upstream))
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("convert request: %w", err)}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		protocol.ChatEndpoint(channel.BaseURL, channel.Protocol), bytes.NewReader(payload))
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("create request: %w", err)}
	}
	for k, v := range protocol.AnthropicHeaders(apiKey) {
		httpReq.Header.Set(k, v)
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("http request: %w", err)}
	}

	if resp.StatusCode != 200 {
		// C4：不读取/传播上游错误正文（可能回显我们附加的 x-api-key）。
		// 只保留状态码与稳定错误类别。
		resp.Body.Close()
		return nil, &upstreamError{StatusCode: resp.StatusCode}
	}

	if req.Stream {
		return protocol.NewAnthropicStreamTransformer(resp.Body, upstreamModel), nil
	}

	// 非流式：读全量并转换为 OpenAI 响应
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &upstreamError{Err: fmt.Errorf("read response: %w", err)}
	}
	converted, err := protocol.AnthropicToOpenAI(body)
	if err != nil {
		return nil, &upstreamError{Err: err}
	}
	return io.NopCloser(bytes.NewReader(converted)), nil
}

// streamResponse 边读边转发 SSE 流。
// 返回 (committed, ok, clientGone)：
//   - committed：是否已向客户端写出数据；
//   - ok：流是否正常结束（H4：从未写出任何字节就 EOF → ok=false，
//     视为上游未产出任何内容，调用方可切换下一个候选）；
//   - clientGone：客户端主动断开（H5：与上游故障严格区分，
//     调用方不得将其计入上游熔断/失败指标）。
//
// B1：嗅探流末尾的 usage chunk（转发不拦截，只透传时顺带解析成本统计用）。
func (h *ProxyHandler) streamResponse(c *gin.Context, body io.ReadCloser, setHeaders func()) (committed bool, ok bool, clientGone bool) {
	defer body.Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	var usagePrompt, usageCompletion int
	buf := make([]byte, 4096)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if !committed {
				setHeaders()
			}
			if p, comp := scanUsageChunk(buf[:n]); p > 0 || comp > 0 {
				usagePrompt, usageCompletion = p, comp
			}
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				// 客户端已断开：不是上游故障
				return committed, false, true
			}
			c.Writer.Flush()
			committed = true
		}
		if err != nil {
			if err == io.EOF {
				// H4：首字节前就 EOF = 空 SSE 响应，不算成功
				if committed {
					c.Set("usage_prompt", usagePrompt)
					c.Set("usage_completion", usageCompletion)
				}
				return committed, committed, false
			}
			return committed, false, false
		}
	}
}

// scanUsageChunk 从 SSE 字节流中嗅探 usage 字段（B1 成本统计，best-effort）。
// 流式响应的 usage 通常出现在最后一帧（include_usage 或结束帧），
// 逐块扫描 `"usage":{...}` 并取最后一次出现（跨块边界不完整时丢弃，
// 只做尽力而为的统计，绝不影响转发）。
func scanUsageChunk(chunk []byte) (promptTokens, completionTokens int) {
	idx := 0
	for {
		i := bytes.Index(chunk[idx:], []byte(`"usage"`))
		if i < 0 {
			return promptTokens, completionTokens
		}
		start := idx + i + len(`"usage"`)
		// 找 { 到匹配 } 的最小窗口
		open := bytes.IndexByte(chunk[start:], '{')
		if open < 0 {
			return promptTokens, completionTokens // 跨块截断：丢弃
		}
		body := chunk[start+open:]
		closeIdx := bytes.IndexByte(body, '}')
		if closeIdx < 0 {
			return promptTokens, completionTokens
		}
		var usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}
		if json.Unmarshal(body[:closeIdx+1], &usage) == nil {
			promptTokens = usage.PromptTokens
			completionTokens = usage.CompletionTokens
			if promptTokens == 0 && completionTokens == 0 && usage.TotalTokens > 0 {
				// 上游只给 total：无法拆分，prompt 记 total（保守计入输入）
				promptTokens = usage.TotalTokens
			}
		}
		idx = start + open + closeIdx + 1
	}
}

// recordRequestHistory 写入请求历史（requestID 复用本次请求，便于全链路追踪）。
// errorClass 用于熔断样本过滤（client_canceled 不参与开闸统计）。
// A3：优先经 CircuitBuffer 异步批量落库；队列满时回退同步。
func (h *ProxyHandler) recordRequestHistory(ctx context.Context, requestID string, channelID int, model string, success bool, firstByte bool, durationMS int, groupID *int, errorClass string, promptTokens, completionTokens int) {
	if h.buffer != nil {
		h.buffer.Enqueue(circuitSample{
			requestID:  requestID,
			channelID:  channelID,
			model:      model,
			groupID:    groupID,
			success:    success,
			firstByte:  firstByte,
			durationMS: durationMS,
			errorClass: errorClass,
			// client_canceled 等不计入熔断的样本（H5）：只写历史不更新熔断
			skipCircuit:      errorClass == "client_canceled",
			promptTokens:     promptTokens,
			completionTokens: completionTokens,
		})
		return
	}
	insertRequestHistory(h.ctxOrDefault(ctx), h.db, requestID, channelID, model, success, firstByte, durationMS, groupID, errorClass, promptTokens, completionTokens)
}

// ctxOrDefault 缓冲不可用时以请求上下文写库（保持旧行为；带兜底超时）。
func (h *ProxyHandler) ctxOrDefault(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx
}

// insertRequestHistory 历史写入的核心语句（同步单语句路径；缓冲批量路径见 insertRequestHistoryQ）。
func insertRequestHistory(ctx context.Context, db *store.DB, requestID string, channelID int, model string, success bool, firstByte bool, durationMS int, groupID *int, errorClass string, promptTokens, completionTokens int) {
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO request_history (
			request_id, channel_id, model, success, first_byte_commit,
			ttft_ms, total_duration_ms, group_id, error_class,
			prompt_tokens, completion_tokens, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
	`, requestID, channelID, model, success, firstByte, durationMS, durationMS,
		groupID, errorClass, nullableInt(promptTokens), nullableInt(completionTokens)); err != nil {
		// 同步路径仅告警（缓冲路径由 worker 负责重试/回退）
	}
}

// asInt 宽松数字转换（usage 字段可能为 float64，JSON 解码默认）。
func asInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

// insertRequestHistoryQ 缓冲批量路径的历史写入（事务内；失败返回错误供回退）。
// prompt/completion tokens 为上游返回的真实用量（B1 成本统计）；0 或 NULL 表示未捕获。
func insertRequestHistoryQ(ctx context.Context, q circuitTx, s circuitSample) error {
	_, err := q.Exec(ctx, `
		INSERT INTO request_history (
			request_id, channel_id, model, success, first_byte_commit,
			ttft_ms, total_duration_ms, group_id, error_class,
			prompt_tokens, completion_tokens, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
	`, s.requestID, s.channelID, s.model, s.success, s.firstByte, s.durationMS, s.durationMS,
		s.groupID, s.errorClass, nullableInt(s.promptTokens), nullableInt(s.completionTokens))
	return err
}

// nullableInt 0 → NULL（未捕获的用量以 NULL 入库，聚合时区分「无数据」与「真实 0」）。
func nullableInt(v int) interface{} {
	if v <= 0 {
		return nil
	}
	return v
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

// 输入 token 估算的经验系数（无分词器，仅用于成本估算与价格上限过滤，
// 不用于计费；真实用量以上游返回的 usage 为准）。
const (
	tokensPerASCIIChar    = 0.25 // 英文约 4 字符/token
	tokensPerNonASCIIChar = 0.6  // 中日韩等约 1.7 字符/token
	messageOverheadTokens = 4    // 每条消息的 role/分隔符开销
	mediaPartTokens       = 800  // 单个图片/音频块的粗略等效 token
)

func extractCapabilities(req *ChatCompletionRequest) []string {
	var caps []string
	if len(req.Tools) > 0 {
		caps = append(caps, "tools")
	}
	return caps
}

func estimateInputTokens(req *ChatCompletionRequest) int {
	total := 0.0
	for _, m := range req.Messages {
		total += messageOverheadTokens
		total += estimateContentTokens(m.Content)
	}
	// 工具定义也计入输入上下文，schema 较大时占比可观
	for _, t := range req.Tools {
		if b, err := json.Marshal(t); err == nil {
			total += estimateTextTokens(string(b))
		}
	}
	if total < 1 {
		return 1
	}
	return int(math.Ceil(total))
}

// estimateContentTokens 估算单条消息 content 的 token 数。
// content 为原始 JSON：可能是字符串，也可能是多模态内容块数组。
func estimateContentTokens(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		return estimateTextTokens(s)
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		total := 0.0
		for _, p := range parts {
			switch p.Type {
			case "text":
				total += estimateTextTokens(p.Text)
			case "image_url", "input_audio":
				total += mediaPartTokens
			}
		}
		return total
	}

	// 无法识别的结构：按原始 JSON 字节兜底，好过记为 0
	return estimateTextTokens(string(raw))
}

// estimateTextTokens 按字符类别估算 token 数。
// 无分词器，用经验比例区分 ASCII 与非 ASCII：英文约 4 字符/token，
// 中日韩等非 ASCII 字符信息密度高，约 1.7 字符/token。
func estimateTextTokens(s string) float64 {
	total := 0.0
	for _, r := range s {
		if r < utf8.RuneSelf {
			total += tokensPerASCIIChar
		} else {
			total += tokensPerNonASCIIChar
		}
	}
	return total
}

func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

// classifyError 将错误归类为稳定类别（写入决策日志与请求历史）
func classifyError(err error) string {
	// P1-02：网关尝试级首字节超时是独立类别，而不是 client_canceled
	if errors.Is(err, errTTFBTimeout) {
		return "timeout"
	}
	var ue *upstreamError
	if errors.As(err, &ue) {
		switch {
		case ue.StatusCode == http.StatusTooManyRequests:
			return "rate_limited"
		case ue.StatusCode == http.StatusUnauthorized || ue.StatusCode == http.StatusForbidden:
			return "auth_error"
		case ue.StatusCode >= 500:
			return "upstream_error"
		case ue.StatusCode >= 400:
			return "bad_request"
		case ue.StatusCode > 0:
			return "upstream_error"
		}
		// StatusCode == 0：上游未返回 HTTP 响应，按底层错误继续分类
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "client_canceled"
	}
	return "network_error"
}

// isRetryable 判断错误是否允许切换到下一个候选渠道。
// callerCtx 为调用方请求上下文：尝试级超时（TTFB 预算/读取阶段超时）在调用方仍存活时可换渠道重试。
func isRetryable(err error, callerCtx context.Context) bool {
	// P1-02：网关尝试级首字节超时 → 可切换
	if errors.Is(err, errTTFBTimeout) {
		return callerCtx.Err() == nil
	}
	var ue *upstreamError
	if errors.As(err, &ue) && ue.StatusCode > 0 {
		// 上游返回了 HTTP 状态码：按状态码判定
		switch {
		case ue.StatusCode == http.StatusTooManyRequests || ue.StatusCode == http.StatusRequestTimeout:
			return true // 限流/超时：换渠道有意义
		case ue.StatusCode >= 500:
			return true // 上游 5xx：换渠道有意义
		default:
			return false // 4xx（认证/参数错误）：换渠道无意义
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return callerCtx.Err() == nil // 尝试级/读取阶段超时可重试；调用方超时则否
	}
	if errors.Is(err, context.Canceled) {
		return false // 调用方断开：重试无意义
	}
	return true // 网络错误（含上游未响应）：可切换
}
