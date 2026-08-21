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
	for _, part := range SplitMessage(out, MaxTelegramMessageLen) {
		if _, err := w.sendToSubscriber(ctx, u.ChatID, part, kb); err != nil {
			w.logger.Warn("Reply send failed", zap.Int64("chat_id", u.ChatID), zap.Error(err))
			return err
		}
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
	out, err := w.cmds.HandleCallback(ctx, u)
	if err != nil {
		w.logger.Warn("Callback handling failed", zap.Int64("chat_id", u.CallbackChatID), zap.Error(err))
		return err
	}
	if out == "" {
		return nil // 已原位编辑或已通过异步通道处理
	}
	for _, part := range SplitMessage(out, MaxTelegramMessageLen) {
		if _, err := w.sendToSubscriber(ctx, u.CallbackChatID, part, nil); err != nil {
			w.logger.Warn("Callback reply send failed", zap.Int64("chat_id", u.CallbackChatID), zap.Error(err))
			return err
		}
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

// reportLoop 整点报告循环（30s tick 检查）。
func (w *Worker) reportLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			w.tryReport(ctx, now)
		}
	}
}

// tryReport 检查是否到达发送窗口（带 advisory lock 选主）。
func (w *Worker) tryReport(ctx context.Context, now time.Time) {
	cfg, err := w.store.LoadConfig(ctx)
	if err != nil {
		return
	}
	if !cfg.ReportEnabled || !ShouldSendReport(now, cfg, cfg.LastReportAtOrDefault()) {
		return
	}
	unlock, err := w.acquire(ctx, TelegramReportLock)
	if err != nil {
		w.logger.Warn("Telegram report lock acquire failed", zap.Error(err))
		return
	}
	defer unlock()

	// 锁内重读配置与窗口（幂等：HasDelivery 已有成功记录则跳过）
	cfg, err = w.store.LoadConfig(ctx)
	if err != nil {
		return
	}
	loc := loadLocation(cfg.Timezone)
	nowLocal := now.In(loc)
	windowStart, windowEnd := reportWindow(nowLocal, cfg)
	if !ShouldSendReport(nowLocal, cfg, cfg.LastReportAtOrDefault()) {
		return
	}
	w.sendReportWindow(ctx, windowStart, windowEnd)
	_ = w.store.UpdateLastReportAt(ctx, now)
}

// sendReportWindow 向全部 enabled + alert_enabled 订阅者发送窗口报告（幂等）。
// A2：报告按订阅者的 GroupIDs 分别构建（授权边界）——
// 分组受限的订阅者只能看到自己分组的数据，系统概况计数同样应用分组过滤。
func (w *Worker) sendReportWindow(ctx context.Context, start, end time.Time) {
	subs, err := w.store.LoadSubscribers(ctx)
	if err != nil {
		w.logger.Warn("Load subscribers for report failed", zap.Error(err))
		return
	}
	// 按分组集合缓存报告内容（相同分组的订阅者共享一份）
	reportCache := map[string]string{}
	for _, s := range subs {
		if !s.Enabled || !s.AlertEnabled {
			continue
		}
		already, err := w.store.HasDelivery(ctx, s.ID, "hourly_report", start, end)
		if err != nil {
			w.logger.Warn("HasDelivery failed", zap.Int64("sub", s.ID), zap.Error(err))
		}
		if already {
			continue // 该订阅者该窗口已成功投递（崩溃后新 owner 不重复发送）
		}
		cacheKey := groupIDsKey(s.GroupIDs)
		report, ok := reportCache[cacheKey]
		if !ok {
			report = w.buildReport(ctx, start, end, s.GroupIDs)
			reportCache[cacheKey] = report
		}
		w.deliverReport(ctx, s, report, start, end)
	}
}

// groupIDsKey 把分组集合归一化成缓存键（排序后拼接，空集合 = "all"）。
func groupIDsKey(ids []int) string {
	if len(ids) == 0 {
		return "all"
	}
	sorted := append([]int(nil), ids...)
	sort.Ints(sorted)
	return fmt.Sprintf("%v", sorted)
}

// buildReport 组装报告内容（ReportBuilder 组装系统概况 + 告警变化；
// groupIDs 控制查询范围，订阅者的分组授权边界在此生效）。
func (w *Worker) buildReport(ctx context.Context, start, end time.Time, groupIDs []int) string {
	if w.builder == nil {
		return "🛰 <b>Smart Router 告警汇总</b>\n━━━━━━━━━━━━━━━━\n时间：" +
			start.Format("2006-01-02 15:04") + "\n告警服务暂不可用。\n"
	}
	cfg, err := w.store.LoadConfig(ctx)
	if err != nil {
		cfg = Config{}
	}
	msg, err := w.builder.Build(ctx, start, int(end.Sub(start)/time.Hour), cfg, groupIDs)
	if err != nil {
		w.logger.Warn("Report build failed", zap.Error(err))
		return "🛰 <b>Smart Router 告警汇总</b>\n时间：" + start.Format("2006-01-02 15:04") + "\n告警查询失败。\n"
	}
	return msg
}

// deliverReport 发送单订阅者报告并写投递日志。
func (w *Worker) deliverReport(ctx context.Context, s Subscriber, report string, start, end time.Time) {
	var msgID int64
	var sendErr error
	for _, part := range SplitMessage(report, MaxTelegramMessageLen) {
		if msgID, sendErr = w.sendSubscriberChatID(ctx, s.ChatID, part); sendErr != nil {
			break
		}
	}
	lg := DeliveryLog{
		SubscriberID:      s.ID,
		MessageKind:       "hourly_report",
		WindowStart:       &start,
		WindowEnd:         &end,
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
}

// acquire 选主锁；未注入实现时直接成功（单实例）。
func (w *Worker) acquire(ctx context.Context, key int64) (func(), error) {
	if w.lock == nil {
		return func() {}, nil
	}
	return w.lock(ctx, key)
}

// ShouldSendReport 判断 now 是否到达发送窗口且 lastReport 已过期。
// 窗口分钟 = minute % interval == report_minute（默认 interval=60 即整点）。
// interval 小于 60 时一小时内有多个窗口（如 30 分钟 → 第 0/30 分钟）。
func ShouldSendReport(now time.Time, cfg Config, lastReport time.Time) bool {
	if !cfg.ReportEnabled {
		return false
	}
	interval := cfg.ReportIntervalMinutes
	if interval <= 0 {
		interval = 60
	}
	if interval > 60 {
		interval = 60 // 窗口最小粒度按小时内的分钟计算
	}
	if cfg.ReportMinute < 0 || cfg.ReportMinute >= 60 {
		cfg.ReportMinute = 0
	}
	loc := loadLocation(cfg.Timezone)
	local := now.In(loc)

	// 当前是否处于发送分钟（minute % interval == report_minute）
	if local.Minute()%interval != cfg.ReportMinute {
		return false
	}
	// 间隔检查：距上一个窗口起点是否 >= interval 分钟
	windowStart := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, loc)
	if !lastReport.IsZero() {
		lastLocal := lastReport.In(loc)
		lastWindow := time.Date(lastLocal.Year(), lastLocal.Month(), lastLocal.Day(), lastLocal.Hour(), lastLocal.Minute(), 0, 0, loc)
		if windowStart.Sub(lastWindow) < time.Duration(interval)*time.Minute {
			return false
		}
	}
	return true
}

// reportWindow 计算本次报告窗口 [start, end)。
func reportWindow(now time.Time, cfg Config) (time.Time, time.Time) {
	interval := cfg.ReportIntervalMinutes
	if interval <= 0 {
		interval = 60
	}
	loc := loadLocation(cfg.Timezone)
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, loc).Add(-time.Duration(interval) * time.Minute)
	return start, start.Add(time.Duration(interval) * time.Minute)
}

// loadLocation 解析时区（失败回退 Asia/Shanghai）。
func loadLocation(name string) *time.Location {
	if name == "" {
		name = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc, _ = time.LoadLocation("Asia/Shanghai")
	}
	return loc
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
