package checker

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchGenericNestedUserQuota(t *testing.T) {
	// new-api 登录/会话响应：余额在 data.user.quota（quota 单位，应换算为美元）
	payload := map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"user": map[string]interface{}{"quota": 4775054},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	b := &BalanceChecker{client: srv.Client()}
	bal, src, err := b.fetchGeneric(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("fetchGeneric error: %v", err)
	}
	if src != "oneapi" {
		t.Fatalf("source = %q, want oneapi", src)
	}
	// 4775054 / 500000 ≈ 9.55 美元
	if bal < 9.5 || bal > 9.6 {
		t.Fatalf("balance = %v, want about 9.55 (USD)", bal)
	}
}

func TestFetchGenericStandardDataQuota(t *testing.T) {
	// 标准 /api/user/self：data.quota 小数值视为已是美元（回归保护）
	payload := map[string]interface{}{
		"success": true,
		"data":    map[string]interface{}{"quota": 123.45},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	b := &BalanceChecker{client: srv.Client()}
	bal, src, err := b.fetchGeneric(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("fetchGeneric error: %v", err)
	}
	if src != "oneapi" || bal != 123.45 {
		t.Fatalf("got source=%q balance=%v, want oneapi/123.45", src, bal)
	}
}

func TestQuotaToUSDHeuristic(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{4775054, 9.550108},  // quota 单位 → ÷500000
		{250000, 0.5},        // quota 单位
		{5.53, 5.53},         // 已是美元（小数值不动）
		{0.68, 0.68},         // 已是美元
		{123.45, 123.45},     // 已是美元
	}
	for _, c := range cases {
		got := quotaToUSD(c.in)
		if math.Abs(got-c.want) > 0.0001 {
			t.Errorf("quotaToUSD(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFetchGenericPostFallback(t *testing.T) {
	// GET 404 → 自动回退 POST，POST 返回余额
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{"user": map[string]interface{}{"quota": 2500000}},
		})
	}))
	defer srv.Close()

	b := &BalanceChecker{client: srv.Client()}
	bal, src, err := b.fetchGeneric(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("fetchGeneric error: %v", err)
	}
	// 2500000 quota → 5 USD
	if src != "oneapi" || math.Abs(bal-5.0) > 0.0001 {
		t.Fatalf("got source=%q balance=%v, want oneapi/5.0", src, bal)
	}
}
