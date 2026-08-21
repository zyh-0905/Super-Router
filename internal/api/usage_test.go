package api

import "testing"

// TestScanUsageChunk 流式 usage 嗅探（B1）。
func TestScanUsageChunk(t *testing.T) {
	// 标准 OpenAI 流式末尾 usage chunk
	chunk := []byte(`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":60,"total_tokens":68}}` + "\n\n")
	p, c := scanUsageChunk(chunk)
	if p != 8 || c != 60 {
		t.Fatalf("usage = %d/%d, want 8/60", p, c)
	}

	// 无 usage 的普通 chunk
	p, c = scanUsageChunk([]byte(`data: {"id":"c1","choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
	if p != 0 || c != 0 {
		t.Fatalf("no usage = %d/%d, want 0/0", p, c)
	}

	// 只有 total_tokens（部分中转站不拆输入输出）
	chunk = []byte(`data: {"usage":{"total_tokens":42}}`)
	p, c = scanUsageChunk(chunk)
	if p != 42 || c != 0 {
		t.Fatalf("total-only = %d/%d, want 42/0（total 计入输入）", p, c)
	}

	// 跨块截断的 usage 对象（不完整 JSON）→ 丢弃
	p, c = scanUsageChunk([]byte(`data: {"usage":{"prompt_tokens":8,"com`))
	if p != 0 || c != 0 {
		t.Fatalf("truncated = %d/%d, want 0/0", p, c)
	}

	// 多个 usage 出现 → 取最后一次（流式惯例：最终帧为准）
	chunk = []byte(`data: {"usage":{"prompt_tokens":1,"completion_tokens":2}}` + "\n\n" +
		`data: {"usage":{"prompt_tokens":30,"completion_tokens":40}}`)
	p, c = scanUsageChunk(chunk)
	if p != 30 || c != 40 {
		t.Fatalf("last-wins = %d/%d, want 30/40", p, c)
	}
}

func TestNullableInt(t *testing.T) {
	if nullableInt(0) != nil {
		t.Fatal("zero must map to NULL")
	}
	if nullableInt(-3) != nil {
		t.Fatal("negative must map to NULL")
	}
	if nullableInt(42) != 42 {
		t.Fatal("positive must pass through")
	}
}

func TestAsInt(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int
	}{
		{float64(8), 8},
		{int(16), 16},
		{int64(32), 32},
		{"64", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := asInt(c.in); got != c.want {
			t.Fatalf("asInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
