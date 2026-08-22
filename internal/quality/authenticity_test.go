package quality

import "testing"

func TestAnswerContains(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"43", true},
		{"答案是 43", true},
		{"The answer is 43.", true},
		{"143", false},   // 不误匹配
		{"430", false},   // 不误匹配
		{"4 3", false},   // 无空格数字不匹配
		{"四十三", false}, // 中文数字不匹配（本实现按阿拉伯数字解析）
		{"", false},
	}
	for _, c := range cases {
		if got := answerContains(c.text, "43"); got != c.want {
			t.Fatalf("answerContains(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestSplitAuthenticityAnswer(t *testing.T) {
	cases := []struct {
		text     string
		wantArit string
		wantRec  string
	}{
		{"算术答案：43\n地震震级：8.8", "算术答案：43\n", "地震震级：8.8"},
		{"算术答案：43\n地震震级：不知道", "算术答案：43\n", "地震震级：不知道"},
		{"no separator", "no separator", "no separator"},
	}
	for _, c := range cases {
		a, r := splitAuthenticityAnswer(c.text)
		if a != c.wantArit || r != c.wantRec {
			t.Fatalf("split(%q) = (%q, %q), want (%q, %q)", c.text, a, r, c.wantArit, c.wantRec)
		}
	}
}

func TestJudgeRecency(t *testing.T) {
	cases := []struct {
		text    string
		correct bool
		unknown bool
	}{
		{"震级为 8.8 级", true, false},
		{"答案是 8.8", true, false},
		{"我不知道", false, true},
		{"无法回答这个问题", false, true},
		{"抱歉，我无法确定", false, true},
		{"7.9", false, false},        // 错误答案，且非"不知道" → 疑似编造
		{"答案是 7.8", false, false}, // 错误答案
		{"", false, false},
	}
	for _, c := range cases {
		correct, unknown := judgeRecency(c.text)
		if correct != c.correct || unknown != c.unknown {
			t.Fatalf("judgeRecency(%q) = (%v, %v), want (%v, %v)",
				c.text, correct, unknown, c.correct, c.unknown)
		}
	}
}
