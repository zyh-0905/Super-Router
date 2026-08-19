package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newFakeBot 启动 Fake Telegram Bot API，返回 client 指向的服务器与控制函数。
func newFakeBot(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	// 客户端 base 固定为 api.telegram.org；用自定义 RoundTripper 把请求重写到 fake server。
	client := &Client{token: "test-token", client: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r2 := r.Clone(r.Context())
			u := *r2.URL
			u.Scheme = "http"
			u.Host = strings.TrimPrefix(srv.URL, "http://")
			u.Path = strings.TrimPrefix(u.Path, "/bottest-token")
			r2.URL = &u
			return http.DefaultTransport.RoundTrip(r2)
		}),
	}}
	return client, srv
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGetMeOK(t *testing.T) {
	client, _ := newFakeBot(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/getMe") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"SR"}}`)
	})
	if err := client.GetMe(context.Background()); err != nil {
		t.Fatalf("getMe: %v", err)
	}
}

func TestGetMeUnauthorized(t *testing.T) {
	client, _ := newFakeBot(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	})
	err := client.GetMe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("err = %v, want 401 Unauthorized", err)
	}
}

func TestGetUpdatesParsesMessages(t *testing.T) {
	client, _ := newFakeBot(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "42" {
			t.Fatalf("offset = %q, want 42", r.URL.Query().Get("offset"))
		}
		fmt.Fprint(w, `{"ok":true,"result":[
			{"update_id":43,"message":{"chat":{"id":7},"text":"/alerts"}},
			{"update_id":44,"message":{"chat":{"id":8},"text":"/relay 3"}}
		]}`)
	})
	updates, err := client.GetUpdates(context.Background(), 42, 50*time.Second)
	if err != nil {
		t.Fatalf("getUpdates: %v", err)
	}
	if len(updates) != 2 || updates[0].ChatID != 7 || updates[1].Text != "/relay 3" {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestGetUpdatesSkipsNonMessage(t *testing.T) {
	client, _ := newFakeBot(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":[{"update_id":50,"edited_message":{"chat":{"id":1}}}]}`)
	})
	updates, err := client.GetUpdates(context.Background(), 0, 30*time.Second)
	if err != nil {
		t.Fatalf("getUpdates: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("updates = %+v, want empty", updates)
	}
}

func TestSendMessagePostsHTMLAndReturnsID(t *testing.T) {
	client, _ := newFakeBot(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("parse_mode") != "HTML" {
			t.Fatalf("parse_mode = %q", r.Form.Get("parse_mode"))
		}
		if r.Form.Get("chat_id") != "12345" {
			t.Fatalf("chat_id = %q", r.Form.Get("chat_id"))
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":999}}`)
	})
	id, err := client.SendMessage(context.Background(), 12345, "<b>hi</b>")
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if id != 999 {
		t.Fatalf("message_id = %d", id)
	}
}

func TestSendMessageRateLimit(t *testing.T) {
	client, _ := newFakeBot(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`)
	})
	_, err := client.SendMessage(context.Background(), 1, "x")
	var ra *ErrRetryAfter
	if !errors.As(err, &ra) {
		t.Fatalf("err = %v, want ErrRetryAfter", err)
	}
	if ra.Seconds != 7 {
		t.Fatalf("retry after = %d, want 7", ra.Seconds)
	}
}

func TestSendMessageOKFalse(t *testing.T) {
	client, _ := newFakeBot(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	})
	_, err := client.SendMessage(context.Background(), 1, "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendMessageDecodeFailure(t *testing.T) {
	client, _ := newFakeBot(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not-json`)
	})
	_, err := client.SendMessage(context.Background(), 1, "x")
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestClientWithoutTokenFails(t *testing.T) {
	c := NewClient("")
	if err := c.GetMe(context.Background()); err == nil {
		t.Fatal("expected error without token")
	}
}

func TestGetUpdatesNetworkError(t *testing.T) {
	client := &Client{token: "t", client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
	}}
	_, err := client.GetUpdates(context.Background(), 0, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestContextCancellationPropagates(t *testing.T) {
	client := &Client{token: "t", client: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, r.Context().Err()
		}),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetUpdates(ctx, 0, 30*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// 辅助：确认响应 JSON 结构一致（避免误用）
func TestAPIResponseShape(t *testing.T) {
	var ar apiResponse
	if err := json.Unmarshal([]byte(`{"ok":true,"result":null}`), &ar); err != nil || !ar.OK {
		t.Fatalf("unmarshal: %v %+v", err, ar)
	}
}
