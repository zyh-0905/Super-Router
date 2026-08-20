package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTelegramTestRouter(h *TelegramHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	adminGroup := r.Group("/admin")
	adminGroup.Use(func(c *gin.Context) { // 简化认证：非 admin 拒绝
		if c.GetHeader("X-Test-Role") != "admin" {
			c.JSON(403, gin.H{"error": "requires admin role"})
			c.Abort()
			return
		}
		c.Set("key_hash", "test-hash")
		c.Next()
	})
	adminGroup.GET("/telegram/config", h.GetConfig)
	adminGroup.PATCH("/telegram/config", h.UpdateConfig)
	adminGroup.GET("/telegram/subscribers", h.ListSubscribers)
	adminGroup.POST("/telegram/subscribers", h.CreateSubscriber)
	adminGroup.PATCH("/telegram/subscribers/:id", h.UpdateSubscriber)
	adminGroup.DELETE("/telegram/subscribers/:id", h.DeleteSubscriber)
	adminGroup.GET("/telegram/delivery-logs", h.GetDeliveryLogs)
	return r
}

func doReq(t *testing.T, r *gin.Engine, method, path string, body interface{}, role string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if role != "" {
		req.Header.Set("X-Test-Role", role)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestTelegramNonAdminForbidden 非 admin 全部拒绝。
func TestTelegramNonAdminForbidden(t *testing.T) {
	r := newTelegramTestRouter(&TelegramHandler{})
	for _, c := range []struct{ m, p string }{
		{"GET", "/admin/telegram/config"},
		{"PATCH", "/admin/telegram/config"},
		{"GET", "/admin/telegram/subscribers"},
		{"POST", "/admin/telegram/subscribers"},
		{"GET", "/admin/telegram/delivery-logs"},
	} {
		if w := doReq(t, r, c.m, c.p, nil, "caller"); w.Code != 403 {
			t.Fatalf("%s %s = %d, want 403", c.m, c.p, w.Code)
		}
	}
}

// TestTelegramConfigValidation 配置校验纯函数（无需 DB）。
func TestTelegramConfigValidation(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	strPtr := func(v string) *string { return &v }
	cases := []struct {
		name    string
		req     telegramConfigPatch
		wantErr string
	}{
		{"interval must be positive", telegramConfigPatch{ReportIntervalMinutes: intPtr(0)}, "report_interval_minutes"},
		{"interval negative", telegramConfigPatch{ReportIntervalMinutes: intPtr(-5)}, "report_interval_minutes"},
		{"minute out of range", telegramConfigPatch{ReportMinute: intPtr(60)}, "report_minute"},
		{"minute negative", telegramConfigPatch{ReportMinute: intPtr(-1)}, "report_minute"},
		{"bad timezone", telegramConfigPatch{Timezone: strPtr("Mars/Olympus")}, "timezone"},
		{"valid payload", telegramConfigPatch{ReportIntervalMinutes: intPtr(30), ReportMinute: intPtr(15), Timezone: strPtr("Asia/Shanghai")}, ""},
	}
	for _, c := range cases {
		err := validateTelegramConfigPatch(c.req)
		if c.wantErr == "" && err != nil {
			t.Fatalf("%s: unexpected error %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Fatalf("%s: err = %v, want contains %q", c.name, err, c.wantErr)
		}
	}
}

// TestTelegramConfigResponseMasking 响应脱敏结构（不依赖 DB 的字段组装纯函数）。
func TestTelegramConfigResponseMasking(t *testing.T) {
	resp := telegramConfigResponse{
		Enabled:               false,
		BotConfigured:         true,
		BotTokenSuffix:        "abcd",
		ReportEnabled:         true,
		ReportIntervalMinutes: 60,
		ReportMinute:          0,
		Timezone:              "Asia/Shanghai",
	}
	b, _ := json.Marshal(resp)
	s := string(b)
	if strings.Contains(s, "sk-") || strings.Contains(s, "token") && strings.Contains(s, "12345:") {
		t.Fatalf("token leaked: %s", s)
	}
	if !strings.Contains(s, `"bot_token_suffix":"abcd"`) {
		t.Fatalf("suffix missing: %s", s)
	}
	if !strings.Contains(s, `"bot_configured":true`) {
		t.Fatalf("configured flag missing: %s", s)
	}
}


func TestNormalizeSubscriberChat(t *testing.T) {
	tests := []struct {
		name string
		id int64
		requested string
		want string
		wantErr bool
	}{
		{name: "positive auto private", id: 123456789, want: "private"},
		{name: "negative auto group", id: -123456789, want: "group"},
		{name: "negative one hundred auto supergroup", id: -1001234567890, want: "supergroup"},
		{name: "explicit channel", id: -1001234567890, requested: "channel", want: "channel"},
		{name: "zero rejected", id: 0, wantErr: true},
		{name: "positive group mismatch", id: 123, requested: "group", wantErr: true},
		{name: "negative private mismatch", id: -123, requested: "private", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeSubscriberChat(tt.id, tt.requested)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got type %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("type = %q, want %q", got, tt.want)
			}
		})
	}
}
