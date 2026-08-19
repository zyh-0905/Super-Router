package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"smart-router/internal/crypto"
	"smart-router/internal/store"

	"go.uber.org/zap"
)

// Executor 质量检测执行器：加载站点 → 模型解析 → 按阶段顺序执行 → 归纳总体结论。
// HTTP Handler 不执行长时间上游检测；Executor 只被 Checker Quality Worker 调用。
type Executor struct {
	DB         *store.DB
	Repo       Repository
	Publisher  Publisher
	HTTPClient *http.Client
	CryptoKey  string
	ProbeModel string
	AlertSink  AlertSink
	Logger     *zap.Logger
}

// NewExecutor 创建执行器（AlertSink 默认 Noop，Task 6 替换为 internal/alert 实现）。
func NewExecutor(db *store.DB, repo Repository, pub Publisher, cryptoKey, probeModel string, logger *zap.Logger) *Executor {
	if logger == nil {
		logger = zap.NewNop()
	}
	if pub == nil {
		pub = nilPublisher{}
	}
	return &Executor{
		DB:         db,
		Repo:       repo,
		Publisher:  pub,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
		CryptoKey:  cryptoKey,
		ProbeModel: probeModel,
		AlertSink:  NoopAlertSink{},
		Logger:     logger,
	}
}

// nilPublisher 无事件发布的兜底。
type nilPublisher struct{}

func (nilPublisher) Publish(context.Context, Event) error { return nil }

// unmarshalJSON 解析 JSON 字符串到目标（空串按零值）。
func unmarshalJSON(s string, v interface{}) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}

// LoadChannel 从 upstreams 读取单站点配置并解密凭据。
// 解密失败不写入 details，只返回通用 credential_decrypt_failed。
func (e *Executor) LoadChannel(ctx context.Context, channelID int) (*Channel, error) {
	var ch Channel
	var mappingJSON, capabilitiesJSON string
	err := e.DB.Pool.QueryRow(ctx, `
		SELECT id, name, base_url, COALESCE(protocol, 'openai'), COALESCE(relay_type, ''),
		       COALESCE(test_model, ''), api_key, access_token,
		       COALESCE(balance_api_url, ''), COALESCE(balance_api_token, ''),
		       COALESCE(model_mapping::text, '{}'), COALESCE(capabilities::text, '[]'),
		       timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms
		FROM upstreams WHERE id = $1
	`, channelID).Scan(&ch.ID, &ch.Name, &ch.BaseURL, &ch.Protocol, &ch.RelayType,
		&ch.TestModel, &ch.APIKey, &ch.AccessToken, &ch.BalanceAPIURL, &ch.BalanceAPIToken,
		&mappingJSON, &capabilitiesJSON,
		&ch.TimeoutConnectMS, &ch.TimeoutFirstByteMS, &ch.TimeoutTotalMS)
	if err != nil {
		return nil, fmt.Errorf("load channel: %w", err)
	}
	if err := unmarshalJSON(mappingJSON, &ch.ModelMapping); err != nil || ch.ModelMapping == nil {
		ch.ModelMapping = map[string]string{}
	}
	if err := unmarshalJSON(capabilitiesJSON, &ch.Capabilities); err != nil {
		ch.Capabilities = []string{}
	}

	var derr error
	if ch.APIKey, derr = crypto.Decrypt(ch.APIKey, e.CryptoKey); derr != nil {
		return nil, fmt.Errorf("credential_decrypt_failed")
	}
	if ch.AccessToken, derr = crypto.Decrypt(ch.AccessToken, e.CryptoKey); derr != nil {
		return nil, fmt.Errorf("credential_decrypt_failed")
	}
	if ch.BalanceAPIToken, derr = crypto.Decrypt(ch.BalanceAPIToken, e.CryptoKey); derr != nil {
		return nil, fmt.Errorf("credential_decrypt_failed")
	}
	if ch.APIKey == "" {
		ch.APIKey = ch.AccessToken
	}
	return &ch, nil
}

// StageNames 按 depth 返回阶段列表。
func StageNames(depth string) []string {
	if depth == "basic" {
		return BasicStages
	}
	return FullStages
}

// Execute 执行一个任务：固定阶段顺序，每阶段之间检查取消；
// 阶段开始/结束都更新 Repository 与 Publisher；basic 在 StreamStage 后完成。
func (e *Executor) Execute(ctx context.Context, run *Run) error {
	ch, err := e.LoadChannel(ctx, run.ChannelID)
	if err != nil {
		return fmt.Errorf("load channel: %w", err)
	}

	// 模型解析：显式（任务自带）→ test_model → probe_model → 首映射
	model, err := ResolveModel(ch, run.Model, e.ProbeModel)
	if err != nil {
		return fmt.Errorf("resolve model: %w", err)
	}
	if run.Model == "" || run.Model != model {
		// 任务创建时已校验；这里兜底修正任务模型字段
		run.Model = model
	}

	stages := StageNames(run.Depth)
	sc := &StageContext{Run: run, Channel: ch}

	// 阶段注册表：connectivity 由本文件实现；chat/behavior 由 Task 4 注入
	registry := e.stageRegistry()

	step := 0
	for _, name := range stages {
		// 取消检查（阶段之间）
		cancelRequested, _ := e.Repo.IsCancelRequested(ctx, run.ID)
		if cancelRequested {
			_ = e.Repo.Cancel(ctx, run.ID)
			e.publish(ctx, run.ID, "task_cancelled", name, 0, nil)
			return nil
		}

		stage, ok := registry[name]
		if !ok {
			// 未实现阶段（如 usage/behavior 在 Task 4 之前）→ skipped
			res := StageResult{Stage: name, CheckName: name, Status: StatusSkipped,
				Details: map[string]interface{}{"reason": "stage_not_implemented"}}
			if err := e.Repo.UpsertResult(ctx, run.ID, res); err != nil {
				e.Logger.Warn("Upsert skipped stage failed", zap.Error(err))
			}
			step++
			e.publish(ctx, run.ID, "stage_result", name, progressOf(step, len(stages)), res)
			continue
		}

		// 阶段开始
		if err := e.Repo.SetProgress(ctx, run.ID, name, progressOf(step, len(stages))); err != nil {
			e.Logger.Warn("SetProgress failed", zap.Error(err))
		}
		e.publish(ctx, run.ID, "stage_started", name, progressOf(step, len(stages)), nil)

		// 阶段执行（内部 HTTP 请求使用 ctx 取消）
		res := stage.Run(ctx, sc)
		if res.CheckName == "" {
			res.CheckName = name
		}
		if err := e.Repo.UpsertResult(ctx, run.ID, res); err != nil {
			e.Logger.Warn("Upsert stage result failed", zap.Error(err))
		}
		step++
		e.publish(ctx, run.ID, "stage_result", name, progressOf(step, len(stages)), res)

		// 阶段之间再次检查取消
		cancelRequested, _ = e.Repo.IsCancelRequested(ctx, run.ID)
		if cancelRequested {
			_ = e.Repo.Cancel(ctx, run.ID)
			e.publish(ctx, run.ID, "task_cancelled", name, 0, nil)
			return nil
		}
	}

	// 归纳总体结论
	_, results, err := e.Repo.Get(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("load results for summary: %w", err)
	}
	overall := Summarize(results)
	if err := e.Repo.Complete(ctx, run.ID, overall); err != nil {
		return fmt.Errorf("complete run: %w", err)
	}
	e.publish(ctx, run.ID, "task_completed", "", 100, map[string]interface{}{"overall": string(overall)})

	// 质量失败告警（Task 6 接入；当前 Noop）
	if overall == OverallFailed {
		failedStages := []string{}
		passedStages := []string{}
		for _, r := range results {
			if criticalStages[r.Stage] && r.Status == StatusFailed {
				failedStages = append(failedStages, r.Stage)
			}
			if r.Status == StatusPassed {
				passedStages = append(passedStages, r.Stage)
			}
		}
		for _, fs := range failedStages {
			_ = e.AlertSink.QualityFailure(ctx, ch.ID, model, fs, "quality check failed", map[string]interface{}{})
		}
		if len(failedStages) == 0 {
			_ = e.AlertSink.ResolveQualityFailures(ctx, ch.ID, model, passedStages)
		}
	} else {
		passedStages := []string{}
		for _, r := range results {
			if r.Status == StatusPassed {
				passedStages = append(passedStages, r.Stage)
			}
		}
		_ = e.AlertSink.ResolveQualityFailures(ctx, ch.ID, model, passedStages)
	}
	return nil
}

// publish 发布事件（Redis 失败不影响任务继续）。
func (e *Executor) publish(ctx context.Context, runID int64, typ, stage string, progress int, result interface{}) {
	ev := Event{Type: typ, RunID: PublicRunID(runID), Stage: stage, Progress: progress, Result: result}
	if err := e.Publisher.Publish(ctx, ev); err != nil {
		e.Logger.Debug("Publish event failed", zap.String("type", typ), zap.Error(err))
	}
}

// progressOf 阶段进度（阶段间线性分布）。
func progressOf(step, total int) int {
	if total <= 0 {
		return 0
	}
	return step * 100 / total
}

// stageRegistry 阶段注册表（Task 4 的 chat/behavior 阶段在 executor_chat.go 中扩展）。
func (e *Executor) stageRegistry() map[string]Stage {
	registry := map[string]Stage{
		StageConnectivity: connectivityStage{executor: e},
	}
	return registry
}

// connectivityStage 包装 RunConnectivity。
type connectivityStage struct {
	executor *Executor
}

func (s connectivityStage) Name() string { return StageConnectivity }

func (s connectivityStage) Run(ctx context.Context, input *StageContext) StageResult {
	timeout := 10 * time.Second
	if input.Channel.TimeoutTotalMS > 0 {
		t := time.Duration(input.Channel.TimeoutTotalMS) * time.Millisecond
		if t < timeout {
			timeout = t
		}
	}
	return RunConnectivity(ctx, input.Channel, timeout)
}
