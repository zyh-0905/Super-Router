package telegram

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Advisory lock 键（多 Checker 实例选主；与迁移 746213081 同号段）。
const (
	TelegramPollerLock = int64(746213082)
	TelegramReportLock = int64(746213083)
)

// ConfigStore Worker 的持久化接口（生产实现为 PostgreSQL，测试用内存假实现）。
type ConfigStore interface {
	LoadConfig(ctx context.Context) (Config, error)
	UpdateLastUpdateID(ctx context.Context, id int64) error
	UpdateLastPollAt(ctx context.Context, t time.Time) error
	UpdateLastReportAt(ctx context.Context, t time.Time) error
	UpdateLastError(ctx context.Context, msg string) error
	ClearLastError(ctx context.Context) error
	LoadSubscribers(ctx context.Context) ([]Subscriber, error)
	HasDelivery(ctx context.Context, subID int64, kind string, start, end time.Time) (bool, error)
	LogDelivery(ctx context.Context, l DeliveryLog) error
	MarkSubscriberFailure(ctx context.Context, subID int64, errMsg string) error
	MarkSubscriberSuccess(ctx context.Context, subID int64) error
}

// DeliveryLog 投递审计条目。
type DeliveryLog struct {
	SubscriberID      int64
	MessageKind       string
	WindowStart       *time.Time
	WindowEnd         *time.Time
	Success           bool
	TelegramMessageID int64
	Error             string
}

// LockFn advisory lock 获取函数（生产为 pg_advisory_lock，测试直接成功）。
type LockFn func(ctx context.Context, key int64) (func(), error)

// Worker Telegram 长轮询 + 小时汇总调度。
type Worker struct {
	store  ConfigStore
	client BotClient
	cmds   *CommandService

	// builder 小时汇总报告构建器（nil 时报告退化为告警查询 stub）
	builder *ReportBuilder

	// lock 选主函数；nil 时默认直接成功（单实例部署）
	lock LockFn

	logger *zap.Logger
}

// NewWorker 创建 Worker。
func NewWorker(store ConfigStore, client BotClient, cmds *CommandService, logger *zap.Logger) *Worker {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cmds == nil {
		cmds = NewCommandService(nil)
	}
	return &Worker{store: store, client: client, cmds: cmds, logger: logger}
}

// SetReportBuilder 注入报告构建器（Checker 装配时注入，共享 DB）。
func (w *Worker) SetReportBuilder(b *ReportBuilder) { w.builder = b }

// SetLock 注入 advisory lock 实现（cmd/checker 装配时注入）。
func (w *Worker) SetLock(fn LockFn) { w.lock = fn }

// Run 主循环：disabled 时立即返回；否则两个独立循环（poller + reporter）。
func (w *Worker) Run(ctx context.Context) {
	cfg, err := w.store.LoadConfig(ctx)
	if err != nil {
		w.logger.Warn("Telegram worker failed to load config, exiting", zap.Error(err))
		return
	}
	if !cfg.Enabled {
		w.logger.Info("Telegram disabled, worker idle")
		return
	}
	w.logger.Info("Telegram worker starting")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.pollLoop(ctx) }()
	go func() { defer wg.Done(); w.reportLoop(ctx) }()
	wg.Wait()
	w.logger.Info("Telegram worker stopped")
}

// pollLoop 长轮询循环。
func (w *Worker) pollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		unlock, err := w.acquire(ctx, TelegramPollerLock)
		if err != nil {
			w.logger.Warn("Telegram poller lock acquire failed, retrying", zap.Error(err))
			sleepCtx(ctx, 10*time.Second)
			continue
		}
		w.logger.Debug("Telegram poller lock acquired")
		err = w.pollOnce(ctx)
		unlock()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// 网络错误指数退避，避免失败时高速重试
			backoff := time.Duration(1+rand.Intn(5)) * time.Second
			var ra *ErrRetryAfter
			if errors.As(err, &ra) {
				backoff = time.Duration(ra.Seconds) * time.Second
			}
			w.logger.Warn("Telegram poll failed, backing off", zap.Duration("backoff", backoff), zap.Error(err))
			sleepCtx(ctx, backoff)
		}
	}
}

// pollOnce 执行一轮 getUpdates 并逐条处理。
func (w *Worker) pollOnce(ctx context.Context) error {
	cfg, err := w.store.LoadConfig(ctx)
	if err != nil {
		return err
	}
	updates, err := w.client.GetUpdates(ctx, cfg.LastUpdateID, 50*time.Second)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	// 处理前刷新订阅者授权
	if subs, err := w.store.LoadSubscribers(ctx); err == nil {
		w.cmds.SetSubscribers(subs)
		// 命令菜单（setMyCommands）：按订阅者能力维护，能力未变时零 API 调用
		if merr := w.cmds.SyncBotMenu(ctx, w.client, subs); merr != nil {
			w.logger.Debug("Sync bot menu failed", zap.Error(merr))
		}
	}
	for _, u := range updates {
		// A1：仅在回复全部成功后才推进 offset。
		// 处理/发送失败时保留当前 offset（返回错误触发退避），
		// 下一轮 getUpdates 会重新投递该 update，消息不丢失。
		err := w.handleUpdate(ctx, u)
		if err != nil {
			if isPermanentTelegramError(err) {
				// M5：确定性拒绝（HTML 解析失败/chat 不存在等）重试永远失败——
				// 记 last_error 后跳过该 update 并推进 offset，
				// 防止毒丸消息无限重放卡死后续全部命令。
				w.logger.Warn("Permanent telegram error, skipping update",
					zap.Int64("update_id", u.UpdateID), zap.Int64("chat_id", u.ChatID), zap.Error(err))
				_ = w.store.UpdateLastError(ctx, truncateStr(err.Error(), 500))
			} else {
				return err
			}
		}
		if err := w.store.UpdateLastUpdateID(ctx, u.UpdateID+1); err != nil {
			return fmt.Errorf("update last_update_id: %w", err)
		}
	}
	// 全部 update 成功处理 → 清除历史错误（防止修复前遗留的 last_error 一直挂在设置页）
	_ = w.store.ClearLastError(ctx)
	_ = w.store.UpdateLastPollAt(ctx, time.Now())
	return nil
}

// handleUpdate 处理单条消息/回调：授权校验 → 分发 → 回复发送。
// 返回错误表示该 update 未能完成处理（offset 不得推进）。
func (w *Worker) handleUpdate(ctx context.Context, u Update) error {
	if u.HasCallback {
		return w.handleCallback(ctx, u)
	}
	if u.Text == "" {
		return nil
	}
	// 命令处理期间显示「正在输入…」（仅查询命令；/sitetest 有独立的进度消息）
	if isTypingCommand(u.Text) {
		_ = w.client.SendChatAction(ctx, u.ChatID, ChatActionTyping)
	}
	out, kb, err := w.cmds.HandleWithKeyboard(ctx, u.ChatID, u.Text)
	if err != nil {
		w.logger.Warn("Command handling failed", zap.Int64("chat_id", u.ChatID), zap.Error(err))
		_ = w.store.UpdateLastError(ctx, truncateStr(err.Error(), 500))
		return err
	}
	if out == "" {
		return nil // /sitetest 进度消息已由异步通道发送
	}
	// 发送拆分后的消息：永久错误（确定性 4xx）终止本组分片但**不返回错误**
	// —— 返回错误会让 offset 不推进、整条 update 无限重放。永久错误
	// 重试永远失败，继续推进 offset 保住后续命令（记 last_error 供排查）。
	permanent := false
	for _, part := range SplitMessage(out, MaxTelegramMessageLen) {
		if _, err := w.sendToSubscriber(ctx, u.ChatID, part, kb); err != nil {
			w.logger.Warn("Reply send failed", zap.Int64("chat_id", u.ChatID), zap.Error(err))
			if isPermanentTelegramError(err) {
				permanent = true
				_ = w.store.UpdateLastError(ctx, truncateStr(err.Error(), 500))
				break // 后续分片同样会失败，不再发送
			}
			return err // 可重试错误：保留 offset 重放
		}
	}
	if permanent {
		return nil // 毒丸消息已跳过（offset 会推进）
	}
	return nil
}

// handleCallback 处理内联键盘回调：先应答（关闭 loading），再执行动作。
func (w *Worker) handleCallback(ctx context.Context, u Update) error {
	// 应答失败不阻断后续处理（按钮 loading 残留 10s 会自动消失）
	if err := w.client.AnswerCallbackQuery(ctx, u.CallbackID); err != nil {
		w.logger.Debug("Answer callback failed", zap.String("callback_id", u.CallbackID), zap.Error(err))
	}
	_ = w.client.SendChatAction(ctx, u.CallbackChatID, ChatActionTyping)
	out, kb, err := w.cmds.HandleCallback(ctx, u)
	if err != nil {
		w.logger.Warn("Callback handling failed", zap.Int64("chat_id", u.CallbackChatID), zap.Error(err))
		return err
	}
	if out == "" {
		return nil // 已原位编辑或已通过异步通道处理
	}
	// kb 必须随新发消息传递（原位编辑失败回退路径的键盘由 HandleCallback 返回）
	permanent := false
	for _, part := range SplitMessage(out, MaxTelegramMessageLen) {
		if _, err := w.sendToSubscriber(ctx, u.CallbackChatID, part, kb); err != nil {
			w.logger.Warn("Callback reply send failed", zap.Int64("chat_id", u.CallbackChatID), zap.Error(err))
			if isPermanentTelegramError(err) {
				permanent = true
				_ = w.store.UpdateLastError(ctx, truncateStr(err.Error(), 500))
				break
			}
			return err
		}
	}
	if permanent {
		return nil
	}
	return nil
}

// isTypingCommand 该命令是否值得显示「正在输入…」（带查询/计算的命令；
// /sitetest 与纯帮助类命令除外）。
func isTypingCommand(text string) bool {
	cmd, _ := parseCommand(text)
	switch cmd {
	case "/alerts", "/alert", "/status", "/relay", "/balance", "/health", "/ratio", "/quality":
		return true
	}
	return false
}

// sendToSubscriber 向单个订阅者发送消息（可携带内联键盘），返回 Telegram 消息 ID。
func (w *Worker) sendToSubscriber(ctx context.Context, chatID int64, html string, kb *InlineKeyboard) (int64, error) {
	id, err := w.client.SendMessage(ctx, chatID, html, kb)
	if err != nil {
		return id, err
	}
	return id, nil
}

// SendToChat 发送 HTML 消息到指定会话（AsyncSender 接口实现，
// 供 /sitetest 等长任务异步推送结果）。
func (w *Worker) SendToChat(ctx context.Context, chatID int64, html string) error {
	_, err := w.sendToSubscriber(ctx, chatID, html, nil)
	return err
}

// RunAsync 发送进度消息 → 后台执行 work → 原位编辑为最终结果。
// work 返回错误时以失败提示推送；编辑失败降级为新发消息（不影响结果送达）。
// 供 /balance 实时刷新等长任务复用（AsyncSender 接口实现）。
func (w *Worker) RunAsync(ctx context.Context, chatID int64, progress string, work AsyncWork) error {
	msgID, err := w.sendToSubscriber(ctx, chatID, progress, nil)
	if err != nil {
		return err
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), balanceRefreshBudget)
		defer cancel()
		result, werr := work(runCtx)
		if werr != nil {
			result = "⚠️ " + EscapeHTML(truncateStr(werr.Error(), 200))
		}
		sendCtx, sendCancel := context.WithTimeout(context.Background(), siteTestSendBudget)
		defer sendCancel()
		if eerr := w.client.EditMessageText(sendCtx, chatID, msgID, result, nil); eerr == nil {
			return
		}
		for _, part := range SplitMessage(result, MaxTelegramMessageLen) {
			if _, serr := w.sendToSubscriber(sendCtx, chatID, part, nil); serr != nil {
				w.logger.Warn("RunAsync result delivery failed",
					zap.Int64("chat_id", chatID), zap.Error(serr))
				return
			}
		}
	}()
	return nil
}

// StartTest 启动一次站点测试的交互流程（AsyncSender 接口实现）：
// 发送进度消息 → 后台执行 → 原位编辑为最终结果（带「再测一次」键盘）。
// 心跳编辑失败只降级为「发一条最终结果」，不影响结果送达。
func (w *Worker) StartTest(ctx context.Context, t *StartTestContext) error {
	msgID, err := w.sendToSubscriber(ctx, t.ChatID, t.StartText(), nil)
	if err != nil {
		return err
	}
	t.ProgressMsg = msgID

	go w.runTestFlow(t)
	return nil
}

// runTestFlow 后台执行测试：测试立即启动，与进度心跳并行；完成后原位更新结果。
func (w *Worker) runTestFlow(t *StartTestContext) {
	start := time.Now()

	// 执行与心跳并行：测试立即开始（不因心跳延迟而晚启动）
	type result struct{ msg string }
	resultCh := make(chan result, 1)
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), siteTestTotalBudget)
		defer cancel()
		msg, err := t.Runner.Run(runCtx, t.ChannelID, t.Model, t.MaxTokens, t.Sub.GroupIDs)
		if err != nil {
			msg = "🧪 站点测试失败：" + err.Error()
		}
		resultCh <- result{msg: msg}
	}()

	// 心跳：每 3s 原位编辑一次进度（文本含已运行时长，保证内容不同——
	// Telegram 编辑接口要求新文本与旧文本不一致）
	var msg string
	heartbeat := time.NewTicker(3 * time.Second)
	defer heartbeat.Stop()
loop:
	for {
		select {
		case r := <-resultCh:
			msg = r.msg
			break loop
		case <-heartbeat.C:
			elapsed := time.Since(start)
			if elapsed >= siteTestTotalBudget-5*time.Second {
				continue
			}
			editCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := w.client.EditMessageText(editCtx, t.ChatID, t.ProgressMsg, t.ProgressText(elapsed), nil)
			cancel()
			if err != nil {
				heartbeat.Stop() // 编辑失败（消息被删等）：停止心跳，等结果
			}
		}
	}

	sendCtx, sendCancel := context.WithTimeout(context.Background(), siteTestSendBudget)
	defer sendCancel()
	// 结果：优先原位编辑进度消息（键盘随最终结果一起更新）
	if eerr := w.client.EditMessageText(sendCtx, t.ChatID, t.ProgressMsg, msg, t.RetryKeyboard()); eerr == nil {
		return
	}
	// 编辑失败降级：拆分新发（长结果可能超单条上限）
	for _, part := range SplitMessage(msg, MaxTelegramMessageLen) {
		if _, serr := w.sendToSubscriber(sendCtx, t.ChatID, part, nil); serr != nil {
			w.logger.Warn("Site test result delivery failed",
				zap.Int64("chat_id", t.ChatID), zap.Error(serr))
			return
		}
	}
}

// EditCallbackMessage 原位编辑回调来源的消息（CallbackResponder 接口实现）。
func (w *Worker) EditCallbackMessage(ctx context.Context, u Update, html string, kb *InlineKeyboard) error {
	editCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.client.EditMessageText(editCtx, u.CallbackChatID, u.CallbackMessageID, html, kb); err != nil {
		return err
	}
	return nil
}

// sendSubscriberChatID 把订阅者存储的 chat_id 字符串转成数值发送（前导零不影响会话身份）。
func (w *Worker) sendSubscriberChatID(ctx context.Context, chatID string, html string) (int64, error) {
	n, err := ParseChatID(chatID)
	if err != nil {
		w.logger.Warn("Invalid stored chat_id, delivery skipped", zap.String("chat_id", chatID), zap.Error(err))
		return 0, err
	}
	return w.sendToSubscriber(ctx, n, html, nil)
}

// reportLoop 告警变化推送循环（15s tick：检测上次推送以来的告警变化，
// 有「新出现/升级/恢复」才推送，无变化沉默——不再定时整点汇报）。
func (w *Worker) reportLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			w.tryPushAlerts(ctx, now)
		}
	}
}

// tryPushAlerts 检测自上次推送（水位线）以来的告警变化，有变化则推送。
// 带 advisory lock 选主；无变化时静默返回，不产生任何消息。
func (w *Worker) tryPushAlerts(ctx context.Context, now time.Time) {
	cfg, err := w.store.LoadConfig(ctx)
	if err != nil {
		return
	}
	if !cfg.ReportEnabled {
		return
	}
	unlock, err := w.acquire(ctx, TelegramReportLock)
	if err != nil {
		w.logger.Warn("Telegram alert push lock acquire failed", zap.Error(err))
		return
	}
	defer unlock()

	// 锁内重读配置与水位线
	cfg, err = w.store.LoadConfig(ctx)
	if err != nil {
		return
	}
	since := cfg.LastReportAtOrDefault()
	if since.IsZero() {
		// 首次启用：静默初始化水位线，不推送存量告警——
		// 避免把长期持续的旧告警误报为「新出现」。
		_ = w.store.UpdateLastReportAt(ctx, now)
		return
	}

	subs, err := w.store.LoadSubscribers(ctx)
	if err != nil {
		w.logger.Warn("Load subscribers for alert push failed", zap.Error(err))
		return
	}

	// 按分组集合缓存变化消息（授权边界：分组受限的订阅者只看自己分组的变化）。
	// 窗口固定为 [since, since]：since 不变则窗口稳定，配合 HasDelivery 保证
	// 部分订阅者发送失败后下轮重试不会重复打扰已成功的订阅者。
	cache := map[string]string{}
	allOK := true
	for _, s := range subs {
		if !s.Enabled || !s.AlertEnabled {
			continue
		}
		already, err := w.store.HasDelivery(ctx, s.ID, alertPushKind, since, since)
		if err != nil {
			w.logger.Warn("HasDelivery failed", zap.Int64("sub", s.ID), zap.Error(err))
		}
		if already {
			continue // 该订阅者该水位线区间已成功投递（崩溃后新 owner 不重复发送）
		}
		key := groupIDsKey(s.GroupIDs)
		msg, ok := cache[key]
		if !ok {
			msg = w.buildPush(ctx, since, s.GroupIDs)
			cache[key] = msg
		}
		if msg == "" {
			continue // 该分组无变化
		}
		if !w.deliverAlertPush(ctx, s, msg, since) {
			allOK = false
		}
	}
	if allOK {
		_ = w.store.UpdateLastReportAt(ctx, now)
	}
}

// alertPushKind 事件驱动推送的投递审计类型（区别于旧 hourly_report）。
const alertPushKind = "alert_push"

// groupIDsKey 把分组集合归一化成缓存键（排序后拼接，空集合 = "all"）。
func groupIDsKey(ids []int) string {
	if len(ids) == 0 {
		return "all"
	}
	sorted := append([]int(nil), ids...)
	sort.Ints(sorted)
	return fmt.Sprintf("%v", sorted)
}

// buildPush 组装告警变化消息；builder 为空或无变化时返回空字符串（调用方沉默）。
// groupIDs 控制查询范围，订阅者的分组授权边界在此生效。
func (w *Worker) buildPush(ctx context.Context, since time.Time, groupIDs []int) string {
	if w.builder == nil {
		return ""
	}
	cfg, err := w.store.LoadConfig(ctx)
	if err != nil {
		cfg = Config{}
	}
	msg, err := w.builder.BuildPush(ctx, since, cfg, groupIDs)
	if err != nil {
		w.logger.Warn("Alert push build failed", zap.Error(err))
		return ""
	}
	return msg
}

// deliverAlertPush 发送单订阅者告警变化消息并写投递日志；返回是否成功。
// window 用 [since, since] 承载「本批变化（since 之后）」的幂等标识。
func (w *Worker) deliverAlertPush(ctx context.Context, s Subscriber, msg string, since time.Time) bool {
	var msgID int64
	var sendErr error
	for _, part := range SplitMessage(msg, MaxTelegramMessageLen) {
		if msgID, sendErr = w.sendSubscriberChatID(ctx, s.ChatID, part); sendErr != nil {
			break
		}
	}
	lg := DeliveryLog{
		SubscriberID:      s.ID,
		MessageKind:       alertPushKind,
		WindowStart:       &since,
		WindowEnd:         &since,
		Success:           sendErr == nil,
		TelegramMessageID: msgID,
	}
	if sendErr != nil {
		lg.Error = truncateStr(sendErr.Error(), 500)
		_ = w.store.MarkSubscriberFailure(ctx, s.ID, lg.Error)
	} else {
		_ = w.store.MarkSubscriberSuccess(ctx, s.ID)
	}
	if err := w.store.LogDelivery(ctx, lg); err != nil {
		w.logger.Warn("LogDelivery failed", zap.Error(err))
	}
	return sendErr == nil
}

// acquire 选主锁；未注入实现时直接成功（单实例）。
func (w *Worker) acquire(ctx context.Context, key int64) (func(), error) {
	if w.lock == nil {
		return func() {}, nil
	}
	return w.lock(ctx, key)
}

// sleepCtx 可取消的等待。
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func zapNop() *zap.Logger { return zap.NewNop() }

// LastReportAtOrDefault 取最近报告时间（nil = 零值，表示从未发送）。
func (c Config) LastReportAtOrDefault() time.Time {
	if c.LastReportAt == nil {
		return time.Time{}
	}
	return *c.LastReportAt
}
