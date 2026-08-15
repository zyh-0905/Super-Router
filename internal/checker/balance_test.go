package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchGenericNestedUserQuota(t *testing.T) {
	// new-api 登录/会话响应：余额在 data.user.quota
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
	if bal != 4775054 {
		t.Fatalf("balance = %v, want 4775054", bal)
	}
}

func TestFetchGenericStandardDataQuota(t *testing.T) {
	// 标准 /api/user/self：data.quota 直接挂余额（回归保护）
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
