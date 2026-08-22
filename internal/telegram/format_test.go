package telegram

import (
	"strings"
	"testing"
)

func TestEscapeHTML(t *testing.T) {
	got := EscapeHTML(`<Relay & "A">`)
	if got != "&lt;Relay &amp; &quot;A&quot;&gt;" {
		t.Fatal(got)
	}
}

func TestSplitTelegramMessage(t *testing.T) {
	parts := SplitMessage(strings.Repeat("x", 9000), 4096)
	if len(parts) != 3 {
		t.Fatalf("got %d parts", len(parts))
	}
	if !strings.Contains(parts[0], "(1/3)") {
		t.Fatal(parts[0])
	}
	if !strings.Contains(parts[1], "(2/3)") {
		t.Fatal(parts[1])
	}
	if !strings.Contains(parts[2], "(3/3)") {
		t.Fatal(parts[2])
	}
	// 各段不超过限制
	for i, p := range parts {
		if len(p) > 4096 {
			t.Fatalf("part %d length %d exceeds limit", i, len(p))
		}
	}
}

func TestSplitMessageShortNoSuffix(t *testing.T) {
	parts := SplitMessage("short message", 4096)
	if len(parts) != 1 || parts[0] != "short message" {
		t.Fatalf("got %v", parts)
	}
}

func TestSplitMessageChinese(t *testing.T) {
	// 中文按 rune 拆分（4096 是 Telegram 按 UTF-16 长度计，保守用 rune 上限）
	msg := strings.Repeat("测", 8000)
	parts := SplitMessage(msg, 4096)
	if len(parts) != 2 {
		t.Fatalf("got %d parts", len(parts))
	}
	if !strings.Contains(parts[0], "(1/2)") || !strings.Contains(parts[1], "(2/2)") {
		t.Fatalf("suffix missing: %q / %q", parts[0], parts[1])
	}
}

// TestSplitMessageHTMLSafe HTML 消息拆分绝不切断标签——
// 每个分片内的 <b>/<i> 等标签必须配平（旧实现按 rune 硬切会截断标签，
// Telegram 返回确定性 400，导致机器人只发一半）。
func TestSplitMessageHTMLSafe(t *testing.T) {
	// 构造超长 HTML：开头 <b>、中间夹 <i>、末尾 </b>
	line := "<b>加粗内容</b> ｜ 普通文本 ｜ <i>斜体</i> ｜ "
	msg := line + strings.Repeat("填充文字", 900)
	msg += "\n结尾</b>"
	parts := SplitMessage(msg, 512)

	// 每个分片的标签必须平衡（不含残缺标签）
	for i, p := range parts {
		if hasDanglingTag(p) {
			t.Fatalf("part %d has dangling tag: %q…", i, p[:min(60, len(p))])
		}
		if !tagsBalanced(p) {
			t.Fatalf("part %d unbalanced tags: %q…", i, p[:min(60, len(p))])
		}
	}
	// 每片不超过上限（含后缀）
	for i, p := range parts {
		if len(p) > 512 {
			t.Fatalf("part %d length %d exceeds 512", i, len(p))
		}
	}
}

// TestSplitMessageEmpty 空串与纯空格边界。
func TestSplitMessageEmpty(t *testing.T) {
	if got := SplitMessage("", 100); len(got) != 1 || got[0] != "" {
		t.Fatalf("got %v", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
