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
	cases := []struct {
		name    string
		payload map[string]interface{}
		wantErr string
	}{
		{"interval must be positive", map[string]interface{}{"report_interval_minutes": float64(0)}, "report_interval_minutes"},
		{"interval negative", map[string]interface{}{"report_interval_minutes": float64(-5)}, "report_interval_minutes"},
		{"minute out of range", map[string]interface{}{"report_minute": float64(60)}, "report_minute"},
		{"minute negative", map[string]interface{}{"report_minute": float64(-1)}, "report_minute"},
		{"bad timezone", map[string]interface{}{"timezone": "Mars/Olympus"}, "timezone"},
		{"valid payload", map[string]interface{}{"report_interval_minutes": float64(30), "report_minute": float64(15), "timezone": "Asia/Shanghai"}, ""},
	}
	for _, c := range cases {
		err := validateTelegramConfigPatch(c.payload)
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
