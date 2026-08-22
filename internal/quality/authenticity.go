package quality

// 模型鉴定（authenticity）阶段：复刻禾维 AI（hvoy.ai）的「真假模型」启发式思路，
// 判断站点声明的模型是否真的是那个模型（而非用便宜的旧模型/其它模型冒充）。
//
// 本实现只复刻**不依赖官方基准**的两个探测维度：
//   1. 算术题：真模型几乎必然算对 17+26=43；
//   2. 时效性问答：问 2025 年真实事件——知识截止 2025 年之后的真模型
//      能答对，或诚实说「不知道」；用旧模型冒充的站点会编造错误答案。
//
// 与禾维不同、也与本项目 design philosophy 一致：结论是**启发式信号**，
// 只输出 attention（疑似不一致），绝不输出「这一定是假模型」的确定性结论。
// 不调用官方 API 做基准对比（无官方 Key，复刻不了）。

import (
	"context"
	"regexp"
	"strings"
)

// authenticity 探测 prompt（单次请求两个问题，节省 token）。
// 结构化格式便于解析两个答案，且互不干扰。
const authenticityProbe = `请分别回答下面两个问题，不要解释，严格按格式输出：

1. 计算 17 + 26，只输出数字。
2. 不允许上网查。2025年7月30日俄罗斯堪察加半岛海岸附近发生的地震震级是多少？不知道就回答「不知道」。

输出格式：
算术答案：<数字>
地震震级：<答案>`

// arithmeticAnswer 算术题期望答案。
const arithmeticAnswer = "43"

// recencyAnswerPatterns 时效题正确答案的接受形态（8.8 级）。
var recencyAnswerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`8\.8`),
	regexp.MustCompile(`八点八`),
}

// recencyUnknownMarkers 模型诚实表示「不知道」的标记。
var recencyUnknownMarkers = []string{
	"不知道", "不清楚", "不确定", "无法", "不能确定", "抱歉", "无法回答", "don't know", "not sure", "cannot", "can't",
}

// authenticityStage 执行模型鉴定（两次小型聊天请求，可能产生少量上游费用）。
type authenticityStage struct {
	executor *Executor
}

func (s authenticityStage) Name() string { return StageAuthenticity }

func (s authenticityStage) Run(ctx context.Context, input *StageContext) StageResult {
	res := StageResult{Stage: StageAuthenticity, CheckName: "authenticity_signals", Details: map[string]interface{}{}}

	timeout := effectiveTimeout(input.Channel, chatTimeout)
	client := s.executor.httpClient()

	// 单次非流式聊天（算术 + 时效两个问题合并，省 token）
	ev, err := RunChat(ctx, input.Channel, ProbeScenario{
		Model:     input.Run.Model,
		Messages:  []ProbeMessage{{Role: "user", Content: authenticityProbe}},
		MaxTokens: 64,
	}, timeout, client)
	if err != nil {
		return chatFailedResult(res, err)
	}
	res.ActualModel = ev.ActualModel
	res.TTFBMS = intPtr(ev.TTFBMS)
	res.LatencyMS = intPtr(ev.TotalMS)

	arithText, recencyText := splitAuthenticityAnswer(ev.Text)
	arithOK := answerContains(arithText, arithmeticAnswer)
	recOK, recUnknown := judgeRecency(recencyText)

	res.Details["arithmetic_answer"] = truncateRunes(strings.TrimSpace(arithText), 40)
	res.Details["arithmetic_correct"] = arithOK
	res.Details["recency_answer"] = truncateRunes(strings.TrimSpace(recencyText), 60)
	res.Details["recency_correct"] = recOK
	res.Details["recency_unknown"] = recUnknown

	// 判定（启发式：arithmetic 错误或时效编造 → attention，绝不 failed）
	switch {
	case !arithOK:
		res.Status = StatusAttention
		res.Error = "arithmetic_mismatch"
		res.Details["code"] = "arithmetic_mismatch"
	case recOK || recUnknown:
		res.Status = StatusPassed
		res.Details["code"] = "consistent"
	default:
		// 时效题既没答对也没诚实说不知道 → 疑似编造（旧模型冒充的信号）
		res.Status = StatusAttention
		res.Error = "recency_hallucination"
		res.Details["code"] = "recency_hallucination"
	}
	return res
}

// splitAuthenticityAnswer 从结构化回答里拆分算术答案与时效答案。
// 优先按「地震震级：」分隔；找不到分隔时退化为整段文本（两处都用同一文本判定）。
func splitAuthenticityAnswer(text string) (arith, recency string) {
	idx := strings.Index(text, "地震震级")
	if idx < 0 {
		return text, text
	}
	arith = text[:idx]
	recency = text[idx:]
	return arith, recency
}

// answerContains 判断模型回答是否包含期望答案（精确数字匹配，避免 43 误匹配 143）。
func answerContains(text, want string) bool {
	// 提取文本中的所有整数，与期望值精确比较
	re := regexp.MustCompile(`-?\d+`)
	for _, m := range re.FindAllString(text, -1) {
		if m == want {
			return true
		}
	}
	return false
}

// judgeRecency 判断时效题回答：(正确, 诚实表示不知道)。
func judgeRecency(text string) (correct bool, unknown bool) {
	lower := strings.ToLower(text)
	for _, re := range recencyAnswerPatterns {
		if re.MatchString(text) {
			return true, false
		}
	}
	for _, m := range recencyUnknownMarkers {
		if strings.Contains(lower, m) {
			return false, true
		}
	}
	return false, false
}
