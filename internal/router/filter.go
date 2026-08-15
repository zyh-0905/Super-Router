package router

import (
	"time"
)

// Exclusion 排除原因
type Exclusion struct {
	ChannelID int    `json:"channel_id"`
	Reason    string `json:"reason"`
}

// 排除原因常量
const (
	ExcludeUserDisabled        = "user_disabled"
	ExcludeModelNotSupported   = "model_not_supported"
	ExcludeCapabilityMissing   = "capability_missing"
	ExcludeCredentialInvalid   = "credential_invalid"
	ExcludeQuotaExhausted      = "quota_exhausted"
	ExcludeOverPriceCap        = "over_price_cap"
	ExcludeCircuitOpen         = "circuit_open"
	ExcludeCircuitCooling      = "circuit_cooling"
	ExcludeCircuitHalfOpen     = "circuit_half_open"
	ExcludeLatencyCapExceeded  = "latency_cap_exceeded"
	ExcludeRegionMismatch      = "region_mismatch"
	ExcludeProtocolUnsupported = "protocol_unsupported"
)

// FilterRequest 过滤请求参数
type FilterRequest struct {
	Model          string
	Capabilities   []string
	EstimatedInput int
	MaxOutput      int
}

// HardFilter 硬过滤器
type HardFilter struct {
	policy *Policy
	prices map[string]*ModelPrice
}

func NewHardFilter(policy *Policy, prices map[string]*ModelPrice) *HardFilter {
	return &HardFilter{policy: policy, prices: prices}
}

// Filter 执行硬过滤，返回排除原因（nil 表示通过）
func (f *HardFilter) Filter(ch *ChannelHealth, req *FilterRequest) *Exclusion {
	// 1. 渠道被用户禁用或全局禁用
	if !ch.Enabled {
		return &Exclusion{
			ChannelID: ch.ID,
			Reason:    ExcludeUserDisabled,
		}
	}

	// 2. 渠道没有当前模型的有效映射
	if ch.ModelMapping == nil || ch.ModelMapping[req.Model] == "" {
		return &Exclusion{
			ChannelID: ch.ID,
			Reason:    ExcludeModelNotSupported,
		}
	}

	// 3. 渠道不支持请求所需能力
	if len(req.Capabilities) > 0 {
		channelCaps := make(map[string]bool)
		for _, cap := range ch.Capabilities {
			channelCaps[cap] = true
		}

		for _, reqCap := range req.Capabilities {
			if !channelCaps[reqCap] {
				return &Exclusion{
					ChannelID: ch.ID,
					Reason:    ExcludeCapabilityMissing,
				}
			}
		}
	}

	// 4. 渠道凭证不存在、失效或权限不足
	// （此项在运行时才能判断，这里暂时跳过，后续在实际调用时判断）

	// 5. 渠道达到并发、配额或速率限制
	// （此项需要运行时状态，这里暂时跳过）

	// 6. 预计成本超过策略价格上限
	maxPriceCap := f.policy.GetConfigFloat("max_price_cap", 100.0)
	estimatedCost := estimateCost(ch, req, f.policy, f.prices)
	if estimatedCost > maxPriceCap {
		return &Exclusion{
			ChannelID: ch.ID,
			Reason:    ExcludeOverPriceCap,
		}
	}

	// 7. 熔断状态不允许正常流量
	switch ch.CircuitState {
	case "open":
		// 冷却未到期的开闸：禁止流量（冷却到期后快照会将其视为 half_open）
		return &Exclusion{
			ChannelID: ch.ID,
			Reason:    ExcludeCircuitOpen,
		}
	case "half_open":
		// 半开：放行探测流量，由代理层按 half_open_probe_count 限额。
		// 不能在过滤层排除，否则 open → half_open 后永远没有流量，熔断无法自愈。
	case "cooling":
		if time.Now().Before(ch.CoolingUntil) {
			return &Exclusion{
				ChannelID: ch.ID,
				Reason:    ExcludeCircuitCooling,
			}
		}
	}

	// 8. 延迟上限（latency_first 策略特有）
	if f.policy.Strategy == "latency_first" {
		maxTTFTMS := f.policy.GetConfigInt("max_ttft_ms", 5000)
		if ttft, ok := ch.TTFTP50[req.Model]; ok && ttft > maxTTFTMS {
			return &Exclusion{
				ChannelID: ch.ID,
				Reason:    ExcludeLatencyCapExceeded,
			}
		}
	}

	// 通过所有过滤
	return nil
}
