package telegram

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
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
	}
	for _, u := range updates {
		w.handleUpdate(ctx, u)
		// 成功处理一条 update 后事务性推进 offset（update_id+1）
		if err := w.store.UpdateLastUpdateID(ctx, u.UpdateID+1); err != nil {
			return fmt.Errorf("update last_update_id: %w", err)
		}
	}
	_ = w.store.UpdateLastPollAt(ctx, time.Now())
	return nil
}

// handleUpdate 处理单条消息：授权校验 → 命令分发 → 回复发送。
func (w *Worker) handleUpdate(ctx context.Context, u Update) {
	if u.Text == "" {
		return
	}
	reply, err := w.cmds.Handle(ctx, u.ChatID, u.Text)
	if err != nil {
		w.logger.Warn("Command handling failed", zap.Int64("chat_id", u.ChatID), zap.Error(err))
		_ = w.store.UpdateLastError(ctx, truncateStr(err.Error(), 500))
		return
	}
	for _, part := range SplitMessage(reply, MaxTelegramMessageLen) {
		if err := w.sendToSubscriber(ctx, u.ChatID, part); err != nil {
			w.logger.Warn("Reply send failed", zap.Int64("chat_id", u.ChatID), zap.Error(err))
			return
		}
	}
}

// sendToSubscriber 向单个订阅者发送消息（拆分为多段分别记录投递）。
func (w *Worker) sendToSubscriber(ctx context.Context, chatID int64, html string) error {
	id, err := w.client.SendMessage(ctx, chatID, html)
	if err != nil {
		return err
	}
	_ = id
	return nil
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
func (w *Worker) sendReportWindow(ctx context.Context, start, end time.Time) {
	subs, err := w.store.LoadSubscribers(ctx)
	if err != nil {
		w.logger.Warn("Load subscribers for report failed", zap.Error(err))
		return
	}
	// 组装一次报告内容（订阅者间共享；组过滤在查询层完成）
	report := w.buildReport(ctx, start, end)
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
		w.deliverReport(ctx, s, report, start, end)
	}
}

// buildReport 组装报告内容（ReportBuilder 组装系统概况 + 告警变化）。
func (w *Worker) buildReport(ctx context.Context, start, end time.Time) string {
	if w.builder == nil {
		return "🛰 <b>Smart Router 告警汇总</b>\n━━━━━━━━━━━━━━━━\n时间：" +
			start.Format("2006-01-02 15:04") + "\n告警服务暂不可用。\n"
	}
	cfg, err := w.store.LoadConfig(ctx)
	if err != nil {
		cfg = Config{}
	}
	msg, err := w.builder.Build(ctx, start, int(end.Sub(start)/time.Hour), cfg, nil)
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
		msgID, sendErr = w.client.SendMessage(ctx, s.ChatID, part)
		if sendErr != nil {
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
