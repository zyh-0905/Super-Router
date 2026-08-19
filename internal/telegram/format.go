package telegram

import (
	"fmt"
	"strings"
)

// EscapeHTML 转义 Telegram HTML 消息中的动态字段（站点名/模型名/错误信息）。
// 只转义 HTML 特殊字符，实体形式与计划验收一致（&quot;）。
func EscapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// MaxTelegramMessageLen Telegram 单条消息上限（UTF-16 码元，保守按 rune 计算）。
const MaxTelegramMessageLen = 4096

// SplitMessage 超长消息按 maxLen 自动拆分并追加 (1/N) 标记；不超长时返回单段原样。
// 拆分按 rune 进行，避免多字节字符被截断。
func SplitMessage(msg string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = MaxTelegramMessageLen
	}
	runes := []rune(msg)
	if len(runes) <= maxLen {
		return []string{msg}
	}

	// 预留每段后缀空间（"\n(1/N)"），避免加后缀后超限
	suffixSpace := 8
	chunk := maxLen - suffixSpace
	if chunk <= 0 {
		chunk = maxLen
	}

	var parts []string
	for len(runes) > 0 {
		n := chunk
		if n > len(runes) {
			n = len(runes)
		}
		parts = append(parts, string(runes[:n]))
		runes = runes[n:]
	}
	if len(parts) == 1 {
		return []string{msg}
	}
	total := len(parts)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s\n(%d/%d)", parts[i], i+1, total)
	}
	return parts
}

// FormatWindow 统计窗口文案（过去 1 小时）。
func FormatWindow(hours int) string {
	if hours <= 1 {
		return "过去 1 小时"
	}
	return fmt.Sprintf("过去 %d 小时", hours)
}

// Bracket 中括号包裹（消息模板辅助）。
func Bracket(s string) string { return "[" + s + "]" }

// CodeBlock Telegram 代码块（<pre>），内容已由调用方转义。
func CodeBlock(s string) string { return "<pre>" + s + "</pre>" }

// JoinLines 多行拼接。
func JoinLines(lines ...string) string { return strings.Join(lines, "\n") }
