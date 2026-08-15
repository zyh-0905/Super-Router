package router

import (
	"sort"
)

// CandidateScore 候选渠道及其得分
type CandidateScore struct {
	Channel *ChannelHealth
	Score   float64
}

// Strategy 排序策略接口（prices 为官方模型价格库，供成本估算）
type Strategy interface {
	Sort(candidates []*ChannelHealth, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) []CandidateScore
}

// GetStrategy 根据策略名称获取策略实现
func GetStrategy(strategyName string) Strategy {
	switch strategyName {
	case "custom_priority":
		return &CustomPriorityStrategy{}
	case "price_first":
		return &PriceFirstStrategy{}
	case "latency_first":
		return &LatencyFirstStrategy{}
	case "reliability_first":
		return &ReliabilityFirstStrategy{}
	case "balanced":
		return &BalancedStrategy{}
	default:
		return &CustomPriorityStrategy{} // 默认策略
	}
}

// ===== 策略 1：Custom Priority（默认，适合自用）=====

type CustomPriorityStrategy struct{}

func (s *CustomPriorityStrategy) Sort(candidates []*ChannelHealth, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) []CandidateScore {
	scores := make([]CandidateScore, len(candidates))

	for i, ch := range candidates {
		scores[i] = CandidateScore{
			Channel: ch,
			Score:   s.calculateScore(ch, req),
		}
	}

	// 排序：role层级 → user_priority → reliability → ttft → id
	sort.SliceStable(scores, func(i, j int) bool {
		a, b := scores[i], scores[j]

		// 1. role 层级（primary=3, backup=2, emergency=1）
		roleA := getRoleLevel(a.Channel.Role)
		roleB := getRoleLevel(b.Channel.Role)
		if roleA != roleB {
			return roleA > roleB
		}

		// 2. user_priority
		if a.Channel.UserPriority != b.Channel.UserPriority {
			return a.Channel.UserPriority > b.Channel.UserPriority
		}

		// 3. reliability_score
		if a.Channel.ReliabilityScore != b.Channel.ReliabilityScore {
			return a.Channel.ReliabilityScore > b.Channel.ReliabilityScore
		}

		// 4. ttft_p50（越低越好）
		ttftA := getTTFT(a.Channel, req.Model)
		ttftB := getTTFT(b.Channel, req.Model)
		if ttftA != ttftB {
			return ttftA < ttftB
		}

		// 5. channel_id（稳定排序）
		return a.Channel.ID < b.Channel.ID
	})

	return scores
}

func (s *CustomPriorityStrategy) calculateScore(ch *ChannelHealth, req *FilterRequest) float64 {
	score := 0.0
	score += float64(getRoleLevel(ch.Role)) * 100
	score += float64(ch.UserPriority)
	score += ch.ReliabilityScore * 10
	ttft := getTTFT(ch, req.Model)
	if ttft > 0 {
		score += 1000.0 / float64(ttft) // 延迟越低得分越高
	}
	return score
}

// ===== 策略 2：Price First（低价优先）=====

type PriceFirstStrategy struct{}

func (s *PriceFirstStrategy) Sort(candidates []*ChannelHealth, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) []CandidateScore {
	scores := make([]CandidateScore, len(candidates))

	for i, ch := range candidates {
		cost := estimateCost(ch, req, policy, prices)
		scores[i] = CandidateScore{
			Channel: ch,
			Score:   -cost, // 成本越低得分越高（取负数）
		}
	}

	// 排序：cost → reliability → ttft → id
	sort.SliceStable(scores, func(i, j int) bool {
		a, b := scores[i], scores[j]

		costA := -a.Score
		costB := -b.Score
		if costA != costB {
			return costA < costB
		}

		if a.Channel.ReliabilityScore != b.Channel.ReliabilityScore {
			return a.Channel.ReliabilityScore > b.Channel.ReliabilityScore
		}

		ttftA := getTTFT(a.Channel, req.Model)
		ttftB := getTTFT(b.Channel, req.Model)
		if ttftA != ttftB {
			return ttftA < ttftB
		}

		return a.Channel.ID < b.Channel.ID
	})

	return scores
}

// ===== 策略 3：Latency First（延迟优先）=====

type LatencyFirstStrategy struct{}

func (s *LatencyFirstStrategy) Sort(candidates []*ChannelHealth, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) []CandidateScore {
	scores := make([]CandidateScore, len(candidates))

	for i, ch := range candidates {
		ttft := getTTFT(ch, req.Model)
		scores[i] = CandidateScore{
			Channel: ch,
			Score:   float64(-ttft), // 延迟越低得分越高
		}
	}

	// 排序：ttft_p50 → ttft_p95 → reliability → id
	sort.SliceStable(scores, func(i, j int) bool {
		a, b := scores[i], scores[j]

		ttftA := getTTFT(a.Channel, req.Model)
		ttftB := getTTFT(b.Channel, req.Model)
		if ttftA != ttftB {
			return ttftA < ttftB
		}

		ttftP95A := getTTFTP95(a.Channel, req.Model)
		ttftP95B := getTTFTP95(b.Channel, req.Model)
		if ttftP95A != ttftP95B {
			return ttftP95A < ttftP95B
		}

		if a.Channel.ReliabilityScore != b.Channel.ReliabilityScore {
			return a.Channel.ReliabilityScore > b.Channel.ReliabilityScore
		}

		return a.Channel.ID < b.Channel.ID
	})

	return scores
}

// ===== 策略 4：Reliability First（成功率优先）=====

type ReliabilityFirstStrategy struct{}

func (s *ReliabilityFirstStrategy) Sort(candidates []*ChannelHealth, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) []CandidateScore {
	scores := make([]CandidateScore, len(candidates))

	for i, ch := range candidates {
		scores[i] = CandidateScore{
			Channel: ch,
			Score:   ch.ReliabilityScore,
		}
	}

	// 排序：reliability → ttft → cost → id
	sort.SliceStable(scores, func(i, j int) bool {
		a, b := scores[i], scores[j]

		if a.Channel.ReliabilityScore != b.Channel.ReliabilityScore {
			return a.Channel.ReliabilityScore > b.Channel.ReliabilityScore
		}

		ttftA := getTTFT(a.Channel, req.Model)
		ttftB := getTTFT(b.Channel, req.Model)
		if ttftA != ttftB {
			return ttftA < ttftB
		}

		costA := estimateCost(a.Channel, req, policy, prices)
		costB := estimateCost(b.Channel, req, policy, prices)
		if costA != costB {
			return costA < costB
		}

		return a.Channel.ID < b.Channel.ID
	})

	return scores
}

// ===== 策略 5：Balanced（综合策略）=====

type BalancedStrategy struct{}

func (s *BalancedStrategy) Sort(candidates []*ChannelHealth, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) []CandidateScore {
	// 读取权重配置（支持嵌套 balanced_weights 或扁平键）
	costWeight := policy.GetConfigNestedFloat("balanced_weights.cost", 0.35)
	reliabilityWeight := policy.GetConfigNestedFloat("balanced_weights.reliability", 0.30)
	latencyWeight := policy.GetConfigNestedFloat("balanced_weights.latency", 0.25)
	loadWeight := policy.GetConfigNestedFloat("balanced_weights.load", 0.10)

	scores := make([]CandidateScore, len(candidates))

	// 归一化因子（用于将各指标映射到 [0, 1]）
	maxCost := 0.0
	maxTTFT := 0
	maxLoad := 0
	for _, ch := range candidates {
		cost := estimateCost(ch, req, policy, prices)
		if cost > maxCost {
			maxCost = cost
		}
		ttft := getTTFT(ch, req.Model)
		if ttft > maxTTFT {
			maxTTFT = ttft
		}
		if ch.RecentAttempts > maxLoad {
			maxLoad = ch.RecentAttempts
		}
	}

	if maxCost == 0 {
		maxCost = 1
	}
	if maxTTFT == 0 {
		maxTTFT = 1
	}

	for i, ch := range candidates {
		cost := estimateCost(ch, req, policy, prices)
		ttft := getTTFT(ch, req.Model)

		// 归一化到 [0, 1]，分数越高越好
		costScore := 1.0 - (cost / maxCost)
		reliabilityScore := ch.ReliabilityScore
		latencyScore := 1.0 - (float64(ttft) / float64(maxTTFT))
		// 负载分：基于滑动窗口内的尝试次数（越空闲分越高）；无数据视为完全空闲
		loadScore := 1.0
		if maxLoad > 0 {
			loadScore = 1.0 - float64(ch.RecentAttempts)/float64(maxLoad)
		}

		// 加权求和
		compositeScore := costWeight*costScore +
			reliabilityWeight*reliabilityScore +
			latencyWeight*latencyScore +
			loadWeight*loadScore

		scores[i] = CandidateScore{
			Channel: ch,
			Score:   compositeScore,
		}
	}

	// 排序：composite_score → reliability → id
	sort.SliceStable(scores, func(i, j int) bool {
		a, b := scores[i], scores[j]

		if a.Score != b.Score {
			return a.Score > b.Score
		}

		if a.Channel.ReliabilityScore != b.Channel.ReliabilityScore {
			return a.Channel.ReliabilityScore > b.Channel.ReliabilityScore
		}

		return a.Channel.ID < b.Channel.ID
	})

	return scores
}

// ===== 辅助函数 =====

func getRoleLevel(role string) int {
	switch role {
	case "primary":
		return 3
	case "backup":
		return 2
	case "emergency":
		return 1
	default:
		return 0
	}
}

func getTTFT(ch *ChannelHealth, model string) int {
	if ttft, ok := ch.TTFTP50[model]; ok {
		return ttft
	}
	return 9999 // 无数据时返回一个大值
}

func getTTFTP95(ch *ChannelHealth, model string) int {
	if ttft, ok := ch.TTFTP95[model]; ok {
		return ttft
	}
	return 9999
}

// estimateCost 估算一次请求的成本（输入输出分开计价）：
// 1. 实测倍率（official）：输入价 = 倍率 × 官网输入价，输出价 = 倍率 × 官网输出价
//    （优先使用探测当时记录的官网价快照，价格库调整不影响历史倍率；快照缺失时回退价格库当前值）
// 2. 实测倍率（baseline）：输入价 = 输出价 = 倍率 × $10/1M（价格库未收录的旧口径）
// 3. 声明价格（declared_prices）
// 4. 无价格信息：保守估计输入 $10/1M、输出 $30/1M
func estimateCost(ch *ChannelHealth, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) float64 {
	var inputPrice, outputPrice float64

	if ratio, ok := ch.RealRatio[req.Model]; ok && ratio > 0 {
		if ch.RealRatioBasis[req.Model] == "official" {
			inPerM := ch.RealRatioOfficialInPerM[req.Model]
			outPerM := ch.RealRatioOfficialOutPerM[req.Model]
			if (inPerM <= 0 || outPerM <= 0) && prices != nil {
				// 旧数据没有官网价快照：回退价格库当前值
				if p, ok := prices[req.Model]; ok && p != nil {
					inPerM, outPerM = p.InputPerM, p.OutputPerM
				}
			}
			if inPerM > 0 && outPerM > 0 {
				inputPrice = ratio * inPerM / 1_000_000
				outputPrice = ratio * outPerM / 1_000_000
				return computeEstimatedCost(req, policy, inputPrice, outputPrice)
			}
		}
		// baseline 兜底：$10/1M 混合基准
		basePrice := 10.0 / 1_000_000
		inputPrice = ratio * basePrice
		outputPrice = ratio * basePrice
	} else if price, ok := ch.DeclaredPrice[req.Model]; ok {
		inputPrice = price.InputPrice / 1_000_000
		outputPrice = price.OutputPrice / 1_000_000
	} else {
		// 无价格信息，使用保守估计
		inputPrice = 10.0 / 1_000_000
		outputPrice = 30.0 / 1_000_000
	}

	return computeEstimatedCost(req, policy, inputPrice, outputPrice)
}

// computeEstimatedCost 按预估输入/输出 token 计算总成本
func computeEstimatedCost(req *FilterRequest, policy *Policy, inputPrice, outputPrice float64) float64 {
	// 读取预期输出比例
	expectedOutputRatio := policy.GetConfigFloat("expected_output_ratio", 2.0)
	maxOutput := req.MaxOutput
	if maxOutput == 0 {
		maxOutput = int(float64(req.EstimatedInput) * expectedOutputRatio)
	}

	return float64(req.EstimatedInput)*inputPrice + float64(maxOutput)*outputPrice
}
