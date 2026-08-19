package checker

import "testing"

// TestSupportsPricingEndpoint 验证按中转站类型决定是否调用 /api/pricing
// （sub2api/custom 不支持，newapi 支持）。
func TestSupportsPricingEndpoint(t *testing.T) {
	cases := map[string]bool{
		"newapi":  true,
		"sub2api": false,
		"custom":  false,
		"":        false, // 未填类型视为不支持
		"unknown": false,
	}
	for relayType, want := range cases {
		if got := supportsPricingEndpoint(relayType); got != want {
			t.Errorf("supportsPricingEndpoint(%q) = %v, want %v", relayType, got, want)
		}
	}
}
