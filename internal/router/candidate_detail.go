package router

import (
	"math"
)

// CandidateDetail 决策候选的六维评分（0-100，越大越好，无数据取中性 50）
type CandidateDetail struct {
	ChannelID int                `json:"channel_id"`
	Dims      map[string]float64 `json:"dims"`
	// Raw 该候选的真实指标（供前端直观展示：费用/延迟/成功率/负载/角色/策略原始分）
	Raw map[string]interface{} `json:"raw,omitempty"`
}

// 六维评分维度键
const (
	DimCost        = "cost"        // 成本优势
	DimReliability = "reliability" // 可靠性
	DimLatency     = "latency"     // 延迟
	DimLoad        = "load"        // 负载
	DimPriority    = "priority"    // 优先级
	DimComposite   = "composite"   // 综合得分
)

// DimOrder 六维展示顺序
var DimOrder = []string{DimCost, DimReliability, DimLatency, DimLoad, DimPriority, DimComposite}

// buildCandidateDetails 计算每个候选的六维评分（0-100，越大越好）：
// 成本 = 1 - cost/maxCost；可靠性 = ReliabilityScore；
// 延迟 = 1 - ttft/maxTTFT（无数据中性 50）；负载 = 1 - attempts/maxAttempts；
// 优先级 = (role×100+user_priority)/max；综合 = 策略得分相对最优比
// （compositeRelative：接近最优的候选得到接近 100 的分，不再被 min-max 硬压成 0）。
func buildCandidateDetails(scored []CandidateScore, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) []CandidateDetail {
	if len(scored) == 0 {
		return []CandidateDetail{}
	}

	// 归一化基准
	maxCost := 0.0
	maxTTFT := 0
	maxLoad := 0
	maxPriority := 0.0
	bestScore := scored[0].Score
	for _, s := range scored {
		if c := estimateCost(s.Channel, req, policy, prices); c > maxCost {
			maxCost = c
		}
		if tt := getTTFT(s.Channel, req.Model); tt < 9999 && tt > maxTTFT {
			maxTTFT = tt
		}
		if s.Channel.RecentAttempts > maxLoad {
			maxLoad = s.Channel.RecentAttempts
		}
		if p := float64(getRoleLevel(s.Channel.Role))*100 + float64(s.Channel.UserPriority); p > maxPriority {
			maxPriority = p
		}
		if s.Score > bestScore {
			bestScore = s.Score
		}
	}

	details := make([]CandidateDetail, 0, len(scored))
	for _, s := range scored {
		ch := s.Channel
		dims := map[string]float64{}

		// 成本优势
		if maxCost > 0 {
			dims[DimCost] = clamp100((1 - estimateCost(ch, req, policy, prices)/maxCost) * 100)
		} else {
			dims[DimCost] = 100
		}
		// 可靠性（本身 0-1）
		dims[DimReliability] = clamp100(ch.ReliabilityScore * 100)
		// 延迟：无数据取中性 50
		tt := getTTFT(ch, req.Model)
		switch {
		case tt >= 9999:
			dims[DimLatency] = 50
		case maxTTFT > 0:
			dims[DimLatency] = clamp100((1 - float64(tt)/float64(maxTTFT)) * 100)
		default:
			dims[DimLatency] = 100
		}
		// 负载：越空闲越高
		if maxLoad > 0 {
			dims[DimLoad] = clamp100((1 - float64(ch.RecentAttempts)/float64(maxLoad)) * 100)
		} else {
			dims[DimLoad] = 100
		}
		// 优先级
		if p := float64(getRoleLevel(ch.Role))*100 + float64(ch.UserPriority); maxPriority > 0 {
			dims[DimPriority] = clamp100(p / maxPriority * 100)
		} else {
			dims[DimPriority] = 50
		}
		// 综合得分：相对最优比（见 compositeRelative）
		dims[DimComposite] = compositeRelative(s.Score, bestScore)

		details = append(details, CandidateDetail{ChannelID: ch.ID, Dims: dims, Raw: rawDetail(ch, req, policy, prices, s.Score)})
	}
	return details
}

// rawDetail 生成候选的真实指标快照（决策时定格，随 decision_logs 持久化）：
// 费用（美元）、首字节延迟（ms，无数据省略）、成功率、近期请求数、角色/优先级、策略原始分。
// 前端据此展示「$0.00042 · 480ms · 98.5%」等直观数值，而非需要公式才能读懂的 0-100 分。
func rawDetail(ch *ChannelHealth, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice, strategyScore float64) map[string]interface{} {
	raw := map[string]interface{}{
		"role":            ch.Role,
		"user_priority":   ch.UserPriority,
		"reliability":     round6(ch.ReliabilityScore),
		"recent_attempts": ch.RecentAttempts,
		"strategy_score":  round6(strategyScore),
		"cost_usd":        round6(estimateCost(ch, req, policy, prices)),
	}
	if tt := getTTFT(ch, req.Model); tt < 9999 {
		raw["ttft_ms"] = tt
	}
	return raw
}

func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

// compositeRelative 综合分的「相对最优比」：
//   - 正分策略（手动优先级/高可靠/加权均衡，分数越大越好）：score/best × 100；
//   - 负分策略（低价优先 = -费用、低延迟优先 = -TTFT，接近 0 最优）：best/score × 100；
//   - best == 0：与最优并列（score == 0）记 100，其余记 0。
//
// 与 min-max 归一化的区别：差距 0.1% 的两个候选显示为 100 与 99.9，
// 而不是被硬压成 100 与 0，避免"微小差距看起来天壤之别"的误导。
func compositeRelative(score, best float64) float64 {
	switch {
	case best > 0:
		if score <= 0 {
			return 0
		}
		return clamp100(score / best * 100)
	case best < 0:
		if score >= 0 {
			return 100 // 防御分支：理论上不会出现
		}
		return clamp100(best / score * 100)
	default: // best == 0
		if score == 0 {
			return 100
		}
		return 0
	}
}

func clamp100(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
