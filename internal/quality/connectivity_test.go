package quality

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newModelsServer 启动 /v1/models mock，记录认证头。
func newModelsServer(t *testing.T, status int, body string) (*httptest.Server, *[]http.Header) {
	t.Helper()
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Clone()
		headers = append(headers, h)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &headers
}

func TestConnectivityOpenAIBearer(t *testing.T) {
	srv, headers := newModelsServer(t, 200, `{"data":[{"id":"gpt-4o"}]}`)
	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	res := RunConnectivity(context.Background(), ch, 10*time.Second)
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, err = %s", res.Status, res.Error)
	}
	if res.HTTPStatus == nil || *res.HTTPStatus != 200 {
		t.Fatalf("http_status = %v", res.HTTPStatus)
	}
	if len(*headers) == 0 || (*headers)[0].Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("auth header = %v", headers)
	}
	// 凭据绝不写入 result.Error 或 details
	if strings.Contains(res.Error, "sk-test") || containsCredential(res.Details) {
		t.Fatalf("credential leaked into result: %+v", res)
	}
}

func TestConnectivityAnthropicHeaders(t *testing.T) {
	srv, headers := newModelsServer(t, 200, `{"data":[]}`)
	ch := &Channel{BaseURL: srv.URL, Protocol: "anthropic", APIKey: "sk-ant-test"}
	res := RunConnectivity(context.Background(), ch, 10*time.Second)
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, err = %s", res.Status, res.Error)
	}
	if len(*headers) == 0 {
		t.Fatal("no headers captured")
	}
	if (*headers)[0].Get("x-api-key") != "sk-ant-test" {
		t.Fatalf("x-api-key = %q", (*headers)[0].Get("x-api-key"))
	}
	if (*headers)[0].Get("anthropic-version") == "" {
		t.Fatal("anthropic-version missing")
	}
}

func TestConnectivityUnauthorized(t *testing.T) {
	srv, _ := newModelsServer(t, 401, `{"error":{"message":"bad key"}}`)
	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	res := RunConnectivity(context.Background(), ch, 10*time.Second)
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if res.HTTPStatus == nil || *res.HTTPStatus != 401 {
		t.Fatalf("http_status = %v", res.HTTPStatus)
	}
}

func TestConnectivityModels404ButChatAvailable(t *testing.T) {
	// 404 模型列表 → attention + models_endpoint_unavailable（后续聊天检测可继续）
	srv, _ := newModelsServer(t, 404, `{"error":"not found"}`)
	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	res := RunConnectivity(context.Background(), ch, 10*time.Second)
	if res.Status != StatusAttention {
		t.Fatalf("status = %s, want attention", res.Status)
	}
	if code, _ := res.Details["code"].(string); code != "models_endpoint_unavailable" {
		t.Fatalf("details = %+v", res.Details)
	}
}

func TestConnectivityTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	res := RunConnectivity(context.Background(), ch, 500*time.Millisecond)
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed on timeout", res.Status)
	}
}

func containsCredential(details map[string]interface{}) bool {
	s, _ := json.Marshal(details)
	txt := string(s)
	return strings.Contains(txt, "sk-") || strings.Contains(txt, "Bearer")
}
