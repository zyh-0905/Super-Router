package router

// CandidateDetail 决策候选的六维评分（0-100，越大越好，无数据取中性 50）
type CandidateDetail struct {
	ChannelID int                `json:"channel_id"`
	Dims      map[string]float64 `json:"dims"`
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
// 优先级 = (role×100+user_priority)/max；综合 = 策略得分 min-max 归一化。
func buildCandidateDetails(scored []CandidateScore, req *FilterRequest, policy *Policy, prices map[string]*ModelPrice) []CandidateDetail {
	if len(scored) == 0 {
		return []CandidateDetail{}
	}

	// 归一化基准
	maxCost := 0.0
	maxTTFT := 0
	maxLoad := 0
	maxPriority := 0.0
	minScore, maxScore := scored[0].Score, scored[0].Score
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
		if s.Score < minScore {
			minScore = s.Score
		}
		if s.Score > maxScore {
			maxScore = s.Score
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
		// 综合得分（可为负，min-max 归一化）
		if maxScore > minScore {
			dims[DimComposite] = clamp100((s.Score - minScore) / (maxScore - minScore) * 100)
		} else {
			dims[DimComposite] = 50
		}

		details = append(details, CandidateDetail{ChannelID: ch.ID, Dims: dims})
	}
	return details
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
