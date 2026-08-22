package telegram

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
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
// HTML 安全拆分：绝不切断标签——
//   - 优先 ≤maxLen 的标签平衡切点；
//   - 标签横跨边界（如 <b> 很早开始、</b> 在边界之后）时，在片尾补闭合标签、
//     下一片开头重新打开，保证每片都是合法 HTML（Telegram 对损坏 HTML
//     返回确定性 400，会导致机器人只发一半）。
func SplitMessage(msg string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = MaxTelegramMessageLen
	}
	// 含 HTML 标签才需要安全拆分；纯文本走快路径
	if !strings.ContainsAny(msg, "<>") {
		return splitPlain(msg, maxLen)
	}
	if len(msg) <= maxLen {
		return []string{msg}
	}

	var parts []string
	rest := msg
	// 与 splitPlain 一致：给后缀 "(N/M)" 预留 8 字符
	chunkLen := maxLen - 8
	if chunkLen <= 0 {
		chunkLen = maxLen
	}
	for len(rest) > chunkLen {
		head, tail := cutHTMLChunk(rest, chunkLen)
		parts = append(parts, head)
		rest = tail
	}
	if rest != "" {
		parts = append(parts, rest)
	}
	total := len(parts)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s\n(%d/%d)", parts[i], i+1, total)
	}
	return parts
}

// cutHTMLChunk 从 rest 切出 ≤maxLen 的 HTML 合法片段，返回 (片段, 剩余)。
func cutHTMLChunk(rest string, maxLen int) (string, string) {
	if cut := lastSafeCut(rest, maxLen); cut > 0 {
		return rest[:cut], rest[cut:]
	}
	// 标签横跨边界：先按 maxLen 切内容，算出未闭合标签的闭合串长度，
	// 把该长度计入预算回缩内容，保证「内容+闭合串」不超 maxLen。
	cut := lastRuneBoundary(rest, maxLen)
	open := unclosedTags(rest[:cut])
	closing := closeTags(open)
	if len(open) == 0 || len(closing) == 0 {
		return rest[:cut], rest[cut:] // 理论不可达：lastSafeCut 会命中
	}
	// 回缩内容给闭合串腾空间
	shrunk := maxLen - len(closing)
	if shrunk > 0 {
		if c2 := lastRuneBoundary(rest, shrunk); c2 > 0 {
			cut = c2
			open = unclosedTags(rest[:cut])
			closing = closeTags(open)
		}
	}
	reopen := reopenTags(open)
	return rest[:cut] + closing, reopen + rest[cut:]
}

// closeTags 按栈序生成闭合串（逆序闭合）。
func closeTags(open []string) string {
	var b strings.Builder
	for i := len(open) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "</%s>", open[i])
	}
	return b.String()
}

// reopenTags 按栈序生成重开串（顺序重开，供下一片开头）。
func reopenTags(open []string) string {
	var b strings.Builder
	for _, t := range open {
		fmt.Fprintf(&b, "<%s>", t)
	}
	return b.String()
}

// lastRuneBoundary 返回 ≤target 的 rune 边界位置。
func lastRuneBoundary(s string, target int) int {
	if target > len(s) {
		target = len(s)
	}
	for target > 0 && !utf8.RuneStart(s[target]) {
		target--
	}
	return target
}

// splitPlain 纯文本拆分（与旧实现同语义：按 rune 切，保留 (1/N) 后缀）。
func splitPlain(msg string, maxLen int) []string {
	runes := []rune(msg)
	if len(runes) <= maxLen {
		return []string{msg}
	}
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

// tagToken 匹配 HTML 标签（开始/结束/自闭合）。
var tagToken = regexp.MustCompile(`</?[a-zA-Z0-9]+(?: [^>]*)?/?>`)

// lastSafeCut 在 rest 中找 ≤targetLen 的最大标签平衡切点。
// 返回 0 表示无安全点（调用方走补标签策略）。
func lastSafeCut(rest string, targetLen int) int {
	p := rest[:targetLen]
	if tagsBalanced(p) && !hasDanglingTag(p) {
		return targetLen
	}
	// 向左回溯（上限 256 字节）：dangling 标签通常在边界附近
	limit := targetLen - 256
	if limit < 0 {
		limit = 0
	}
	for n := targetLen - 1; n > limit; n-- {
		if !utf8.RuneStart(rest[n]) {
			continue
		}
		q := rest[:n]
		if tagsBalanced(q) && !hasDanglingTag(q) {
			return n
		}
	}
	return 0
}

// unclosedTags 返回 s 中未闭合的开始标签名（栈序）。
func unclosedTags(s string) []string {
	var stack []string
	for _, loc := range tagToken.FindAllStringIndex(s, -1) {
		tag := s[loc[0]:loc[1]]
		if strings.HasPrefix(tag, "</") {
			if len(stack) > 0 && stack[len(stack)-1] == tagName(tag) {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if strings.HasSuffix(tag, "/>") {
			continue // 自闭合
		}
		stack = append(stack, tagName(tag))
	}
	return stack
}

// tagName 提取标签名（小写）。
func tagName(tag string) string {
	t := strings.TrimPrefix(tag, "<")
	t = strings.TrimPrefix(t, "/")
	t = strings.TrimSuffix(t, ">")
	t = strings.TrimSuffix(t, "/")
	t = strings.TrimSpace(t)
	if i := strings.IndexAny(t, " \t"); i >= 0 {
		t = t[:i]
	}
	return strings.ToLower(t)
}

// tagsBalanced 前缀内开始/结束标签是否配平（自闭合与单标签如 <br> 视为平衡）。
func tagsBalanced(s string) bool {
	return len(unclosedTags(s)) == 0
}

// hasDanglingTag 前缀末尾是否残留未完成的标签（如 "…<b" 或 "…</bo"）。
func hasDanglingTag(s string) bool {
	// 最后一个 '<' 之后没有对应的 '>'
	lt := strings.LastIndex(s, "<")
	if lt < 0 {
		return false
	}
	gt := strings.LastIndex(s, ">")
	return gt < lt
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
