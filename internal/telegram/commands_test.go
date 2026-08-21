package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// fakeQueryService 记录调用并返回固定文本。
type fakeQueryService struct {
	lastMethod string
	lastID     int
	lastGroups []int
	results    map[string]string
	summaries  []RelaySummary
}

func (f *fakeQueryService) RelayList(ctx context.Context, groupIDs []int) (string, error) {
	f.lastMethod, f.lastGroups = "relay_list", groupIDs
	return f.results["relay_list"], nil
}
func (f *fakeQueryService) RelaySummaries(ctx context.Context, groupIDs []int) ([]RelaySummary, error) {
	f.lastMethod, f.lastGroups = "relay_summaries", groupIDs
	return f.summaries, nil
}
func (f *fakeQueryService) RelayDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "relay_detail", id, groupIDs
	return f.results["relay_detail"], nil
}
func (f *fakeQueryService) BalanceList(ctx context.Context, groupIDs []int) (string, error) {
	f.lastMethod, f.lastGroups = "balance_list", groupIDs
	return f.results["balance_list"], nil
}
func (f *fakeQueryService) BalanceDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "balance_detail", id, groupIDs
	return f.results["balance_detail"], nil
}
func (f *fakeQueryService) HealthList(ctx context.Context, groupIDs []int) (string, error) {
	f.lastMethod, f.lastGroups = "health_list", groupIDs
	return f.results["health_list"], nil
}
func (f *fakeQueryService) HealthDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "health_detail", id, groupIDs
	return f.results["health_detail"], nil
}
func (f *fakeQueryService) RatioList(ctx context.Context, groupIDs []int) (string, error) {
	f.lastMethod, f.lastGroups = "ratio_list", groupIDs
	return f.results["ratio_list"], nil
}
func (f *fakeQueryService) RatioDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "ratio_detail", id, groupIDs
	return f.results["ratio_detail"], nil
}
func (f *fakeQueryService) QualityLatest(ctx context.Context, channelID int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "quality_latest", channelID, groupIDs
	return f.results["quality_latest"], nil
}

const unauthorizedText = "⛔ 当前 Chat ID 未授权，请联系管理员。"

// fakeSiteTestRunner 记录调用并返回固定结果。
type fakeSiteTestRunner struct {
	preflightErr error
	runMsg       string
	runErr       error

	lastID        int
	lastModel     string
	lastMaxTokens int
	lastGroups    []int
	runCalled     chan struct{} // Run 被调用时关闭（异步测试用）
}

func (f *fakeSiteTestRunner) Preflight(ctx context.Context, channelID int, model string, groupIDs []int) (string, string, string, error) {
	f.lastID, f.lastModel, f.lastGroups = channelID, model, groupIDs
	if f.preflightErr != nil {
		return "", "", "", f.preflightErr
	}
	return "测试站", model, "upstream-model", nil
}

func (f *fakeSiteTestRunner) Run(ctx context.Context, channelID int, model string, maxTokens int, groupIDs []int) (string, error) {
	f.lastID, f.lastModel, f.lastMaxTokens, f.lastGroups = channelID, model, maxTokens, groupIDs
	if f.runCalled != nil {
		close(f.runCalled)
	}
	if f.runErr != nil {
		return "", f.runErr
	}
	return f.runMsg, nil
}

// fakeAsyncSender 记录异步发送内容；StartTest 同步执行 Run（模拟 Worker 流程）。
type fakeAsyncSender struct {
	mu       sync.Mutex
	msgs     []string
	starts   []*StartTestContext
	startErr error
}

func (f *fakeAsyncSender) SendToChat(ctx context.Context, chatID int64, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, html)
	return nil
}

func (f *fakeAsyncSender) StartTest(ctx context.Context, t *StartTestContext) error {
	f.mu.Lock()
	f.starts = append(f.starts, t)
	f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	// 模拟 Worker 的 runTestFlow（测试路径同步执行，避免真实 goroutine 时序）
	msg, err := t.Runner.Run(ctx, t.ChannelID, t.Model, t.MaxTokens, t.Sub.GroupIDs)
	if err != nil {
		msg = "🧪 站点测试失败：" + err.Error()
	}
	f.mu.Lock()
	f.msgs = append(f.msgs, msg)
	f.mu.Unlock()
	return nil
}

func (f *fakeAsyncSender) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.msgs, "\n")
}

func (f *fakeAsyncSender) lastStart() *StartTestContext {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) == 0 {
		return nil
	}
	return f.starts[len(f.starts)-1]
}

// fakeResponder 记录原位编辑调用（CallbackResponder 假实现）。
type fakeResponder struct {
	lastChatID int64
	lastMsgID  int64
	lastText   string
	lastKB     *InlineKeyboard
	editErr    error
}

func (f *fakeResponder) EditCallbackMessage(ctx context.Context, u Update, html string, kb *InlineKeyboard) error {
	if f.editErr != nil {
		return f.editErr
	}
	f.lastChatID = u.CallbackChatID
	f.lastMsgID = u.CallbackMessageID
	f.lastText = html
	f.lastKB = kb
	return nil
}

func authorizedSub(chatID string) Subscriber {
	return Subscriber{ID: 1, ChatID: chatID, Enabled: true, AlertEnabled: true, QueryEnabled: true}
}

// TestSiteTestCommand tests
func TestSiteTestNotConfigured(t *testing.T) {
	svc := NewCommandService(&fakeQueryService{results: map[string]string{}})
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, _ := svc.Handle(context.Background(), 7, "/sitetest 5")
	if !strings.Contains(out, "暂不可用") {
		t.Fatalf("out = %q", out)
	}
}

func TestSiteTestUsage(t *testing.T) {
	svc := NewCommandService(&fakeQueryService{results: map[string]string{}})
	svc.SetSiteTestRunner(&fakeSiteTestRunner{})
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, _ := svc.Handle(context.Background(), 7, "/sitetest")
	if !strings.Contains(out, "用法") {
		t.Fatalf("out = %q", out)
	}
}

func TestSiteTestBadArgs(t *testing.T) {
	svc := NewCommandService(&fakeQueryService{results: map[string]string{}})
	runner := &fakeSiteTestRunner{}
	svc.SetSiteTestRunner(runner)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})

	out, _ := svc.Handle(context.Background(), 7, "/sitetest 5 -1")
	if !strings.Contains(out, "max_tokens 必须为正整数") {
		t.Fatalf("negative tokens: %q", out)
	}
	out, _ = svc.Handle(context.Background(), 7, "/sitetest 5 16 32")
	if !strings.Contains(out, "重复指定") {
		t.Fatalf("dup tokens: %q", out)
	}
	out, _ = svc.Handle(context.Background(), 7, "/sitetest 5 a b c")
	if !strings.Contains(out, "多余参数") {
		t.Fatalf("extra args: %q", out)
	}
	if runner.lastID != 0 {
		t.Fatalf("runner called despite bad args: %+v", runner.lastID)
	}
}

func TestSiteTestPreflightFailure(t *testing.T) {
	svc := NewCommandService(&fakeQueryService{results: map[string]string{}})
	svc.SetSiteTestRunner(&fakeSiteTestRunner{preflightErr: context.DeadlineExceeded})
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, _ := svc.Handle(context.Background(), 7, "/sitetest 5 claude-opus-4-8")
	if !strings.Contains(out, "🧪 无法测试") {
		t.Fatalf("out = %q", out)
	}
}

func TestSiteTestSyncPath(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	runner := &fakeSiteTestRunner{runMsg: "RESULT-HTML"}
	svc.SetSiteTestRunner(runner)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})

	// 无异步发送器 → 同步返回 Run 结果
	out, _ := svc.Handle(context.Background(), 7, "/sitetest 5 claude-opus-4-8 64")
	if out != "RESULT-HTML" {
		t.Fatalf("out = %q", out)
	}
	if runner.lastID != 5 || runner.lastModel != "claude-opus-4-8" || runner.lastMaxTokens != 64 {
		t.Fatalf("runner args = %d/%q/%d", runner.lastID, runner.lastModel, runner.lastMaxTokens)
	}
	if len(runner.lastGroups) != 0 {
		t.Fatalf("groups = %v, want empty (all)", runner.lastGroups)
	}
}

func TestSiteTestAsyncPath(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	runner := &fakeSiteTestRunner{runMsg: "ASYNC-RESULT", runCalled: make(chan struct{})}
	sender := &fakeAsyncSender{}
	svc.SetSiteTestRunner(runner)
	svc.SetAsyncSender(sender)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true, GroupIDs: []int{6}},
	})

	// 有异步发送器 → 无直接回复（进度消息走 StartTest 通道）
	out, kb, err := svc.HandleWithKeyboard(context.Background(), 7, "/sitetest 19")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out != "" || kb != nil {
		t.Fatalf("out = %q kb=%v, want empty（异步通道处理）", out, kb)
	}

	st := sender.lastStart()
	if st == nil {
		t.Fatal("StartTest not called")
	}
	if st.ChannelID != 19 || st.ChatID != 7 || st.ChannelName != "测试站" || st.UpstreamModel != "upstream-model" {
		t.Fatalf("start ctx = %+v", st)
	}
	if st.MaxTokens != siteTestDefaultTokens {
		t.Fatalf("maxTokens = %d, want %d（默认）", st.MaxTokens, siteTestDefaultTokens)
	}
	if len(st.Sub.GroupIDs) != 1 || st.Sub.GroupIDs[0] != 6 {
		t.Fatalf("groups = %v, want [6]", st.Sub.GroupIDs)
	}
	if !strings.Contains(sender.all(), "ASYNC-RESULT") {
		t.Fatalf("result not delivered: %q", sender.all())
	}
	if runner.lastID != 19 {
		t.Fatalf("runner id = %d, want 19", runner.lastID)
	}
}

func TestSiteTestAsyncRunErrorStillDelivered(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	runner := &fakeSiteTestRunner{runErr: context.DeadlineExceeded, runCalled: make(chan struct{})}
	sender := &fakeAsyncSender{}
	svc.SetSiteTestRunner(runner)
	svc.SetAsyncSender(sender)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})

	_, _, err := svc.HandleWithKeyboard(context.Background(), 7, "/sitetest 5")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if sender.lastStart() == nil {
		t.Fatal("StartTest not called")
	}
	if !strings.Contains(sender.all(), "站点测试失败") {
		t.Fatalf("error result not delivered: %q", sender.all())
	}
}

func TestSiteTestStartTestSendErrorPropagates(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	runner := &fakeSiteTestRunner{runMsg: "R"}
	sender := &fakeAsyncSender{startErr: context.DeadlineExceeded}
	svc.SetSiteTestRunner(runner)
	svc.SetAsyncSender(sender)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})

	_, _, err := svc.HandleWithKeyboard(context.Background(), 7, "/sitetest 5")
	if err == nil {
		t.Fatal("StartTest 发送失败必须返回错误（worker 保留 offset 重试）")
	}
}

// ===== 回调与键盘测试 =====

func TestCallbackCmdRelayDispatches(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_detail": "DETAIL"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, err := svc.HandleCallback(context.Background(), Update{
		HasCallback:       true,
		CallbackData:      "cmd:/relay:19",
		CallbackChatID:    7,
		CallbackMessageID: 42,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out != "DETAIL" {
		t.Fatalf("out = %q", out)
	}
	if q.lastID != 19 {
		t.Fatalf("id = %d, want 19", q.lastID)
	}
}

func TestCallbackCmdRelayInPlaceEdit(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_detail": "DETAIL"}}
	svc := NewCommandService(q)
	resp := &fakeResponder{}
	svc.SetCallbackResponder(resp)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, err := svc.HandleCallback(context.Background(), Update{
		HasCallback:       true,
		CallbackData:      "cmd:/relay:19",
		CallbackChatID:    7,
		CallbackMessageID: 42,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty（原位编辑）", out)
	}
	if resp.lastMsgID != 42 || resp.lastText != "DETAIL" {
		t.Fatalf("responder = %+v", resp)
	}
	if resp.lastKB == nil || len(resp.lastKB.Rows) != 3 {
		t.Fatalf("keyboard not attached: %+v", resp.lastKB)
	}
}

func TestCallbackEditFailureFallsBackToNewMessage(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_detail": "DETAIL"}}
	svc := NewCommandService(q)
	svc.SetCallbackResponder(&fakeResponder{editErr: context.DeadlineExceeded})
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, err := svc.HandleCallback(context.Background(), Update{
		HasCallback: true, CallbackData: "cmd:/relay:19", CallbackChatID: 7,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out != "DETAIL" {
		t.Fatalf("out = %q, want fallback new message", out)
	}
}

func TestCallbackFilterCritical(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
	})
	// 注入告警查询捕获 criticalOnly
	captured := ""
	SetAlertsQuery(func(ctx context.Context, groupIDs []int, criticalOnly bool) (string, error) {
		if criticalOnly {
			captured = "critical"
		} else {
			captured = "all"
		}
		return "ALERTS", nil
	})
	defer SetAlertsQuery(nil)

	out, err := svc.HandleCallback(context.Background(), Update{
		HasCallback: true, CallbackData: "filter:critical", CallbackChatID: 7,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out != "ALERTS" || captured != "critical" {
		t.Fatalf("out = %q captured = %q", out, captured)
	}
	_, _ = svc.HandleCallback(context.Background(), Update{
		HasCallback: true, CallbackData: "filter:all", CallbackChatID: 7,
	})
	if captured != "all" {
		t.Fatalf("captured = %q, want all", captured)
	}
}

func TestCallbackStStartsTest(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	runner := &fakeSiteTestRunner{runMsg: "ST-RESULT"}
	sender := &fakeAsyncSender{}
	svc.SetSiteTestRunner(runner)
	svc.SetAsyncSender(sender)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})

	out, err := svc.HandleCallback(context.Background(), Update{
		HasCallback: true, CallbackData: "st:19:claude-opus-4-8:64", CallbackChatID: 7,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty（异步通道）", out)
	}
	st := sender.lastStart()
	if st == nil {
		t.Fatal("StartTest not called")
	}
	if st.ChannelID != 19 || st.Model != "claude-opus-4-8" || st.MaxTokens != 64 {
		t.Fatalf("start ctx = %+v", st)
	}
	if !strings.Contains(sender.all(), "ST-RESULT") {
		t.Fatalf("result missing: %q", sender.all())
	}
}

func TestCallbackUnauthorizedChat(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, err := svc.HandleCallback(context.Background(), Update{
		HasCallback: true, CallbackData: "cmd:/relay", CallbackChatID: 999,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
}

func TestHelpKeyboardByCapability(t *testing.T) {
	svc := NewCommandService(&fakeQueryService{results: map[string]string{}})
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
	})
	out, kb, err := svc.HandleWithKeyboard(context.Background(), 7, "/help")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "/sitetest") {
		t.Fatalf("help missing /sitetest:\n%s", out)
	}
	if kb == nil || len(kb.Rows) < 3 {
		t.Fatalf("help keyboard rows = %d, want >= 3", lenOrNil(kb))
	}
	// 仅告警订阅者：键盘只有告警行
	svc.SetSubscribers([]Subscriber{
		{ID: 2, ChatID: "8", Enabled: true, AlertEnabled: true, QueryEnabled: false},
	})
	_, kb2, _ := svc.HandleWithKeyboard(context.Background(), 8, "/help")
	if kb2 == nil || len(kb2.Rows) != 1 {
		t.Fatalf("alert-only keyboard rows = %d, want 1", lenOrNil(kb2))
	}
}

func lenOrNil(kb *InlineKeyboard) int {
	if kb == nil {
		return 0
	}
	return len(kb.Rows)
}

func TestRelayListAttachesStationKeyboard(t *testing.T) {
	q := &fakeQueryService{
		results: map[string]string{"relay_list": "RELAYS"},
		summaries: []RelaySummary{
			{ID: 19, Name: "247-claudemax"},
			{ID: 31, Name: "supe-claude-MAX"},
		},
	}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, kb, err := svc.HandleWithKeyboard(context.Background(), 7, "/relay")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "点击下方按钮") {
		t.Fatalf("out missing hint: %q", out)
	}
	if kb == nil || len(kb.Rows) != 1 || len(kb.Rows[0]) != 2 {
		t.Fatalf("keyboard = %+v", kb)
	}
	if kb.Rows[0][0].Data != "cmd:/relay:19" || kb.Rows[0][1].Data != "cmd:/relay:31" {
		t.Fatalf("buttons = %+v", kb.Rows[0])
	}
}

func TestStDataRoundTrip(t *testing.T) {
	d := stData(19, "claude-opus-4-8", 64)
	id, model, tokens, usageErr := parseStData(d)
	if usageErr != "" || id != 19 || model != "claude-opus-4-8" || tokens != 64 {
		t.Fatalf("roundtrip = %d/%q/%d/%q", id, model, tokens, usageErr)
	}
	// 无模型无 tokens
	id, model, tokens, _ = parseStData(stData(5, "", 0))
	if id != 5 || model != "" || tokens != 0 {
		t.Fatalf("bare = %d/%q/%d", id, model, tokens)
	}
	// 超长模型省略（回调上限 64 字节）
	long := "very-long-model-name-exceeding-64-bytes-callback-limit"
	d = stData(5, long, 0)
	if len(d) > 64 {
		t.Fatalf("data len = %d, want <= 64", len(d))
	}
	if strings.Contains(d, long) {
		t.Fatal("long model must be omitted from callback data")
	}
}

func TestSyncBotMenu(t *testing.T) {
	client := &fakeBotClient{}
	svc := NewCommandService(&fakeQueryService{results: map[string]string{}})

	// 全能力订阅者 → 全菜单
	subs := []Subscriber{{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true}}
	if err := svc.SyncBotMenu(context.Background(), client, subs); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(client.menuCmds) != 11 {
		t.Fatalf("menu len = %d, want 11", len(client.menuCmds))
	}
	// 能力未变 → 不重复调用
	client.menuCmds = nil
	if err := svc.SyncBotMenu(context.Background(), client, subs); err != nil {
		t.Fatalf("sync2: %v", err)
	}
	if client.menuCmds != nil {
		t.Fatal("menu re-synced despite unchanged capabilities")
	}
	// 能力变化 → 重新同步（仅查询，无告警）
	client.menuCmds = nil
	subs = []Subscriber{{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: false, QueryEnabled: true}}
	if err := svc.SyncBotMenu(context.Background(), client, subs); err != nil {
		t.Fatalf("sync3: %v", err)
	}
	for _, c := range client.menuCmds {
		if c.Command == "/alerts" || c.Command == "/alert" {
			t.Fatalf("menu should exclude alerts: %+v", client.menuCmds)
		}
	}
}

func TestSiteTestHelpTextUpdated(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{authorizedSub("7")})
	out, _ := svc.Handle(context.Background(), 7, "/help")
	if !strings.Contains(out, "/sitetest") {
		t.Fatalf("help missing /sitetest:\n%s", out)
	}
}

func TestUnknownChatIDRejected(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	out, err := svc.Handle(context.Background(), 999, "/alerts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
	if q.lastMethod != "" {
		t.Fatalf("query executed for unauthorized chat: %s", q.lastMethod)
	}
}

func TestDisabledSubscriberRejected(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: false, AlertEnabled: true, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "/alerts")
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
}

func TestQueryDisabledRejectsQueries(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: false},
	})
	out, _ := svc.Handle(context.Background(), 7, "/relay")
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
}

func TestEmptyGroupIDsCanSeeAll(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_list": "ALL"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true, GroupIDs: []int{}},
	})
	out, _ := svc.Handle(context.Background(), 7, "/relay")
	if out != "ALL" {
		t.Fatalf("out = %q", out)
	}
	if len(q.lastGroups) != 0 {
		t.Fatalf("groups = %v, want empty (all)", q.lastGroups)
	}
}

func TestBoundGroupsPassedToQuery(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_list": "FILTERED"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true, GroupIDs: []int{2}},
	})
	out, _ := svc.Handle(context.Background(), 7, "/relay")
	if out != "FILTERED" {
		t.Fatalf("out = %q", out)
	}
	if len(q.lastGroups) != 1 || q.lastGroups[0] != 2 {
		t.Fatalf("groups = %v, want [2]", q.lastGroups)
	}
}

func TestCommandWithBotnameSuffix(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_detail": "DETAIL"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "/relay@sr_bot 3")
	if out != "DETAIL" {
		t.Fatalf("out = %q", out)
	}
	if q.lastID != 3 {
		t.Fatalf("id = %d, want 3", q.lastID)
	}
}

func TestEmptyArgCommandsReturnHelp(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "")
	if !strings.Contains(out, "/alerts") || !strings.Contains(out, "/relay") {
		t.Fatalf("help missing commands: %s", out)
	}
	out, _ = svc.Handle(context.Background(), 7, "/help")
	if !strings.Contains(out, "/balance") {
		t.Fatalf("help missing balance: %s", out)
	}
}

func TestUnknownCommandReturnsHelp(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "/frobnicate 5")
	// 未知命令返回帮助，不伪造结果
	if !strings.Contains(out, "/alerts") {
		t.Fatalf("unknown command should return help: %s", out)
	}
}

func TestQualityCommandRoutesToQuery(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"quality_latest": "QUALITY"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: true, QueryEnabled: true, GroupIDs: []int{2}},
	})
	out, _ := svc.Handle(context.Background(), 7, "/quality 5")
	if out != "QUALITY" {
		t.Fatalf("out = %q", out)
	}
	if q.lastID != 5 || len(q.lastGroups) != 1 || q.lastGroups[0] != 2 {
		t.Fatalf("id=%d groups=%v, want 5/[2]", q.lastID, q.lastGroups)
	}
	// 无参数 → 用法提示
	out, _ = svc.Handle(context.Background(), 7, "/quality")
	if !strings.Contains(out, "用法") {
		t.Fatalf("empty args should return usage: %s", out)
	}
}

func TestAlertCommandRequiresAlertEnabled(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"alerts": "ALERTS"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: "7", Enabled: true, AlertEnabled: false, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "/alerts")
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
}
