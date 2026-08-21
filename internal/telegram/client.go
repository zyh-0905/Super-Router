package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// apiBaseURL Telegram Bot API base（固定常量，不允许配置改写）。
const apiBaseURL = "https://api.telegram.org"

// ErrRetryAfter 429 限流（Retry-After 秒数见字段）。
type ErrRetryAfter struct {
	Seconds int
	Err     error
}

func (e *ErrRetryAfter) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("telegram rate limited, retry after %ds", e.Seconds)
}

func (e *ErrRetryAfter) Unwrap() error { return e.Err }

// permanentError Telegram API 确定性拒绝（4xx 类错误码，429 除外）：
// HTML 解析失败、chat 不存在、bot 被踢出群等——重试永远失败。
// 调用方（worker poll 循环）识别后跳过该 update 并推进 offset，
// 防止毒丸消息无限重放卡死命令轮询（M5）。
type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// isPermanentTelegramError 判断错误是否属于 API 确定性拒绝。
func isPermanentTelegramError(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

// wrapTelegramAPIError 按错误码分类：4xx（非 429）→ 确定性拒绝；
// 其余（5xx/429/网络）→ 可重试错误原样返回。
func wrapTelegramAPIError(code int, err error) error {
	if code >= 400 && code < 500 && code != 429 {
		return &permanentError{err: err}
	}
	return err
}

// Client Bot API HTTP 客户端。Bot Token 通过 URL path 传入、只保存在内存，绝不写日志。
type Client struct {
	token  string
	client *http.Client
}

// NewClient 创建客户端。token 为空时所有方法返回错误。
func NewClient(token string) *Client {
	return &Client{
		token: token,
		client: &http.Client{
			Timeout: 60 * time.Second, // 长轮询上限；单次调用用 context 细化
		},
	}
}

// apiResponse Telegram 通用响应包装。
type apiResponse struct {
	OK          bool            `json:"ok"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

// do 执行一次 Bot API 调用。
// 调用路径中的动态字段只有 method 名与已编码的查询参数（不含 Token 明文日志）。
func (c *Client) do(ctx context.Context, method string, params url.Values) (*apiResponse, error) {
	if c.token == "" {
		return nil, errors.New("telegram bot token not configured")
	}
	u := fmt.Sprintf("%s/bot%s/%s", apiBaseURL, c.token, method)
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create telegram request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A4：url.Error 包含完整请求 URL（含 Bot Token 于 path），
		// 绝不能原样写入日志/last_error/API 响应。转换为不含 URL 的稳定错误。
		return nil, fmt.Errorf("telegram api unreachable: %s", sanitizeNetErr(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read telegram response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		secs := 30
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n > 0 {
				secs = n
			}
		}
		return nil, &ErrRetryAfter{Seconds: secs, Err: fmt.Errorf("telegram 429")}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, wrapTelegramAPIError(resp.StatusCode,
			fmt.Errorf("telegram http %d: %s", resp.StatusCode, truncateStr(string(body), 300)))
	}

	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("decode telegram response: %w", err)
	}
	if !ar.OK {
		return nil, wrapTelegramAPIError(ar.ErrorCode,
			fmt.Errorf("telegram error %d: %s", ar.ErrorCode, truncateStr(ar.Description, 300)))
	}
	return &ar, nil
}

// GetMe 验证 Bot Token 有效性。
func (c *Client) GetMe(ctx context.Context) error {
	_, err := c.do(ctx, "getMe", nil)
	return err
}

// GetUpdates 长轮询（timeout 秒；响应立即返回时也可为 0）。
// 同时接收消息与内联键盘回调（callback_query）。
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	params := url.Values{}
	params.Set("timeout", strconv.Itoa(int(timeout.Seconds())))
	params.Set("allowed_updates", `["message","callback_query"]`)
	if offset > 0 {
		params.Set("offset", strconv.FormatInt(offset, 10))
	}
	ar, err := c.do(ctx, "getUpdates", params)
	if err != nil {
		return nil, err
	}

	var raws []struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
		CallbackQuery *struct {
			ID      string `json:"id"`
			Data    string `json:"data"`
			Message *struct {
				MessageID int64 `json:"message_id"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
	}
	if err := json.Unmarshal(ar.Result, &raws); err != nil {
		return nil, fmt.Errorf("decode telegram updates: %w", err)
	}
	updates := make([]Update, 0, len(raws))
	for _, r := range raws {
		switch {
		case r.CallbackQuery != nil:
			cb := r.CallbackQuery
			updates = append(updates, Update{
				UpdateID:          r.UpdateID,
				HasCallback:       true,
				CallbackID:        cb.ID,
				CallbackData:      cb.Data,
				CallbackMessageID: cb.Message.MessageID,
				CallbackChatID:    cb.Message.Chat.ID,
			})
		case r.Message != nil:
			updates = append(updates, Update{
				UpdateID: r.UpdateID,
				ChatID:   r.Message.Chat.ID,
				Text:     r.Message.Text,
			})
		}
		// 其余更新类型忽略（allowed_updates 已过滤，双保险）
	}
	return updates, nil
}

// SendMessage 发送 HTML 消息（可携带内联键盘），返回 Telegram message_id。
func (c *Client) SendMessage(ctx context.Context, chatID int64, html string, kb *InlineKeyboard) (int64, error) {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(chatID, 10))
	params.Set("text", html)
	params.Set("parse_mode", "HTML")
	params.Set("disable_web_page_preview", "true")
	applyInlineKeyboard(params, kb)

	ar, err := c.postForm(ctx, "sendMessage", params)
	if err != nil {
		return 0, err
	}
	var result struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(ar.Result, &result); err != nil {
		return 0, fmt.Errorf("decode telegram sendMessage result: %w", err)
	}
	return result.MessageID, nil
}

// EditMessageText 原位编辑消息文本与键盘（内联进度/回调响应用）。
func (c *Client) EditMessageText(ctx context.Context, chatID int64, messageID int64, html string, kb *InlineKeyboard) error {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(chatID, 10))
	params.Set("message_id", strconv.FormatInt(messageID, 10))
	params.Set("text", html)
	params.Set("parse_mode", "HTML")
	params.Set("disable_web_page_preview", "true")
	applyInlineKeyboard(params, kb)

	_, err := c.postForm(ctx, "editMessageText", params)
	return err
}

// SendChatAction 发送聊天状态（typing：命令处理期间显示「正在输入…」）。
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(chatID, 10))
	params.Set("action", action)
	_, err := c.postForm(ctx, "sendChatAction", params)
	return err
}

// AnswerCallbackQuery 应答回调（关闭按钮上的 loading 动画）。
func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackID string) error {
	params := url.Values{}
	params.Set("callback_query_id", callbackID)
	_, err := c.postForm(ctx, "answerCallbackQuery", params)
	return err
}

// SetMyCommands 设置私聊命令菜单（编辑框的「/」菜单）。
func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	payload, err := json.Marshal(commands)
	if err != nil {
		return fmt.Errorf("marshal bot commands: %w", err)
	}
	params := url.Values{}
	params.Set("commands", string(payload))
	_, err = c.postForm(ctx, "setMyCommands", params)
	return err
}

// applyInlineKeyboard 把内联键盘序列化为 reply_markup.inline_keyboard 参数。
func applyInlineKeyboard(params url.Values, kb *InlineKeyboard) {
	if kb == nil || len(kb.Rows) == 0 {
		return
	}
	rows := make([][]map[string]string, 0, len(kb.Rows))
	for _, row := range kb.Rows {
		if len(row) == 0 {
			continue
		}
		buttons := make([]map[string]string, 0, len(row))
		for _, b := range row {
			if b.URL != "" {
				buttons = append(buttons, map[string]string{"text": b.Text, "url": b.URL})
			} else {
				buttons = append(buttons, map[string]string{"text": b.Text, "callback_data": b.Data})
			}
		}
		rows = append(rows, buttons)
	}
	if len(rows) == 0 {
		return
	}
	markup, _ := json.Marshal(map[string]interface{}{"inline_keyboard": rows})
	params.Set("reply_markup", string(markup))
}

// postForm POST 表单编码调用 Bot API，返回成功响应（ok=true 校验在内）。
// 错误路径与 do() 同口径：429 → ErrRetryAfter；4xx → permanentError；不泄露 Token。
func (c *Client) postForm(ctx context.Context, method string, params url.Values) (*apiResponse, error) {
	u := fmt.Sprintf("%s/bot%s/%s", apiBaseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewBufferString(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A4：同 do()——错误不得包含带 Token 的 URL
		return nil, fmt.Errorf("telegram api unreachable: %s", sanitizeNetErr(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read telegram response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		secs := 30
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n > 0 {
				secs = n
			}
		}
		return nil, &ErrRetryAfter{Seconds: secs, Err: fmt.Errorf("telegram 429")}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, wrapTelegramAPIError(resp.StatusCode,
			fmt.Errorf("telegram http %d: %s", resp.StatusCode, truncateStr(string(body), 300)))
	}

	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("decode telegram response: %w", err)
	}
	if !ar.OK {
		return nil, wrapTelegramAPIError(ar.ErrorCode,
			fmt.Errorf("telegram error %d: %s", ar.ErrorCode, truncateStr(ar.Description, 300)))
	}
	return &ar, nil
}

// truncateStr 截断错误文本，避免超长上游错误撑爆日志。
func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// sanitizeNetErr 把网络错误压平为不含 URL/Token 的稳定描述。
// url.Error 的 Error() 会包含完整请求 URL（Bot Token 位于 path），
// 只提取底层错误类别（dns/连接拒绝/超时/TLS/EOF），丢弃 URL 部分。
func sanitizeNetErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		msg := strings.ToLower(ue.Err.Error())
		switch {
		case strings.Contains(msg, "no such host"):
			return "dns lookup failed"
		case strings.Contains(msg, "connection refused"):
			return "connection refused"
		case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
			return "request timed out"
		case strings.Contains(msg, "tls"), strings.Contains(msg, "certificate"):
			return "tls handshake failed"
		case strings.Contains(msg, "eof"), strings.Contains(msg, "reset"):
			return "connection closed"
		}
		return "network error"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "bot") && strings.Contains(msg, "/") {
		// 兜底：任何疑似包含 URL 的错误一律压平
		return "network error"
	}
	return err.Error()
}
