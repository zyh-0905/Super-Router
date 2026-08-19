package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"smart-router/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChannelBusy 同站点已有活跃任务（HTTP 409 依据）。
type ErrChannelBusy struct {
	ExistingRunID int64
}

func (e *ErrChannelBusy) Error() string {
	return fmt.Sprintf("channel already has an active quality check (run %d)", e.ExistingRunID)
}

// Repository 质量任务持久化接口。
type Repository interface {
	Create(ctx context.Context, channelID int, model, depth, requesterHash string) (*Run, error)
	Get(ctx context.Context, id int64) (*Run, []StageResult, error)
	ListByChannel(ctx context.Context, channelID, limit int) ([]Run, error)
	FindActiveByChannel(ctx context.Context, channelID int) (*Run, error)
	ClaimNext(ctx context.Context, workerID string) (*Run, error)
	UpsertResult(ctx context.Context, runID int64, result StageResult) error
	SetProgress(ctx context.Context, runID int64, stage string, progress int) error
	Heartbeat(ctx context.Context, runID int64, workerID string) error
	RequestCancel(ctx context.Context, runID int64) error
	IsCancelRequested(ctx context.Context, runID int64) (bool, error)
	Complete(ctx context.Context, runID int64, overall OverallStatus) error
	Fail(ctx context.Context, runID int64, message string) error
	Cancel(ctx context.Context, runID int64) error
	RecoverStale(ctx context.Context, olderThan time.Time, maxAttempts int) (int64, error)
}

// PostgresRepository Repository 的 PostgreSQL 实现。
type PostgresRepository struct {
	Pool *pgxpool.Pool
}

// NewPostgresRepository 创建 Repository（db 为 nil 时返回 nil，便于测试注入）。
func NewPostgresRepository(db *store.DB) *PostgresRepository {
	if db == nil {
		return nil
	}
	return &PostgresRepository{Pool: db.Pool}
}

// Create 创建任务；捕获活跃任务唯一索引冲突并转换为 ErrChannelBusy。
func (r *PostgresRepository) Create(ctx context.Context, channelID int, model, depth, requesterHash string) (*Run, error) {
	var id int64
	err := r.Pool.QueryRow(ctx, `
		INSERT INTO quality_check_runs (channel_id, model, depth, status, requested_by_key_hash)
		VALUES ($1, $2, $3, 'queued', $4)
		RETURNING id
	`, channelID, model, depth, requesterHash).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// 部分唯一索引冲突 → 找到已有活跃任务
			active, ferr := r.FindActiveByChannel(ctx, channelID)
			if ferr == nil && active != nil {
				return nil, &ErrChannelBusy{ExistingRunID: active.ID}
			}
			return nil, &ErrChannelBusy{}
		}
		return nil, fmt.Errorf("create run: %w", err)
	}
	run, _, err := r.Get(ctx, id)
	return run, err
}

// Get 返回任务及其阶段结果（按阶段顺序排列）。
func (r *PostgresRepository) Get(ctx context.Context, id int64) (*Run, []StageResult, error) {
	run, err := scanRun(r.Pool.QueryRow(ctx, `
		SELECT id, channel_id, model, depth, status, COALESCE(overall_status, ''), current_stage,
		       progress, attempt_count, worker_id, heartbeat_at, requested_by_key_hash, error,
		       created_at, started_at, finished_at
		FROM quality_check_runs WHERE id = $1
	`, id))
	if err != nil {
		return nil, nil, err
	}
	results, err := r.results(ctx, id)
	return run, results, err
}

// scanRun 扫描单行任务。
func scanRun(row rowScanner) (*Run, error) {
	var run Run
	var overall, currentStage, workerID, requesterHash, errMsg string
	if err := row.Scan(
		&run.ID, &run.ChannelID, &run.Model, &run.Depth, &run.Status, &overall, &currentStage,
		&run.Progress, &run.AttemptCount, &workerID, &run.HeartbeatAt, &requesterHash, &errMsg,
		&run.CreatedAt, &run.StartedAt, &run.FinishedAt,
	); err != nil {
		return nil, err
	}
	run.OverallStatus = OverallStatus(overall)
	run.CurrentStage = currentStage
	run.WorkerID = workerID
	run.RequestedByKeyHash = requesterHash
	run.Error = errMsg
	return &run, nil
}

// results 读取任务全部阶段结果（阶段固定顺序 + created_at 保证稳定性）。
func (r *PostgresRepository) results(ctx context.Context, runID int64) ([]StageResult, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT stage, check_name, status, http_status, latency_ms, ttfb_ms, actual_model,
		       prompt_tokens, completion_tokens, total_tokens, COALESCE(details::text, '{}'), error
		FROM quality_check_results
		WHERE run_id = $1
		ORDER BY created_at, id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("query results: %w", err)
	}
	defer rows.Close()

	var out []StageResult
	for rows.Next() {
		var sr StageResult
		var detailsJSON string
		if err := rows.Scan(&sr.Stage, &sr.CheckName, &sr.Status, &sr.HTTPStatus, &sr.LatencyMS, &sr.TTFBMS,
			&sr.ActualModel, &sr.PromptTokens, &sr.CompletionTokens, &sr.TotalTokens, &detailsJSON, &sr.Error); err != nil {
			continue
		}
		sr.Details = map[string]interface{}{}
		_ = json.Unmarshal([]byte(detailsJSON), &sr.Details)
		out = append(out, sr)
	}
	return out, rows.Err()
}

// ListByChannel 站点最近任务（limit 上限 100）。
func (r *PostgresRepository) ListByChannel(ctx context.Context, channelID, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT id, channel_id, model, depth, status, COALESCE(overall_status, ''), current_stage,
		       progress, attempt_count, worker_id, heartbeat_at, requested_by_key_hash, error,
		       created_at, started_at, finished_at
		FROM quality_check_runs
		WHERE channel_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			continue
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

// FindActiveByChannel 返回站点当前活跃任务（无则 nil）。
func (r *PostgresRepository) FindActiveByChannel(ctx context.Context, channelID int) (*Run, error) {
	run, err := scanRun(r.Pool.QueryRow(ctx, `
		SELECT id, channel_id, model, depth, status, COALESCE(overall_status, ''), current_stage,
		       progress, attempt_count, worker_id, heartbeat_at, requested_by_key_hash, error,
		       created_at, started_at, finished_at
		FROM quality_check_runs
		WHERE channel_id = $1 AND status IN ('queued', 'running', 'cancel_requested')
		ORDER BY id DESC LIMIT 1
	`, channelID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return run, err
}

// ClaimNext 领取队首任务（SKIP LOCKED，多 Worker 不重复领取）。
func (r *PostgresRepository) ClaimNext(ctx context.Context, workerID string) (*Run, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer tx.Rollback(ctx)

	var id int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM quality_check_runs
		WHERE status = 'queued'
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next: %w", err)
	}

	now := time.Now()
	if _, err := tx.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'running', attempt_count = attempt_count + 1,
		    worker_id = $2, started_at = COALESCE(started_at, $3), heartbeat_at = $3
		WHERE id = $1
	`, id, workerID, now); err != nil {
		return nil, fmt.Errorf("mark running: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	run, results, err := r.Get(ctx, id)
	_ = results
	return run, err
}

// UpsertResult 写入/更新单阶段结果。
func (r *PostgresRepository) UpsertResult(ctx context.Context, runID int64, result StageResult) error {
	detailsJSON := []byte("{}")
	if result.Details != nil {
		detailsJSON, _ = json.Marshal(result.Details)
	}
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO quality_check_results
			(run_id, stage, check_name, status, http_status, latency_ms, ttfb_ms, actual_model,
			 prompt_tokens, completion_tokens, total_tokens, details, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (run_id, stage, check_name)
		DO UPDATE SET status = EXCLUDED.status, http_status = EXCLUDED.http_status,
			latency_ms = EXCLUDED.latency_ms, ttfb_ms = EXCLUDED.ttfb_ms,
			actual_model = EXCLUDED.actual_model, prompt_tokens = EXCLUDED.prompt_tokens,
			completion_tokens = EXCLUDED.completion_tokens, total_tokens = EXCLUDED.total_tokens,
			details = EXCLUDED.details, error = EXCLUDED.error, updated_at = NOW()
	`, runID, result.Stage, result.CheckName, string(result.Status), result.HTTPStatus,
		result.LatencyMS, result.TTFBMS, result.ActualModel, result.PromptTokens,
		result.CompletionTokens, result.TotalTokens, detailsJSON, result.Error)
	if err != nil {
		return fmt.Errorf("upsert result: %w", err)
	}
	return nil
}

// SetProgress 更新阶段与进度。
func (r *PostgresRepository) SetProgress(ctx context.Context, runID int64, stage string, progress int) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs SET current_stage = $2, progress = $3 WHERE id = $1
	`, runID, stage, progress)
	return err
}

// Heartbeat 心跳（同时校验所有权，避免已回收任务继续写结果）。
func (r *PostgresRepository) Heartbeat(ctx context.Context, runID int64, workerID string) error {
	ct, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs SET heartbeat_at = NOW()
		WHERE id = $1 AND worker_id = $2 AND status = 'running'
	`, runID, workerID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("heartbeat lost ownership of run %d", runID)
	}
	return nil
}

// RequestCancel 请求取消（仅活跃任务）。
func (r *PostgresRepository) RequestCancel(ctx context.Context, runID int64) error {
	ct, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'cancel_requested'
		WHERE id = $1 AND status IN ('queued', 'running')
	`, runID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("run %d is not cancellable", runID)
	}
	return nil
}

// IsCancelRequested 查询取消标志。
func (r *PostgresRepository) IsCancelRequested(ctx context.Context, runID int64) (bool, error) {
	var status RunStatus
	if err := r.Pool.QueryRow(ctx, `
		SELECT status FROM quality_check_runs WHERE id = $1
	`, runID).Scan(&status); err != nil {
		return false, err
	}
	return status == RunCancelRequested, nil
}

// Complete 标记完成。
func (r *PostgresRepository) Complete(ctx context.Context, runID int64, overall OverallStatus) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'completed', overall_status = $2, progress = 100,
		    finished_at = NOW(), heartbeat_at = NOW()
		WHERE id = $1
	`, runID, string(overall))
	return err
}

// Fail 标记失败。
func (r *PostgresRepository) Fail(ctx context.Context, runID int64, message string) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'failed', error = $2, finished_at = NOW(), heartbeat_at = NOW()
		WHERE id = $1
	`, runID, message)
	return err
}

// Cancel 标记已取消。
func (r *PostgresRepository) Cancel(ctx context.Context, runID int64) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'cancelled', finished_at = NOW(), heartbeat_at = NOW()
		WHERE id = $1
	`, runID)
	return err
}

// RecoverStale 回收心跳超时的 running 任务：
//   - 未超重试次数 → 回 queued；
//   - 已超重试次数 → 标 expired。
// 返回回收数量。
func (r *PostgresRepository) RecoverStale(ctx context.Context, olderThan time.Time, maxAttempts int) (int64, error) {
	ct, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = CASE
		        WHEN attempt_count < $2 THEN 'queued'
		        ELSE 'expired'
		    END,
		    worker_id = '',
		    finished_at = CASE WHEN attempt_count >= $2 THEN NOW() ELSE NULL END,
		    heartbeat_at = NOW()
		WHERE status = 'running'
		  AND heartbeat_at < $1
	`, olderThan, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("recover stale: %w", err)
	}
	return ct.RowsAffected(), nil
}

// rowScanner 行扫描接口（pgx.Row / pgx.Rows 都满足）。
type rowScanner interface {
	Scan(dest ...interface{}) error
}
