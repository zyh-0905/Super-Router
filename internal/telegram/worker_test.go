package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBotClient 记录调用并支持注入错误。
type fakeBotClient struct {
	mu       sync.Mutex
	updates  []Update
	updErr   error
	sent     map[int64][]string
	sendErr  error
	lastMsg  string
	meCalled bool

	// 扩展协议调用记录
	edited       map[int64][]string // messageID → 每次编辑的文本
	editErr      error
	actions      []string
	callbacks    []string
	menuCmds     []BotCommand
	menuErr      error
	lastKeyboard *InlineKeyboard // 最后一次发送携带的键盘
}

func (f *fakeBotClient) GetMe(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meCalled = true
	return nil
}
func (f *fakeBotClient) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updErr != nil {
		return nil, f.updErr
	}
	out := f.updates
	f.updates = nil // 已消费，避免无限循环
	return out, nil
}
func (f *fakeBotClient) SendMessage(ctx context.Context, chatID int64, html string, kb *InlineKeyboard) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	if f.sent == nil {
		f.sent = map[int64][]string{}
	}
	f.sent[chatID] = append(f.sent[chatID], html)
	f.lastMsg = html
	f.lastKeyboard = kb
	return int64(len(f.sent[chatID])), nil
}
func (f *fakeBotClient) EditMessageText(ctx context.Context, chatID int64, messageID int64, html string, kb *InlineKeyboard) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	if f.edited == nil {
		f.edited = map[int64][]string{}
	}
	f.edited[messageID] = append(f.edited[messageID], html)
	return nil
}
func (f *fakeBotClient) SendChatAction(ctx context.Context, chatID int64, action string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, action)
	return nil
}
func (f *fakeBotClient) AnswerCallbackQuery(ctx context.Context, callbackID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callbacks = append(f.callbacks, callbackID)
	return nil
}
func (f *fakeBotClient) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.menuErr != nil {
		return f.menuErr
	}
	f.menuCmds = append([]BotCommand(nil), commands...)
	return nil
}

// fakeConfigStore 配置/订阅者/投递日志的内存实现。
type fakeConfigStore struct {
	mu          sync.Mutex
	cfg         Config
	subscribers []Subscriber
	logs        []DeliveryLog
}

func (f *fakeConfigStore) LoadConfig(ctx context.Context) (Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, nil
}
func (f *fakeConfigStore) UpdateLastUpdateID(ctx context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg.LastUpdateID = id
	return nil
}
func (f *fakeConfigStore) UpdateLastPollAt(ctx context.Context, t time.Time) error { return nil }
func (f *fakeConfigStore) UpdateLastReportAt(ctx context.Context, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg.LastReportAt = &t
	return nil
}
func (f *fakeConfigStore) UpdateLastError(ctx context.Context, msg string) error { return nil }
func (f *fakeConfigStore) LoadSubscribers(ctx context.Context) ([]Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribers, nil
}
func (f *fakeConfigStore) HasDelivery(ctx context.Context, subID int64, kind string, start, end time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.logs {
		if l.SubscriberID == subID && l.MessageKind == kind && l.Success {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeConfigStore) LogDelivery(ctx context.Context, l DeliveryLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, l)
	return nil
}
func (f *fakeConfigStore) MarkSubscriberFailure(ctx context.Context, subID int64, errMsg string) error {
	return nil
}
func (f *fakeConfigStore) MarkSubscriberSuccess(ctx context.Context, subID int64) error { return nil }

func TestShouldSendReport(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 14:00 整点，lastReport 为 nil → 应发送
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, loc)
	cfg := Config{ReportEnabled: true, ReportIntervalMinutes: 60, ReportMinute: 0, Timezone: "Asia/Shanghai"}
	if !ShouldSendReport(now, cfg, time.Time{}) {
		t.Fatal("first report at 14:00 should send")
	}
	// 同一小时已发 → 不再发
	last := time.Date(2026, 8, 19, 14, 0, 1, 0, loc)
	if ShouldSendReport(now.Add(time.Minute), cfg, last) {
		t.Fatal("same hour re-send not allowed")
	}
	// 15:00 整点 → 发送
	if !ShouldSendReport(time.Date(2026, 8, 19, 15, 0, 30, 0, loc), cfg, last) {
		t.Fatal("15:00 should send")
	}
	// report_minute=30：14:30 发送，14:45 不发
	cfg2 := Config{ReportEnabled: true, ReportIntervalMinutes: 60, ReportMinute: 30, Timezone: "Asia/Shanghai"}
	if !ShouldSendReport(time.Date(2026, 8, 19, 14, 30, 0, 0, loc), cfg2, time.Time{}) {
		t.Fatal("14:30 with report_minute=30 should send")
	}
	if ShouldSendReport(time.Date(2026, 8, 19, 14, 45, 0, 0, loc), cfg2, time.Time{}) {
		t.Fatal("14:45 should not send (minute 30 not reached)")
	}
	// disabled 不发送
	cfg3 := Config{ReportEnabled: false}
	if ShouldSendReport(now, cfg3, time.Time{}) {
		t.Fatal("disabled report must not send")
	}
	// 每 30 分钟间隔
	cfg4 := Config{ReportEnabled: true, ReportIntervalMinutes: 30, ReportMinute: 0, Timezone: "Asia/Shanghai"}
	last2 := time.Date(2026, 8, 19, 14, 0, 0, 0, loc)
	if !ShouldSendReport(time.Date(2026, 8, 19, 14, 30, 0, 0, loc), cfg4, last2) {
		t.Fatal("30min interval should send at 14:30")
	}
	if ShouldSendReport(time.Date(2026, 8, 19, 14, 20, 0, 0, loc), cfg4, last2) {
		t.Fatal("14:20 before 30min interval must not send")
	}
}

func TestWorkerDisabledDoesNothing(t *testing.T) {
	store := &fakeConfigStore{cfg: Config{Enabled: false}}
	bot := &fakeBotClient{}
	w := NewWorker(store, bot, nil, zapNop())
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	if bot.meCalled {
		t.Fatal("getMe called while disabled")
	}
	if len(bot.sent) > 0 {
		t.Fatal("messages sent while disabled")
	}
}

// 简化：直接用命令服务 + poller 逻辑测试授权（Worker 集成细节在下文）
func TestWorkerReplyUnauthorized(t *testing.T) {
	store := &fakeConfigStore{
		cfg: Config{Enabled: true},
		subscribers: []Subscriber{
			{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
		},
	}
	bot := &fakeBotClient{}
	q := &fakeQueryService{results: map[string]string{"relay_list": "RELAYS"}}
	cmds := NewCommandService(q)
	cmds.SetSubscribers(store.subscribers) // 模拟 pollOnce 的订阅者刷新

	w := NewWorker(store, bot, cmds, zapNop())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// 直接测试 handleUpdate 授权路径
	w.handleUpdate(ctx, Update{UpdateID: 1, ChatID: 7, Text: "/relay"})
	w.handleUpdate(ctx, Update{UpdateID: 2, ChatID: 999, Text: "/relay"})

	if len(bot.sent[7]) != 1 || bot.sent[7][0] != "RELAYS" {
		t.Fatalf("authorized chat replies = %v", bot.sent[7])
	}
	if len(bot.sent[999]) != 1 || bot.sent[999][0] != unauthorized {
		t.Fatalf("unauthorized chat replies = %v", bot.sent[999])
	}
}

func TestWorkerFailureDoesNotStopOthers(t *testing.T) {
	store := &fakeConfigStore{
		cfg: Config{Enabled: true},
		subscribers: []Subscriber{
			{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
		},
	}
	bot := &fakeBotClient{}
	q := &fakeQueryService{results: map[string]string{}}
	w := NewWorker(store, bot, NewCommandService(q), zapNop())
	ctx := context.Background()

	// 首次失败（连接错误），继续处理下一条
	bot.sendErr = errors.New("network down")
	if _, err := w.sendToSubscriber(ctx, 7, "first", nil); err == nil {
		t.Fatal("expected send error")
	}
	bot.sendErr = nil
	if _, err := w.sendToSubscriber(ctx, 7, "second", nil); err != nil {
		t.Fatalf("second send after failure: %v", err)
	}
	if len(bot.sent[7]) != 1 {
		t.Fatalf("sent = %v", bot.sent[7])
	}
}

func TestWorkerReportIdempotentPerWindow(t *testing.T) {
	store := &fakeConfigStore{
		cfg: Config{Enabled: true, ReportEnabled: true, ReportMinute: 0},
		subscribers: []Subscriber{
			{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
		},
	}
	bot := &fakeBotClient{}
	w := NewWorker(store, bot, nil, zapNop())

	ctx := context.Background()
	start := time.Now().Add(-time.Hour).Truncate(time.Hour)
	end := start.Add(time.Hour)

	// 第一次发送成功并记录
	w.sendReportWindow(ctx, start, end)
	if len(bot.sent[7]) != 1 {
		t.Fatalf("first window sends = %v", bot.sent[7])
	}
	// 同窗口重试：已有成功记录 → 跳过
	w.sendReportWindow(ctx, start, end)
	if len(bot.sent[7]) != 1 {
		t.Fatalf("duplicate window send detected: %v", bot.sent[7])
	}
}

// ===== 回调 / 打字状态 / 测试进度流 =====

func TestWorkerTypingActionOnQueryCommands(t *testing.T) {
	store := &fakeConfigStore{
		cfg: Config{Enabled: true},
		subscribers: []Subscriber{
			{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
		},
	}
	bot := &fakeBotClient{}
	q := &fakeQueryService{results: map[string]string{"relay_list": "RELAYS"}}
	cmds := NewCommandService(q)
	cmds.SetSubscribers(store.subscribers)
	w := NewWorker(store, bot, cmds, zapNop())

	_ = w.handleUpdate(context.Background(), Update{UpdateID: 1, ChatID: 7, Text: "/relay"})
	if len(bot.actions) != 1 || bot.actions[0] != ChatActionTyping {
		t.Fatalf("actions = %v, want [typing]", bot.actions)
	}
	// 帮助命令不触发打字状态
	_ = w.handleUpdate(context.Background(), Update{UpdateID: 2, ChatID: 7, Text: "/help"})
	if len(bot.actions) != 1 {
		t.Fatalf("actions = %v, want no extra typing for /help", bot.actions)
	}
}

func TestWorkerCallbackAnswersAndEdits(t *testing.T) {
	store := &fakeConfigStore{
		cfg: Config{Enabled: true},
		subscribers: []Subscriber{
			{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
		},
	}
	bot := &fakeBotClient{}
	q := &fakeQueryService{results: map[string]string{"relay_detail": "DETAIL"}}
	cmds := NewCommandService(q)
	cmds.SetSubscribers(store.subscribers)
	cmds.SetCallbackResponder(nil) // 让 Worker 提供实现
	cmds.SetSubscribers(store.subscribers)
	w := NewWorker(store, bot, cmds, zapNop())
	cmds.SetCallbackResponder(w) // Worker 实现 EditCallbackMessage

	err := w.handleUpdate(context.Background(), Update{
		UpdateID: 10, HasCallback: true, CallbackID: "cb-1",
		CallbackData: "cmd:/relay:19", CallbackChatID: 7, CallbackMessageID: 42,
	})
	if err != nil {
		t.Fatalf("handleUpdate: %v", err)
	}
	if len(bot.callbacks) != 1 || bot.callbacks[0] != "cb-1" {
		t.Fatalf("callbacks = %v, want [cb-1]", bot.callbacks)
	}
	if len(bot.edited[42]) != 1 || bot.edited[42][0] != "DETAIL" {
		t.Fatalf("edited = %v, want message 42 edited to DETAIL", bot.edited)
	}
}

func TestWorkerCallbackFallsBackToNewMessage(t *testing.T) {
	store := &fakeConfigStore{
		cfg: Config{Enabled: true},
		subscribers: []Subscriber{
			{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
		},
	}
	bot := &fakeBotClient{}
	q := &fakeQueryService{results: map[string]string{"relay_detail": "DETAIL"}}
	cmds := NewCommandService(q)
	cmds.SetSubscribers(store.subscribers)
	w := NewWorker(store, bot, cmds, zapNop())
	cmds.SetCallbackResponder(w)
	bot.editErr = errors.New("edit failed")

	err := w.handleUpdate(context.Background(), Update{
		UpdateID: 10, HasCallback: true, CallbackID: "cb-1",
		CallbackData: "cmd:/relay:19", CallbackChatID: 7, CallbackMessageID: 42,
	})
	if err != nil {
		t.Fatalf("handleUpdate: %v", err)
	}
	if len(bot.sent[7]) != 1 || bot.sent[7][0] != "DETAIL" {
		t.Fatalf("fallback message = %v", bot.sent[7])
	}
}

func TestWorkerStartTestEditsInPlace(t *testing.T) {
	store := &fakeConfigStore{
		cfg: Config{Enabled: true},
		subscribers: []Subscriber{
			{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
		},
	}
	bot := &fakeBotClient{}
	runner := &fakeSiteTestRunner{runMsg: "FINAL-RESULT"}
	cmds := NewCommandService(&fakeQueryService{results: map[string]string{}})
	cmds.SetSiteTestRunner(runner)
	cmds.SetAsyncSender(nil)
	w := NewWorker(store, bot, cmds, zapNop())
	cmds.SetAsyncSender(w)
	cmds.SetSubscribers(store.subscribers)

	tc := &StartTestContext{
		ChatID: 7, ChannelID: 19, ChannelName: "247-claudemax",
		Model: "claude-opus-4-8", UpstreamModel: "claude-opus-4-8",
		MaxTokens: 64,
		Sub:       Subscriber{ID: 1, ChatID: "7", Enabled: true, QueryEnabled: true},
		Runner:    runner,
	}
	if err := w.StartTest(context.Background(), tc); err != nil {
		t.Fatalf("StartTest: %v", err)
	}
	if len(bot.sent[7]) != 1 || !strings.Contains(bot.sent[7][0], "进行中") {
		t.Fatalf("progress message = %v", bot.sent[7])
	}
	// 结果原位编辑：进度消息 ID = 第一条发送的消息 ID（异步 goroutine，轮询等待）
	deadline := time.Now().Add(5 * time.Second)
	for {
		if contains(bot.edited[1], "FINAL-RESULT") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("edited = %v, want final result in message 1", bot.edited)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkerStartTestSendFailurePropagates(t *testing.T) {
	store := &fakeConfigStore{cfg: Config{Enabled: true}}
	bot := &fakeBotClient{sendErr: errors.New("network")}
	w := NewWorker(store, bot, nil, zapNop())
	tc := &StartTestContext{ChatID: 7, ChannelID: 19, ChannelName: "x", Model: "m", Runner: &fakeSiteTestRunner{runMsg: "R"}}
	if err := w.StartTest(context.Background(), tc); err == nil {
		t.Fatal("StartTest 发送失败必须返回错误")
	}
}

func contains(list []string, substr string) bool {
	for _, s := range list {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
