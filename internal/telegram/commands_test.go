package telegram

import (
	"context"
	"strings"
	"testing"
)

// fakeQueryService 记录调用并返回固定文本。
type fakeQueryService struct {
	lastMethod string
	lastID     int
	lastGroups []int
	results    map[string]string
}

func (f *fakeQueryService) RelayList(ctx context.Context, groupIDs []int) (string, error) {
	f.lastMethod, f.lastGroups = "relay_list", groupIDs
	return f.results["relay_list"], nil
}
func (f *fakeQueryService) RelayDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "relay_detail", id, groupIDs
	return f.results["relay_detail"], nil
}
func (f *fakeQueryService) BalanceList(ctx context.Context, groupIDs []int) (string, error) {
	f.lastMethod, f.lastGroups = "balance_list", groupIDs
	return f.results["balance_list"], nil
}
func (f *fakeQueryService) BalanceDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "balance_detail", id, groupIDs
	return f.results["balance_detail"], nil
}
func (f *fakeQueryService) HealthList(ctx context.Context, groupIDs []int) (string, error) {
	f.lastMethod, f.lastGroups = "health_list", groupIDs
	return f.results["health_list"], nil
}
func (f *fakeQueryService) HealthDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "health_detail", id, groupIDs
	return f.results["health_detail"], nil
}
func (f *fakeQueryService) RatioList(ctx context.Context, groupIDs []int) (string, error) {
	f.lastMethod, f.lastGroups = "ratio_list", groupIDs
	return f.results["ratio_list"], nil
}
func (f *fakeQueryService) RatioDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	f.lastMethod, f.lastID, f.lastGroups = "ratio_detail", id, groupIDs
	return f.results["ratio_detail"], nil
}

const unauthorizedText = "⛔ 当前 Chat ID 未授权，请联系管理员。"

func TestUnknownChatIDRejected(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	out, err := svc.Handle(context.Background(), 999, "/alerts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
	if q.lastMethod != "" {
		t.Fatalf("query executed for unauthorized chat: %s", q.lastMethod)
	}
}

func TestDisabledSubscriberRejected(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: 7, Enabled: false, AlertEnabled: true, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "/alerts")
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
}

func TestQueryDisabledRejectsQueries(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: 7, Enabled: true, AlertEnabled: true, QueryEnabled: false},
	})
	out, _ := svc.Handle(context.Background(), 7, "/relay")
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
}

func TestEmptyGroupIDsCanSeeAll(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_list": "ALL"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: 7, Enabled: true, AlertEnabled: true, QueryEnabled: true, GroupIDs: []int{}},
	})
	out, _ := svc.Handle(context.Background(), 7, "/relay")
	if out != "ALL" {
		t.Fatalf("out = %q", out)
	}
	if len(q.lastGroups) != 0 {
		t.Fatalf("groups = %v, want empty (all)", q.lastGroups)
	}
}

func TestBoundGroupsPassedToQuery(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_list": "FILTERED"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: 7, Enabled: true, AlertEnabled: true, QueryEnabled: true, GroupIDs: []int{2}},
	})
	out, _ := svc.Handle(context.Background(), 7, "/relay")
	if out != "FILTERED" {
		t.Fatalf("out = %q", out)
	}
	if len(q.lastGroups) != 1 || q.lastGroups[0] != 2 {
		t.Fatalf("groups = %v, want [2]", q.lastGroups)
	}
}

func TestCommandWithBotnameSuffix(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"relay_detail": "DETAIL"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: 7, Enabled: true, AlertEnabled: true, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "/relay@sr_bot 3")
	if out != "DETAIL" {
		t.Fatalf("out = %q", out)
	}
	if q.lastID != 3 {
		t.Fatalf("id = %d, want 3", q.lastID)
	}
}

func TestEmptyArgCommandsReturnHelp(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: 7, Enabled: true, AlertEnabled: true, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "")
	if !strings.Contains(out, "/alerts") || !strings.Contains(out, "/relay") {
		t.Fatalf("help missing commands: %s", out)
	}
	out, _ = svc.Handle(context.Background(), 7, "/help")
	if !strings.Contains(out, "/balance") {
		t.Fatalf("help missing balance: %s", out)
	}
}

func TestUnknownCommandReturnsHelp(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: 7, Enabled: true, AlertEnabled: true, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "/quality 5")
	// 计划 A 阶段 /quality 未接入：未知命令返回帮助，不伪造质量结果
	if !strings.Contains(out, "/alerts") {
		t.Fatalf("unknown command should return help: %s", out)
	}
}

func TestAlertCommandRequiresAlertEnabled(t *testing.T) {
	q := &fakeQueryService{results: map[string]string{"alerts": "ALERTS"}}
	svc := NewCommandService(q)
	svc.SetSubscribers([]Subscriber{
		{ID: 1, ChatID: 7, Enabled: true, AlertEnabled: false, QueryEnabled: true},
	})
	out, _ := svc.Handle(context.Background(), 7, "/alerts")
	if out != unauthorizedText {
		t.Fatalf("out = %q", out)
	}
}
