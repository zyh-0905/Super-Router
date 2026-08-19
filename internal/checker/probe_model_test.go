package checker

import "testing"

// TestScheduledProbeModel 验证定时探针的模型选择：
// 站点默认测试模型（test_model）优先，未配置时回退全局 probe_model。
func TestScheduledProbeModel(t *testing.T) {
	p := &ProbeChecker{probeModel: "gpt-4o"}

	cases := []struct {
		name      string
		upstream  Upstream
		wantModel string
	}{
		{"站点有默认测试模型", Upstream{TestModel: "gpt-5.5"}, "gpt-5.5"},
		{"站点未设置回退全局", Upstream{TestModel: ""}, "gpt-4o"},
	}
	for _, c := range cases {
		if got := p.ScheduledProbeModel(c.upstream); got != c.wantModel {
			t.Errorf("%s: ScheduledProbeModel() = %q, want %q", c.name, got, c.wantModel)
		}
	}
}
