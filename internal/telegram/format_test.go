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
