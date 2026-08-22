package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// QueryService 中转站只读查询接口（Web 与 Telegram 同数据口径）。
type QueryService interface {
	RelayList(ctx context.Context, groupIDs []int) (string, error)
	RelaySummaries(ctx context.Context, groupIDs []int) ([]RelaySummary, error)
	RelayDetail(ctx context.Context, id int, groupIDs []int) (string, error)
	BalanceList(ctx context.Context, groupIDs []int) (string, error)
	BalanceDetail(ctx context.Context, id int, groupIDs []int) (string, error)
	HealthList(ctx context.Context, groupIDs []int) (string, error)
	HealthDetail(ctx context.Context, id int, groupIDs []int) (string, error)
	RatioList(ctx context.Context, groupIDs []int) (string, error)
	RatioDetail(ctx context.Context, id int, groupIDs []int) (string, error)
	QualityLatest(ctx context.Context, channelID int, groupIDs []int) (string, error)
}

// SiteTestExecutor 站点测试执行器接口（实现见 sitetest.go；nil = 命令不可用）。
// 返回的字符串为格式化好的 Telegram HTML 消息。
type SiteTestExecutor interface {
	Preflight(ctx context.Context, channelID int, model string, groupIDs []int) (name, resolvedModel, upstreamModel string, err error)
	Run(ctx context.Context, channelID int, model string, maxTokens int, groupIDs []int) (string, error)
	// ListModels 列出站点映射中的模型 key（排序后），供向导点选模型。
	ListModels(ctx context.Context, channelID int, groupIDs []int) ([]string, error)
}

// AsyncWork 后台异步任务：返回要推送的最终文本（错误时由调用方包装为失败提示）。
type AsyncWork func(ctx context.Context) (string, error)

// AsyncSender 异步回复发送器：长任务（站点测试/余额刷新）的进度消息与结果推送。
// 生产实现为 Worker；nil = 命令同步返回结果（测试/退化路径）。
type AsyncSender interface {
	SendToChat(ctx context.Context, chatID int64, html string) error
	StartTest(ctx context.Context, t *StartTestContext) error
	// RunAsync 发送进度消息 → 后台执行 work → 原位编辑为结果（编辑失败降级新发）。
	RunAsync(ctx context.Context, chatID int64, progress string, work AsyncWork) error
}

// CallbackResponder 回调处理期间的原位编辑能力（Worker 实现）。
type CallbackResponder interface {
	EditCallbackMessage(ctx context.Context, u Update, html string, kb *InlineKeyboard) error
}

// StartTestContext 一次站点测试的交互状态（Worker 组装执行流）。
type StartTestContext struct {
	ChatID        int64
	ChannelID     int
	ChannelName   string
	Model         string // 解析后的模型（站点映射键）
	UpstreamModel string // 上游侧模型名
	MaxTokens     int
	Sub           Subscriber
	Runner        SiteTestExecutor
	ProgressMsg   int64 // 进度消息 ID（原位编辑用）
}

// StartText 测试开始时的进度消息文本。
func (t *StartTestContext) StartText() string {
	return fmt.Sprintf("🧪 <b>站点测试进行中</b>\n站点：%s\n模型：%s · max_tokens：%d\n⏱ 非流式+流式各一次真实推理，完成后自动更新本消息。",
		EscapeHTML(t.ChannelName), EscapeHTML(t.UpstreamModel), t.MaxTokens)
}

// ProgressText 运行中的心跳文本（每次编辑内容必须变化，否则 Telegram 拒绝编辑）。
func (t *StartTestContext) ProgressText(elapsed time.Duration) string {
	return fmt.Sprintf("🧪 <b>站点测试进行中</b>\n站点：%s\n模型：%s\n⏱ 已运行：%s\n完成后自动更新本消息…",
		EscapeHTML(t.ChannelName), EscapeHTML(t.UpstreamModel), elapsed.Round(time.Second))
}

// RetryKeyboard 结果消息的「再测一次」键盘。
func (t *StartTestContext) RetryKeyboard() *InlineKeyboard {
	return &InlineKeyboard{Rows: [][]InlineButton{
		{{Text: "🔁 再测一次", Data: stData(t.ChannelID, t.Model, t.MaxTokens)}},
	}}
}

// CommandService 解析命令、校验授权并分发查询。
type CommandService struct {
	query            QueryService
	subscribers      map[int64]Subscriber
	siteTest         SiteTestExecutor
	balanceRefresher *BalanceRefresher
	sender           AsyncSender
	responder        CallbackResponder
	logger           *zap.Logger

	// 站点测试向导会话（按 chat_id 隔离；仅 poller owner 单实例处理，内存态即可）
	wizardMu sync.Mutex
	wizards  map[int64]*wizardSession

	// 命令菜单（setMyCommands）上次同步的订阅者能力状态
	menuAlerts bool
	menuQuery  bool
}

// NewCommandService 创建命令服务。
func NewCommandService(q QueryService) *CommandService {
	return &CommandService{query: q, subscribers: map[int64]Subscriber{}, logger: zap.NewNop(), wizards: map[int64]*wizardSession{}}
}

// SetSubscribers 刷新授权订阅者（Worker 每次处理命令前从 DB 重新加载）。
// 键为 chat_id 数值：Chat ID 以数值判定会话身份，存储字符串的前导零（如 00123）
// 不影响授权匹配。
func (c *CommandService) SetSubscribers(subs []Subscriber) {
	m := make(map[int64]Subscriber, len(subs))
	for _, s := range subs {
		chatNum, err := ParseChatID(s.ChatID)
		if err != nil {
			continue // 非法存储值跳过（不应存在：API 写入时已校验）
		}
		m[chatNum] = s
	}
	c.subscribers = m
}

// SetSiteTestRunner 注入站点测试执行器（checker 装配时注入；nil = /sitetest 不可用）。
func (c *CommandService) SetSiteTestRunner(r SiteTestExecutor) { c.siteTest = r }

// SetBalanceRefresher 注入余额实时刷新器（checker 装配时注入；
// nil 或未注入 sender 时 /balance 退化为只读缓存查询）。
func (c *CommandService) SetBalanceRefresher(r *BalanceRefresher) { c.balanceRefresher = r }

// SetAsyncSender 注入异步发送器；nil 时 /sitetest 同步返回结果。
func (c *CommandService) SetAsyncSender(s AsyncSender) { c.sender = s }

// SetCallbackResponder 注入回调原位编辑实现；nil 时回调结果以新消息回复。
func (c *CommandService) SetCallbackResponder(r CallbackResponder) { c.responder = r }

// SetLogger 注入日志器（异步路径错误记录用；nil 回退 Nop）。
func (c *CommandService) SetLogger(l *zap.Logger) {
	if l != nil {
		c.logger = l
	}
}

const helpText = `📋 <b>可用命令</b>
━━━━━━━━━━━━
📡 <code>/relay [id]</code> — 站点列表 / 详情
💰 <code>/balance [id]</code> — 中转站账户余额
🩺 <code>/health [id]</code> — 健康状态
📐 <code>/ratio [id]</code> — 实测倍率
🚨 <code>/alerts [critical]</code> — 活跃告警
🔍 <code>/alert &lt;key&gt;</code> — 单条告警详情
🛰 <code>/status</code> — 系统状态摘要
🧪 <code>/quality &lt;id&gt;</code> — 最近一次质量检测
🚀 <code>/sitetest</code> — 站点直达测试（点选引导，少量费用，异步推送）
━━━━━━━━━━━━
💡 也可以点下方按钮快速查看`

// unauthorized 统一拒绝文案（未授权 Chat ID / 停用订阅者 / 无查询权限）。
const unauthorized = "⛔ 当前 Chat ID 未授权，请联系管理员。"

// Handle 处理一条用户消息，返回回复文本（兼容入口：丢弃内联键盘）。
func (c *CommandService) Handle(ctx context.Context, chatID int64, text string) (string, error) {
	out, _, err := c.HandleWithKeyboard(ctx, chatID, text)
	return out, err
}

// HandleWithKeyboard 处理一条用户消息，返回回复文本与可选内联键盘。
// 授权链：订阅者存在 → enabled → （命令属查询类还需 query_enabled）。
// 返回空文本表示无需回复（如 /sitetest 已通过异步通道发送进度消息）。
func (c *CommandService) HandleWithKeyboard(ctx context.Context, chatID int64, text string) (string, *InlineKeyboard, error) {
	sub, ok := c.subscribers[chatID]
	if !ok || !sub.Enabled {
		return unauthorized, nil, nil
	}

	cmd, args := parseCommand(text)

	// 帮助类命令无需 query_enabled
	switch cmd {
	case "/start", "/help", "":
		return helpText, helpKeyboard(sub), nil
	}

	if !sub.QueryEnabled {
		return unauthorized, nil, nil
	}

	return c.dispatch(ctx, chatID, sub, cmd, args)
}

// dispatch 命令分发（消息与回调共用）。
func (c *CommandService) dispatch(ctx context.Context, chatID int64, sub Subscriber, cmd string, args []string) (string, *InlineKeyboard, error) {
	switch cmd {
	case "/alerts":
		if !sub.AlertEnabled {
			return unauthorized, nil, nil
		}
		out, err := c.handleAlerts(ctx, sub, args)
		return out, alertsKeyboard(), err
	case "/alert":
		if !sub.AlertEnabled {
			return unauthorized, nil, nil
		}
		out, err := c.handleAlertDetail(ctx, sub, args)
		return out, nil, err
	case "/status":
		out, err := c.handleStatus(ctx, sub)
		return out, quickKeyboard(), err
	case "/relay":
		if len(args) > 0 {
			id, err := parseID(args[0])
			if err != nil {
				return helpText, helpKeyboard(sub), nil
			}
			out, err := c.query.RelayDetail(ctx, id, sub.GroupIDs)
			return out, relayDetailKeyboard(id), err
		}
		out, err := c.query.RelayList(ctx, sub.GroupIDs)
		var kb *InlineKeyboard
		if err == nil {
			if items, serr := c.query.RelaySummaries(ctx, sub.GroupIDs); serr == nil {
				if kb = relayListKeyboard(items); kb != nil {
					out += "\n💡 点击下方按钮直达站点详情"
				}
			}
		}
		return out, kb, err
	case "/balance":
		if len(args) > 0 {
			id, err := parseID(args[0])
			if err != nil {
				return helpText, helpKeyboard(sub), nil
			}
			out, err := c.query.BalanceDetail(ctx, id, sub.GroupIDs)
			return out, backToRelayKeyboard(id), err
		}
		// 无参数：实时刷新。有 sender 且有刷新器 → 异步探测并原位更新；
		// 否则退化为只读缓存查询（测试/退化路径）。
		if c.balanceRefresher != nil && c.sender != nil {
			if err := c.sender.RunAsync(ctx, chatID, "💰 正在实时刷新中转站余额…", func(wctx context.Context) (string, error) {
				return c.balanceRefresher.Refresh(wctx, sub.GroupIDs)
			}); err != nil {
				return "", nil, err // 发送失败：返回错误让 worker 保留 offset 重试
			}
			return "", nil, nil // 结果由 RunAsync 异步推送
		}
		out, err := c.query.BalanceList(ctx, sub.GroupIDs)
		return out, nil, err
	case "/health":
		if len(args) > 0 {
			id, err := parseID(args[0])
			if err != nil {
				return helpText, helpKeyboard(sub), nil
			}
			out, err := c.query.HealthDetail(ctx, id, sub.GroupIDs)
			return out, backToRelayKeyboard(id), err
		}
		out, err := c.query.HealthList(ctx, sub.GroupIDs)
		return out, nil, err
	case "/ratio":
		if len(args) > 0 {
			id, err := parseID(args[0])
			if err != nil {
				return helpText, helpKeyboard(sub), nil
			}
			out, err := c.query.RatioDetail(ctx, id, sub.GroupIDs)
			return out, backToRelayKeyboard(id), err
		}
		out, err := c.query.RatioList(ctx, sub.GroupIDs)
		return out, nil, err
	case "/quality":
		// 只读最近一次检测结果，不启动新任务（避免 Telegram 触发上游费用）
		if len(args) == 0 {
			return "用法：/quality &lt;channel_id&gt;", nil, nil
		}
		id, err := parseID(args[0])
		if err != nil {
			return helpText, helpKeyboard(sub), nil
		}
		out, err := c.query.QualityLatest(ctx, id, sub.GroupIDs)
		return out, backToRelayKeyboard(id), err
	case "/sitetest":
		return c.handleSiteTest(ctx, chatID, sub, args)
	default:
		// 未知命令返回帮助，不伪造结果
		return helpText, helpKeyboard(sub), nil
	}
}

// ===== 内联键盘回调 =====

// HandleCallback 处理内联键盘回调。
// 返回 (需要新发送的回复文本, 内联键盘, error)：
//   - 文本空 = 已原位编辑或已通过异步通道处理；
//   - 键盘必须随文本一起返回——replyOrEdit 原位编辑失败回退为新发时，
//     键盘信息必须穿透到 Worker 发送层（此前丢失键盘导致向导无按钮）。
func (c *CommandService) HandleCallback(ctx context.Context, u Update) (string, *InlineKeyboard, error) {
	sub, ok := c.subscribers[u.CallbackChatID]
	if !ok || !sub.Enabled {
		return unauthorized, nil, nil
	}
	data := u.CallbackData

	switch {
	case strings.HasPrefix(data, "cmd:"):
		// cmd:/balance:19 → "/balance 19"（复用 parseCommand 与 dispatch）
		text := strings.TrimPrefix(data, "cmd:")
		if i := strings.Index(text, ":"); i > 0 {
			text = text[:i] + " " + text[i+1:]
		}
		cmd, args := parseCommand(text)
		if cmd == "" {
			return "", nil, nil
		}
		if cmd != "/start" && cmd != "/help" && !sub.QueryEnabled {
			return unauthorized, nil, nil
		}
		out, kb, err := c.dispatch(ctx, u.CallbackChatID, sub, cmd, args)
		if err != nil {
			return "", nil, err
		}
		return c.replyOrEdit(ctx, u, out, kb)
	case strings.HasPrefix(data, "filter:"):
		if !sub.QueryEnabled {
			return unauthorized, nil, nil
		}
		out, err := c.handleAlerts(ctx, sub, []string{strings.TrimPrefix(data, "filter:")})
		if err != nil {
			return "", nil, err
		}
		return c.replyOrEdit(ctx, u, out, alertsKeyboard())
	case strings.HasPrefix(data, "st:"):
		if !sub.QueryEnabled {
			return unauthorized, nil, nil
		}
		return c.handleSiteTestCallback(ctx, u, sub, data)
	case strings.HasPrefix(data, "wiz:"):
		if !sub.QueryEnabled {
			return unauthorized, nil, nil
		}
		return c.handleWizardCallback(ctx, u, sub, data)
	default:
		return "", nil, nil
	}
}

// replyOrEdit 优先原位编辑原消息（按钮就地响应）；编辑失败（消息过旧/被删）
// 回退为返回文本与键盘由 Worker 新发——键盘必须穿透，否则按钮丢失。
func (c *CommandService) replyOrEdit(ctx context.Context, u Update, html string, kb *InlineKeyboard) (string, *InlineKeyboard, error) {
	if c.responder != nil {
		if err := c.responder.EditCallbackMessage(ctx, u, html, kb); err == nil {
			return "", nil, nil
		}
		c.logger.Debug("Callback edit failed, falling back to new message",
			zap.Int64("chat_id", u.CallbackChatID))
	}
	return html, kb, nil
}

// handleSiteTestCallback st:<id>[:model][:tokens] 回调：
// 有异步发送器时启动进度流（新消息）；否则同步执行并原位编辑结果。
func (c *CommandService) handleSiteTestCallback(ctx context.Context, u Update, sub Subscriber, data string) (string, *InlineKeyboard, error) {
	if c.siteTest == nil {
		return "⚠️ 站点测试服务暂不可用，请稍后再试。", nil, nil
	}
	id, model, maxTokens, usageErr := parseStData(data)
	if usageErr != "" {
		return usageErr, nil, nil
	}
	t, err := c.startTestContext(ctx, u.CallbackChatID, sub, id, model, maxTokens)
	if err != nil {
		return "🧪 无法测试：" + err.Error(), nil, nil
	}
	if c.sender != nil {
		if serr := c.sender.StartTest(ctx, t); serr != nil {
			return "", nil, serr // 发送失败：返回错误让 worker 保留 offset 重试
		}
		return "", nil, nil // 进度消息由 StartTest 发送
	}
	msg, rerr := t.Runner.Run(ctx, t.ChannelID, t.Model, t.MaxTokens, sub.GroupIDs)
	if rerr != nil {
		return "🧪 站点测试失败：" + rerr.Error(), nil, nil
	}
	return c.replyOrEdit(ctx, u, msg, t.RetryKeyboard())
}

// ===== 站点测试引导式向导 =====

// wizardSession 一次引导式站点测试的会话状态（按 chat_id 隔离）。
type wizardSession struct {
	chatID    int64
	groupIDs  []int
	step      string // "station" → "model" → "tokens"
	stationID int
	model     string
}

// wizardTtl 向导会话超时：长时间不操作自动过期，避免残留状态拦截后续按钮。
const wizardTtl = 10 * time.Minute

// siteTestTokenChoices 向导可选 max_tokens（含默认 128）。
var siteTestTokenChoices = []int{64, 128, 256, 512}

// startSiteTestWizard 启动向导第一步：列出站点供点选。
func (c *CommandService) startSiteTestWizard(ctx context.Context, chatID int64, sub Subscriber) (string, *InlineKeyboard, error) {
	items, err := c.query.RelaySummaries(ctx, sub.GroupIDs)
	if err != nil {
		return "", nil, err
	}
	if len(items) == 0 {
		return "🧪 站点测试\n暂无可用站点，请先添加站点。", nil, nil
	}
	kb := wizardStationKeyboard(items)
	if kb == nil {
		return "🧪 站点测试\n暂无可选站点。", nil, nil
	}

	w := &wizardSession{chatID: chatID, groupIDs: sub.GroupIDs, step: "station"}
	c.wizardMu.Lock()
	c.wizards[chatID] = w
	c.wizardMu.Unlock()

	return "🧪 <b>站点测试</b>\n━━━━━━━━━━━━\n请选择要测试的站点：", kb, nil
}

// handleWizardCallback 处理向导各步骤回调。
func (c *CommandService) handleWizardCallback(ctx context.Context, u Update, sub Subscriber, data string) (string, *InlineKeyboard, error) {
	c.wizardMu.Lock()
	w, ok := c.wizards[u.CallbackChatID]
	c.wizardMu.Unlock()
	if !ok || w == nil {
		return "🧪 向导已过期，请重新发送 /sitetest。", nil, nil
	}
	w.groupIDs = sub.GroupIDs // 授权范围随当前订阅者刷新

	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return "🧪 无效的操作。", nil, nil
	}
	action := parts[1]

	switch action {
	case "station":
		return c.wizardPickStation(ctx, u, sub, w, parts)
	case "model":
		return c.wizardPickModel(ctx, u, sub, w, parts)
	case "tokens":
		return c.wizardPickTokens(ctx, u, sub, w, parts)
	case "back":
		return c.wizardBack(ctx, u, sub, w)
	case "cancel":
		c.wizardMu.Lock()
		delete(c.wizards, u.CallbackChatID)
		c.wizardMu.Unlock()
		return "已取消站点测试。", nil, nil
	default:
		return "🧪 无效的操作。", nil, nil
	}
}

// wizardPickStation 选择站点后，列出该站点模型供点选。
func (c *CommandService) wizardPickStation(ctx context.Context, u Update, sub Subscriber, w *wizardSession, parts []string) (string, *InlineKeyboard, error) {
	if len(parts) < 3 {
		return "🧪 无效的站点。", nil, nil
	}
	id, err := parseID(parts[2])
	if err != nil {
		return "🧪 无效的站点。", nil, nil
	}
	models, err := c.siteTest.ListModels(ctx, id, w.groupIDs)
	if err != nil {
		return "🧪 无法测试该站点：" + err.Error(), nil, nil
	}
	if len(models) == 0 {
		return "🧪 该站点没有已映射模型，无法测试。", nil, nil
	}
	w.stationID = id
	w.step = "model"

	name := ""
	if items, _ := c.query.RelaySummaries(ctx, w.groupIDs); items != nil {
		for _, it := range items {
			if it.ID == id {
				name = it.Name
				break
			}
		}
	}
	if name == "" {
		name = strconv.Itoa(id)
	}

	out := "🧪 <b>站点测试</b>\n站点：" + EscapeHTML(name) + "\n━━━━━━━━━━━━\n请选择测试模型："
	return c.replyOrEdit(ctx, u, out, wizardModelKeyboard(models))
}

// wizardPickModel 选择模型后，列出 tokens 供点选。
func (c *CommandService) wizardPickModel(ctx context.Context, u Update, sub Subscriber, w *wizardSession, parts []string) (string, *InlineKeyboard, error) {
	if len(parts) < 3 || parts[2] == "" {
		return "🧪 无效的模型。", nil, nil
	}
	w.model = parts[2]
	w.step = "tokens"

	out := "🧪 <b>站点测试</b>\n模型：" + EscapeHTML(w.model) + "\n━━━━━━━━━━━━\n请选择 max_tokens（生成长度）："
	return c.replyOrEdit(ctx, u, out, wizardTokenKeyboard())
}

// wizardPickTokens 选择 tokens 后，启动测试并结束向导。
func (c *CommandService) wizardPickTokens(ctx context.Context, u Update, sub Subscriber, w *wizardSession, parts []string) (string, *InlineKeyboard, error) {
	if len(parts) < 3 {
		return "🧪 无效的 tokens。", nil, nil
	}
	maxTokens, err := parseID(parts[2])
	if err != nil || maxTokens <= 0 {
		return "🧪 无效的 tokens。", nil, nil
	}

	c.wizardMu.Lock()
	delete(c.wizards, u.CallbackChatID)
	c.wizardMu.Unlock()

	t, err := c.startTestContext(ctx, u.CallbackChatID, sub, w.stationID, w.model, maxTokens)
	if err != nil {
		return "🧪 无法测试：" + err.Error(), nil, nil
	}
	if c.sender != nil {
		if serr := c.sender.StartTest(ctx, t); serr != nil {
			return "", nil, serr
		}
		return "", nil, nil
	}
	msg, rerr := t.Runner.Run(ctx, t.ChannelID, t.Model, t.MaxTokens, sub.GroupIDs)
	if rerr != nil {
		return "🧪 站点测试失败：" + rerr.Error(), nil, nil
	}
	return msg, nil, nil
}

// wizardBack 返回上一步（model 步返回站点列表，tokens 步返回模型列表）。
func (c *CommandService) wizardBack(ctx context.Context, u Update, sub Subscriber, w *wizardSession) (string, *InlineKeyboard, error) {
	switch w.step {
	case "model", "tokens":
		if w.step == "model" {
			// 返回站点选择
			items, err := c.query.RelaySummaries(ctx, w.groupIDs)
			if err != nil {
				return "", nil, err
			}
			w.step = "station"
			out := "🧪 <b>站点测试</b>\n━━━━━━━━━━━━\n请选择要测试的站点："
			return c.replyOrEdit(ctx, u, out, wizardStationKeyboard(items))
		}
		// 返回模型选择
		models, err := c.siteTest.ListModels(ctx, w.stationID, w.groupIDs)
		if err != nil {
			return "🧪 无法测试该站点：" + err.Error(), nil, nil
		}
		w.step = "model"
		out := "🧪 <b>站点测试</b>\n━━━━━━━━━━━━\n请选择测试模型："
		return c.replyOrEdit(ctx, u, out, wizardModelKeyboard(models))
	default:
		return "🧪 无效的操作。", nil, nil
	}
}

// wizardStationKeyboard 站点列表键盘（wiz:station 回调，每行 2 个）。
func wizardStationKeyboard(items []RelaySummary) *InlineKeyboard {
	if len(items) == 0 {
		return nil
	}
	if len(items) > 24 {
		items = items[:24]
	}
	rows := make([][]InlineButton, 0, (len(items)+1)/2)
	row := make([]InlineButton, 0, 2)
	for _, it := range items {
		row = append(row, InlineButton{Text: truncateButtonText(it.Name), Data: "wiz:station:" + strconv.Itoa(it.ID)})
		if len(row) == 2 {
			rows = append(rows, row)
			row = make([]InlineButton, 0, 2)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, InlineRow(InlineButton{Text: "❌ 取消", Data: "wiz:cancel"}))
	return &InlineKeyboard{Rows: rows}
}

// wizardModelKeyboard 模型列表键盘（wiz:model 回调，每行 2 个）。
func wizardModelKeyboard(models []string) *InlineKeyboard {
	rows := make([][]InlineButton, 0, (len(models)+1)/2)
	row := make([]InlineButton, 0, 2)
	for _, m := range models {
		row = append(row, InlineButton{Text: truncateButtonText(m), Data: "wiz:model:" + m})
		if len(row) == 2 {
			rows = append(rows, row)
			row = make([]InlineButton, 0, 2)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, InlineRow(InlineButton{Text: "← 返回站点", Data: "wiz:back"}, InlineButton{Text: "❌ 取消", Data: "wiz:cancel"}))
	return &InlineKeyboard{Rows: rows}
}

// wizardTokenKeyboard tokens 列表键盘（wiz:tokens 回调）。
func wizardTokenKeyboard() *InlineKeyboard {
	var row []InlineButton
	for _, n := range siteTestTokenChoices {
		row = append(row, InlineButton{Text: strconv.Itoa(n), Data: "wiz:tokens:" + strconv.Itoa(n)})
	}
	rows := [][]InlineButton{row, InlineRow(InlineButton{Text: "← 返回模型", Data: "wiz:back"}, InlineButton{Text: "❌ 取消", Data: "wiz:cancel"})}
	return &InlineKeyboard{Rows: rows}
}

// handleSiteTest /sitetest [id] [模型] [max_tokens]：
//   - 无参数：启动引导式向导（点选站点 → 模型 → tokens），不再让用户手打 ID；
//   - 带参数：兼容旧的手动调用路径（保留 /sitetest <id> ... 直接测试）。
func (c *CommandService) handleSiteTest(ctx context.Context, chatID int64, sub Subscriber, args []string) (string, *InlineKeyboard, error) {
	if c.siteTest == nil {
		return "⚠️ 站点测试服务暂不可用，请稍后再试。", nil, nil
	}
	if len(args) == 0 {
		return c.startSiteTestWizard(ctx, chatID, sub)
	}
	id, err := parseID(args[0])
	if err != nil {
		return "用法：/sitetest &lt;channel_id&gt; [模型] [max_tokens]\n也可直接输入 /sitetest 走引导式选择。", nil, nil
	}
	model, maxTokens, usageErr := parseSiteTestArgs(args[1:])
	if usageErr != "" {
		return usageErr, nil, nil
	}

	t, err := c.startTestContext(ctx, chatID, sub, id, model, maxTokens)
	if err != nil {
		return "🧪 无法测试：" + err.Error(), nil, nil
	}
	if c.sender != nil {
		if serr := c.sender.StartTest(ctx, t); serr != nil {
			return "", nil, serr // 发送失败：返回错误让 worker 保留 offset 重试
		}
		return "", nil, nil // 进度消息由 StartTest 发送
	}
	// 无异步发送器（测试路径）：同步执行并返回结果
	msg, rerr := t.Runner.Run(ctx, t.ChannelID, t.Model, t.MaxTokens, sub.GroupIDs)
	if rerr != nil {
		return "🧪 站点测试失败：" + rerr.Error(), nil, nil
	}
	return msg, t.RetryKeyboard(), nil
}

// startTestContext 校验并组装站点测试上下文（授权 + 站点 + 模型解析 + 参数归一化）。
func (c *CommandService) startTestContext(ctx context.Context, chatID int64, sub Subscriber, channelID int, model string, maxTokens int) (*StartTestContext, error) {
	name, resolved, upstreamModel, err := c.siteTest.Preflight(ctx, channelID, model, sub.GroupIDs)
	if err != nil {
		return nil, err
	}
	return &StartTestContext{
		ChatID:        chatID,
		ChannelID:     channelID,
		ChannelName:   name,
		Model:         resolved,
		UpstreamModel: upstreamModel,
		MaxTokens:     clampSiteTestTokens(maxTokens),
		Sub:           sub,
		Runner:        c.siteTest,
	}, nil
}

// parseSiteTestArgs 解析 /sitetest 可选参数（模型与 max_tokens 与顺序无关）。
// 返回 usageErr 非空表示参数非法（直接回复用户）。
func parseSiteTestArgs(args []string) (model string, maxTokens int, usageErr string) {
	for _, a := range args {
		if n, err := strconv.Atoi(a); err == nil {
			if n <= 0 {
				return "", 0, "max_tokens 必须为正整数"
			}
			if maxTokens != 0 {
				return "", 0, "重复指定了 max_tokens"
			}
			maxTokens = n
			continue
		}
		if model != "" {
			return "", 0, "多余参数：" + a
		}
		model = a
	}
	return model, maxTokens, "" // maxTokens=0 → Runner 按默认 128
}

// ===== 回调数据编解码 =====

// stData 站点测试按钮回调数据（Telegram 回调上限 64 字节；超长模型省略）。
func stData(channelID int, model string, maxTokens int) string {
	d := fmt.Sprintf("st:%d", channelID)
	if model != "" && len(d)+1+len(model) <= 58 {
		d += ":" + model
	}
	if maxTokens > 0 && len(d) < 61 {
		d += fmt.Sprintf(":%d", maxTokens)
	}
	return d
}

// parseStData 解析 st:<id>[:model][:tokens]。
func parseStData(data string) (id int, model string, maxTokens int, usageErr string) {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return 0, "", 0, "无效的测试请求"
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n <= 0 {
		return 0, "", 0, "无效的站点 ID"
	}
	if len(parts) >= 3 && parts[2] != "" {
		model = parts[2]
	}
	if len(parts) >= 4 {
		if tk, terr := strconv.Atoi(parts[3]); terr == nil {
			maxTokens = tk
		}
	}
	return n, model, maxTokens, ""
}

// ===== 内联键盘构建 =====

// helpKeyboard 帮助消息的快捷入口键盘（按订阅者能力裁剪）。
func helpKeyboard(sub Subscriber) *InlineKeyboard {
	var rows [][]InlineButton
	if sub.QueryEnabled {
		rows = append(rows,
			InlineRow(InlineButton{Text: "🛰 系统状态", Data: "cmd:/status"}, InlineButton{Text: "📡 站点列表", Data: "cmd:/relay"}),
			InlineRow(InlineButton{Text: "💰 余额", Data: "cmd:/balance"}, InlineButton{Text: "🩺 健康", Data: "cmd:/health"}),
			InlineRow(InlineButton{Text: "📐 倍率", Data: "cmd:/ratio"}, InlineButton{Text: "🧪 站点测试", Data: "cmd:/sitetest"}),
		)
	}
	if sub.AlertEnabled {
		rows = append(rows, InlineRow(InlineButton{Text: "⚠️ 活跃告警", Data: "cmd:/alerts"}))
	}
	if len(rows) == 0 {
		return nil
	}
	return &InlineKeyboard{Rows: rows}
}

// quickKeyboard 系统状态摘要的快捷入口键盘。
func quickKeyboard() *InlineKeyboard {
	return &InlineKeyboard{Rows: [][]InlineButton{
		{{Text: "📡 站点列表", Data: "cmd:/relay"}, {Text: "⚠️ 活跃告警", Data: "cmd:/alerts"}},
	}}
}

// alertsKeyboard 告警消息的过滤键盘。
func alertsKeyboard() *InlineKeyboard {
	return &InlineKeyboard{Rows: [][]InlineButton{
		{{Text: "🔍 只看严重", Data: "filter:critical"}, {Text: "📊 全部告警", Data: "filter:all"}},
	}}
}

// relayDetailKeyboard 站点详情的操作键盘（下钻其他详情/启动测试/返回列表）。
func relayDetailKeyboard(id int) *InlineKeyboard {
	return &InlineKeyboard{Rows: [][]InlineButton{
		InlineRow(
			InlineButton{Text: "💰 余额", Data: "cmd:/balance:" + strconv.Itoa(id)},
			InlineButton{Text: "🩺 健康", Data: "cmd:/health:" + strconv.Itoa(id)},
			InlineButton{Text: "📐 倍率", Data: "cmd:/ratio:" + strconv.Itoa(id)},
		),
		InlineRow(
			InlineButton{Text: "🔍 质量检测", Data: "cmd:/quality:" + strconv.Itoa(id)},
			InlineButton{Text: "🧪 站点测试", Data: stData(id, "", 0)},
		),
		InlineRow(InlineButton{Text: "← 返回列表", Data: "cmd:/relay"}),
	}}
}

// backToRelayKeyboard 子详情页（余额/健康/倍率/质量）的返回与测试键盘。
func backToRelayKeyboard(id int) *InlineKeyboard {
	return &InlineKeyboard{Rows: [][]InlineButton{
		InlineRow(
			InlineButton{Text: "🧪 站点测试", Data: stData(id, "", 0)},
			InlineButton{Text: "← 站点详情", Data: "cmd:/relay:" + strconv.Itoa(id)},
		),
	}}
}

// relayListKeyboard 站点列表的逐站按钮（每行 2 个，最多 24 个防键盘过大）。
func relayListKeyboard(items []RelaySummary) *InlineKeyboard {
	if len(items) == 0 {
		return nil
	}
	if len(items) > 24 {
		items = items[:24]
	}
	rows := make([][]InlineButton, 0, (len(items)+1)/2)
	row := make([]InlineButton, 0, 2)
	for _, it := range items {
		row = append(row, InlineButton{Text: truncateButtonText(it.Name), Data: "cmd:/relay:" + strconv.Itoa(it.ID)})
		if len(row) == 2 {
			rows = append(rows, row)
			row = make([]InlineButton, 0, 2)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return &InlineKeyboard{Rows: rows}
}

// truncateButtonText 按钮文本截断（防止长站点名撑爆键盘行宽）。
func truncateButtonText(s string) string {
	r := []rune(s)
	if len(r) <= 18 {
		return s
	}
	return string(r[:17]) + "…"
}

// ===== 命令菜单（setMyCommands） =====

// SyncBotMenu 按订阅者能力维护私聊命令菜单（编辑框「/」菜单）。
// 能力组合未变化时不调用 API（避免每轮轮询无谓请求）。
func (c *CommandService) SyncBotMenu(ctx context.Context, client BotClient, subs []Subscriber) error {
	hasAlerts, hasQuery := false, false
	for _, s := range subs {
		if !s.Enabled {
			continue
		}
		if s.AlertEnabled {
			hasAlerts = true
		}
		if s.QueryEnabled {
			hasQuery = true
		}
	}
	if hasAlerts == c.menuAlerts && hasQuery == c.menuQuery {
		return nil
	}
	cmds := []BotCommand{
		{Command: "/start", Description: "显示帮助"},
		{Command: "/help", Description: "显示帮助"},
	}
	if hasAlerts {
		cmds = append(cmds,
			BotCommand{Command: "/alerts", Description: "当前活跃告警"},
			BotCommand{Command: "/alert", Description: "单条告警详情"},
		)
	}
	if hasQuery {
		cmds = append(cmds,
			BotCommand{Command: "/status", Description: "系统状态摘要"},
			BotCommand{Command: "/relay", Description: "中转站列表与详情"},
			BotCommand{Command: "/balance", Description: "站点余额"},
			BotCommand{Command: "/health", Description: "站点健康"},
			BotCommand{Command: "/ratio", Description: "站点倍率"},
			BotCommand{Command: "/quality", Description: "质量检测结果"},
			BotCommand{Command: "/sitetest", Description: "站点直达测试（引导式）"},
		)
	}
	if err := client.SetMyCommands(ctx, cmds); err != nil {
		return err
	}
	c.menuAlerts, c.menuQuery = hasAlerts, hasQuery
	return nil
}

// ===== 告警查询注入 =====

// handleAlerts 由 Worker 注入的告警查询回调实现（见 worker.go setAlertsQuery）。
var alertsQuery func(ctx context.Context, groupIDs []int, criticalOnly bool) (string, error)

// SetAlertsQuery 注入告警查询实现（避免 commands ↔ alert/telegram 循环依赖）。
func SetAlertsQuery(fn func(ctx context.Context, groupIDs []int, criticalOnly bool) (string, error)) {
	alertsQuery = fn
}

func (c *CommandService) handleAlerts(ctx context.Context, sub Subscriber, args []string) (string, error) {
	if alertsQuery == nil {
		return "⚠️ 告警服务暂不可用，请稍后再试。", nil
	}
	criticalOnly := false
	if len(args) > 0 && args[0] == "critical" {
		criticalOnly = true
	}
	return alertsQuery(ctx, sub.GroupIDs, criticalOnly)
}

// alertDetailQuery 由 RegisterAlertQueries 注入的单条详情实现（见 alerts.go）。
var alertDetailQuery func(ctx context.Context, key string, groupIDs []int) (string, error)

// SetAlertDetailQuery 注入单条告警详情查询（避免 commands ↔ alert/telegram 循环依赖）。
func SetAlertDetailQuery(fn func(ctx context.Context, key string, groupIDs []int) (string, error)) {
	alertDetailQuery = fn
}

// handleAlertDetail 单条告警详情（/alert <alert_key>，按 key 精确查询并校验分组授权）。
func (c *CommandService) handleAlertDetail(ctx context.Context, sub Subscriber, args []string) (string, error) {
	if alertDetailQuery == nil {
		return "⚠️ 告警服务暂不可用，请稍后再试。", nil
	}
	if len(args) == 0 {
		return "用法：/alert &lt;alert_key&gt;", nil
	}
	return alertDetailQuery(ctx, args[0], sub.GroupIDs)
}

// handleStatus 系统状态摘要（复用 RelayList + 告警计数）。
func (c *CommandService) handleStatus(ctx context.Context, sub Subscriber) (string, error) {
	relays, err := c.query.RelayList(ctx, sub.GroupIDs)
	if err != nil {
		return "", err
	}
	var alertsLine string
	if alertsQuery != nil {
		alertsLine, _ = alertsQuery(ctx, sub.GroupIDs, false)
	}
	out := "🛰 <b>系统状态</b>\n━━━━━━━━━━━━\n"
	// RelayList 自带「📡 中转站列表（N 站）」标题，剥离后作为状态分区
	body := relays
	if i := indexOfNewline(relays); i >= 0 {
		body = relays[i+1:]
	}
	// 移除 RelayList 的底部提示行（避免与状态摘要提示重复）
	body = strings.TrimSuffix(body, "💡 详情：/relay &lt;ID&gt;")
	body = strings.TrimRight(body, "\n")
	out += "📡 <b>站点状态</b>\n" + body + "\n━━━━━━━━━━━━\n"
	if alertsLine != "" {
		if strings.HasPrefix(alertsLine, "✅") {
			out += alertsLine
		} else {
			// FormatAlerts 自带标题与分隔线，直接拼接
			out += alertsLine
		}
	}
	return out, nil
}

func indexOfNewline(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return i
		}
	}
	return -1
}

// parseCommand 去掉 /command@botname 后缀，返回命令与空白分隔的参数。
func parseCommand(text string) (string, []string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", nil
	}
	cmd := fields[0]
	if !strings.HasPrefix(cmd, "/") {
		return "", nil // 非命令文本：返回帮助
	}
	if at := strings.Index(cmd, "@"); at > 0 {
		cmd = cmd[:at]
	}
	cmd = strings.ToLower(cmd)
	return cmd, fields[1:]
}

// parseID 解析命令参数中的站点 ID（非正整数返回错误）。
func parseID(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid id %q", s)
	}
	return n, nil
}
